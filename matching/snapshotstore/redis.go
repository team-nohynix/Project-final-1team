// Package snapshotstore는 engine.SnapshotStore를 Redis(ElastiCache 호환)로 구현합니다.
package snapshotstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"
	"github.com/shopspring/decimal"

	"matching/engine"
)

// orderView/snapshotDoc은 Redis에 저장하는 JSON 모양입니다. orderapi도 이 모양을 그대로
// (독립적으로 다시 선언해서) 읽어 조회 API(FR-12) 응답을 만듭니다 — 필드명이 바뀌면
// orderapi 쪽도 같이 맞춰야 합니다.
type orderView struct {
	OrderID  string `json:"orderId"`
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
	Offset   int64  `json:"offset"`
}

type snapshotDoc struct {
	Market string      `json:"market"`
	Offset int64       `json:"offset"`
	Bids   []orderView `json:"bids"`
	Asks   []orderView `json:"asks"`
}

// RedisStore는 engine.SnapshotStore를 Redis로 구현합니다. Save는 매칭 루프를 막지
// 않도록 버퍼 채널 + 백그라운드 고루틴으로 비동기 처리합니다 — 호출부(engine)는 이
// 사실을 몰라도 됩니다.
type RedisStore struct {
	client *redis.Client
	queue  chan engine.Snapshot
}

func NewRedisStore(addr string) *RedisStore {
	s := &RedisStore{
		client: redis.NewClient(&redis.Options{Addr: addr}),
		queue:  make(chan engine.Snapshot, 64),
	}
	go s.worker()
	return s
}

func (s *RedisStore) worker() {
	for snap := range s.queue {
		if err := s.writeNow(context.Background(), snap); err != nil {
			log.Printf("스냅샷 Redis 저장 실패 (market=%s): %v", snap.Market, err)
		}
	}
}

// Save는 즉시 반환합니다 — 실제 Redis 쓰기는 백그라운드 고루틴이 처리합니다. 큐가
// 가득 차면(Redis 장애 등으로 백그라운드 쓰기가 못 따라가는 극단적 상황) 이번
// 스냅샷은 버리고 다음 주기에 다시 시도합니다 — 매칭 루프가 절대 블록되면 안 됩니다.
func (s *RedisStore) Save(_ context.Context, snap engine.Snapshot) error {
	select {
	case s.queue <- snap:
	default:
		log.Printf("스냅샷 큐가 가득 차서 이번 스냅샷은 건너뜀 (market=%s)", snap.Market)
	}
	return nil
}

// Handoff는 Save와 달리 큐를 거치지 않고 즉시(동기적으로) 확정 저장합니다 —
// engine.SnapshotStore의 설명 참고. 마켓을 다른 인스턴스에 넘겨줄 때만 씁니다.
func (s *RedisStore) Handoff(ctx context.Context, snap engine.Snapshot) error {
	return s.writeNow(ctx, snap)
}

func (s *RedisStore) writeNow(ctx context.Context, snap engine.Snapshot) error {
	doc := snapshotDoc{
		Market: snap.Market,
		Offset: snap.Offset,
		Bids:   toOrderViews(snap.Bids),
		Asks:   toOrderViews(snap.Asks),
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, key(snap.Market), body, 0).Err()
}

// Load는 engine.SnapshotStore를 만족합니다 — 최초 실행이라 저장된 스냅샷이 없으면
// (redis.Nil) ok=false를 돌려줍니다.
func (s *RedisStore) Load(ctx context.Context, market string) (engine.Snapshot, bool, error) {
	body, err := s.client.Get(ctx, key(market)).Bytes()
	if err == redis.Nil {
		return engine.Snapshot{}, false, nil
	}
	if err != nil {
		return engine.Snapshot{}, false, err
	}

	var doc snapshotDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return engine.Snapshot{}, false, fmt.Errorf("스냅샷 파싱 실패: %w", err)
	}

	bids, err := fromOrderViews(doc.Bids)
	if err != nil {
		return engine.Snapshot{}, false, fmt.Errorf("bids 파싱 실패: %w", err)
	}
	asks, err := fromOrderViews(doc.Asks)
	if err != nil {
		return engine.Snapshot{}, false, fmt.Errorf("asks 파싱 실패: %w", err)
	}

	return engine.Snapshot{Market: doc.Market, Offset: doc.Offset, Bids: bids, Asks: asks}, true, nil
}

func key(market string) string {
	return "orderbook:" + market
}

func toOrderViews(ovs []engine.OrderView) []orderView {
	out := make([]orderView, 0, len(ovs))
	for _, ov := range ovs {
		out = append(out, orderView{OrderID: ov.OrderID, Price: ov.Price.String(), Quantity: ov.Quantity.String(), Offset: ov.Offset})
	}
	return out
}

func fromOrderViews(ovs []orderView) ([]engine.OrderView, error) {
	out := make([]engine.OrderView, 0, len(ovs))
	for _, ov := range ovs {
		price, err := decimal.NewFromString(ov.Price)
		if err != nil {
			return nil, err
		}
		qty, err := decimal.NewFromString(ov.Quantity)
		if err != nil {
			return nil, err
		}
		out = append(out, engine.OrderView{OrderID: ov.OrderID, Price: price, Quantity: qty, Offset: ov.Offset})
	}
	return out, nil
}
