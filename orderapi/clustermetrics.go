package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"
)

// GET /v1/cluster-metrics (2026-08-24, 사용자 제안) — 활성 노드 수/백엔드 전체
// 파드 수/파드 재시작 누적/매칭엔진 호가창 잔량/오토스케일링 현황을
// 그라파나 team1-overview 대시보드가 이미 쓰고 있는 PromQL을 그대로
// 모니터링 EC2의 Prometheus(infra/monitoring-ec2.tf, 포트 9090)에 물어봐서
// 돌려줍니다 — kube-state-metrics/cadvisor/매칭엔진 자체 지표를 orderapi가
// 다시 만들 필요 없이, 그라파나가 이미 검증해 쓰고 있는 값을 재사용합니다.
// 각 PromQL은 grafana team1-overview 대시보드(panel id 7/205/6/9107/9300)의
// target.expr을 그대로 옮긴 것 — 패널이 바뀌면 여기도 같이 확인해야 합니다.
//
// 경로가 "/v1/metrics/..."가 아니라 "/v1/cluster-metrics"인 이유(실측으로
// 발견) — orderapi/recorder를 같이 태우는 공유 ALB Ingress(infra/k8s/backend/
// orderapi-ingress.yaml, 있다면 확인)가 "/v1/metrics" 프리픽스를 통째로
// recorder:8082로 보내도록 이미 규칙이 잡혀 있어서(recorder가 GET
// /v1/metrics/dashboard·/v1/metrics/throughput을 그 프리픽스로 서빙하기
// 때문), orderapi가 새로 "/v1/metrics/*" 아래에 라우트를 등록해봤자 그
// Ingress 규칙에 먼저 가로채여 recorder의 404로 떨어집니다(orderapi mux
// 자체엔 라우트가 정상 등록돼 있고 파드 안에서 직접 찔러보면 200이 나오는데
// 바깥에서는 404가 나서 처음엔 원인을 못 찾았습니다) — 그래서 이 프리픽스
// 충돌이 없는 별도 경로를 씁니다.

// promMatchingMaxReplicas/promRecorderMaxReplicas는 KEDA ScaledObject의
// maxReplicaCount를 그대로 옮긴 상수입니다(infra/k8s/backend/
// matching-engine-scaledobject.yaml, recorder-scaledobject.yaml) — Prometheus가
// 아니라 정적 설정값이라 쿼리로 가져올 게 아니라 여기 그대로 둡니다.
const (
	promMatchingMaxReplicas = 10
	promRecorderMaxReplicas = 10
)

type promQuerier struct {
	baseURL string
	client  *http.Client
}

func newPromQuerier(baseURL string) *promQuerier {
	return &promQuerier{baseURL: baseURL, client: &http.Client{Timeout: 5 * time.Second}}
}

type promSample struct {
	Labels map[string]string
	Value  float64
}

func (p *promQuerier) query(ctx context.Context, expr string) ([]promSample, error) {
	u := p.baseURL + "/api/v1/query?query=" + url.QueryEscape(expr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus 요청 실패: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus 응답 코드 %d", resp.StatusCode)
	}

	var body struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus 응답 파싱 실패: %w", err)
	}
	if body.Status != "success" {
		return nil, fmt.Errorf("prometheus 쿼리 실패(status=%s)", body.Status)
	}

	out := make([]promSample, 0, len(body.Data.Result))
	for _, r := range body.Data.Result {
		var v float64
		if s, ok := r.Value[1].(string); ok {
			v, _ = strconv.ParseFloat(s, 64)
		}
		out = append(out, promSample{Labels: r.Metric, Value: v})
	}
	return out, nil
}

// queryScalar는 단일 스칼라 결과를 기대하는 쿼리용 — 결과가 없으면 0을
// 반환합니다(예: count()가 대상이 하나도 없을 때는 아예 빈 벡터를 돌려주는
// PromQL 특성 때문에, "값이 0"과 "쿼리를 못 함"을 구분해야 해서 에러는
// 그대로 반환하고 빈 결과만 0으로 다룹니다).
func (p *promQuerier) queryScalar(ctx context.Context, expr string) (float64, error) {
	samples, err := p.query(ctx, expr)
	if err != nil {
		return 0, err
	}
	if len(samples) == 0 {
		return 0, nil
	}
	return samples[0].Value, nil
}

type podRestartDTO struct {
	Pod      string `json:"pod"`
	Restarts int64  `json:"restarts"`
}

type replicaStatusDTO struct {
	Current int64 `json:"current"`
	Max     int64 `json:"max"`
}

type autoscalingStatusDTO struct {
	Matching       replicaStatusDTO `json:"matching"`
	Recorder       replicaStatusDTO `json:"recorder"`
	KarpenterNodes int64            `json:"karpenterNodes"`
}

type clusterMetricsResponse struct {
	ActiveNodes        int64                `json:"activeNodes"`
	RunningPodsBackend int64                `json:"runningPodsBackend"`
	MatchingBookSize   int64                `json:"matchingBookSize"`
	PodRestarts        []podRestartDTO      `json:"podRestarts"`
	Autoscaling        autoscalingStatusDTO `json:"autoscaling"`
	// MatchingLag/RecorderLag는 2026-08-26 추가 — 실시간 처리 흐름 다이어그램의
	// 랙 뱃지용. matching-engine-scaledobject.yaml/recorder-scaledobject.yaml이
	// KEDA 스케일링 기준으로 이미 쓰고 있는 것과 정확히 같은 PromQL을 재사용한다
	// (sum(matching_engine_lag)/sum(recorder_consumer_lag)) — 이 다이어그램을
	// 처음 이식할 때(8/24) "소스 데이터가 없어 제외"했던 두 항목 중 하나인데,
	// 이제 clusterMetricsHandler가 이미 같은 Prometheus에 붙어있으니 쿼리
	// 두 개만 추가하면 된다.
	MatchingLag int64 `json:"matchingLag"`
	RecorderLag int64 `json:"recorderLag"`
}

// clusterMetricsHandler는 위 7개 PromQL을 병렬로 날립니다 — 순차로 하면
// Prometheus 응답이 느려질 때 최악의 경우 쿼리 수 × 타임아웃(5초)까지
// 늘어질 수 있어서(대시보드 로드를 막는 요인), 하나가 느려도 나머지는
// 기다리지 않게 goroutine으로 동시에 보냅니다.
func clusterMetricsHandler(prom *promQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)
		ctx := r.Context()

		var (
			wg                                                    sync.WaitGroup
			activeNodes, runningPods, bookSize, matchingReplicas float64
			recorderReplicas, karpenterNodes                     float64
			matchingLag, recorderLag                             float64
			restartSamples                                       []promSample
			errs                                                 [9]error
		)

		run := func(i int, fn func() error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = fn()
			}()
		}

		run(0, func() (err error) {
			activeNodes, err = prom.queryScalar(ctx, `count(up{job="k8s-nodes-cadvisor"} == 1)`)
			return
		})
		run(1, func() (err error) {
			runningPods, err = prom.queryScalar(ctx, `sum(kube_pod_status_phase{exported_namespace="backend", phase="Running"})`)
			return
		})
		run(2, func() (err error) {
			bookSize, err = prom.queryScalar(ctx, `sum(matching_engine_book_size)`)
			return
		})
		run(3, func() (err error) {
			restartSamples, err = prom.query(ctx, `sum by (pod) (kube_pod_container_status_restarts_total{exported_namespace="backend"})`)
			return
		})
		run(4, func() (err error) {
			matchingReplicas, err = prom.queryScalar(ctx, `kube_deployment_status_replicas{exported_namespace="backend", deployment="matching-engine"}`)
			return
		})
		run(5, func() (err error) {
			recorderReplicas, err = prom.queryScalar(ctx, `kube_deployment_status_replicas{exported_namespace="backend", deployment="recorder"}`)
			return
		})
		run(6, func() (err error) {
			karpenterNodes, err = prom.queryScalar(ctx, `count(kube_node_spec_taint{key="workload", value="backend"})`)
			return
		})
		run(7, func() (err error) {
			matchingLag, err = prom.queryScalar(ctx, `sum(matching_engine_lag)`)
			return
		})
		run(8, func() (err error) {
			recorderLag, err = prom.queryScalar(ctx, `sum(recorder_consumer_lag)`)
			return
		})
		wg.Wait()

		for _, err := range errs {
			if err != nil {
				log.Printf("클러스터 지표 조회 실패 (Prometheus): %v", err)
				writeError(w, reqID, http.StatusBadGateway, "PROMETHEUS_UNAVAILABLE", "모니터링 지표 조회에 실패했습니다.")
				return
			}
		}

		restarts := make([]podRestartDTO, 0, len(restartSamples))
		for _, s := range restartSamples {
			if s.Value <= 0 {
				continue
			}
			restarts = append(restarts, podRestartDTO{Pod: s.Labels["pod"], Restarts: int64(s.Value)})
		}
		sort.Slice(restarts, func(i, j int) bool { return restarts[i].Restarts > restarts[j].Restarts })
		if len(restarts) > 20 {
			restarts = restarts[:20]
		}

		writeJSON(w, http.StatusOK, clusterMetricsResponse{
			ActiveNodes:        int64(activeNodes),
			RunningPodsBackend: int64(runningPods),
			MatchingBookSize:   int64(bookSize),
			PodRestarts:        restarts,
			Autoscaling: autoscalingStatusDTO{
				Matching:       replicaStatusDTO{Current: int64(matchingReplicas), Max: promMatchingMaxReplicas},
				Recorder:       replicaStatusDTO{Current: int64(recorderReplicas), Max: promRecorderMaxReplicas},
				KarpenterNodes: int64(karpenterNodes),
			},
			MatchingLag: int64(matchingLag),
			RecorderLag: int64(recorderLag),
		})
	}
}
