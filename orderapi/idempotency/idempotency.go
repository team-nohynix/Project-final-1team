// Package idempotency는 Idempotency-Key별 최초 응답을 캐싱합니다.
package idempotency

import (
	"sync"
	"time"
)

// CachedResponse는 한 Idempotency-Key에 대해 처음 만들어진 응답(상태 코드+본문)입니다.
type CachedResponse struct {
	Status int
	Body   []byte
}

// Store는 Idempotency-Key별로 최초 응답을 인메모리로 캐싱합니다
// (docs/api-specification.md 2.2 — 동일 키로 재요청 시 재검증·재발행 없이 최초 응답을
// 그대로 반환). 성공/실패 응답 모두 캐싱합니다 — 실패도 결과가 결정돼 있으니 같은 키로
// 재요청하면 항상 같은 결과가 나와야 "멱등"이라는 이름에 맞습니다.
//
// **insertedAt/Sweep — order.Store와 같은 이유(order.go 주석 참고, 2026-08-27
// orderapi 메모리 축출 사고 대응).** 이 캐시도 지금까지 지운 적이 없어서 body
// 바이트까지 같이 영원히 쌓였습니다 — 재요청으로 인한 재검증/재발행을 막는다는
// 목적상 필요한 건 "최근" 키뿐이라, order.Store와 같은 maxAge로 함께 정리합니다.
type Store struct {
	mu         sync.Mutex
	cache      map[string]CachedResponse
	insertedAt map[string]time.Time
}

// NewStore는 빈 Store를 만듭니다.
func NewStore() *Store {
	return &Store{cache: make(map[string]CachedResponse), insertedAt: make(map[string]time.Time)}
}

// Get은 key에 대해 캐싱된 응답이 있으면 반환합니다.
func (s *Store) Get(key string) (CachedResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.cache[key]
	return r, ok
}

// Put은 key에 대한 응답을 캐싱합니다. 이미 있으면 덮어쓰지 않습니다(최초 응답 유지).
func (s *Store) Put(key string, status int, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.cache[key]; exists {
		return
	}
	s.cache[key] = CachedResponse{Status: status, Body: body}
	s.insertedAt[key] = time.Now()
}

// Sweep은 order.Store.Sweep과 같은 규약입니다 — maxAge보다 오래된 캐시 항목을
// 지우고 지운 건수를 돌려줍니다.
func (s *Store) Sweep(maxAge time.Duration) int {
	cutoff := time.Now().Add(-maxAge)
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, t := range s.insertedAt {
		if t.Before(cutoff) {
			delete(s.cache, key)
			delete(s.insertedAt, key)
			removed++
		}
	}
	return removed
}
