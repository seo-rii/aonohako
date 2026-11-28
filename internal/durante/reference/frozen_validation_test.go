package reference

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func frozenFixture(t *testing.T) (Dataset, *ValidationCorpus, *FrozenPolicy) {
	t.Helper()
	data := syntheticDataset()
	path := filepath.Join(t.TempDir(), "manifest.json")
	body := `{"schema":1,"language":"CPP20","workloads":[{"id":"submission","source":"int main(){}","cases":[{"scale":1,"input":"1\n","expected_stdout":"42\n"}]}]}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	corpus, err := LoadValidationCorpus(path)
	if err != nil {
		t.Fatal(err)
	}
	data.CorpusID, data.CorpusSHA256 = ExternalCorpusID, corpus.SHA256()
	data.Samples = data.Samples[:data.Repetitions]
	for i := range data.Samples {
		sample := &data.Samples[i]
		sample.WorkloadID, sample.Split = "submission", SplitValidation
		sample.SourceSHA256 = referenceDigest("int main(){}")
		sample.InputSHA256, sample.ExpectedSHA256 = referenceDigest("1\n"), referenceDigest("42\n")
		sample.Metered.WorkMeasurement = syntheticReport(20_000_000, 0)
		sample.NativeBefore, sample.NativeAfter = syntheticNativeResponse(40_000_000), syntheticNativeResponse(40_000_000)
	}
	policy := durante.TimePolicy{
		ModelID: "frozen", ModelVersion: 1, ReferenceID: "before-collection", RuntimeID: "CPP20@" + data.RuntimeImage,
		Family: "cpp", EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault,
		InstructionNSNumerator: 2, REPByteNSNumerator: 3, NSDenominator: 1,
	}
	frozen := freezeTestPolicy(t, policy)
	data.FrozenPolicySHA256 = frozen.SHA256()
	return data, corpus, frozen
}

func freezeTestPolicy(t *testing.T, policy durante.TimePolicy) *FrozenPolicy {
	t.Helper()
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := ParseFrozenPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}

func TestValidateFrozenKeepsCoefficientsAndUsesEveryHeldOutRepetition(t *testing.T) {
	data, corpus, policy := frozenFixture(t)
	result, err := ValidateFrozen(data, corpus, policy)
	if err != nil || !result.Passed || result.CandidatePolicy != nil || len(result.Cases) != 1 ||
		result.Cases[0].EstimatedNs != 40_000_000 || result.Cases[0].AbsoluteErrorNs != 0 || result.Cases[0].Split != SplitValidation {
		t.Fatalf("unexpected frozen evaluation: %+v, %v", result, err)
	}
	if result, err := Fit(data, syntheticFitOptions()); err == nil || result.Passed || result.CandidatePolicy != nil {
		t.Fatal("used external held-out cases for fitting")
	}
	// Exactly +20% is accepted. A single repetition just over
	// the threshold must fail even if the other observations are unchanged.
	for i := range data.Samples {
		data.Samples[i].Metered.WorkMeasurement = syntheticReport(24_000_000, 0)
	}
	if result, err := ValidateFrozen(data, corpus, policy); err != nil || !result.Passed {
		t.Fatalf("rejected equality with error threshold: %+v, %v", result, err)
	}
	*data.Samples[1].Metered.WorkMeasurement.Instructions++
	result, err = ValidateFrozen(data, corpus, policy)
	if err != nil || result.Passed || result.CandidatePolicy != nil || result.Cases[0].EstimatedNs != 48_000_002 ||
		len(result.Failures) != 1 || !strings.Contains(result.Failures[0], "repetition 2: held-out error") {
		t.Fatalf("changed policy or lost failing repetition: %+v, %v", result, err)
	}
}

func TestValidateFrozenObservedDeadlineDisagreement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		native [6]uint64
		counts [3]uint64 // the frozen policy charges two ns per instruction
		want   *DeadlineDisagreement
	}{
		{"false accept at one limit", [6]uint64{40_000_001, 42_000_000, 41_000_000, 43_000_000, 43_000_000, 42_000_000},
			[3]uint64{18_000_000, 20_000_000, 19_000_000}, &DeadlineDisagreement{"false_accept", 40, 40}},
		{"false timeout", [6]uint64{38_000_000, 39_000_000, 40_000_000, 39_000_000, 38_000_000, 39_000_000},
			[3]uint64{23_000_000, 20_500_001, 23_000_000}, &DeadlineDisagreement{"false_timeout", 40, 41}},
		{"touching rounded ranges", [6]uint64{40_000_001, 43_000_000, 42_000_000, 42_000_000, 43_000_000, 42_000_000},
			[3]uint64{19_000_000, 20_000_001, 19_000_000}, nil},
		{"clamp to supported limits", [6]uint64{40_000_000, 40_000_000, 40_000_000, 40_000_000, 40_000_000, 40_000_000},
			[3]uint64{1_000_000_000_000, 1_000_000_000_000, 1_000_000_000_000}, &DeadlineDisagreement{"false_timeout", 40, 600_000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, corpus, policy := frozenFixture(t)
			for i := range data.Samples {
				data.Samples[i].NativeBefore = syntheticNativeResponse(tc.native[2*i])
				data.Samples[i].NativeAfter = syntheticNativeResponse(tc.native[2*i+1])
				data.Samples[i].Metered.WorkMeasurement = syntheticReport(tc.counts[i], 0)
			}
			result, err := ValidateFrozen(data, corpus, policy)
			if err != nil || len(result.Cases) != 1 {
				t.Fatalf("missing case: %v, %v", result, err)
			}
			if got := result.Cases[0].ObservedDeadlineDisagreement; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("observed interval = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestValidateFrozenRejectsWrongIdentitiesAndRecordSets(t *testing.T) {
	for name, edit := range map[string]func(*Dataset, *ValidationCorpus){
		"smoke":              func(d *Dataset, _ *ValidationCorpus) { d.Smoke = true },
		"missing record":     func(d *Dataset, _ *ValidationCorpus) { d.Samples = d.Samples[1:] },
		"failed collection":  func(d *Dataset, _ *ValidationCorpus) { d.Complete = false },
		"duplicate":          func(d *Dataset, _ *ValidationCorpus) { d.Samples[1] = d.Samples[0] },
		"changed artifact":   func(d *Dataset, _ *ValidationCorpus) { d.Samples[1].BinarySHA256 = referenceDigest("other") },
		"changed input":      func(d *Dataset, _ *ValidationCorpus) { d.Samples[1].InputSHA256 = referenceDigest("other") },
		"changed source":     func(d *Dataset, _ *ValidationCorpus) { d.Samples[1].SourceSHA256 = referenceDigest("other") },
		"changed oracle":     func(d *Dataset, _ *ValidationCorpus) { d.Samples[1].ExpectedSHA256 = referenceDigest("other") },
		"wrong manifest":     func(d *Dataset, _ *ValidationCorpus) { d.CorpusSHA256 = referenceDigest("other") },
		"unloaded manifest":  func(_ *Dataset, c *ValidationCorpus) { *c = ValidationCorpus{} },
		"wrong language":     func(d *Dataset, _ *ValidationCorpus) { d.SourceLanguage = "PYTHON3" },
		"wrong runtime":      func(d *Dataset, _ *ValidationCorpus) { d.RuntimeImage = "sha256:" + strings.Repeat("b", 64) },
		"wrong engine":       func(d *Dataset, _ *ValidationCorpus) { d.Samples[0].Metered.WorkMeasurement.EngineProfile = "wrong" },
		"wrong family":       func(d *Dataset, _ *ValidationCorpus) { d.Samples[0].Metered.WorkMeasurement.Family = "native" },
		"wrong dispatch":     func(d *Dataset, _ *ValidationCorpus) { d.DispatchProfile = durante.DispatchGLIBC241 },
		"wrong cpuid":        func(d *Dataset, _ *ValidationCorpus) { d.CPUIDProfile = "x86-64-v2" },
		"wrong determinism":  func(d *Dataset, _ *ValidationCorpus) { d.RuntimeDeterminism = "v1" },
		"wrong report cpuid": func(d *Dataset, _ *ValidationCorpus) { d.Samples[0].Metered.WorkMeasurement.CPUIDProfile = "x86-64-v2" },
		"wrong installation": func(d *Dataset, _ *ValidationCorpus) {
			d.Samples[0].Metered.WorkMeasurement.Installation.ClientSHA256 = strings.Repeat("d", 64)
		},
		"scored native control":  func(d *Dataset, _ *ValidationCorpus) { score := 0.5; d.Samples[0].NativeBefore.Score = &score },
		"invalid native control": func(d *Dataset, _ *ValidationCorpus) { d.Samples[0].NativeAfter.Status = model.RunStatusWA },
	} {
		t.Run(name, func(t *testing.T) {
			data, corpus, policy := frozenFixture(t)
			edit(&data, corpus)
			if result, err := ValidateFrozen(data, corpus, policy); err == nil || result.Passed || result.CandidatePolicy != nil {
				t.Fatalf("accepted incompatible inputs: %+v, %v", result, err)
			}
		})
	}
}

func TestValidateFrozenCannotReplaceCollectedPolicy(t *testing.T) {
	data, corpus, original := frozenFixture(t)
	// Callers can inspect a copy; editing it cannot change the policy used in
	// the comparison. A different serialized model necessarily has a different digest.
	changed := original.Policy()
	changed.InstructionNSNumerator++
	replacement := freezeTestPolicy(t, changed)
	if replacement.SHA256() == original.SHA256() {
		t.Fatal("changing model weights preserved its file identity")
	}
	for _, policy := range []*FrozenPolicy{nil, {}, replacement} {
		if result, err := ValidateFrozen(data, corpus, policy); err == nil || result.Passed || result.CandidatePolicy != nil {
			t.Fatal("accepted missing or substituted frozen coefficients")
		}
	}
	result, err := ValidateFrozen(data, corpus, original)
	if err != nil || !result.Passed || result.Cases[0].EstimatedNs != 40_000_000 {
		t.Fatalf("editing an inspected model changed its evaluation: %+v, %v", result, err)
	}
	for _, digest := range []string{"", "not-a-digest", referenceDigest("a different policy")} {
		data.FrozenPolicySHA256 = digest
		if result, err := ValidateFrozen(data, corpus, original); err == nil || result.Passed || result.CandidatePolicy != nil {
			t.Fatal("accepted wrong frozen policy digest")
		}
	}
}

func TestValidateFrozenRejectsUnstableControlsAndNumericOverflow(t *testing.T) {
	data, corpus, policy := frozenFixture(t)
	data.Samples[0].NativeAfter = syntheticNativeResponse(10_000_000)
	result, err := ValidateFrozen(data, corpus, policy)
	if err != nil || result.Passed || !strings.Contains(strings.Join(result.Failures, "\n"), "native spread") ||
		!strings.Contains(strings.Join(result.Failures, "\n"), "below min_native_ns") {
		t.Fatalf("accepted insufficient or unstable native timings: %+v, %v", result, err)
	}
	data, corpus, policy = frozenFixture(t)
	*data.Samples[0].Metered.WorkMeasurement.Instructions = math.MaxUint64
	result, err = ValidateFrozen(data, corpus, policy)
	if err != nil || result.Passed || !strings.Contains(strings.Join(result.Failures, "\n"), "exceeds int64 nanoseconds") ||
		!strings.Contains(strings.Join(result.Failures, "\n"), "instruction spread") {
		t.Fatalf("lost overflow or instability: %+v, %v", result, err)
	}
	if row := result.Cases[0]; row.ObservedDeadlineDisagreement != nil || row.EstimatedNs != 40_000_000 {
		t.Fatalf("partial predictions must retain the first successful estimate and omit the interval: %+v", row)
	}
}

func TestValidateFrozenOperationSpecificStability(t *testing.T) {
	data, corpus, policy := frozenFixture(t)
	split := policy.Policy()
	split.Method, split.REPByteNSNumerator = durante.TimeMethodREPBreakdown, 0
	split.MOVSByteNSNumerator, split.STOSByteNSNumerator = 2, 3
	policy = freezeTestPolicy(t, split)
	data.FrozenPolicySHA256 = policy.SHA256()
	for i := range data.Samples {
		r := data.Samples[i].Metered.WorkMeasurement
		*r.REPBytes, *r.REPEntries = 1000, 2
		r.REPBreakdown = &durante.REPBreakdown{Version: 1, MOVSEntries: 1, STOSEntries: 1, MOVSBytes: 500, STOSBytes: 500}
	}
	if result, err := ValidateFrozen(data, corpus, policy); err != nil || !result.Passed {
		t.Fatalf("stable breakdown rejected: %+v, %v", result, err)
	}
	// Aggregate REP is unchanged; changing the mix still changes the time model inputs.
	data.Samples[0].Metered.WorkMeasurement.REPBreakdown.MOVSBytes++
	data.Samples[0].Metered.WorkMeasurement.REPBreakdown.STOSBytes--
	result, err := ValidateFrozen(data, corpus, policy)
	reasons := strings.Join(result.Failures, "\n")
	if err != nil || result.Passed || !strings.Contains(reasons, "MOVS byte spread") || !strings.Contains(reasons, "STOS byte spread") {
		t.Fatalf("operand-specific spread hidden in aggregate: %+v, %v", result, err)
	}
	data.Samples[0].Metered.WorkMeasurement.REPBreakdown = nil
	if result, err := ValidateFrozen(data, corpus, policy); err == nil || result.Passed {
		t.Fatal("split policy accepted missing MOVS/STOS partition")
	}
}

func TestValidateFrozenPolicyFileRejectsRuntimeOptionBinding(t *testing.T) {
	_, _, policy := frozenFixture(t)
	raw, err := json.Marshal(policy.Policy())
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"runtime_options_id":"a-registry-binding"}`)...)
	if _, err := ParseFrozenPolicy(raw); err == nil {
		t.Fatal("accepted an unrecorded runtime-options binding in a standalone policy file")
	}
}
