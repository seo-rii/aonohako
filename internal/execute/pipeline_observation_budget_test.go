package execute

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

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
