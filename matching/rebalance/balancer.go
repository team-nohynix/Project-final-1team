package rebalance

import (
	"context"
	"log"

	kafka "github.com/segmentio/kafka-go"
)

// LoadAwareBalancer는 kafka.GroupBalancer를 구현합니다 — Kafka가 그룹의 리더로
// 뽑은 인스턴스 하나만 AssignGroups를 실제로 호출하고, 그 결과가 SyncGroup을 통해
// 그룹 전체에 전파됩니다(다른 멤버는 이 메서드를 실행조차 안 함 — kafka-go 컨슈머
// 그룹 프로토콜 자체의 동작이라, Assign이 결정적이어야 하는 이유이기도 함:
// 리더가 바뀌어도 같은 입력엔 같은 결과가 나와야 예측 가능함).
//
// 파티션 인덱스는 orderapi/kafkaclient의 marketPartitioner와 정확히 같은 순서
// (markets 슬라이스, matching/market.go의 TargetMarkets)로 마켓명과 대응됩니다.
type LoadAwareBalancer struct {
	tracker *LoadTracker
	markets []string // 인덱스 = 파티션 번호
}

func NewLoadAwareBalancer(tracker *LoadTracker, markets []string) *LoadAwareBalancer {
	return &LoadAwareBalancer{tracker: tracker, markets: markets}
}

func (b *LoadAwareBalancer) ProtocolName() string      { return "load-aware" }
func (b *LoadAwareBalancer) UserData() ([]byte, error) { return nil, nil }

// AssignGroups는 kafka.GroupBalancer를 만족합니다.
func (b *LoadAwareBalancer) AssignGroups(members []kafka.GroupMember, partitions []kafka.Partition) kafka.GroupMemberAssignments {
	ctx := context.Background()

	loads, err := b.tracker.Loads(ctx)
	if err != nil {
		// 측정에 실패해도 배정 자체는 계속 진행해야 합니다 — 여기서 멈추면 리밸런스가
		// 안 끝나서 그룹 전체가 멈춥니다. 전부 부하 0으로 두면 Assign이 개수 기준
		// 균등 분배로 대체하는 것과 같아집니다(측정 실패 시의 안전한 폴백).
		log.Printf("[rebalance] 부하 측정 실패, 균등 분배로 대체: %v", err)
		loads = make([]Load, len(b.markets))
		for i, m := range b.markets {
			loads[i] = Load{Market: m, Value: 0}
		}
	}

	memberIDs := make([]string, len(members))
	for i, m := range members {
		memberIDs[i] = m.ID
	}

	assignment := Assign(loads, memberIDs)
	log.Printf("[rebalance] 새 배정 계산 (멤버 %d명, 마켓 %d개) -> %+v", len(members), len(b.markets), assignment)

	partitionIndex := make(map[string]int, len(b.markets))
	for i, m := range b.markets {
		partitionIndex[m] = i
	}

	topic := ""
	if len(partitions) > 0 {
		topic = partitions[0].Topic
	}

	result := make(kafka.GroupMemberAssignments, len(members))
	for _, m := range members {
		result[m.ID] = make(map[string][]int)
	}
	for market, memberID := range assignment {
		idx, ok := partitionIndex[market]
		if !ok {
			continue // 대상 20개 마켓이 아닌 파티션 — 있을 수 없지만 안전하게 무시
		}
		result[memberID][topic] = append(result[memberID][topic], idx)
	}
	return result
}
