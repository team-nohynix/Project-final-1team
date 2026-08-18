package backpressure

import (
	"testing"
)

func TestNextStateTurnsOnAtHighWatermark(t *testing.T) {
	w := &Watcher{HighWatermark: 5000, LowWatermark: 1000}
	if got := w.nextState(false, 4999); got {
		t.Errorf("HighWatermark 미달인데 켜짐: got %v", got)
	}
	if got := w.nextState(false, 5000); !got {
		t.Errorf("HighWatermark 도달인데 안 켜짐: got %v", got)
	}
}

func TestNextStateStaysOnInHysteresisBand(t *testing.T) {
	w := &Watcher{HighWatermark: 5000, LowWatermark: 1000}
	// 켜진 상태에서 LowWatermark보다는 높지만 HighWatermark보다는 낮은 구간 —
	// 계속 켜진 채로 유지돼야 함(그래야 임계값 근처에서 깜빡이지 않음).
	if got := w.nextState(true, 3000); !got {
		t.Errorf("히스테리시스 구간에서 꺼짐: got %v", got)
	}
}

func TestNextStateTurnsOffAtLowWatermark(t *testing.T) {
	w := &Watcher{HighWatermark: 5000, LowWatermark: 1000}
	if got := w.nextState(true, 1001); !got {
		t.Errorf("LowWatermark 미달인데 벌써 꺼짐: got %v", got)
	}
	if got := w.nextState(true, 1000); got {
		t.Errorf("LowWatermark 도달인데 안 꺼짐: got %v", got)
	}
}

func TestNextStateOffStaysOffBelowHighWatermark(t *testing.T) {
	w := &Watcher{HighWatermark: 5000, LowWatermark: 1000}
	if got := w.nextState(false, 2000); got {
		t.Errorf("꺼진 상태에서 HighWatermark 미달인데 켜짐: got %v", got)
	}
}

func TestMaxLagPicksLargest(t *testing.T) {
	w := &Watcher{Sources: []LagSource{
		func() int64 { return 100 },
		func() int64 { return 9000 },
		func() int64 { return 500 },
	}}
	if got := w.maxLag(); got != 9000 {
		t.Errorf("maxLag() = %d, want 9000", got)
	}
}

func TestMaxLagNoSourcesIsZero(t *testing.T) {
	w := &Watcher{}
	if got := w.maxLag(); got != 0 {
		t.Errorf("maxLag() = %d, want 0", got)
	}
}
