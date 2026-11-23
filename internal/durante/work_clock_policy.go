package durante

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
)

func (r WorkClockRequest) MetricName() string {
	if r.TimePolicy == nil {
		return WorkClockV1
	}
	return "model_weighted_nanoseconds_v1"
}

// ModelBinding covers the resolved operator coefficients and effective launcher
// identity. Both ends compute it independently before an instrumented launch.
func (r WorkClockRequest) ModelBinding() string {
	p := r.TimePolicy
	if p == nil {
		return ""
	}
	binding := struct {
		Policy             TimePolicy
		RuntimeOptionsID   string
		RuntimeDeterminism string
	}{*p, p.runtimeOptionsID, p.runtimeDeterminism}
	data, _ := json.Marshal(binding) // Struct containing only strings and integers.
	sum := sha256.Sum256(data)
	return "work-clock-v1:sha256:" + hex.EncodeToString(sum[:])
}

// Exceeds compares the published lower bound to the budget without rounding.
// An overflowing component proves at least 2^64 units of that component. It
// need not exceed a weighted budget whose denominator can also be large.
func (r WorkClockRequest) Exceeds(progress WorkClockProgress) bool {
	p := r.TimePolicy
	if p == nil {
		return progress.Exceeds(r.Budget)
	}
	weights := [3]uint64{p.InstructionNSNumerator, p.REPByteNSNumerator, p.REPByteNSNumerator}
	if p.Formula() == TimeMethodREPBreakdown {
		weights[1], weights[2] = p.MOVSByteNSNumerator, p.STOSByteNSNumerator
	}
	var total, term, factor, limit big.Int
	for i, count := range [3]uint64{progress.Instructions, progress.MOVSBytes, progress.STOSBytes} {
		term.SetUint64(count)
		if progress.OverflowMask&(1<<i) != 0 {
			term.SetUint64(1)
			term.Lsh(&term, 64)
		}
		total.Add(&total, term.Mul(&term, factor.SetUint64(weights[i])))
	}
	limit.SetUint64(r.Budget)
	return total.Cmp(limit.Mul(&limit, factor.SetUint64(p.NSDenominator))) > 0
}
