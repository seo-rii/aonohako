package compile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

func TestRemoteRunnerForwardsCompileRequest(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compile" {
			t.Fatalf("unexpected remote path: %s", r.URL.Path)
		}
		var req model.CompileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.EntryPoint != "src/Main.py" {
			t.Fatalf("unexpected entry_point: %+v", req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"OK\",\"stdout\":\"from-remote\\n\",\"artifacts\":[{\"name\":\"Main.pyc\",\"data_b64\":\"Ynl0ZWNvZGU=\"}]}\n\n"))
	}))
	defer remote.Close()

	runner, err := Build(config.Config{
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetDev,
				ExecutionTransport: platform.ExecutionTransportRemote,
				SandboxBackend:     platform.SandboxBackendNone,
			},
			Remote: config.RemoteExecutorConfig{
				URL: remote.URL,
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang:       "PYTHON3",
		EntryPoint: "src/Main.py",
		Sources: []model.Source{{
			Name:    "src/Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.Stdout != "from-remote\n" {
		t.Fatalf("stdout mismatch: %+v", resp)
	}
	if len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.pyc" {
		t.Fatalf("unexpected artifacts: %+v", resp.Artifacts)
	}
}

func TestRemoteRunnerCompileIncludesRemoteErrorMessage(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte("data: {\"message\":\"remote compile failed\"}\n\n"))
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Compile Error\",\"stderr\":\"boom\\n\"}\n\n"))
	}))
	defer remote.Close()

	runner, err := Build(config.Config{
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetDev,
				ExecutionTransport: platform.ExecutionTransportRemote,
				SandboxBackend:     platform.SandboxBackendNone,
			},
			Remote: config.RemoteExecutorConfig{
				URL: remote.URL,
			},
		},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}

	resp := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "PYTHON3",
		Sources: []model.Source{{
			Name:    "Main.py",
			DataB64: "cHJpbnQoJ29rJykK",
		}},
	})
	if resp.Status != model.CompileStatusCompileError {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if !strings.Contains(resp.Reason, "remote compile failed") {
		t.Fatalf("expected remote error to survive, got %+v", resp)
	}
}
