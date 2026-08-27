package snapshotstore

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"matching/engine"
)

func newTestStore(t *testing.T) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return NewRedisStore(client), mr
}

func snap(market string, offset int64) engine.Snapshot {
	return engine.Snapshot{
		Market: market,
		Offset: offset,
		Bids:   []engine.OrderView{{OrderID: "ord_1", Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1), Offset: offset}},
	}
}

// TestHandoffThenStaleSaveIsRejected — 2026-08-27 중복 체결 사고의 핵심 시나리오를
// 그대로 재현합니다: Handoff(offset이 큼, 최신)가 먼저 쓰이고, 그보다 offset이
// 작은(오래된) Save가 나중에 도착해도 Handoff의 최신 상태를 덮어쓰면 안 됩니다.
func TestHandoffThenStaleSaveIsRejected(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()

	if err := s.Handoff(ctx, snap("KRW-BTC", 100)); err != nil {
		t.Fatalf("Handoff 실패: %v", err)
	}

	// writeNow를 직접 불러 Save의 비동기 큐를 거치지 않고 "나중에 도착한 오래된
	// 쓰기"를 결정론적으로 재현합니다(큐/고루틴 타이밍에 테스트가 의존하면 안 됨).
	if err := s.writeNow(ctx, snap("KRW-BTC", 90)); err != nil {
		t.Fatalf("writeNow(stale) 실패: %v", err)
	}

	got, ok, err := s.Load(ctx, "KRW-BTC")
	if err != nil || !ok {
		t.Fatalf("Load 실패: ok=%v err=%v", ok, err)
	}
	if got.Offset != 100 {
		t.Errorf("Offset = %d, want 100 (오래된 쓰기가 덮어쓰면 안 됨)", got.Offset)
	}
	_ = mr
}

// TestNewerSaveAfterHandoffWins — 반대 방향도 확인: Handoff 이후 실제로 더 진행된
// (offset이 더 큰) 정상적인 후속 Save는 당연히 반영돼야 합니다 — 조건부 쓰기가
// 모든 쓰기를 막아버리는 방향으로 과하게 걸리면 안 됨.
func TestNewerSaveAfterHandoffWins(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.Handoff(ctx, snap("KRW-BTC", 100)); err != nil {
		t.Fatalf("Handoff 실패: %v", err)
	}
	if err := s.writeNow(ctx, snap("KRW-BTC", 150)); err != nil {
		t.Fatalf("writeNow(newer) 실패: %v", err)
	}

	got, ok, err := s.Load(ctx, "KRW-BTC")
	if err != nil || !ok {
		t.Fatalf("Load 실패: ok=%v err=%v", ok, err)
	}
	if got.Offset != 150 {
		t.Errorf("Offset = %d, want 150 (더 새 쓰기는 반영돼야 함)", got.Offset)
	}
}

// TestEqualOffsetWriteIsRejected — 같은 offset 재시도(재전달 등)도 굳이 다시 쓸
// 필요 없다는 걸 확인 — 스크립트가 ">="로 막는지(">"가 아니라) 검증.
func TestEqualOffsetWriteIsRejected(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.Handoff(ctx, snap("KRW-BTC", 100)); err != nil {
		t.Fatalf("Handoff 실패: %v", err)
	}
	// 같은 offset이지만 내용이 다른(bids 비어있음) 스냅샷 — 반영되면 안 됨.
	dup := engine.Snapshot{Market: "KRW-BTC", Offset: 100}
	if err := s.writeNow(ctx, dup); err != nil {
		t.Fatalf("writeNow(dup) 실패: %v", err)
	}

	got, ok, err := s.Load(ctx, "KRW-BTC")
	if err != nil || !ok {
		t.Fatalf("Load 실패: ok=%v err=%v", ok, err)
	}
	if len(got.Bids) != 1 {
		t.Errorf("Bids = %v, want 1건 (같은 offset의 뒤늦은 쓰기가 반영되면 안 됨)", got.Bids)
	}
}

// TestFirstWriteAlwaysSucceeds — 키가 아예 없는 최초 저장(마켓을 처음 맡는 경우)은
// 비교할 대상이 없으니 무조건 성공해야 합니다.
func TestFirstWriteAlwaysSucceeds(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.writeNow(ctx, snap("KRW-ETH", 5)); err != nil {
		t.Fatalf("최초 저장 실패: %v", err)
	}
	got, ok, err := s.Load(ctx, "KRW-ETH")
	if err != nil || !ok || got.Offset != 5 {
		t.Errorf("got=%+v ok=%v err=%v, want Offset=5", got, ok, err)
	}
}

// TestDifferentMarketsAreIndependent — 마켓 A의 오래된 쓰기가 마켓 B의 최신 상태에
// 영향을 주면 안 됩니다(키가 마켓별로 분리돼 있는지 재확인).
func TestDifferentMarketsAreIndependent(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.Handoff(ctx, snap("KRW-BTC", 100)); err != nil {
		t.Fatalf("Handoff(BTC) 실패: %v", err)
	}
	if err := s.writeNow(ctx, snap("KRW-ETH", 1)); err != nil {
		t.Fatalf("writeNow(ETH) 실패: %v", err)
	}

	btc, _, _ := s.Load(ctx, "KRW-BTC")
	eth, _, _ := s.Load(ctx, "KRW-ETH")
	if btc.Offset != 100 {
		t.Errorf("BTC Offset = %d, want 100", btc.Offset)
	}
	if eth.Offset != 1 {
		t.Errorf("ETH Offset = %d, want 1", eth.Offset)
	}
}

// TestSaveQueueEventuallyReachesRedis — Save는 비동기지만, 결국(짧은 시간 안에)
// Redis에 반영돼야 합니다 — 조건부 쓰기 스크립트를 넣으면서 Save 자체가 조용히
// 실패하게 만들지 않았는지 확인.
func TestSaveQueueEventuallyReachesRedis(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	if err := s.Save(ctx, snap("KRW-BTC", 42)); err != nil {
		t.Fatalf("Save 실패: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok, _ := s.Load(ctx, "KRW-BTC"); ok && got.Offset == 42 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Save한 스냅샷이 2초 안에 Redis에 반영되지 않음")
}
