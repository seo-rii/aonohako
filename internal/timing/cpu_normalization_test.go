package timing

import (
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
)

func TestCPUNormalizerNanosecondsPreserveFractionalMilliseconds(t *testing.T) {
	normalizer, err := NewCPUNormalizer("test-v1", 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		raw, want uint64
	}{
		{raw: 0, want: 0},
		{raw: 1, want: 2},
		{raw: 666_666, want: 999_999},
		{raw: 666_667, want: 1_000_001},
		{raw: 1_999_999, want: 2_999_999},
	} {
		if got := normalizer.NormalizeNanoseconds(tc.raw); got != tc.want {
			t.Errorf("NormalizeNanoseconds(%d) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestCPUNormalizerNanosecondForwardInverseBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		reference uint64
		observed  uint64
		limitMs   int
		wantRawNs uint64
	}{
		{name: "identity", reference: 100, observed: 100, limitMs: 1, wantRawNs: 1_000_000},
		{name: "slow host", reference: 100, observed: 175, limitMs: 1, wantRawNs: 1_750_000},
		{name: "fast host", reference: 100, observed: 60, limitMs: 1, wantRawNs: 600_000},
		{name: "fractional floor", reference: 3, observed: 2, limitMs: 5, wantRawNs: 3_333_333},
		{name: "sub nanosecond allowance", reference: 2_000_000, observed: 1, limitMs: 1, wantRawNs: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalizer, err := NewCPUNormalizer("test-v1", tc.reference, tc.observed)
			if err != nil {
				t.Fatal(err)
			}
			rawLimit := normalizer.RawLimitNanoseconds(tc.limitMs)
			if rawLimit != tc.wantRawNs {
				t.Fatalf("RawLimitNanoseconds(%d) = %d, want %d", tc.limitMs, rawLimit, tc.wantRawNs)
			}
			limitNs := uint64(tc.limitMs) * 1_000_000
			if got := normalizer.NormalizeNanoseconds(rawLimit); got > limitNs {
				t.Errorf("normalized raw limit = %d, exceeds %d", got, limitNs)
			}
			if got := normalizer.NormalizeNanoseconds(rawLimit + 1); got <= limitNs {
				t.Errorf("normalized first over-limit raw time = %d, want > %d", got, limitNs)
			}
		})
	}
}

func TestDisabledCPUNormalizerNanoseconds(t *testing.T) {
	var normalizer CPUNormalizer
	for _, raw := range []uint64{0, 1, 123_456_789, math.MaxUint64} {
		if got := normalizer.NormalizeNanoseconds(raw); got != raw {
			t.Errorf("NormalizeNanoseconds(%d) = %d, want identity", raw, got)
		}
	}
	for _, tc := range []struct {
		limit int
		want  uint64
	}{
		{limit: -1, want: 0},
		{limit: 0, want: 0},
		{limit: 123, want: 123_000_000},
	} {
		if got := normalizer.RawLimitNanoseconds(tc.limit); got != tc.want {
			t.Errorf("RawLimitNanoseconds(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
	normalizer, err := NewCPUNormalizer("test-v1", 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{-1, 0} {
		if got := normalizer.RawLimitNanoseconds(limit); got != 0 {
			t.Errorf("enabled RawLimitNanoseconds(%d) = %d, want 0", limit, got)
		}
	}
}

func TestCPUNormalizerNanosecondsOverflowMatchesExactArithmetic(t *testing.T) {
	// Compare with arbitrary-precision arithmetic, including an overflowing
	// intermediate product whose final quotient still fits in uint64.
	for _, ratio := range []struct{ reference, observed uint64 }{
		{1, 1},
		{1, math.MaxUint64},
		{math.MaxUint64, 1},
		{math.MaxUint64, math.MaxUint64},
		{1_000_000, math.MaxUint64},
		{999_999, math.MaxUint64},
		{math.MaxUint64, math.MaxUint64 - 1},
	} {
		normalizer, err := NewCPUNormalizer("test-v1", ratio.reference, ratio.observed)
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range []uint64{0, 1, 1_000_001, math.MaxUint64 - 1, math.MaxUint64} {
			product := new(big.Int).Mul(new(big.Int).SetUint64(raw), new(big.Int).SetUint64(ratio.reference))
			product.Add(product, new(big.Int).SetUint64(ratio.observed-1))
			product.Quo(product, new(big.Int).SetUint64(ratio.observed))
			want := uint64(math.MaxUint64)
			if product.IsUint64() {
				want = product.Uint64()
			}
			if got := normalizer.NormalizeNanoseconds(raw); got != want {
				t.Errorf("NormalizeNanoseconds(%d) reference=%d observed=%d: got %d, want %d", raw, ratio.reference, ratio.observed, got, want)
			}
		}
		for _, limit := range []int{1, 2, 1_000_001, math.MaxInt} {
			product := new(big.Int).Mul(big.NewInt(int64(limit)), big.NewInt(1_000_000))
			product.Mul(product, new(big.Int).SetUint64(ratio.observed))
			product.Quo(product, new(big.Int).SetUint64(ratio.reference))
			want := uint64(math.MaxUint64)
			if product.IsUint64() {
				want = product.Uint64()
			}
			if got := normalizer.RawLimitNanoseconds(limit); got != want {
				t.Errorf("RawLimitNanoseconds(%d) reference=%d observed=%d: got %d, want %d", limit, ratio.reference, ratio.observed, got, want)
			}
		}
	}
}

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

func TestMaximumCPUWallLimitMillis(t *testing.T) {
	for _, tc := range []struct {
		limit int
		want  int
	}{
		{limit: -1, want: -1},
		{limit: 0, want: 0},
		{limit: 1, want: 104},
		{limit: 25, want: 200},
		{limit: 251, want: 1105},
		{limit: 60_000, want: 264_000},
		{limit: 600_000, want: 2_640_000},
		{limit: math.MaxInt, want: math.MaxInt},
	} {
		if got := MaximumCPUWallLimitMillis(tc.limit); got != tc.want {
			t.Errorf("MaximumCPUWallLimitMillis(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}

func TestMaximumCPUWallLimitCoversSupportedCalibrations(t *testing.T) {
	for _, observed := range []uint64{25, 50, 100, 175, 400} {
		normalizer, err := NewCPUNormalizer("test-v1", 100, observed)
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{1, 25, 251, 1_000, 60_000, 600_000} {
			if bound, actual := MaximumCPUWallLimitMillis(limit), normalizer.WallLimitMillis(limit); bound < actual {
				t.Errorf("limit=%d observed=%d: maximum wall allowance %d is below actual %d", limit, observed, bound, actual)
			}
		}
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

func TestCalibrationStabilityUsesCentralThreeSamples(t *testing.T) {
	tests := []struct {
		name    string
		samples []uint64
		median  uint64
		wantErr bool
	}{
		{
			name:    "single high outlier is ignored",
			samples: []uint64{100, 102, 500, 95, 90},
			median:  100,
		},
		{
			name:    "ten percent boundary is accepted",
			samples: []uint64{100, 105, 1_000, 1, 95},
			median:  100,
		},
		{
			name:    "central spread above ten percent is rejected",
			samples: []uint64{100, 105, 1_000, 1, 94},
			median:  100,
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCalibrationStability(tc.samples, tc.median)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateCalibrationStability(%v, %d) error = %v, wantErr %v", tc.samples, tc.median, err, tc.wantErr)
			}
		})
	}
}

func TestCalibrationStabilityErrorIncludesDiagnostics(t *testing.T) {
	err := validateCalibrationStability([]uint64{1, 94, 100, 105, 1_000}, 100)
	if err == nil {
		t.Fatal("validateCalibrationStability unexpectedly succeeded")
	}
	message := err.Error()
	for _, want := range []string{
		"samples_ns=[1 94 100 105 1000]",
		"median_ns=100",
		"central_spread_ns=11",
		"central_spread_ppm=110000",
		"max_spread_ppm=100000",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not contain %q", message, want)
		}
	}
}

func TestCalibrationStabilityRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		samples []uint64
		median  uint64
	}{
		{samples: nil, median: 1},
		{samples: []uint64{1, 2}, median: 1},
		{samples: []uint64{1, 2, 3}, median: 0},
	} {
		if err := validateCalibrationStability(tc.samples, tc.median); err == nil {
			t.Fatalf("validateCalibrationStability(%v, %d) unexpectedly succeeded", tc.samples, tc.median)
		}
	}
}

func TestCalibrationMedianWithRetriesRecoversFromTransientInstability(t *testing.T) {
	attempts := [][]uint64{
		{1, 94, 100, 105, 1_000},
		{90, 95, 100, 102, 500},
	}
	collected := 0
	var unstableAttempts []int
	median, err := calibrationMedianWithRetries(5, func() ([]uint64, error) {
		samples := attempts[collected]
		collected++
		return samples, nil
	}, func(attempt int, _ error) {
		unstableAttempts = append(unstableAttempts, attempt)
	})
	if err != nil {
		t.Fatalf("calibrationMedianWithRetries: %v", err)
	}
	if median != 100 || collected != 2 {
		t.Fatalf("result = (median %d, attempts %d), want (100, 2)", median, collected)
	}
	if len(unstableAttempts) != 1 || unstableAttempts[0] != 1 {
		t.Fatalf("unstable attempts = %v, want [1]", unstableAttempts)
	}
}

func TestCalibrationMedianWithRetriesFailsAfterAllAttemptsAreUnstable(t *testing.T) {
	const maxAttempts = 5
	collected := 0
	median, err := calibrationMedianWithRetries(maxAttempts, func() ([]uint64, error) {
		collected++
		return []uint64{1, 94, 100, 105, uint64(1_000 + collected)}, nil
	}, nil)
	if err == nil {
		t.Fatal("calibrationMedianWithRetries unexpectedly succeeded")
	}
	if median != 0 || collected != maxAttempts {
		t.Fatalf("result = (median %d, attempts %d), want (0, %d)", median, collected, maxAttempts)
	}
	message := err.Error()
	if !strings.Contains(message, "remained unstable after 5 attempts") || !strings.Contains(message, "attempt 1:") || !strings.Contains(message, "attempt 5:") {
		t.Fatalf("error lacks retry diagnostics: %q", message)
	}
}

func TestCalibrationMedianWithRetriesDoesNotRetryCollectorErrors(t *testing.T) {
	wantErr := errors.New("clock unavailable")
	collected := 0
	_, err := calibrationMedianWithRetries(5, func() ([]uint64, error) {
		collected++
		return nil, wantErr
	}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want wrapped %v", err, wantErr)
	}
	if collected != 1 {
		t.Fatalf("collector calls = %d, want 1", collected)
	}
}

func TestCalibrationMedianWithRetriesRejectsInvalidConfiguration(t *testing.T) {
	if _, err := calibrationMedianWithRetries(0, func() ([]uint64, error) { return nil, nil }, nil); err == nil {
		t.Fatal("zero attempts unexpectedly succeeded")
	}
	if _, err := calibrationMedianWithRetries(1, nil, nil); err == nil {
		t.Fatal("nil collector unexpectedly succeeded")
	}
}
