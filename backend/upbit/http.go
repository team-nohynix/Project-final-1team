package upbit

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// requestInterval은 업비트 API 요청 사이 최소 간격입니다 (초당 약 10회 제한 대응).
const requestInterval = 110 * time.Millisecond

const max429Retries = 5

// throttleMu/lastRequestAt은 requestInterval을 프로세스 전체에서 공유합니다.
// 이전엔 doRequest 호출부마다(고루틴마다) 독립적으로 time.Sleep(requestInterval)만
// 했는데, 이건 "이 고루틴은 초당 9회로 제한"일 뿐 "프로세스 전체가 업비트에
// 초당 9회로 제한"이 아니었습니다 — 온디맨드 수집이 마켓 하나당 고루틴 하나(=
// collectMarket 호출 하나)라, 20개 마켓이 동시에 수집되면(2026-08-25 홈서버
// 첫 콜드캐시 20마켓 동시 수집 때 실측) 합산 요청률이 20배로 뛰어 업비트
// 429가 연쇄로 터지고 max429Retries마저 소진돼 500으로 이어졌습니다. S3/로컬
// market-data 캐시가 쌓여 있으면 온디맨드 수집 자체가 거의 발생하지 않아 이
// 경합이 드러나지 않습니다 — 캐시가 비어있는 상태(신규 배포 직후 등)에서
// 여러 마켓이 동시에 처음 요청될 때 재현됩니다.
var (
	throttleMu    sync.Mutex
	lastRequestAt time.Time
)

// throttle은 마지막 업비트 요청 이후 requestInterval이 지날 때까지 대기합니다 —
// 여러 고루틴이 동시에 불러도 뮤텍스로 직렬화되므로 실제 요청 간격은 프로세스
// 전체 기준 requestInterval 이상으로 보장됩니다.
func throttle() {
	throttleMu.Lock()
	defer throttleMu.Unlock()
	if wait := requestInterval - time.Since(lastRequestAt); wait > 0 {
		time.Sleep(wait)
	}
	lastRequestAt = time.Now()
}

// doRequest는 업비트 API 요청을 실행합니다. 모든 요청(단일/페이지네이션 무관) 사이에
// requestInterval만큼 간격을 두고(프로세스 전체 공유, throttle 참고), 429(Too Many
// Requests) 응답을 받으면 점점 늘어나는 대기 시간을 두고 재시도합니다.
func doRequest(req *http.Request) (*http.Response, error) {
	backoff := 500 * time.Millisecond

	for attempt := 0; ; attempt++ {
		throttle()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}

		resp.Body.Close()
		if attempt >= max429Retries {
			return nil, fmt.Errorf("업비트 API 요청 제한(429) 재시도 초과: %s", req.URL.String())
		}
		time.Sleep(backoff)
		backoff *= 2
	}
}
