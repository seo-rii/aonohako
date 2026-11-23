package reference

import (
	_ "embed"
	"fmt"
	"strconv"
)

// Each split has distinct programs. Larger inputs of a training program never
// become validation data, and smoke observations are not calibration samples.
//
//go:embed fixtures/integer.cpp
var integerSource string

//go:embed fixtures/rep_copy.cpp
var repCopySource string

//go:embed fixtures/branch.cpp
var branchSource string

//go:embed fixtures/sort.cpp
var sortSource string

//go:embed fixtures/rep_fill.cpp
var repFillSource string

const integerSeed uint64 = 0x0123456789abcdef
const branchSeed uint64 = 0xace1

// Corpus returns fresh manifests for a fixed x86-64 C++20 workload corpus.
// Full cases use scales 1, 2 and 4. Smoke cases retain the five algorithms with
// deliberately short inputs, solely to exercise collection and output checks.
func Corpus(smoke bool) []Workload {
	scales := []int{1, 2, 4}
	integerIterations, branchIterations := uint64(32<<20), uint64(16<<20)
	sortElements, repBytes, repIterations := uint64(512<<10), uint64(4<<20), uint64(512)
	if smoke {
		scales = []int{1}
		integerIterations, branchIterations = 256<<10, 1<<16
		sortElements, repBytes, repIterations = 4096, 256<<10, 32
	}
	workloads := []Workload{
		{ID: "integer", Split: SplitTrain, Source: integerSource},
		{ID: "rep_copy", Split: SplitTrain, Source: repCopySource},
		{ID: "branch", Split: SplitValidation, Source: branchSource},
		{ID: "sort", Split: SplitValidation, Source: sortSource},
		{ID: "rep_fill", Split: SplitValidation, Source: repFillSource},
	}
	for _, scale := range scales {
		n, branchN := integerIterations*uint64(scale), branchIterations*uint64(scale)
		elements, repeats := sortElements*uint64(scale), repIterations*uint64(scale)
		cases := []Case{
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", n, integerSeed), ExpectedStdout: checksumOutput(integerChecksum(n, integerSeed))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", repBytes, repeats), ExpectedStdout: checksumOutput(repCopyChecksum(repBytes, repeats))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", branchN, branchSeed), ExpectedStdout: checksumOutput(branchChecksum(branchN, branchSeed))},
			{Scale: scale, Input: fmt.Sprintf("%d\n", elements), ExpectedStdout: checksumOutput(sumSquares(elements))},
			{Scale: scale, Input: fmt.Sprintf("%d %d\n", repBytes, repeats), ExpectedStdout: checksumOutput(repFillChecksum(repBytes, repeats))},
		}
		for i := range workloads {
			workloads[i].Cases = append(workloads[i].Cases, cases[i])
		}
	}
	return workloads
}

func checksumOutput(checksum uint64) string {
	return strconv.FormatUint(checksum, 10) + "\n"
}

// Compose affine transformations modulo 2^64 instead of running the C++
// recurrence. Unsigned wraparound is part of both contracts.
func integerChecksum(iterations, seed uint64) uint64 {
	multiplier, increment := uint64(6364136223846793005), uint64(1442695040888963407)
	combinedMultiplier, combinedIncrement := uint64(1), uint64(0)
	for iterations != 0 {
		if iterations&1 != 0 {
			combinedIncrement = multiplier*combinedIncrement + increment
			combinedMultiplier *= multiplier
		}
		increment *= multiplier + 1
		multiplier *= multiplier
		iterations >>= 1
	}
	return combinedMultiplier*seed + combinedIncrement
}

// An odd affine mapping visits every 16-bit input exactly once per cycle.
// Compute the three branch ranges independently, without replaying the full
// workload. The short tail supports oracle tests of partial cycles as well.
func branchChecksum(iterations, seed uint64) uint64 {
	const first, second, period = uint64(21845), uint64(43690), uint64(1 << 16)
	cycle := sumSquares(first-1) + 11*first
	cycle += sumCubes(second-1) - sumCubes(first-1) + 17*(second-first)
	cycle += xorPrefixSum(period, 0x5a5a) - xorPrefixSum(second, 0x5a5a) + 23*(period-second)
	checksum := (iterations / period) * cycle
	for i := uint64(0); i < iterations%period; i++ {
		x := (i*40503 + seed) & (period - 1)
		switch {
		case x < first:
			checksum += x*x + 11
		case x < second:
			checksum += x*x*x + 17
		default:
			checksum += (x ^ 0x5a5a) + 23
		}
	}
	return checksum
}

// Sum x^mask for 0 <= x < count by counting set bits in each binary period.
func xorPrefixSum(count, mask uint64) uint64 {
	var sum uint64
	for bit := uint(0); bit < 16; bit++ {
		half := uint64(1) << bit
		ones := (count / (half * 2)) * half
		if tail := count % (half * 2); tail > half {
			ones += tail - half
		}
		if mask&half != 0 {
			ones = count - ones
		}
		sum += ones * half
	}
	return sum
}

func triangular(n uint64) uint64 {
	if n%2 == 0 {
		return (n / 2) * (n + 1)
	}
	return n * ((n + 1) / 2)
}

// Divide before multiplying: the largest sort case's unreduced numerator
// exceeds uint64 even though its checksum fits.
func sumSquares(n uint64) uint64 {
	a, b, c := n, n+1, 2*n+1
	if a%2 == 0 {
		a /= 2
	} else {
		b /= 2
	}
	if a%3 == 0 {
		a /= 3
	} else if b%3 == 0 {
		b /= 3
	} else {
		c /= 3
	}
	return a * b * c
}

func sumCubes(n uint64) uint64 {
	t := triangular(n)
	return t * t
}

// REP inputs use power-of-two buffers divisible by the 256-byte pattern.
// The oracle sums that pattern algebraically and accounts for both changed
// endpoints and the sampled byte after every copy, without copying buffers.
func repCopyChecksum(size, repeats uint64) uint64 {
	var patternSum, weightedPatternSum, samples uint64
	for i := uint64(0); i < 256; i++ {
		value := (i*131 + 17) & 255
		patternSum += value
		weightedPatternSum += (i + 1) * value
	}
	blocks := size / 256
	weighted := blocks*weightedPatternSum + 256*patternSum*triangular(blocks-1)
	for r := uint64(0); r < repeats; r++ {
		index := (r * 4099) & (size - 1)
		value := (index*131 + 17) & 255
		if index == 0 {
			value = (r & 255) ^ 17
		} else if index == size-1 {
			value = ((r >> 8) & 255) ^ 91
		}
		samples += (r + 1) * value
	}
	last := repeats - 1
	weighted = weighted - 17 - size*142 + ((last & 255) ^ 17) + size*(((last>>8)&255)^91)
	return samples + weighted
}

func repFillChecksum(size, repeats uint64) uint64 {
	var samples uint64
	for r := uint64(0); r < repeats; r++ {
		samples += (r + 1) * ((r*73 + 19) & 255)
	}
	last := ((repeats-1)*73 + 19) & 255
	return samples + last*triangular(size)
}
