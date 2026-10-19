package api

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/compile"
	"aonohako/internal/compilewire"
	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

type compileWireObserver struct {
	http.ResponseWriter
	bytes, logs, results atomic.Int64
}

func (w *compileWireObserver) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *compileWireObserver) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes.Add(int64(n))
	switch string(p) {
	case "event: log\n":
		w.logs.Add(1)
	case "event: result\n":
		w.results.Add(1)
	}
	return n, err
}

func TestCompileMaximumArtifactsThroughAPIAndRemoteClient(t *testing.T) {
	// TEXT compilation materializes real files and collects them with the same
	// descriptor checks and decoded byte budgets as compiled artifacts. Empty
	// files exercise the maximum artifact count and escaped long path metadata.
	parent := strings.Repeat(strings.Repeat("<>&", 70)+"/", 18)
	data := base64.StdEncoding.EncodeToString(make([]byte, 16<<20))
	sources := make([]model.Source, compile.MaxSourceFiles)
	for i := range sources {
		sources[i].Name = fmt.Sprintf("%s%03d-<>&.bin", parent, i)
		if i < 3 {
			sources[i].DataB64 = data
		}
	}
	local := compile.New().Run(context.Background(), &model.CompileRequest{Lang: "TEXT", Sources: sources})
	if local.Status != model.CompileStatusOK || len(local.Artifacts) != len(sources) {
		t.Fatalf("artifact collection: status=%s reason=%s artifacts=%d", local.Status, local.Reason, len(local.Artifacts))
	}
	// These bytes all expand sixfold in encoding/json.
	local.Stdout = strings.Repeat("\x00", compile.OutputCaptureBytes)
	local.Stderr = strings.Repeat("\x01", compile.OutputCaptureBytes)
	local.Reason = strings.Repeat("<", compile.OutputCaptureBytes)
	local.ReasonCode = "compile_diagnostic"
	server := NewWithServices(config.Config{
		MaxActiveRuns: 1, MaxPendingQueue: 1, HeartbeatInterval: time.Second,
	}, compileRunnerStub{run: func(context.Context, *model.CompileRequest) model.CompileResponse {
		return local
	}}, nil)
	var observer compileWireObserver
	handlerDone := make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		observer.ResponseWriter = w
		server.Handler().ServeHTTP(&observer, r)
	}))
	defer remote.Close()
	runner, err := compile.Build(config.Config{Execution: config.ExecutionConfig{
		Platform: platform.RuntimeOptions{
			DeploymentTarget:   platform.DeploymentTargetDev,
			ExecutionTransport: platform.ExecutionTransportRemote,
			SandboxBackend:     platform.SandboxBackendNone,
		},
		Remote: config.RemoteExecutorConfig{URL: remote.URL, StrictProtocol: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// A small request selects the fixture so the assertion isolates response
	// transport from the independent 64 MiB JSON request envelope.
	got := runner.Run(context.Background(), &model.CompileRequest{
		Lang: "TEXT", Sources: []model.Source{{Name: "Main.txt", DataB64: "eA=="}},
	})
	if got.Status != local.Status || len(got.Artifacts) != len(local.Artifacts) {
		t.Fatalf("remote result: status=%s reason=%s artifacts=%d", got.Status, got.Reason, len(got.Artifacts))
	}
	for i := range local.Artifacts {
		if got.Artifacts[i] != local.Artifacts[i] {
			t.Fatalf("artifact %d changed during SSE transport", i)
		}
	}
	if got.Stdout != local.Stdout || got.Stderr != local.Stderr || got.Reason != local.Reason || got.ReasonCode != local.ReasonCode {
		t.Fatal("compile diagnostics changed during SSE transport")
	}
	if got.StdoutTruncated || got.StderrTruncated {
		t.Fatal("permitted compile output was truncated")
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("compile handler did not finish after delivering its result")
	}
	if observer.logs.Load() != 2 || observer.results.Load() != 1 || observer.bytes.Load() > compilewire.SSEStreamBytes {
		t.Fatalf("wire events: logs=%d results=%d bytes=%d", observer.logs.Load(), observer.results.Load(), observer.bytes.Load())
	}
}
