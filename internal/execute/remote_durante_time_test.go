package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func exchangeTimeMeasurementResult(t *testing.T, req *model.RunRequest, response model.RunResponse, measurement durante.Config) model.RunResponse {
	t.Helper()
	wire, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		var forwarded model.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
			t.Errorf("decode forwarded request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if forwarded.Lang != req.Lang || forwarded.WorkMeasurementTime != req.WorkMeasurementTime || forwarded.WorkMeasurement != req.WorkMeasurement || forwarded.Limits != req.Limits || !reflect.DeepEqual(forwarded.WorkMeasurementLimits, req.WorkMeasurementLimits) {
			t.Errorf("forwarding changed measurement flags or limits: %+v", forwarded)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: result\ndata: %s\n\n", wire)
	}))
	defer server.Close()
	runner := newRemoteRunner(config.Config{Execution: config.ExecutionConfig{
		WorkMeasurement: measurement, Remote: config.RemoteExecutorConfig{URL: server.URL},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := runner.Run(ctx, req, Hooks{})
	if !called.Load() {
		t.Fatalf("valid request did not reach the downstream runner: %+v", got)
	}
	return got
}

func TestRemoteWorkMeasurementTimeRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		signals      bool
		instructions uint64
	}{
		{"equal nominal limit", model.RunStatusAccepted, false, 10_000_000},
		{"exceeds nominal limit", model.RunStatusAccepted, false, 10_000_001},
		{"wrong output", model.RunStatusWA, false, 1234},
		{"conversion unavailable", model.RunStatusAccepted, true, 1234},
		{"raw resource stop", model.RunStatusTLE, false, 1234},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
			report := completeEnvelopeReport()
			report.Instructions = &tc.instructions
			if tc.signals {
				report.SignalsObserved, report.Quality = 1, "signals_observed"
			}
			local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, tc.status, report), cfg)
			got := exchangeTimeMeasurementResult(t, req, local, cfg)
			if !reflect.DeepEqual(got, local) {
				t.Fatalf("SSE round trip changed estimate, diagnostic status, or raw accounting:\nlocal=%+v\nremote=%+v", local, got)
			}
		})
	}
}

func TestRemoteWorkMeasurementTimeRejectsInconsistentEstimates(t *testing.T) {
	for name, edit := range map[string]func(*model.RunResponse){
		"model":         func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.ModelID = "other-model" },
		"model version": func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.ModelVersion++ },
		"runtime": func(r *model.RunResponse) {
			r.WorkMeasurementExecution.TimeEstimate.RuntimeID = "CPP17@sha256:" + strings.Repeat("a", 64)
		},
		"weights":            func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.InstructionNSNumerator++ },
		"nanoseconds":        func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.Nanoseconds++ },
		"milliseconds":       func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.Milliseconds++ },
		"nominal exceedance": func(r *model.RunResponse) { v := true; r.WorkMeasurementExecution.NominalTimeExceeded = &v },
		"missing exceedance": func(r *model.RunResponse) { r.WorkMeasurementExecution.NominalTimeExceeded = nil },
		"unavailable reason": func(r *model.RunResponse) {
			r.WorkMeasurementExecution.TimeEstimateUnavailable = "observation_not_convertible"
		},
		"promoted authority":                func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate.Authority = "trusted" },
		"observation differs from estimate": func(r *model.RunResponse) { (*r.WorkMeasurement.Instructions)++ },
		"old runner omitted conversion": func(r *model.RunResponse) {
			r.WorkMeasurementExecution.TimeEstimate, r.WorkMeasurementExecution.NominalTimeExceeded = nil, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
			local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, completeEnvelopeReport()), cfg)
			if local.WorkMeasurementExecution.TimeEstimate == nil {
				t.Fatal("fixture did not produce an estimate")
			}
			edit(&local)
			got := exchangeTimeMeasurementResult(t, req, local, cfg)
			if got.Status != model.RunStatusInitFail || got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "invalid remote result") {
				t.Fatalf("inconsistent estimate was accepted: %+v", got)
			}
		})
	}
}

func TestRemoteWorkMeasurementTimeRejectsChangedUnavailableReason(t *testing.T) {
	req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
	report := completeEnvelopeReport()
	report.SignalsObserved, report.Quality = 1, "signals_observed"
	local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, report), cfg)
	if local.WorkMeasurementExecution.TimeEstimateUnavailable != "observation_not_convertible" {
		t.Fatal("fixture must have a nonconvertible complete observation")
	}
	local.WorkMeasurementExecution.TimeEstimateUnavailable = "execution_cancelled"
	got := exchangeTimeMeasurementResult(t, req, local, cfg)
	if got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "invalid remote result") {
		t.Fatalf("unavailable reason was not checked against the observation: %+v", got)
	}
}

func TestRemoteWorkMeasurementTimeRejectsUnsolicitedFields(t *testing.T) {
	for _, field := range []string{"estimate", "exceeded", "unavailable"} {
		t.Run(field, func(t *testing.T) {
			req, cfg := timeMeasurementRequest(), timeMeasurementConfig()
			local := finishWorkMeasurementTime(req, timeMeasurementDiagnostic(req, model.RunStatusAccepted, completeEnvelopeReport()), cfg)
			d := local.WorkMeasurementExecution
			if field != "estimate" {
				d.TimeEstimate = nil
			}
			if field != "exceeded" {
				d.NominalTimeExceeded = nil
			}
			if field == "unavailable" {
				d.TimeEstimateUnavailable = "observation_not_convertible"
			}
			req.WorkMeasurementTime = false
			got := exchangeTimeMeasurementResult(t, req, local, cfg)
			if got.Status != model.RunStatusInitFail || got.WorkMeasurementExecution != nil || !strings.Contains(got.Reason, "unsolicited work measurement time") {
				t.Fatalf("unsolicited time field survived forwarding: %+v", got)
			}
		})
	}
}

func TestRemoteWorkMeasurementTimePreservesLegacyAndPreExecutionResults(t *testing.T) {
	t.Run("unrequested conversion", func(t *testing.T) {
		req := envelopeRequest()
		local := timeMeasurementDiagnostic(req, model.RunStatusAccepted, completeEnvelopeReport())
		got := exchangeTimeMeasurementResult(t, req, local, durante.Config{})
		if !reflect.DeepEqual(got, local) {
			t.Fatalf("existing diagnostic runner now requires a time model: %+v", got)
		}
	})
	t.Run("downstream admission failure", func(t *testing.T) {
		req := timeMeasurementRequest()
		local := model.RunResponse{Status: model.RunStatusInitFail, Reason: "downstream policy unavailable"}
		got := exchangeTimeMeasurementResult(t, req, local, timeMeasurementConfig())
		if !reflect.DeepEqual(got, local) {
			t.Fatalf("pre-execution failure required nonexistent estimate metadata: %+v", got)
		}
	})
}

func TestRemoteWorkMeasurementTimeRejectsMissingOrMismatchedConfigurationBeforeUpload(t *testing.T) {
	for name, edit := range map[string]func(*durante.Config){
		"missing policy":             func(c *durante.Config) { c.TimePolicy = nil },
		"missing image":              func(c *durante.Config) { c.RuntimeImage = "" },
		"different deployment image": func(c *durante.Config) { c.RuntimeImage = "sha256:" + strings.Repeat("b", 64) },
		"different source profile":   func(c *durante.Config) { c.TimePolicy.RuntimeID = "CPP17@" + c.RuntimeImage },
	} {
		t.Run(name, func(t *testing.T) {
			measurement := timeMeasurementConfig()
			edit(&measurement)
			var called atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called.Store(true)
				http.Error(w, "unexpected upload", http.StatusInternalServerError)
			}))
			defer server.Close()
			runner := newRemoteRunner(config.Config{Execution: config.ExecutionConfig{
				WorkMeasurement: measurement, Remote: config.RemoteExecutorConfig{URL: server.URL},
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got := runner.Run(ctx, timeMeasurementRequest(), Hooks{})
			if called.Load() || got.Status != model.RunStatusInitFail || got.Reason == "" || got.WorkMeasurementExecution != nil {
				t.Fatalf("invalid front-end conversion policy reached execution: called=%t response=%+v", called.Load(), got)
			}
		})
	}
}
