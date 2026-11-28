package reference

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sort"

	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/runvalidation"
)

const coefficientDenominator = uint64(1_000_000_000)

type referenceCase struct {
	workload Workload
	fixture  Case
	samples  []*Sample
	median   uint64
}

// Fit evaluates the whole fixed corpus without removing inconvenient samples.
// Invalid observations return an error; measured instability or an unsuitable
// two-coefficient model returns a failed evaluation with explicit reasons.
func Fit(dataset Dataset, options FitOptions) (Evaluation, error) {
	return evaluateReference(dataset, options, nil, nil)
}

func evaluateReference(dataset Dataset, options FitOptions, external *ValidationCorpus, frozen *durante.TimePolicy) (Evaluation, error) {
	evaluation := Evaluation{Schema: Schema, Authority: durante.Authority, Options: options}
	invalid := func(err error) (Evaluation, error) {
		evaluation.Failures = append(evaluation.Failures, err.Error())
		return evaluation, err
	}
	useLatency := options.Method == durante.TimeMethodLatencyBoundREP
	if frozen == nil && options.Method != "" && options.Method != durante.TimeMethod && !useLatency {
		return invalid(fmt.Errorf("reference fitting supports only %s and %s", durante.TimeMethod, durante.TimeMethodLatencyBoundREP))
	}
	if options.MinRepetitions < 3 || options.MinRepetitions > 9 || options.MinNativeNs == 0 {
		return invalid(fmt.Errorf("fit requires min_repetitions between 3 and 9 and positive min_native_ns"))
	}
	if dataset.Schema != Schema || !dataset.Complete || len(dataset.Failures) != 0 {
		return invalid(fmt.Errorf("reference dataset is incomplete, failed, or has an incompatible schema/corpus"))
	}
	var profile CorpusProfile
	var err error
	if frozen == nil {
		profile, err = referenceDatasetProfile(dataset)
	} else {
		if external == nil || dataset.CorpusID != ExternalCorpusID || dataset.CorpusSHA256 != external.SHA256() {
			return invalid(fmt.Errorf("dataset does not match the external CPP20 validation corpus digest"))
		}
		if err := frozen.Validate(); err != nil {
			return invalid(fmt.Errorf("frozen time policy: %w", err))
		}
		profile, err = referenceCollectionProfile(dataset)
	}
	if err != nil {
		return invalid(err)
	}
	if useLatency && dataset.LatencyBoundVersion != 1 {
		return invalid(fmt.Errorf("%s requires data collected with latency_bound_version 1", options.Method))
	}
	if dataset.Smoke {
		return invalid(fmt.Errorf("smoke observations cannot qualify a reference-time policy"))
	}
	if dataset.Repetitions < options.MinRepetitions || dataset.Repetitions > 9 {
		return invalid(fmt.Errorf("dataset repetitions %d must be between required minimum %d and 9", dataset.Repetitions, options.MinRepetitions))
	}
	if dataset.DispatchProfile != durante.DispatchDefault || !referenceSHA256(dataset.RunnerSHA256) {
		return invalid(fmt.Errorf("reference dataset requires default dispatch and a canonical runner SHA256"))
	}
	runtimeID, err := durante.TimeRuntimeID(dataset.SourceLanguage, dataset.RuntimeImage)
	if err != nil {
		return invalid(err)
	}
	evaluation.RuntimeID = runtimeID
	// These weights only validate report contracts without overflowing even at
	// maximum uint64 counters. They never become a fitted candidate.
	contract := durante.TimePolicy{
		ModelID: options.ModelID, ModelVersion: options.ModelVersion, ReferenceID: options.ReferenceID,
		RuntimeID: runtimeID, Family: profile.Family, EngineProfile: durante.EngineProfile,
		DispatchProfile: durante.DispatchDefault, CPUIDProfile: dataset.CPUIDProfile,
		InstructionNSNumerator: 1, REPByteNSNumerator: 1, NSDenominator: math.MaxUint64,
	}
	if useLatency {
		contract.Method = options.Method
		contract.InstructionNSNumerator = 0
		contract.LatencyX16NSNumerator = 1
	}
	if options.Method == durante.TimeMethodREPBreakdown {
		contract.Method = options.Method
		contract.REPByteNSNumerator = 0
		contract.MOVSByteNSNumerator, contract.STOSByteNSNumerator = 1, 1
	}
	if frozen != nil && (frozen.RuntimeID != runtimeID || frozen.Family != profile.Family || frozen.EngineProfile != durante.EngineProfile ||
		frozen.DispatchProfile != dataset.DispatchProfile || frozen.CPUIDProfile != dataset.CPUIDProfile) {
		return invalid(fmt.Errorf("frozen policy does not match the collected runtime or measurement profile"))
	}
	if err := contract.Validate(); err != nil {
		return invalid(fmt.Errorf("fit options: %w", err))
	}
	pinned := durante.Config{RuntimeDeterminism: dataset.RuntimeDeterminism}
	if dataset.RuntimeDeterminism != "" && pinned.RuntimeDeterminismFor(profiles.NormalizeRunLang(dataset.SourceLanguage)) != dataset.RuntimeDeterminism {
		return invalid(fmt.Errorf("reference dataset records runtime determinism the source launcher does not have"))
	}
	contract = contract.WithRuntimeDeterminism(dataset.RuntimeDeterminism)
	if err := runvalidation.ValidateWorkMeasurementLimits(&model.RunRequest{
		Lang: dataset.SourceLanguage, WorkMeasurement: true,
		Limits: dataset.NativeLimits, WorkMeasurementLimits: &dataset.MeteredLimits,
	}); err != nil {
		return invalid(fmt.Errorf("dataset limits: %w", err))
	}

	var corpus []Workload
	if frozen == nil {
		corpus, err = profile.workloads(false)
	} else {
		corpus, err = external.snapshot()
	}
	if err != nil {
		return invalid(err)
	}
	cases, err := referenceCases(dataset, corpus, contract)
	if err != nil {
		return invalid(err)
	}
	for _, item := range cases {
		native := make([]uint64, 0, 2*len(item.samples))
		minimumNative := uint64(math.MaxUint64)
		instructions, rep := make([]uint64, 0, len(item.samples)), make([]uint64, 0, len(item.samples))
		var latency, movs, stos []uint64
		for _, sample := range item.samples {
			if contract.Formula() == durante.TimeMethodREPBreakdown {
				part := sample.Metered.WorkMeasurement.REPBreakdown
				movs, stos = append(movs, part.MOVSBytes), append(stos, part.STOSBytes)
			}
			if bound := sample.Metered.WorkMeasurement.LatencyBound; bound != nil {
				latency = append(latency, bound.CyclesX16)
			}
			native = append(native, sample.NativeBefore.CPUTimeNs, sample.NativeAfter.CPUTimeNs)
			minimumNative = min(minimumNative, sample.NativeBefore.CPUTimeNs, sample.NativeAfter.CPUTimeNs)
			instructions = append(instructions, *sample.Metered.WorkMeasurement.Instructions)
			rep = append(rep, *sample.Metered.WorkMeasurement.REPBytes)
		}
		item.median = referenceMedian(native)
		nativeRange, instructionRange, repRange := referenceRange(native), referenceRange(instructions), referenceRange(rep)
		instructionMedian, repMedian := referenceMedian(instructions), referenceMedian(rep)
		row := CaseEvaluation{
			WorkloadID: item.workload.ID, Split: item.workload.Split, Scale: item.fixture.Scale,
			NativeMedianNs: item.median, NativeSpreadPPM: referencePPM(nativeRange, item.median),
			InstructionSpreadPPM: referencePPM(instructionRange, instructionMedian), REPSpreadPPM: referencePPM(repRange, repMedian),
		}
		label := fmt.Sprintf("%s scale %d", row.WorkloadID, row.Scale)
		if len(latency) > 0 {
			latencyRange, latencyMedian := referenceRange(latency), referenceMedian(latency)
			spread := referencePPM(latencyRange, latencyMedian)
			row.LatencyBoundSpreadPPM = &spread
			if useLatency && referenceExceedsPPM(latencyRange, latencyMedian, options.MaxWorkSpreadPPM) {
				evaluation.Failures = append(evaluation.Failures, label+": latency bound spread exceeds max_work_spread_ppm")
			}
		}
		if len(movs) != 0 {
			if referenceExceedsPPM(referenceRange(movs), referenceMedian(movs), options.MaxWorkSpreadPPM) {
				evaluation.Failures = append(evaluation.Failures, label+": MOVS byte spread exceeds max_work_spread_ppm")
			}
			if referenceExceedsPPM(referenceRange(stos), referenceMedian(stos), options.MaxWorkSpreadPPM) {
				evaluation.Failures = append(evaluation.Failures, label+": STOS byte spread exceeds max_work_spread_ppm")
			}
		}
		evaluation.Cases = append(evaluation.Cases, row)
		if minimumNative < options.MinNativeNs {
			evaluation.Failures = append(evaluation.Failures, label+": native observation below min_native_ns")
		}
		if referenceExceedsPPM(nativeRange, item.median, options.MaxNativeSpreadPPM) {
			evaluation.Failures = append(evaluation.Failures, label+": native spread exceeds max_native_spread_ppm")
		}
		if referenceExceedsPPM(instructionRange, instructionMedian, options.MaxWorkSpreadPPM) {
			evaluation.Failures = append(evaluation.Failures, label+": instruction spread exceeds max_work_spread_ppm")
		}
		if referenceExceedsPPM(repRange, repMedian, options.MaxWorkSpreadPPM) {
			evaluation.Failures = append(evaluation.Failures, label+": REP spread exceeds max_work_spread_ppm")
		}
	}
	var policy durante.TimePolicy
	if frozen == nil {
		policy, err = fitReferenceCoefficients(cases, contract)
		if err != nil {
			evaluation.Failures = append(evaluation.Failures, err.Error())
			return evaluation, nil
		}
		evaluation.CandidatePolicy = &policy
	} else {
		policy = *frozen
	}
	for index, item := range cases {
		row := &evaluation.Cases[index]
		nativeMin, nativeMax := item.samples[0].NativeBefore.CPUTimeMs, item.samples[0].NativeBefore.CPUTimeMs
		predictedMin, predictedMax := int64(math.MaxInt64), int64(0)
		estimates := 0
		for repetition, sample := range item.samples {
			estimate, err := policy.Estimate(runtimeID, *sample.Metered.WorkMeasurement)
			if err != nil {
				evaluation.Failures = append(evaluation.Failures, fmt.Sprintf("%s scale %d repetition %d: final policy: %v", item.workload.ID, item.fixture.Scale, repetition+1, err))
				continue
			}
			nativeMin = min(nativeMin, sample.NativeBefore.CPUTimeMs, sample.NativeAfter.CPUTimeMs)
			nativeMax = max(nativeMax, sample.NativeBefore.CPUTimeMs, sample.NativeAfter.CPUTimeMs)
			predictedMin = min(predictedMin, estimate.Milliseconds)
			predictedMax = max(predictedMax, estimate.Milliseconds)
			predicted := uint64(estimate.Nanoseconds)
			var absolute uint64
			if predicted >= item.median {
				absolute = predicted - item.median
			} else {
				absolute = item.median - predicted
			}
			// Keep a real repetition, not a fabricated median counter vector.
			// The displayed prediction and error refer to the same worst sample.
			if estimates == 0 || absolute > row.AbsoluteErrorNs {
				row.EstimatedNs = estimate.Nanoseconds
				row.AbsoluteErrorNs = absolute
				row.AbsoluteErrorPPM = referencePPM(absolute, item.median)
				row.Underestimated = predicted < item.median
			}
			if referenceExceedsPPM(absolute, item.median, options.MaxErrorPPM) {
				splitLabel := "training"
				if item.workload.Split == SplitValidation {
					splitLabel = "held-out"
				}
				evaluation.Failures = append(evaluation.Failures, fmt.Sprintf("%s scale %d repetition %d: %s error exceeds max_error_ppm", item.workload.ID, item.fixture.Scale, repetition+1, splitLabel))
			}
			estimates++
		}
		if estimates == len(item.samples) {
			row.ObservedDeadlineDisagreement = deadlineDisagreement(nativeMin, nativeMax, predictedMin, predictedMax)
		}
	}
	evaluation.Passed = len(evaluation.Failures) == 0
	return evaluation, nil
}

// Times here already use the judge's ceiling to whole milliseconds. A time
// equal to the limit passes, so the slower side must exceed the upper endpoint.
func deadlineDisagreement(nativeMin, nativeMax, predictedMin, predictedMax int64) *DeadlineDisagreement {
	var gap DeadlineDisagreement
	switch {
	case predictedMax < nativeMin:
		gap = DeadlineDisagreement{"false_accept", predictedMax, nativeMin - 1}
	case nativeMax < predictedMin:
		gap = DeadlineDisagreement{"false_timeout", nativeMax, predictedMin - 1}
	default:
		return nil
	}
	gap.MinTimeMs = max(1, gap.MinTimeMs)
	gap.MaxTimeMs = min(runvalidation.MaxTimeMs, gap.MaxTimeMs)
	if gap.MinTimeMs > gap.MaxTimeMs {
		return nil
	}
	return &gap
}

func referenceCases(dataset Dataset, corpus []Workload, contract durante.TimePolicy) ([]*referenceCase, error) {
	type key struct {
		workload string
		scale    int
	}
	cases := []*referenceCase{}
	byKey := map[key]*referenceCase{}
	for _, workload := range corpus {
		for _, fixture := range workload.Cases {
			item := &referenceCase{workload: workload, fixture: fixture}
			cases = append(cases, item)
			byKey[key{workload.ID, fixture.Scale}] = item
		}
	}
	// Division avoids overflowing an expected-case-count * repetitions product.
	if len(cases) == 0 || len(dataset.Samples)%len(cases) != 0 || len(dataset.Samples)/len(cases) != dataset.Repetitions {
		return nil, fmt.Errorf("dataset must contain every full corpus case and repetition exactly once")
	}
	for _, item := range cases {
		item.samples = make([]*Sample, dataset.Repetitions)
	}
	binaries := map[string]string{}
	var installation *durante.InstallationIdentity
	for index := range dataset.Samples {
		sample := &dataset.Samples[index]
		label := fmt.Sprintf("sample %d (%s scale %d repetition %d)", index+1, sample.WorkloadID, sample.Scale, sample.Repetition)
		item, ok := byKey[key{sample.WorkloadID, sample.Scale}]
		if !ok || sample.Repetition < 1 || sample.Repetition > dataset.Repetitions {
			return nil, fmt.Errorf("%s: unknown corpus case or repetition", label)
		}
		if item.samples[sample.Repetition-1] != nil {
			return nil, fmt.Errorf("%s: duplicate corpus observation", label)
		}
		if sample.Split != item.workload.Split || sample.SourceSHA256 != referenceDigest(item.workload.Source) || sample.InputSHA256 != referenceDigest(item.fixture.Input) || sample.ExpectedSHA256 != referenceDigest(item.fixture.ExpectedStdout) {
			return nil, fmt.Errorf("%s: corpus split, source, input, or expected output mismatch", label)
		}
		if !referenceSHA256(sample.BinarySHA256) {
			return nil, fmt.Errorf("%s: invalid binary SHA256", label)
		}
		if previous, exists := binaries[sample.WorkloadID]; exists && previous != sample.BinarySHA256 {
			return nil, fmt.Errorf("%s: workload binary changed between observations", label)
		}
		binaries[sample.WorkloadID] = sample.BinarySHA256
		for _, native := range []struct {
			name     string
			response model.RunResponse
		}{{"native_before", sample.NativeBefore}, {"native_after", sample.NativeAfter}} {
			if err := validateReferenceNative(native.response, dataset.NativeLimits); err != nil {
				return nil, fmt.Errorf("%s %s: %w", label, native.name, err)
			}
		}
		if err := validateReferenceMetered(sample.Metered, dataset, contract); err != nil {
			return nil, fmt.Errorf("%s metered: %w", label, err)
		}
		current := sample.Metered.WorkMeasurement.Installation
		if installation != nil && *installation != *current {
			return nil, fmt.Errorf("%s: Durante installation changed between observations", label)
		}
		installation = current
		item.samples[sample.Repetition-1] = sample
	}
	// The cardinality and duplicate checks imply coverage; keep the explicit
	// check so a future corpus change cannot turn a missing row into a nil read.
	for _, item := range cases {
		for repetition, sample := range item.samples {
			if sample == nil {
				return nil, fmt.Errorf("missing %s scale %d repetition %d", item.workload.ID, item.fixture.Scale, repetition+1)
			}
		}
	}
	return cases, nil
}

func validateReferenceCPU(response model.RunResponse, cpuLimitMs, wallLimitMs int) error {
	if response.WorkMeasurementObservation != nil {
		return fmt.Errorf("reference phase contains an automatic work measurement observation")
	}
	if response.CPUTimeNs == 0 || response.CPUAccounting == nil || response.CPUTimeNormalization != nil || response.CPUTimeSampling != nil || response.RawCPUTimeNs != nil || response.RawCPUTimeMs != nil {
		return fmt.Errorf("requires positive unnormalized CPU nanoseconds, accounting metadata, and no automatic sampling")
	}
	// Dataset limits were validated before examining any responses, so this
	// multiplication is bounded by the raw envelope's 600,000 ms ceiling.
	if response.CPUTimeNs > uint64(cpuLimitMs)*1_000_000 {
		return fmt.Errorf("CPU nanoseconds exceed declared execution cap")
	}
	if wallLimitMs > 0 && response.WallTimeMs > int64(wallLimitMs) {
		return fmt.Errorf("wall milliseconds exceed declared execution cap")
	}
	if response.StdoutTruncated || response.StderrTruncated {
		return fmt.Errorf("reference execution truncated its output")
	}
	wantMS := response.CPUTimeNs / 1_000_000
	if response.CPUTimeNs%1_000_000 != 0 {
		wantMS++
	}
	if response.CPUTimeMs != int64(wantMS) || response.TimeMs < 0 || response.WallTimeMs < 0 || response.MemoryKB < 0 || response.ProcessCPUTimeMs < 0 {
		return fmt.Errorf("inconsistent raw resource accounting")
	}
	switch response.CPUAccounting.SelectedSource {
	case "process_clock", "wait_rusage", "cgroup":
	default:
		return fmt.Errorf("unknown CPU accounting source")
	}
	// WaitTargetNs is only populated on the non-cgroup finalization path.
	// Preserve the independent baseline/wait fields without inventing an
	// equality that would reject valid cgroup or process-clock selections.
	return nil
}

func validateReferenceNative(response model.RunResponse, limits model.Limits) error {
	if response.Status != model.RunStatusAccepted || response.ExitCode == nil || *response.ExitCode != 0 || response.VerdictSource != "stdout" || response.Score != nil || response.WorkMeasurement != nil || response.WorkMeasurementExecution != nil || len(response.WorkMeasurements) != 0 || len(response.Steps) != 0 || response.Match != nil {
		return fmt.Errorf("requires an accepted, normally exited unmetered ordinary run with matching output")
	}
	return validateReferenceCPU(response, limits.TimeMs, limits.WallTimeMs)
}

func validateReferenceMetered(response model.RunResponse, dataset Dataset, contract durante.TimePolicy) error {
	d := response.WorkMeasurementExecution
	if response.Status != model.RunStatusInitFail || response.VerdictSource != "durante_complete" || response.Score != nil || response.ExitCode == nil || *response.ExitCode != 0 || len(response.WorkMeasurements) != 0 || len(response.Steps) != 0 || response.Match != nil || d == nil {
		return fmt.Errorf("requires a normally exited complete diagnostic envelope")
	}
	if d.Authority != durante.Authority || d.State != "complete" || d.OutputMatches == nil || !*d.OutputMatches || d.TerminationSource != "stdout" || d.NominalLimits != dataset.NativeLimits || d.RawLimits != dataset.MeteredLimits || d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "" || d.TimeEstimateEligibility != nil {
		return fmt.Errorf("diagnostic output, limits, or envelope contract mismatch")
	}
	if err := validateReferenceCPU(response, dataset.MeteredLimits.CPUTimeMs, dataset.MeteredLimits.WallTimeMs); err != nil {
		return err
	}
	report := response.WorkMeasurement
	if report == nil || !report.ValidInstallationIdentity() {
		return fmt.Errorf("missing or incompatible Durante installation identity")
	}
	if report.RuntimeDeterminism != dataset.RuntimeDeterminism {
		return fmt.Errorf("metered runtime determinism disagrees with dataset")
	}
	if err := validateReferenceCounters(report, dataset.LatencyBoundVersion, dataset.MemoryOperandBytesVersion); err != nil {
		return err
	}
	if _, err := contract.Estimate(contract.RuntimeID, *report); err != nil {
		return fmt.Errorf("incompatible measurement report: %w", err)
	}
	return nil
}

func validateReferenceCounters(report *durante.Report, latencyVersion, memoryVersion int) error {
	if latencyVersion != 0 && latencyVersion != 1 {
		return fmt.Errorf("unsupported dataset latency_bound_version %d", latencyVersion)
	}
	if (latencyVersion == 1) != (report.LatencyBound != nil) || !report.ValidLatencyBound() {
		return fmt.Errorf("metered latency bound is invalid or disagrees with dataset latency_bound_version")
	}
	if memoryVersion != 0 && memoryVersion != 1 {
		return fmt.Errorf("unsupported dataset memory_operand_bytes_version %d", memoryVersion)
	}
	if (memoryVersion == 1) != (report.MemoryOperandBytes != nil) || !report.ValidMemoryOperandBytes() {
		return fmt.Errorf("metered memory operand bytes are invalid or disagree with dataset memory_operand_bytes_version")
	}
	return nil
}

func fitReferenceCoefficients(cases []*referenceCase, template durante.TimePolicy) (durante.TimePolicy, error) {
	useLatency := template.Formula() == durante.TimeMethodLatencyBoundREP
	ww, wr, rr, wt, rt := new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat), new(big.Rat)
	for _, item := range cases {
		if item.workload.Split != SplitTrain {
			continue
		}
		weight := big.NewRat(1, int64(len(item.samples)))
		for _, sample := range item.samples {
			report := sample.Metered.WorkMeasurement
			work := *report.Instructions
			if useLatency {
				work = report.LatencyBound.CyclesX16
			}
			x := new(big.Rat).SetFrac(new(big.Int).SetUint64(work), new(big.Int).SetUint64(item.median))
			y := new(big.Rat).SetFrac(new(big.Int).SetUint64(*report.REPBytes), new(big.Int).SetUint64(item.median))
			ww.Add(ww, new(big.Rat).Mul(weight, new(big.Rat).Mul(x, x)))
			wr.Add(wr, new(big.Rat).Mul(weight, new(big.Rat).Mul(x, y)))
			rr.Add(rr, new(big.Rat).Mul(weight, new(big.Rat).Mul(y, y)))
			wt.Add(wt, new(big.Rat).Mul(weight, x))
			rt.Add(rt, new(big.Rat).Mul(weight, y))
		}
	}
	product := new(big.Rat).Mul(ww, rr)
	determinant := new(big.Rat).Sub(product, new(big.Rat).Mul(wr, wr))
	if determinant.Sign() <= 0 {
		return durante.TimePolicy{}, fmt.Errorf("training counters are rank deficient; both coefficients must be identifiable")
	}
	// This fixed numerical gate rejects almost-collinear feature columns:
	// det/(ww*rr) = 1 - squared cosine must be at least 1e-12. It is not a
	// deployment accuracy threshold; the explicit prediction-error gate is separate.
	if new(big.Rat).Mul(determinant, big.NewRat(1_000_000_000_000, 1)).Cmp(product) < 0 {
		return durante.TimePolicy{}, fmt.Errorf("training counters are ill-conditioned: normalized determinant below 1e-12")
	}
	work := new(big.Rat).Quo(new(big.Rat).Sub(new(big.Rat).Mul(wt, rr), new(big.Rat).Mul(rt, wr)), determinant)
	rep := new(big.Rat).Quo(new(big.Rat).Sub(new(big.Rat).Mul(rt, ww), new(big.Rat).Mul(wt, wr)), determinant)
	if work.Sign() <= 0 || rep.Sign() <= 0 {
		return durante.TimePolicy{}, fmt.Errorf("training fit does not have strictly positive work and REP coefficients")
	}
	workWeight, err := quantizeReferenceCoefficient(work)
	if err != nil {
		return durante.TimePolicy{}, fmt.Errorf("work coefficient: %w", err)
	}
	if useLatency {
		template.LatencyX16NSNumerator = workWeight
	} else {
		template.InstructionNSNumerator = workWeight
	}
	template.REPByteNSNumerator, err = quantizeReferenceCoefficient(rep)
	if err != nil {
		return durante.TimePolicy{}, fmt.Errorf("REP coefficient: %w", err)
	}
	template.NSDenominator = coefficientDenominator
	return template, template.Validate()
}

func quantizeReferenceCoefficient(value *big.Rat) (uint64, error) {
	scaled := new(big.Int).Mul(value.Num(), new(big.Int).SetUint64(coefficientDenominator))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled, value.Denom(), remainder)
	// Positive coefficients round to nearest integer, with exact halves up.
	if new(big.Int).Lsh(remainder, 1).Cmp(value.Denom()) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if quotient.Sign() <= 0 || !quotient.IsUint64() {
		return 0, fmt.Errorf("positive coefficient cannot be represented at denominator 1e9 without zero or uint64 overflow")
	}
	return quotient.Uint64(), nil
}

func referenceMedian(values []uint64) uint64 {
	ordered := append([]uint64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	middle := len(ordered) / 2
	if len(ordered)%2 != 0 {
		return ordered[middle]
	}
	// The native sample count is even. Round its half-nanosecond midpoint up
	// exactly, without overflowing when both central observations are large.
	low, high := ordered[middle-1], ordered[middle]
	return low + (high-low)/2 + (high-low)%2
}

func referenceRange(values []uint64) uint64 {
	low, high := values[0], values[0]
	for _, value := range values[1:] {
		low, high = min(low, value), max(high, value)
	}
	return high - low
}

func referencePPM(numerator, denominator uint64) uint64 {
	if denominator == 0 {
		if numerator == 0 {
			return 0
		}
		return math.MaxUint64 // Undefined positive spread around a zero median.
	}
	scaled := new(big.Int).Mul(new(big.Int).SetUint64(numerator), big.NewInt(1_000_000))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(scaled, new(big.Int).SetUint64(denominator), remainder)
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsUint64() {
		return math.MaxUint64
	}
	return quotient.Uint64()
}

func referenceExceedsPPM(numerator, denominator, threshold uint64) bool {
	if denominator == 0 {
		return numerator != 0
	}
	left := new(big.Int).Mul(new(big.Int).SetUint64(numerator), big.NewInt(1_000_000))
	right := new(big.Int).Mul(new(big.Int).SetUint64(denominator), new(big.Int).SetUint64(threshold))
	return left.Cmp(right) > 0
}

func referenceDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func referenceSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
