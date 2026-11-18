package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/runvalidation"
)

type pipelineCapabilityCache struct {
	lock      chan struct{}
	supported bool
	expires   time.Time
}

func (s *Server) isEmbeddedHelper() bool {
	return s.cfg.Execution.Platform.ExecutionTransport == platform.ExecutionTransportEmbedded &&
		s.cfg.Execution.Platform.SandboxBackend == platform.SandboxBackendHelper
}

// Public discovery and /execute admission share one short-lived, bounded probe.
// The actual execution response must also identify the serving worker.
func (s *Server) supportsPipelineV1(ctx context.Context) bool {
	if s.isEmbeddedHelper() {
		return true
	}
	remote, ok := s.execute.(interface{ SupportsPipelineV1(context.Context) bool })
	if !ok || s.cfg.Execution.Platform.ExecutionTransport != platform.ExecutionTransportRemote {
		return false
	}
	c := &s.pipelineCapability
	select {
	case c.lock <- struct{}{}:
		defer func() { <-c.lock }()
	case <-ctx.Done():
		return false
	}
	if time.Now().Before(c.expires) {
		return c.supported
	}
	supported := remote.SupportsPipelineV1(ctx)
	if ctx.Err() == nil {
		c.supported = supported
		c.expires = time.Now().Add(5 * time.Second)
	}
	return supported
}

func (s *Server) canAttestPipelineWorker() bool {
	if !s.isEmbeddedHelper() {
		return false
	}
	auth := s.cfg.InboundAuth
	return auth.Mode == config.InboundAuthBearer || (auth.Mode == config.InboundAuthPlatform &&
		(auth.PlatformPrincipalHMACSecret != "" || (s.cfg.TrustedPlatformHeaders && len(s.cfg.TrustedPlatformHeaderCIDRs) > 0)))
}

// Discovery accepts only an empty GET before the normal execution authentication
// middleware, which may hash the request body for a signed platform principal.
func (s *Server) pipelineWorkerCapability(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		writeJSONErrorMessage(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeJSONError(w, http.StatusBadRequest, "unexpected_request_body")
		return
	}
	if !s.canAttestPipelineWorker() {
		writeJSONError(w, http.StatusNotFound, "unsupported_capability")
		return
	}
	s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		contract := runvalidation.PipelineV1Contract()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(model.PipelineV1WorkerCapability{Protocol: model.PipelineV1WorkerProtocol, Pipeline: &contract})
	})).ServeHTTP(w, r)
}
