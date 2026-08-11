package archive

import (
	"log"
	"sync"
)

// Batcher는 한 종류(kind)의 레코드를 버퍼링해서, 건수(every) 또는 시간(interval)
// 중 먼저 도달하면 하나의 Store.Save 호출로 플러시합니다 —
// matching/engine.Engine.shouldSnapshot()과 같은 이중 트리거 패턴입니다.
// 건수 트리거는 Add에서 즉시 확인하고, 시간 트리거는 호출부(main.go)가 별도
// 타이머로 주기적으로 Flush를 불러줘야 합니다(빈 버퍼면 아무 일도 안 함).
//
// kind마다 별도의 Batcher를 둬야 합니다 — 한쪽이 몰릴 때 다른 쪽 플러시 주기가
// 밀리지 않게 하기 위함입니다.
type Batcher struct {
	store Store
	kind  string
	every int

	mu  sync.Mutex
	buf []any
}

func NewBatcher(store Store, kind string, every int) *Batcher {
	return &Batcher{store: store, kind: kind, every: every}
}

// Add는 레코드 하나를 버퍼에 넣고, 건수 트리거가 도달했으면 즉시 플러시합니다.
func (b *Batcher) Add(record any) {
	b.mu.Lock()
	b.buf = append(b.buf, record)
	trigger := len(b.buf) >= b.every
	b.mu.Unlock()

	if trigger {
		b.Flush()
	}
}

// Flush는 지금까지 쌓인 버퍼를 하나의 배치로 저장합니다. 버퍼가 비어 있으면
// 아무 일도 하지 않습니다 — 시간 트리거용 주기 타이머가 매번 불러도 안전합니다.
func (b *Batcher) Flush() {
	b.mu.Lock()
	if len(b.buf) == 0 {
		b.mu.Unlock()
		return
	}
	records := b.buf
	b.buf = nil
	b.mu.Unlock()

	if _, err := b.store.Save(b.kind, records); err != nil {
		log.Printf("아카이브 저장 실패 (kind=%s, %d건): %v", b.kind, len(records), err)
	}
}
