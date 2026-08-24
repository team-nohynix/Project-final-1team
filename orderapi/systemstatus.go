package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	kafka "github.com/segmentio/kafka-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
)

// systemStatusCheckTimeout은 구성요소 하나를 확인하는 데 쓰는 상한입니다 —
// DashboardView가 이 응답을 기다리는 동안 사용자를 붙잡아두지 않도록 짧게
// 잡았습니다(짧은 타임아웃=장애로 간주, 느린 것도 결국 사용자 입장에선
// 장애와 다르지 않음).
const systemStatusCheckTimeout = 3 * time.Second

// matchingEngineMetricsURL은 matching-engine-metrics Service(같은 backend
// 네임스페이스, infra/k8s/backend/matching-engine-deployment.yaml 참고)를
// 클러스터 내부 DNS로 직접 부릅니다 — Ingress를 거치지 않는 파드↔파드 호출이라
// matchingEngineNamespace 상수와 별개로 고정 문자열로 둡니다(다른 네임스페이스로
// 옮기지 않는 한 안 바뀜).
const matchingEngineMetricsURL = "http://matching-engine-metrics:9090/metrics"

// componentStatus는 DashboardView "시스템 구성요소 상태" 패널 한 줄입니다.
type componentStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // "up" | "down"
}

// podCounts는 DashboardView "실시간 처리 흐름" 패널이 각 노드 아래 "레플리카
// N개"를 표시하는 데 씁니다 — Ready는 지금 실제로 트래픽을 받을 준비가 된
// 파드 수, Desired는 Deployment가 지금 맞추려는 목표 수(KEDA가 오토스케일링
// 중이면 그 시점의 목표치)입니다.
type podCounts struct {
	Ready   int32 `json:"ready"`
	Desired int32 `json:"desired"`
}

// podDeployments는 podCounts를 채울 때 조회할 Deployment 이름 목록입니다 —
// infra/k8s/backend/*-deployment.yaml의 metadata.name과 반드시 같아야 합니다.
var podDeployments = []string{"orderapi", "matching-engine", "recorder"}

// systemStatusHandler는 GET /v1/system-status를 처리합니다 — 프론트
// DashboardView의 "시스템 구성요소 상태" + "실시간 처리 흐름" 패널(주문 접수
// API/Kafka 브로커/매칭 엔진/MySQL/Redis 캐시 상태, 각 구성요소 파드 수)을
// 지원합니다. 각 구성요소를 얕게(연결/응답 가능 여부만) 확인합니다 — 이
// 엔드포인트 자체가 상태 대시보드용이라, 확인 로직이 무거워지면 그 자체가
// 또 다른 부하 원인이 되므로 일부러 얕게 유지합니다.
//
// deployments는 nil일 수 있습니다(main.go 참고, K8s in-cluster 접근이 안
// 되는 로컬 개발 환경) — 그 경우 pods는 빈 맵으로 응답합니다(프론트는 그
// 노드의 레플리카 수를 그냥 "--"로 표시하면 됩니다).
func systemStatusHandler(redisClient *redis.Client, kafkaDialer *kafka.Dialer, kafkaBroker, ordersTopic, recorderURL string, recorderHTTPClient *http.Client, deployments appsv1.DeploymentInterface) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		components := []componentStatus{
			// 이 핸들러가 실행되고 있다는 것 자체가 주문 접수 API(이 프로세스)가
			// 살아있다는 증거입니다 — 별도 확인이 필요 없습니다.
			{Name: "주문 접수 API", Status: "up"},
			{Name: "Kafka 브로커", Status: boolStatus(checkKafka(ctx, kafkaDialer, kafkaBroker, ordersTopic))},
			{Name: "매칭 엔진", Status: boolStatus(checkHTTPReachable(ctx, matchingEngineMetricsURL))},
			{Name: "MySQL", Status: boolStatus(checkRecorderHealth(ctx, recorderURL, recorderHTTPClient))},
			{Name: "Redis 캐시", Status: boolStatus(redisClient.Ping(ctx).Err() == nil)},
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"components": components,
			"pods":       fetchPodCounts(ctx, deployments),
		})
	}
}

func fetchPodCounts(ctx context.Context, deployments appsv1.DeploymentInterface) map[string]podCounts {
	out := make(map[string]podCounts, len(podDeployments))
	if deployments == nil {
		return out
	}
	for _, name := range podDeployments {
		dep, err := deployments.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			// 조회 실패한 배포는 응답에서 그냥 빠짐(나머지는 정상 표시) —
			// 다만 로그는 남긴다. RBAC resourceNames 범위(infra/k8s/backend/
			// orderapi-matching-restart-rbac.yaml)를 안 넓히고 podDeployments에
			// 이름만 추가하면 매번 여기서 Forbidden으로 조용히 빠지는데,
			// 로그가 없으면 "pods가 왜 비어있지"를 원인 추적하기 어렵다
			// (2026-08-24 실측 — orderapi/recorder를 RBAC에 안 넣고 배포해서
			// matching-engine만 나오던 걸 이 로그 없이 알아챔).
			log.Printf("시스템 상태 — 파드 수 조회 실패 (deployment=%s): %v", name, err)
			continue
		}
		out[name] = podCounts{Ready: dep.Status.ReadyReplicas, Desired: dep.Status.Replicas}
	}
	return out
}

func boolStatus(ok bool) string {
	if ok {
		return "up"
	}
	return "down"
}

func checkKafka(ctx context.Context, dialer *kafka.Dialer, broker, topic string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, systemStatusCheckTimeout)
	defer cancel()
	conn, err := dialLeader(checkCtx, dialer, broker, topic, 0)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func checkHTTPReachable(ctx context.Context, url string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, systemStatusCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// checkRecorderHealth는 recorder의 GET /v1/health(server.go)를 부릅니다 —
// RECORDER_URL이 안 채워진 환경(로컬 개발 등)에서는 recorder 연동 자체가
// 꺼져 있는 게 정상이라 무조건 down 대신 그 사실 그대로 down을 반환합니다
// (다른 recorder 의존 기능들과 같은 "선택적 기능" 취급).
func checkRecorderHealth(ctx context.Context, recorderURL string, client *http.Client) bool {
	if recorderURL == "" {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, systemStatusCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, recorderURL+"/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
