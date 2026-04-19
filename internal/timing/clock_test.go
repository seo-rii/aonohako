package timing

import "testing"

func TestMonotonicNowIsNonDecreasing(t *testing.T) {
	a := MonotonicNow()
	b := MonotonicNow()
	if b < a {
		t.Fatalf("monotonic clock went backwards: a=%d b=%d", a, b)
	}
}

func TestCurrentProcessCPUTimeIncreasesUnderLoad(t *testing.T) {
	before, err := CurrentProcessCPUTimeNs()
	if err != nil {
		t.Fatalf("CurrentProcessCPUTimeNs(before): %v", err)
	}
	var total uint64
	for i := 0; i < 5_000_000; i++ {
		total += uint64(i)
	}
	after, err := CurrentProcessCPUTimeNs()
	if err != nil {
		t.Fatalf("CurrentProcessCPUTimeNs(after): %v", err)
	}
	if total == 0 {
		t.Fatalf("unexpected zero total")
	}
	if after <= before {
		t.Fatalf("expected cpu time to increase: before=%d after=%d", before, after)
	}
}
