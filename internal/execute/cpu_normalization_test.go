package execute

import (
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/timing"
)

func testCPUNormalizer(t *testing.T, reference, observed uint64) timing.CPUNormalizer {
	t.Helper()
	normalizer, err := timing.NewCPUNormalizer("test-v1", reference, observed)
	if err != nil {
		t.Fatalf("NewCPUNormalizer: %v", err)
	}
	return normalizer
}

func TestNormalizeExecResultCPUPreservesRawTime(t *testing.T) {
	normalizer := testCPUNormalizer(t, 60, 100)
	result := execResult{Status: "OK", CPUTimeMs: 50, ProcessCPUTimeMs: 55}
	normalizeExecResultCPU(&result, normalizer)

	if result.CPUTimeMs != 30 {
		t.Fatalf("normalized CPU time = %d, want 30", result.CPUTimeMs)
	}
	if result.RawCPUTimeMs == nil || *result.RawCPUTimeMs != 50 {
		t.Fatalf("raw CPU time = %v, want 50", result.RawCPUTimeMs)
	}
	if result.ProcessCPUTimeMs != 55 {
		t.Fatalf("process CPU time = %d, want raw diagnostic 55", result.ProcessCPUTimeMs)
	}
}

func TestNormalizeExecResultCPURoundsOnlyAfterNanosecondScaling(t *testing.T) {
	result := execResult{CPUTimeMs: 8, CPUTimeNs: 7_100_001, CPUAccounting: &model.CPUAccounting{}}
	normalizeExecResultCPU(&result, testCPUNormalizer(t, 3, 2))
	if result.CPUTimeNs != 10_650_002 || result.CPUTimeMs != 11 {
		t.Fatalf("normalized result = %+v; premature ms rounding would report 12 ms", result)
	}
	if result.RawCPUTimeNs == nil || *result.RawCPUTimeNs != 7_100_001 || result.RawCPUTimeMs == nil || *result.RawCPUTimeMs != 8 {
		t.Fatalf("raw accounting was not retained: %+v", result)
	}
}

func TestFinalCPUTimeStatusNanosecondBoundary(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		cpu, limit uint64
		want       string
	}{
		{"exact", "OK", 1_000_000, 1_000_000, "OK"},
		{"one ns over", "OK", 1_000_001, 1_000_000, model.RunStatusTLE},
		{"sub-ms limit", model.RunStatusAccepted, 999_001, 999_000, model.RunStatusTLE},
		{"zero rounded raw allowance", "OK", 1, 0, model.RunStatusTLE},
		{"preserve failure", model.RunStatusRE, 2_000_000, 1_000_000, model.RunStatusRE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, _ := applyFinalCPUTimeStatusNs(tc.status, "", "", tc.cpu, tc.limit, false)
			if status != tc.want {
				t.Fatalf("status = %q, want %q", status, tc.want)
			}
		})
	}
}

func TestFinalizeWaitCPUTimeRequiresValidFinalAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, status, wantStatus       string
		usage, baseline, poll, wantCPU uint64
		available                      bool
		exitCode                       int
	}{
		{"final tail", "OK", "OK", 8_500_001, 3_000_000, 4_000_000, 5_500_001, true, 0},
		{"matching zero", "OK", "OK", 3_000_000, 3_000_000, 0, 0, true, 0},
		{"underflow without poll", "OK", model.RunStatusInitFail, 2_000_000, 3_000_000, 0, 0, true, 0},
		{"underflow with poll", "OK", model.RunStatusInitFail, 2_000_000, 3_000_000, 1_000_000, 1_000_000, true, 0},
		{"missing wait", "OK", model.RunStatusInitFail, 0, 0, 1_000_000, 1_000_000, false, 0},
		{"preserve TLE", model.RunStatusTLE, model.RunStatusTLE, 2_000_000, 3_000_000, 1_000_000, 1_000_000, true, 0},
		{"preserve nonzero exit classification", "OK", "OK", 2_000_000, 3_000_000, 1_000_000, 1_000_000, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := execResult{Status: tc.status, ExitCode: &tc.exitCode, CPUTimeNs: tc.poll, CPUAccounting: &model.CPUAccounting{RusageBaselineNs: tc.baseline}}
			finalizeWaitCPUTime(&result, tc.usage, tc.available)
			if result.Status != tc.wantStatus || result.CPUTimeNs != tc.wantCPU {
				t.Fatalf("result = %+v", result)
			}
			if tc.wantStatus == model.RunStatusInitFail && result.VerdictSource != "cpu_accounting" {
				t.Fatalf("unclassified bad accounting: %+v", result)
			}
		})
	}
}

func TestNormalizeExecResultCPUPreservesMeasuredZero(t *testing.T) {
	result := execResult{Status: "OK"}
	normalizeExecResultCPU(&result, testCPUNormalizer(t, 60, 100))
	if result.CPUTimeMs != 0 || result.RawCPUTimeMs == nil || *result.RawCPUTimeMs != 0 {
		t.Fatalf("normalized zero result = %+v", result)
	}
}

func TestNormalizeExecResultCPUDisabledLeavesLegacyShape(t *testing.T) {
	result := execResult{Status: "OK", CPUTimeMs: 50}
	normalizeExecResultCPU(&result, timing.CPUNormalizer{})
	if result.CPUTimeMs != 50 || result.RawCPUTimeMs != nil {
		t.Fatalf("disabled normalization changed result: %+v", result)
	}
}

func TestServiceDecoratesNormalizationMetadata(t *testing.T) {
	service := &Service{cpuNormalizer: testCPUNormalizer(t, 60, 100)}
	response := service.decorateCPUTimeNormalization(model.RunResponse{Status: model.RunStatusAccepted})
	if response.CPUTimeNormalization == nil {
		t.Fatal("normalization metadata is missing")
	}
	if got := response.CPUTimeNormalization; got.Method != "test-v1" || got.ScalePPM != 600_000 || got.ReferenceTimeNs != 60 || got.ObservedTimeNs != 100 {
		t.Fatalf("normalization metadata = %+v", got)
	}

	legacy := (&Service{}).decorateCPUTimeNormalization(model.RunResponse{Status: model.RunStatusAccepted})
	if legacy.CPUTimeNormalization != nil {
		t.Fatalf("disabled service exposed normalization metadata: %+v", legacy.CPUTimeNormalization)
	}
}

func TestRawCPUTimeAggregation(t *testing.T) {
	a, b := int64(20), int64(30)
	got := sumRawCPUTime(&a, nil, &b)
	if got == nil || *got != 50 {
		t.Fatalf("sumRawCPUTime = %v, want 50", got)
	}
	if got := sumRawCPUTime(nil, nil); got != nil {
		t.Fatalf("sumRawCPUTime(nil) = %v, want nil", got)
	}
}

func TestAggregateStepResponsePreservesNormalizedAndRawTotals(t *testing.T) {
	rawA, rawB := int64(50), int64(80)
	lastRawNs := uint64(80_000_000)
	response := aggregateStepResponse(model.RunResponse{Status: model.RunStatusAccepted, CPUTimeNs: 48_000_000, RawCPUTimeNs: &lastRawNs, CPUAccounting: &model.CPUAccounting{}}, []model.StepResult{
		{CPUTimeMs: 30, RawCPUTimeMs: &rawA},
		{CPUTimeMs: 48, RawCPUTimeMs: &rawB},
	})
	if response.CPUTimeMs != 78 {
		t.Fatalf("normalized aggregate CPU time = %d, want 78", response.CPUTimeMs)
	}
	if response.RawCPUTimeMs == nil || *response.RawCPUTimeMs != 130 {
		t.Fatalf("raw aggregate CPU time = %v, want 130", response.RawCPUTimeMs)
	}
	if response.CPUTimeNs != 0 || response.RawCPUTimeNs != nil || response.CPUAccounting != nil {
		t.Fatalf("aggregate response mislabeled last-stage accounting: %+v", response)
	}
}
