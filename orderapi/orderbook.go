package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"orderapi/validate"
)

// orderRestingView/orderbookDoc은 매칭 엔진(matching/snapshotstore/redis.go)이 Redis에
// 저장하는 JSON과 같은 모양입니다 — 모듈 간 타입 비공유 원칙에 따라 독립적으로 다시
// 선언합니다. 필드가 바뀌면 양쪽을 같이 맞춰야 합니다.
type orderRestingView struct {
	OrderID  string `json:"orderId"`
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
	Offset   int64  `json:"offset"`
}

type orderbookDoc struct {
	Market string             `json:"market"`
	Offset int64              `json:"offset"`
	Bids   []orderRestingView `json:"bids"`
	Asks   []orderRestingView `json:"asks"`
}

// orderbookLevel/orderbookResponse는 docs/api-specification.md §3.1의 응답 형태입니다.
type orderbookLevel struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

type orderbookResponse struct {
	Market    string           `json:"market"`
	Timestamp string           `json:"timestamp"`
	Bids      []orderbookLevel `json:"bids"`
	Asks      []orderbookLevel `json:"asks"`
}

const (
	defaultOrderbookDepth = 20
	maxOrderbookDepth     = 100
)

// orderbookHandler는 GET /v1/markets/{market}/orderbook을 처리합니다(FR-12). 매칭
// 엔진이 Redis에 남긴 스냅샷(개별 미체결 주문 목록)을 읽어 가격 레벨별로 집계해
// depth 단계까지만 반환합니다. 이 스냅샷은 비동기·주기적으로 갱신되므로(매칭
// 엔진 쪽 설계), 응답이 매칭 엔진의 그 순간 상태보다 최대 한 주기 정도 뒤처질 수
// 있습니다.
func orderbookHandler(redisClient *redis.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := requestID(r)
		w.Header().Set("X-Request-Id", reqID)

		market := r.PathValue("market")
		if !validate.IsTargetMarket(market) {
			writeError(w, reqID, http.StatusNotFound, "MARKET_NOT_FOUND", "지원하지 않는 마켓입니다.")
			return
		}

		depth := defaultOrderbookDepth
		if raw := r.URL.Query().Get("depth"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				writeError(w, reqID, http.StatusBadRequest, "INVALID_DEPTH", "depth는 1 이상의 정수여야 합니다.")
				return
			}
			depth = min(parsed, maxOrderbookDepth)
		}

		body, err := redisClient.Get(r.Context(), "orderbook:"+market).Bytes()
		if err == redis.Nil {
			// 매칭 엔진이 이 마켓에 대해 아직 스냅샷을 남기기 전(막 시작했거나 미체결
			// 주문이 하나도 없었던 경우) — 빈 호가창으로 응답합니다.
			writeJSON(w, http.StatusOK, orderbookResponse{Market: market, Timestamp: nowISO(), Bids: []orderbookLevel{}, Asks: []orderbookLevel{}})
			return
		}
		if err != nil {
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "호가창 조회에 실패했습니다.")
			return
		}

		var doc orderbookDoc
		if err := json.Unmarshal(body, &doc); err != nil {
			writeError(w, reqID, http.StatusInternalServerError, "INTERNAL_ERROR", "호가창 데이터 파싱에 실패했습니다.")
			return
		}

		writeJSON(w, http.StatusOK, orderbookResponse{
			Market:    market,
			Timestamp: nowISO(),
			Bids:      aggregateLevels(doc.Bids, depth),
			Asks:      aggregateLevels(doc.Asks, depth),
		})
	}
}

// aggregateLevels는 개별 미체결 주문 목록(매칭 엔진이 이미 가격 레벨 순서대로 저장해둠)을
// depth 단계만큼 가격별로 합산합니다. 같은 레벨의 주문들은 항상 서로 이어서 나오므로
// 문자열이 같은 연속 구간을 그대로 한 레벨로 묶으면 됩니다. 수량 합산은 float 오차를
// 피하려고 decimal로 합니다(matching/orderbook과 같은 이유).
func aggregateLevels(orders []orderRestingView, depth int) []orderbookLevel {
	type level struct {
		price string
		qty   decimal.Decimal
	}
	var levels []level
	for _, o := range orders {
		qty, err := decimal.NewFromString(o.Quantity)
		if err != nil {
			continue
		}
		if len(levels) > 0 && levels[len(levels)-1].price == o.Price {
			levels[len(levels)-1].qty = levels[len(levels)-1].qty.Add(qty)
			continue
		}
		if len(levels) >= depth {
			break
		}
		levels = append(levels, level{price: o.Price, qty: qty})
	}

	out := make([]orderbookLevel, 0, len(levels))
	for _, lv := range levels {
		out = append(out, orderbookLevel{Price: lv.price, Quantity: lv.qty.String()})
	}
	return out
}
