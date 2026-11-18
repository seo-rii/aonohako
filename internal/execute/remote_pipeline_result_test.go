package execute

import (
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/timing"
)

func TestRemotePipelineNativeResult(t *testing.T) {
	req := syntheticInteractiveBatchPipeline(100)
	nano := uint64(2_670_000)
	rawNano := uint64(2_900_000)
	rawMs := int64(3)
	zero, partial := 0.0, 0.75
	nonzeroExit := 7
	one := model.StepResult{
		ID: "phase1", ProgramID: "participant", Status: model.RunStatusAccepted, VerdictSource: "interactor",
		TimeMs: 21, WallTimeMs: 21, CPUTimeMs: 3, CPUTimeNs: &nano, RawCPUTimeNs: &rawNano, RawCPUTimeMs: &rawMs,
		ProcessCPUTimeMs: 2, MemoryKB: 30, HandoffBytes: 13,
	}
	two := model.StepResult{
		ID: "phase2", ProgramID: "participant", Status: model.RunStatusAccepted,
		TimeMs: 14, WallTimeMs: 14, CPUTimeMs: 3, CPUTimeNs: &nano, RawCPUTimeNs: &rawNano, RawCPUTimeMs: &rawMs,
		ProcessCPUTimeMs: 2, MemoryKB: 35,
	}
	native := func(status, source string, rows ...model.StepResult) model.RunResponse {
		return aggregateStepResponse(model.RunResponse{Status: status, VerdictSource: source}, rows)
	}
	failed := two
	failed.Status, failed.VerdictSource = model.RunStatusRE, "exit_code"
	failedInteractor := one
	failedInteractor.Status, failedInteractor.VerdictSource, failedInteractor.HandoffBytes = model.RunStatusWA, "interactor:exit_code", 0
	interrupted := two
	interrupted.Status, interrupted.VerdictSource, interrupted.CPUTimeNs, interrupted.RawCPUTimeNs = model.RunStatusInitFail, "", nil, nil
	artifact := one
	artifact.HandoffBytes = 0
	t.Run("native success and partial execution", func(t *testing.T) {
		for _, resp := range []model.RunResponse{
			native(model.RunStatusAccepted, "final:spj", one, two),
			native(model.RunStatusWA, "final:spj", one, two),
			native(model.RunStatusRE, "final:spj", one, two),
			native(model.RunStatusInitFail, "final:spj", one, two),
			native(model.RunStatusInitFail, "pipeline:resource"),
			native(model.RunStatusInitFail, "pipeline:artifact"),
			native(model.RunStatusInitFail, ""),
			native(model.RunStatusInitFail, "step:phase1:artifact"),
			native(model.RunStatusInitFail, "step:phase2:stdin", one),
			native(model.RunStatusRE, "step:phase1:artifact", artifact),
			native(model.RunStatusRE, "step:phase2:exit_code", one, failed),
			native(model.RunStatusInitFail, "step:phase2", one, interrupted),
			native(model.RunStatusWA, "step:phase1:interactor:exit_code", failedInteractor),
		} {
			if err := validateRemotePipelineResult(req, &resp); err != nil {
				t.Errorf("%q, %q, %d stages: %v", resp.VerdictSource, resp.Status, len(resp.Steps), err)
			}
		}
	})

	t.Run("native interactive failure score and accepted grace", func(t *testing.T) {
		failure := native(model.RunStatusWA, "step:phase1:interactor:exit_code", failedInteractor)
		failure.Score = &zero
		grace := one
		grace.ExitCode = &nonzeroExit
		for _, resp := range []model.RunResponse{failure, native(model.RunStatusAccepted, "final:spj", grace, two)} {
			if err := validateRemotePipelineResult(req, &resp); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("bind native role counters to the submitted programs", func(t *testing.T) {
		response := native(model.RunStatusAccepted, "final:spj", one, two)
		zero := 0
		cpu, raw := nano, rawNano
		nothing := uint64(0)
		response.Steps[0].ExitCode = &zero
		roles := &model.PipelineRoleTimes{
			Contestant: model.PipelineRoleCPU{ProgramID: "participant", Status: model.RunStatusAccepted, ExitCode: &zero, CPUTimeMs: 3, CPUTimeNs: &cpu, RawCPUTimeNs: &raw},
			Interactor: model.PipelineRoleCPU{ProgramID: req.Pipeline.Steps[0].Executor.InteractorProgramID, Status: model.RunStatusAccepted, ExitCode: &zero, CPUTimeNs: &nothing, RawCPUTimeNs: &nothing},
		}
		response.Steps[0].CPUTimeByRole = roles
		if err := validateRemotePipelineResult(req, &response); err != nil {
			t.Fatal(err)
		}
		roles.Interactor.Status = model.RunStatusWA
		if validateRemotePipelineResult(req, &response) == nil {
			t.Fatal("accepted a native accepted stage from a rejecting interactor")
		}
		roles.Interactor.Status = model.RunStatusAccepted
		roles.Interactor.ProgramID = "other"
		if validateRemotePipelineResult(req, &response) == nil {
			t.Fatal("accepted timing from the wrong judge")
		}
		roles.Interactor.ProgramID = req.Pipeline.Steps[0].Executor.InteractorProgramID
		cpu++
		if validateRemotePipelineResult(req, &response) == nil {
			t.Fatal("accepted inconsistent contestant CPU nanoseconds")
		}
	})
	t.Run("old worker or missing precise samples", func(t *testing.T) {
		oldOne, oldTwo := one, two
		oldOne.CPUTimeNs, oldTwo.CPUTimeNs, oldOne.RawCPUTimeNs, oldTwo.RawCPUTimeNs = nil, nil, nil, nil
		for _, resp := range []model.RunResponse{
			native(model.RunStatusAccepted, "final:spj", oldOne, oldTwo),
			native(model.RunStatusAccepted, "final:spj", one, oldTwo),
		} {
			if err := validateRemotePipelineResult(req, &resp); err != nil || resp.CPUTimeNs != 0 || resp.RawCPUTimeNs != nil {
				t.Errorf("missing precise counters: %+v, %v", resp, err)
			}
		}
	})

	t.Run("discard unrelated response metadata and keep normalization", func(t *testing.T) {
		resp := native(model.RunStatusAccepted, "final:spj", one, two)
		resp.CPUAccounting = &model.CPUAccounting{SelectedSource: "private"}
		resp.CPUTimeSampling = &model.CPUTimeSampling{Method: "private"}
		resp.Match = &model.MatchResult{Message: "private"}
		resp.StartedParticipants = 1
		norm := &model.CPUTimeNormalization{Method: timing.CPUNormalizationMethod, ScalePPM: 1_000_000, ObservedTimeNs: 500, ReferenceTimeNs: 500}
		resp.CPUTimeNormalization = norm
		if err := validateRemotePipelineResult(req, &resp); err != nil || resp.CPUTimeNormalization != norm ||
			resp.CPUAccounting != nil || resp.CPUTimeSampling != nil || resp.Match != nil || resp.StartedParticipants != 0 {
			t.Errorf("pipeline metadata: %+v, %v", resp, err)
		}
	})
	for _, tc := range []struct {
		name string
		edit func(*model.RunResponse)
	}{
		{"accepted batch with nonzero exit", func(r *model.RunResponse) { r.Steps[1].ExitCode = &nonzeroExit }},
		{"accepted batch with failure source", func(r *model.RunResponse) { r.Steps[1].VerdictSource = "cpu_time" }},
		{"accepted batch with truncated execution output", func(r *model.RunResponse) { r.Steps[1].StdoutTruncated = true }},
		{"accepted interactive with failure source", func(r *model.RunResponse) { r.Steps[0].VerdictSource = "contestant:wall_time" }},
		{"score before first stage", func(r *model.RunResponse) {
			*r = native(model.RunStatusInitFail, "pipeline:resource")
			r.Score = &partial
		}},
		{"score before next stage", func(r *model.RunResponse) {
			*r = native(model.RunStatusInitFail, "step:phase2:stdin", one)
			r.Score = &zero
		}},
		{"score after artifact failure", func(r *model.RunResponse) {
			*r = native(model.RunStatusRE, "step:phase1:artifact", artifact)
			r.Score = &partial
		}},
		{"score after batch failure", func(r *model.RunResponse) {
			*r = native(model.RunStatusRE, "step:phase2:exit_code", one, failed)
			r.Score = &zero
		}},
		{"stale positive interactive failure score", func(r *model.RunResponse) {
			*r = native(model.RunStatusWA, "step:phase1:interactor:exit_code", failedInteractor)
			r.Score = &partial
		}},
		{"wrong role", func(r *model.RunResponse) { r.Steps[0].ProgramID = "interactor" }},
		{"wrong order", func(r *model.RunResponse) { r.Steps[0], r.Steps[1] = r.Steps[1], r.Steps[0] }},
		{"early final", func(r *model.RunResponse) { *r = native(model.RunStatusAccepted, "final:spj", one) }},
		{"wrong final judge", func(r *model.RunResponse) { r.VerdictSource = "final:diff" }},
		{"native failure followed by stage", func(r *model.RunResponse) { r.Steps[0] = failedInteractor }},
		{"declared artifact limit", func(r *model.RunResponse) { r.Steps[0].HandoffBytes = 101 }},
		{"private role source", func(r *model.RunResponse) { r.Steps[0].VerdictSource = "interactor:private" }},
		{"private top source", func(r *model.RunResponse) { r.VerdictSource = "step:phase2:private" }},
		{"inconsistent stop", func(r *model.RunResponse) { *r = native(model.RunStatusTLE, "step:phase2:exit_code", one, failed) }},
		{"wrong native CPU", func(r *model.RunResponse) { r.CPUTimeMs++ }},
		{"wrong precise CPU", func(r *model.RunResponse) { r.CPUTimeNs++ }},
		{"missing raw total", func(r *model.RunResponse) { r.RawCPUTimeNs = nil }},
		{"wrong wall", func(r *model.RunResponse) { r.TimeMs++ }},
		{"wrong peak memory", func(r *model.RunResponse) { r.MemoryKB++ }},
		{"free-form normalization", func(r *model.RunResponse) { r.CPUTimeNormalization = &model.CPUTimeNormalization{Method: "private"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := native(model.RunStatusAccepted, "final:spj", one, two)
			tc.edit(&resp)
			if validateRemotePipelineResult(req, &resp) == nil {
				t.Fatal("accepted inconsistent pipeline reply")
			}
		})
	}
}
