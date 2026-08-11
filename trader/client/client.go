package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// NewHTTPClient는 모든 마켓 고루틴이 공유할 단일 HTTP 클라이언트를 만듭니다.
// 타임아웃을 넉넉히 잡은 이유: 캐시 미스 시 backend가 온디맨드 수집(ensureMarketCollected)을
// 마친 뒤 응답하므로, 마켓당 정상적으로 수십 초가 걸릴 수 있습니다.
//
// MaxIdleConnsPerHost를 기본값(2)보다 훨씬 크게 잡은 이유: 마켓당 최대 3개 알고리즘
// 봇(60개) + 전체 조망형 봇(2개) 등 최대 62개 고루틴이 이 클라이언트 하나(backend/orderapi
// 각각 한 호스트)를 동시에 씁니다. 기본값 2로는 동시 요청이 몰릴 때마다 커넥션을 계속
// 새로 열고 닫게 되는데, 실제로 이렇게 겪어보니(속도를 크게 올려 재생했을 때) Windows에서
// 아웃바운드 임시 포트가 순식간에 고갈되어 "Only one usage of each socket address" 에러로
// 이후 모든 요청이 로컬에서부터 막혀버렸습니다 — 커넥션을 재사용하도록 풀을 넉넉히 잡아야
// 합니다.
func NewHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 200
	transport.MaxIdleConnsPerHost = 100

	return &http.Client{Timeout: 5 * time.Minute, Transport: transport}
}

// FetchManifest는 GET /v1/markets/data?date=...를 호출합니다.
func FetchManifest(ctx context.Context, httpClient *http.Client, baseURL, date string) (Manifest, error) {
	u := fmt.Sprintf("%s/v1/markets/data?date=%s", baseURL, url.QueryEscape(date))
	var m Manifest
	err := fetchJSON(ctx, httpClient, u, &m)
	return m, err
}

// FetchBatch는 매니페스트가 돌려준 상대 경로(BatchURL)로 batch 파일을 받아옵니다.
func FetchBatch(ctx context.Context, httpClient *http.Client, baseURL, path string) (BatchFile, error) {
	var b BatchFile
	err := fetchJSON(ctx, httpClient, baseURL+path, &b)
	return b, err
}

// FetchStream은 매니페스트가 돌려준 상대 경로(StreamURL)로 stream 파일을 받아옵니다.
func FetchStream(ctx context.Context, httpClient *http.Client, baseURL, path string) (StreamFile, error) {
	var s StreamFile
	err := fetchJSON(ctx, httpClient, baseURL+path, &s)
	return s, err
}

func fetchJSON(ctx context.Context, httpClient *http.Client, target string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("요청 실패 (%s): status=%d", target, resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("응답 파싱 실패 (%s): %w", target, err)
	}
	return nil
}
