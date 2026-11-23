package reference

import (
	"crypto/sha256"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestCorpusFixedProgramsHaveSeparateTrainingAndValidationSplits(t *testing.T) {
	wantSplits := map[string]string{
		"integer": SplitTrain, "rep_copy": SplitTrain,
		"branch": SplitValidation, "sort": SplitValidation, "rep_fill": SplitValidation,
	}
	full := Corpus(false)
	if len(full) != len(wantSplits) {
		t.Fatalf("got %d workloads, want %d", len(full), len(wantSplits))
	}
	seenIDs, seenSources := map[string]bool{}, map[[32]byte]string{}
	for _, workload := range full {
		if seenIDs[workload.ID] || workload.Split != wantSplits[workload.ID] || workload.Split == "" {
			t.Fatalf("duplicate, unexpected or incorrectly split workload: %+v", workload)
		}
		seenIDs[workload.ID] = true
		if !strings.Contains(workload.Source, "int main()") || !strings.HasSuffix(workload.Source, "\n") {
			t.Fatalf("%s does not embed its complete C++ program", workload.ID)
		}
		sourceHash := sha256.Sum256([]byte(workload.Source))
		if other, exists := seenSources[sourceHash]; exists {
			t.Fatalf("%s reuses %s's program across workload identities", workload.ID, other)
		}
		seenSources[sourceHash] = workload.ID
		if len(workload.Cases) != 3 {
			t.Fatalf("%s: expected three full scales", workload.ID)
		}
		seenInputs := map[string]bool{}
		for i, c := range workload.Cases {
			if c.Scale != 1<<i || seenInputs[c.Input] {
				t.Fatalf("%s: duplicate input or unexpected scale: %+v", workload.ID, c)
			}
			seenInputs[c.Input] = true
			if !strings.HasSuffix(c.Input, "\n") || !strings.HasSuffix(c.ExpectedStdout, "\n") || strings.Count(c.ExpectedStdout, "\n") != 1 {
				t.Fatalf("%s: input/output bytes are not newline-terminated records", workload.ID)
			}
			if _, err := strconv.ParseUint(strings.TrimSuffix(c.ExpectedStdout, "\n"), 10, 64); err != nil {
				t.Fatalf("%s: invalid checksum oracle: %v", workload.ID, err)
			}
		}
	}
}

func corpusInput(t *testing.T, c Case) []uint64 {
	t.Helper()
	var values []uint64
	for _, field := range strings.Fields(c.Input) {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			t.Fatalf("invalid numeric input %q: %v", c.Input, err)
		}
		values = append(values, value)
	}
	return values
}

func TestCorpusInputsAreBoundedAndSmokeOnlyReducesWork(t *testing.T) {
	full, smoke := Corpus(false), Corpus(true)
	if len(smoke) != len(full) {
		t.Fatal("smoke omitted a workload")
	}
	for i, workload := range full {
		small := smoke[i]
		if small.ID != workload.ID || small.Split != workload.Split || small.Source != workload.Source || len(small.Cases) != 1 || small.Cases[0].Scale != 1 {
			t.Fatalf("smoke changed program identity or split: %+v", small)
		}
		baseline := corpusInput(t, workload.Cases[0])
		smallInput := corpusInput(t, small.Cases[0])
		if len(smallInput) != len(baseline) || smallInput[0] >= baseline[0] || small.Cases[0].Input == workload.Cases[0].Input {
			t.Fatalf("%s smoke is not a smaller independent input", workload.ID)
		}
		all := append(append([]Case(nil), workload.Cases...), small.Cases...)
		for _, c := range all {
			input := corpusInput(t, c)
			switch workload.ID {
			case "integer":
				if len(input) != 2 || input[0] == 0 || input[0] > 128<<20 || input[1] != integerSeed {
					t.Fatalf("invalid integer input: %q", c.Input)
				}
			case "branch":
				if len(input) != 2 || input[0] == 0 || input[0] > 64<<20 || input[0]%(1<<16) != 0 || input[1] >= 1<<16 {
					t.Fatalf("invalid branch input: %q", c.Input)
				}
			case "sort":
				if len(input) != 1 || input[0] < 2 || input[0] > 2<<20 || input[0]&(input[0]-1) != 0 || input[0]*4 >= 128<<20 {
					t.Fatalf("invalid sort input: %q", c.Input)
				}
			case "rep_copy", "rep_fill":
				if len(input) != 2 || input[0] < 256 || input[0] > 4<<20 || input[0]&(input[0]-1) != 0 || input[1] == 0 || input[1] > 2048 || input[0]*2 >= 128<<20 {
					t.Fatalf("invalid REP input: %q", c.Input)
				}
			}
		}
		for _, c := range workload.Cases {
			input := corpusInput(t, c)
			if workload.ID == "rep_copy" || workload.ID == "rep_fill" {
				if input[0] != baseline[0] || input[1] != baseline[1]*uint64(c.Scale) {
					t.Fatalf("%s did not scale repetitions at fixed buffer size", workload.ID)
				}
			} else if input[0] != baseline[0]*uint64(c.Scale) {
				t.Fatalf("%s has inconsistent input scaling", workload.ID)
			}
		}
	}
}

func TestCorpusReturnsFreshDeterministicManifests(t *testing.T) {
	want := Corpus(false)
	changed := Corpus(false)
	changed[0].ID = "changed"
	changed[0].Source = "changed"
	changed[1].Cases[0].Input = "changed"
	changed[1].Cases[0].ExpectedStdout = "changed"
	if got := Corpus(false); !reflect.DeepEqual(got, want) {
		t.Fatal("caller mutation or invocation order changed the fixed manifest")
	}
}

func TestIntegerOracleMatchesIteratedUnsignedArithmetic(t *testing.T) {
	for _, seed := range []uint64{0, 1, integerSeed, ^uint64(0)} {
		for _, count := range []uint64{0, 1, 2, 17, 10001} {
			want := seed
			for i := uint64(0); i < count; i++ {
				want = want*6364136223846793005 + 1442695040888963407
			}
			if got := integerChecksum(count, seed); got != want {
				t.Fatalf("seed=%d count=%d: affine oracle %d, direct recurrence %d", seed, count, got, want)
			}
		}
	}
}

func TestBranchOracleMatchesFullPermutationAndPartialCycles(t *testing.T) {
	for _, seed := range []uint64{0, branchSeed, 65535} {
		for _, count := range []uint64{0, 1, 21845, 65535, 65536, 65537, 131073} {
			var want uint64
			for i := uint64(0); i < count; i++ {
				x := (i*40503 + seed) & 65535
				if x < 21845 {
					want += x*x + 11
				} else if x < 43690 {
					want += x*x*x + 17
				} else {
					want += (x ^ 0x5a5a) + 23
				}
			}
			if got := branchChecksum(count, seed); got != want {
				t.Fatalf("seed=%d count=%d: range oracle %d, enumerated branches %d", seed, count, got, want)
			}
		}
	}
}

func TestSortOracleChecksOrderAndAvoidsIntermediateOverflow(t *testing.T) {
	for _, size := range []int{2, 16, 4096} {
		values := make([]int, size)
		var unsorted uint64
		for i := range values {
			values[i] = int((uint64(i)*1664525 + 1013904223) & uint64(size-1))
			unsorted += uint64(i+1) * uint64(values[i]+1)
		}
		sort.Ints(values)
		var sorted uint64
		for i, value := range values {
			if value != i {
				t.Fatalf("size=%d: generator is not a permutation", size)
			}
			sorted += uint64(i+1) * uint64(value+1)
		}
		if got := sumSquares(uint64(size)); got != sorted || got == unsorted {
			t.Fatalf("size=%d: oracle %d, sorted %d, unsorted %d", size, got, sorted, unsorted)
		}
	}
	// Independent wide arithmetic covers the largest corpus input, whose
	// unreduced sum-of-squares numerator is greater than MaxUint64.
	n := uint64(2 << 20)
	want := new(big.Int).SetUint64(n)
	want.Mul(want, new(big.Int).SetUint64(n+1))
	want.Mul(want, new(big.Int).SetUint64(2*n+1))
	if want.IsUint64() {
		t.Fatal("fixture does not exercise an overflowing intermediate numerator")
	}
	want.Div(want, big.NewInt(6))
	if !want.IsUint64() || sumSquares(n) != want.Uint64() {
		t.Fatalf("largest sort checksum overflowed: got %d, want %s", sumSquares(n), want)
	}
}

func TestREPOraclesMatchCopiedAndFilledBuffers(t *testing.T) {
	for _, size := range []int{256, 512, 4096} {
		for _, repeats := range []int{1, 2, 17, 257} {
			source, copied, filled := make([]byte, size), make([]byte, size), make([]byte, size)
			for i := range source {
				source[i] = byte(i*131 + 17)
			}
			var copySum, fillSum uint64
			for r := 0; r < repeats; r++ {
				source[0], source[size-1] = byte(r)^17, byte(r>>8)^91
				copy(copied, source)
				for i := range filled {
					filled[i] = byte(r*73 + 19)
				}
				index := (r * 4099) & (size - 1)
				copySum += uint64(r+1) * uint64(copied[index])
				fillSum += uint64(r+1) * uint64(filled[index])
			}
			for i := range copied {
				copySum += uint64(i+1) * uint64(copied[i])
				fillSum += uint64(i+1) * uint64(filled[i])
			}
			if got := repCopyChecksum(uint64(size), uint64(repeats)); got != copySum {
				t.Fatalf("size=%d repeats=%d: copy oracle %d, buffer checksum %d", size, repeats, got, copySum)
			}
			if got := repFillChecksum(uint64(size), uint64(repeats)); got != fillSum {
				t.Fatalf("size=%d repeats=%d: fill oracle %d, buffer checksum %d", size, repeats, got, fillSum)
			}
		}
	}
}
