package execute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/pythonpolicy"
)

func TestRemoteRunnerForwardsPythonLibraryMode(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req model.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.PythonLibraryMode != pythonpolicy.LibraryModeInstalled {
			t.Fatalf("python_library_mode = %q, want installed", req.PythonLibraryMode)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Accepted\",\"time_ms\":1,\"wall_time_ms\":1,\"cpu_time_ms\":1}\n\n"))
	}))
	defer remote.Close()

	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{
				DeploymentTarget:   platform.DeploymentTargetDev,
				ExecutionTransport: platform.ExecutionTransportRemote,
				SandboxBackend:     platform.SandboxBackendNone,
			},
			Remote: config.RemoteExecutorConfig{URL: remote.URL},
		},
	})
	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:              "python",
		PythonLibraryMode: pythonpolicy.LibraryModeInstalled,
		Binaries:          []model.Binary{{Name: "Main.py", DataB64: "cHJpbnQoMSk="}},
		Limits:            model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("unexpected response: %+v", resp)
	}
}
