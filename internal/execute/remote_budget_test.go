package execute

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func TestRemoteExecutionBudgetBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    model.RunRequest
		upload time.Duration
		want   time.Duration
	}{
		{
			name: "one second execution includes setup upload and transfer",
			req:  model.RunRequest{Limits: model.Limits{TimeMs: 1000}},
			want: 253_200 * time.Millisecond,
		},
		{
			name: "stdin URL download is separate from sandbox setup",
			req:  model.RunRequest{Limits: model.Limits{TimeMs: 1000}, StdinURL: "https://input.example/data"},
			want: 313_200 * time.Millisecond,
		},
		{
			name: "long execution also has a full startup allowance",
			req:  model.RunRequest{Limits: model.Limits{TimeMs: 120_000}},
			want: 3228 * time.Second,
		},
		{
			name:   "custom upload allowance",
			req:    model.RunRequest{Limits: model.Limits{TimeMs: 1000}},
			upload: 5 * time.Second,
			want:   228_200 * time.Millisecond,
		},
		{
			name: "default checker is a separate sandbox stage",
			req:  model.RunRequest{Limits: model.Limits{TimeMs: 1000}, SPJ: &model.SPJSpec{}},
			want: 188_800 * time.Millisecond,
		},
		{
			name: "interactive startup and execution cover the longer role",
			req: model.RunRequest{
				Limits:     model.Limits{TimeMs: 1000},
				Interactor: &model.InteractorSpec{Limits: &model.Limits{TimeMs: 90_000}},
			},
			want: 852 * time.Second,
		},
		{
			name: "interactive omitted role time inherits contestant time",
			req: model.RunRequest{
				Limits:     model.Limits{TimeMs: 9000},
				Interactor: &model.InteractorSpec{Limits: &model.Limits{}},
			},
			want: 159_600 * time.Millisecond,
		},
		{
			name: "legacy URL parts share one download allowance",
			req: model.RunRequest{Steps: []model.RunStep{
				{Limits: model.Limits{TimeMs: 1000}, StdinURL: "https://input.example/a"},
				{Limits: model.Limits{TimeMs: 2000}, StdinParts: []model.StdinPart{
					{Type: "text", DataURL: "https://input.example/b"},
					{Type: "text", DataURL: "https://input.example/c"},
				}},
			}},
			want: 253_200 * time.Millisecond,
		},
		{
			name: "pipeline resources are resolved before the remote call",
			req: model.RunRequest{Pipeline: &model.PipelineV1{
				Resources: map[string]model.PipelineResource{"input": {DataURL: "https://input.example/a"}},
				Steps:     []model.PipelineStep{{Limits: model.Limits{TimeMs: 1000}}},
			}},
			want: 124_400 * time.Millisecond,
		},
		{
			name: "pipeline interactive role and long checker each get setup",
			req: model.RunRequest{Pipeline: &model.PipelineV1{
				Steps: []model.PipelineStep{{
					Limits:   model.Limits{TimeMs: 1000},
					Executor: model.PipelineExecutor{Kind: "interactive", InteractorLimits: &model.Limits{TimeMs: 90_000}},
				}},
				FinalJudge: model.PipelineFinalJudge{SPJ: &model.SPJSpec{Limits: &model.Limits{TimeMs: 120_000}}},
			}},
			want: 1908 * time.Second,
		},
		{
			name: "explicit wall allowance is reserved for each batch sample",
			req:  model.RunRequest{Limits: model.Limits{TimeMs: 1000, WallTimeMs: 30_000}},
			want: 330 * time.Second,
		},
		{
			name: "explicit wall allowance for interactive role is also reserved remotely",
			req: model.RunRequest{
				Limits:     model.Limits{TimeMs: 1000, WallTimeMs: 30_000},
				Interactor: &model.InteractorSpec{Limits: &model.Limits{TimeMs: 1000}},
			},
			want: 150 * time.Second,
		},
		{
			name: "interactive pipeline step receives slack, batch step does not",
			req: model.RunRequest{Pipeline: &model.PipelineV1{
				Steps: []model.PipelineStep{
					{Limits: model.Limits{TimeMs: 500}, Executor: model.PipelineExecutor{Kind: "interactive"}},
					{Limits: model.Limits{TimeMs: 500}, Executor: model.PipelineExecutor{Kind: "batch"}},
				},
			}},
			want: 185_700 * time.Millisecond,
		},
		{
			name: "explicit interactive wall overrides default slack",
			req: model.RunRequest{
				Limits:     model.Limits{TimeMs: 10_000, WallTimeMs: 12_000},
				Interactor: &model.InteractorSpec{Limits: &model.Limits{TimeMs: 10_000}},
			},
			want: 132 * time.Second,
		},
		{
			name: "explicit sub-CPU wall deadline is never scaled",
			req: model.RunRequest{
				Limits: model.Limits{TimeMs: 600_000, WallTimeMs: 250}, IgnoreTLE: true,
			},
			want: 120_250 * time.Millisecond,
		},
		{
			name: "pipeline role walls and wall-only checker remain explicit",
			req: model.RunRequest{Pipeline: &model.PipelineV1{
				Steps: []model.PipelineStep{{
					Limits:   model.Limits{TimeMs: 1000, WallTimeMs: 30_000},
					Executor: model.PipelineExecutor{Kind: "interactive", InteractorLimits: &model.Limits{TimeMs: 20_000, WallTimeMs: 5000}},
				}},
				FinalJudge: model.PipelineFinalJudge{SPJ: &model.SPJSpec{Limits: &model.Limits{WallTimeMs: 90_000}}},
			}},
			want: 330 * time.Second,
		},
		{
			name: "interactive minimum slack exceeds the normalized CPU guard",
			req: model.RunRequest{
				Limits: model.Limits{TimeMs: 1}, Interactor: &model.InteractorSpec{},
			},
			want: 123_001 * time.Millisecond,
		},
		{
			name: "zero time still has a nonzero execution allowance",
			req:  model.RunRequest{},
			want: 240_312 * time.Millisecond,
		},
		{
			name: "unvalidated excessive time cannot overflow the budget",
			req:  model.RunRequest{Limits: model.Limits{TimeMs: runvalidation.MaxTimeMs + 1}},
			want: 15900 * time.Second,
		},
		{
			name: "communication covers manager time and one CPU scheduling waves",
			req: model.RunRequest{
				Limits:        model.Limits{TimeMs: 10_000},
				Communication: &model.CommunicationSpec{ParticipantCount: 16},
			},
			// 17 targets at the manager's 44-second raw CPU limit plus 15% wall slack,
			// a minute for startup, and the upload/transfer allowances.
			want: 980_200 * time.Millisecond,
		},
		{
			name: "maximum communication remains bounded",
			req: model.RunRequest{
				Limits:        model.Limits{TimeMs: runvalidation.MaxTimeMs},
				Communication: &model.CommunicationSpec{ParticipantCount: 64},
			},
			want: 179_520 * time.Second,
		},
		{
			name: "unvalidated communication values cannot overflow the budget",
			req: model.RunRequest{
				Limits:        model.Limits{TimeMs: int(^uint(0) >> 1)},
				Communication: &model.CommunicationSpec{ParticipantCount: int(^uint(0) >> 1)},
			},
			want: 179_520 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := remoteExecutionTimeout(&tc.req, tc.upload); got != tc.want {
				t.Fatalf("remote execution budget = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestComputedRemoteDeadlineStillExpiresWithHeartbeats(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runner := &remoteRunner{
			client: &http.Client{Transport: executeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(req.Context())
				if trace != nil && trace.WroteRequest != nil {
					trace.WroteRequest(httptrace.WroteRequestInfo{})
				}
				reader, writer := io.Pipe()
				go func() {
					defer writer.Close()
					ticker := time.NewTicker(5 * time.Second)
					defer ticker.Stop()
					for {
						select {
						case <-req.Context().Done():
							_ = writer.CloseWithError(req.Context().Err())
							return
						case <-ticker.C:
							if _, err := fmt.Fprint(writer, ": heartbeat\n\n"); err != nil {
								return
							}
						}
					}
				}()
				return &http.Response{
					StatusCode: http.StatusOK, Status: "200 OK",
					Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader,
				}, nil
			})},
			executeURL: "http://remote.example/execute",
		}
		started := time.Now()
		response := runner.Run(context.Background(), &model.RunRequest{
			StdinURL: "https://input.example/data", Limits: model.Limits{TimeMs: 1000},
		}, Hooks{})
		if response.Status != model.RunStatusInitFail || !strings.Contains(response.Reason, "absolute deadline") {
			t.Fatalf("heartbeats bypassed the computed deadline: %+v", response)
		}
		if elapsed := time.Since(started); elapsed != 313_200*time.Millisecond {
			t.Fatalf("computed deadline expired after %s, want 313.2s", elapsed)
		}
	})
}
