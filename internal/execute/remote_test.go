package execute

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/remoteio"
)

func TestRemoteRunnerForwardsSSELogsImagesAndResult(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: progress\n"))
		_, _ = w.Write([]byte("data: {\"stage\":\"accepted\"}\n\n"))
		_, _ = w.Write([]byte("event: log\n"))
		_, _ = w.Write([]byte("data: {\"stream\":\"stdout\",\"chunk\":\"hello\\n\"}\n\n"))
		_, _ = w.Write([]byte("event: image\n"))
		_, _ = w.Write([]byte("data: {\"mime\":\"image/png\",\"b64\":\"Zm9v\",\"ts\":123}\n\n"))
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Accepted\",\"time_ms\":4,\"wall_time_ms\":4,\"cpu_time_ms\":2,\"stdout\":\"hello\\n\"}\n\n"))
	}))
	defer remote.Close()

	runner := newRemoteRunner(config.Config{
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

	var logs []string
	var images []string
	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "python",
		Binaries: []model.Binary{{Name: "main.py", DataB64: "cHJpbnQoMSk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{
		OnLog: func(stream, msg string) {
			logs = append(logs, stream+":"+msg)
		},
		OnImage: func(mime, b64 string, ts int64) {
			images = append(images, fmt.Sprintf("%s:%s:%d", mime, b64, ts))
		},
	})

	if resp.Status != model.RunStatusAccepted || resp.CPUTimeMs != 2 {
		t.Fatalf("unexpected remote response: %+v", resp)
	}
	if len(logs) != 1 || logs[0] != "stdout:hello\n" {
		t.Fatalf("unexpected log forwarding: %#v", logs)
	}
	if len(images) != 1 || images[0] != "image/png:Zm9v:123" {
		t.Fatalf("unexpected image forwarding: %#v", images)
	}
}

func TestRemoteRunnerSendsBearerToken(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Accepted\",\"time_ms\":1,\"wall_time_ms\":1,\"cpu_time_ms\":1}\n\n"))
	}))
	defer remote.Close()

	runner := &remoteRunner{
		client:      &http.Client{},
		executeURL:  remote.URL + "/execute",
		auth:        config.RemoteAuthBearer,
		bearerToken: "test-token",
	}

	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "plain",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRemoteRunnerRejectsNonSSESuccessResponses(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"Accepted"}`))
	}))
	defer remote.Close()

	runner := newRemoteRunner(config.Config{
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

	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "text",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail {
		t.Fatalf("expected init failure for non-SSE upstream response, got %+v", resp)
	}
	if got := resp.Reason; got == "" || got == "remote execute stream ended without result" {
		t.Fatalf("expected explicit non-SSE reason, got %+v", resp)
	}
}

func TestRemoteRunnerRejectsOversizedSSEEvents(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: log\n"))
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write([]byte(strings.Repeat("x", 300<<10)))
		_, _ = w.Write([]byte("\n\n"))
	}))
	defer remote.Close()

	runner := newRemoteRunner(config.Config{
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

	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "text",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "sse line too large") {
		t.Fatalf("expected bounded SSE failure, got %+v", resp)
	}
}

func TestRemoteRunnerTimesOutIdleSSEStream(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("test server does not support flushing")
		}
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer remote.Close()

	runner := &remoteRunner{
		client:      remote.Client(),
		executeURL:  remote.URL + "/execute",
		idleTimeout: 10 * time.Millisecond,
	}

	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "text",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "idle timeout") {
		t.Fatalf("expected idle timeout failure, got %+v", resp)
	}
}

func TestRemoteRunnerRejectsProtocolVersionMismatch(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set(remoteio.ProtocolVersionHeader, "1900-01-01")
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
			Remote: config.RemoteExecutorConfig{
				URL: remote.URL,
			},
		},
	})

	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "text",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "protocol mismatch") {
		t.Fatalf("expected protocol mismatch failure, got %+v", resp)
	}
}

func TestNormalizeRemoteExecuteURLAppendsExecutePath(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{raw: "https://runner.internal", want: "https://runner.internal/execute"},
		{raw: "https://runner.internal/base", want: "https://runner.internal/base/execute"},
		{raw: "https://runner.internal/execute", want: "https://runner.internal/execute"},
	}

	for _, tc := range tests {
		if got := normalizeRemoteExecuteURL(tc.raw); got != tc.want {
			t.Fatalf("normalizeRemoteExecuteURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
