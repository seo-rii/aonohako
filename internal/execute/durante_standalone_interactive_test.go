package execute

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestStandaloneInteractiveContestantRequest(t *testing.T) {
	req := observerRequest()
	req.Interactor = &model.InteractorSpec{Lang: "PYTHON3"}
	req.Stdin, req.StdinURL = "private input", "input URL"
	req.ExpectedStdout, req.ExpectedStdoutURL = "private answer", "answer URL"
	req.PythonLibraryMode, req.EntryPoint, req.RuntimeProfile = "stdlib", "Main", "named"
	req.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.AVX}
	t.Run("contestant settings and artifacts", func(t *testing.T) {
		single := standaloneInteractiveContestant(req)
		if single == nil || single.Interactor != nil || single.Stdin != "" || single.StdinURL != "" ||
			single.ExpectedStdout != "" || single.ExpectedStdoutURL != "" || single.PythonLibraryMode != "" {
			t.Fatalf("interactor-only inputs reached the contestant: %+v", single)
		}
		if single.Lang != req.Lang || single.EntryPoint != req.EntryPoint || single.RuntimeProfile != req.RuntimeProfile ||
			!reflect.DeepEqual(single.Binaries, req.Binaries) || !reflect.DeepEqual(single.DisallowedCPUFeatures, req.DisallowedCPUFeatures) ||
			single.Limits != req.Limits || single.CaptureLimits != req.CaptureLimits {
			t.Fatal("contestant options changed")
		}
		copy := *req
		copy.Lang = "PYTHON3"
		if standaloneInteractiveContestant(&copy).PythonLibraryMode != "stdlib" || req.Interactor == nil || req.Stdin != "private input" {
			t.Fatal("lost contestant Python configuration or changed primary")
		}
	})
	for name, edit := range map[string]func(*model.RunRequest){
		"batch":         func(r *model.RunRequest) { r.Interactor = nil },
		"communication": func(r *model.RunRequest) { r.Communication = &model.CommunicationSpec{} },
		"pipeline":      func(r *model.RunRequest) { r.Pipeline = &model.PipelineV1{} },
		"legacy":        func(r *model.RunRequest) { r.Steps = []model.RunStep{{}} },
		"SPJ":           func(r *model.RunRequest) { r.SPJ = &model.SPJSpec{} },
		"entry":         func(r *model.RunRequest) { r.EntryMode = "state" },
		"network":       func(r *model.RunRequest) { r.EnableNetwork = true },
		"ignore TLE":    func(r *model.RunRequest) { r.IgnoreTLE = true },
		"sidecar":       func(r *model.RunRequest) { r.SidecarOutputs = []model.OutputFile{{Path: "output"}} },
		"output file":   func(r *model.RunRequest) { r.FileOutputs = []model.OutputFile{{Path: "output"}} },
	} {
		t.Run(name, func(t *testing.T) {
			copy := *req
			edit(&copy)
			if standaloneInteractiveContestant(&copy) != nil {
				t.Fatal("unreplayable request produced a contestant replay")
			}
		})
	}
}

func TestCompletedStandaloneInteractiveExchange(t *testing.T) {
	req := observerRequest()
	req.Interactor = &model.InteractorSpec{Lang: "CPP20"}
	clean := func(exit int) model.RunResponse {
		return interactiveResponse(req, interactorRunRequest(req),
			execResult{Status: "OK", ExitCode: new(int)}, execResult{Status: "OK", ExitCode: &exit}, 17, false, false)
	}
	for _, exit := range []int{0, 1, 2, 6} {
		primary := clean(exit)
		if !completedStandaloneInteractiveExchange(primary) {
			t.Fatalf("cleanly completed peer exit %d was rejected: %+v", exit, primary)
		}
		primary.Steps[0].StdoutTruncated = true
		primary.StdoutTruncated = exit != 0
		if !completedStandaloneInteractiveExchange(primary) {
			t.Fatalf("display clipping obscured a clean exchange with peer exit %d", exit)
		}
	}
	for name, edit := range map[string]func(*model.RunResponse){
		"grace cancellation":      func(p *model.RunResponse) { p.Steps[0].Status = model.RunStatusTLE },
		"nonzero contestant":      func(p *model.RunResponse) { exit := 2; p.Steps[0].ExitCode = &exit },
		"top exit":                func(p *model.RunResponse) { p.ExitCode = nil },
		"signal":                  func(p *model.RunResponse) { exit := -1; p.Steps[1].ExitCode = &exit },
		"interactor failure":      func(p *model.RunResponse) { exit := 3; p.Steps[1].ExitCode = &exit },
		"missing peer":            func(p *model.RunResponse) { p.Steps = p.Steps[:1] },
		"swapped peers":           func(p *model.RunResponse) { p.Steps[0], p.Steps[1] = p.Steps[1], p.Steps[0] },
		"wrong target":            func(p *model.RunResponse) { p.Steps[0].ProgramID = "interactor" },
		"wrong source":            func(p *model.RunResponse) { p.VerdictSource = "contestant" },
		"role failure source":     func(p *model.RunResponse) { p.Steps[1].VerdictSource = "interactor:wall_time" },
		"stale score":             func(p *model.RunResponse) { *p.Score = 1 },
		"interactor overflow":     func(p *model.RunResponse) { p.Steps[1].StdoutTruncated = true },
		"inconsistent truncation": func(p *model.RunResponse) { p.StdoutTruncated = true },
	} {
		t.Run(name, func(t *testing.T) {
			primary := clean(1)
			edit(&primary)
			if completedStandaloneInteractiveExchange(primary) {
				t.Fatalf("ineligible native result: %+v", primary)
			}
		})
	}
}

func TestStandaloneInteractiveObservationRunsNativeOnce(t *testing.T) {
	for _, tc := range []struct {
		name           string
		prepare        func(context.Context, *model.RunRequest, *Service) context.Context
		capture, early bool
	}{
		{"missing transcript", nil, true, false},
		{"failed preparation", nil, true, true},
		{"old replay budget", func(ctx context.Context, _ *model.RunRequest, _ *Service) context.Context {
			return WithPipelineObservationBudget(ctx, 0)
		}, false, false},
		{"unsupported contestant", func(ctx context.Context, req *model.RunRequest, _ *Service) context.Context {
			req.Lang = "binary"
			return ctx
		}, false, false},
		{"no policy", func(ctx context.Context, _ *model.RunRequest, s *Service) context.Context {
			s.workMeasurementPolicyError = errors.New("unavailable")
			return ctx
		}, false, false},
		{"sidecar", func(ctx context.Context, req *model.RunRequest, _ *Service) context.Context {
			req.SidecarOutputs = []model.OutputFile{{Path: "out"}}
			return ctx
		}, false, false},
		{"cancelled", func(ctx context.Context, _ *model.RunRequest, _ *Service) context.Context {
			ctx, cancel := context.WithCancel(ctx)
			cancel()
			return ctx
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := observerRequest()
			req.Interactor = &model.InteractorSpec{Lang: "PYTHON3"}
			svc := &Service{workMeasurement: observerConfig()}
			ctx := t.Context()
			if tc.prepare != nil {
				ctx = tc.prepare(ctx, req, svc)
			}
			primary := interactiveResponse(req, interactorRunRequest(req), execResult{Status: "OK", ExitCode: new(int)},
				execResult{Status: "OK", ExitCode: new(int)}, 50, false, false)
			if tc.early {
				primary = model.RunResponse{Status: model.RunStatusInitFail, Reason: "cannot start peer"}
			}
			count := 0
			got := svc.runWithObservation(ctx, req, Hooks{OnLog: func(string, string) {}}, func(ctx context.Context, native *model.RunRequest, hooks Hooks) model.RunResponse {
				count++
				if native != req || native.WorkMeasurement || hooks.OnLog == nil {
					t.Fatal("replaced native request or hooks")
				}
				if (ctx.Value(standaloneInteractiveCaptureKey{}) != nil) != tc.capture {
					t.Fatal("unexpected native recorder selection")
				}
				return primary
			})
			observation := got.WorkMeasurementObservation
			if count != 1 || observation == nil || observation.Version != 2 || observation.State != "interactive" ||
				observation.Authority != durante.Authority || observation.Report != nil || observation.StdinSHA256 != "" {
				t.Fatalf("observation: %+v", observation)
			}
			if tc.early {
				if len(observation.Steps) != 0 {
					t.Fatal("invented an executed contestant")
				}
			} else {
				if len(observation.Steps) != 1 {
					t.Fatalf("observation: %+v", observation)
				}
				row := observation.Steps[0]
				if row.StepID != "contestant" || row.ProgramID != "contestant" || row.State != "unavailable" ||
					row.Report != nil || row.OutputCheck != "" || strings.TrimSpace(row.Reason) == "" {
					t.Fatalf("row: %+v", row)
				}
			}
			got.WorkMeasurementObservation = nil
			if !reflect.DeepEqual(got, primary) {
				t.Fatal("original native judgement changed")
			}
		})
	}
}
