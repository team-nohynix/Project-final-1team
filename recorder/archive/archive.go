// Package archive는 원본 주문/체결 이벤트를 S3(또는 로컬 개발용 디스크)에
// 아카이빙합니다 — RDS가 확정 저장소이고, 이건 원본 보관 목적의 best-effort
// 저장입니다(docs/architecture.md).
package archive

// Store는 한 종류("orders" 또는 "executions")의 마이크로배치 하나를 어딘가에
// 저장합니다. 호출마다 항상 새로운 오브젝트/파일을 만듭니다 — 같은 배치를 같은
// 키로 두 번 쓰는 경우가 없으므로, 구현체는 idempotency(재실행 시 덮어쓰기 방지)
// 체크가 필요 없습니다.
type Store interface {
	Save(kind string, records []any) (string, error)
}
