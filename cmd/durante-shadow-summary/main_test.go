package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/timing"
)

func timedResponse(t *testing.T, primary string, ns uint64) model.RunResponse {
	t.Helper()
	zero, verified, match := uint64(0), false, true
	exitCode := 0
	report := &durante.Report{
		Schema: 1, ClientVersion: 1, Complete: true, Authority: durante.Authority,
		Scope: durante.Scope, Metric: durante.Metric, Quality: "uninterrupted",
		EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault, CPUIDProfile: durante.CPUIDProfileX8664V2, Family: "cpp",
		Processes: 1, Images: 1, REPEnabled: true, REPTelemetryStatus: durante.REPMovsStosOnly,
		Instructions: &ns, REPBytes: &zero, REPEntries: &zero, UnsupportedREPEntries: &zero,
		IntegrityVerified: &verified, MeasurementContract: durante.DiagnosticContract(),
		Installation: &durante.InstallationIdentity{
			SourceCommit: durante.SourceCommit, RuntimeSourceCommit: "6847bb210bea0ca11d1bf47704383d6a075da490",
			RuntimePatchSHA256:           durante.RuntimePatchSHA256,
			RuntimeCodeReplayPatchSHA256: durante.RuntimeCodeReplayPatchSHA256,
			LauncherSHA256:               strings.Repeat("a", 64), ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true,
		},
	}
	policy := durante.TimePolicy{
		ModelID: "test", ModelVersion: 1, ReferenceID: "test", RuntimeID: "CPP20@sha256:" + strings.Repeat("a", 64),
		EngineProfile: report.EngineProfile, Family: report.Family, DispatchProfile: report.DispatchProfile, CPUIDProfile: report.CPUIDProfile,
		InstructionNSNumerator: 1, REPByteNSNumerator: 1, NSDenominator: 1,
	}
	estimate, eligibility, err := policy.EstimateWithEligibility(policy.RuntimeID, *report)
	if err != nil {
		t.Fatal(err)
	}
	limit := model.Limits{TimeMs: 1, WallTimeMs: 2, MemoryMB: 128}
	exceeded := ns > 1_000_000
	status := model.RunStatusAccepted
	fullScore := 1.0
	score := &fullScore
	if exceeded {
		status = model.RunStatusTLE
		score = nil
	}
	agreement := model.ShadowDisagree
	if status == primary {
		agreement = model.ShadowAgree
	}
	return model.RunResponse{Status: primary, CPUTimeMs: 2, Stdout: "private-submission-output", WorkMeasurementShadow: &model.WorkMeasurementShadow{
		Version: 1, Authority: durante.Authority, PrimaryStatus: primary, PrimaryCPUTimeMs: 2, ShadowStatus: status, Agreement: agreement,
		Response: &model.RunResponse{Status: status, VerdictSource: "durante_judge", Score: score, WorkMeasurement: report, ExitCode: &exitCode,
			WorkMeasurementExecution: &model.WorkMeasurementExecution{
				Authority: durante.Authority, State: "complete", NominalLimits: limit, RawLimits: durante.ResourceLimits{CPUTimeMs: 100, WallTimeMs: 200, MemoryMB: 512},
				OutputMatches: &match, TerminationSource: "stdout", TimeEstimate: &estimate, TimeEstimateEligibility: &eligibility, NominalTimeExceeded: &exceeded,
			}, WorkMeasurementJudgment: &model.WorkMeasurementJudgment{
				Authority: durante.Authority, State: "complete", Verdict: status, NominalTimeMs: 1, EstimatedTimeNs: &estimate.Nanoseconds, EstimatedTimeMs: &estimate.Milliseconds,
				IntegrityMode: "off", TerminationSource: "stdout",
			},
		},
	}}
}

func rawStop(t *testing.T, source, status string) model.RunResponse {
	r := timedResponse(t, model.RunStatusAccepted, 1_000_001)
	block := r.WorkMeasurementShadow
	block.ShadowStatus = status
	if status == model.RunStatusInitFail {
		block.Agreement, block.Unavailable = model.ShadowUnavailable, "raw resource exhausted"
	}
	inner := block.Response
	inner.Status = status
	inner.Score = nil
	inner.ExitCode = nil
	inner.WorkMeasurement = inner.WorkMeasurement.WithoutTotals("raw limit")
	d, j := inner.WorkMeasurementExecution, inner.WorkMeasurementJudgment
	d.State, d.TerminationSource, d.OutputMatches = "resource_exhausted", source, nil
	d.TimeEstimate, d.NominalTimeExceeded, d.TimeEstimateUnavailable = nil, nil, "execution_resource_exhausted"
	d.TimeEstimateEligibility = &durante.TimeEligibility{Version: 1, ReasonCodes: []string{durante.TimeExecutionResourceStop}}
	j.State, j.Verdict, j.TerminationSource, j.EstimatedTimeNs, j.EstimatedTimeMs = d.State, status, source, nil, nil
	return r
}

func TestSummaryRequiresInputProvenanceForHistoricalTimeComparisons(t *testing.T) {
	r := timedResponse(t, model.RunStatusTLE, 1_000_000)
	inner := r.WorkMeasurementShadow.Response
	report, d := inner.WorkMeasurement, inner.WorkMeasurementExecution
	report.EngineProfile = "dynamorio-11.3-durante-v5-vm32m"
	report.Installation.SourceCommit = "4016b33fc69b93d36a13efbd00953213b8f6317f"
	report.Installation.RuntimePatchSHA256 = "59fc79b5055a0c42b463703768891f0a10199ef1135d43028826c8c3894bab7c"
	report.Installation.RuntimeCodeReplayPatchSHA256 = "aad9b561fb89e09d52d7a765124226659b8dbd8b886da61bc73d15fde28b5b72"
	d.TimeEstimate.EngineProfile = report.EngineProfile
	d.TimeEstimateEligibility = nil
	d.TerminationSource, inner.WorkMeasurementJudgment.TerminationSource = "file_output", "file_output"
	for _, tc := range []struct {
		source                  string
		comparable, unavailable int
	}{
		{"", 0, 1}, {"url", 0, 1}, {"inline", 1, 0},
	} {
		item := map[string]any{"problem_id": "p", "response": r}
		if tc.source != "" {
			item["stdin_source"] = tc.source
		}
		var input, output bytes.Buffer
		if err := json.NewEncoder(&input).Encode(item); err != nil {
			t.Fatal(err)
		}
		if err := summarize(&input, &output); err != nil {
			t.Fatal(err)
		}
		var summary struct {
			Problems map[string]counts `json:"problems"`
		}
		if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		got := summary.Problems["p"]
		if got.Testcases != 1 || got.PrimaryTLE != 1 || got.PrimaryTLEShadowAC != tc.comparable || got.ComparablePrimaryTLE != tc.comparable || got.Unavailable != tc.unavailable {
			t.Fatalf("historical file output, stdin source %q: %+v", tc.source, got)
		}
	}
}

func TestSummaryChecksExplicitInputSourceAgainstCurrentTimeReceipts(t *testing.T) {
	digest := strings.Repeat("ab", 32)
	for _, tc := range []struct {
		source, receipt string
		valid           bool
	}{
		{"url", digest, true}, {"url", "", false}, {"url", strings.ToUpper(digest), false},
		{"url", digest[:60], false}, {"inline", digest, false}, {"inline", "", true}, {"", digest, true},
	} {
		r := timedResponse(t, model.RunStatusAccepted, 1_000_001)
		r.WorkMeasurementShadow.StdinSHA256 = tc.receipt
		var row counts
		err := row.add(r, tc.source)
		if (err == nil) != tc.valid || (tc.valid && row.PrimaryACShadowTLE != 1) {
			t.Fatalf("source %q, receipt %q: %+v, error %v", tc.source, tc.receipt, row, err)
		}
	}
}

func TestSummaryCountsDirectionalDisagreementsAndRawStopsSeparately(t *testing.T) {
	var input, output bytes.Buffer
	encoder := json.NewEncoder(&input)
	add := func(id any, r model.RunResponse) {
		t.Helper()
		if err := encoder.Encode(map[string]any{"problem_id": id, "response": r}); err != nil {
			t.Fatal(err)
		}
	}
	add(42, timedResponse(t, model.RunStatusAccepted, 1_000_000))
	add("42", timedResponse(t, model.RunStatusAccepted, 1_000_001))
	add("42", timedResponse(t, model.RunStatusTLE, 1_000_000))
	add("42", timedResponse(t, model.RunStatusTLE, 1_000_001))
	legacyStop := rawStop(t, "wall_time", model.RunStatusTLE)
	legacyReport := legacyStop.WorkMeasurementShadow.Response.WorkMeasurement
	legacyReport.EngineProfile = "dynamorio-11.3-durante-v5-vm32m"
	legacyReport.Installation.SourceCommit = "4016b33fc69b93d36a13efbd00953213b8f6317f"
	legacyReport.Installation.RuntimePatchSHA256 = "59fc79b5055a0c42b463703768891f0a10199ef1135d43028826c8c3894bab7c"
	legacyReport.Installation.RuntimeCodeReplayPatchSHA256 = "aad9b561fb89e09d52d7a765124226659b8dbd8b886da61bc73d15fde28b5b72"
	add("42", legacyStop)
	add("42", rawStop(t, "memory_rss", model.RunStatusInitFail))
	add("42", rawStop(t, "cpu_time_final", model.RunStatusInitFail))
	add("42", rawStop(t, "private-submission-output", model.RunStatusInitFail))
	add("42", model.RunResponse{Status: model.RunStatusAccepted, WorkMeasurementShadow: &model.WorkMeasurementShadow{
		Version: 1, Authority: durante.Authority, PrimaryStatus: model.RunStatusAccepted, Agreement: model.ShadowUnavailable, Unavailable: "private-submission-output",
	}})
	add("42", model.RunResponse{Status: model.RunStatusRE})
	add("separate", timedResponse(t, model.RunStatusWA, 1_000_000))
	if err := summarize(&input, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "private-submission-output") {
		t.Fatalf("summary disclosed input: %s", &output)
	}
	var got struct {
		Problems map[string]counts `json:"problems"`
	}
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := counts{CPUComparison: cpuComparisons{Cohorts: map[string]*cpuCohort{}, Excluded: map[string]int{
		"no_comparable_shadow": 5, "other_native_verdict": 1, "unverified_native_ac": 2, "unverified_native_tle": 2,
	}}, Testcases: 10, PrimaryAC: 7, PrimaryTLE: 2, ComparablePrimaryAC: 2, ComparablePrimaryTLE: 2, PrimaryACShadowTLE: 1, PrimaryTLEShadowAC: 1,
		RawStopsBySource: map[string]int{"wall_time": 1, "memory_rss": 1, "cpu_time_final": 1, "other": 1}, Unavailable: 1, MissingShadow: 1}
	if !reflect.DeepEqual(got.Problems["42"], want) || len(got.Problems) != 2 || got.Problems["separate"].Other != 1 {
		t.Fatalf("summary %+v", got.Problems)
	}
}

func TestSummaryRejectsInconsistentRecordsWithoutPartialOutput(t *testing.T) {
	for name, change := range map[string]func(*model.RunResponse){
		"primary echo": func(r *model.RunResponse) { r.Status = model.RunStatusTLE },
		"missing estimate": func(r *model.RunResponse) {
			r.WorkMeasurementShadow.Response.WorkMeasurementExecution.TimeEstimate = nil
		},
		"false threshold": func(r *model.RunResponse) {
			*r.WorkMeasurementShadow.Response.WorkMeasurementExecution.NominalTimeExceeded = true
		},
		"wrong units": func(r *model.RunResponse) {
			r.WorkMeasurementShadow.Response.WorkMeasurementExecution.TimeEstimate.Milliseconds++
		},
		"missing report":     func(r *model.RunResponse) { r.WorkMeasurementShadow.Response.WorkMeasurement = nil },
		"label disagreement": func(r *model.RunResponse) { r.WorkMeasurementShadow.Agreement = model.ShadowDisagree },
		"missing exit":       func(r *model.RunResponse) { r.WorkMeasurementShadow.Response.ExitCode = nil },
		"nonzero exit":       func(r *model.RunResponse) { *r.WorkMeasurementShadow.Response.ExitCode = 1 },
		"raw stop presented as complete": func(r *model.RunResponse) {
			inner := r.WorkMeasurementShadow.Response
			inner.WorkMeasurementExecution.TerminationSource, inner.WorkMeasurementJudgment.TerminationSource = "wall_time", "wall_time"
		},
		"missing installation": func(r *model.RunResponse) { r.WorkMeasurementShadow.Response.WorkMeasurement.Installation = nil },
		"changed installation": func(r *model.RunResponse) {
			r.WorkMeasurementShadow.Response.WorkMeasurement.Installation.Unchanged = false
		},
		"wrong profile for worker": func(r *model.RunResponse) {
			inner := r.WorkMeasurementShadow.Response
			inner.WorkMeasurement.EngineProfile = "dynamorio-11.3-durante-v5-vm32m"
			inner.WorkMeasurementExecution.TimeEstimate.EngineProfile = inner.WorkMeasurement.EngineProfile
		},
	} {
		t.Run(name, func(t *testing.T) {
			good := timedResponse(t, model.RunStatusAccepted, 1_000_000)
			bad := timedResponse(t, model.RunStatusAccepted, 1_000_000)
			change(&bad)
			var in, out bytes.Buffer
			for _, r := range []model.RunResponse{good, bad} {
				if err := json.NewEncoder(&in).Encode(map[string]any{"problem_id": "p", "response": r}); err != nil {
					t.Fatal(err)
				}
			}
			err := summarize(&in, &out)
			if err == nil || !strings.Contains(err.Error(), "line 2") || out.Len() != 0 || strings.Contains(err.Error(), "private-submission-output") {
				t.Fatalf("invalid result: %v output=%s", err, &out)
			}
		})
	}
}

func cpuTimedResponse(t *testing.T, status string, native, shadow uint64) model.RunResponse {
	t.Helper()
	r := timedResponse(t, status, shadow)
	r.CPUTimeNs, r.CPUTimeMs = native, ceilMillis(native)
	r.WorkMeasurementShadow.PrimaryCPUTimeMs = r.CPUTimeMs
	r.VerdictSource = "cpu_time_final"
	if status == model.RunStatusAccepted {
		exit := 0
		r.ExitCode, r.VerdictSource = &exit, "stdout"
	}
	return r
}

func TestCPUComparisonAtNativeAndVirtualBoundaries(t *testing.T) {
	var row counts
	ac, tle := model.RunStatusAccepted, model.RunStatusTLE
	for _, native := range []uint64{1_000_000, 1_000_001} {
		for _, shadow := range []uint64{1_000_000, 1_000_001} {
			status := ac
			if native > 1_000_000 {
				status = tle
			}
			if err := row.add(cpuTimedResponse(t, status, native, shadow), "inline"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(row.CPUComparison.Excluded) != 0 || len(row.CPUComparison.Cohorts) != 1 {
		t.Fatal(row.CPUComparison)
	}
	for _, c := range row.CPUComparison.Cohorts {
		if c.NativeACShadowAC != 1 || c.NativeACShadowTLE != 1 || c.NativeTLEShadowAC != 1 || c.NativeTLEShadowTLE != 1 {
			t.Fatal(c)
		}
	}
}

func TestCompletedNativeTimeAccuracy(t *testing.T) {
	var row counts
	// Every native AC and shadow AC below has the same rounded duration, yet
	// the underlying nanoseconds span both boundaries.
	for _, ns := range []uint64{399_999, 400_000, 500_000, 600_000, 600_001} {
		if err := row.add(cpuTimedResponse(t, model.RunStatusAccepted, 500_000, ns), "inline"); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []model.RunResponse{
		cpuTimedResponse(t, model.RunStatusAccepted, 1_000_000, 1_200_001), // completed shadow TLE
		cpuTimedResponse(t, model.RunStatusTLE, 1_000_001, 400_000),        // interrupted native run
	} {
		if err := row.add(r, "inline"); err != nil {
			t.Fatal(err)
		}
	}
	want := completedTimeAccuracy{UnderByMoreThan20Percent: 1, Within20Percent: 3, OverByMoreThan20Percent: 2}
	if len(row.CPUComparison.Cohorts) != 1 {
		t.Fatal(row.CPUComparison)
	}
	for _, cohort := range row.CPUComparison.Cohorts {
		if cohort.CompletedTimeAccuracy != want || cohort.NativeTLEShadowAC != 1 || cohort.NativeACShadowTLE != 1 {
			t.Fatal(cohort)
		}
	}
}

func TestCompletedNativeTimeAccuracyFractionalTolerance(t *testing.T) {
	var accuracy completedTimeAccuracy
	for _, estimate := range []uint64{8, 9, 13, 14} {
		accuracy.add(11, estimate)
	}
	accuracy.add(^uint64(0), 1)
	accuracy.add(1, ^uint64(0))
	want := completedTimeAccuracy{UnderByMoreThan20Percent: 2, Within20Percent: 2, OverByMoreThan20Percent: 2}
	if accuracy != want {
		t.Fatal(accuracy)
	}
}

func TestNativeTLETimeBound(t *testing.T) {
	const ac, tle = model.RunStatusAccepted, model.RunStatusTLE
	// These two CPU samples cross the same 1 ms limit, with very different
	// overshoots. Only the strictly crossed deadline enters the bound.
	var row counts
	for _, item := range []struct {
		status            string
		native, estimated uint64
		sampled           bool
	}{
		{tle, 1_000_001, 800_000, false},
		{tle, 9_000_000, 800_001, false},
		{tle, 1_000_001, 1_000_001, false},
		{tle, 1_000_001, 1, true},
		{ac, 900_000, 500_000, false},
	} {
		r := cpuTimedResponse(t, item.status, item.native, item.estimated)
		if item.sampled {
			r.CPUTimeSampling = &model.CPUTimeSampling{Method: "short-case-median-v1", Samples: []model.CPUTimeSample{
				{Status: ac, CPUTimeNs: 900_000, CPUTimeMs: 1},
				{Status: tle, CPUTimeNs: item.native, CPUTimeMs: 2},
			}}
		}
		if err := row.add(r, "inline"); err != nil {
			t.Fatal(err)
		}
	}
	if len(row.CPUComparison.Cohorts) != 1 || len(row.CPUComparison.Excluded) != 0 {
		t.Fatal(row.CPUComparison)
	}
	for _, c := range row.CPUComparison.Cohorts {
		want := nativeTLETimeBound{UnderByMoreThan20Percent: 1, Inconclusive: 2, Sampled: 1}
		if c.NativeTLETimeBound != want || c.CompletedTimeAccuracy.UnderByMoreThan20Percent != 1 ||
			c.NativeTLEShadowAC != 3 || c.NativeTLEShadowTLE != 1 {
			t.Fatal(c)
		}
	}
}

func TestNativeTLEBoundAtLargestCrossedLimit(t *testing.T) {
	maxNative := ^uint64(0)
	limit := int(ceilMillis(maxNative) - 1)
	if int64(limit) != ceilMillis(maxNative)-1 {
		t.Skip("this architecture has smaller time limits")
	}
	r := cpuTimedResponse(t, model.RunStatusTLE, maxNative, 800_000)
	inner := r.WorkMeasurementShadow.Response
	inner.WorkMeasurementExecution.NominalLimits.TimeMs = limit
	inner.WorkMeasurementJudgment.NominalTimeMs = int64(limit)
	var row counts
	if err := row.add(r, "inline"); err != nil {
		t.Fatal(err)
	}
	if len(row.CPUComparison.Cohorts) != 1 {
		t.Fatal(row.CPUComparison)
	}
	for _, c := range row.CPUComparison.Cohorts {
		if c.NativeTLETimeBound.UnderByMoreThan20Percent != 1 {
			t.Fatal(c.NativeTLETimeBound)
		}
	}
}

func TestNativeTLETimeBoundUsesValidatedNormalizedCPU(t *testing.T) {
	r := cpuTimedResponse(t, model.RunStatusTLE, 1_200_000, 800_000)
	raw, rawMs := uint64(600_000), int64(1)
	r.RawCPUTimeNs, r.RawCPUTimeMs = &raw, &rawMs
	r.CPUTimeNormalization = &model.CPUTimeNormalization{Method: timing.CPUNormalizationMethod,
		ReferenceTimeNs: 2, ObservedTimeNs: 1, ScalePPM: 2_000_000}
	var row counts
	if err := row.add(r, "inline"); err != nil {
		t.Fatal(err)
	}
	// The second record's raw time fails to reproduce its normalized time.
	raw++
	if err := row.add(r, "inline"); err != nil {
		t.Fatal(err)
	}
	if len(row.CPUComparison.Cohorts) != 1 || row.CPUComparison.Excluded["unverified_native_normalization"] != 1 {
		t.Fatal(row.CPUComparison)
	}
	for _, c := range row.CPUComparison.Cohorts {
		if c.NativeTLETimeBound != (nativeTLETimeBound{UnderByMoreThan20Percent: 1}) {
			t.Fatal(c)
		}
	}
}

func TestCPUComparisonNativeEvidence(t *testing.T) {
	ac, tle := model.RunStatusAccepted, model.RunStatusTLE
	for _, f := range []struct {
		name      string
		status    string
		source    string
		ns        uint64
		change    func(*model.RunResponse)
		exclusion string
	}{
		{"CPU polling", tle, "cpu_time", 1_000_001, nil, ""},
		{"cgroup polling", tle, "cpu_time_cgroup", 1_000_001, nil, ""},
		{"final cgroup", tle, "cpu_time_cgroup_final", 1_000_001, nil, ""},
		{"wall", tle, "wall_time", 2_000_000, nil, "native_wall_tle"},
		{"signal", tle, "cpu_rlimit", 2_000_000, nil, "native_signal_tle"},
		{"legacy source", tle, "", 2_000_000, nil, "unverified_native_tle"},
		{"at CPU limit", tle, "cpu_time_final", 1_000_000, nil, "inconsistent_native_cpu"},
		{"over CPU limit", ac, "stdout", 1_000_001, nil, "inconsistent_native_cpu"},
		{"missing precise sample", ac, "stdout", 0, nil, "missing_native_cpu_ns"},
		{"rounded falsely", tle, "cpu_time", 1_000_001, func(r *model.RunResponse) { r.CPUTimeMs = 1; r.WorkMeasurementShadow.PrimaryCPUTimeMs = 1 }, "inconsistent_native_cpu"},
		{"missing exit", ac, "stdout", 999_999, func(r *model.RunResponse) { r.ExitCode = nil }, "unverified_native_ac"},
		{"wrong primary output", ac, "file_output", 999_999, nil, "unverified_native_ac"},
		{"nonpublic source", tle, "private-submission-output", 1_000_001, nil, "unverified_native_tle"},
		{"missing normalization", ac, "stdout", 800_000, func(r *model.RunResponse) { r.RawCPUTimeNs = &r.CPUTimeNs }, "unverified_native_normalization"},
	} {
		t.Run(f.name, func(t *testing.T) {
			r := cpuTimedResponse(t, f.status, f.ns, 999_999)
			r.VerdictSource = f.source
			if f.change != nil {
				f.change(&r)
			}
			var row counts
			if err := row.add(r, "inline"); err != nil {
				t.Fatal(err)
			}
			if row.ComparablePrimaryAC+row.ComparablePrimaryTLE != 1 {
				t.Fatal("legacy comparison changed")
			}
			if f.exclusion != "" {
				if len(row.CPUComparison.Cohorts) != 0 || row.CPUComparison.Excluded[f.exclusion] != 1 {
					t.Fatalf("%+v", row.CPUComparison)
				}
			} else if len(row.CPUComparison.Cohorts) != 1 || len(row.CPUComparison.Excluded) != 0 {
				t.Fatal(row.CPUComparison)
			}
		})
	}
}

func TestCPUComparisonSeparatesEffectivePolicies(t *testing.T) {
	var row counts
	for i := range 9 {
		r := cpuTimedResponse(t, model.RunStatusAccepted, 999_999, 1_000_001)
		inner := r.WorkMeasurementShadow.Response
		e := inner.WorkMeasurementExecution.TimeEstimate
		switch i {
		case 0, 1: // Two cases share one complete configuration.
		case 2:
			e.InstructionNSNumerator, e.NSDenominator = 2, 2
		case 3:
			e.ModelVersion++
		case 4:
			e.ReferenceID = "another-reference"
		case 5:
			e.RuntimeID = "CPP20@sha256:" + strings.Repeat("d", 64)
		case 6:
			e.RuntimeOptionsID = "runtime-options-v1:sha256:" + strings.Repeat("1", 64)
		case 7:
			inner.WorkMeasurement.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.AVX}
		case 8:
			inner.WorkMeasurement.Installation.ClientSHA256 = strings.Repeat("2", 64)
		}
		if err := row.add(r, "inline"); err != nil {
			t.Fatal(err)
		}
	}
	if len(row.CPUComparison.Cohorts) != 8 || len(row.CPUComparison.Excluded) != 0 {
		t.Fatal(row.CPUComparison)
	}
	var doubled int
	for _, c := range row.CPUComparison.Cohorts {
		if c.NativeACShadowTLE == 2 {
			doubled++
		} else if c.NativeACShadowTLE != 1 {
			t.Fatal(c)
		}
	}
	if doubled != 1 {
		t.Fatal("mixed same-label policies")
	}
}

func TestCPUComparisonRecomputesArchivedCoefficientsAndCalibration(t *testing.T) {
	for _, f := range []struct {
		name, excluded string
		change         func(*model.RunResponse)
	}{
		{"calibrated", "", nil},
		{"bad scale", "unverified_native_normalization", func(r *model.RunResponse) { r.CPUTimeNormalization.ScalePPM++ }},
		{"bad raw sample", "unverified_native_normalization", func(r *model.RunResponse) { v := *r.RawCPUTimeNs + 1; r.RawCPUTimeNs = &v }},
		{"wrong weight", "unverified_model", func(r *model.RunResponse) {
			r.WorkMeasurementShadow.Response.WorkMeasurementExecution.TimeEstimate.InstructionNSNumerator++
		}},
		{"bad option binding", "unverified_model", func(r *model.RunResponse) {
			r.WorkMeasurementShadow.Response.WorkMeasurementExecution.TimeEstimate.RuntimeOptionsID = "bad"
		}},
		{"legacy conversion", "unverified_model", func(r *model.RunResponse) {
			inner := r.WorkMeasurementShadow.Response
			inner.WorkMeasurement.EngineProfile = "dynamorio-11.3-durante-v5-vm32m"
			inner.WorkMeasurement.Installation.SourceCommit = "4016b33fc69b93d36a13efbd00953213b8f6317f"
			inner.WorkMeasurement.Installation.RuntimePatchSHA256 = "59fc79b5055a0c42b463703768891f0a10199ef1135d43028826c8c3894bab7c"
			inner.WorkMeasurement.Installation.RuntimeCodeReplayPatchSHA256 = "aad9b561fb89e09d52d7a765124226659b8dbd8b886da61bc73d15fde28b5b72"
			inner.WorkMeasurementExecution.TimeEstimate.EngineProfile = inner.WorkMeasurement.EngineProfile
		}},
	} {
		t.Run(f.name, func(t *testing.T) {
			r := cpuTimedResponse(t, model.RunStatusAccepted, 800_000, 1_000_001)
			raw, rawMillis := uint64(400_000), int64(1)
			r.RawCPUTimeNs, r.RawCPUTimeMs = &raw, &rawMillis
			r.CPUTimeNormalization = &model.CPUTimeNormalization{Method: timing.CPUNormalizationMethod, ReferenceTimeNs: 2, ObservedTimeNs: 1, ScalePPM: 2_000_000}
			if f.change != nil {
				f.change(&r)
			}
			var row counts
			if err := row.add(r, "inline"); err != nil {
				t.Fatal(err)
			}
			if row.PrimaryACShadowTLE != 1 {
				t.Fatal("legacy disagreement lost")
			}
			if f.excluded != "" {
				if row.CPUComparison.Excluded[f.excluded] != 1 || len(row.CPUComparison.Cohorts) != 0 {
					t.Fatal(row.CPUComparison)
				}
				return
			}
			for _, c := range row.CPUComparison.Cohorts {
				if c.NativeReferenceNs != 2 || c.NormalizationMethod != timing.CPUNormalizationMethod {
					t.Fatal(c)
				}
			}
			if len(row.CPUComparison.Cohorts) != 1 {
				t.Fatal(row.CPUComparison)
			}
		})
	}
}

func TestCompletedNativeTimeAccuracyUsesNormalizedTime(t *testing.T) {
	r := cpuTimedResponse(t, model.RunStatusAccepted, 800_000, 960_000)
	raw, rawMillis := uint64(400_000), int64(1)
	r.RawCPUTimeNs, r.RawCPUTimeMs = &raw, &rawMillis
	r.CPUTimeNormalization = &model.CPUTimeNormalization{Method: timing.CPUNormalizationMethod, ReferenceTimeNs: 2, ObservedTimeNs: 1, ScalePPM: 2_000_000}
	var row counts
	if err := row.add(r, "inline"); err != nil {
		t.Fatal(err)
	}
	if len(row.CPUComparison.Cohorts) != 1 {
		t.Fatal(row.CPUComparison)
	}
	for _, cohort := range row.CPUComparison.Cohorts {
		if cohort.CompletedTimeAccuracy != (completedTimeAccuracy{Within20Percent: 1}) {
			t.Fatal(cohort)
		}
	}
}

func TestCPUComparisonVerifiesScoresAndSharedInput(t *testing.T) {
	score := func(v float64) *float64 { return &v }
	ac, tle := model.RunStatusAccepted, model.RunStatusTLE
	for _, tc := range []struct {
		name, status             string
		nativeScore              *float64
		shadowAC                 bool
		shadowScore              *float64
		source, digest, excluded string
	}{
		{"real URL receipt", ac, nil, true, score(1), "url", strings.Repeat("ab", 32), ""},
		{"unannotated URL receipt", ac, nil, true, score(1), "", strings.Repeat("ab", 32), ""},
		{"unannotated invalid digest", ac, nil, true, score(1), "", "not-a-hash", "unverified_shared_input"},
		{"unannotated uppercase digest", ac, nil, true, score(1), "", strings.Repeat("AB", 32), "unverified_shared_input"},
		{"native output score", ac, score(1), true, score(1), "", "", "unverified_native_ac"},
		{"native TLE partial score", tle, score(0.5), true, score(1), "", "", "unverified_native_tle"},
		{"native TLE zero credit", tle, score(0), true, score(1), "", "", ""},
		{"native TLE full output credit", tle, score(1), true, score(1), "", "", ""},
		{"shadow AC lacking score", ac, nil, true, nil, "", "", "inconsistent_shadow_score"},
		{"shadow AC partial score", ac, nil, true, score(0.5), "", "", "inconsistent_shadow_score"},
		{"virtual TLE with score", ac, nil, false, score(0), "", "", "inconsistent_shadow_score"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native, shadow := uint64(999_999), uint64(1_000_001)
			if tc.status == tle {
				native = 1_000_001
			}
			if tc.shadowAC {
				shadow = 1_000_000
			}
			r := cpuTimedResponse(t, tc.status, native, shadow)
			r.Score = tc.nativeScore
			block := r.WorkMeasurementShadow
			block.StdinSHA256 = tc.digest
			block.Response.Score = tc.shadowScore
			var c counts
			if err := c.add(r, tc.source); err != nil {
				t.Fatal(err)
			}
			if c.ComparablePrimaryAC+c.ComparablePrimaryTLE != 1 {
				t.Fatal("legacy verdict comparison changed")
			}
			if tc.excluded == "" {
				if len(c.CPUComparison.Cohorts) != 1 {
					t.Fatal(c.CPUComparison)
				}
			} else if len(c.CPUComparison.Cohorts) != 0 || c.CPUComparison.Excluded[tc.excluded] != 1 {
				t.Fatal(c.CPUComparison)
			}
		})
	}
}

func TestCPUComparisonRequiresAdmissibleRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, source, family, cpuid, determinism string
		eligible                                 bool
	}{
		{"pinned CPython", "PYTHON3", "cpython", durante.CPUIDProfileX8664V2, durante.RuntimeDeterminismV1, true},
		{"unpinned CPython", "PYTHON3", "cpython", durante.CPUIDProfileX8664V2, "", false},
		{"host CPUID", "CPP20", "cpp", "", "", false},
		{"unexpected CPP launcher pin", "CPP20", "cpp", durante.CPUIDProfileX8664V2, durante.RuntimeDeterminismV1, false},
		{"source family mismatch", "PYTHON3", "cpp", durante.CPUIDProfileX8664V2, "", false},
		{"unregistered language", "unknown", "cpp", durante.CPUIDProfileX8664V2, "", false},
		{"uncanonical language", "cpp20", "cpp", durante.CPUIDProfileX8664V2, "", false},
		{"unqualified JVM", "JAVA17", "jvm", durante.CPUIDProfileX8664V2, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := cpuTimedResponse(t, model.RunStatusAccepted, 999_999, 1_000_001)
			s := r.WorkMeasurementShadow.Response
			e := s.WorkMeasurementExecution.TimeEstimate
			e.RuntimeID = tc.source + "@sha256:" + strings.Repeat("a", 64)
			e.Family, s.WorkMeasurement.Family = tc.family, tc.family
			e.CPUIDProfile, s.WorkMeasurement.CPUIDProfile = tc.cpuid, tc.cpuid
			e.RuntimeDeterminism, s.WorkMeasurement.RuntimeDeterminism = tc.determinism, tc.determinism
			_, eligible := verifiedCPUModel(s)
			if eligible != tc.eligible {
				t.Fatalf("eligibility=%v, want %v", eligible, tc.eligible)
			}
		})
	}
}
