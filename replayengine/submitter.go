package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"replayengine/orderstore"
)

// NewHTTPClient는 마켓별 고루틴들이 공유할 단일 HTTP 클라이언트를 만듭니다.
//
// MaxIdleConnsPerHost를 기본값(2)보다 크게 잡은 이유: 최대 20개 마켓 고루틴이 이
// 클라이언트 하나로 동시에 orderapi를 두드립니다. trader/client/client.go에서 이
// 값을 기본값으로 두고 실제로 재생을 돌렸을 때, 동시 요청이 몰릴 때마다 커넥션을
// 계속 새로 열고 닫다가 Windows 아웃바운드 임시 포트가 고갈되어 이후 모든 요청이
// 로컬에서부터 막히는 걸 겪었습니다 — 그 교훈을 여기서는 처음부터 반영합니다.
func NewHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 50

	return &http.Client{Timeout: 30 * time.Second, Transport: transport}
}

// orderRequest는 orderapi/server.go의 orderRequest와 필드가 정확히 대응됩니다.
// SourceOrderID는 선택 필드(sourceOrderId) — trader가 FR-17 기록 파일에 남긴
// 원본 주문 번호를 그대로 실어 보내면, orderapi가 이를 Kafka NEW 이벤트에 태워
// 기록기가 TRADE_ORDER.source_order_id(docs/erd.md)를 채울 수 있습니다. 오래된
// 기록 파일(OrderID 필드가 없던 시절)을 재생할 땐 빈 문자열이라 omitempty로
// 아예 필드를 안 보냅니다.
type orderRequest struct {
	Market        string `json:"market"`
	Side          string `json:"side"`
	Price         string `json:"price"`
	Quantity      string `json:"quantity"`
	SourceOrderID string `json:"sourceOrderId,omitempty"`
}

// HTTPOrderSubmitter는 기록된 주문을 그대로 orderapi에 다시 접수시킵니다(FR-18 —
// 판단 로직 재실행 없이 그대로 재생). trader/order/http_submitter.go와 동일한
// 이유로 매 호출마다 새 Idempotency-Key를 씁니다(재시도 로직이 없어 호출 1회 =
// 신규 제출 시도 1회).
type HTTPOrderSubmitter struct {
	Client  *http.Client
	BaseURL string
}

func (s HTTPOrderSubmitter) Submit(ctx context.Context, market string, o orderstore.RecordedOrder) error {
	body, err := json.Marshal(orderRequest{
		Market:        market,
		Side:          o.Side,
		Price:         o.Price,
		Quantity:      o.Quantity,
		SourceOrderID: o.OrderID,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/v1/orders", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", newIdempotencyKey())
	req.Header.Set("X-Order-Mode", "REPLAY")

	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 응답 바디를 끝까지 읽어야(성공이든 실패든) Go의 Transport가 이 커넥션을 재사용할
	// 수 있습니다 — 다 안 읽고 Close만 하면 커넥션을 재사용 못 하고 매번 새로 열게
	// 되는데, 마켓 20개가 동시에 몰릴 때 이것 때문에 Windows 아웃바운드 포트가
	// 고갈되는 걸 실제로 겪었습니다(풀 크기를 키우는 것만으로는 해결이 안 됐음 —
	// 근본 원인은 재사용 자체가 안 되고 있었던 것).
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("주문 접수 실패 (status=%d): %s", resp.StatusCode, respBody)
	}
	return nil
}

func newIdempotencyKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("idem-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
