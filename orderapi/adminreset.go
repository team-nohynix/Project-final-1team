package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"
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
// 두 단계로 해결합니다:
//  1. Redis의 전체 스냅샷(orderbook:<market>) 키만 지웁니다 — 워터마크
//     (orderbook:<market>:watermark, matching/engine의 SaveWatermark/
//     LoadWatermark)는 반드시 남겨둡니다. 전체 스냅샷이 없어도 워터마크가
//     있으면 Engine.Recover()가 거기서부터 이어 읽어서, offset 0 전체 재생
//     사고를 안 냅니다 — 다만 그 시점 호가창 상태 자체는(스냅샷이 없으니)
//     빈 채로 다시 시작합니다. 이게 바로 우리가 원하는 "잔량 제거"입니다.
//  2. matching-engine Deployment에 rollout restart를 걸어서, 지금 각 파드가
//     메모리에 들고 있는 잔량도 실제로 비웁니다 — Redis만 지우면 다음
//     자연스러운 재시작/리밸런스 전까지는 살아있는 파드의 인메모리 상태가
//     그대로입니다(마켓 배정이 파드마다 나뉘어 있어, 이 API를 호출한 파드
//     하나만으로는 다른 파드의 인메모리 상태를 지울 방법이 없습니다).
func resetMatchingEngineBookHandler(redisClient *redis.Client, deployments appsv1.DeploymentInterface, deploymentName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

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

		log.Printf("매칭엔진 호가창 잔량 초기화 완료 — 스냅샷 %d개 삭제, 매칭엔진 rollout restart 트리거함", deleted)
		writeJSON(w, http.StatusOK, map[string]any{
			"deletedSnapshots": deleted,
			"restartTriggered": true,
		})
	}
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
