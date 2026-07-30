package runvalidation

import (
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestValidateCaptureLimits(t *testing.T) {
	zero := 0
	oneKiB := 1024
	maximum := MaxCaptureBytes
	negative := -1
	oversized := MaxCaptureBytes + 1
	for _, tc := range []struct {
		name   string
		limits *model.CaptureLimits
		want   string
	}{
		{name: "omitted"},
		{name: "empty", limits: &model.CaptureLimits{}},
		{
			name: "zero and one kibibyte",
			limits: &model.CaptureLimits{
				StdoutBytes: &zero,
				StderrBytes: &oneKiB,
			},
		},
		{
			name: "maximum",
			limits: &model.CaptureLimits{
				StdoutBytes: &maximum,
				StderrBytes: &maximum,
			},
		},
		{
			name:   "negative stdout",
			limits: &model.CaptureLimits{StdoutBytes: &negative},
			want:   "capture_limits.stdout_bytes",
		},
		{
			name:   "oversized stderr",
			limits: &model.CaptureLimits{StderrBytes: &oversized},
			want:   "capture_limits.stderr_bytes",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCaptureLimits(tc.limits)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateCaptureLimits() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateCaptureLimits() error = %v, want %q", err, tc.want)
			}
		})
	}
}
