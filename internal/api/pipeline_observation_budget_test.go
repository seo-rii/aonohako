package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/model"
)

func TestPipelineWorkerValidatesObservationBudgetBeforeAdmission(t *testing.T) {
	cfg := configForTest(t)
	cfg.InboundAuth = config.InboundAuthConfig{Mode: config.InboundAuthBearer, BearerToken: "worker"}
	handler := NewWithServices(cfg, nil, nil).Handler()
	maximum := execute.MaxPipelineObservationBudget.Milliseconds()
	for _, tc := range []struct {
		name     string
		protocol bool
		values   []string
		want     string
	}{
		{"missing", true, nil, "invalid_pipeline_observation_budget"},
		{"empty", true, []string{""}, "invalid_pipeline_observation_budget"},
		{"negative", true, []string{"-1"}, "invalid_pipeline_observation_budget"},
		{"non-canonical", true, []string{"01"}, "invalid_pipeline_observation_budget"},
		{"repeated", true, []string{"0", "1"}, "invalid_pipeline_observation_budget"},
		{"excessive", true, []string{strconv.FormatInt(maximum+1, 10)}, "invalid_pipeline_observation_budget"},
		{"unnegotiated", false, []string{"0"}, "unsupported_capability"},
		// Well-formed metadata reaches ordinary validation of this incomplete pipeline.
		{"disabled", true, []string{"0"}, "invalid_request"},
		{"maximum", true, []string{strconv.FormatInt(maximum, 10)}, "invalid_request"},
		{"public", false, nil, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(`{"pipeline":{"version":1}}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer worker")
			if tc.protocol {
				r.Header.Set(model.PipelineV1WorkerHeader, model.PipelineV1WorkerProtocol)
			}
			for _, value := range tc.values {
				r.Header.Add(model.PipelineObservationBudgetHeader, value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var payload struct{ Error string }
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != http.StatusBadRequest || payload.Error != tc.want {
				t.Fatalf("status=%d body=%s, want %s", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestLegacyObservationBudgetRequiresNoWorkerHandshake(t *testing.T) {
	handler := newServerForTest(t).Handler()
	maximum := execute.MaxLegacyObservationBudget.Milliseconds()
	for _, tc := range []struct {
		name, payload string
		header        []string
		want          string
	}{
		// Incomplete requests let us distinguish ordinary payload validation from a rejected header.
		{"old client", `{"steps":[{}]}`, nil, "invalid_request"},
		{"zero", `{"steps":[{}]}`, []string{"0"}, "invalid_request"},
		{"max", `{"steps":[{}]}`, []string{strconv.FormatInt(maximum, 10)}, "invalid_request"},
		{"negative", `{"steps":[{}]}`, []string{"-5"}, "invalid_legacy_observation_budget"},
		{"duplicate", `{"steps":[{}]}`, []string{"5", "5"}, "invalid_legacy_observation_budget"},
		{"excessive", `{"steps":[{}]}`, []string{strconv.FormatInt(maximum+1, 10)}, "invalid_legacy_observation_budget"},
		{"pipeline v1", `{"pipeline":{"version":1}}`, []string{"5"}, "invalid_legacy_observation_budget"},
		{"standalone", `{}`, []string{"5"}, "invalid_legacy_observation_budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(tc.payload))
			r.Header.Set("Content-Type", "application/json")
			for _, value := range tc.header {
				r.Header.Add(model.LegacyObservationBudgetHeader, value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var payload struct{ Error string }
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != http.StatusBadRequest || payload.Error != tc.want {
				t.Fatalf("status=%d body=%s, want %s", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestStandaloneInteractiveObservationBudgetIsOptional(t *testing.T) {
	handler := newServerForTest(t).Handler()
	maximum := execute.MaxInteractiveObservationBudget.Milliseconds()
	for _, tc := range []struct {
		name, payload string
		header        []string
		want          string
	}{
		{"direct", `{"interactor":{}}`, nil, "invalid_request"},
		{"off", `{"interactor":{}}`, []string{"0"}, "invalid_request"},
		{"maximum", `{"interactor":{}}`, []string{strconv.FormatInt(maximum, 10)}, "invalid_request"},
		{"malformed", `{"interactor":{}}`, []string{"-1"}, "invalid_interactive_observation_budget"},
		{"duplicate", `{"interactor":{}}`, []string{"1", "1"}, "invalid_interactive_observation_budget"},
		{"excessive", `{"interactor":{}}`, []string{strconv.FormatInt(maximum+1, 10)}, "invalid_interactive_observation_budget"},
		{"batch", `{}`, []string{"1"}, "invalid_interactive_observation_budget"},
		{"legacy", `{"interactor":{},"steps":[{}]}`, []string{"1"}, "invalid_interactive_observation_budget"},
		{"pipeline", `{"pipeline":{"version":1}}`, []string{"1"}, "invalid_interactive_observation_budget"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(tc.payload))
			r.Header.Set("Content-Type", "application/json")
			for _, value := range tc.header {
				r.Header.Add(model.InteractiveObservationBudgetHeader, value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var payload struct{ Error string }
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != http.StatusBadRequest || payload.Error != tc.want {
				t.Fatalf("status=%d body=%s, want %s", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}
