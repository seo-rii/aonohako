package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/runvalidation"
)

func TestPipelineWorkerAttestation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   config.InboundAuthMode
		remote bool
		token  string
		status int
	}{
		{"authorized embedded helper", config.InboundAuthBearer, false, "worker-token", http.StatusOK},
		{"unauthorized embedded helper", config.InboundAuthBearer, false, "wrong", http.StatusUnauthorized},
		{"anonymous worker cannot attest", config.InboundAuthNone, false, "", http.StatusNotFound},
		{"unverified development platform", config.InboundAuthPlatform, false, "", http.StatusNotFound},
		{"remote controller cannot attest", config.InboundAuthBearer, true, "worker-token", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := configForTest(t)
			if tc.remote {
				cfg.Execution.Platform.ExecutionTransport = platform.ExecutionTransportRemote
				cfg.Execution.Platform.SandboxBackend = platform.SandboxBackendNone
			}
			cfg.InboundAuth = config.InboundAuthConfig{Mode: tc.mode, BearerToken: "worker-token"}
			s := NewWithServices(cfg, nil, nil)
			req := httptest.NewRequest(http.MethodGet, model.PipelineV1WorkerPath, nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if tc.status == http.StatusOK {
				var proof model.PipelineV1WorkerCapability
				if err := json.Unmarshal(w.Body.Bytes(), &proof); err != nil || proof.Protocol != model.PipelineV1WorkerProtocol ||
					!runvalidation.SupportsPipelineV1Contract(proof.Pipeline) {
					t.Fatalf("invalid worker contract: %s", w.Body.String())
				}
			}
		})
	}
}

type changingPipelineWorker struct {
	executeRunnerStub
	probes atomic.Int32
	ready  atomic.Bool
}

func (w *changingPipelineWorker) SupportsPipelineV1(context.Context) bool {
	w.probes.Add(1)
	return w.ready.Load()
}

func TestRemoteCapabilityCacheTracksWorkerChanges(t *testing.T) {
	cfg := configForTest(t)
	cfg.Execution.Platform.ExecutionTransport = platform.ExecutionTransportRemote
	cfg.Execution.Platform.SandboxBackend = platform.SandboxBackendNone
	worker := new(changingPipelineWorker)
	s := NewWithServices(cfg, nil, worker)
	synctest.Test(t, func(t *testing.T) {
		for _, want := range []bool{false, true, false} {
			worker.ready.Store(want)
			before := worker.probes.Load()
			var group sync.WaitGroup
			for range 10 {
				group.Go(func() {
					if got := s.supportsPipelineV1(t.Context()); got != want {
						t.Errorf("pipeline support = %v, want %v", got, want)
					}
				})
			}
			group.Wait()
			if calls := worker.probes.Load() - before; calls != 1 {
				t.Fatalf("discovery made %d worker probes", calls)
			}
			time.Sleep(6 * time.Second)
		}
	})
}
