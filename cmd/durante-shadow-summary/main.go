// Command durante-shadow-summary aggregates control-plane /execute results.
// Input is JSON Lines, with one {problem_id,response} object per testcase.
// Optional stdin_source records whether the archived request used inline or URL input.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/timing"
)

type cpuCohortIdentity struct {
	durante.TimePolicy
	RuntimeOptionsID    string                        `json:"runtime_options_id,omitempty"`
	RuntimeDeterminism  string                        `json:"runtime_determinism,omitempty"`
	LatencyBoundVersion int                           `json:"latency_bound_version,omitempty"`
	HostLibc            string                        `json:"host_glibc_version,omitempty"`
	DisallowedFeatures  []cpupolicy.Feature           `json:"disallowed_cpu_features,omitempty"`
	Installation        *durante.InstallationIdentity `json:"installation"`
	IntegrityMode       string                        `json:"integrity_mode"`
	NormalizationMethod string                        `json:"native_normalization_method,omitempty"`
	NativeReferenceNs   uint64                        `json:"native_reference_ns,omitempty"`
}

type cpuCohort struct {
	cpuCohortIdentity
	NativeACShadowAC   int `json:"native_ac_shadow_ac"`
	NativeACShadowTLE  int `json:"native_ac_shadow_tle"`
	NativeTLEShadowAC  int `json:"native_tle_shadow_ac"`
	NativeTLEShadowTLE int `json:"native_tle_shadow_tle"`
}

type cpuComparisons struct {
	Cohorts  map[string]*cpuCohort `json:"cohorts"`
	Excluded map[string]int        `json:"excluded"`
}

type counts struct {
	CPUComparison        cpuComparisons `json:"cpu_comparison"`
	Testcases            int            `json:"testcases"`
	PrimaryAC            int            `json:"primary_ac"`
	PrimaryTLE           int            `json:"primary_tle"`
	ComparablePrimaryAC  int            `json:"comparable_primary_ac"`
	ComparablePrimaryTLE int            `json:"comparable_primary_tle"`
	PrimaryACShadowTLE   int            `json:"primary_ac_shadow_virtual_tle"`
	PrimaryTLEShadowAC   int            `json:"primary_tle_shadow_ac"`
	RawStopsBySource     map[string]int `json:"raw_stops_by_source"`
	MissingShadow        int            `json:"missing_shadow"`
	Unavailable          int            `json:"unavailable"`
	Other                int            `json:"other"`
}

func knownStatus(s string) bool {
	switch s {
	case model.RunStatusAccepted, model.RunStatusWA, model.RunStatusTLE, model.RunStatusMLE,
		model.RunStatusWLE, model.RunStatusRE, model.RunStatusInitFail:
		return true
	}
	return false
}

func (c *counts) add(primary model.RunResponse, stdinSource string) error {
	// The verdict summary already checks URL provenance, output, and raw or incomplete stops.
	previous := c.ComparablePrimaryAC + c.ComparablePrimaryTLE
	if err := c.addVerdict(primary, stdinSource); err != nil {
		return err
	}
	if c.CPUComparison.Cohorts == nil {
		c.CPUComparison.Cohorts = make(map[string]*cpuCohort)
		c.CPUComparison.Excluded = make(map[string]int)
	}
	if primary.Status != model.RunStatusAccepted && primary.Status != model.RunStatusTLE {
		c.CPUComparison.Excluded["other_native_verdict"]++
		return nil
	}
	if previous == c.ComparablePrimaryAC+c.ComparablePrimaryTLE {
		c.CPUComparison.Excluded["no_comparable_shadow"]++
		return nil
	}
	block := primary.WorkMeasurementShadow
	if block.StdinSHA256 != "" && !canonicalSHA256(block.StdinSHA256) {
		c.CPUComparison.Excluded["unverified_shared_input"]++
		return nil
	}
	inner := block.Response
	if (inner.Status == model.RunStatusAccepted && (inner.Score == nil || *inner.Score != 1)) ||
		(inner.Status == model.RunStatusTLE && inner.Score != nil) {
		c.CPUComparison.Excluded["inconsistent_shadow_score"]++
		return nil
	}
	if reason := nativeCPUExclusion(primary, inner.WorkMeasurementExecution); reason != "" {
		c.CPUComparison.Excluded[reason]++
		return nil
	}
	cohort, ok := verifiedCPUModel(inner)
	if !ok {
		c.CPUComparison.Excluded["unverified_model"]++
		return nil
	}
	if norm := primary.CPUTimeNormalization; norm != nil {
		cohort.NormalizationMethod, cohort.NativeReferenceNs = norm.Method, norm.ReferenceTimeNs
	}
	encoded, err := json.Marshal(cohort)
	if err != nil {
		return errors.New("cannot encode CPU comparison cohort")
	}
	digest := sha256.Sum256(encoded)
	key := hex.EncodeToString(digest[:])
	row := c.CPUComparison.Cohorts[key]
	if row == nil {
		row = &cpuCohort{cpuCohortIdentity: cohort}
		c.CPUComparison.Cohorts[key] = row
	}
	switch {
	case primary.Status == model.RunStatusAccepted && inner.Status == model.RunStatusAccepted:
		row.NativeACShadowAC++
	case primary.Status == model.RunStatusAccepted:
		row.NativeACShadowTLE++
	case inner.Status == model.RunStatusAccepted:
		row.NativeTLEShadowAC++
	default:
		row.NativeTLEShadowTLE++
	}
	return nil
}

func canonicalSHA256(value string) bool {
	if len(value) != 2*sha256.Size {
		return false
	}
	digest, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(digest) == value
}

func ceilMillis(ns uint64) int64 {
	if ns == 0 {
		return 0
	}
	return int64(1 + (ns-1)/1_000_000)
}

func nativeCPUExclusion(primary model.RunResponse, shadow *model.WorkMeasurementExecution) string {
	if primary.Status == model.RunStatusAccepted {
		if primary.Score != nil || primary.ExitCode == nil || *primary.ExitCode != 0 || primary.VerdictSource != shadow.TerminationSource {
			return "unverified_native_ac"
		}
	} else {
		// With ignore_tle the ordinary checker can also report binary credit
		// for the output already produced before the native CPU stop.
		if primary.Score != nil && *primary.Score != 0 && *primary.Score != 1 {
			return "unverified_native_tle"
		}
		switch primary.VerdictSource {
		case "cpu_time", "cpu_time_cgroup", "cpu_time_final", "cpu_time_cgroup_final":
		case "wall_time":
			return "native_wall_tle"
		case "cpu_rlimit":
			return "native_signal_tle"
		default:
			return "unverified_native_tle"
		}
	}
	// Zero is omitted on the wire. Historical millisecond-only or missing
	// samples cannot establish which side of a nanosecond boundary was hit.
	if primary.CPUTimeNs == 0 {
		return "missing_native_cpu_ns"
	}
	if primary.CPUTimeMs != ceilMillis(primary.CPUTimeNs) ||
		(primary.CPUTimeMs > int64(shadow.NominalLimits.TimeMs)) != (primary.Status == model.RunStatusTLE) {
		return "inconsistent_native_cpu"
	}
	if norm := primary.CPUTimeNormalization; norm != nil {
		calibration, err := timing.NewCPUNormalizer(norm.Method, norm.ReferenceTimeNs, norm.ObservedTimeNs)
		info, enabled := calibration.Info()
		if err != nil || !enabled || norm.Method != timing.CPUNormalizationMethod || norm.ScalePPM != info.ScalePPM ||
			primary.RawCPUTimeMs == nil || primary.RawCPUTimeNs == nil || *primary.RawCPUTimeMs != ceilMillis(*primary.RawCPUTimeNs) ||
			calibration.NormalizeNanoseconds(*primary.RawCPUTimeNs) != primary.CPUTimeNs {
			return "unverified_native_normalization"
		}
	} else if primary.RawCPUTimeNs != nil || primary.RawCPUTimeMs != nil {
		return "unverified_native_normalization"
	}
	return ""
}

// Recompute the archived conversion with the same integer arithmetic used by
// the worker. A label can be reused across versions of a candidate's weights.
func verifiedCPUModel(inner *model.RunResponse) (cpuCohortIdentity, bool) {
	e := inner.WorkMeasurementExecution.TimeEstimate
	report := inner.WorkMeasurement
	language, image, _ := strings.Cut(e.RuntimeID, "@")
	id, err := durante.TimeRuntimeID(language, image)
	family, ok := durante.FamilyForLanguage(language)
	// Judge mode requires a fixed virtual CPU and the pinned runtime options
	// for launchers affected by CPython, PyPy or Node nondeterminism.
	pinned := durante.Config{RuntimeDeterminism: durante.RuntimeDeterminismV1}
	if err != nil || id != e.RuntimeID || !ok || family != e.Family || !durante.ValidCPUIDProfile(e.CPUIDProfile) ||
		e.RuntimeDeterminism != pinned.RuntimeDeterminismFor(profiles.NormalizeRunLang(language)) {
		return cpuCohortIdentity{}, false
	}
	policy := durante.TimePolicy{
		Method: e.Method, ModelID: e.ModelID, ModelVersion: e.ModelVersion, ReferenceID: e.ReferenceID,
		RuntimeID: e.RuntimeID, Family: e.Family, EngineProfile: e.EngineProfile, DispatchProfile: e.DispatchProfile, CPUIDProfile: e.CPUIDProfile,
		InstructionNSNumerator: e.InstructionNSNumerator, REPByteNSNumerator: e.REPByteNSNumerator, LatencyX16NSNumerator: e.LatencyX16NSNumerator,
		MOVSByteNSNumerator: e.MOVSByteNSNumerator, STOSByteNSNumerator: e.STOSByteNSNumerator, NSDenominator: e.NSDenominator,
	}
	result, err := policy.WithRuntimeDeterminism(e.RuntimeDeterminism).Estimate(e.RuntimeID, *report)
	if err != nil || result.Nanoseconds != e.Nanoseconds || result.Milliseconds != e.Milliseconds {
		return cpuCohortIdentity{}, false
	}
	if e.RuntimeOptionsID != "" {
		suffix, ok := strings.CutPrefix(e.RuntimeOptionsID, "runtime-options-v1:sha256:")
		if !ok || !canonicalSHA256(suffix) {
			return cpuCohortIdentity{}, false
		}
	}
	if report.DispatchProfile == durante.DispatchDefault && report.HostLibc != "" {
		return cpuCohortIdentity{}, false
	}
	var latencyVersion int
	if report.LatencyBound != nil {
		latencyVersion = report.LatencyBound.Version
	}
	features := slices.Clone(report.DisallowedCPUFeatures)
	if cpupolicy.Validate(features) != nil {
		return cpuCohortIdentity{}, false
	}
	slices.Sort(features)
	return cpuCohortIdentity{
		TimePolicy: policy, RuntimeOptionsID: e.RuntimeOptionsID, RuntimeDeterminism: e.RuntimeDeterminism,
		LatencyBoundVersion: latencyVersion, HostLibc: report.HostLibc, DisallowedFeatures: features, Installation: report.Installation,
		IntegrityMode: inner.WorkMeasurementJudgment.IntegrityMode,
	}, true
}

func (c *counts) addVerdict(primary model.RunResponse, stdinSource string) error {
	if stdinSource != "" && stdinSource != "inline" && stdinSource != "url" {
		return errors.New("stdin_source must be inline or url when supplied")
	}
	if !knownStatus(primary.Status) || primary.CPUTimeMs < 0 {
		return errors.New("invalid primary verdict or CPU time")
	}
	c.Testcases++
	switch primary.Status {
	case model.RunStatusAccepted:
		c.PrimaryAC++
	case model.RunStatusTLE:
		c.PrimaryTLE++
	}
	block := primary.WorkMeasurementShadow
	if block == nil {
		c.MissingShadow++
		return nil
	}
	if block.Version != 1 || block.Authority != durante.Authority || block.PrimaryStatus != primary.Status || block.PrimaryCPUTimeMs != primary.CPUTimeMs {
		return errors.New("shadow header does not match the primary response")
	}
	inner := block.Response
	if inner == nil {
		if block.Agreement != model.ShadowUnavailable || block.ShadowStatus != "" {
			return errors.New("missing shadow execution for claimed comparison")
		}
		c.Unavailable++
		return nil
	}
	if inner.Status != block.ShadowStatus || !knownStatus(inner.Status) {
		return errors.New("shadow status does not match its execution")
	}
	d, j := inner.WorkMeasurementExecution, inner.WorkMeasurementJudgment
	if j == nil || d == nil {
		if j != nil || d != nil || block.Agreement != model.ShadowUnavailable || inner.Status != model.RunStatusInitFail {
			return errors.New("incomplete evidence for a shadow verdict")
		}
		c.Unavailable++
		return nil
	}
	if j.Verdict != inner.Status || inner.VerdictSource != "durante_judge" || j.Authority != durante.Authority ||
		d.Authority != durante.Authority || j.State != d.State || j.TerminationSource != d.TerminationSource ||
		j.NominalTimeMs <= 0 || j.NominalTimeMs != int64(d.NominalLimits.TimeMs) {
		return errors.New("inconsistent shadow judgment and execution")
	}
	agreement := model.ShadowDisagree
	if primary.Status == inner.Status {
		agreement = model.ShadowAgree
	}
	switch d.State {
	case "resource_exhausted":
		if d.TerminationSource == "" || d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.OutputMatches != nil ||
			(inner.WorkMeasurement != nil && inner.WorkMeasurement.Complete) || (d.TimeEstimateEligibility != nil && d.TimeEstimateEligibility.Eligible) ||
			j.EstimatedTimeNs != nil || j.EstimatedTimeMs != nil {
			return errors.New("raw stop contains an estimate, output decision, or missing cause")
		}
		// Older judges returned TLE and compared that verdict to the primary.
		// A current raw stop has no credited virtual-time verdict.
		if !(inner.Status == model.RunStatusTLE && block.Agreement == agreement ||
			inner.Status == model.RunStatusInitFail && block.Agreement == model.ShadowUnavailable) {
			return errors.New("inconsistent raw-stop comparison")
		}
		source := d.TerminationSource
		switch source {
		case "cpu_time", "cpu_time_cgroup", "cpu_time_final", "cpu_time_cgroup_final", "cpu_rlimit", "wall_time",
			"memory_rss", "memory_reported", "memory_cgroup", "memory_cgroup_final", "address_space", "runtime_memory":
		default:
			source = "other" // Never publish free-form input as a termination cause.
		}
		c.RawStopsBySource[source]++
		return nil
	case "cancelled":
		if inner.Status != model.RunStatusRE || block.Agreement != model.ShadowUnavailable {
			return errors.New("inconsistent cancelled comparison")
		}
		c.Unavailable++
		return nil
	case "failed", "incomplete":
		if inner.Status != model.RunStatusRE || block.Agreement != agreement {
			return errors.New("inconsistent incomplete comparison")
		}
		c.Unavailable++
		return nil
	case "complete":
	default:
		return errors.New("unknown shadow execution state")
	}
	if block.Agreement != agreement || block.Unavailable != "" || d.OutputMatches == nil {
		return errors.New("inconsistent completed comparison")
	}
	if !*d.OutputMatches {
		if inner.Status != model.RunStatusWA {
			return errors.New("shadow output mismatch without wrong-answer verdict")
		}
		c.Other++
		return nil
	}
	if inner.Status == model.RunStatusRE {
		if d.TimeEstimate != nil && j.IntegrityMode != string(durante.JudgeIntegrityPlausibility) {
			return errors.New("unexpected runtime-error verdict with a time estimate")
		}
		c.Unavailable++
		return nil
	}
	exceeded, err := validatedTimeComparison(inner)
	if err != nil {
		return err
	}
	if stdinSource == "inline" && block.StdinSHA256 != "" {
		return errors.New("inline request conflicts with the shared URL input receipt")
	}
	// v5 fetched the primary and shadow URLs independently. Those workers
	// emitted the same response shape for inline and URL inputs.
	if inner.WorkMeasurement.EngineProfile == "dynamorio-11.3-durante-v5-vm32m" && stdinSource != "inline" {
		c.Unavailable++
		return nil
	}
	if stdinSource == "url" {
		if !canonicalSHA256(block.StdinSHA256) {
			return errors.New("URL comparison lacks a canonical shared input SHA-256")
		}
	}
	switch primary.Status {
	case model.RunStatusAccepted:
		c.ComparablePrimaryAC++
		if exceeded {
			c.PrimaryACShadowTLE++
		}
	case model.RunStatusTLE:
		c.ComparablePrimaryTLE++
		if !exceeded {
			c.PrimaryTLEShadowAC++
		}
	default:
		c.Other++
	}
	return nil
}

// validatedTimeComparison checks a finished control-plane response against its
// observation and both copies of the estimate. Model qualification and policy
// selection belong to the control plane that produced the input.
func validatedTimeComparison(inner *model.RunResponse) (bool, error) {
	d, j := inner.WorkMeasurementExecution, inner.WorkMeasurementJudgment
	estimate := d.TimeEstimate
	report := inner.WorkMeasurement
	if j.IntegrityFlagged || (j.IntegrityMode != string(durante.JudgeIntegrityOff) && j.IntegrityMode != string(durante.JudgeIntegrityPlausibility)) {
		return false, errors.New("invalid integrity decision for a virtual-time verdict")
	}
	if report == nil || estimate == nil || d.TimeEstimateUnavailable != "" || d.NominalTimeExceeded == nil || j.EstimatedTimeNs == nil || j.EstimatedTimeMs == nil {
		return false, errors.New("missing evidence for a virtual-time verdict")
	}
	if inner.ExitCode == nil || *inner.ExitCode != 0 || (d.TerminationSource != "stdout" && d.TerminationSource != "file_output") {
		return false, errors.New("virtual-time verdict requires a normal exit and an output comparison")
	}
	if !report.ValidShadowInstallationIdentity() {
		return false, errors.New("incompatible installation for shadow engine profile")
	}
	if estimate.Authority != durante.Authority || estimate.Nanoseconds <= 0 ||
		estimate.Method == "" || estimate.ModelID == "" || estimate.ModelVersion == 0 || estimate.ReferenceID == "" || estimate.RuntimeID == "" ||
		estimate.Family == "" || estimate.EngineProfile == "" || estimate.DispatchProfile == "" {
		return false, errors.New("invalid virtual-time estimate identity")
	}
	if estimate.Family != report.Family || estimate.EngineProfile != report.EngineProfile ||
		estimate.DispatchProfile != report.DispatchProfile || estimate.CPUIDProfile != report.CPUIDProfile ||
		estimate.RuntimeDeterminism != report.RuntimeDeterminism || !report.TimeConversionEligibility(estimate.Method).Eligible {
		return false, errors.New("incompatible observation for declared time estimate")
	}
	if e := d.TimeEstimateEligibility; e != nil && (!e.Eligible || e.Validate() != nil) {
		return false, errors.New("time-conversion eligibility disagrees with the estimate")
	}
	// The API compares integer nanoseconds. At a whole-millisecond limit its
	// rounded-up millisecond estimate yields the same strict comparison without
	// multiplying user-supplied limits, which could otherwise overflow.
	ms := 1 + (estimate.Nanoseconds-1)/1_000_000
	exceeded := ms > j.NominalTimeMs
	if estimate.Milliseconds != ms || *j.EstimatedTimeMs != ms || *j.EstimatedTimeNs != estimate.Nanoseconds ||
		*d.NominalTimeExceeded != exceeded || (exceeded && inner.Status != model.RunStatusTLE) || (!exceeded && inner.Status != model.RunStatusAccepted) {
		return false, errors.New("virtual-time verdict disagrees with estimate or nominal limit")
	}
	return exceeded, nil
}

func problemID(raw json.RawMessage) (string, error) {
	var name string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &name) == nil && strings.TrimSpace(name) != "" {
		return name, nil
	}
	if len(raw) > 0 && strings.Trim(string(raw), "0123456789") == "" {
		return string(raw), nil
	}
	return "", errors.New("expected a nonempty string or unsigned integer problem_id")
}

func summarize(in io.Reader, out io.Writer) error {
	problems := make(map[string]*counts)
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for line := 1; scanner.Scan(); line++ {
		var item struct {
			ProblemID   json.RawMessage   `json:"problem_id"`
			StdinSource string            `json:"stdin_source"`
			Response    model.RunResponse `json:"response"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &item); err != nil {
			// Input and decoder errors can contain participant-controlled text.
			return fmt.Errorf("line %d: invalid testcase JSON or response fields", line)
		}
		id, err := problemID(item.ProblemID)
		if err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		row := problems[id]
		if row == nil {
			row = &counts{RawStopsBySource: make(map[string]int)}
			problems[id] = row
		}
		if err := row.add(item.Response, item.StdinSource); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Problems map[string]*counts `json:"problems"`
	}{problems})
}

func main() {
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: durante-shadow-summary [testcases.jsonl]\nReads one {problem_id,response} object per line from the file or stdin.")
	}
	flag.Parse()
	if flag.NArg() > 1 {
		flag.Usage()
		os.Exit(2)
	}
	input := os.Stdin
	if flag.NArg() == 1 {
		file, err := os.Open(flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer file.Close()
		input = file
	}
	if err := summarize(input, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
