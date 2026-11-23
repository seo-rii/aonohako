package durante

import (
	"math"
	"testing"
)

func TestWeightedWorkClockStrictCrossing(t *testing.T) {
	policy := TimePolicy{Method: TimeMethodREPBreakdown, InstructionNSNumerator: 1, MOVSByteNSNumerator: 2, STOSByteNSNumerator: 5, NSDenominator: 1}
	plan := WorkClockRequest{Version: 1, Budget: 39, TimePolicy: &policy}
	progress := WorkClockProgress{Instructions: 3, MOVSBytes: 8, STOSBytes: 4}
	if plan.Exceeds(progress) {
		t.Fatal("equality crossed the time limit")
	}
	plan.Budget--
	if !plan.Exceeds(progress) {
		t.Fatal("missed one-unit crossing")
	}
	policy.NSDenominator = math.MaxUint64
	progress = WorkClockProgress{Instructions: math.MaxUint64, OverflowMask: 1}
	plan.Budget = 2
	if plan.Exceeds(progress) {
		t.Fatal("saturated lower bound is insufficient to prove the limit exceeded")
	}
	plan.Budget = 1
	if !plan.Exceeds(progress) {
		t.Fatal("missed a one-unit crossing above the 64-bit boundary")
	}
	binding := plan.ModelBinding()
	policy.runtimeOptionsID = "different launcher"
	if plan.ModelBinding() == binding {
		t.Fatal("model binding did not include effective runtime options")
	}
}
