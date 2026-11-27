package reference

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func syntheticFitOptions() FitOptions {
	return FitOptions{ModelID: "synthetic-fit-test", ModelVersion: 1, ReferenceID: "synthetic-not-calibrated", MinRepetitions: 3, MinNativeNs: 1}
}

func syntheticNativeResponse(ns uint64) model.RunResponse {
	exit := 0
	ms := ns / 1_000_000
	if ns%1_000_000 != 0 {
		ms++
	}
	return model.RunResponse{
		Status: model.RunStatusAccepted, VerdictSource: "stdout", ExitCode: &exit,
		CPUTimeNs: ns, CPUTimeMs: int64(ms), TimeMs: int64(ms), WallTimeMs: int64(ms), MemoryKB: 1024,
		CPUAccounting: &model.CPUAccounting{RusageBaselineNs: 1000, WaitUserNs: ns + 1000, WaitTargetNs: ns, ProcessClockPeakNs: ns / 2, SelectedSource: "wait_rusage"},
	}
}

func syntheticReport(instructions, rep uint64) *durante.Report {
	verified, zero, entries := false, uint64(0), uint64(0)
	if rep != 0 {
		entries = 1
	}
	return &durante.Report{
		Schema: 1, ClientVersion: 1, Metric: durante.Metric, Scope: durante.Scope, Authority: durante.Authority,
		Complete: true, Quality: "uninterrupted", EngineProfile: durante.EngineProfile, Family: "cpp", DispatchProfile: durante.DispatchDefault,
		REPEnabled: true, REPTelemetryStatus: durante.REPMovsStosOnly, Processes: 1, Images: 1,
		Instructions: &instructions, REPBytes: &rep, REPEntries: &entries, UnsupportedREPEntries: &zero,
		IntegrityVerified: &verified, MeasurementContract: durante.DiagnosticContract(),
		Installation: &durante.InstallationIdentity{
			SourceCommit: durante.SourceCommit, RuntimeSourceCommit: "6847bb210bea0ca11d1bf47704383d6a075da490",
			RuntimePatchSHA256:           durante.RuntimePatchSHA256,
			RuntimeCodeReplayPatchSHA256: durante.RuntimeCodeReplayPatchSHA256,
			LauncherSHA256:               strings.Repeat("a", 64), ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true,
		},
	}
}

// These counter/time pairs have known coefficients 2 ns/instruction and
// 3 ns/REP byte. They exercise the evaluator, not reference hardware accuracy.
func syntheticDataset() Dataset {
	dataset := Dataset{
		Schema: Schema, CorpusID: CorpusID, Complete: true, Repetitions: 3,
		RuntimeImage: "sha256:" + strings.Repeat("a", 64), SourceLanguage: "CPP20", DispatchProfile: durante.DispatchDefault,
		RunnerSHA256:  strings.Repeat("b", 64),
		NativeLimits:  model.Limits{TimeMs: 600000, WallTimeMs: 600000, MemoryMB: 128},
		MeteredLimits: durante.ResourceLimits{CPUTimeMs: 600000, WallTimeMs: 600000, MemoryMB: 512},
	}
	for _, workload := range Corpus(false) {
		for _, fixture := range workload.Cases {
			var instructions, rep uint64
			switch workload.ID {
			case "integer":
				instructions = 1000
			case "rep_copy":
				instructions, rep = 100, 1000
			case "branch":
				instructions = 700
			case "sort":
				instructions, rep = 1200, 10
			case "rep_fill":
				instructions, rep = 50, 900
			}
			instructions, rep = instructions*uint64(fixture.Scale), rep*uint64(fixture.Scale)
			native := 2*instructions + 3*rep
			for repetition := 1; repetition <= dataset.Repetitions; repetition++ {
				metered := syntheticNativeResponse(10*native + 2000)
				metered.Status, metered.VerdictSource = model.RunStatusInitFail, "durante_complete"
				matches := true
				metered.WorkMeasurementExecution = &model.WorkMeasurementExecution{
					Authority: durante.Authority, State: "complete", NominalLimits: dataset.NativeLimits,
					RawLimits: dataset.MeteredLimits, OutputMatches: &matches, TerminationSource: "stdout",
				}
				metered.WorkMeasurement = syntheticReport(instructions, rep)
				dataset.Samples = append(dataset.Samples, Sample{
					WorkloadID: workload.ID, Split: workload.Split, Scale: fixture.Scale, Repetition: repetition,
					SourceSHA256: referenceDigest(workload.Source), BinarySHA256: referenceDigest("synthetic binary " + workload.ID),
					InputSHA256: referenceDigest(fixture.Input), ExpectedSHA256: referenceDigest(fixture.ExpectedStdout),
					NativeBefore: syntheticNativeResponse(native), NativeAfter: syntheticNativeResponse(native), Metered: metered,
				})
			}
		}
	}
	return dataset
}

func requireFit(t *testing.T, dataset Dataset, options FitOptions) Evaluation {
	t.Helper()
	evaluation, err := Fit(dataset, options)
	if err != nil {
		t.Fatal(err)
	}
	return evaluation
}

func TestFitKnownPositiveCoefficientsWithoutMutatingEvidence(t *testing.T) {
	dataset, options := syntheticDataset(), syntheticFitOptions()
	before, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	got := requireFit(t, dataset, options)
	if !got.Passed || len(got.Failures) != 0 || got.CandidatePolicy == nil || got.Options != options || got.Authority != durante.Authority {
		t.Fatalf("known model did not qualify: %+v", got)
	}
	policy := got.CandidatePolicy
	if policy.InstructionNSNumerator != 2_000_000_000 || policy.REPByteNSNumerator != 3_000_000_000 || policy.NSDenominator != 1_000_000_000 || policy.RuntimeID != "CPP20@"+dataset.RuntimeImage {
		t.Fatalf("wrong fitted integer model: %+v", policy)
	}
	if len(got.Cases) != 15 {
		t.Fatalf("full corpus was not evaluated: %d cases", len(got.Cases))
	}
	for _, row := range got.Cases {
		if row.AbsoluteErrorNs != 0 || row.AbsoluteErrorPPM != 0 || row.EstimatedNs != int64(row.NativeMedianNs) {
			t.Fatalf("known model has error: %+v", row)
		}
	}
	after, err := json.Marshal(dataset)
	if err != nil || string(before) != string(after) {
		t.Fatalf("Fit mutated its observations: %v", err)
	}
	// Dataset order cannot change the mathematical fit or case report order.
	for i, j := 0, len(dataset.Samples)-1; i < j; i, j = i+1, j-1 {
		dataset.Samples[i], dataset.Samples[j] = dataset.Samples[j], dataset.Samples[i]
	}
	if reordered := requireFit(t, dataset, options); !reflect.DeepEqual(got, reordered) {
		t.Fatal("sample ordering changed the evaluation")
	}
}

func TestFitHeldOutErrorNeverChangesTrainingCoefficients(t *testing.T) {
	dataset, options := syntheticDataset(), syntheticFitOptions()
	baseline := requireFit(t, dataset, options)
	for i := range dataset.Samples {
		sample := &dataset.Samples[i]
		if sample.Split == SplitValidation {
			sample.NativeBefore = syntheticNativeResponse(sample.NativeBefore.CPUTimeNs * 2)
			sample.NativeAfter = syntheticNativeResponse(sample.NativeAfter.CPUTimeNs * 2)
		}
	}
	failed := requireFit(t, dataset, options)
	if failed.Passed || failed.CandidatePolicy == nil || *failed.CandidatePolicy != *baseline.CandidatePolicy || !strings.Contains(strings.Join(failed.Failures, "\n"), "held-out error") {
		t.Fatalf("validation data leaked into fit or failure was lost: %+v", failed)
	}
	options.MaxErrorPPM = 500_000
	if exact := requireFit(t, dataset, options); !exact.Passed {
		t.Fatalf("exact error threshold must pass: %+v", exact)
	}
	options.MaxErrorPPM--
	if below := requireFit(t, dataset, options); below.Passed {
		t.Fatal("one-PPM tighter threshold accepted the held-out error")
	}
}

func TestFitEvaluatesEveryMeteredRepetition(t *testing.T) {
	dataset, options := syntheticDataset(), syntheticFitOptions()
	options.MaxWorkSpreadPPM = 1_000_000
	var expected uint64
	for i := range dataset.Samples {
		sample := &dataset.Samples[i]
		if sample.WorkloadID == "branch" && sample.Scale == 1 && sample.Repetition == 2 {
			expected = sample.NativeBefore.CPUTimeNs * 2
			*sample.Metered.WorkMeasurement.Instructions *= 2
		}
	}
	evaluation := requireFit(t, dataset, options)
	if evaluation.Passed || !strings.Contains(strings.Join(evaluation.Failures, "\n"), "branch scale 1 repetition 2: held-out error") {
		t.Fatalf("middle repetition escaped the validation gate: %+v", evaluation)
	}
	for _, row := range evaluation.Cases {
		if row.WorkloadID == "branch" && row.Scale == 1 {
			if row.EstimatedNs != int64(expected) || row.AbsoluteErrorPPM != 1_000_000 {
				t.Fatalf("case does not report its actual worst repetition: %+v", row)
			}
		}
	}
}

func TestFitRejectsTrainingErrorEvenWhenEveryHeldOutCasePasses(t *testing.T) {
	dataset, options := syntheticDataset(), syntheticFitOptions()
	options.MaxErrorPPM = 200_000
	for i := range dataset.Samples {
		sample := &dataset.Samples[i]
		if sample.WorkloadID == "integer" && sample.Scale == 1 {
			sample.NativeBefore = syntheticNativeResponse(sample.NativeBefore.CPUTimeNs * 2)
			sample.NativeAfter = syntheticNativeResponse(sample.NativeAfter.CPUTimeNs * 2)
		}
	}
	evaluation := requireFit(t, dataset, options)
	if evaluation.Passed || evaluation.CandidatePolicy == nil || len(evaluation.Failures) != dataset.Repetitions {
		t.Fatalf("training mismatch did not reject the candidate: %+v", evaluation)
	}
	for _, failure := range evaluation.Failures {
		if !strings.Contains(failure, "integer scale 1 repetition") || !strings.Contains(failure, "training error exceeds max_error_ppm") {
			t.Fatalf("fixture should fail only the three training repetitions: %s", failure)
		}
	}
	for _, row := range evaluation.Cases {
		if row.Split == SplitValidation && row.AbsoluteErrorPPM > options.MaxErrorPPM {
			t.Fatalf("fixture must keep all held-out cases within the error gate: %+v", row)
		}
	}
}

func TestFitChecksEachExecutionAgainstItsOwnCPUCap(t *testing.T) {
	for _, phase := range []string{"native_before", "native_after", "metered"} {
		for _, boundary := range []struct {
			name   string
			overBy uint64
		}{{"at cap", 0}, {"one ns over", 1}} {
			t.Run(phase+"/"+boundary.name, func(t *testing.T) {
				dataset := syntheticDataset()
				dataset.NativeLimits.TimeMs, dataset.MeteredLimits.CPUTimeMs = 1, 2
				for i := range dataset.Samples {
					d := dataset.Samples[i].Metered.WorkMeasurementExecution
					d.NominalLimits, d.RawLimits = dataset.NativeLimits, dataset.MeteredLimits
				}
				sample := &dataset.Samples[0]
				var response *model.RunResponse
				limit := uint64(dataset.NativeLimits.TimeMs) * 1_000_000
				switch phase {
				case "native_before":
					response = &sample.NativeBefore
				case "native_after":
					response = &sample.NativeAfter
				case "metered":
					response = &sample.Metered
					limit = uint64(dataset.MeteredLimits.CPUTimeMs) * 1_000_000
				}
				accounting := syntheticNativeResponse(limit + boundary.overBy)
				response.CPUTimeNs, response.CPUTimeMs = accounting.CPUTimeNs, accounting.CPUTimeMs
				response.CPUAccounting = accounting.CPUAccounting
				response.TimeMs, response.WallTimeMs = accounting.TimeMs, accounting.WallTimeMs
				evaluation, err := Fit(dataset, syntheticFitOptions())
				if boundary.overBy != 0 {
					if err == nil || evaluation.Passed || evaluation.CandidatePolicy != nil || !strings.Contains(err.Error(), phase) || !strings.Contains(err.Error(), "CPU nanoseconds exceed declared execution cap") {
						t.Fatalf("execution above its cap was accepted: evaluation=%+v err=%v", evaluation, err)
					}
				} else if err != nil || evaluation.CandidatePolicy == nil || len(evaluation.Cases) != 15 {
					t.Fatalf("exact CPU cap must pass data validation: evaluation=%+v err=%v", evaluation, err)
				}
			})
		}
	}
}

func TestFitRejectsInvalidOrIncompleteEvidence(t *testing.T) {
	for name, edit := range map[string]func(*Dataset){
		"wrong schema":          func(d *Dataset) { d.Schema++ },
		"wrong corpus":          func(d *Dataset) { d.CorpusID = "other" },
		"smoke":                 func(d *Dataset) { d.Smoke = true },
		"incomplete":            func(d *Dataset) { d.Complete = false },
		"recorded failure":      func(d *Dataset) { d.Failures = []string{"collection failed"} },
		"too few repetitions":   func(d *Dataset) { d.Repetitions = 2 },
		"too many repetitions":  func(d *Dataset) { d.Repetitions = 10 },
		"source profile":        func(d *Dataset) { d.SourceLanguage = "C11" },
		"dispatch":              func(d *Dataset) { d.DispatchProfile = durante.DispatchGLIBC241 },
		"mutable image":         func(d *Dataset) { d.RuntimeImage = "runner:latest" },
		"runner hash":           func(d *Dataset) { d.RunnerSHA256 = strings.Repeat("B", 64) },
		"missing sample":        func(d *Dataset) { d.Samples = d.Samples[1:] },
		"duplicate sample":      func(d *Dataset) { d.Samples[len(d.Samples)-1] = d.Samples[0] },
		"unknown workload":      func(d *Dataset) { d.Samples[0].WorkloadID = "other" },
		"wrong scale":           func(d *Dataset) { d.Samples[0].Scale = 7 },
		"wrong repetition":      func(d *Dataset) { d.Samples[0].Repetition = 0 },
		"wrong split":           func(d *Dataset) { d.Samples[0].Split = SplitValidation },
		"wrong source":          func(d *Dataset) { d.Samples[0].SourceSHA256 = referenceDigest("other") },
		"wrong input":           func(d *Dataset) { d.Samples[0].InputSHA256 = referenceDigest("other") },
		"wrong expected output": func(d *Dataset) { d.Samples[0].ExpectedSHA256 = referenceDigest("other") },
		"invalid binary hash":   func(d *Dataset) { d.Samples[0].BinarySHA256 = "abc" },
		"binary changed":        func(d *Dataset) { d.Samples[1].BinarySHA256 = referenceDigest("other") },
		"native failed":         func(d *Dataset) { d.Samples[0].NativeBefore.Status = model.RunStatusWA },
		"native nonzero exit":   func(d *Dataset) { *d.Samples[0].NativeAfter.ExitCode = 7 },
		"native zero CPU":       func(d *Dataset) { d.Samples[0].NativeBefore = syntheticNativeResponse(0) },
		"native normalization":  func(d *Dataset) { d.Samples[0].NativeBefore.CPUTimeNormalization = &model.CPUTimeNormalization{} },
		"native sampling":       func(d *Dataset) { d.Samples[0].NativeAfter.CPUTimeSampling = &model.CPUTimeSampling{} },
		"missing accounting":    func(d *Dataset) { d.Samples[0].NativeBefore.CPUAccounting = nil },
		"wrong accounting source": func(d *Dataset) {
			d.Samples[0].NativeBefore.CPUAccounting.SelectedSource = "unknown"
		},
		"inconsistent CPU units": func(d *Dataset) { d.Samples[0].NativeBefore.CPUTimeMs++ },
		"metered native":         func(d *Dataset) { d.Samples[0].NativeBefore.WorkMeasurement = d.Samples[0].Metered.WorkMeasurement },
		"incomplete envelope":    func(d *Dataset) { d.Samples[0].Metered.WorkMeasurementExecution.State = "incomplete" },
		"wrong output":           func(d *Dataset) { *d.Samples[0].Metered.WorkMeasurementExecution.OutputMatches = false },
		"extra replay on native control": func(d *Dataset) {
			d.Samples[0].NativeAfter.WorkMeasurementObservation = &model.WorkMeasurementObservation{Version: 1, State: "complete"}
		},
		"extra replay on metered phase": func(d *Dataset) {
			d.Samples[0].Metered.WorkMeasurementObservation = &model.WorkMeasurementObservation{Version: 1, State: "unavailable"}
		},
		"nominal limits":  func(d *Dataset) { d.Samples[0].Metered.WorkMeasurementExecution.NominalLimits.TimeMs-- },
		"raw limits":      func(d *Dataset) { d.Samples[0].Metered.WorkMeasurementExecution.RawLimits.CPUTimeMs-- },
		"missing counter": func(d *Dataset) { d.Samples[0].Metered.WorkMeasurement.Instructions = nil },
		"changed installation": func(d *Dataset) {
			d.Samples[1].Metered.WorkMeasurement.Installation.ClientSHA256 = strings.Repeat("d", 64)
		},
		"invalid installation": func(d *Dataset) { d.Samples[0].Metered.WorkMeasurement.Installation.Unchanged = false },
		"observed signal": func(d *Dataset) {
			r := d.Samples[0].Metered.WorkMeasurement
			r.SignalsObserved, r.Quality = 1, "signals_observed"
		},
		"partial REP": func(d *Dataset) {
			r := d.Samples[0].Metered.WorkMeasurement
			*r.UnsupportedREPEntries, r.REPTelemetryStatus = 1, durante.REPPartial
		},
	} {
		t.Run(name, func(t *testing.T) {
			dataset := syntheticDataset()
			edit(&dataset)
			got, err := Fit(dataset, syntheticFitOptions())
			if err == nil || got.Passed || got.CandidatePolicy != nil || len(got.Failures) == 0 {
				t.Fatalf("invalid evidence produced a model: %+v, %v", got, err)
			}
		})
	}
}

func TestFitKeepsUnstableAndShortMeasurementsInFailedEvaluation(t *testing.T) {
	for _, kind := range []string{"native spread", "instruction spread", "REP zero median", "native too short"} {
		t.Run(kind, func(t *testing.T) {
			dataset, options := syntheticDataset(), syntheticFitOptions()
			sample := &dataset.Samples[0]
			switch kind {
			case "native spread":
				sample.NativeBefore = syntheticNativeResponse(sample.NativeBefore.CPUTimeNs * 2)
			case "instruction spread":
				(*sample.Metered.WorkMeasurement.Instructions)++
			case "REP zero median":
				*sample.Metered.WorkMeasurement.REPBytes, *sample.Metered.WorkMeasurement.REPEntries = 1, 1
			case "native too short":
				options.MinNativeNs = 1_000_000
			}
			got := requireFit(t, dataset, options)
			if got.Passed || len(got.Failures) == 0 || len(got.Cases) != 15 {
				t.Fatalf("poor evidence was discarded or qualified: %+v", got)
			}
			if kind == "REP zero median" && got.Cases[0].REPSpreadPPM != math.MaxUint64 {
				t.Fatalf("undefined positive REP spread around zero was hidden: %+v", got.Cases[0])
			}
		})
	}
}

func TestFitPreservesCgroupAccountingWithoutWaitTargetEquality(t *testing.T) {
	dataset := syntheticDataset()
	for i := range dataset.Samples {
		for _, response := range []*model.RunResponse{&dataset.Samples[i].NativeBefore, &dataset.Samples[i].NativeAfter, &dataset.Samples[i].Metered} {
			response.CPUAccounting.SelectedSource = "cgroup"
			response.CPUAccounting.WaitTargetNs = 0
		}
	}
	if got := requireFit(t, dataset, syntheticFitOptions()); !got.Passed {
		t.Fatalf("valid cgroup observations were rejected: %+v", got)
	}
}

func TestFitChecksEveryNativeDurationBeforeMedianAggregation(t *testing.T) {
	dataset, options := syntheticDataset(), syntheticFitOptions()
	options.MinNativeNs, options.MaxNativeSpreadPPM = 1000, 600_000
	dataset.Samples[0].NativeBefore = syntheticNativeResponse(999)
	got := requireFit(t, dataset, options)
	if got.Passed || got.CandidatePolicy == nil || len(got.Failures) != 1 || !strings.Contains(got.Failures[0], "native observation below min_native_ns") {
		t.Fatalf("short individual observation disappeared into a valid median: %+v", got)
	}
	if got.Cases[0].NativeMedianNs != 2000 || got.Cases[0].NativeMedianNs < options.MinNativeNs {
		t.Fatalf("test must retain a median above the minimum: %+v", got.Cases[0])
	}
}

func TestFitRejectsUnidentifiableOrUnrepresentableCoefficients(t *testing.T) {
	for _, kind := range []string{"rank deficient", "no REP", "nonpositive", "zero coefficient", "ill-conditioned", "quantized zero", "uint64 overflow"} {
		t.Run(kind, func(t *testing.T) {
			dataset := syntheticDataset()
			for i := range dataset.Samples {
				sample := &dataset.Samples[i]
				if sample.Split != SplitTrain {
					continue
				}
				scale := uint64(sample.Scale)
				instructions, rep, native := uint64(1000), uint64(0), uint64(2000)
				if sample.WorkloadID == "rep_copy" {
					rep, native = 1000, 5000
				}
				switch kind {
				case "rank deficient":
					rep, native = 1000, 5000
				case "no REP":
					rep = 0
				case "nonpositive":
					if rep != 0 {
						native = 1000
					}
				case "zero coefficient":
					native = 2000
				case "ill-conditioned":
					instructions, rep = 1_000_000_000, 1_000_000_000
					if sample.WorkloadID == "rep_copy" {
						rep++
					}
					native = 2*instructions + 3*rep
				case "quantized zero":
					instructions, rep, native = 1_000_000_000_000, 0, 1
					if sample.WorkloadID == "rep_copy" {
						rep, native = 1_000_000_000_000, 2
					}
				case "uint64 overflow":
					instructions, rep, native = 1, 0, 30_000_000_000
					if sample.WorkloadID == "rep_copy" {
						rep, native = 1, 30_000_000_002
					}
				}
				sample.Metered.WorkMeasurement = syntheticReport(instructions*scale, rep*scale)
				sample.NativeBefore, sample.NativeAfter = syntheticNativeResponse(native*scale), syntheticNativeResponse(native*scale)
			}
			got := requireFit(t, dataset, syntheticFitOptions())
			if got.Passed || got.CandidatePolicy != nil || len(got.Failures) == 0 {
				t.Fatalf("unsuitable coefficients produced a policy: %+v", got)
			}
		})
	}
}

func TestFitRechecksQuantizedPolicyForNanosecondOverflow(t *testing.T) {
	dataset := syntheticDataset()
	for i := range dataset.Samples {
		sample := &dataset.Samples[i]
		if sample.WorkloadID == "branch" {
			sample.Metered.WorkMeasurement = syntheticReport(math.MaxUint64, 0)
		}
	}
	got := requireFit(t, dataset, syntheticFitOptions())
	if got.Passed || got.CandidatePolicy == nil || !strings.Contains(strings.Join(got.Failures, "\n"), "exceeds int64 nanoseconds") {
		t.Fatalf("overflowing final policy escaped re-evaluation: %+v", got)
	}
}

func TestFitRetainsCounterPrecisionBeyondFloat64(t *testing.T) {
	dataset, options := syntheticDataset(), syntheticFitOptions()
	options.MaxErrorPPM = 1
	for i := range dataset.Samples {
		sample := &dataset.Samples[i]
		instructions, rep := uint64(9_007_200_000_000_001), uint64(0)
		if sample.WorkloadID == "rep_copy" || sample.WorkloadID == "rep_fill" {
			instructions, rep = 1_000_000_000_000_001, 20_000_000_000_000_001
		}
		instructions, rep = instructions*uint64(sample.Scale), rep*uint64(sample.Scale)
		native := (instructions + 2*rep + 999_999_999) / 1_000_000_000
		sample.Metered.WorkMeasurement = syntheticReport(instructions, rep)
		sample.NativeBefore, sample.NativeAfter = syntheticNativeResponse(native), syntheticNativeResponse(native)
	}
	got := requireFit(t, dataset, options)
	if !got.Passed || got.CandidatePolicy == nil || got.CandidatePolicy.InstructionNSNumerator != 1 || got.CandidatePolicy.REPByteNSNumerator != 2 {
		t.Fatalf("large counters lost their exact rational model: %+v", got)
	}
	for _, row := range got.Cases {
		if row.WorkloadID == "branch" && row.Scale == 1 && row.EstimatedNs != 9_007_201 {
			t.Fatalf("one instruction beyond a rounding boundary disappeared: %+v", row)
		}
	}
}

func TestReferenceIntegerStatisticsAndQuantizationBoundaries(t *testing.T) {
	if got := referenceMedian([]uint64{math.MaxUint64 - 1, math.MaxUint64}); got != math.MaxUint64 {
		t.Fatalf("median midpoint overflowed or rounded down: %d", got)
	}
	if referenceExceedsPPM(math.MaxUint64, math.MaxUint64, 1_000_000) || !referenceExceedsPPM(math.MaxUint64, math.MaxUint64-1, 1_000_000) {
		t.Fatal("exact PPM comparison lost the strict boundary above float64 precision")
	}
	if referencePPM(1, 3) != 333_334 || !referenceExceedsPPM(1, 3, 333_333) || referenceExceedsPPM(1, 3, 333_334) {
		t.Fatal("display rounding changed the rational error gate")
	}
	if got, err := quantizeReferenceCoefficient(big.NewRat(1, 2_000_000_000)); err != nil || got != 1 {
		t.Fatalf("positive half did not round up: %d, %v", got, err)
	}
	if _, err := quantizeReferenceCoefficient(big.NewRat(1, 2_000_000_001)); err == nil {
		t.Fatal("coefficient rounded to zero was accepted")
	}
	maximum := new(big.Rat).SetFrac(new(big.Int).SetUint64(math.MaxUint64), big.NewInt(1_000_000_000))
	if got, err := quantizeReferenceCoefficient(maximum); err != nil || got != math.MaxUint64 {
		t.Fatalf("maximum numerator lost precision: %d, %v", got, err)
	}
	maximum.Add(maximum, big.NewRat(1, 1_000_000_000))
	if _, err := quantizeReferenceCoefficient(maximum); err == nil {
		t.Fatal("numerator overflow was accepted")
	}
}

func TestFitRequiresExplicitValidOptions(t *testing.T) {
	for _, edit := range []func(*FitOptions){
		func(o *FitOptions) { o.MinRepetitions = 2 },
		func(o *FitOptions) { o.MinRepetitions = 10 },
		func(o *FitOptions) { o.MinNativeNs = 0 },
		func(o *FitOptions) { o.ModelID = "" },
		func(o *FitOptions) { o.ModelVersion = 0 },
		func(o *FitOptions) { o.ReferenceID = "" },
	} {
		options := syntheticFitOptions()
		edit(&options)
		got, err := Fit(syntheticDataset(), options)
		if err == nil || got.Passed || got.CandidatePolicy != nil || got.Options != options {
			t.Fatalf("invalid options acquired defaults: %+v, %v", got, err)
		}
	}
}

func TestFitComparesLatencyAndInstructionsOnIdenticalTriplets(t *testing.T) {
	dataset := syntheticDataset()
	dataset.LatencyBoundVersion = 1
	for i := range dataset.Samples {
		sample := &dataset.Samples[i]
		report := sample.Metered.WorkMeasurement
		cyclesX16 := 8 * *report.Instructions
		if sample.Split == SplitValidation {
			cyclesX16 = 4 * *report.Instructions
		}
		report.LatencyBound = &durante.LatencyBound{Version: 1, CyclesX16: cyclesX16}
		ns := 2*cyclesX16 + 3**report.REPBytes
		sample.NativeBefore, sample.NativeAfter = syntheticNativeResponse(ns), syntheticNativeResponse(ns)
	}

	options := syntheticFitOptions()
	baseline := requireFit(t, dataset, options)
	if baseline.Passed || baseline.CandidatePolicy == nil || baseline.CandidatePolicy.InstructionNSNumerator != 16*coefficientDenominator ||
		!strings.Contains(strings.Join(baseline.Failures, "\n"), "held-out error") {
		t.Fatalf("instruction model must expose its held-out error: %+v", baseline)
	}
	options.Method = durante.TimeMethodLatencyBoundREP
	got := requireFit(t, dataset, options)
	policy := got.CandidatePolicy
	if !got.Passed || policy == nil || policy.Method != options.Method || policy.LatencyX16NSNumerator != 2*coefficientDenominator ||
		policy.InstructionNSNumerator != 0 || policy.REPByteNSNumerator != 3*coefficientDenominator {
		t.Fatalf("latency model did not recover its known weights: %+v", got)
	}
	for _, c := range got.Cases {
		if c.AbsoluteErrorNs != 0 || c.LatencyBoundSpreadPPM == nil || *c.LatencyBoundSpreadPPM != 0 {
			t.Fatalf("wrong prediction or missing observed zero spread: %+v", c)
		}
	}

	// Perturb one held-out count without changing training. Allow arbitrary
	// prediction errors here to test the candidate's separate stability gate.
	for i := range dataset.Samples {
		if sample := &dataset.Samples[i]; sample.WorkloadID == "branch" && sample.Scale == 1 && sample.Repetition == 2 {
			sample.Metered.WorkMeasurement.LatencyBound.CyclesX16 *= 2
		}
	}
	options.MaxErrorPPM = math.MaxUint64
	unstable := requireFit(t, dataset, options)
	if unstable.Passed || unstable.CandidatePolicy == nil || *unstable.CandidatePolicy != *policy ||
		!strings.Contains(strings.Join(unstable.Failures, "\n"), "branch scale 1: latency bound spread exceeds") {
		t.Fatalf("unstable latency should fail its gate and preserve training coefficients: %+v", unstable)
	}
	options.Method = durante.TimeMethod
	if result := requireFit(t, dataset, options); !result.Passed {
		t.Fatalf("latency spread changed qualification of the instruction method: %+v", result)
	}
}

func TestReferenceRequiresConsistentLatencyCapability(t *testing.T) {
	options := syntheticFitOptions()
	options.Method = durante.TimeMethodLatencyBoundREP
	if _, err := Fit(syntheticDataset(), options); err == nil {
		t.Fatal("latency method accepted a dataset collected without a latency counter")
	}
	for _, tc := range []struct {
		name    string
		version int
		bound   *durante.LatencyBound
	}{
		{"unrequested counter", 0, &durante.LatencyBound{Version: 1, CyclesX16: 4000}},
		{"missing counter", 1, nil},
		{"unknown dataset version", 2, nil},
		{"unknown report version", 1, &durante.LatencyBound{Version: 2, CyclesX16: 4000}},
		{"below issue floor", 1, &durante.LatencyBound{Version: 1, CyclesX16: 3999}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := syntheticDataset()
			data.LatencyBoundVersion = tc.version
			if tc.version == 1 {
				for i := range data.Samples {
					report := data.Samples[i].Metered.WorkMeasurement
					report.LatencyBound = &durante.LatencyBound{Version: 1, CyclesX16: 4 * *report.Instructions}
				}
			}
			data.Samples[0].Metered.WorkMeasurement.LatencyBound = tc.bound
			if err := validateCollectedMetered(data.Samples[0].Metered, data); err == nil {
				t.Fatal("collection accepted inconsistent latency metadata")
			}
			for _, method := range []string{durante.TimeMethod, durante.TimeMethodLatencyBoundREP} {
				options.Method = method
				if evaluation, err := Fit(data, options); err == nil || evaluation.Passed || evaluation.CandidatePolicy != nil {
					t.Fatalf("%s fitted inconsistent observations: %+v, %v", method, evaluation, err)
				}
			}
		})
	}
}

func TestReferenceMemoryOperandBytesMatchDataset(t *testing.T) {
	dataset := syntheticDataset()
	report := dataset.Samples[0].Metered.WorkMeasurement
	report.EngineProfile = "dynamorio-11.3-durante-v11-vm32m"
	report.LatencyBound = &durante.LatencyBound{Version: 1, CyclesX16: 4 * *report.Instructions}
	report.MemoryOperandBytes = &durante.MemoryOperandBytes{Version: 1, Bytes: 0}
	if err := validateReferenceCounters(report, 1, 1); err != nil {
		t.Fatalf("observed zero must survive collection and fit: %v", err)
	}
	for _, version := range []int{0, 2} {
		if err := validateReferenceCounters(report, 1, version); err == nil {
			t.Fatalf("accepted unexpected memory counter with dataset version %d", version)
		}
	}
	report.MemoryOperandBytes = nil
	if err := validateReferenceCounters(report, 1, 1); err == nil {
		t.Fatal("accepted absent memory count in a v1 dataset")
	}
	if err := validateReferenceCounters(report, 1, 0); err != nil {
		t.Fatalf("rejected historical latency-only sample: %v", err)
	}
}
