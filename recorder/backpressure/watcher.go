// Package backpressure는 RDS 백프레셔(2026-08-07, CLAUDE.md의 "RDS admission
// control via recorder consumer lag" 참고)를 구현합니다 — 기록기의 컨슈머 랙이
// 임계값을 넘으면 Redis 플래그를 켜고, orderapi가 그 플래그를 보고 신규 주문을
// 429로 거절합니다. 기록기 자체는 이 플래그를 켜고 끄는 쪽만 담당하고, 읽는
// 쪽(orderapi)은 독립적으로 다시 선언합니다(모듈 간 타입 비공유 원칙).
package backpressure

import (
	"context"
	"log"
	"time"
)

// LagSource는 어떤 컨슈머(리더)의 현재 랙(메시지 건수)을 반환합니다 —
// `recorder/kafka.OrderReader.Lag`/`ExecutionReader.Lag`가 실제로 이 타입에
// 맞습니다.
type LagSource func() int64

// FlagSetter는 백프레셔 활성/비활성 상태를 어딘가에(실제로는 Redis) 기록합니다.
// 이 인터페이스는 Watcher를 실제 Redis 없이 테스트하기 위해 존재합니다
// (orderapi/kafkaclient.Publisher와 같은 패턴).
type FlagSetter interface {
	SetActive(ctx context.Context, active bool) error
}

// Watcher는 주기적으로 Sources의 최댓값을 확인해 히스테리시스(상한/하한
// 워터마크)로 플래그를 켜고 끕니다. 임계값 하나만 쓰면 랙이 그 값 근처에서
// 오르내릴 때마다 플래그가 계속 깜빡여서(그리고 그때마다 orderapi가 429/정상을
// 왔다갔다) 오히려 더 불안정해지므로, 켜는 기준(HighWatermark)과 끄는 기준
// (LowWatermark, 더 낮음)을 분리했습니다.
type Watcher struct {
	Sources       []LagSource
	Flag          FlagSetter
	HighWatermark int64
	LowWatermark  int64
	CheckInterval time.Duration
}

// Run은 ctx가 끝날 때까지 CheckInterval마다 검사하고, 매번 Flag를 다시
// 씁니다(활성 상태면 Redis TTL 갱신, 비활성이면 명시적으로 끔) — 검사할
// 때마다 다시 쓰는 이유는 기록기가 살아있는 동안은 그 판단을 계속 재확인해서
// Redis 쪽 자기치유 TTL에만 의존하지 않게 하려는 것입니다.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.CheckInterval)
	defer ticker.Stop()

	var active bool
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lag := w.maxLag()
			next := w.nextState(active, lag)
			if next != active {
				log.Printf("백프레셔 상태 전환: %v -> %v (랙=%d)", active, next, lag)
			}
			active = next
			if err := w.Flag.SetActive(ctx, active); err != nil {
				log.Printf("백프레셔 플래그 갱신 실패: %v", err)
			}
		}
	}
}

func (w *Watcher) maxLag() int64 {
	var max int64
	for _, src := range w.Sources {
		if lag := src(); lag > max {
			max = lag
		}
	}
	return max
}

// nextState는 현재 활성 여부와 이번에 측정한 랙으로부터 다음 활성 여부를
// 결정합니다 — 순수 함수라 실제 Redis/Kafka 없이 테스트하기 쉽습니다.
// 꺼진 상태에서는 HighWatermark 이상이어야 켜지고, 켜진 상태에서는
// LowWatermark 이하여야 꺼집니다 — 그 사이 구간에서는 이전 상태를 그대로
// 유지합니다(히스테리시스).
func (w *Watcher) nextState(active bool, lag int64) bool {
	switch {
	case !active && lag >= w.HighWatermark:
		return true
	case active && lag <= w.LowWatermark:
		return false
	default:
		return active
	}
}
