package jobtrigger

import "testing"

func floatPtr(v float64) *float64 { return &v }
func intPtr(v int) *int           { return &v }

func TestValidateRequest(t *testing.T) {
	cases := []struct {
		name     string
		req      Request
		wantOK   bool
		wantCode string
	}{
		{"valid ai-trader", Request{JobType: "ai-trader", Date: "2026-08-11"}, true, ""},
		{"valid replay with shard", Request{JobType: "replay", Date: "2026-08-11", ShardCount: intPtr(4)}, true, ""},
		{"missing jobType", Request{Date: "2026-08-11"}, false, "INVALID_JOB_TYPE"},
		{"unknown jobType", Request{JobType: "bogus", Date: "2026-08-11"}, false, "INVALID_JOB_TYPE"},
		{"bad date format", Request{JobType: "ai-trader", Date: "2026/08/11"}, false, "INVALID_DATE"},
		{"empty date", Request{JobType: "ai-trader", Date: ""}, false, "INVALID_DATE"},
		{"zero speed", Request{JobType: "ai-trader", Date: "2026-08-11", Speed: floatPtr(0)}, false, "INVALID_SPEED"},
		{"negative speed", Request{JobType: "ai-trader", Date: "2026-08-11", Speed: floatPtr(-1)}, false, "INVALID_SPEED"},
		{"positive speed ok", Request{JobType: "ai-trader", Date: "2026-08-11", Speed: floatPtr(60)}, true, ""},
		{"zero shard count", Request{JobType: "replay", Date: "2026-08-11", ShardCount: intPtr(0)}, false, "INVALID_SHARD_COUNT"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, _, ok := ValidateRequest(c.req)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !ok && code != c.wantCode {
				t.Fatalf("code = %q, want %q", code, c.wantCode)
			}
		})
	}
}
