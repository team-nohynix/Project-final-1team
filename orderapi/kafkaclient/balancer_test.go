package kafkaclient

import (
	"testing"

	kafka "github.com/segmentio/kafka-go"

	"orderapi/validate"
)

func TestMarketPartitionerAssignsIndexBijection(t *testing.T) {
	partitions := make([]int, len(validate.TargetMarkets))
	for i := range partitions {
		partitions[i] = i
	}

	seen := make(map[int]string)
	b := marketPartitioner{}
	for i, market := range validate.TargetMarkets {
		p := b.Balance(kafka.Message{Key: []byte(market)}, partitions...)
		if p != i {
			t.Errorf("market %s -> partition %d, want %d (TargetMarkets 인덱스)", market, p, i)
		}
		if other, ok := seen[p]; ok {
			t.Errorf("파티션 %d가 %s와 %s 둘 다에 배정됨 — 1마켓=1파티션 깨짐", p, other, market)
		}
		seen[p] = market
	}
}

func TestMarketPartitionerStableAcrossCalls(t *testing.T) {
	partitions := make([]int, len(validate.TargetMarkets))
	for i := range partitions {
		partitions[i] = i
	}
	b := marketPartitioner{}

	for _, market := range validate.TargetMarkets {
		first := b.Balance(kafka.Message{Key: []byte(market)}, partitions...)
		second := b.Balance(kafka.Message{Key: []byte(market)}, partitions...)
		if first != second {
			t.Errorf("market %s가 호출마다 다른 파티션(%d, %d)을 받음", market, first, second)
		}
	}
}
