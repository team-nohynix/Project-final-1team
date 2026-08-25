package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"orderapi/backpressure"
	"orderapi/idempotency"
	"orderapi/kafkaclient"
	"orderapi/order"
	"orderapi/validate"
)

var ordersTotalCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "orders_total",
		Help: "Total number of orders accepted",
	},
	[]string{"status"},
)

func init() {
	prometheus.MustRegister(ordersTotalCounter)
}

// orderRequest는 POST /v1/orders의 요청 본문입니다. SourceOrderID는 선택
// 필드(docs/api-specification.md 2.1) — replayengine이 리플레이 주문을 제출할
// 때만 원본 페이퍼 트레이딩 주문의 orderId를 실어 보내고, trader의 신규 주문은
// 아예 보내지 않습니다(주문 검증 로직에는 관여하지 않고 그대로 Kafka 메시지에
// 실어 보내 "기록기"가 TRADE_ORDER.source_order_id를 채울 수 있게 하는 용도).
type orderRequest struct {
	Market        string `json:"market"`
	Side          string `json:"side"`
	Price         string `json:"price"`
	Quantity      string `json:"quantity"`
	SourceOrderID string `json:"sourceOrderId,omitempty"`
}

// errorResponse는 docs/api-specification.md 4장의 공통 오류 응답 포맷입니다.
type errorResponse struct {
	ErrorCode string `json:"errorCode"`
	Message   string `json:"message"`
	RequestID string `json:"requestId"`
}

// cancelResponse는 DELETE /v1/orders/{orderId}의 200 응답 본문입니다.
type cancelResponse struct {
	OrderID          string `json:"orderId"`
	Status           string `json:"status"`
	CanceledQuantity string `json:"canceledQuantity"`
	CanceledAt       string `json:"canceledAt"`
}

// nowISO는 현재 시각을 docs/api-specification.md 1.2가 요구하는
// "ISO-8601 UTC, 밀리초 포함"(예: 2026-07-31T09:00:00.000Z) 형식으로 반환합니다.
// 이 API 자체 문서의 결정이라, market-data API의 KST 결정과는 별개입니다.
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// requestID는 X-Request-Id 요청 헤더를 그대로 쓰거나, 없으면 새로 발급합니다.
func requestID(r *http.Request) string {
	if id := r.Header.Get("X-Request-Id"); id != "" {
		return id
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// orderMode는 X-Order-Mode 헤더를 읽습니다(docs/erd.md의 TRADE_ORDER.mode를
// "기록기"가 채울 수 있게 하는 배관 — PAPER_TRADING은 trader가, REPLAY는
// replayengine이 보냅니다). Idempotency-Key와 달리 이 값은 처리 정합성이 아니라
// 라벨링용이라, 헤더가 없거나 알 수 없는 값이면 요청을 막지 않고 PAPER_TRADING으로
// 기본 처리하면서 경고만 남깁니다 — 그래야 지금까지의 curl/수동 테스트 워크플로가
// 그대로 동작합니다.
func orderMode(r *http.Request) string {
	switch v := r.Header.Get("X-Order-Mode"); v {
	case "PAPER_TRADING", "REPLAY":
		return v
	case "":
		return "PAPER_TRADING"
	default:
		log.Printf("알 수 없는 X-Order-Mode 값 %q, PAPER_TRADING으로 처리", v)
		return "PAPER_TRADING"
	}
}

// acceptOrderHandler는 POST /v1/orders를 처리합니다 (docs/api-specification.md 2.1, 2.2).
// checker는 NFR-13(RDS 백프레셔, 2026-08-07 — CLAUDE.md의 "RDS admission
// control via recorder consumer lag" 참고)의 읽기 쪽입니다.
func acceptOrderHandler(store *order.Store, idem *idempotency.Store, producer kafkaclient.Publisher, checker backpressure.Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			writeError(w, reqID, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key 헤더가 필요합니다.")
			return
		}

		if cached, ok := idem.Get(key); ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(cached.Status)
			w.Write(cached.Body)
			return
		}

		// 캐시된 응답 재생(위)이 아니라 새 처리를 시작하는 시점에만 체크합니다 —
		// 이미 결정된 재시도 응답은 백프레셔 상태와 무관하게 그대로 돌려줘야
		// 합니다(새로운 부하가 아니라 이미 끝난 요청의 재생일 뿐이므로).
		// idempotency 캐시에는 절대 남기지 않습니다(writeError, writeErrorAndCache
		// 아님) — idempotency.go의 "5xx는 캐시 안 함" 규칙과 같은 이유로, 이
		// 429도 일시적 상태라 같은 키로 나중에 재시도하면 성공할 수 있어야
		// 합니다.
		if active, err := checker.Active(r.Context()); err != nil {
			log.Printf("백프레셔 플래그 조회 실패, 열린 것으로 간주(fail-open): %v", err)
		} else if active {
			// 2026-08-10부터 checker가 recorder 랙/matching 자체 랙 두 플래그를 모두
			// 확인하는 MultiChecker이므로(main.go 참고), 메시지도 어느 한쪽으로
			// 특정하지 않습니다 — 클라이언트 입장에선 원인이 뭐든 "잠시 후 재시도"가
			// 동일한 대응이라 구분해서 알려줄 실익도 없습니다.
			ordersTotalCounter.WithLabelValues("rejected_backpressure").Inc()
			recordDroppedOrder()
			writeError(w, reqID, http.StatusTooManyRequests, "CONSUMER_LAG_EXCEEDED", "시스템이 처리 지연 중입니다. 잠시 후 다시 시도해주세요.")
			return
		}

		var req orderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, reqID, http.StatusBadRequest, "INVALID_REQUEST", "요청 본문 JSON 파싱 실패")
			return
		}

		if !validate.IsTargetMarket(req.Market) {
			writeErrorAndCache(w, idem, key, reqID, http.StatusBadRequest, "INVALID_MARKET", "대상 20개 마켓에 없는 마켓 코드입니다.")
			return
		}
		if code, msg, ok := validate.ValidateSide(req.Side); !ok {
			writeErrorAndCache(w, idem, key, reqID, http.StatusBadRequest, code, msg)
			return
		}
		if _, code, msg, ok := validate.ValidatePrice(req.Market, req.Price); !ok {
			writeErrorAndCache(w, idem, key, reqID, http.StatusBadRequest, code, msg)
			return
		}
		if _, code, msg, ok := validate.ValidateQuantity(req.Quantity); !ok {
			writeErrorAndCache(w, idem, key, reqID, http.StatusBadRequest, code, msg)
			return
		}

		// TODO(NFR-13): 컨슈머 랙 기준치 초과 시 429 CONSUMER_LAG_EXCEEDED로 즉시 거절.
		// matching이 자신의 컨슈머 랙을 orderapi에 알려주는 경로가 아직 없어서 미구현.

		now := time.Now()
		o := &order.Order{
			OrderID:    store.NewOrderID(now),
			Market:     req.Market,
			Side:       req.Side,
			Price:      req.Price,
			Quantity:   req.Quantity,
			Status:     order.StatusAccepted,
			AcceptedAt: nowISO(),
		}

		if err := producer.PublishNew(r.Context(), o, key, orderMode(r), req.SourceOrderID); err != nil {
			// Kafka 발행 실패는 일시적일 수 있어(브로커 재시작 등, 또는 토픽 자동 생성
			// 중인 최초 1회 등) 멱등성 캐시에 남기지 않습니다 — 같은 키로 재시도하면
			// 다시 시도할 수 있어야 합니다. 실제로 dev-kafka에서 토픽이 갓 생성될 때
			// 첫 발행이 이 경로로 실패하고 재시도가 성공하는 걸 확인함.
			log.Printf("주문 발행 실패 (market=%s, orderId=%s): %v", o.Market, o.OrderID, err)
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "주문 발행에 실패했습니다.")
			return
		}
		store.Save(o)
		ordersTotalCounter.WithLabelValues("accepted").Inc()
		log.Printf("주문 접수 완료 (market=%s, side=%s, orderId=%s)", o.Market, o.Side, o.OrderID)

		body, _ := json.Marshal(o)
		idem.Put(key, http.StatusAccepted, body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		w.Write(body)
	}
}

// cancelOrderHandler는 DELETE /v1/orders/{orderId}를 처리합니다 (docs/api-specification.md 2.3).
func cancelOrderHandler(store *order.Store, producer kafkaclient.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		orderID := r.PathValue("orderId")
		o, ok := store.Get(orderID)
		if !ok {
			writeError(w, reqID, http.StatusNotFound, "ORDER_NOT_FOUND", "해당 주문을 찾을 수 없습니다.")
			return
		}

		if o.Status == order.StatusFilled {
			writeError(w, reqID, http.StatusConflict, "ORDER_ALREADY_FILLED", "이미 전량 체결된 주문은 취소할 수 없습니다.")
			return
		}

		// 이미 취소된 주문을 다시 취소 요청하면, 새로 처리하지 않고 그때 남긴 결과를
		// 그대로 돌려줍니다(재시도에 안전하게 만들기 위함).
		if o.Status != order.StatusCanceled {
			o.Status = order.StatusCanceled
			// 2026-08-10부터 ApplyFill이 체결마다 RemainingQuantity를 깎으므로,
			// 취소 수량은 원래 주문 전량(o.Quantity)이 아니라 그 시점의 남은
			// 잔량이어야 합니다 — 부분체결 후 취소된 주문은 남은 만큼만
			// 취소된 것이지 전량이 취소된 게 아닙니다.
			o.CanceledQuantity = o.RemainingQuantity
			o.CanceledAt = nowISO()

			if err := producer.PublishCancel(r.Context(), o.OrderID, o.Market, o.CanceledAt); err != nil {
				log.Printf("취소 발행 실패 (market=%s, orderId=%s): %v", o.Market, o.OrderID, err)
				writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "취소 발행에 실패했습니다.")
				return
			}
			ordersTotalCounter.WithLabelValues("canceled").Inc()
			log.Printf("주문 취소 완료 (market=%s, orderId=%s)", o.Market, o.OrderID)
		}

		writeJSON(w, http.StatusOK, cancelResponse{
			OrderID:          o.OrderID,
			Status:           o.Status,
			CanceledQuantity: o.CanceledQuantity,
			CanceledAt:       o.CanceledAt,
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, reqID string, status int, errorCode, message string) {
	writeJSON(w, status, errorResponse{ErrorCode: errorCode, Message: message, RequestID: reqID})
}

// writeErrorAndCache는 검증 실패 응답을 클라이언트에 보내고, 같은 Idempotency-Key로
// 재요청해도 재검증 없이 같은 결과가 나오도록 캐싱합니다.
func writeErrorAndCache(w http.ResponseWriter, idem *idempotency.Store, key, reqID string, status int, errorCode, message string) {
	body, _ := json.Marshal(errorResponse{ErrorCode: errorCode, Message: message, RequestID: reqID})
	idem.Put(key, status, body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}
