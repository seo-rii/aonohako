package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/cpupolicy"
	"aonohako/internal/model"
)

func TestPipelineProgramRequestOwnsCPUFeaturePolicy(t *testing.T) {
	req := &model.RunRequest{DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX, cpupolicy.SIMD}}
	program := model.RunProgram{ID: "participant", Lang: "binary"}
	child := pipelineProgramRequest(req, program, model.Limits{TimeMs: 1000, MemoryMB: 64})
	if !slices.Equal(child.DisallowedCPUFeatures, req.DisallowedCPUFeatures) {
		t.Fatalf("child policy = %v, want %v", child.DisallowedCPUFeatures, req.DisallowedCPUFeatures)
	}
	child.DisallowedCPUFeatures[0] = cpupolicy.SIMD
	if req.DisallowedCPUFeatures[0] != cpupolicy.AVX {
		t.Fatal("child policy mutation changed the parent request")
	}
}

func TestInteractorRequestDoesNotInheritContestantCPUFeaturePolicy(t *testing.T) {
	req := &model.RunRequest{
		Lang: "binary", DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.SIMD},
		Limits:     model.Limits{TimeMs: 1000, MemoryMB: 64},
		Interactor: &model.InteractorSpec{Lang: "python", Binaries: []model.Binary{{Name: "interactor.py", DataB64: "eA=="}}},
	}
	interactor := interactorRunRequest(req)
	if len(interactor.DisallowedCPUFeatures) != 0 {
		t.Fatalf("trusted interactor inherited contestant policy: %v", interactor.DisallowedCPUFeatures)
	}
	if !slices.Equal(req.DisallowedCPUFeatures, []cpupolicy.Feature{cpupolicy.SIMD}) {
		t.Fatal("constructing the interactor removed the contestant policy")
	}
}

func TestServiceRejectsUnsupportedCPUFeaturePolicyBeforeSandbox(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  model.RunRequest
	}{
		{name: "unknown feature", req: model.RunRequest{Lang: "binary", DisallowedCPUFeatures: []cpupolicy.Feature{"unknown"}}},
		{name: "unsupported runtime", req: model.RunRequest{Lang: "python", DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX}}},
		{name: "network", req: model.RunRequest{Lang: "binary", EnableNetwork: true, DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX}}},
		{name: "communication", req: model.RunRequest{Communication: &model.CommunicationSpec{}, DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := New().Run(context.Background(), &tc.req, Hooks{})
			if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "disallowed_cpu_features") {
				t.Fatalf("response = %+v, want CPU policy rejection before sandbox setup", resp)
			}
		})
	}
}

func TestRemoteRunnerForwardsDisallowedCPUFeatures(t *testing.T) {
	for _, features := range [][]cpupolicy.Feature{nil, {}, {cpupolicy.AVX}, {cpupolicy.SIMD, cpupolicy.AVX}} {
		t.Run(fmt.Sprint(features), func(t *testing.T) {
			var calls atomic.Int64
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					http.Error(w, "read failure", http.StatusBadRequest)
					return
				}
				var decoded model.RunRequest
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Error(err)
				}
				if err := json.Unmarshal(raw, &fields); err != nil {
					t.Error(err)
				}
				_, present := fields["disallowed_cpu_features"]
				if present != (len(features) != 0) || !slices.Equal(decoded.DisallowedCPUFeatures, features) {
					t.Errorf("forwarded policy = %v, present = %v; want %v", decoded.DisallowedCPUFeatures, present, features)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: result\ndata: {\"status\":\"Accepted\"}\n\n")
			}))
			defer remote.Close()
			runner := newRemoteRunner(config.Config{Execution: config.ExecutionConfig{Remote: config.RemoteExecutorConfig{URL: remote.URL}}})
			resp := runner.Run(context.Background(), &model.RunRequest{
				Lang: "binary", DisallowedCPUFeatures: features,
				Binaries: []model.Binary{{Name: "Main", DataB64: "eA==", Mode: "exec"}},
				Limits:   model.Limits{TimeMs: 1000, MemoryMB: 64},
			}, Hooks{})
			if resp.Status != model.RunStatusAccepted || calls.Load() != 1 {
				t.Fatalf("response = %+v, calls = %d", resp, calls.Load())
			}
		})
	}
}

func TestRemoteRunnerRejectsCPUFeaturePolicyBeforeAuthOrUpload(t *testing.T) {
	called := false
	client := &http.Client{Transport: executeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, fmt.Errorf("unexpected auth or upload request")
	})}
	runner := &remoteRunner{
		client: client, metadataClient: client, executeURL: "http://runner.example/execute",
		auth: config.RemoteAuthCloudRunIDToken, metadataURL: "http://metadata.example/token",
	}
	resp := runner.Run(context.Background(), &model.RunRequest{
		Lang: "python", DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX},
	}, Hooks{})
	if resp.Status != model.RunStatusInitFail || !strings.Contains(resp.Reason, "disallowed_cpu_features") || called {
		t.Fatalf("response = %+v, HTTP called = %v", resp, called)
	}
}
