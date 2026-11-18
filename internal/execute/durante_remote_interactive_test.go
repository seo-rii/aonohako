package execute

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/remoteio"
)

func observedInteractiveRemoteRequest() *model.RunRequest {
	req := observerRequest()
	req.Binaries = []model.Binary{{Name: "solution", DataB64: "eA==", Mode: "exec"}}
	req.Interactor = &model.InteractorSpec{Lang: "binary", Binaries: []model.Binary{{Name: "oracle", DataB64: "eQ==", Mode: "exec"}}}
	req.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.AVX}
	return req
}

func observedInteractiveRemoteResult(req *model.RunRequest, judgeExit int) model.RunResponse {
	zero := 0
	contestant := execResult{Status: "OK", ExitCode: &zero, CPUTimeMs: 7, CPUTimeNs: 7000001, MemoryKB: 1000}
	judge := execResult{Status: "OK", ExitCode: &judgeExit, CPUTimeMs: 6, CPUTimeNs: 6000002, MemoryKB: 800}
	return interactiveResponse(req, interactorRunRequest(req), contestant, judge, 15, false, false)
}

func observedInteractiveRemoteReceipt() model.WorkMeasurementObservation {
	report := completeEnvelopeReport()
	report.CPUIDProfile = observerConfig().CPUIDProfile
	report.DisallowedCPUFeatures = []cpupolicy.Feature{cpupolicy.AVX}
	return model.WorkMeasurementObservation{Version: 2, Authority: durante.Authority, State: "interactive",
		Steps: []model.PipelineStepObservation{{StepID: "contestant", ProgramID: "contestant", State: "complete",
			OutputCheck: "interactive_transcript", Report: report}}}
}

func TestRemoteStandaloneInteractiveReceiptUsesNativeRoles(t *testing.T) {
	type fixture struct {
		req     *model.RunRequest
		native  model.RunResponse
		receipt model.WorkMeasurementObservation
	}
	for _, tc := range []struct {
		name     string
		change   func(*fixture)
		complete bool
	}{
		{"AC with native total above contestant CPU limit", nil, true},
		{"clean WA", func(f *fixture) { f.native = observedInteractiveRemoteResult(f.req, 1) }, true},
		{"grace shutdown", func(f *fixture) { f.native.Steps[0].ExitCode = nil }, false},
		{"missing judge", func(f *fixture) { f.native.Steps = f.native.Steps[:1] }, false},
		{"swapped judge", func(f *fixture) { f.native.Steps[1].ProgramID = "contestant" }, false},
		{"coordinator verdict inconsistent", func(f *fixture) { f.native.VerdictSource = "stdout" }, false},
		{"display-clipped contestant preview", func(f *fixture) { f.native.Steps[0].StdoutTruncated = true }, true},
		{"truncated judge stream", func(f *fixture) { f.native.Steps[1].StdoutTruncated = true }, false},
		{"contradictory aggregate", func(f *fixture) { f.native.CPUTimeNs++ }, false},
		{"contestant over its own limit", func(f *fixture) { f.req.Limits.TimeMs = 6 }, false},
		{"interactor over its own limit", func(f *fixture) { f.req.Interactor.Limits = &model.Limits{TimeMs: 5} }, false},
		{"interactor report", func(f *fixture) { f.receipt.Steps[0].ProgramID = "interactor" }, false},
		{"extra row", func(f *fixture) { f.receipt.Steps = append(f.receipt.Steps, f.receipt.Steps[0]) }, false},
		{"batch check", func(f *fixture) { f.receipt.Steps[0].OutputCheck = "native_output" }, false},
		{"unknown counters", func(f *fixture) { f.receipt.Steps[0].Report.ClientVersion++ }, false},
		{"foreign profile", func(f *fixture) { f.receipt.Steps[0].Report.CPUIDProfile = "" }, false},
		{"missing CPU restriction", func(f *fixture) { f.receipt.Steps[0].Report.DisallowedCPUFeatures = nil }, false},
		{"changed request features", func(f *fixture) { f.req.DisallowedCPUFeatures = nil }, false},
		{"nominal over envelope", func(f *fixture) { f.req.Limits.MemoryMB = 8192 }, false},
		{"unsupported contestant", func(f *fixture) { f.req.Lang = "binary" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := observedInteractiveRemoteRequest()
			f := fixture{req, observedInteractiveRemoteResult(req, 0), observedInteractiveRemoteReceipt()}
			if tc.change != nil {
				tc.change(&f)
			}
			wire := f.native
			wire.WorkMeasurementObservation = &f.receipt
			got := exchangeTimeMeasurementResult(t, req, wire, observerConfig())
			obs := got.WorkMeasurementObservation
			got.WorkMeasurementObservation = nil
			if !reflect.DeepEqual(got, f.native) {
				t.Fatalf("native verdict or role counters changed: got %+v, want %+v", got, f.native)
			}
			if obs == nil {
				t.Fatal("expected an observation result")
			}
			complete := obs.State == "interactive" && len(obs.Steps) == 1 && obs.Steps[0].State == "complete" && obs.Steps[0].Report != nil
			if complete != tc.complete {
				t.Fatalf("unexpected receipt: %+v", obs)
			}
		})
	}
}

func TestRemoteStandaloneInteractiveBudgetMixedVersions(t *testing.T) {
	req := observedInteractiveRemoteRequest()
	cfg := observerConfig()
	allowed := remoteObservationBudget(req, cfg)
	if allowed <= 0 || allowed > MaxInteractiveObservationBudget {
		t.Fatalf("expected a bounded replay allowance: %s", allowed)
	}
	native := observedInteractiveRemoteResult(req, 0)
	report, err := json.Marshal(observedInteractiveRemoteReceipt())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name           string
		observe        bool
		incoming, want time.Duration
		receipt        json.RawMessage
		wantState      string
	}{
		{"off controller and enabled worker", false, -1, 0, report, ""},
		{"on controller and old worker", true, -1, allowed, nil, "unavailable"},
		{"on controller and incompatible worker", true, -1, allowed, json.RawMessage(`{"version":"future"}`), "unavailable"},
		{"on controller and enabled worker", true, -1, allowed, report, "complete"},
		{"caller suppresses", true, 0, 0, report, "unavailable"},
		{"caller shortens", true, 10 * time.Millisecond, 10 * time.Millisecond, report, "complete"},
		{"caller cannot extend", true, MaxInteractiveObservationBudget, allowed, report, "complete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				if tc.incoming >= 0 {
					ctx = WithPipelineObservationBudget(ctx, tc.incoming)
				}
				policy := cfg
				policy.Observe = tc.observe
				posted := 0
				runner := remoteRunner{executeURL: "https://worker.example/execute", workMeasurement: policy,
					client: &http.Client{Transport: executeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						posted++
						if r.Method != http.MethodPost || r.URL.Path != "/execute" || r.Header.Get(model.PipelineV1WorkerHeader) != "" ||
							r.Header.Get(model.InteractiveObservationBudgetHeader) != strconv.FormatInt(tc.want.Milliseconds(), 10) {
							t.Error("unexpected worker request or observation header")
						}
						wantTimeout := remoteExecutionTimeout(req, 0) + tc.want
						if tc.want > 0 {
							wantTimeout += remoteio.DefaultOperationOverhead
						}
						if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) != wantTimeout {
							t.Errorf("deadline does not cover native and negotiated replay budget: want %s", wantTimeout)
						}
						wire, err := json.Marshal(struct {
							model.RunResponse
							Observation json.RawMessage `json:"work_measurement_observation,omitempty"`
						}{native, tc.receipt})
						if err != nil {
							return nil, err
						}
						body := io.NopCloser(strings.NewReader("event: result\ndata: " + string(wire) + "\n\n"))
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
					})}}
				result := runner.Run(ctx, req, Hooks{})
				obs := result.WorkMeasurementObservation
				result.WorkMeasurementObservation = nil
				if posted != 1 || !reflect.DeepEqual(result, native) {
					t.Fatalf("posted %d times or changed native result: %+v", posted, result)
				}
				gotState := ""
				if obs != nil {
					gotState = obs.State
					if len(obs.Steps) == 1 {
						gotState = obs.Steps[0].State
					}
				}
				if gotState != tc.wantState {
					t.Fatalf("unexpected receipt: %+v", obs)
				}
			})
		})
	}
}
