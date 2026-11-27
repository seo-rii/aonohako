package execute

import (
	"context"
	"math"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// Durante v12 keeps the live page through signals and CMPS/SCAS entries. A raw
// clock then reconciles such a natural exit with its terminal counts. A
// weighted clock still ends it incomplete: its time estimate is ineligible for
// that report. A live stop has only the page, which carries no eligibility
// field, so both clocks decide it on the published counts alone.
func TestWorkClockEligibilityAtExitAndDuringRun(t *testing.T) {
	req := judgeRequest()
	cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{}).Execution.WorkMeasurement
	cfg.Judge = judgeMeasurementConfig().Judge
	cfg.CPUIDProfile = durante.CPUIDProfileX8664V2
	cfg.TimeModels.Profiles[0].CPUIDProfile = cfg.CPUIDProfile
	weighted := effectiveWorkClock(req, cfg, config.DefaultRuntimeTuningConfig())
	if weighted == nil || weighted.TimePolicy == nil {
		t.Fatal("model did not bind")
	}
	weighted.Budget = math.MaxUint64
	raw := &durante.WorkClockRequest{Version: 1, Budget: math.MaxUint64}
	report := func(mutate func(*durante.Report)) *durante.Report {
		r := completeEnvelopeReport()
		r.CPUIDProfile = cfg.CPUIDProfile
		r.REPBreakdown = &durante.REPBreakdown{Version: 1}
		mutate(r)
		return r
	}
	signals := func(r *durante.Report) { r.SignalsObserved, r.Quality = 2, "signals_observed" }
	scas := func(r *durante.Report) {
		one := uint64(1)
		r.UnsupportedREPEntries, r.REPTelemetryStatus = &one, durante.REPPartial
	}
	zero := 0
	finish := func(plan *durante.WorkClockRequest, r *durante.Report, status, source string, exit *int, progress durante.WorkClockProgress) *model.WorkMeasurementExecution {
		t.Helper()
		response := finishWorkMeasurementDiagnostic(context.Background(), req,
			model.RunResponse{Status: status, VerdictSource: source, ExitCode: exit, WorkMeasurement: r})
		response = finishWorkClock(req, plan, response, &progress)
		if err := validateRemoteWorkClock(req, response, plan); err != nil {
			t.Fatal(err)
		}
		return response.WorkMeasurementExecution
	}
	for _, tc := range []struct {
		name            string
		mutate          func(*durante.Report)
		raw, weightedAt string
	}{
		{"eligible", func(*durante.Report) {}, "complete", "complete"},
		{"signals", signals, "complete", "incomplete"},
		{"CMPS/SCAS entry", scas, "complete", "incomplete"},
	} {
		for _, side := range []struct {
			name, want string
			plan       *durante.WorkClockRequest
		}{{"raw", tc.raw, raw}, {"weighted", tc.weightedAt, weighted}} {
			r := report(tc.mutate)
			progress := durante.WorkClockProgress{Instructions: *r.Instructions}
			if d := finish(side.plan, r, model.RunStatusAccepted, "stdout", &zero, progress); d.State != side.want {
				t.Errorf("%s exit under %s clock: state %s, want %s", tc.name, side.name, d.State, side.want)
			}
		}
	}
	// The same reports stopped by a live crossing: the page has no field that
	// could make the weighted clock refuse them.
	for _, mutate := range []func(*durante.Report){func(*durante.Report) {}, signals, scas} {
		for _, plan := range []*durante.WorkClockRequest{raw, weighted} {
			small := *plan
			small.Budget = 1
			r := report(mutate)
			d := finish(&small, r, model.RunStatusTLE, "durante_work_clock", nil, durante.WorkClockProgress{Instructions: *r.Instructions})
			if d.State != "budget_exceeded" || d.TerminationSource != "durante_work_clock" {
				t.Errorf("live crossing under %s clock: %+v", plan.MetricName(), d)
			}
		}
	}
}
