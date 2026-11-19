package execute

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func legacyClockFixture() *model.RunRequest {
	limits := model.Limits{TimeMs: 1500, MemoryMB: 128}
	return &model.RunRequest{Programs: []model.RunProgram{{ID: "solver", Lang: "CPP20"}},
		Steps:           []model.RunStep{{ID: "one", ProgramID: "solver", Limits: limits, Handoff: &model.StepHandoff{ID: "x"}}, {ID: "two", ProgramID: "solver", Limits: limits, StdinFrom: "x"}},
		WorkMeasurement: true, WorkMeasurementPeerTime: &model.PeerTimeRequest{Version: 2, Budgets: []model.PeerTimeBudget{
			{StepID: "two", Role: "contestant", ProgramID: "solver", TimeMs: 2}, {StepID: "one", Role: "contestant", ProgramID: "solver", TimeMs: 2}}}}
}

func TestStagedClockAdmissionAndForwarding(t *testing.T) {
	req := legacyClockFixture()
	cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{}).Execution
	cfg.WorkMeasurement.Judge = durante.JudgePolicy{Enabled: true, VerdictsAllowed: true, Integrity: durante.JudgeIntegrityOff, Families: cfg.WorkMeasurement.Families}
	cfg.WorkMeasurement.ResourcePolicy = &durante.ResourcePolicy{MaxLimits: durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 4000, MemoryMB: 256}, MemoryBudgetMB: 4096, ReservedMemoryMB: 256}
	if err := ValidateStagedClockAdmission(req, cfg); err != nil {
		t.Fatal(err)
	}
	plans, err := workMeasurementPeerTimePlans(req, cfg.WorkMeasurement)
	if err != nil {
		t.Fatal(err)
	}
	fwd := forwardedPeerClockRequest(req, plans)
	if plans[peerTimeKey("contestant", "solver", nil, "two")].order != 1 || fwd.WorkMeasurementPeerTime.Budgets[0].StepID != "two" || fwd.WorkMeasurementPeerTime.Budgets[0].ModelBinding == "" || req.WorkMeasurementPeerTime.Budgets[0].ModelBinding != "" {
		t.Fatal("stage identity or model binding lost")
	}
	for name, mutate := range map[string]func(*model.RunRequest){
		"missing budget":          func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets = r.WorkMeasurementPeerTime.Budgets[:1] },
		"unknown step":            func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].StepID = "other" },
		"duplicate identity":      func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].StepID = "one" },
		"unknown model":           func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].ModelBinding = "unqualified" },
		"raw cap":                 func(r *model.RunRequest) { r.Steps[1].Limits.TimeMs = 2 },
		"unknown source language": func(r *model.RunRequest) { r.Programs[0].Lang = "binary" },
		"partial score":           func(r *model.RunRequest) { r.IgnoreTLE = true },
		"v1":                      func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Version = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			r := legacyClockFixture()
			mutate(r)
			if runvalidation.ValidateWorkMeasurementPeerTime(r) == nil && ValidateStagedClockAdmission(r, cfg) == nil {
				t.Fatal("invalid role admitted")
			}
		})
	}
}

func TestStagedClockRejectsInconsistentExecutionPrefix(t *testing.T) {
	req := legacyClockFixture()
	cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{}).Execution.WorkMeasurement
	cfg.Judge = durante.JudgePolicy{Enabled: true, VerdictsAllowed: true, Integrity: durante.JudgeIntegrityOff, Families: cfg.Families}
	cfg.ResourcePolicy = &durante.ResourcePolicy{MaxLimits: durante.ResourceLimits{CPUTimeMs: 3000, WallTimeMs: 4000, MemoryMB: 256}, MemoryBudgetMB: 4096, ReservedMemoryMB: 256}
	plans, err := workMeasurementPeerTimePlans(req, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, data := peerTimeFixture(t, "interactive")
	report := data.WorkMeasurements[0].Report
	p := &durante.WorkClockProgress{Instructions: 3_000_000}
	ns := uint64(23)
	wall, mem := int64(5), int64(10)
	work := newStagedWorkClocks(plans, cfg)
	plan := stageRolePlan(plans, "one", "contestant", "solver")
	work.record("one", "contestant", "solver", model.WorkMeasurementProcess{Role: "contestant", ExecutionStatus: model.RunStatusTLE, TerminationSource: "durante_work_clock", Report: report.WithoutTotals("private test input"), TimeBudget: &model.PeerTimeBudgetResult{Clock: plan.clock.Receipt(p), RawSource: "durante_work_clock", RawUsage: &model.PeerClockUsage{CPUTimeNs: &ns, WallTimeMs: &wall, MemoryKB: &mem}}})
	step := model.StepResult{ID: "one", ProgramID: "solver", Status: model.RunStatusTLE, VerdictSource: "durante_work_clock", TimeMs: wall, WallTimeMs: wall, CPUTimeNs: &ns, CPUTimeMs: 1, MemoryKB: mem}
	base := aggregateStepResponse(model.RunResponse{Status: step.Status, VerdictSource: "step:one:durante_work_clock", WorkMeasurements: work.rows}, []model.StepResult{step})
	r := finishWorkMeasurementPeerTime(req, base, cfg)
	if r.Status != model.RunStatusTLE || len(r.WorkMeasurements) != 2 || !stageNotStarted(r.WorkMeasurements[1]) {
		t.Fatalf("valid cutoff rejected: %+v", r)
	}
	verify := func(resp model.RunResponse) error {
		if err := validateRemoteMeasurementCounters(req, resp, cfg); err != nil {
			return err
		}
		return validateRemoteWorkMeasurementPeerTime(req, resp, cfg)
	}
	if err := verify(r); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*model.RunResponse){
		"forged later execution": func(v *model.RunResponse) {
			v.WorkMeasurements[1] = v.WorkMeasurements[0]
			v.WorkMeasurements[1].StepID = "two"
		},
		"forged step":   func(v *model.RunResponse) { v.Steps = append(v.Steps, step) },
		"forged total":  func(v *model.RunResponse) { v.CPUTimeMs++ },
		"fake complete": func(v *model.RunResponse) { v.WorkMeasurements[0].Report = report },
		"wrong budget": func(v *model.RunResponse) {
			b := *v.WorkMeasurements[0].TimeBudget
			b.BudgetMs++
			v.WorkMeasurements[0].TimeBudget = &b
		},
		"wrong judge": func(v *model.RunResponse) { v.Status = model.RunStatusAccepted },
		"score":       func(v *model.RunResponse) { v.Score = new(float64) },
	} {
		t.Run(name, func(t *testing.T) {
			x := r
			x.WorkMeasurements = slices.Clone(r.WorkMeasurements)
			x.Steps = slices.Clone(r.Steps)
			change(&x)
			if verify(x) == nil {
				t.Fatal("forged staged evidence accepted")
			}
		})
	}
	private := r
	private.WorkMeasurements = slices.Clone(r.WorkMeasurements)
	b := *private.WorkMeasurements[0].TimeBudget
	b.RawSource = "private test input"
	private.WorkMeasurements[0].TimeBudget = &b
	redactStagedClockRows(private.WorkMeasurements)
	wire, err := json.Marshal(private.WorkMeasurements)
	if err != nil || strings.Contains(string(wire), "private test input") || !private.WorkMeasurements[0].Report.ValidUnavailableResult() {
		t.Fatal("failed to redact partial clock evidence")
	}
}
