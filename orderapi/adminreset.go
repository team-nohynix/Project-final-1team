package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	kafka "github.com/segmentio/kafka-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"orderapi/validate"
)

// resetMatchingEngineBookHandler는 POST /v1/admin/reset-matching-engine-book를
// 처리합니다 — 프론트 "매칭엔진 호가창 잔량 지우기" 버튼(2026-08-21)용입니다.
//
// 이건 DB 정리(cleanupAllUnresolvedOrdersHandler)와는 다른 문제를 겨냥합니다 —
// 매칭엔진이 크래시 중이라 CANCEL을 못 받았거나, 받았어도 비동기 스냅샷 저장
// 큐가 가득 차 저장에 실패하면, DB는 이미 정리됐어도 매칭엔진 자신의 Redis
// 전체 스냅샷(orderbook:<market>)과 각 파드의 인메모리 호가창에는 미체결
// 주문이 그대로 남습니다(2026-08-21 새벽 실측: DB는 거의 비었는데 이 값만
// 186만 건). DB 정리 버튼은 이 상태를 못 건드립니다.
//
// 기본 동작(force 없음) — 두 단계:
//  1. Redis의 전체 스냅샷(orderbook:<market>) 키만 지웁니다 — 워터마크
//     (orderbook:<market>:watermark, matching/engine의 SaveWatermark/
//     LoadWatermark)는 반드시 남겨둡니다. 전체 스냅샷이 없어도 워터마크가
//     있으면 Engine.Recover()가 거기서부터 이어 읽어서, offset 0 전체 재생
//     사고를 안 냅니다 — 다만 그 시점 호가창 상태 자체는(스냅샷이 없으니)
//     빈 채로 다시 시작합니다.
//  2. matching-engine Deployment에 rollout restart를 걸어서, 지금 각 파드가
//     메모리에 들고 있는 잔량도 실제로 비웁니다.
//
// **?force=true — 워터마크가 없는 마켓도(오늘 밤 KRW-WLD/AI/ONDO처럼) 즉시
// 비우고 싶을 때** 씁니다(2026-08-21, "왜 한번에 다 안지워져?" 질문 대응).
// 기본 동작은 워터마크가 없는 마켓은 안전하게 offset 0부터 재생하는 것 말고
// 선택지가 없습니다 — 실제로 그 시점에 뭐가 미체결이었는지 아는 유일한 방법이
// orders 토픽을 읽는 것뿐이기 때문입니다. force는 이 안전장치를 의도적으로
// 건너뜁니다 — 모든 마켓의 워터마크를 "지금 이 순간 orders 토픽의 최신
// 오프셋"으로 강제로 세팅해서, 과거를 전혀 안 읽고 완전히 빈 호가창으로
// 즉시 시작하게 합니다. 트레이드오프: 그 순간 실제로 미체결이었던 주문들을
// 매칭엔진이 통째로 잊어버립니다(DB엔 여전히 ACCEPTED/PARTIALLY_FILLED로
// 남음). 실거래면 절대 안 될 일이지만, 이 프로젝트는 테스트/페이퍼 트레이딩
// 환경이라 사용자가 "완전 초기화"를 명시적으로 원할 때만 쓰는 파괴적 옵션으로
// 둡니다.
func resetMatchingEngineBookHandler(redisClient *redis.Client, deployments appsv1.DeploymentInterface, deploymentName string, kafkaDialer *kafka.Dialer, kafkaBroker, ordersTopic string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		force := r.URL.Query().Get("force") == "true"

		var watermarksForced int
		if force {
			n, err := forceResetWatermarksToLatest(r.Context(), redisClient, kafkaDialer, kafkaBroker, ordersTopic)
			if err != nil {
				log.Printf("매칭엔진 호가창 강제 초기화 실패 — 워터마크 강제 설정 실패: %v", err)
				writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "워터마크 강제 설정에 실패했습니다.")
				return
			}
			watermarksForced = n
		}

		deleted, err := deleteOrderbookSnapshots(r.Context(), redisClient)
		if err != nil {
			log.Printf("매칭엔진 호가창 잔량 초기화 실패 — 스냅샷 삭제 실패: %v", err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "스냅샷 삭제에 실패했습니다.")
			return
		}

		if err := triggerRolloutRestart(r.Context(), deployments, deploymentName); err != nil {
			log.Printf("매칭엔진 호가창 잔량 초기화 — 스냅샷 %d개 삭제는 성공, 재시작 트리거 실패: %v", deleted, err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "스냅샷은 지웠지만 매칭엔진 재시작에 실패했습니다 — 다음 자연 재시작 전까지 이미 떠 있는 파드의 메모리 상태는 그대로입니다.")
			return
		}

		log.Printf("매칭엔진 호가창 잔량 초기화 완료 — 스냅샷 %d개 삭제, force=%v(워터마크 %d개 강제 설정), 매칭엔진 rollout restart 트리거함", deleted, force, watermarksForced)
		writeJSON(w, http.StatusOK, map[string]any{
			"deletedSnapshots": deleted,
			"forced":           force,
			"watermarksForced": watermarksForced,
			"restartTriggered": true,
		})
	}
}

// forceResetWatermarksToLatest는 validate.TargetMarkets의 모든 마켓(=orders
// 토픽의 모든 파티션)에 대해 "지금 이 순간의 최신 오프셋"을 워터마크로 강제
// 기록합니다. Engine.Recover()는 워터마크+1부터 읽으므로(engine.go), 다음에
// 쓰일 오프셋(ReadLastOffset)을 그대로 resumeFrom으로 쓰려면 워터마크 값은
// ReadLastOffset()-1이어야 합니다.
func forceResetWatermarksToLatest(ctx context.Context, redisClient *redis.Client, dialer *kafka.Dialer, broker, topic string) (int, error) {
	n := 0
	for partition, market := range validate.TargetMarkets {
		conn, err := dialLeader(ctx, dialer, broker, topic, partition)
		if err != nil {
			return n, fmt.Errorf("파티션 연결 실패 (market=%s, partition=%d): %w", market, partition, err)
		}
		lastOffset, err := conn.ReadLastOffset()
		closeErr := conn.Close()
		if err != nil {
			return n, fmt.Errorf("최신 오프셋 조회 실패 (market=%s): %w", market, err)
		}
		if closeErr != nil {
			log.Printf("파티션 연결 종료 실패 (market=%s, 무시하고 진행): %v", market, closeErr)
		}
		watermark := lastOffset - 1
		if err := redisClient.Set(ctx, "orderbook:"+market+":watermark", watermark, 0).Err(); err != nil {
			return n, fmt.Errorf("워터마크 강제 설정 실패 (market=%s): %w", market, err)
		}
		n++
	}
	return n, nil
}

// dialLeader는 kafkaDialer가 nil이어도(useIAM=false, 로컬 dev-kafka) 동작하도록
// 감쌉니다 — kafka.Dialer의 메서드 대부분은 nil 리시버를 지원하지 않으므로,
// nil이면 인증 없는 기본 연결을 쓰는 패키지 레벨 kafka.DialLeader로 대신합니다
// (kafkaclient.NewDialer/newTransport의 nil-이면-기본값 관례와 동일).
func dialLeader(ctx context.Context, dialer *kafka.Dialer, broker, topic string, partition int) (*kafka.Conn, error) {
	if dialer == nil {
		return kafka.DialLeader(ctx, "tcp", broker, topic, partition)
	}
	return dialer.DialLeader(ctx, "tcp", broker, topic, partition)
}

// deleteOrderbookSnapshots는 orderbook:<market> 키(전체 스냅샷)만 지웁니다 —
// orderbook:<market>:watermark(워터마크)는 SCAN 결과에서 걸러내 남깁니다.
// 20개 안팎의 마켓뿐이라 SCAN을 배치 없이 한 번에 처리해도 충분합니다.
func deleteOrderbookSnapshots(ctx context.Context, client *redis.Client) (int, error) {
	var toDelete []string
	iter := client.Scan(ctx, 0, "orderbook:*", 0).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		if len(key) >= len(":watermark") && key[len(key)-len(":watermark"):] == ":watermark" {
			continue
		}
		toDelete = append(toDelete, key)
	}
	if err := iter.Err(); err != nil {
		return 0, err
	}
	if len(toDelete) == 0 {
		return 0, nil
	}
	if err := client.Del(ctx, toDelete...).Err(); err != nil {
		return 0, err
	}
	return len(toDelete), nil
}

// triggerRolloutRestart는 `kubectl rollout restart`와 정확히 같은 방식으로
// 동작합니다 — 파드 템플릿의 restartedAt 어노테이션을 지금 시각으로 갱신하는
// 전략적 병합 패치를 걸면, 디플로이먼트 컨트롤러가 그걸 스펙 변경으로 보고
// 롤링 재시작합니다(이미지/설정은 안 바뀌므로 순수 재시작).
func triggerRolloutRestart(ctx context.Context, deployments appsv1.DeploymentInterface, name string) error {
	patch := []byte(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"` + time.Now().UTC().Format(time.RFC3339) + `"}}}}}`)
	_, err := deployments.Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	return err
}
