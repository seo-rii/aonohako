package runvalidation

import (
	"strconv"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestCommunicationOutputBudgetBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes int
		valid bool
	}{
		{"default", 0, true},
		{"ordinary maximum", 64 << 20, true},
		{"above ordinary maximum", (64 << 20) + 1, true},
		{"communication maximum", 128 << 20, true},
		{"above communication maximum", (128 << 20) + 1, false},
		{"negative", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := validCommunicationRequest()
			req.Limits.OutputBytes = tc.bytes
			err := Validate(req)
			if tc.valid && err != nil {
				t.Fatalf("legal output budget rejected: %v", err)
			}
			if !tc.valid && (err == nil || !strings.Contains(err.Error(), "limits.output_bytes must be between 0 and 134217728")) {
				t.Fatalf("expected communication output limit rejection, got %v", err)
			}
		})
	}
}

func TestNonCommunicationOutputBudgetRemains64MiB(t *testing.T) {
	for _, mode := range []string{"ordinary", "legacy steps", "pipeline v1", "optional limits"} {
		for _, bytes := range []int{64 << 20, (64 << 20) + 1} {
			t.Run(mode+"/"+strconv.Itoa(bytes), func(t *testing.T) {
				var err error
				switch mode {
				case "ordinary":
					err = Validate(&model.RunRequest{Lang: "binary", Binaries: []model.Binary{{Name: "main", DataB64: "eA==", Mode: "exec"}}, Limits: model.Limits{TimeMs: 1000, MemoryMB: 64, OutputBytes: bytes}})
				case "legacy steps":
					req := validTwoStepPipelineRequest()
					req.Steps[0].Limits.OutputBytes = bytes
					err = Validate(req)
				case "pipeline v1":
					req := validPipelineRequest()
					req.Pipeline.Steps[0].Limits.OutputBytes = bytes
					err = Validate(req)
				case "optional limits":
					err = ValidateOptionalLimits("limits", model.Limits{OutputBytes: bytes})
				}
				if bytes == 64<<20 && err != nil {
					t.Fatalf("64 MiB output budget rejected: %v", err)
				}
				if bytes > 64<<20 && (err == nil || !strings.Contains(err.Error(), "output_bytes must be between 0 and 67108864")) {
					t.Fatalf("expected unchanged 64 MiB rejection, got %v", err)
				}
			})
		}
	}
}
