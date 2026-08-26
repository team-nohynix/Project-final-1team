// Package engine은 마켓 하나를 담당하는 조립체입니다 — orderbook.OrderBook을 소유하고,
// orders 토픽에서 온 이벤트를 순서대로 적용하며, 체결이 나오면 발행하고, 주기적으로
// 스냅샷을 남깁니다(FR-08 재시작 복구 + FR-12 호가창 조회를 하나의 스냅샷으로 처리).
// Kafka/Redis의 구체 구현은 모르고 인터페이스로만 의존합니다.
package engine

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/shopspring/decimal"

	"matching/orderbook"
)

// OrderEventType은 orders 토픽 메시지의 종류입니다.
type OrderEventType string

const (
	EventNew    OrderEventType = "NEW"
	EventCancel OrderEventType = "CANCEL"
)

// OrderEvent는 orders 토픽의 한 메시지를 매칭 엔진이 다루기 좋은 형태로 옮긴 것입니다.
// kafkaclient 패키지가 실제 Kafka 메시지(JSON)를 이 타입으로 변환해 넘겨줍니다.
type OrderEvent struct {
	Type     OrderEventType
	OrderID  string
	Market   string
	Side     orderbook.Side
	Price    decimal.Decimal
	Quantity decimal.Decimal
	Offset   int64 // 이 이벤트를 소비한 orders 토픽 파티션 안의 오프셋
}

// ExecutionPublisher는 체결 결과를 내보냅니다(FR-09 중 Kafka 발행 부분 — DB 저장은
// "기록기"가 executions를 구독해서 하는 것이라 매칭 엔진의 책임이 아닙니다).
type ExecutionPublisher interface {
	Publish(ctx context.Context, exec orderbook.Execution) error
}

// OrderView는 스냅샷에 저장하는 개별 미체결 주문입니다(FR-08 복구용 — 잔량과 FIFO
// 순서를 그대로 보존해야 해서 집계하지 않습니다. 조회용 집계 뷰는 orderapi가 이
// 스냅샷을 읽어 depth만큼만 잘라 합산해서 만듭니다).
type OrderView struct {
	OrderID  string
	Price    decimal.Decimal
	Quantity decimal.Decimal
	Offset   int64
}

// Snapshot은 한 마켓의 특정 시점 상태 + 그 상태가 반영된 마지막 오프셋입니다.
type Snapshot struct {
	Market string
	Offset int64
	Bids   []OrderView
	Asks   []OrderView
}

// SnapshotStore는 스냅샷을 저장/로드합니다. engine은 이게 Redis인지, 어떻게
// 비동기 처리되는지 몰라도 됩니다 — Save가 핫패스를 막지 않는 건 구현체(snapshotstore
// 패키지)의 책임입니다.
//
// Save와 Handoff는 의도적으로 분리돼 있습니다 — Save는 비동기·유실 허용(평상시
// FR-12 조회 신선도/FR-08 크래시 복구용, 큐가 차면 이번 스냅샷을 그냥 건너뜀).
// Handoff는 동기·확정 저장이라, 이 마켓을 다른 인스턴스에 넘겨주기 직전에만 씁니다
// (FR-11) — Save의 비동기 지연을 그대로 두면, 마켓을 넘겨받는 인스턴스가 Recover()할
// 때 실제보다 몇 초 뒤처진 오프셋에서 시작해 그 사이 이벤트를 다시 매칭해 체결을
// 중복 발행하게 됩니다. 크래시 복구(가끔 발생)에서는 감내할 지연이지만, 정상적인
// 마켓 인계(리밸런스마다 발생)에서는 매번 재현되는 문제라 이 경로만 확정 저장이
// 필요합니다.
type SnapshotStore interface {
	Load(ctx context.Context, market string) (Snapshot, bool, error)
	Save(ctx context.Context, snap Snapshot) error
	Handoff(ctx context.Context, snap Snapshot) error
	// SaveWatermark/LoadWatermark는 전체 스냅샷과 별개인 아주 가벼운 체크포인트
	// (오프셋 하나)입니다(2026-08-20) — 실제 배포에서 OOMKilled(SIGKILL)로 죽은
	// 마켓이 그때까지 전체 스냅샷을 단 한 번도 못 남긴 경우, Recover()가 "이
	// 마켓이 예전에 처리된 적이 있다는 사실"만이라도 알 수 있게 해서, offset
	// 0부터 하루치를 통째로 재생하며 이미 끝난 매칭을 다시 실행(=체결 중복
	// 발행)하는 사고를 막습니다. Save와 같은 주기로 갱신하되, Save의 64개짜리
	// 비동기 큐를 거치지 않고 항상 동기로 씁니다 — 오프셋 하나만 담으므로 전체
	// 스냅샷(수십MB까지 갈 수 있음)보다 훨씬 가볍고, 그래서 훨씬 덜 유실됩니다.
	SaveWatermark(ctx context.Context, market string, offset int64) error
	LoadWatermark(ctx context.Context, market string) (offset int64, ok bool, err error)
}

// Engine은 마켓 하나를 담당합니다. 한 마켓은 고루틴 하나가 이 타입의 Apply를 순차
// 호출한다는 전제로 동시성 보호를 하지 않습니다(orderbook.OrderBook과 같은 이유).
type Engine struct {
	Market string
	book   *orderbook.OrderBook

	publisher ExecutionPublisher
	snapshots SnapshotStore

	snapshotEvery    int
	snapshotInterval time.Duration

	sinceSnapshot  int
	lastSnapshotAt time.Time
	// lastOffset는 "마지막으로 처리한 이벤트의 offset"입니다. 아직 하나도 처리한 적이
	// 없으면 -1입니다(0이 아님) — 0으로 두면 "offset 0을 이미 처리했다"와 구분이 안 돼서,
	// 이벤트를 하나도 못 받은 마켓(예: 아직 주문이 없는 마켓)을 Handoff하면 다음
	// 담당자가 resumeFrom=snap.Offset+1=1부터 읾어서 그 파티션에 실제로 처음 쓰이는
	// offset 0 메시지를 영원히 건너뛰게 됩니다(실제 인스턴스 2개로 검증하다 발견함:
	// 트래픽이 없던 마켓의 스냅샷이 {"offset":0, ...}으로 저장돼 있었음).
	lastOffset int64
}

// New는 market을 담당하는 Engine을 만듭니다. snapshotEvery/snapshotInterval은
// "N건마다 또는 T 시간마다, 둘 중 먼저 도달하면 스냅샷"의 기준입니다.
func New(market string, publisher ExecutionPublisher, snapshots SnapshotStore, snapshotEvery int, snapshotInterval time.Duration) *Engine {
	return &Engine{
		Market:           market,
		book:             orderbook.New(market),
		publisher:        publisher,
		snapshots:        snapshots,
		snapshotEvery:    snapshotEvery,
		snapshotInterval: snapshotInterval,
		lastSnapshotAt:   time.Now(),
		lastOffset:       -1,
	}
}

// Recover는 저장된 스냅샷을 불러와 호가창을 그 상태로 세팅합니다(FR-08). 스냅샷이
// 없으면(최초 실행) 빈 호가창인 채로 오프셋 0부터 시작합니다. 반환값은 이어서 컨슘을
// 시작해야 할 offset입니다.
//
// 전체 스냅샷이 없을 때 무조건 0부터 시작하던 게 실제 배포에서 문제가 됐습니다
// (2026-08-20) — OOMKilled(SIGKILL)로 죽은 마켓이 전체 스냅샷을 단 한 번도 못
// 남긴 경우, 다음 인수자가 그 마켓의 orders 파티션을 처음부터(며칠치일 수 있음)
// 다시 읽으면서 이미 끝난 매칭을 전부 재실행 — 빈 호가창에서 다시 매칭되며
// executions에 체결이 중복 발행되는 사고로 이어졌습니다. 그래서 전체 스냅샷이
// 없어도 워터마크(가벼운 오프셋 체크포인트, SnapshotStore 설명 참고)가 있으면
// "이 마켓은 예전에 분명히 처리된 적 있다"로 판단해 그 지점 다음부터 이어서
// 읽습니다 — 그 시점에 떠 있던 미체결 주문 자체는(호가창 상태가 없으니) 복구
// 못 하지만(스냅샷이 정말 없는 이상 피할 수 없는 손실), 과거 이력 전체를
// 재매칭하는 훨씬 나쁜 결과는 막습니다. 워터마크는 전체 스냅샷과 같은 주기로
// 갱신되므로 이 경로로 재생되는 이벤트는 최악의 경우도 그 정도 분량뿐이라,
// 원래 있던 "가끔 있는 크래시에서 비동기 스냅샷이 살짝 뒤처진 채로 복구되는"
// 것과 같은 수준의(이미 감내하기로 한) 위험으로 줄어듭니다.
func (e *Engine) Recover(ctx context.Context) (resumeFromOffset int64, err error) {
	snap, ok, err := e.snapshots.Load(ctx, e.Market)
	if err != nil {
		return 0, fmt.Errorf("스냅샷 로드 실패: %w", err)
	}
	if ok {
		for _, ov := range snap.Bids {
			e.book.Restore(&orderbook.Order{OrderID: ov.OrderID, Market: e.Market, Side: orderbook.Buy, Price: ov.Price, Quantity: ov.Quantity, Offset: ov.Offset})
		}
		for _, ov := range snap.Asks {
			e.book.Restore(&orderbook.Order{OrderID: ov.OrderID, Market: e.Market, Side: orderbook.Sell, Price: ov.Price, Quantity: ov.Quantity, Offset: ov.Offset})
		}
		e.lastOffset = snap.Offset
		// 임시 진단 로깅(2026-08-27, orderbook/match.go 주석 참고) — book 인스턴스
		// 포인터로 "이 담당 기간이 언제 시작했는지"를 남겨, 반복 체결이 한 담당
		// 기간 안에서 벌어지는지 여러 기간에 걸쳐 벌어지는지 구분한다.
		log.Printf("[진단] Recover 완료 (market=%s bookPtr=%p bids=%d asks=%d resumeFrom=%d)",
			e.Market, e.book, len(snap.Bids), len(snap.Asks), snap.Offset+1)
		return snap.Offset + 1, nil
	}

	wOffset, wOk, wErr := e.snapshots.LoadWatermark(ctx, e.Market)
	if wErr != nil {
		// 워터마크 조회 자체가 실패하면(Redis 순간 장애 등) "워터마크 없음"과
		// 구분할 방법이 없습니다 — 기존 동작(0부터 시작)을 그대로 유지합니다.
		// 이 폴백을 잘못된 방향(=있는데 없다고 오판)으로 타면 최악의 경우
		// 지금과 같은 전체 재생이 재발하지만, 반대 방향(=없는데 있다고
		// 오판해 tail 근처부터 시작)은 진짜 첫 실행인 마켓의 미처리 메시지를
		// 조용히 건너뛰는, 되돌릴 수 없는 데이터 손실이라 더 위험합니다.
		log.Printf("워터마크 조회 실패 (market=%s): %v — offset 0부터 시작합니다", e.Market, wErr)
		return 0, nil
	}
	if wOk {
		e.lastOffset = wOffset
		return wOffset + 1, nil
	}
	return 0, nil
}

// BookSize는 이 마켓의 호가창에 지금 남아있는 미체결 주문 개수입니다(orderbook.Size
// 그대로 위임) — matching_engine_book_size 메트릭이 marketRegistry를 통해 이걸
// 인스턴스가 담당 중인 모든 마켓에 대해 합산합니다.
func (e *Engine) BookSize() int {
	return e.book.Size()
}

// Apply는 orders 토픽에서 온 이벤트 하나를 처리합니다. NEW면 매칭 후 체결을 전부
// 발행하고(FR-09), CANCEL이면 호가창에서 제거합니다(FR-10). 체결 발행이 전부 성공한
// 뒤에만 이 이벤트의 offset을 "안전하게 처리됨"으로 기록합니다 — 발행 확인 전에 먼저
// 기록해두면, 그 사이 크래시가 나면 다음 복구 시 이 체결이 재생되지 않고 영원히
// 사라집니다(스냅샷 체크포인트 설계, FR-08).
//
// **offset 단조성 가드 — 2026-08-27, 정합성 검사 "중복 체결" 사고 대응(두 번째
// 방어선).** orderbook.Apply 자체도 같은 OrderID가 이미 호가창에 있으면 무시하지만,
// 그건 "그 주문이 아직 미체결로 남아있는 동안" 재전달된 경우만 잡는다 — 이미 전량
// 체결/취소돼 호가창에서 빠진 뒤에 같은 이벤트가 재전달되면 그 체크로는 못 잡고
// 완전히 새 주문처럼 다시 매칭돼버린다. 이 마켓은 파티션 하나를 고루틴 하나가
// 순차 소비하므로(consumer.go 참고) offset은 항상 엄격한 전순서다 — 정상 상황이면
// ev.Offset은 항상 e.lastOffset+1이어야 하고, 그보다 작거나 같으면 무조건 이미
// 처리된(=재전달된) 이벤트다. NEW/CANCEL 둘 다 여기서 한 번에 막는다.
func (e *Engine) Apply(ctx context.Context, ev OrderEvent) error {
	if ev.Offset <= e.lastOffset {
		log.Printf("[진단] offset 가드 발동 (market=%s orderId=%s evOffset=%d lastOffset=%d)", e.Market, ev.OrderID, ev.Offset, e.lastOffset)
		return nil
	}

	switch ev.Type {
	case EventNew:
		o := &orderbook.Order{OrderID: ev.OrderID, Market: ev.Market, Side: ev.Side, Price: ev.Price, Quantity: ev.Quantity, Offset: ev.Offset}
		for _, exec := range e.book.Apply(o) {
			if err := e.publisher.Publish(ctx, exec); err != nil {
				return fmt.Errorf("체결 발행 실패 (market=%s): %w", e.Market, err)
			}
		}
	case EventCancel:
		e.book.Cancel(ev.OrderID)
	}

	e.lastOffset = ev.Offset
	e.sinceSnapshot++
	if e.shouldSnapshot() {
		if err := e.snapshot(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) shouldSnapshot() bool {
	return e.sinceSnapshot >= e.snapshotEvery || time.Since(e.lastSnapshotAt) >= e.snapshotInterval
}

func (e *Engine) snapshot(ctx context.Context) error {
	if err := e.snapshots.Save(ctx, e.currentSnapshot()); err != nil {
		return fmt.Errorf("스냅샷 저장 실패 (market=%s): %w", e.Market, err)
	}
	// 워터마크 저장은 실패해도 스냅샷 저장 자체(=Apply)를 막지 않습니다 —
	// Recover()의 마지막 안전장치일 뿐인 보조 신호라, 이번 주기에 실패해도
	// 다음 주기에 다시 시도하면 됩니다(SnapshotStore 인터페이스 설명 참고).
	if err := e.snapshots.SaveWatermark(ctx, e.Market, e.lastOffset); err != nil {
		log.Printf("워터마크 저장 실패 (market=%s, offset=%d): %v", e.Market, e.lastOffset, err)
	}
	e.sinceSnapshot = 0
	e.lastSnapshotAt = time.Now()
	return nil
}

// Handoff는 이 마켓을 다른 인스턴스에 넘겨주기 직전에 호출합니다(FR-11) — 지금
// 상태를 동기적으로 확정 저장해서, 다음 담당 인스턴스의 Recover()가 실제 마지막
// 처리 지점부터 정확히 이어받게 합니다. 호출 전에 이 Engine으로의 Apply 호출이
// 전부 끝나 있어야 합니다(호출부가 고루틴을 완전히 join한 뒤에 불러야 함) — 안
// 그러면 저장하는 도중에도 상태가 계속 바뀌어 스냅샷이 일관되지 않을 수 있습니다.
func (e *Engine) Handoff(ctx context.Context) error {
	snap := e.currentSnapshot()
	log.Printf("[진단] Handoff 시작 (market=%s bookPtr=%p bids=%d asks=%d offset=%d)",
		e.Market, e.book, len(snap.Bids), len(snap.Asks), snap.Offset)
	if err := e.snapshots.Handoff(ctx, snap); err != nil {
		return fmt.Errorf("핸드오프 스냅샷 저장 실패 (market=%s): %w", e.Market, err)
	}
	return nil
}

func (e *Engine) currentSnapshot() Snapshot {
	return Snapshot{
		Market: e.Market,
		Offset: e.lastOffset,
		Bids:   toOrderViews(e.book.AllOrders(orderbook.Buy)),
		Asks:   toOrderViews(e.book.AllOrders(orderbook.Sell)),
	}
}

func toOrderViews(orders []*orderbook.Order) []OrderView {
	out := make([]OrderView, 0, len(orders))
	for _, o := range orders {
		out = append(out, OrderView{OrderID: o.OrderID, Price: o.Price, Quantity: o.Quantity, Offset: o.Offset})
	}
	return out
}
