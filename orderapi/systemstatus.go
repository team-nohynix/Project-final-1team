package main

import (
	"context"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	kafka "github.com/segmentio/kafka-go"
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

// systemStatusHandler는 GET /v1/system-status를 처리합니다 — 프론트
// DashboardView의 "시스템 구성요소 상태" 패널(주문 접수 API/Kafka 브로커/매칭
// 엔진/MySQL/Redis 캐시)을 지원합니다. 각 구성요소를 얕게(연결/응답 가능
// 여부만) 확인합니다 — 이 엔드포인트 자체가 상태 대시보드용이라, 확인 로직이
// 무거워지면 그 자체가 또 다른 부하 원인이 되므로 일부러 얕게 유지합니다.
func systemStatusHandler(redisClient *redis.Client, kafkaDialer *kafka.Dialer, kafkaBroker, ordersTopic, recorderURL string, recorderHTTPClient *http.Client) http.HandlerFunc {
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

		writeJSON(w, http.StatusOK, map[string][]componentStatus{"components": components})
	}
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
