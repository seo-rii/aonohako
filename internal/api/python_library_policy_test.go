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

	"aonohako/internal/compile"
	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
)

func pythonExecutePayload(t *testing.T, lang, mode, problemID string) []byte {
	t.Helper()
	payload := map[string]any{
		"lang":     lang,
		"binaries": []map[string]any{{"name": "Main.py", "data_b64": base64.StdEncoding.EncodeToString([]byte("print('ok')\n"))}},
		"limits":   map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	if mode != "" {
		payload["python_library_mode"] = mode
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

func postPythonPolicyRequest(t *testing.T, serverURL string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL+"/execute", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("execute request: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, string(raw)
}

func TestExecuteDefaultsPythonLibraryModeToStdlib(t *testing.T) {
	cfg := configForTest(t)
	var seen pythonpolicy.LibraryMode
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(_ context.Context, req *model.RunRequest, _ execute.Hooks) model.RunResponse {
		seen = req.PythonLibraryMode
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	status, body := postPythonPolicyRequest(t, ts.URL, pythonExecutePayload(t, "python", "", ""))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	if seen != pythonpolicy.LibraryModeStdlib {
		t.Fatalf("runner mode = %q, want stdlib", seen)
	}
}

func TestExecuteAllowsInstalledPyPyLibraryMode(t *testing.T) {
	cfg := configForTest(t)
	cfg.DefaultPythonLibraryMode = pythonpolicy.LibraryModeStdlib
	cfg.AllowRequestPythonInstalledLibraries = true
	var seen pythonpolicy.LibraryMode
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(_ context.Context, req *model.RunRequest, _ execute.Hooks) model.RunResponse {
		seen = req.PythonLibraryMode
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	status, body := postPythonPolicyRequest(t, ts.URL, pythonExecutePayload(t, "PYPY3", "installed", ""))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	if seen != pythonpolicy.LibraryModeInstalled {
		t.Fatalf("runner mode = %q, want installed", seen)
	}
}

func TestExecuteEnforcesInstalledPythonLibraryPolicy(t *testing.T) {
	t.Run("direct requests denied", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultPythonLibraryMode = pythonpolicy.LibraryModeStdlib
		cfg.AllowRequestPythonInstalledLibraries = false
		s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
			t.Fatalf("runner should not receive denied installed mode")
			return model.RunResponse{}
		}})
		ts := httptest.NewServer(s.Handler())
		defer ts.Close()

		for _, lang := range []string{"python", "PYPY3"} {
			status, body := postPythonPolicyRequest(t, ts.URL, pythonExecutePayload(t, lang, "installed", ""))
			if status != http.StatusBadRequest || !strings.Contains(body, "server policy") {
				t.Fatalf("lang %s: status = %d, body=%s", lang, status, body)
			}
			active, pending := s.queue.Snapshot()
			if active != 0 || pending != 0 {
				t.Fatalf("lang %s: denied request entered queue: active=%d pending=%d", lang, active, pending)
			}
		}
	})

	t.Run("direct request allowed", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultPythonLibraryMode = pythonpolicy.LibraryModeStdlib
		cfg.AllowRequestPythonInstalledLibraries = true
		var seen pythonpolicy.LibraryMode
		s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(_ context.Context, req *model.RunRequest, _ execute.Hooks) model.RunResponse {
			seen = req.PythonLibraryMode
			return model.RunResponse{Status: model.RunStatusAccepted}
		}})
		ts := httptest.NewServer(s.Handler())
		defer ts.Close()

		status, body := postPythonPolicyRequest(t, ts.URL, pythonExecutePayload(t, "python", "installed", ""))
		if status != http.StatusOK {
			t.Fatalf("status = %d, body=%s", status, body)
		}
		if seen != pythonpolicy.LibraryModeInstalled {
			t.Fatalf("runner mode = %q, want installed", seen)
		}
	})

	t.Run("installed server default needs no request elevation", func(t *testing.T) {
		cfg := configForTest(t)
		cfg.DefaultPythonLibraryMode = pythonpolicy.LibraryModeInstalled
		cfg.AllowRequestPythonInstalledLibraries = false
		var seen pythonpolicy.LibraryMode
		s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(_ context.Context, req *model.RunRequest, _ execute.Hooks) model.RunResponse {
			seen = req.PythonLibraryMode
			return model.RunResponse{Status: model.RunStatusAccepted}
		}})
		ts := httptest.NewServer(s.Handler())
		defer ts.Close()

		status, body := postPythonPolicyRequest(t, ts.URL, pythonExecutePayload(t, "python", "installed", ""))
		if status != http.StatusOK {
			t.Fatalf("status = %d, body=%s", status, body)
		}
		if seen != pythonpolicy.LibraryModeInstalled {
			t.Fatalf("runner mode = %q, want installed", seen)
		}
	})
}

func TestExecuteAppliesProblemPythonLibraryMode(t *testing.T) {
	cfg := configForTest(t)
	cfg.DefaultPythonLibraryMode = pythonpolicy.LibraryModeStdlib
	cfg.AllowRequestPythonInstalledLibraries = false
	cfg.ProblemPythonLibraryModes = map[string]pythonpolicy.LibraryMode{"contest-1/a": pythonpolicy.LibraryModeInstalled}
	var seen pythonpolicy.LibraryMode
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(_ context.Context, req *model.RunRequest, _ execute.Hooks) model.RunResponse {
		seen = req.PythonLibraryMode
		return model.RunResponse{Status: model.RunStatusAccepted}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	status, body := postPythonPolicyRequest(t, ts.URL, pythonExecutePayload(t, "python", "", "contest-1/a"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body=%s", status, body)
	}
	if seen != pythonpolicy.LibraryModeInstalled {
		t.Fatalf("runner mode = %q, want installed", seen)
	}

	status, body = postPythonPolicyRequest(t, ts.URL, pythonExecutePayload(t, "python", "stdlib", "contest-1/a"))
	if status != http.StatusBadRequest || !strings.Contains(body, "problem policy") {
		t.Fatalf("conflict status = %d, body=%s", status, body)
	}
}

func TestExecuteRejectsPythonModeWithoutPythonTarget(t *testing.T) {
	cfg := configForTest(t)
	s := NewWithServices(cfg, compile.New(), executeRunnerStub{run: func(context.Context, *model.RunRequest, execute.Hooks) model.RunResponse {
		t.Fatalf("runner should not receive non-Python mode")
		return model.RunResponse{}
	}})
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	payload := map[string]any{
		"lang":                "binary",
		"python_library_mode": "stdlib",
		"binaries": []map[string]any{{
			"name":     "Main",
			"data_b64": base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\nexit 0\n")),
			"mode":     "exec",
		}},
		"limits": map[string]any{"time_ms": 1000, "memory_mb": 64},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	status, responseBody := postPythonPolicyRequest(t, ts.URL, body)
	if status != http.StatusBadRequest || !strings.Contains(responseBody, "requires a Python") {
		t.Fatalf("status = %d, body=%s", status, responseBody)
	}
}
