package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"matching/orderbook"
)

type fakePublisher struct {
	published  []orderbook.Execution
	failNext   bool
	lastCtxErr error // Publish가 실제로 받은 ctx의 Err() — 호출부 컨텍스트 취소 여부 검증용
}

func (p *fakePublisher) Publish(ctx context.Context, exec orderbook.Execution) error {
	p.lastCtxErr = ctx.Err()
	if p.failNext {
		p.failNext = false
		return errors.New("발행 실패(테스트용)")
	}
	p.published = append(p.published, exec)
	return nil
}

type fakeSnapshotStore struct {
	saved        map[string]Snapshot
	handoffCalls int
	watermarks   map[string]int64
	watermarkErr error
}

func newFakeSnapshotStore() *fakeSnapshotStore {
	return &fakeSnapshotStore{saved: make(map[string]Snapshot), watermarks: make(map[string]int64)}
}

func (s *fakeSnapshotStore) Load(_ context.Context, market string) (Snapshot, bool, error) {
	snap, ok := s.saved[market]
	return snap, ok, nil
}

func (s *fakeSnapshotStore) Save(_ context.Context, snap Snapshot) error {
	s.saved[snap.Market] = snap
	return nil
}

func (s *fakeSnapshotStore) Handoff(_ context.Context, snap Snapshot) error {
	s.handoffCalls++
	s.saved[snap.Market] = snap
	return nil
}

func (s *fakeSnapshotStore) SaveWatermark(_ context.Context, market string, offset int64) error {
	s.watermarks[market] = offset
	return nil
}

func (s *fakeSnapshotStore) LoadWatermark(_ context.Context, market string) (int64, bool, error) {
	if s.watermarkErr != nil {
		return 0, false, s.watermarkErr
	}
	v, ok := s.watermarks[market]
	return v, ok, nil
}

func dec(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	v, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return v
}

func newOrderEvent(t OrderEventType, id string, side orderbook.Side, price, qty string, offset int64) OrderEvent {
	return OrderEvent{Type: t, OrderID: id, Market: "KRW-BTC", Side: side, Price: dec(price), Quantity: dec(qty), Offset: offset}
}

func TestEngineApplyPublishesExecutions(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1000, time.Hour)

	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 1)); err != nil {
		t.Fatalf("ask1 접수 실패: %v", err)
	}
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 2)); err != nil {
		t.Fatalf("bid1 접수 실패: %v", err)
	}

	if len(pub.published) != 1 {
		t.Fatalf("체결 발행 1건을 기대했으나 %d건", len(pub.published))
	}
	if pub.published[0].BuyOrderID != "bid1" || pub.published[0].SellOrderID != "ask1" {
		t.Errorf("체결 내용이 예상과 다름: %+v", pub.published[0])
	}
}

func TestEngineApplyCancel(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1000, time.Hour)

	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 1)); err != nil {
		t.Fatalf("ask1 접수 실패: %v", err)
	}
	if err := e.Apply(context.Background(), newOrderEvent(EventCancel, "ask1", orderbook.Sell, "", "", 2)); err != nil {
		t.Fatalf("ask1 취소 실패: %v", err)
	}
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 3)); err != nil {
		t.Fatalf("bid1 접수 실패: %v", err)
	}

	if len(pub.published) != 0 {
		t.Fatalf("취소된 주문과는 체결이 없어야 하는데 %d건 발행됨", len(pub.published))
	}
}

func TestEnginePublishFailureReturnsErrorAndDoesNotAdvanceOffset(t *testing.T) {
	pub := &fakePublisher{failNext: true}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1, time.Hour) // snapshotEvery=1이라 다음 이벤트에서 바로 스냅샷 트리거됨

	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 1)); err != nil {
		t.Fatalf("ask1 접수 실패: %v", err)
	}
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 2)); err == nil {
		t.Fatal("체결 발행이 실패했는데 Apply가 에러를 반환하지 않음")
	}

	// 발행 실패한 이벤트(offset=2)의 오프셋은 안전하게 처리된 것으로 기록되면 안 된다.
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "ask2", orderbook.Sell, "200", "1", 3)); err != nil {
		t.Fatalf("ask2 접수 실패: %v", err)
	}
	snap, ok, _ := store.Load(context.Background(), "KRW-BTC")
	if !ok {
		t.Fatal("스냅샷이 저장되지 않음")
	}
	if snap.Offset == 2 {
		t.Errorf("발행 실패한 이벤트의 오프셋(2)이 체크포인트에 기록됨: %+v", snap)
	}
}

func TestEngineSnapshotTriggersByCount(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 2, time.Hour)

	e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 1))
	if _, ok, _ := store.Load(context.Background(), "KRW-BTC"); ok {
		t.Fatal("아직 스냅샷 주기(2건)에 도달 안 했는데 저장됨")
	}
	e.Apply(context.Background(), newOrderEvent(EventNew, "ask2", orderbook.Sell, "101", "1", 2))
	if _, ok, _ := store.Load(context.Background(), "KRW-BTC"); !ok {
		t.Fatal("2건째에 스냅샷이 저장돼야 함")
	}
}

func TestEngineRecoverRestoresBookAndResumeOffset(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	store.saved["KRW-BTC"] = Snapshot{
		Market: "KRW-BTC",
		Offset: 41,
		Bids:   []OrderView{{OrderID: "bid1", Price: dec("100"), Quantity: dec("1"), Offset: 10}},
		Asks:   []OrderView{{OrderID: "ask1", Price: dec("101"), Quantity: dec("2"), Offset: 11}},
	}

	e := New("KRW-BTC", pub, store, 1000, time.Hour)
	resumeFrom, err := e.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover 실패: %v", err)
	}
	if resumeFrom != 42 {
		t.Errorf("resumeFrom = %d, want 42 (스냅샷 offset+1)", resumeFrom)
	}

	// 복구된 매도(101, 수량 2)와 매수(102)가 실제로 매칭되는지 확인 — 복구가 제대로
	// 됐다면 이 신규 주문은 정상적으로 체결돼야 한다.
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "bid2", orderbook.Buy, "101", "2", 42)); err != nil {
		t.Fatalf("복구 후 신규 주문 접수 실패: %v", err)
	}
	if len(pub.published) != 1 || pub.published[0].SellOrderID != "ask1" {
		t.Fatalf("복구된 매도 주문과 체결돼야 하는데: %+v", pub.published)
	}
}

// TestEngineHandoffWithNoEventsDoesNotSkipOffsetZero는 실제 인스턴스 2개로 검증하다
// 발견한 버그의 회귀 테스트입니다 — 트래픽이 전혀 없던 마켓을 (제너레이션이 끝나서)
// 곧바로 Handoff하면, 다음 담당자가 resumeFrom=1부터 읽어서 그 파티션에 실제로 처음
// 쓰이는 offset 0 메시지를 영원히 건너뛰는 게 관찰됐다. 아무 이벤트도 처리하지 않은
// Engine을 Handoff해도 다음 Recover는 반드시 0부터 시작해야 한다.
func TestEngineHandoffWithNoEventsDoesNotSkipOffsetZero(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-USDT", pub, store, 1000, time.Hour)

	if err := e.Handoff(context.Background()); err != nil {
		t.Fatalf("Handoff 실패: %v", err)
	}

	newOwner := New("KRW-USDT", pub, store, 1000, time.Hour)
	resumeFrom, err := newOwner.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover 실패: %v", err)
	}
	if resumeFrom != 0 {
		t.Errorf("resumeFrom = %d, want 0 (아무 이벤트도 처리 안 했으면 offset 0부터 시작해야 함)", resumeFrom)
	}
}

func TestEngineRecoverNoSnapshotStartsFromZero(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1000, time.Hour)

	resumeFrom, err := e.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover 실패: %v", err)
	}
	if resumeFrom != 0 {
		t.Errorf("스냅샷이 없으면 0부터 시작해야 하는데 resumeFrom = %d", resumeFrom)
	}
}

// TestEngineRecoverNoSnapshotButWatermarkResumesFromWatermark는 2026-08-20 수정의
// 핵심 시나리오입니다 — 실제 배포에서 OOMKilled로 죽은 마켓이 전체 스냅샷을 단
// 한 번도 못 남기고, 대신 워터마크(가벼운 오프셋 체크포인트)만 남아있는 경우를
// 재현합니다. 이때 0부터(=하루치를 통째로 재생하며 이미 끝난 매칭을 재실행) 시작하면
// 안 되고, 워터마크 다음부터 이어서 읽어야 합니다. 호가창 자체는(스냅샷이 없으니)
// 비어있는 채로 시작하는 것도 함께 확인합니다 — 이건 감수하는 손실이지 버그가 아닙니다.
func TestEngineRecoverNoSnapshotButWatermarkResumesFromWatermark(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	store.watermarks["KRW-BTC"] = 999 // 전체 스냅샷은 없음(store.saved가 비어있음)

	e := New("KRW-BTC", pub, store, 1000, time.Hour)
	resumeFrom, err := e.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover 실패: %v", err)
	}
	if resumeFrom != 1000 {
		t.Errorf("resumeFrom = %d, want 1000 (워터마크 offset+1) — 0부터 재생하면 안 됨", resumeFrom)
	}

	// 호가창은 비어있어야 정상(복구할 상태 자체가 없음) — 새 주문 하나만 접수돼도
	// 체결 없이 그냥 미체결로 남아야 한다(상대편이 없으므로).
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 1000)); err != nil {
		t.Fatalf("주문 접수 실패: %v", err)
	}
	if len(pub.published) != 0 {
		t.Errorf("빈 호가창에서 시작했어야 하는데 체결이 발생함: %+v", pub.published)
	}
}

// TestEngineRecoverWatermarkLoadErrorFallsBackToZero는 워터마크 조회 자체가
// 실패하면(Redis 순간 장애 등) "워터마크 없음"과 구분할 수 없으므로 기존 동작
// (0부터 시작)을 유지하는지 확인합니다 — 반대로 갔다가 정말 첫 실행인 마켓의
// 메시지를 조용히 건너뛰는 게 더 위험하다는 판단(Recover의 주석 참고).
func TestEngineRecoverWatermarkLoadErrorFallsBackToZero(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	store.watermarkErr = errors.New("redis 순간 장애(테스트용)")

	e := New("KRW-BTC", pub, store, 1000, time.Hour)
	resumeFrom, err := e.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover가 에러를 반환하면 안 됨(워터마크 조회 실패는 fail-open): %v", err)
	}
	if resumeFrom != 0 {
		t.Errorf("워터마크 조회 실패 시 resumeFrom = %d, want 0", resumeFrom)
	}
}

// TestEngineSnapshotAlsoSavesWatermark는 주기적 스냅샷이 트리거될 때마다
// 워터마크도 같이 갱신되는지 확인합니다 — 이게 없으면 위 두 테스트가 지키는
// 계약을 실제로 채울 방법이 없습니다.
func TestEngineSnapshotAlsoSavesWatermark(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1, time.Hour) // 1건마다 스냅샷 트리거

	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 5)); err != nil {
		t.Fatalf("ask1 접수 실패: %v", err)
	}
	if got, ok := store.watermarks["KRW-BTC"]; !ok || got != 5 {
		t.Errorf("watermarks[KRW-BTC] = %d (ok=%v), want 5", got, ok)
	}
}

// TestEngineHandoffWritesSynchronouslyAndSurvivesToNewOwner는 FR-11 인계 시나리오를
// 재현합니다 — snapshotEvery/snapshotInterval을 아주 크게 잡아 평상시 비동기 스냅샷이
// 전혀 안 일어나게 한 뒤, Handoff를 직접 호출해도 그 상태가 그대로 저장되고, 새
// Engine(=인수하는 인스턴스)이 그 지점부터 정확히 이어받는지 확인합니다.
func TestEngineHandoffWritesSynchronouslyAndSurvivesToNewOwner(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1_000_000, time.Hour) // 평상시 스냅샷이 안 일어나도록

	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 1)); err != nil {
		t.Fatalf("ask1 접수 실패: %v", err)
	}
	if _, ok, _ := store.Load(context.Background(), "KRW-BTC"); ok {
		t.Fatal("Handoff를 부르기 전인데 스냅샷이 이미 저장돼 있음(비동기 주기 설정이 잘못됨)")
	}

	if err := e.Handoff(context.Background()); err != nil {
		t.Fatalf("Handoff 실패: %v", err)
	}
	if store.handoffCalls != 1 {
		t.Errorf("Handoff 호출 횟수 = %d, want 1", store.handoffCalls)
	}

	newOwner := New("KRW-BTC", pub, store, 1_000_000, time.Hour)
	resumeFrom, err := newOwner.Recover(context.Background())
	if err != nil {
		t.Fatalf("새 담당 인스턴스의 Recover 실패: %v", err)
	}
	if resumeFrom != 2 { // ask1의 offset(1) 다음부터
		t.Errorf("resumeFrom = %d, want 2", resumeFrom)
	}

	// 인수한 상태가 실제로 매칭 가능한지도 확인 — ask1이 그대로 남아있어야 함.
	if err := newOwner.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 2)); err != nil {
		t.Fatalf("인수 후 신규 주문 접수 실패: %v", err)
	}
	if len(pub.published) != 1 || pub.published[0].SellOrderID != "ask1" {
		t.Fatalf("인수받은 ask1과 체결돼야 하는데: %+v", pub.published)
	}
}

// TestApplyDuplicateOffsetIsIgnored — 2026-08-27 중복 체결 사고의 두 번째 방어선을
// 검증합니다: 이미 처리한 offset과 같거나 더 작은 이벤트는(재전달 등으로 다시
// 도착해도) 완전히 무시돼야 합니다 — orderbook.Apply 자체의 OrderID 체크(이미
// 미체결로 남아있는 동안만 잡음)와 달리, 이 가드는 전량 체결/취소돼 호가창에서
// 빠진 뒤에 재전달되는 경우까지 포함해 offset만으로 통째로 막습니다.
func TestApplyDuplicateOffsetIsIgnored(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1000, time.Hour)

	// ask1이 bid1과 정확히 맞아 전량 체결되고 둘 다 호가창에서 사라짐.
	e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 1))
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 2)); err != nil {
		t.Fatalf("bid1 접수 실패: %v", err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("첫 체결 1건을 기대했으나 %d건", len(pub.published))
	}

	// bid1(offset=2)이 재전달됨 — 이미 전량 체결/제거된 뒤라 orderbook의 OrderID
	// 체크만으로는 못 잡는 케이스. offset 가드가 없으면 완전히 새 주문처럼
	// 재매칭돼(상대가 없어 이번엔 호가창에 편입) 유령 잔량이 생긴다.
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 2)); err != nil {
		t.Fatalf("재전달된 bid1 처리 실패: %v", err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("재전달은 무시돼 체결이 늘면 안 되는데 published=%d", len(pub.published))
	}

	// 재전달이 새 미체결 주문으로 편입되지 않았는지 확인 — 상대가 나타나도 체결이
	// 없어야 함.
	e.Apply(context.Background(), newOrderEvent(EventNew, "ask2", orderbook.Sell, "100", "1", 3))
	if len(pub.published) != 1 {
		t.Fatalf("유령 bid1이 있었다면 여기서 체결이 하나 더 났을 것 — published=%d", len(pub.published))
	}
}

// TestApplyOlderOffsetAfterRecoveryIsIgnored — Recover 이후 정상적으로 이어받은
// offset보다 작은 이벤트가(스냅샷 경쟁 등으로) 들어와도 무시되는지 확인합니다.
func TestApplyOlderOffsetAfterRecoveryIsIgnored(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1000, time.Hour)

	e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 5))
	e.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 6))
	if len(pub.published) != 1 {
		t.Fatalf("체결 1건을 기대했으나 %d건", len(pub.published))
	}

	// offset=3은 이미 지나온 offset=6보다 작음 — 오래된/재전달된 이벤트로 간주해 무시.
	if err := e.Apply(context.Background(), newOrderEvent(EventNew, "late1", orderbook.Sell, "100", "1", 3)); err != nil {
		t.Fatalf("오래된 offset 처리 실패: %v", err)
	}
	bids, asks := e.book.Snapshot(10)
	if len(bids) != 0 || len(asks) != 0 {
		t.Errorf("오래된 offset 이벤트가 무시되지 않고 호가창에 반영됨: bids=%v asks=%v", bids, asks)
	}
}

// TestApplyPublishSurvivesCallerContextCancellation — 2026-08-27 중복 체결 사고의
// 진짜 근본 원인을 그대로 재현합니다: Apply에 넘어온 ctx가(리밸런스로 genCtx가
// 취소되는 실제 상황을 흉내내) 이미 취소돼 있어도, book 변형은 이미 되돌릴 수 없이
// 끝난 뒤이므로 체결 발행은 그 취소와 무관하게 시도돼야 합니다 — publisher가 실제로
// 받은 ctx가 살아있는(Err()==nil) 별도 컨텍스트인지 직접 확인합니다.
func TestApplyPublishSurvivesCallerContextCancellation(t *testing.T) {
	pub := &fakePublisher{}
	store := newFakeSnapshotStore()
	e := New("KRW-BTC", pub, store, 1000, time.Hour)

	e.Apply(context.Background(), newOrderEvent(EventNew, "ask1", orderbook.Sell, "100", "1", 1))

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // 리밸런스로 genCtx가 이미 끝난 상황을 흉내냄

	if err := e.Apply(cancelledCtx, newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 2)); err != nil {
		t.Fatalf("호출부 ctx가 취소돼 있어도 Apply 자체는 성공해야 하는데: %v", err)
	}
	if len(pub.published) != 1 {
		t.Fatalf("체결 발행 1건을 기대했으나 %d건 (취소된 ctx 때문에 발행이 막혔을 가능성)", len(pub.published))
	}
	if pub.lastCtxErr != nil {
		t.Errorf("publisher가 받은 ctx.Err() = %v, want nil (호출부의 취소된 ctx를 그대로 전달하면 안 됨)", pub.lastCtxErr)
	}

	// offset이 정상적으로 전진했는지도 확인 — 발행이 성공했으니 이 이벤트는 "처리됨"으로
	// 기록돼야 하고, 재전달돼도 offset 가드에 걸려 무시돼야 함.
	e.Apply(context.Background(), newOrderEvent(EventNew, "bid1", orderbook.Buy, "100", "1", 2))
	if len(pub.published) != 1 {
		t.Fatalf("offset이 전진했다면 재전달은 무시돼야 하는데 published=%d", len(pub.published))
	}
}
