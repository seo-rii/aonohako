package execute

import (
	"context"
	"reflect"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestPeerWorkClockBindingsAndEvidence(t *testing.T) {
	req, response := peerTimeFixture(t, "interactive")
	req.WorkMeasurementPeerTime.Version = 2
	req.Limits = model.Limits{TimeMs: 3000, WallTimeMs: 8000, MemoryMB: 128}
	req.Interactor.Limits = &model.Limits{TimeMs: 1000, WallTimeMs: 5000, MemoryMB: 256}
	cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{}).Execution.WorkMeasurement
	cfg.Judge = durante.JudgePolicy{Enabled: true, VerdictsAllowed: true, Integrity: durante.JudgeIntegrityOff, Families: cfg.Families}
	cfg.ResourcePolicy = &durante.ResourcePolicy{MaxLimits: durante.ResourceLimits{CPUTimeMs: 5000, WallTimeMs: 10000, MemoryMB: 512}, MemoryBudgetMB: 4096, ReservedMemoryMB: 512}
	plans, err := workMeasurementPeerTimePlans(req, cfg)
	if err != nil {
		t.Fatal(err)
	}
	forward := forwardedPeerClockRequest(req, plans)
	for i, item := range forward.WorkMeasurementPeerTime.Budgets {
		if item.ModelBinding == "" || req.WorkMeasurementPeerTime.Budgets[i].ModelBinding != "" {
			t.Fatal("lost model binding or mutated caller")
		}
	}
	if _, err := workMeasurementPeerTimePlans(forward, cfg); err != nil {
		t.Fatal(err)
	}
	forward.WorkMeasurementPeerTime.Budgets[0].ModelBinding = "mismatch"
	if _, err := workMeasurementPeerTimePlans(forward, cfg); err == nil {
		t.Fatal("accepted mismatched model before launch")
	}
	plan := plans[peerTimeKey("contestant", "contestant", nil)]
	roleReq := *req
	applyPeerWorkClock(&roleReq, plan)
	report := response.WorkMeasurements[0].Report
	p := &durante.WorkClockProgress{Instructions: *report.Instructions, MOVSBytes: report.REPBreakdown.MOVSBytes, STOSBytes: report.REPBreakdown.STOSBytes}
	zero := 0
	natural := execResult{Status: "OK", ExitCode: &zero, CPUAccounting: &model.CPUAccounting{}, CPUTimeNs: 10000, WallTimeMs: 8, MemoryKB: 10, WorkMeasurement: report, workClockProgress: p}
	for _, tc := range []struct {
		name, source, status string
		cause                error
		change               func(*execResult)
	}{
		{"equal", "", "OK", nil, func(res *execResult) {}},
		{"final", "durante_work_clock_final", model.RunStatusTLE, nil, func(res *execResult) {
			above := *p
			above.Instructions++
			r := *report
			r.Instructions = &above.Instructions
			res.workClockProgress, res.WorkMeasurement = &above, &r
		}},
		{"hard stdout and work", "durante_peer_clock_error", model.RunStatusInitFail, nil, func(res *execResult) {
			above := *p
			above.Instructions++
			res.StdoutTruncated, res.workClockProgress = true, &above
		}},
		{"live", "durante_work_clock", model.RunStatusTLE, nil, func(res *execResult) {
			res.Status, res.VerdictSource, res.ExitCode = model.RunStatusTLE, "durante_work_clock", nil
			above := *p
			above.Instructions++
			res.workClockProgress = &above
			res.WorkMeasurement = report.WithoutTotals("forced")
		}},
		{"wall", "durante_peer_clock_error", model.RunStatusInitFail, context.DeadlineExceeded, func(res *execResult) { res.Status, res.VerdictSource = model.RunStatusTLE, "wall_time" }},
		{"cancel sibling", "durante_peer_cancelled", model.RunStatusInitFail, context.Canceled, func(res *execResult) { res.Status, res.VerdictSource = model.RunStatusTLE, "wall_time" }},
		{"unavailable", "durante_peer_clock_error", model.RunStatusInitFail, nil, func(res *execResult) { res.workClockProgress = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := natural
			tc.change(&res)
			finishPeerWorkClock(&roleReq, nil, &res, tc.cause, "contestant")
			if res.Status != tc.status || res.VerdictSource != tc.source {
				t.Fatalf("unexpected peer result: %s %s %s", res.Status, res.VerdictSource, res.Reason)
			}
			role := collectPeerWorkMeasurement(nil, res, false, "contestant", "contestant", nil)
			r, err := peerClockResult(plan, role)
			if err != nil {
				t.Fatal(err)
			}
			role.TimeBudget = r
			terminal := tc.name == "equal" || tc.name == "final"
			if (r.Estimate != nil) != terminal || role.Report.Complete != terminal {
				t.Fatalf("terminal estimate/totals do not match the stop: %+v", r)
			}
			if terminal || tc.name == "live" {
				inconsistent := role
				if terminal {
					inconsistent.Report = role.Report.WithoutTotals("missing final")
				} else {
					inconsistent.Report = report
				}
				if _, err := peerClockResult(plan, inconsistent); err == nil {
					t.Fatal("accepted conflicting terminal/partial evidence")
				}
			}
			reconstructed, err := peerClockResult(plan, role)
			if err != nil || !reflect.DeepEqual(reconstructed, r) {
				t.Fatalf("remote mismatch %v", err)
			}
			if r.Clock.Progress != nil {
				modified := *r.Clock.Progress
				modified.Instructions = 0
				r.Clock.Progress = &modified
				if _, err := peerClockResult(plan, role); err == nil {
					t.Fatal("accepted inconsistent progress")
				}
			}
		})
	}
	cfg.Judge.VerdictsAllowed = false
	if _, err := workMeasurementPeerTimePlans(req, cfg); err == nil {
		t.Fatal("admitted operator without judge permission")
	}
}

func TestPeerWorkClockMemoryAdmission(t *testing.T) {
	p := &durante.ResourcePolicy{MaxLimits: durante.ResourceLimits{CPUTimeMs: 5000, WallTimeMs: 10000, MemoryMB: 512}, MemoryBudgetMB: 2048, ReservedMemoryMB: 512}
	if err := p.ValidatePeerAdmission(2, 0, 768); err != nil {
		t.Fatal(err)
	}
	if err := p.ValidatePeerAdmission(2, 1, 768); err == nil {
		t.Fatal("did not reserve memory for concurrent peers and work root")
	}
}
