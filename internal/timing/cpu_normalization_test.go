package timing

import (
	"math"
	"testing"
)

func TestCPUNormalizerForwardInverseBoundary(t *testing.T) {
	tests := []struct {
		name       string
		reference  uint64
		observed   uint64
		limit      int
		wantRawMax int
	}{
		{name: "identity", reference: 100, observed: 100, limit: 100, wantRawMax: 100},
		{name: "slow host", reference: 100, observed: 175, limit: 100, wantRawMax: 175},
		{name: "fast host", reference: 100, observed: 60, limit: 100, wantRawMax: 60},
		{name: "fractional floor", reference: 3, observed: 2, limit: 5, wantRawMax: 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			normalizer, err := NewCPUNormalizer("test-v1", tc.reference, tc.observed)
			if err != nil {
				t.Fatalf("NewCPUNormalizer: %v", err)
			}
			rawLimit := normalizer.RawLimitMillis(tc.limit)
			if rawLimit != tc.wantRawMax {
				t.Fatalf("RawLimitMillis(%d) = %d, want %d", tc.limit, rawLimit, tc.wantRawMax)
			}
			if got := normalizer.NormalizeMillis(int64(rawLimit)); got > int64(tc.limit) {
				t.Fatalf("normalized raw limit = %d, exceeds %d", got, tc.limit)
			}
			if got := normalizer.NormalizeMillis(int64(rawLimit + 1)); got <= int64(tc.limit) {
				t.Fatalf("normalized first over-limit raw time = %d, want > %d", got, tc.limit)
			}
		})
	}
}

func TestCPUNormalizerUsesCeilingForReportedMilliseconds(t *testing.T) {
	normalizer, err := NewCPUNormalizer("test-v1", 3, 2)
	if err != nil {
		t.Fatalf("NewCPUNormalizer: %v", err)
	}
	if got := normalizer.NormalizeMillis(1); got != 2 {
		t.Fatalf("NormalizeMillis(1) = %d, want 2", got)
	}
	if got := normalizer.NormalizeMillis(0); got != 0 {
		t.Fatalf("NormalizeMillis(0) = %d, want 0", got)
	}
}

func TestCPUNormalizerWallLimitNeverPreemptsCPUAllowance(t *testing.T) {
	normalizer, err := NewCPUNormalizer("test-v1", 100, 175)
	if err != nil {
		t.Fatalf("NewCPUNormalizer: %v", err)
	}
	if got := normalizer.WallLimitMillis(100); got < normalizer.RawLimitMillis(100) {
		t.Fatalf("WallLimitMillis(100) = %d, below raw CPU limit %d", got, normalizer.RawLimitMillis(100))
	}
	if got := normalizer.WallLimitMillis(100); got <= 175 {
		t.Fatalf("WallLimitMillis(100) = %d, want supervision slack above 175", got)
	}
}

func TestDisabledCPUNormalizerIsIdentity(t *testing.T) {
	var normalizer CPUNormalizer
	if normalizer.Enabled() {
		t.Fatal("zero normalizer must be disabled")
	}
	if got := normalizer.NormalizeMillis(123); got != 123 {
		t.Fatalf("NormalizeMillis(123) = %d", got)
	}
	if got := normalizer.RawLimitMillis(123); got != 123 {
		t.Fatalf("RawLimitMillis(123) = %d", got)
	}
	if got := normalizer.WallLimitMillis(123); got != 123 {
		t.Fatalf("WallLimitMillis(123) = %d", got)
	}
}

func TestCPUNormalizerRejectsInvalidCalibration(t *testing.T) {
	for _, tc := range []struct {
		method    string
		reference uint64
		observed  uint64
	}{
		{method: "", reference: 100, observed: 100},
		{method: "test-v1", reference: 0, observed: 100},
		{method: "test-v1", reference: 100, observed: 0},
	} {
		if _, err := NewCPUNormalizer(tc.method, tc.reference, tc.observed); err == nil {
			t.Fatalf("NewCPUNormalizer(%q, %d, %d) unexpectedly succeeded", tc.method, tc.reference, tc.observed)
		}
	}
}

func TestCPUNormalizerSaturatesOverflow(t *testing.T) {
	normalizer, err := NewCPUNormalizer("test-v1", math.MaxUint64, 1)
	if err != nil {
		t.Fatalf("NewCPUNormalizer: %v", err)
	}
	if got := normalizer.NormalizeMillis(math.MaxInt64); got != math.MaxInt64 {
		t.Fatalf("NormalizeMillis(max) = %d, want saturation", got)
	}

	normalizer, err = NewCPUNormalizer("test-v1", 1, math.MaxUint64)
	if err != nil {
		t.Fatalf("NewCPUNormalizer: %v", err)
	}
	if got := normalizer.RawLimitMillis(math.MaxInt); got != math.MaxInt {
		t.Fatalf("RawLimitMillis(max) = %d, want saturation", got)
	}
}

func TestCalibrationStatisticUsesMedianAndRejectsInvalidSamples(t *testing.T) {
	got, err := calibrationMedian([]uint64{90, 500, 100, 95, 110})
	if err != nil {
		t.Fatalf("calibrationMedian: %v", err)
	}
	if got != 100 {
		t.Fatalf("calibrationMedian = %d, want 100", got)
	}
	for _, samples := range [][]uint64{nil, {1, 2}, {0, 1, 2}} {
		if _, err := calibrationMedian(samples); err == nil {
			t.Fatalf("calibrationMedian(%v) unexpectedly succeeded", samples)
		}
	}
}
