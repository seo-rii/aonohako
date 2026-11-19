package execute

import (
	"slices"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func spjClockRequestFixture() *model.RunRequest {
	return &model.RunRequest{Lang: "CPP20", WorkMeasurement: true, Limits: model.Limits{TimeMs: 1200, MemoryMB: 128},
		SPJ: &model.SPJSpec{Lang: "CPP20", EmitScore: true, Binary: &model.Binary{Name: "check", Mode: "exec", DataB64: "eA=="}},
		WorkMeasurementPeerTime: &model.PeerTimeRequest{Version: 2, Budgets: []model.PeerTimeBudget{
			{Role: "contestant", ProgramID: "contestant", TimeMs: 5}, {Role: "checker", ProgramID: "checker", TimeMs: 3},
		}},
	}
}

func TestSPJPeerClockAdmissionAndOptions(t *testing.T) {
	req := spjClockRequestFixture()
	if err := runvalidation.ValidateWorkMeasurementPeerTime(req); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*model.RunRequest){
		func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Version = 1 },
		func(r *model.RunRequest) { r.IgnoreTLE = true },
		func(r *model.RunRequest) { r.Interactor = &model.InteractorSpec{} },
		func(r *model.RunRequest) { r.Lang = "binary" },
		func(r *model.RunRequest) { r.SPJ.Lang = "" },
		func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[1].Role = "interactor" },
		func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[1].ProgramID = "reused" },
	} {
		copy := spjClockRequestFixture()
		mutate(copy)
		if err := runvalidation.ValidateWorkMeasurementPeerTime(copy); err == nil {
			t.Fatal("invalid SPJ peer clock admitted")
		}
	}
	cfg := boundRuntimeOptionsConfig(t, req, config.ExecutionConfig{}).Execution.WorkMeasurement
	cfg.Judge = durante.JudgePolicy{Enabled: true, VerdictsAllowed: true, Families: cfg.Families, Integrity: durante.JudgeIntegrityOff}
	cfg.ResourcePolicy = &durante.ResourcePolicy{MaxLimits: durante.ResourceLimits{CPUTimeMs: 20000, WallTimeMs: 30000, MemoryMB: 1024}, MemoryBudgetMB: 4096, ReservedMemoryMB: 256}
	plans, err := workMeasurementPeerTimePlans(req, cfg)
	if err != nil {
		t.Fatal(err)
	}
	descs, err := DescribeWorkMeasurementRuntimeOptions(req, config.ExecutionConfig{WorkMeasurement: cfg})
	if err != nil {
		t.Fatal(err)
	}
	guest, checker := descs[0], descs[1]
	if guest.Role != "contestant" || checker.Role != "checker" || guest.RuntimeOptionsID == checker.RuntimeOptionsID ||
		!slices.Equal(checker.RuntimeOptions.Argv[len(checker.RuntimeOptions.Argv)-3:], []string{runtimeOptionsWorkspace + "/.tmp/spj-input", runtimeOptionsWorkspace + "/.tmp/spj-output", runtimeOptionsWorkspace + "/.tmp/spj-solution"}) {
		t.Fatal("checker argv/protocol was not bound separately")
	}
	if plans[peerTimeKey("contestant", "contestant", nil)].rawLimits.WallTimeMs != req.Limits.TimeMs ||
		plans[peerTimeKey("checker", "checker", nil)].rawLimits.CPUTimeMs != spjRunRequest(req).Limits.TimeMs {
		t.Fatal("unexpected default raw wall/CPU limit")
	}
}

func TestSPJPeerClockVerdict(t *testing.T) {
	req := spjClockRequestFixture()
	zero, one := 0, 1
	score := 0.75
	usage := &model.PeerClockUsage{CPUTimeNs: new(uint64), WallTimeMs: new(int64), MemoryKB: new(int64)}
	c := model.WorkMeasurementProcess{Role: "contestant", ProgramID: "contestant", ExecutionStatus: "OK", ExitCode: &zero, TimeBudget: &model.PeerTimeBudgetResult{RawUsage: usage}}
	k := model.WorkMeasurementProcess{Role: "checker", ProgramID: "checker", ExecutionStatus: "OK", ExitCode: &zero, TimeBudget: &model.PeerTimeBudgetResult{}, CheckerScore: &score}
	good := model.RunResponse{WorkMeasurements: []model.WorkMeasurementProcess{c, k}, Status: model.RunStatusAccepted, VerdictSource: "spj", Score: &score, ExitCode: &zero}
	check := func(pass bool, r model.RunResponse) {
		t.Helper()
		if (validateSPJPeerClockVerdict(req, r) == nil) != pass {
			t.Fatalf("unexpected validation: %+v", r)
		}
	}
	check(true, good)
	bad := good
	bad.TimeMs = 99
	check(false, bad)
	wrongNs := uint64(1)
	bad = good
	bad.RawCPUTimeNs = &wrongNs
	check(false, bad)
	bad = good
	bad.Score = nil
	check(false, bad)
	bad = good
	bad.VerdictSource = "checker:durante_work_clock"
	check(false, bad)
	wa := good
	wa.WorkMeasurements = slices.Clone(good.WorkMeasurements)
	wa.WorkMeasurements[1].ExitCode = &one
	wa.WorkMeasurements[1].CheckerScore = nil
	wa.Status = model.RunStatusWA
	wa.Score = new(float64)
	check(true, wa)
	bad = wa
	bad.Score = &score
	check(false, bad)
	partial := good
	partial.WorkMeasurements = slices.Clone(good.WorkMeasurements)
	partial.WorkMeasurements[0].ExecutionStatus = model.RunStatusTLE
	partial.WorkMeasurements[0].TerminationSource = "durante_work_clock"
	partial.WorkMeasurements[0].ExitCode = nil
	partial.ExitCode = nil
	partial.Score = nil
	partial.Status = model.RunStatusTLE
	partial.VerdictSource = "contestant:durante_work_clock"
	check(false, partial)
	partial.WorkMeasurements[1] = model.WorkMeasurementProcess{Role: "checker", ProgramID: "checker", ExecutionStatus: model.RunStatusInitFail, TerminationSource: "spj_checker_not_started", TimeBudget: &model.PeerTimeBudgetResult{Unavailable: "role_not_started", RawSource: "spj_checker_not_started"}}
	check(true, partial)
	partial.Score = &score
	check(false, partial)
}
