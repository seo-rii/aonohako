package execute

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/remoteio"
)

func TestRemotePipelineObservationBudgetMatchesDeadline(t *testing.T) {
	for _, observe := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ordinary := observerRequest()
			step := model.PipelineStep{ID: "first", Executor: model.PipelineExecutor{Kind: "batch", ProgramID: "p"}, Limits: ordinary.Limits}
			req := &model.RunRequest{Pipeline: &model.PipelineV1{Version: 1, Steps: []model.PipelineStep{step},
				Programs: []model.RunProgram{{ID: "p", Lang: ordinary.Lang, Binaries: ordinary.Binaries}}}}
			cfg := observerConfig()
			cfg.Observe = observe
			budget := remoteObservationBudget(req, cfg)
			if (budget > 0) != observe || budget > MaxPipelineObservationBudget {
				t.Fatal("test request eligibility changed")
			}
			wantDeadline := remoteExecutionTimeout(req, 0) + budget
			if observe {
				wantDeadline += remoteio.DefaultOperationOverhead
			}
			called := false
			runner := &remoteRunner{executeURL: "https://worker.example/execute", workMeasurement: cfg,
				client: &http.Client{Transport: executeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					called = true
					deadline, ok := r.Context().Deadline()
					if r.Header.Get(model.PipelineObservationBudgetHeader) != strconv.FormatInt(budget.Milliseconds(), 10) ||
						!ok || time.Until(deadline) != wantDeadline {
						t.Errorf("observe=%v header=%q time remaining=%v", observe, r.Header.Get(model.PipelineObservationBudgetHeader), time.Until(deadline))
					}
					return nil, errors.New("probe complete")
				})}}
			runner.Run(context.Background(), req, Hooks{})
			if !called {
				t.Fatal("pipeline did not reach worker")
			}
		})
	}
}

func TestPipelineObservationBudgetExpiryKeepsNativeJudgment(t *testing.T) {
	requireSandboxSupport(t)
	s := New()
	s.workMeasurement = observerConfig()
	ctx := WithPipelineObservationBudget(t.Context(), time.Nanosecond)
	resp := s.Run(ctx, syntheticInteractiveBatchPipeline(4096), Hooks{})
	if resp.Status != model.RunStatusAccepted || resp.VerdictSource != "final:spj" || len(resp.Steps) != 2 {
		t.Fatalf("diagnostic deadline changed native verdict: %+v", resp)
	}
	observation := resp.WorkMeasurementObservation
	if observation == nil || len(observation.Steps) != 2 {
		t.Fatal("missing executed steps")
	}
	for _, row := range observation.Steps {
		if row.State != "unavailable" || row.Reason != "pipeline observation cancelled" || row.Report != nil {
			t.Fatalf("unexpected replay after observation deadline: %+v", row)
		}
	}
}

func TestLegacyBudgetPreservesWorkerJudgment(t *testing.T) {
	ordinary := observerRequest()
	req := &model.RunRequest{
		Programs: []model.RunProgram{{ID: "p", Lang: ordinary.Lang, Binaries: ordinary.Binaries}},
		Steps: []model.RunStep{
			{ID: "one", ProgramID: "p", Limits: ordinary.Limits, Handoff: &model.StepHandoff{ID: "data", From: "stdout"}},
			{ID: "two", ProgramID: "p", Limits: ordinary.Limits, StdinFrom: "data"},
		},
	}
	zero := 0
	native := model.RunResponse{Status: model.RunStatusAccepted, CPUTimeMs: 7, VerdictSource: "step:two:stdout", Steps: []model.StepResult{
		{ID: "one", ProgramID: "p", Status: model.RunStatusAccepted, CPUTimeMs: 3, ExitCode: &zero},
		{ID: "two", ProgramID: "p", Status: model.RunStatusAccepted, CPUTimeMs: 4, ExitCode: &zero, VerdictSource: "stdout"},
	}}
	report := completeEnvelopeReport()
	report.CPUIDProfile = observerConfig().CPUIDProfile
	receipt := &model.WorkMeasurementObservation{Version: 2, State: "steps", Authority: durante.Authority}
	for _, step := range native.Steps {
		receipt.Steps = append(receipt.Steps, model.PipelineStepObservation{StepID: step.ID, ProgramID: step.ProgramID,
			State: "complete", OutputCheck: "native_output", Report: report})
	}
	configured := remoteObservationBudget(req, observerConfig())
	if configured <= 0 || configured > MaxLegacyObservationBudget {
		t.Fatalf("unexpected budget for both steps: %v", configured)
	}
	for _, tc := range []struct {
		name                   string
		observe, workerReports bool
		incoming, want         time.Duration // A negative input means the caller supplied no header.
	}{
		{"controller disabled", false, false, -1, 0},
		{"controller enabled", true, false, -1, configured},
		{"caller suppresses replay", true, true, 0, 0},
		{"caller shortens budget", true, true, 9 * time.Millisecond, 9 * time.Millisecond},
		{"caller cannot extend budget", true, false, MaxLegacyObservationBudget, configured},
		{"caller cannot enable disabled policy", false, true, time.Second, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cfg := observerConfig()
				cfg.Observe = tc.observe
				ctx := t.Context()
				if tc.incoming >= 0 {
					ctx = WithPipelineObservationBudget(ctx, tc.incoming)
				}
				requests := 0
				runner := &remoteRunner{executeURL: "https://worker.example/execute", workMeasurement: cfg,
					client: &http.Client{Transport: executeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						requests++
						if r.Method != http.MethodPost || r.URL.Path != "/execute" || r.Header.Get(model.LegacyObservationBudgetHeader) != strconv.FormatInt(tc.want.Milliseconds(), 10) ||
							r.Header.Get(model.PipelineV1WorkerHeader) != "" || r.Header.Get(model.PipelineObservationBudgetHeader) != "" {
							t.Error("legacy execution received incorrect observation headers or a capability probe")
						}
						wantDeadline := remoteExecutionTimeout(req, 0) + tc.want
						if tc.want > 0 {
							wantDeadline += remoteio.DefaultOperationOverhead
						}
						if d, ok := r.Context().Deadline(); !ok || time.Until(d) != wantDeadline {
							t.Errorf("time remaining = %v, want %v", time.Until(d), wantDeadline)
						}
						response := native
						// Older workers omit the optional receipt.
						if tc.workerReports {
							response.WorkMeasurementObservation = receipt
						}
						encoded, err := json.Marshal(response)
						if err != nil {
							return nil, err
						}
						body := "event: result\ndata: " + string(encoded) + "\n\n"
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
					})}}
				got := runner.Run(ctx, req, Hooks{})
				if requests != 1 || got.Status != model.RunStatusAccepted || got.CPUTimeMs != 7 || len(got.Steps) != 2 || got.VerdictSource != "step:two:stdout" {
					t.Fatalf("native legacy result changed: requests=%d response=%+v", requests, got)
				}
				obs := got.WorkMeasurementObservation
				switch {
				case !tc.observe:
					if obs != nil {
						t.Fatalf("disabled controller accepted observation: %+v", obs)
					}
				case tc.workerReports:
					if obs == nil || obs.State != "steps" || len(obs.Steps) != len(native.Steps) {
						t.Fatalf("missing stage receipt: %+v", obs)
					}
					for _, row := range obs.Steps {
						if (row.State == "complete") != (tc.want > 0) || (row.Report != nil) != (tc.want > 0) {
							t.Fatalf("wrong receipt for budget %v: %+v", tc.want, row)
						}
					}
				default:
					if obs == nil || obs.State != "unavailable" {
						t.Fatalf("unexpected receipt from old worker: %+v", obs)
					}
				}
			})
		})
	}
}
