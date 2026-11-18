package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func timedResponse(t *testing.T, primary string, ns uint64) model.RunResponse {
	t.Helper()
	zero, verified, match := uint64(0), false, true
	exitCode := 0
	report := &durante.Report{
		Schema: 1, ClientVersion: 1, Complete: true, Authority: durante.Authority,
		Scope: durante.Scope, Metric: durante.Metric, Quality: "uninterrupted",
		EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault, Family: "cpp",
		Processes: 1, Images: 1, REPEnabled: true, REPTelemetryStatus: durante.REPMovsStosOnly,
		Instructions: &ns, REPBytes: &zero, REPEntries: &zero, UnsupportedREPEntries: &zero,
		IntegrityVerified: &verified, MeasurementContract: durante.DiagnosticContract(),
		Installation: &durante.InstallationIdentity{
			SourceCommit: "238193f90f5896b300563180e7939088ac8c346c", RuntimeSourceCommit: "6847bb210bea0ca11d1bf47704383d6a075da490",
			RuntimePatchSHA256:           "59fc79b5055a0c42b463703768891f0a10199ef1135d43028826c8c3894bab7c",
			RuntimeCodeReplayPatchSHA256: "aad9b561fb89e09d52d7a765124226659b8dbd8b886da61bc73d15fde28b5b72",
			LauncherSHA256:               strings.Repeat("a", 64), ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true,
		},
	}
	policy := durante.TimePolicy{
		ModelID: "test", ModelVersion: 1, ReferenceID: "test", RuntimeID: "CPP20@sha256:" + strings.Repeat("a", 64),
		EngineProfile: report.EngineProfile, Family: report.Family, DispatchProfile: report.DispatchProfile,
		InstructionNSNumerator: 1, REPByteNSNumerator: 1, NSDenominator: 1,
	}
	estimate, eligibility, err := policy.EstimateWithEligibility(policy.RuntimeID, *report)
	if err != nil {
		t.Fatal(err)
	}
	limit := model.Limits{TimeMs: 1, WallTimeMs: 2, MemoryMB: 128}
	exceeded := ns > 1_000_000
	status := model.RunStatusAccepted
	if exceeded {
		status = model.RunStatusTLE
	}
	agreement := model.ShadowDisagree
	if status == primary {
		agreement = model.ShadowAgree
	}
	return model.RunResponse{Status: primary, CPUTimeMs: 2, Stdout: "private-submission-output", WorkMeasurementShadow: &model.WorkMeasurementShadow{
		Version: 1, Authority: durante.Authority, PrimaryStatus: primary, PrimaryCPUTimeMs: 2, ShadowStatus: status, Agreement: agreement,
		Response: &model.RunResponse{Status: status, VerdictSource: "durante_judge", WorkMeasurement: report, ExitCode: &exitCode,
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
	want := counts{Testcases: 10, PrimaryAC: 7, PrimaryTLE: 2, ComparablePrimaryAC: 2, ComparablePrimaryTLE: 2, PrimaryACShadowTLE: 1, PrimaryTLEShadowAC: 1,
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
