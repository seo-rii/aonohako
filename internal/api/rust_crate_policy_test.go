package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/rustpolicy"
)

func rustCompilePayload(t *testing.T, lang, mode, problemID string) []byte {
	t.Helper()
	payload := map[string]any{
		"lang": lang,
		"sources": []map[string]any{{
			"name":     "main.rs",
			"data_b64": base64.StdEncoding.EncodeToString([]byte("fn main() {}\n")),
		}},
	}
	if mode != "" {
		payload["rust_crate_mode"] = mode
	}
	if problemID != "" {
		payload["problem_id"] = problemID
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return body
}

func postRustCratePolicyRequest(t *testing.T, serverURL string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL+"/compile", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("compile request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func rustPolicyServer(t *testing.T, cfg config.Config, run func(*model.CompileRequest) model.CompileResponse) *httptest.Server {
	t.Helper()
	s := NewWithServices(cfg, compileRunnerStub{run: func(_ context.Context, req *model.CompileRequest) model.CompileResponse {
		return run(req)
	}}, execute.New())
	return httptest.NewServer(s.Handler())
}

func TestCompileDefaultsRustCrateModeToStdlib(t *testing.T) {
	cfg := configForTest(t)
	var seen rustpolicy.CrateMode
	ts := rustPolicyServer(t, cfg, func(req *model.CompileRequest) model.CompileResponse {
		seen = req.RustCrateMode
		return model.CompileResponse{Status: model.CompileStatusOK}
	})
	defer ts.Close()

	status, body := postRustCratePolicyRequest(t, ts.URL, rustCompilePayload(t, "RUST2021", "", ""))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	if seen != rustpolicy.CrateModeStdlib {
		t.Fatalf("runner mode = %q, want stdlib", seen)
	}
}

func TestCompileEnforcesInstalledRustCratePolicy(t *testing.T) {
	t.Run("direct request denied", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultRustCrateMode = rustpolicy.CrateModeStdlib
		cfg.AllowRequestRustInstalledCrates = false
		ts := rustPolicyServer(t, cfg, func(*model.CompileRequest) model.CompileResponse {
			t.Fatalf("runner should not receive denied installed mode")
			return model.CompileResponse{}
		})
		defer ts.Close()

		status, body := postRustCratePolicyRequest(t, ts.URL, rustCompilePayload(t, "RUST2021", "installed", ""))
		if status != http.StatusBadRequest || !strings.Contains(body, "server policy") {
			t.Fatalf("status = %d, body=%s", status, body)
		}
	})

	t.Run("direct request allowed", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultRustCrateMode = rustpolicy.CrateModeStdlib
		cfg.AllowRequestRustInstalledCrates = true
		var seen rustpolicy.CrateMode
		ts := rustPolicyServer(t, cfg, func(req *model.CompileRequest) model.CompileResponse {
			seen = req.RustCrateMode
			return model.CompileResponse{Status: model.CompileStatusOK}
		})
		defer ts.Close()

		status, body := postRustCratePolicyRequest(t, ts.URL, rustCompilePayload(t, "RUST2021", "installed", ""))
		if status != http.StatusOK || seen != rustpolicy.CrateModeInstalled {
			t.Fatalf("status = %d, body=%s, mode=%q", status, body, seen)
		}
	})

	t.Run("installed server default needs no request elevation", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultRustCrateMode = rustpolicy.CrateModeInstalled
		cfg.AllowRequestRustInstalledCrates = false
		var seen rustpolicy.CrateMode
		ts := rustPolicyServer(t, cfg, func(req *model.CompileRequest) model.CompileResponse {
			seen = req.RustCrateMode
			return model.CompileResponse{Status: model.CompileStatusOK}
		})
		defer ts.Close()

		status, body := postRustCratePolicyRequest(t, ts.URL, rustCompilePayload(t, "RUST2021", "installed", ""))
		if status != http.StatusOK || seen != rustpolicy.CrateModeInstalled {
			t.Fatalf("status = %d, body=%s, mode=%q", status, body, seen)
		}
	})
}

func TestCompileAppliesProblemRustCrateMode(t *testing.T) {
	cfg := configForTest(t)
	cfg.DefaultRustCrateMode = rustpolicy.CrateModeStdlib
	cfg.AllowRequestRustInstalledCrates = false
	cfg.ProblemRustCrateModes = map[string]rustpolicy.CrateMode{"contest-1/a": rustpolicy.CrateModeInstalled}
	var seen rustpolicy.CrateMode
	ts := rustPolicyServer(t, cfg, func(req *model.CompileRequest) model.CompileResponse {
		seen = req.RustCrateMode
		return model.CompileResponse{Status: model.CompileStatusOK}
	})
	defer ts.Close()

	status, body := postRustCratePolicyRequest(t, ts.URL, rustCompilePayload(t, "RUST2021", "", "contest-1/a"))
	if status != http.StatusOK || seen != rustpolicy.CrateModeInstalled {
		t.Fatalf("status = %d, body=%s, mode=%q", status, body, seen)
	}

	status, body = postRustCratePolicyRequest(t, ts.URL, rustCompilePayload(t, "RUST2021", "stdlib", "contest-1/a"))
	if status != http.StatusBadRequest || !strings.Contains(body, "problem policy") {
		t.Fatalf("conflict status = %d, body=%s", status, body)
	}
}

func TestCompileRejectsRustCrateModeForNonRustLanguage(t *testing.T) {
	cfg := configForTest(t)
	ts := rustPolicyServer(t, cfg, func(*model.CompileRequest) model.CompileResponse {
		t.Fatalf("runner should not receive non-Rust mode")
		return model.CompileResponse{}
	})
	defer ts.Close()

	status, body := postRustCratePolicyRequest(t, ts.URL, rustCompilePayload(t, "C11", "stdlib", ""))
	if status != http.StatusBadRequest || !strings.Contains(body, "requires a Rust compile language") {
		t.Fatalf("status = %d, body=%s", status, body)
	}
}

func TestCompileRejectsInvalidRustCrateModeBeforeRunnerAdmission(t *testing.T) {
	cfg := configForTest(t)
	ts := rustPolicyServer(t, cfg, func(*model.CompileRequest) model.CompileResponse {
		t.Fatalf("runner should not receive an invalid Rust crate mode")
		return model.CompileResponse{}
	})
	defer ts.Close()

	status, body := postRustCratePolicyRequest(t, ts.URL, rustCompilePayload(t, "RUST2021", "vendor", ""))
	if status != http.StatusBadRequest || !strings.Contains(body, "invalid_rust_crate_mode") {
		t.Fatalf("status = %d, body=%s", status, body)
	}
}
