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
	response := aggregateStepResponse(model.RunResponse{Status: model.RunStatusAccepted}, []model.StepResult{
		{CPUTimeMs: 30, RawCPUTimeMs: &rawA},
		{CPUTimeMs: 48, RawCPUTimeMs: &rawB},
	})
	if response.CPUTimeMs != 78 {
		t.Fatalf("normalized aggregate CPU time = %d, want 78", response.CPUTimeMs)
	}
	if response.RawCPUTimeMs == nil || *response.RawCPUTimeMs != 130 {
		t.Fatalf("raw aggregate CPU time = %v, want 130", response.RawCPUTimeMs)
	}
}
