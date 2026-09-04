package timing

import (
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strings"
)

const (
	cpuNormalizationScaleUnit    = uint64(1_000_000)
	cpuNormalizationWallSlackPct = uint64(10)
	cpuNormalizationMinWallSlack = 100
	cpuCalibrationMaxSpreadPPM   = uint64(100_000)
	CPUNormalizationMethod       = "go-fixed-int-v1"
	DefaultCPUReferenceTimeNs    = uint64(60_000_000)
	MinimumCPUCalibrationTimeNs  = uint64(1_000_000)
	MaximumCPUCalibrationTimeNs  = uint64(5_000_000_000)
)

// CPUNormalizer translates scheduled CPU milliseconds on one host into a
// versioned fixed-work reference. The zero value is an identity mapping.
type CPUNormalizer struct {
	method          string
	referenceTimeNs uint64
	observedTimeNs  uint64
}

type CPUNormalizationInfo struct {
	Method          string
	ScalePPM        int64
	ReferenceTimeNs uint64
	ObservedTimeNs  uint64
}

func NewCPUNormalizer(method string, referenceTimeNs, observedTimeNs uint64) (CPUNormalizer, error) {
	if strings.TrimSpace(method) == "" {
		return CPUNormalizer{}, fmt.Errorf("CPU normalization method is required")
	}
	if referenceTimeNs == 0 {
		return CPUNormalizer{}, fmt.Errorf("CPU normalization reference time must be positive")
	}
	if observedTimeNs == 0 {
		return CPUNormalizer{}, fmt.Errorf("CPU normalization observed time must be positive")
	}
	return CPUNormalizer{
		method:          method,
		referenceTimeNs: referenceTimeNs,
		observedTimeNs:  observedTimeNs,
	}, nil
}

func (n CPUNormalizer) Enabled() bool {
	return n.method != "" && n.referenceTimeNs > 0 && n.observedTimeNs > 0
}

// NormalizeMillis rounds up so the returned integer and RawLimitMillis share
// an exact strict-over-limit boundary:
//
//	NormalizeMillis(raw) <= limit  iff  raw <= RawLimitMillis(limit)
func (n CPUNormalizer) NormalizeMillis(rawTimeMs int64) int64 {
	if rawTimeMs <= 0 {
		return 0
	}
	if !n.Enabled() {
		return rawTimeMs
	}
	return mulDivCeilInt64(rawTimeMs, n.referenceTimeNs, n.observedTimeNs)
}

func (n CPUNormalizer) RawLimitMillis(normalizedLimitMs int) int {
	if normalizedLimitMs <= 0 {
		return normalizedLimitMs
	}
	if !n.Enabled() {
		return normalizedLimitMs
	}
	raw := mulDivFloorUint64(uint64(normalizedLimitMs), n.observedTimeNs, n.referenceTimeNs)
	if raw == 0 {
		return 1
	}
	maxInt := uint64(^uint(0) >> 1)
	if raw > maxInt {
		return int(maxInt)
	}
	return int(raw)
}

// WallLimitMillis is a supervision guardrail, not the reported wall time. It
// never shortens the public limit and leaves enough slack for a CPU-bound
// target to consume its normalized CPU allowance on a slower host.
func (n CPUNormalizer) WallLimitMillis(normalizedLimitMs int) int {
	if normalizedLimitMs <= 0 || !n.Enabled() {
		return normalizedLimitMs
	}
	base := max(normalizedLimitMs, n.RawLimitMillis(normalizedLimitMs))
	base64 := uint64(base)
	percentageSlack := mulDivCeilUint64(base64, cpuNormalizationWallSlackPct, 100)
	slack := max(uint64(cpuNormalizationMinWallSlack), percentageSlack)
	maxInt := uint64(^uint(0) >> 1)
	if base64 > maxInt-slack {
		return int(maxInt)
	}
	return int(base64 + slack)
}

func (n CPUNormalizer) Info() (CPUNormalizationInfo, bool) {
	if !n.Enabled() {
		return CPUNormalizationInfo{}, false
	}
	scale := mulDivCeilUint64(n.referenceTimeNs, cpuNormalizationScaleUnit, n.observedTimeNs)
	if scale > math.MaxInt64 {
		scale = math.MaxInt64
	}
	return CPUNormalizationInfo{
		Method:          n.method,
		ScalePPM:        int64(scale),
		ReferenceTimeNs: n.referenceTimeNs,
		ObservedTimeNs:  n.observedTimeNs,
	}, true
}

func calibrationMedian(samples []uint64) (uint64, error) {
	if len(samples) == 0 || len(samples)%2 == 0 {
		return 0, fmt.Errorf("CPU calibration requires a non-empty odd sample count")
	}
	ordered := slices.Clone(samples)
	for _, sample := range ordered {
		if sample == 0 {
			return 0, fmt.Errorf("CPU calibration sample must be positive")
		}
	}
	slices.Sort(ordered)
	return ordered[len(ordered)/2], nil
}

func validateCalibrationStability(samples []uint64, median uint64) error {
	if len(samples) < 3 || len(samples)%2 == 0 {
		return fmt.Errorf("CPU calibration stability requires at least three samples and an odd sample count")
	}
	if median == 0 {
		return fmt.Errorf("CPU calibration stability requires a positive median")
	}

	ordered := slices.Clone(samples)
	slices.Sort(ordered)
	middle := len(ordered) / 2
	centralSpreadNs := ordered[middle+1] - ordered[middle-1]
	centralSpreadPPM := mulDivCeilUint64(centralSpreadNs, cpuNormalizationScaleUnit, median)
	if centralSpreadPPM > cpuCalibrationMaxSpreadPPM {
		return fmt.Errorf(
			"CPU calibration samples are unstable: samples_ns=%v median_ns=%d central_spread_ns=%d central_spread_ppm=%d max_spread_ppm=%d",
			samples,
			median,
			centralSpreadNs,
			centralSpreadPPM,
			cpuCalibrationMaxSpreadPPM,
		)
	}
	return nil
}

func mulDivCeilInt64(value int64, multiplier, divisor uint64) int64 {
	if value <= 0 || multiplier == 0 {
		return 0
	}
	result := mulDivCeilUint64(uint64(value), multiplier, divisor)
	if result > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(result)
}

func mulDivCeilUint64(value, multiplier, divisor uint64) uint64 {
	quotient, remainder, overflow := mulDivUint64(value, multiplier, divisor)
	if overflow {
		return math.MaxUint64
	}
	if remainder == 0 {
		return quotient
	}
	if quotient == math.MaxUint64 {
		return math.MaxUint64
	}
	return quotient + 1
}

func mulDivFloorUint64(value, multiplier, divisor uint64) uint64 {
	quotient, _, overflow := mulDivUint64(value, multiplier, divisor)
	if overflow {
		return math.MaxUint64
	}
	return quotient
}

func mulDivUint64(value, multiplier, divisor uint64) (uint64, uint64, bool) {
	if divisor == 0 {
		return math.MaxUint64, 0, true
	}
	high, low := bits.Mul64(value, multiplier)
	if high >= divisor {
		return math.MaxUint64, 0, true
	}
	quotient, remainder := bits.Div64(high, low, divisor)
	return quotient, remainder, false
}
