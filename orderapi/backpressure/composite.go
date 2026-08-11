package backpressure

import "context"

// MultiChecker는 여러 Checker를 하나로 합칩니다 — 2026-08-10, matching 자체
// 컨슈머 랙 기반 백프레셔(NFR-13, matching 쪽 랙)가 recorder 랙 기반 백프레셔와
// 별개로 추가되면서, acceptOrderHandler가 두 플래그(recorder 랙 키 +
// matching 랙 키)를 모두 확인해야 하게 됐습니다. 둘 중 하나라도 "밀리고
// 있다"고 하면 신규 주문을 받지 않아야 하므로, 하나라도 active면 나머지를
// 더 볼 필요 없이 즉시 true를 반환합니다 — 그래서 다른 Checker가 에러를
// 내더라도(예: matching 쪽 Redis 키는 못 읽었지만 recorder 쪽은 active로 읽힌
// 경우) 이미 확정된 "거절해야 한다"는 결론이 뒤집히지 않습니다.
//
// 전부 비활성이거나 에러였던 경우, fail-open 원칙(server.go의 checker.Active
// 호출부 참고 — 에러면 열린 것으로 간주)을 유지하기 위해 처음 만난 에러를
// 그대로 반환합니다 — 호출부가 그 에러를 로그하고 계속 진행(=거절하지 않음)할
// 수 있게 합니다.
type MultiChecker []Checker

func (m MultiChecker) Active(ctx context.Context) (bool, error) {
	var firstErr error
	for _, c := range m {
		active, err := c.Active(ctx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if active {
			return true, nil
		}
	}
	return false, firstErr
}
