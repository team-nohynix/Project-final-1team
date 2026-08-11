package order

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrTooManyRequests는 orderapi가 429(RDS 백프레셔, CLAUDE.md의 "RDS admission
// control via recorder consumer lag" 참고)로 거절했음을 나타냅니다.
// RetryingSubmitter가 이 에러인지 확인해서, 이것만 재시도합니다 — 검증 실패
// (400) 등 다른 에러는 재시도해도 똑같이 실패할 가능성이 높거나 원인이 다르므로
// 재시도 대상이 아닙니다.
var ErrTooManyRequests = errors.New("주문 접수 API가 429(백프레셔)로 거절함")

// HTTPOrderSubmitter는 생성된 주문을 실제 주문 접수 API(orderapi, POST /v1/orders)로 보냅니다.
type HTTPOrderSubmitter struct {
	Client  *http.Client
	BaseURL string
}

// orderRequest는 orderapi/server.go의 orderRequest와 필드가 정확히 대응됩니다.
type orderRequest struct {
	Market   string `json:"market"`
	Side     string `json:"side"`
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

// orderResponse는 orderapi가 202 응답으로 돌려주는 바디 중 여기서 필요한
// 필드만 뽑아 파싱합니다(order.Order 전체와 같은 모양, orderId만 실제로 씀).
type orderResponse struct {
	OrderID string `json:"orderId"`
}

// Submit은 o를 orderapi에 신규 주문으로 접수하고, orderapi가 발급한 orderId를
// 반환합니다(RecordingSubmitter가 FR-17 기록에 남기기 위해 필요). Idempotency-Key는
// o.IdempotencyKey(생성 시점에 한 번 정해짐, order.go 참고)를 그대로 씁니다 —
// RetryingSubmitter가 같은 o로 여러 번 재시도해도 항상 같은 키가 나가야 하기
// 때문입니다.
func (s HTTPOrderSubmitter) Submit(ctx context.Context, o Order) (string, error) {
	body, err := json.Marshal(orderRequest{
		Market:   o.Market,
		Side:     o.Side,
		Price:    o.Price,
		Quantity: o.Quantity,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/v1/orders", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", o.IdempotencyKey)
	req.Header.Set("X-Order-Mode", "PAPER_TRADING")

	resp, err := s.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	// 응답 바디를 끝까지 읽어야(성공이든 실패든) Go의 Transport가 이 커넥션을 재사용할
	// 수 있습니다 — 다 안 읽고 Close만 하면 커넥션을 재사용 못 하고 매번 새로 열게
	// 되는데, replayengine에서 이것 때문에 MaxIdleConnsPerHost를 키워도 Windows
	// 아웃바운드 포트가 고갈되는 걸 실제로 겪었습니다(풀 크기만으로는 해결이 안 됐음 —
	// 근본 원인은 재사용 자체가 안 되고 있었던 것이라, 여기도 같은 문제가 있었습니다).
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", fmt.Errorf("%w: %s", ErrTooManyRequests, respBody)
	}
	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("주문 접수 실패 (status=%d): %s", resp.StatusCode, respBody)
	}

	var parsed orderResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("주문 접수 응답 파싱 실패: %w", err)
	}
	return parsed.OrderID, nil
}

// newIdempotencyKey는 orderapi/server.go의 requestID()와 같은 방식(crypto/rand -> hex)으로
// 새 키를 만듭니다. order.go의 NewOrder가 Order 생성 시점에 한 번만 호출합니다
// (예전엔 여기서 Submit마다 호출했지만, 재시도가 도입되면서 같은 논리적 주문의
// 재시도가 매번 다른 키를 쓰게 되는 걸 막기 위해 옮겼습니다).
func newIdempotencyKey() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("idem-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
