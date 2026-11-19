package execute

import (
	"context"
	"math"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func overflowClockFixture(t *testing.T) (*model.RunRequest, durante.Config, *durante.WorkClockRequest, *durante.Report, *durante.WorkClockProgress) {
	t.Helper()
	req := judgeRequest()
	cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{}).Execution.WorkMeasurement
	cfg.Judge = judgeMeasurementConfig().Judge
	cfg.CPUIDProfile = durante.CPUIDProfileX8664V2
	cfg.TimeModels.Profiles[0].CPUIDProfile = cfg.CPUIDProfile
	report := completeEnvelopeReport()
	report.CPUIDProfile = cfg.CPUIDProfile
	report.REPBreakdown = &durante.REPBreakdown{Version: 1}
	cfg.TimeModels.Models[0].Coefficients.InstructionNSNumerator = math.MaxInt64 / *report.Instructions + 1
	clock := effectiveWorkClock(req, cfg, config.DefaultRuntimeTuningConfig())
	if clock == nil {
		t.Fatal("model did not bind")
	}
	return req, cfg, clock, report, &durante.WorkClockProgress{Instructions: *report.Instructions}
}

func TestTerminalWorkClockOverflowDecidesWithoutDisplayEstimate(t *testing.T) {
	req, cfg, clock, report, progress := overflowClockFixture(t)
	zero := 0
	response := finishWorkMeasurementDiagnostic(context.Background(), req,
		model.RunResponse{Status: model.RunStatusAccepted, VerdictSource: "stdout", ExitCode: &zero, WorkMeasurement: report})
	response = finishWorkClock(req, clock, response, progress)
	response = finishWorkMeasurementTime(req, response, cfg)
	response = finishWorkMeasurementJudge(req, response, cfg)
	d := response.WorkMeasurementExecution
	if response.Status != model.RunStatusTLE || d.State != "budget_exceeded" || d.TerminationSource != "durante_work_clock_final" ||
		d.TimeEstimate != nil || d.TimeEstimateUnavailable != durante.TimeNumericOverflow || !d.TimeEstimateEligibility.NumericOverflow() ||
		d.NominalTimeExceeded == nil || !*d.NominalTimeExceeded || response.WorkMeasurementJudgment.EstimatedTimeNs != nil {
		t.Fatalf("unexpected terminal overflow judgment: %+v, %+v", d, response.WorkMeasurementJudgment)
	}
	if err := validateRemoteWorkClock(req, response, clock); err != nil {
		t.Fatal(err)
	}
	if err := validateRemoteWorkMeasurementTime(req, response, cfg); err != nil {
		t.Fatal(err)
	}
	if err := validateRemoteWorkMeasurementJudge(req, response, cfg); err != nil {
		t.Fatal(err)
	}
	no := false
	d.NominalTimeExceeded = &no
	if err := validateRemoteWorkMeasurementTime(req, response, cfg); err == nil {
		t.Fatal("accepted an incorrect crossing flag without an estimate")
	}
	unreached := *clock
	unreached.Budget = math.MaxUint64
	if terminalWorkClockMatches(req, &unreached, report, progress) {
		t.Fatal("numeric overflow alone accepted without a strict crossing")
	}
	mismatch := *report
	mismatch.CPUIDProfile = ""
	if terminalWorkClockMatches(req, clock, &mismatch, progress) {
		t.Fatal("accepted an incompatible runtime")
	}
}

func TestRoleWorkClockOverflowPreservesExitPolicy(t *testing.T) {
	req, _, clock, report, progress := overflowClockFixture(t)
	raw := workMeasurementRawRequest(req)
	raw.WorkMeasurementClock = clock
	for _, tc := range []struct {
		role           string
		exit           int
		status, source string
	}{
		{"contestant", 0, model.RunStatusTLE, "durante_work_clock_final"},
		{"contestant", 1, "OK", ""},
		{"checker", 1, model.RunStatusTLE, "durante_work_clock_final"},
	} {
		plan := peerTimePlan{role: workMeasurementRole{role: tc.role, language: req.Lang}, budgetMs: req.Limits.TimeMs,
			policy: *clock.TimePolicy, runtimeID: clock.TimePolicy.RuntimeID, clock: clock, rawLimits: *req.WorkMeasurementLimits}
		res := execResult{Status: "OK", ExitCode: &tc.exit, CPUAccounting: &model.CPUAccounting{},
			CPUTimeNs: 10000, WallTimeMs: 1, MemoryKB: 10, WorkMeasurement: report, workClockProgress: progress}
		finishPeerWorkClock(&raw, nil, &res, nil, tc.role)
		if res.Status != tc.status || res.VerdictSource != tc.source {
			t.Fatalf("%s exit %d: %+v", tc.role, tc.exit, res)
		}
		row := collectPeerWorkMeasurement(nil, res, false, tc.role, tc.role, nil)
		budget, err := peerClockResult(plan, row)
		if err != nil || budget.Estimate != nil || budget.Unavailable != durante.TimeNumericOverflow ||
			budget.Exceeded == nil || !*budget.Exceeded || budget.Clock.State != "observed" {
			t.Fatalf("%s exit %d: %+v, %v", tc.role, tc.exit, budget, err)
		}
		plan.runtimeID = "other"
		if _, err := peerClockResult(plan, row); err == nil {
			t.Fatal("accepted a terminal receipt with a mismatched runtime")
		}
	}
}
