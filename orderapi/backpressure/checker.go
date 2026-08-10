// Package backpressure는 RDS 백프레셔(2026-08-07, CLAUDE.md의 "RDS admission
// control via recorder consumer lag" 참고)의 orderapi 쪽 읽기 절반을 다룹니다 —
// recorder/backpressure.Watcher가 기록기의 컨슈머 랙을 보고 Redis에 켜둔
// 플래그를 확인해서, acceptOrderHandler가 신규 주문을 429로 거절할지 정합니다.
// recorder와 독립적으로 다시 선언합니다(모듈 간 타입 비공유 원칙) — 플래그
// 키 이름만 양쪽이 일치하면 됩니다.
package backpressure

import "context"

// Checker는 백프레셔(컨슈머 랙 초과) 활성 여부를 확인합니다. RedisChecker가
// 실제 구현이고, 이 인터페이스는 핸들러를 실제 Redis 없이 테스트하기 위해
// 존재합니다(kafkaclient.Publisher/session.Store와 같은 패턴).
type Checker interface {
	Active(ctx context.Context) (bool, error)
}
