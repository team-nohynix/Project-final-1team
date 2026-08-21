package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"orderapi/kafkaclient"
)

// unresolvedOrder는 recorder의 GET /v1/orders/unresolved(/all) 응답 항목입니다 —
// recorder/query.UnresolvedOrder와 같은 모양(모듈 간 타입 비공유 원칙에 따라
// 독립적으로 다시 선언).
type unresolvedOrder struct {
	OrderID string `json:"orderId"`
	Market  string `json:"market"`
}

type unresolvedOrdersResponse struct {
	Orders []unresolvedOrder `json:"orders"`
}

// cleanupUnresolvedOrders는 세션 하나가 완전히 끝났을 때(그룹의 마지막
// 멤버가 반납했을 때) recorder에 남아있는 미종결 주문 전부를 취소합니다
// (2026-08-19 도입, 2026-08-20에 범위를 "이 세션 몫만"에서 "전체"로 넓힘) —
// 부하테스트를 반복하면 매칭 엔진의 인메모리 오더북에 체결 안 된 주문이
// 계속 쌓여 결국 OOMKilled까지 갔던 사고 대응입니다. "취소"를 쓰는 이유는
// 이 시스템에서 이미 "오더북에서 빼기"와 동의어이기 때문 — 새 메커니즘
// (예: 오더북 강제 리셋)을 만들 필요가 없고, recorder에도 정상적으로
// CANCELED/canceled_at이 기록되어 나중에 조회해도 데이터가 정직합니다
// (단순히 메모리에서만 지우면 RDS엔 영원히 "미체결"로 남아 통계가 왜곡됩니다).
//
// **범위를 전체로 넓힌 이유(2026-08-20)**: 처음엔 mode+[startedAt,endedAt)
// 구간으로 "방금 끝난 세션 몫만" 정리했는데, 실제로는 이걸로 부족했습니다 —
// (1) 이 자동 정리 기능이 생기기 전에 쌓인 주문들은 애초에 정리 시도 자체가
// 없었고, (2) 정리 시도가 있어도 recorder 자신이 그 시점에 밀려있거나
// 다운돼 있으면 CANCEL이 recorder의 컨슈머 랙에 그냥 쌓여있을 뿐이라, 그
// 세션 몫만 매번 다시 시도해도 과거에 못 지운 잔재는 영원히 안 없어집니다.
// orderapi/session의 세션 배타적 잠금 덕분에 활성 세션은 항상 최대 1개뿐이라
// — 세션이 끝나는 시점에 DB에 남아있는 미종결 주문은 전부 (a) 방금 끝난
// 세션 것이거나 (b) 과거에 정리 안 된 잔재뿐입니다(제3의 "다른 데서 지금
// 정상 진행 중인 것"은 있을 수 없음) — 그래서 mode/기간으로 좁히지 않고
// 매번 전체를 정리해도 정상 진행 중인 실행을 잘못 건드릴 위험이 없습니다.
// 백로그가 한 번 정리되고 나면 그 뒤로는 이 쿼리 비용도 "이 세션 몫만"과
// 별 차이 없어집니다(status 하나만 보는 조건이라 idx_trade_order_status로
// 충분히 빠름).
//
// 실패해도 세션 반납 자체(이미 클라이언트에 204로 응답 끝남)에는 영향을
// 주지 않습니다 — best-effort 백그라운드 정리이지, 세션 종료 자체를 막을
// 이유가 없는 부가 기능입니다. recorderURL이 비어있으면(config.go 참고)
// 아예 시도하지 않습니다. record는 이제 로그용(runId)으로만 씁니다 —
// StartedAt은 "이 RunRecord가 제대로 채워진 게 맞는지"를 확인하는 방어적
// 체크로만 남겨뒀습니다(조회 자체엔 더 이상 안 씀).
func cleanupUnresolvedOrders(ctx context.Context, httpClient *http.Client, recorderURL string, producer kafkaclient.Publisher, runID string, startedAt time.Time) {
	if recorderURL == "" {
		// 시작 로그(main.go)에 이미 "비활성화"로 남지만, 실제로 세션이 끝날
		// 때마다 여기서도 남겨야 "이번 실행은 정리가 왜 안 됐지"를 그
		// 세션의 로그만 보고도 바로 알 수 있습니다(2026-08-20, 이 값이
		// 빠진 걸 늦게 발견했던 사고 대응).
		log.Printf("세션 정리 건너뜀 — RECORDER_URL이 설정되어 있지 않음 (runId=%s)", runID)
		return
	}
	if startedAt.IsZero() {
		log.Printf("세션 정리 건너뜀 — startedAt 없음, RunRecord가 비정상적으로 비어있음 (runId=%s)", runID)
		return
	}

	orders, err := fetchAllUnresolvedOrders(ctx, httpClient, recorderURL)
	if err != nil {
		log.Printf("세션 정리 실패 — 미종결 주문 조회 실패 (runId=%s): %v", runID, err)
		return
	}
	if len(orders) == 0 {
		log.Printf("세션 정리 완료 — 미종결 주문 없음 (runId=%s)", runID)
		return
	}

	canceledAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	canceled := 0
	for _, o := range orders {
		if err := producer.PublishCancel(ctx, o.OrderID, o.Market, canceledAt); err != nil {
			log.Printf("세션 정리 중 취소 발행 실패 (runId=%s, orderId=%s): %v", runID, o.OrderID, err)
			continue
		}
		canceled++
	}
	log.Printf("세션 정리 완료 — 미종결 주문 %d/%d건 취소 발행 (runId=%s, 범위=전체 미종결)", canceled, len(orders), runID)
}

// cleanupAllStatus는 관리용 "미종결 주문 일괄 정리" 작업의 현재/마지막
// 상태를 담는 단일 슬롯입니다(2026-08-20) — 여러 관리자가 동시에 이 버튼을
// 누를 상황을 상정하지 않아 잡 ID 레지스트리 대신 슬롯 하나로 충분하다고
// 판단했습니다. 이미 진행 중이면 새 요청은 409로 거절합니다.
type cleanupAllStatus struct {
	Status    string `json:"status"` // "IN_PROGRESS" | "COMPLETED" | "FAILED"
	Canceled  int    `json:"canceled"`
	Total     int    `json:"total"`
	StartedAt string `json:"startedAt"`
	EndedAt   string `json:"endedAt,omitempty"`
	Message   string `json:"message,omitempty"`
}

var (
	cleanupAllMu     sync.Mutex
	cleanupAllLatest cleanupAllStatus
)

// startCleanupAllUnresolvedOrdersHandler는 POST /v1/admin/cleanup-unresolved-orders를
// 처리합니다 — 프론트/운영자의 수동 "미종결 주문 일괄 정리" 트리거
// (2026-08-20, 자동 정리 기능이 생기기 전에 쌓인 대량 백로그를 한 번에
// 정리하기 위한 용도). **2026-08-20에 동기에서 비동기로 바뀌었습니다** —
// 처음엔 요청 안에서 전부 처리하고 취소 건수를 바로 응답했는데, 실제로
// 186만 건 규모의 백로그를 만나면서 5분짜리 HTTP 클라이언트 타임아웃을
// 절대 못 맞추는 게 확인됐습니다(POST /v1/collect가 똑같은 이유로 동기에서
// 비동기+폴링으로 바뀐 것과 같은 문제, CLAUDE.md 참고). 이제 즉시 202를
// 반환하고, 실제 조회+취소 발행은 백그라운드 고루틴에서 진행합니다 —
// 진행 상황은 GET .../status로 폴링합니다.
func startCleanupAllUnresolvedOrdersHandler(httpClient *http.Client, recorderURL string, producer kafkaclient.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		if recorderURL == "" {
			writeError(w, reqID, http.StatusServiceUnavailable, "RECORDER_URL_NOT_CONFIGURED", "RECORDER_URL이 설정되어 있지 않습니다.")
			return
		}

		cleanupAllMu.Lock()
		if cleanupAllLatest.Status == "IN_PROGRESS" {
			cleanupAllMu.Unlock()
			writeError(w, reqID, http.StatusConflict, "CLEANUP_ALREADY_IN_PROGRESS", "이미 일괄 정리가 진행 중입니다 — GET .../status로 진행 상황을 확인하세요.")
			return
		}
		cleanupAllLatest = cleanupAllStatus{Status: "IN_PROGRESS", StartedAt: time.Now().UTC().Format(time.RFC3339)}
		snapshot := cleanupAllLatest
		cleanupAllMu.Unlock()

		// 요청이 끝나도(이미 202로 응답함) 계속 진행돼야 하므로 r.Context()가
		// 아니라 별도 컨텍스트를 씁니다 — cleanupUnresolvedOrders(세션 종료
		// 자동 정리)의 fire-and-forget 고루틴과 같은 이유.
		go runCleanupAllUnresolvedOrders(context.Background(), httpClient, recorderURL, producer)

		writeJSON(w, http.StatusAccepted, snapshot)
	}
}

// cleanupAllStatusHandler는 GET /v1/admin/cleanup-unresolved-orders/status를
// 처리합니다 — 위 비동기 작업의 진행 상황 폴링용. 한 번도 실행된 적 없으면
// 404입니다.
func cleanupAllStatusHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		cleanupAllMu.Lock()
		snapshot := cleanupAllLatest
		cleanupAllMu.Unlock()

		if snapshot.Status == "" {
			writeError(w, reqID, http.StatusNotFound, "NO_CLEANUP_YET", "아직 일괄 정리를 실행한 적이 없습니다.")
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}

// runCleanupAllUnresolvedOrders는 startCleanupAllUnresolvedOrdersHandler가
// 백그라운드로 띄우는 실제 작업입니다 — cleanupAllLatest를 진행 상황에 맞춰
// 갱신합니다. 1000건마다 Canceled를 갱신하는 이유는 대량(수십만~수백만 건)
// 처리 중에도 폴링하는 쪽이 "멈춘 건지 진행 중인 건지"를 알 수 있게
// 하기 위함이지, 그 이상 촘촘하게 잠금을 걸 이유는 없습니다.
func runCleanupAllUnresolvedOrders(ctx context.Context, httpClient *http.Client, recorderURL string, producer kafkaclient.Publisher) {
	orders, err := fetchAllUnresolvedOrders(ctx, httpClient, recorderURL)
	if err != nil {
		log.Printf("일괄 정리 실패 — 전체 미종결 주문 조회 실패: %v", err)
		cleanupAllMu.Lock()
		cleanupAllLatest.Status = "FAILED"
		cleanupAllLatest.EndedAt = time.Now().UTC().Format(time.RFC3339)
		cleanupAllLatest.Message = err.Error()
		cleanupAllMu.Unlock()
		return
	}

	cleanupAllMu.Lock()
	cleanupAllLatest.Total = len(orders)
	cleanupAllMu.Unlock()

	canceledAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	canceled := 0
	for _, o := range orders {
		if err := producer.PublishCancel(ctx, o.OrderID, o.Market, canceledAt); err != nil {
			log.Printf("일괄 정리 중 취소 발행 실패 (orderId=%s): %v", o.OrderID, err)
			continue
		}
		canceled++
		if canceled%1000 == 0 {
			cleanupAllMu.Lock()
			cleanupAllLatest.Canceled = canceled
			cleanupAllMu.Unlock()
		}
	}

	log.Printf("미종결 주문 일괄 정리 완료 — %d/%d건 취소 발행", canceled, len(orders))
	cleanupAllMu.Lock()
	cleanupAllLatest.Status = "COMPLETED"
	cleanupAllLatest.Canceled = canceled
	cleanupAllLatest.EndedAt = time.Now().UTC().Format(time.RFC3339)
	cleanupAllMu.Unlock()
}

// fetchAllUnresolvedOrders는 recorder의 GET /v1/orders/unresolved/all을
// 부릅니다 — mode/기간 제한 없이 지금 이 순간 미종결(ACCEPTED/PARTIALLY_FILLED)
// 상태인 주문 전부를 돌려받습니다. cleanupUnresolvedOrders(자동)와
// runCleanupAllUnresolvedOrders(수동) 둘 다 이걸 씁니다 — 2026-08-20부터
// 둘의 "무엇을 정리할지" 로직 자체가 동일해졌기 때문(차이는 트리거 시점과
// 응답 방식뿐).
func fetchAllUnresolvedOrders(ctx context.Context, httpClient *http.Client, recorderURL string) ([]unresolvedOrder, error) {
	u := recorderURL + "/v1/orders/unresolved/all"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("recorder가 %d를 반환함", resp.StatusCode)
	}
	var body unresolvedOrdersResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("응답 파싱 실패: %w", err)
	}
	return body.Orders, nil
}
