package kafkaclient

import (
	"slices"

	kafka "github.com/segmentio/kafka-go"

	"orderapi/validate"
)

// marketPartitioner는 kafka.Balancer(프로듀서 쪽 파티션 선택 인터페이스)를 구현합니다.
// 이전에 쓰던 &kafka.Hash{}는 메시지 Key(마켓명)를 해시해서 파티션을 고르는데,
// 해시 충돌이 나면 서로 다른 마켓이 같은 파티션을 공유할 수 있어(마켓 20개, 파티션
// 20개라도 해시 충돌 자체를 배제 못 함) FR-11(마켓 재분배)이 전제하는 "파티션 1개 =
// 마켓 1개" 매핑을 보장하지 못합니다. 대신 validate.TargetMarkets(정렬된 20개 리스트,
// matching/market.go와 순서까지 동일함을 확인함)에서 마켓명의 인덱스를 그대로 파티션
// 번호로 씁니다 — 해시가 아니라 조회라 충돌이 원천적으로 없습니다.
type marketPartitioner struct{}

func (marketPartitioner) Balance(msg kafka.Message, partitions ...int) int {
	market := string(msg.Key)
	idx := slices.Index(validate.TargetMarkets, market)
	if idx < 0 {
		// 대상 20개 마켓이 아닌 메시지(있어서는 안 되지만) — kafka.Hash{}와 같은
		// 안전망으로 폴백. 이 코드 경로를 타면 안 되므로 로그를 남기고 싶다면
		// 호출부(publish)에서 확인하는 편이 더 낫다(여기는 순수 함수로 유지).
		return (&kafka.Hash{}).Balance(msg, partitions...)
	}
	return partitions[idx%len(partitions)]
}
