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
	"aonohako/internal/gomodulepolicy"
	"aonohako/internal/model"
)

func goCompilePayload(t *testing.T, lang, mode, problemID string) []byte {
	t.Helper()
	payload := map[string]any{
		"lang": lang,
		"sources": []map[string]any{{
			"name":     "main.go",
			"data_b64": base64.StdEncoding.EncodeToString([]byte("package main\nfunc main() {}\n")),
		}},
	}
	if mode != "" {
		payload["go_module_mode"] = mode
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

func postGoModulePolicyRequest(t *testing.T, serverURL string, body []byte) (int, string) {
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

func goPolicyServer(t *testing.T, cfg config.Config, run func(*model.CompileRequest) model.CompileResponse) *httptest.Server {
	t.Helper()
	s := NewWithServices(cfg, compileRunnerStub{run: func(_ context.Context, req *model.CompileRequest) model.CompileResponse {
		return run(req)
	}}, execute.New())
	return httptest.NewServer(s.Handler())
}

func TestCompileDefaultsGoModuleModeToStdlib(t *testing.T) {
	cfg := configForTest(t)
	var seen gomodulepolicy.Mode
	ts := goPolicyServer(t, cfg, func(req *model.CompileRequest) model.CompileResponse {
		seen = req.GoModuleMode
		return model.CompileResponse{Status: model.CompileStatusOK}
	})
	defer ts.Close()

	status, body := postGoModulePolicyRequest(t, ts.URL, goCompilePayload(t, "GO", "", ""))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	if seen != gomodulepolicy.ModeStdlib {
		t.Fatalf("runner mode = %q, want stdlib", seen)
	}
}

func TestCompileEnforcesInstalledGoModulePolicy(t *testing.T) {
	t.Run("direct request denied", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultGoModuleMode = gomodulepolicy.ModeStdlib
		cfg.AllowRequestGoInstalledModules = false
		ts := goPolicyServer(t, cfg, func(*model.CompileRequest) model.CompileResponse {
			t.Fatalf("runner should not receive denied installed mode")
			return model.CompileResponse{}
		})
		defer ts.Close()

		status, body := postGoModulePolicyRequest(t, ts.URL, goCompilePayload(t, "GO", "installed", ""))
		if status != http.StatusBadRequest || !strings.Contains(body, "server policy") {
			t.Fatalf("status = %d, body=%s", status, body)
		}
	})

	t.Run("direct request allowed", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultGoModuleMode = gomodulepolicy.ModeStdlib
		cfg.AllowRequestGoInstalledModules = true
		var seen gomodulepolicy.Mode
		ts := goPolicyServer(t, cfg, func(req *model.CompileRequest) model.CompileResponse {
			seen = req.GoModuleMode
			return model.CompileResponse{Status: model.CompileStatusOK}
		})
		defer ts.Close()

		status, body := postGoModulePolicyRequest(t, ts.URL, goCompilePayload(t, "GO", "installed", ""))
		if status != http.StatusOK {
			t.Fatalf("status = %d, body=%s", status, body)
		}
		if seen != gomodulepolicy.ModeInstalled {
			t.Fatalf("runner mode = %q, want installed", seen)
		}
	})

	t.Run("installed server default needs no request elevation", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultGoModuleMode = gomodulepolicy.ModeInstalled
		cfg.AllowRequestGoInstalledModules = false
		var seen gomodulepolicy.Mode
		ts := goPolicyServer(t, cfg, func(req *model.CompileRequest) model.CompileResponse {
			seen = req.GoModuleMode
			return model.CompileResponse{Status: model.CompileStatusOK}
		})
		defer ts.Close()

		status, body := postGoModulePolicyRequest(t, ts.URL, goCompilePayload(t, "GO", "installed", ""))
		if status != http.StatusOK {
			t.Fatalf("status = %d, body=%s", status, body)
		}
		if seen != gomodulepolicy.ModeInstalled {
			t.Fatalf("runner mode = %q, want installed", seen)
		}
	})
}

func TestCompileAppliesProblemGoModuleMode(t *testing.T) {
	cfg := configForTest(t)
	cfg.DefaultGoModuleMode = gomodulepolicy.ModeStdlib
	cfg.AllowRequestGoInstalledModules = false
	cfg.ProblemGoModuleModes = map[string]gomodulepolicy.Mode{"contest-1/a": gomodulepolicy.ModeInstalled}
	var seen gomodulepolicy.Mode
	ts := goPolicyServer(t, cfg, func(req *model.CompileRequest) model.CompileResponse {
		seen = req.GoModuleMode
		return model.CompileResponse{Status: model.CompileStatusOK}
	})
	defer ts.Close()

	status, body := postGoModulePolicyRequest(t, ts.URL, goCompilePayload(t, "GO", "", "contest-1/a"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	if seen != gomodulepolicy.ModeInstalled {
		t.Fatalf("runner mode = %q, want installed", seen)
	}

	status, body = postGoModulePolicyRequest(t, ts.URL, goCompilePayload(t, "GO", "stdlib", "contest-1/a"))
	if status != http.StatusBadRequest || !strings.Contains(body, "problem policy") {
		t.Fatalf("conflict status = %d, body=%s", status, body)
	}
}

func TestCompileRejectsGoModuleModeForNonGoLanguage(t *testing.T) {
	cfg := configForTest(t)
	ts := goPolicyServer(t, cfg, func(*model.CompileRequest) model.CompileResponse {
		t.Fatalf("runner should not receive non-Go mode")
		return model.CompileResponse{}
	})
	defer ts.Close()

	status, body := postGoModulePolicyRequest(t, ts.URL, goCompilePayload(t, "C11", "stdlib", ""))
	if status != http.StatusBadRequest || !strings.Contains(body, "requires a Go compile request") {
		t.Fatalf("status = %d, body=%s", status, body)
	}
}

func TestCompileRejectsInvalidGoModuleModeBeforeRunnerAdmission(t *testing.T) {
	cfg := configForTest(t)
	ts := goPolicyServer(t, cfg, func(*model.CompileRequest) model.CompileResponse {
		t.Fatalf("runner should not receive an invalid Go module mode")
		return model.CompileResponse{}
	})
	defer ts.Close()

	status, body := postGoModulePolicyRequest(t, ts.URL, goCompilePayload(t, "GO", "vendor", ""))
	if status != http.StatusBadRequest || !strings.Contains(body, "invalid_go_module_mode") {
		t.Fatalf("status = %d, body=%s", status, body)
	}
}
