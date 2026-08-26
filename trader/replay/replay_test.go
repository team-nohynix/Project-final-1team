package replay

import (
	"testing"

	"trader/client"
)

func TestFilterEventRangeNoLimitsReturnsAll(t *testing.T) {
	events := []client.StreamEvent{{TS: 1}, {TS: 2}, {TS: 3}}
	got := filterEventRange(events, 0, 0)
	if len(got) != 3 {
		t.Errorf("len(got) = %d, want 3", len(got))
	}
}

func TestFilterEventRangeAppliesFromAndTo(t *testing.T) {
	events := []client.StreamEvent{{TS: 100}, {TS: 200}, {TS: 300}, {TS: 400}}
	got := filterEventRange(events, 150, 350)
	if len(got) != 2 || got[0].TS != 200 || got[1].TS != 300 {
		t.Errorf("got = %+v", got)
	}
}

func TestFilterEventRangeOnlyFrom(t *testing.T) {
	events := []client.StreamEvent{{TS: 100}, {TS: 200}, {TS: 300}}
	got := filterEventRange(events, 200, 0)
	if len(got) != 2 || got[0].TS != 200 || got[1].TS != 300 {
		t.Errorf("got = %+v", got)
	}
}

func TestFilterEventRangeOnlyTo(t *testing.T) {
	events := []client.StreamEvent{{TS: 100}, {TS: 200}, {TS: 300}}
	got := filterEventRange(events, 0, 200)
	if len(got) != 2 || got[0].TS != 100 || got[1].TS != 200 {
		t.Errorf("got = %+v", got)
	}
}
