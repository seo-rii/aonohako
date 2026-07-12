package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/remoteio"
)

type executeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f executeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

	if resp.Status != model.RunStatusAccepted || resp.CPUTimeMs != 2 || resp.Reason != "" {
		t.Fatalf("unexpected remote response: %+v", resp)
	}
	if len(logs) != 1 || logs[0] != "stdout:hello\n" {
		t.Fatalf("unexpected log forwarding: %#v", logs)
	}
	if len(images) != 1 || images[0] != "image/png:Zm9v:123" {
		t.Fatalf("unexpected image forwarding: %#v", images)
	}
}

func TestRemoteRunnerForwardsRuntimeProfile(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/execute" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req model.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.RuntimeProfile != "low-memory" {
			t.Fatalf("runtime_profile = %q, want low-memory", req.RuntimeProfile)
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
			Remote: config.RemoteExecutorConfig{
				URL: remote.URL,
			},
		},
	})

	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:           "plain",
		RuntimeProfile: "low-memory",
		Binaries:       []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:         model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRemoteRunnerPrefersPreSSEErrorMessage(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request","message":"binary path duplicated"}`))
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
		Lang:     "binary",
		Binaries: []model.Binary{{Name: "run.sh", DataB64: "ZWNobw==", Mode: "exec"}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "binary path duplicated") {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRemoteRunnerClassifiesAcceptedCPUOverrun(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Accepted\",\"time_ms\":1,\"wall_time_ms\":1,\"cpu_time_ms\":101}\n\n"))
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
		Lang:     "plain",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 100, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusTLE {
		t.Fatalf("status = %q, want TLE; resp=%+v", resp.Status, resp)
	}
	if resp.Reason != "cpu time limit exceeded" {
		t.Fatalf("reason = %q, want cpu limit reason", resp.Reason)
	}
	if resp.VerdictSource != "cpu_time_final" {
		t.Fatalf("source = %q, want cpu_time_final", resp.VerdictSource)
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

func TestRemoteRunnerRejectsDeceptiveSSEContentTypes(t *testing.T) {
	for _, contentType := range []string{`application/json; note="text/event-stream"`, "text/event-streaming"} {
		t.Run(contentType, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", contentType)
				_, _ = w.Write([]byte("event: result\ndata: {\"status\":\"Accepted\"}\n\n"))
			}))
			defer remote.Close()

			runner := newRemoteRunner(config.Config{
				Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{URL: remote.URL}},
			})
			resp := runner.Run(context.Background(), &model.RunRequest{
				Lang:     "plain",
				Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
				Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
			}, Hooks{})
			if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "unexpected content type") {
				t.Fatalf("deceptive content type response = %+v", resp)
			}
		})
	}
}

func TestRemoteRunnerRejectsOversizedSSEEvents(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: log\n"))
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write([]byte(strings.Repeat("x", remoteio.DefaultSSELineBytes+1)))
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

func TestRemoteRunnerAbsoluteDeadlineCannotBeExtendedByHeartbeats(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_, _ = fmt.Fprint(w, ": heartbeat\n\n")
				flusher.Flush()
			}
		}
	}))
	defer remote.Close()

	runner := &remoteRunner{
		client:          remote.Client(),
		executeURL:      remote.URL + "/execute",
		idleTimeout:     20 * time.Millisecond,
		absoluteTimeout: 60 * time.Millisecond,
	}
	resp := runner.Run(context.Background(), &model.RunRequest{Limits: model.Limits{TimeMs: 1, MemoryMB: 64}}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "absolute deadline") {
		t.Fatalf("response = %+v, want absolute deadline failure", resp)
	}
}

func TestRemoteRunnerAbsoluteDeadlineIncludesPipelineLimitsAndOverhead(t *testing.T) {
	var remaining time.Duration
	runner := &remoteRunner{
		client: &http.Client{Transport: executeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			deadline, ok := req.Context().Deadline()
			if !ok {
				t.Fatal("remote request context has no deadline")
			}
			remaining = time.Until(deadline)
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("event: result\ndata: {\"status\":\"Accepted\"}\n\n")),
			}, nil
		})},
		executeURL: "http://remote.example/execute",
	}
	resp := runner.Run(context.Background(), &model.RunRequest{Steps: []model.RunStep{
		{Limits: model.Limits{TimeMs: 1000}},
		{Limits: model.Limits{TimeMs: 2000}},
	}}, Hooks{})
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("response = %+v", resp)
	}
	want := 3*time.Second + remoteio.DefaultOperationOverhead
	if remaining < want-time.Second || remaining > want+time.Second {
		t.Fatalf("remote deadline remaining = %s, want about %s", remaining, want)
	}
}

func TestRemoteRunnerRejectsMissingOrUnknownResultStatus(t *testing.T) {
	for _, payload := range []string{`{}`, `{"status":"Not A Verdict"}`} {
		t.Run(payload, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: result\ndata: %s\n\n", payload)
			}))
			defer remote.Close()

			runner := &remoteRunner{client: remote.Client(), executeURL: remote.URL + "/execute"}
			resp := runner.Run(context.Background(), &model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}}, Hooks{})
			if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "invalid remote result status") {
				t.Fatalf("response = %+v, want invalid remote result status", resp)
			}
		})
	}
}

func TestRemoteRunnerRejectsOversizedLogAndImageEvents(t *testing.T) {
	for _, tc := range []struct {
		name    string
		event   string
		payload string
	}{
		{name: "log", event: "log", payload: `{"stream":"stdout","chunk":"` + strings.Repeat("x", maxResponseOutputBytes+1) + `"}`},
		{name: "image", event: "image", payload: `{"mime":"image/png","b64":"` + strings.Repeat("A", 4*((maxImageEventBytes/3)+1)) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", tc.event, tc.payload)
			}))
			defer remote.Close()

			called := false
			runner := &remoteRunner{client: remote.Client(), executeURL: remote.URL + "/execute"}
			resp := runner.Run(context.Background(), &model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}}, Hooks{
				OnLog:   func(string, string) { called = true },
				OnImage: func(string, string, int64) { called = true },
			})
			if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "too large") {
				t.Fatalf("response = %+v, want oversized remote event rejection", resp)
			}
			if called {
				t.Fatal("oversized remote event reached a hook")
			}
		})
	}
}

func TestRemoteRunnerRejectsCumulativeLogOverflow(t *testing.T) {
	chunk := strings.Repeat("x", maxResponseOutputBytes/2+1)
	raw, err := json.Marshal(map[string]string{"stream": "stdout", "chunk": chunk})
	if err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 2 {
			_, _ = fmt.Fprintf(w, "event: log\ndata: %s\n\n", raw)
		}
		_, _ = fmt.Fprint(w, "event: result\ndata: {\"status\":\"Accepted\"}\n\n")
	}))
	defer remote.Close()

	runner := &remoteRunner{client: remote.Client(), executeURL: remote.URL + "/execute"}
	resp := runner.Run(context.Background(), &model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "log stream too large") {
		t.Fatalf("response = %+v, want cumulative log rejection", resp)
	}
}

func TestRemoteRunnerCapsFinalOutputLikeLocalExecution(t *testing.T) {
	raw, err := json.Marshal(model.RunResponse{
		Status: model.RunStatusRE,
		Stdout: strings.Repeat("x", maxResponseOutputBytes+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: result\ndata: %s\n\n", raw)
	}))
	defer remote.Close()

	runner := &remoteRunner{client: remote.Client(), executeURL: remote.URL + "/execute"}
	resp := runner.Run(context.Background(), &model.RunRequest{Limits: model.Limits{TimeMs: 1000, MemoryMB: 64}}, Hooks{})
	if resp.Status != model.RunStatusRE || len(resp.Stdout) != maxResponseOutputBytes || !resp.StdoutTruncated {
		t.Fatalf("response output was not capped: status=%q len=%d truncated=%v", resp.Status, len(resp.Stdout), resp.StdoutTruncated)
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

func TestRemoteRunnerStrictProtocolRejectsMissingVersion(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Accepted\",\"time_ms\":1,\"wall_time_ms\":1,\"cpu_time_ms\":1}\n\n"))
	}))
	defer remote.Close()

	runner := &remoteRunner{
		client:      remote.Client(),
		executeURL:  remote.URL + "/execute",
		strictProto: true,
	}

	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "text",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "missing remote protocol version header") {
		t.Fatalf("expected missing protocol header failure, got %+v", resp)
	}
}

func TestRemoteRunnerRejectsMalformedLogEvents(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: log\n"))
		_, _ = w.Write([]byte("data: not-json\n\n"))
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
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "remote log decode failed") {
		t.Fatalf("expected malformed log failure, got %+v", resp)
	}
}

func TestRemoteRunnerMetadataAuthUsesProxyDisabledClient(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	t.Setenv("http_proxy", "http://proxy.invalid:3128")

	metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Metadata-Flavor"); got != "Google" {
			t.Errorf("Metadata-Flavor = %q, want Google", got)
		}
		if got := r.URL.Query().Get("audience"); got != "https://runner.internal" {
			t.Errorf("audience = %q, want runner URL", got)
		}
		if got := r.URL.Query().Get("format"); got != "full" {
			t.Errorf("format = %q, want full", got)
		}
		_, _ = w.Write([]byte("metadata-token\n"))
	}))
	defer metadata.Close()

	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{
			Remote: config.RemoteExecutorConfig{
				URL:      "https://runner.internal",
				Auth:     config.RemoteAuthCloudRunIDToken,
				Audience: "https://runner.internal",
			},
		},
	}).(*remoteRunner)
	if runner.metadataClient == runner.client {
		t.Fatalf("metadata and runner requests must use separate clients")
	}
	transport, ok := runner.metadataClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("metadata transport type = %T, want *http.Transport", runner.metadataClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatalf("metadata transport must not consult HTTP_PROXY")
	}
	runner.client = nil
	runner.metadataURL = metadata.URL

	header, err := runner.authorizationHeader(context.Background())
	if err != nil {
		t.Fatalf("authorizationHeader returned error: %v", err)
	}
	if header != "Bearer metadata-token" {
		t.Fatalf("authorization header = %q, want metadata token", header)
	}
}

func TestRemoteRunnerExecuteRejectsCredentialRedirect(t *testing.T) {
	var targetRequests atomic.Int64
	var targetCredentialRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		if r.Header.Get("Authorization") != "" {
			targetCredentialRequests.Add(1)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: result\n"))
		_, _ = w.Write([]byte("data: {\"status\":\"Accepted\"}\n\n"))
	}))
	defer target.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer execute-secret" {
			t.Errorf("initial authorization header = %q, want bearer token", got)
		}
		http.Redirect(w, r, target.URL+"/execute", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{
			URL:         redirect.URL,
			Auth:        config.RemoteAuthBearer,
			BearerToken: "execute-secret",
		}},
	})
	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "plain",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})

	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "redirects are not allowed") {
		t.Fatalf("redirect response = %+v, want rejected remote request", resp)
	}
	if got := targetRequests.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, want 0", got)
	}
	if got := targetCredentialRequests.Load(); got != 0 {
		t.Fatalf("redirect target credential requests = %d, want 0", got)
	}
}

func TestRemoteRunnerExecuteMetadataAuthRejectsIncompleteTokens(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
		want    string
	}{
		{
			name: "partial token timeout",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("partial-token"))
				flusher, ok := w.(http.Flusher)
				if !ok {
					t.Errorf("response writer does not support flushing")
					return
				}
				flusher.Flush()
				<-r.Context().Done()
			},
			timeout: 100 * time.Millisecond,
			want:    "deadline exceeded",
		},
		{
			name: "oversized token",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("x", remoteio.MaxMetadataIdentityTokenBytes+1)))
			},
			want: "response body too large",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metadata := httptest.NewServer(tc.handler)
			defer metadata.Close()

			runner := newRemoteRunner(config.Config{
				Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{
					URL:      "https://runner.internal",
					Auth:     config.RemoteAuthCloudRunIDToken,
					Audience: "https://runner.internal",
				}},
			}).(*remoteRunner)
			runner.metadataURL = metadata.URL
			if tc.timeout > 0 {
				runner.metadataClient.Timeout = tc.timeout
			}

			header, err := runner.authorizationHeader(context.Background())
			if header != "" || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("authorizationHeader = (%q, %v), want empty header and %q", header, err, tc.want)
			}
		})
	}
}

func TestRemoteRunnerExecuteBoundsErrorResponseBodyRead(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("response writer does not support flushing")
			return
		}
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer remote.Close()

	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{URL: remote.URL}},
	}).(*remoteRunner)
	runner.errorBodyTimeout = 100 * time.Millisecond
	started := time.Now()
	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "plain",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})

	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "response body read timed out") {
		t.Fatalf("stalled error response = %+v, want bounded body-read failure", resp)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled error response returned after %v, want under 1s", elapsed)
	}
}

func TestRemoteRunnerExecuteBoundsStalledRequestUpload(t *testing.T) {
	runner := newRemoteRunner(config.Config{
		Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{URL: "https://runner.internal"}},
	}).(*remoteRunner)
	runner.uploadTimeout = 20 * time.Millisecond
	runner.client = &http.Client{Transport: executeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})}

	started := time.Now()
	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang:     "plain",
		Binaries: []model.Binary{{Name: "main.txt", DataB64: "SGk="}},
		Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "request upload timed out") {
		t.Fatalf("stalled upload response = %+v, want bounded upload failure", resp)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled upload returned after %v, want under 1s", elapsed)
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
		{raw: "https://runner.internal/execute/", want: "https://runner.internal/execute"},
	}

	for _, tc := range tests {
		if got := normalizeRemoteExecuteURL(tc.raw); got != tc.want {
			t.Fatalf("normalizeRemoteExecuteURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
