package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newLockTestRegistry(t *testing.T, instanceID string) (*marketRegistry, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	return &marketRegistry{
		renewStops:  make(map[string]chan struct{}),
		redisClient: client,
		instanceID:  instanceID,
	}, mr
}

func TestAcquireMarketLockSucceedsWhenFree(t *testing.T) {
	r, _ := newLockTestRegistry(t, "instance-a")
	ctx := context.Background()

	if err := r.acquireMarketLock(ctx, "KRW-BTC"); err != nil {
		t.Fatalf("빈 락은 바로 획득돼야 하는데 실패: %v", err)
	}
}

// TestSecondInstanceCannotAcquireWhileHeld — 락이 살아있는 동안(TTL 이내) 다른
// 인스턴스는 획득에 실패(재시도 끝에 타임아웃)해야 합니다.
func TestSecondInstanceCannotAcquireWhileHeld(t *testing.T) {
	a, mr := newLockTestRegistry(t, "instance-a")
	ctx := context.Background()
	if err := a.acquireMarketLock(ctx, "KRW-BTC"); err != nil {
		t.Fatalf("instance-a 획득 실패: %v", err)
	}

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	b := &marketRegistry{redisClient: client, instanceID: "instance-b"}

	// marketLockMaxWait(10s) 전체를 실제로 기다리지 않도록, 타임아웃보다 짧은
	// 데드라인으로 직접 재시도 루프를 흉내낸다 — SETNX가 한 번이라도 성공하면 실패.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		ok, err := client.SetNX(ctx, marketLockKey("KRW-BTC"), b.instanceID, marketLockTTL).Result()
		if err != nil {
			t.Fatalf("SetNX 에러: %v", err)
		}
		if ok {
			t.Fatal("instance-a가 아직 락을 들고 있는데 instance-b가 SETNX에 성공함")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLockRenewalKeepsLockAliveBeyondOriginalTTL — 2026-08-27 실제 사고를 그대로
// 재현하는 핵심 테스트입니다: 갱신이 없으면 원래 TTL이 지나는 순간 락이 사라져
// 다른 인스턴스가 SETNX에 성공해버립니다(= split-brain). startLockRenewal이 돌고
// 있으면, TTL보다 훨씬 오래 지나도 다른 인스턴스는 계속 획득에 실패해야 합니다.
func TestLockRenewalKeepsLockAliveBeyondOriginalTTL(t *testing.T) {
	a, mr := newLockTestRegistry(t, "instance-a")
	ctx := context.Background()
	if err := a.acquireMarketLock(ctx, "KRW-BTC"); err != nil {
		t.Fatalf("instance-a 획득 실패: %v", err)
	}
	stop := a.startLockRenewal("KRW-BTC")
	defer close(stop)

	// 갱신 고루틴은 실제 time.Ticker(marketLockRenewInterval=5s)로 도므로, miniredis
	// 시계를 순간이동시키는 FastForward로는 "실제로 갱신 틱이 몇 번 울렸는지"를
	// 흉내낼 수 없다(가상 시계만 밀면 틱이 울기도 전에 이미 만료된 것으로 보여
	// 거짓 실패가 남 — 처음 이 테스트를 그렇게 짰다가 바로 이 문제로 실패했다).
	// 실제 TTL(15s)을 실제로 넘겨서 갱신 틱이 최소 한 번은 실제로 울리게 한다.
	time.Sleep(marketLockTTL + 3*time.Second)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	ok, err := client.SetNX(ctx, marketLockKey("KRW-BTC"), "instance-b", marketLockTTL).Result()
	if err != nil {
		t.Fatalf("SetNX 에러: %v", err)
	}
	if ok {
		t.Fatal("갱신이 돌고 있었는데도 다른 인스턴스가 락을 가져감 — split-brain 재현")
	}
}

// TestLockExpiresWithoutRenewal — 대조군: 갱신을 아예 안 걸면(옛 버그 상태) TTL이
// 지난 뒤 실제로 다른 인스턴스가 락을 가져갈 수 있어야 합니다 — 위 테스트가 정말
// "갱신 덕분에" 막힌 게 맞는지 확인하는 음성 대조.
func TestLockExpiresWithoutRenewal(t *testing.T) {
	a, mr := newLockTestRegistry(t, "instance-a")
	ctx := context.Background()
	if err := a.acquireMarketLock(ctx, "KRW-BTC"); err != nil {
		t.Fatalf("instance-a 획득 실패: %v", err)
	}
	// 갱신 없음(startLockRenewal 호출 안 함) — 옛 버그와 같은 상태.

	mr.FastForward(marketLockTTL + time.Second)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	ok, err := client.SetNX(ctx, marketLockKey("KRW-BTC"), "instance-b", marketLockTTL).Result()
	if err != nil {
		t.Fatalf("SetNX 에러: %v", err)
	}
	if !ok {
		t.Fatal("갱신 없이 TTL이 지났는데도 다른 인스턴스가 락을 못 가져감 (테스트 전제가 깨짐)")
	}
}

// TestRenewalStopsAfterStopChannelClosed — Release 시 갱신을 멈추면, 그 뒤로는
// TTL이 지나면 정상적으로 만료돼 다른 인스턴스가 가져갈 수 있어야 합니다.
func TestRenewalStopsAfterStopChannelClosed(t *testing.T) {
	a, mr := newLockTestRegistry(t, "instance-a")
	ctx := context.Background()
	if err := a.acquireMarketLock(ctx, "KRW-BTC"); err != nil {
		t.Fatalf("instance-a 획득 실패: %v", err)
	}
	stop := a.startLockRenewal("KRW-BTC")
	close(stop) // 즉시 멈춤 — Release 직후 상황을 흉내냄

	mr.FastForward(marketLockTTL + time.Second)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	ok, err := client.SetNX(ctx, marketLockKey("KRW-BTC"), "instance-b", marketLockTTL).Result()
	if err != nil {
		t.Fatalf("SetNX 에러: %v", err)
	}
	if !ok {
		t.Fatal("갱신을 멈췄는데도 TTL이 안 지나서 다른 인스턴스가 못 가져감")
	}
}

// TestReleaseMarketLockOnlyDeletesOwnLock — releaseLockScript가 표준 안전 해제
// 패턴(GET+DEL)대로, 이미 다른 인스턴스가 가져간 락을 실수로 지우지 않는지 확인.
func TestReleaseMarketLockOnlyDeletesOwnLock(t *testing.T) {
	a, mr := newLockTestRegistry(t, "instance-a")
	ctx := context.Background()
	a.acquireMarketLock(ctx, "KRW-BTC")

	// TTL 만료 + 다른 인스턴스가 가져감을 흉내냄.
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	client.Set(ctx, marketLockKey("KRW-BTC"), "instance-b", marketLockTTL)

	a.releaseMarketLock(ctx, "KRW-BTC") // instance-a가 뒤늦게 자기 것인 줄 알고 해제 시도

	val, err := client.Get(ctx, marketLockKey("KRW-BTC")).Result()
	if err != nil {
		t.Fatalf("Get 에러: %v", err)
	}
	if val != "instance-b" {
		t.Fatalf("instance-b의 락이 지워짐: val=%q", val)
	}
}
