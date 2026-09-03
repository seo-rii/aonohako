package sandbox

import (
	"testing"

	"aonohako/internal/model"
)

func TestCPUTimeLimitMillisPrefersNormalizedRawBudget(t *testing.T) {
	tests := []struct {
		name string
		req  ExecRequest
		want int
	}{
		{
			name: "explicit raw CPU budget",
			req: ExecRequest{
				Limits:         model.Limits{TimeMs: 100},
				CPUTimeLimitMs: 175,
			},
			want: 175,
		},
		{
			name: "legacy public budget",
			req:  ExecRequest{Limits: model.Limits{TimeMs: 100}},
			want: 100,
		},
		{
			name: "minimum",
			req:  ExecRequest{},
			want: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cpuTimeLimitMillis(tc.req); got != tc.want {
				t.Fatalf("cpuTimeLimitMillis() = %d, want %d", got, tc.want)
			}
		})
	}
}
