// Package backpressure는 matching 자체의 컨슈머 랙 기반 백프레셔(NFR-13, 원래
// matching 쪽으로 스코프된 버전 — recorder 쪽 랙 기반 버전과는 별개 병목을
// 다룹니다: matching이 못 따라가면 orders 토픽의 NEW/CANCEL이 쌓여서 매칭 자체가
// 밀리는 것이고, recorder가 못 따라가는 것과는 다른 상황입니다)를 구현합니다.
// recorder/backpressure와 같은 히스테리시스 설계를 그대로 재사용합니다 — 모듈
// 간 타입 비공유 원칙에 따라 독립적으로 재선언합니다.
package backpressure

import (
	"context"
	"log"
	"time"
)

// LagSource는 어떤 컨슈머의 현재 랙(메시지 건수)을 반환합니다 —
// `matching/kafkaclient.GroupConsumer.Lag`가 실제로 이 타입에 맞습니다.
type LagSource func() int64

// FlagSetter는 백프레셔 활성/비활성 상태를 어딘가에(실제로는 Redis) 기록합니다.
// Watcher를 실제 Redis 없이 테스트하기 위해 존재합니다.
type FlagSetter interface {
	SetActive(ctx context.Context, active bool) error
}

// Watcher는 주기적으로 Sources의 최댓값을 확인해 히스테리시스(상한/하한
// 워터마크)로 플래그를 켜고 끕니다 — recorder/backpressure.Watcher와 동일한
// 설계(단일 임계값 대신 상/하한을 분리해 깜빡임을 막음).
type Watcher struct {
	Sources       []LagSource
	Flag          FlagSetter
	HighWatermark int64
	LowWatermark  int64
	CheckInterval time.Duration
}

// Run은 ctx가 끝날 때까지 CheckInterval마다 검사하고, 매번 Flag를 다시
// 씁니다. matching은 여러 인스턴스가 동시에 도는 게 정상이므로(FR-11), 여기서
// 넘기는 Flag가 "비활성이어도 명시적으로 끄지 않는" 구현(matching용
// RedisFlag)일 수 있다는 점이 recorder와의 차이입니다 — 그 판단은 Flag 구현이
// 담당하고, Watcher 자체는 활성 여부를 그대로 넘길 뿐입니다.
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
				log.Printf("matching 백프레셔 상태 전환: %v -> %v (랙=%d)", active, next, lag)
			}
			active = next
			if err := w.Flag.SetActive(ctx, active); err != nil {
				log.Printf("matching 백프레셔 플래그 갱신 실패: %v", err)
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
// 결정합니다 — 순수 함수라 테스트하기 쉽습니다. recorder/backpressure와
// 동일한 히스테리시스 로직입니다.
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
