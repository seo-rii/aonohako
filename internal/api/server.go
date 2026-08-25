package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aonohako/internal/compile"
	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/queue"
	"aonohako/internal/remoteio"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/runvalidation"
	"aonohako/internal/rustpolicy"
	"aonohako/internal/sse"
	"aonohako/internal/util"
)

const (
	MaxJSONBodyBytes = 64 << 20

	maxRunTextFieldBytes              = runvalidation.MaxTextFieldBytes
	maxRunTimeMs                      = runvalidation.MaxTimeMs
	maxRunMemoryMB                    = runvalidation.MaxMemoryMB
	maxRunOutputBytes                 = runvalidation.MaxOutputBytes
	maxRunCaptureBytes                = runvalidation.MaxCaptureBytes
	maxRunWorkspaceBytes              = runvalidation.MaxWorkspaceBytes
	maxCompileSourceFiles             = compile.MaxSourceFiles
	maxCompileDecodedSourceBytes      = compile.MaxDecodedSourceBytes
	maxCompileDecodedSourceTotalBytes = compile.MaxDecodedSourceTotalBytes
	maxJSONBodyBytes                  = MaxJSONBodyBytes
	defaultPlatformBodyHashSlots      = 4
	defaultPayloadURLFetchSlots       = 4
	platformPrincipalHeader           = "X-Aonohako-Principal"
	platformPrincipalSignatureHeader  = "X-Aonohako-Principal-Signature"
	platformPrincipalTimestampHeader  = "X-Aonohako-Principal-Timestamp"
	platformPrincipalNonceHeader      = "X-Aonohako-Principal-Nonce"
	platformPrincipalSignatureSkew    = 5 * time.Minute
)

type principalContextKey struct{}
type uploadAdmissionContextKey struct{}

type principalRateWindow struct {
	start time.Time
	count int
}

type uploadAdmission struct {
	once    sync.Once
	release func()
}

func (a *uploadAdmission) Release() {
	if a == nil {
		return
	}
	a.once.Do(a.release)
}

type Server struct {
	cfg     config.Config
	compile interface {
		Run(context.Context, *model.CompileRequest) model.CompileResponse
	}
	execute execute.Runner
	queue   *queue.Manager
	seq     atomic.Uint64
	streams atomic.Int64
	uploads atomic.Int64

	principalMu      sync.Mutex
	principalStreams map[string]int
	principalUploads map[string]int
	principalRates   map[string]principalRateWindow
	rateLastCleanup  time.Time

	platformBodyHashSlots chan struct{}
	platformReplayCache   *platformReplayCache
	payloadURLFetchSlots  chan struct{}
	payloadURLTimeout     time.Duration
	sseWriteTimeout       time.Duration
	readinessCheck        func() error
}

func New(cfg config.Config) (*Server, error) {
	compileRunner, err := compile.Build(cfg)
	if err != nil {
		return nil, err
	}
	runner, err := execute.Build(cfg)
	if err != nil {
		return nil, err
	}
	return NewWithServices(cfg, compileRunner, runner), nil
}

func NewWithServices(cfg config.Config, compileService interface {
	Run(context.Context, *model.CompileRequest) model.CompileResponse
}, executeRunner execute.Runner) *Server {
	payloadURLFetchSlots := defaultPayloadURLFetchSlots
	if cfg.MaxActiveStreams > 0 {
		payloadURLFetchSlots = min(payloadURLFetchSlots, cfg.MaxActiveStreams)
	} else if cfg.MaxActiveRuns > 0 {
		payloadURLFetchSlots = min(payloadURLFetchSlots, cfg.MaxActiveRuns)
	}
	payloadURLFetchSlots = max(1, payloadURLFetchSlots)
	return &Server{
		cfg:              cfg,
		compile:          compileService,
		execute:          executeRunner,
		queue:            queue.New(cfg.MaxActiveRuns, cfg.MaxPendingQueue),
		principalStreams: map[string]int{},
		principalUploads: map[string]int{},
		principalRates:   map[string]principalRateWindow{},
		platformBodyHashSlots: make(
			chan struct{},
			platformBodyHashConcurrency(cfg),
		),
		platformReplayCache:  newPlatformReplayCache(),
		payloadURLFetchSlots: make(chan struct{}, payloadURLFetchSlots),
		payloadURLTimeout:    payloadURLRequestTimeout,
		sseWriteTimeout:      sse.DefaultWriteTimeout,
		readinessCheck:       newRuntimeReadinessCheck(cfg),
	}
}

func platformBodyHashConcurrency(cfg config.Config) int {
	if cfg.PlatformBodyHashConcurrency > 0 {
		return cfg.PlatformBodyHashConcurrency
	}
	if cfg.MaxActiveStreams > 0 {
		return min(defaultPlatformBodyHashSlots, cfg.MaxActiveStreams)
	}
	if cfg.MaxActiveRuns > 0 {
		return min(defaultPlatformBodyHashSlots, max(1, cfg.MaxActiveRuns))
	}
	return defaultPlatformBodyHashSlots
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/livez", s.livez)
	mux.HandleFunc("/readyz", s.readyz)
	mux.HandleFunc("/healthz", s.readyz)
	mux.HandleFunc("/capabilities", s.capabilities)
	mux.Handle("/compile", s.withUploadAdmission(s.requireAuth(http.HandlerFunc(s.compileHandler))))
	mux.Handle("/execute", s.withUploadAdmission(s.requireAuth(http.HandlerFunc(s.executeHandler))))
	return mux
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErrorMessage(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	capabilities := make([]string, 0, 1)
	if platform.SupportsCommunicationV1(s.cfg.Execution.Platform, s.cfg.Execution.Cgroup.ParentDir, s.cfg.CommunicationEnabled) {
		capabilities = append(capabilities, "communication-v1")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": capabilities})
}

func (s *Server) nextID(prefix string) string {
	n := s.seq.Add(1)
	return prefix + "-" + strconv.FormatUint(n, 10)
}

func (s *Server) livez(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	if s.readinessCheck != nil {
		if err := s.readinessCheck(); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) compileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErrorMessage(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	principal := principalFromContext(r.Context())
	if !s.allowPrincipalRequest(principal, time.Now()) {
		writeJSONError(w, http.StatusTooManyRequests, "principal_rate_limited")
		return
	}

	var req model.CompileRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_json", "invalid json: "+err.Error())
		return
	}
	if err := s.applyRuntimeProfilePolicy(req.ProblemID, &req.RuntimeProfile); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_runtime_profile", err.Error())
		return
	}
	if err := s.applyRustCratePolicy(&req); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_rust_crate_mode", err.Error())
		return
	}
	if len(req.Sources) == 0 {
		writeJSONErrorMessage(w, http.StatusBadRequest, "no_sources", "no sources")
		return
	}
	if len(req.Sources) > maxCompileSourceFiles {
		writeJSONErrorMessage(w, http.StatusBadRequest, "too_many_sources", fmt.Sprintf("too many sources: max %d", maxCompileSourceFiles))
		return
	}
	totalDecodedSourceBytes := 0
	seenSourcePaths := map[string]struct{}{}
	for i, src := range req.Sources {
		clean, err := util.ValidateRelativePath(src.Name)
		if err != nil {
			writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_source_path", fmt.Sprintf("sources[%d].name: %s", i, err.Error()))
			return
		}
		if _, exists := seenSourcePaths[clean]; exists {
			writeJSONErrorMessage(w, http.StatusBadRequest, "duplicate_source_path", "duplicate source path: "+clean)
			return
		}
		seenSourcePaths[clean] = struct{}{}
		if strings.TrimSpace(src.DataURL) != "" {
			if src.DataB64 != "" {
				writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_payload_url", fmt.Sprintf("sources[%d] cannot combine data_b64 with data_url", i))
				return
			}
			if err := validatePayloadURL(src.DataURL); err != nil {
				writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_payload_url", fmt.Sprintf("sources[%d].data_url: %s", i, err.Error()))
				return
			}
		}
		data, err := base64.StdEncoding.DecodeString(src.DataB64)
		if err != nil {
			writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_base64", fmt.Sprintf("sources[%d].data_b64 invalid base64: %s", i, err.Error()))
			return
		}
		decodedLen := len(data)
		if decodedLen > maxCompileDecodedSourceBytes {
			writeJSONErrorMessage(w, http.StatusBadRequest, "source_too_large", fmt.Sprintf("source too large: max %d bytes decoded", maxCompileDecodedSourceBytes))
			return
		}
		totalDecodedSourceBytes += decodedLen
		if totalDecodedSourceBytes > maxCompileDecodedSourceTotalBytes {
			writeJSONErrorMessage(w, http.StatusBadRequest, "sources_total_size_exceeded", fmt.Sprintf("sources total size exceeded: max %d bytes decoded", maxCompileDecodedSourceTotalBytes))
			return
		}
	}
	if compileHasPayloadURLs(&req) {
		releaseFetch, ok := s.acquirePayloadURLFetch()
		if !ok {
			writeJSONError(w, http.StatusTooManyRequests, "payload_url_fetch_limit_exceeded")
			return
		}
		fetchCtx, cancelFetch := context.WithTimeout(r.Context(), s.payloadURLTimeout)
		err := resolveCompilePayloadURLs(fetchCtx, &req, totalDecodedSourceBytes)
		cancelFetch()
		releaseFetch()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				writeJSONErrorMessage(w, http.StatusGatewayTimeout, "payload_url_fetch_timeout", "payload url fetch timed out")
				return
			}
			writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_payload_url", err.Error())
			return
		}
	}

	releaseStream, ok, code := s.acquireStream(principal)
	if !ok {
		writeJSONError(w, http.StatusTooManyRequests, code)
		return
	}
	defer releaseStream()

	permit, err := s.queue.Acquire()
	if err != nil {
		if errors.Is(err, queue.ErrQueueFull) {
			writeJSONError(w, http.StatusTooManyRequests, "queue_full")
			return
		}
		writeJSONErrorMessage(w, http.StatusInternalServerError, "queue_error", "queue error")
		return
	}
	releaseUploadAdmission(r.Context())

	w.Header().Set(remoteio.ProtocolVersionHeader, remoteio.ProtocolVersion)
	stream, err := sse.New(w, s.sseWriteTimeout)
	if err != nil {
		permit.Cancel()
		writeJSONErrorMessage(w, http.StatusInternalServerError, "stream_init_failed", err.Error())
		return
	}
	streamCtx, stopStream := context.WithCancel(r.Context())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		if err := stream.Heartbeat(streamCtx, s.cfg.HeartbeatInterval); err != nil {
			stopStream()
		}
	}()
	defer func() {
		stopStream()
		<-heartbeatDone
	}()

	reqID := s.nextID("compile")
	active, pending := s.queue.Snapshot()
	if err := stream.Event("progress", map[string]any{
		"stage":          "accepted",
		"request_id":     reqID,
		"queue_position": permit.Position(),
		"active_runs":    active,
		"queue_pending":  pending,
		"ts":             time.Now().UnixMilli(),
	}); err != nil {
		permit.Cancel()
		return
	}

	if err := permit.Wait(streamCtx); err != nil {
		if r.Context().Err() == nil && streamCtx.Err() == nil {
			_ = stream.Event("error", map[string]any{"message": err.Error()})
		}
		return
	}
	defer permit.Release()

	if err := stream.Event("progress", map[string]any{"stage": "start", "request_id": reqID, "ts": time.Now().UnixMilli()}); err != nil {
		return
	}

	resp := s.compile.Run(streamCtx, &req)
	if streamCtx.Err() != nil {
		return
	}
	if req.EmitLogs == nil || *req.EmitLogs {
		if resp.Stdout != "" {
			if err := stream.Event("log", map[string]any{"stream": "stdout", "chunk": resp.Stdout}); err != nil {
				return
			}
		}
		if resp.Stderr != "" {
			if err := stream.Event("log", map[string]any{"stream": "stderr", "chunk": resp.Stderr}); err != nil {
				return
			}
		}
	}
	if resp.Status != model.CompileStatusOK {
		errorMessage := firstNonEmpty(resp.Reason, "compile failed")
		if req.EmitLogs == nil || *req.EmitLogs {
			errorMessage = firstNonEmpty(resp.Reason, resp.Stderr, resp.Stdout, "compile failed")
		}
		if err := stream.Event("error", map[string]any{"message": errorMessage}); err != nil {
			return
		}
	}
	if err := stream.Event("result", resp); err != nil {
		return
	}
}

func (s *Server) executeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONErrorMessage(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	principal := principalFromContext(r.Context())
	if !s.allowPrincipalRequest(principal, time.Now()) {
		writeJSONError(w, http.StatusTooManyRequests, "principal_rate_limited")
		return
	}

	var req model.RunRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_json", "invalid json: "+err.Error())
		return
	}
	if err := s.applyRuntimeProfilePolicy(req.ProblemID, &req.RuntimeProfile); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_runtime_profile", err.Error())
		return
	}
	if err := s.applyPythonLibraryPolicy(&req); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_python_library_mode", err.Error())
		return
	}
	if err := runvalidation.Validate(&req); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if (req.EnableNetwork || runvalidation.ProgramsEnableNetwork(&req)) && !s.cfg.AllowRequestNetwork {
		writeJSONErrorMessage(w, http.StatusBadRequest, "network_not_allowed", "enable_network is not allowed by server policy")
		return
	}
	if runHasBufferedPayloadURLs(&req) {
		releaseFetch, ok := s.acquirePayloadURLFetch()
		if !ok {
			writeJSONError(w, http.StatusTooManyRequests, "payload_url_fetch_limit_exceeded")
			return
		}
		fetchCtx, cancelFetch := context.WithTimeout(r.Context(), s.payloadURLTimeout)
		err := resolveRunPayloadURLs(fetchCtx, &req)
		cancelFetch()
		releaseFetch()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				writeJSONErrorMessage(w, http.StatusGatewayTimeout, "payload_url_fetch_timeout", "payload url fetch timed out")
				return
			}
			writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_payload_url", err.Error())
			return
		}
		if err := runvalidation.Validate(&req); err != nil {
			writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	} else if err := resolveRunPayloadURLs(r.Context(), &req); err != nil {
		// Streaming stdin URLs are not fetched here, but still receive the same
		// syntax and credential validation before stream/queue admission.
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_payload_url", err.Error())
		return
	}
	releaseStream, ok, code := s.acquireStream(principal)
	if !ok {
		writeJSONError(w, http.StatusTooManyRequests, code)
		return
	}
	defer releaseStream()

	permit, err := s.queue.Acquire()
	if err != nil {
		if errors.Is(err, queue.ErrQueueFull) {
			writeJSONError(w, http.StatusTooManyRequests, "queue_full")
			return
		}
		writeJSONErrorMessage(w, http.StatusInternalServerError, "queue_error", "queue error")
		return
	}
	releaseUploadAdmission(r.Context())

	w.Header().Set(remoteio.ProtocolVersionHeader, remoteio.ProtocolVersion)
	stream, err := sse.New(w, s.sseWriteTimeout)
	if err != nil {
		permit.Cancel()
		writeJSONErrorMessage(w, http.StatusInternalServerError, "stream_init_failed", err.Error())
		return
	}
	streamCtx, stopStream := context.WithCancel(r.Context())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		if err := stream.Heartbeat(streamCtx, s.cfg.HeartbeatInterval); err != nil {
			stopStream()
		}
	}()
	defer func() {
		stopStream()
		<-heartbeatDone
	}()

	reqID := s.nextID("execute")
	active, pending := s.queue.Snapshot()
	if err := stream.Event("progress", map[string]any{
		"stage":          "accepted",
		"request_id":     reqID,
		"queue_position": permit.Position(),
		"active_runs":    active,
		"queue_pending":  pending,
		"ts":             time.Now().UnixMilli(),
	}); err != nil {
		permit.Cancel()
		return
	}

	if err := permit.Wait(streamCtx); err != nil {
		if r.Context().Err() == nil && streamCtx.Err() == nil {
			_ = stream.Event("error", map[string]any{"message": err.Error()})
		}
		return
	}
	defer permit.Release()

	if err := stream.Event("progress", map[string]any{"stage": "start", "request_id": reqID, "ts": time.Now().UnixMilli()}); err != nil {
		return
	}

	hooks := execute.Hooks{
		OnImage: func(mime, b64 string, ts int64) {
			if err := stream.Event("image", map[string]any{"mime": mime, "b64": b64, "ts": ts}); err != nil {
				stopStream()
			}
		},
	}
	if req.EmitLogs == nil || *req.EmitLogs {
		hooks.OnLog = func(streamName, msg string) {
			if err := stream.Event("log", map[string]any{"stream": streamName, "chunk": msg}); err != nil {
				stopStream()
			}
		}
	}
	resp := s.execute.Run(streamCtx, &req, hooks)
	if streamCtx.Err() != nil {
		return
	}

	if resp.Status == model.RunStatusInitFail {
		errorMessage := firstNonEmpty(resp.Reason, "execution failed")
		if req.EmitLogs == nil || *req.EmitLogs {
			errorMessage = firstNonEmpty(resp.Reason, resp.Stderr, resp.Stdout, "execution failed")
		}
		if err := stream.Event("error", map[string]any{"message": errorMessage}); err != nil {
			return
		}
	}
	if err := stream.Event("result", resp); err != nil {
		slog.Error("execute: write result failed", "reqID", reqID, "err", err)
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func principalFromContext(ctx context.Context) string {
	if principal, ok := ctx.Value(principalContextKey{}).(string); ok && principal != "" {
		return principal
	}
	return "anonymous:unknown"
}

func releaseUploadAdmission(ctx context.Context) {
	if admission, ok := ctx.Value(uploadAdmissionContextKey{}).(*uploadAdmission); ok {
		admission.Release()
	}
}

func (s *Server) withUploadAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, ok, code := s.acquireUpload(s.preAuthPrincipal(r))
		if !ok {
			writeJSONError(w, http.StatusTooManyRequests, code)
			return
		}
		admission := &uploadAdmission{release: release}
		defer admission.Release()
		ctx := context.WithValue(r.Context(), uploadAdmissionContextKey{}, admission)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) acquireUpload(principal string) (func(), bool, string) {
	if s.cfg.MaxActiveUploads <= 0 {
		if s.cfg.MaxPrincipalUploads <= 0 {
			return func() {}, true, ""
		}
	} else {
		active := s.uploads.Add(1)
		if active > int64(s.cfg.MaxActiveUploads) {
			s.uploads.Add(-1)
			return nil, false, "upload_limit_exceeded"
		}
	}
	principalAcquired := false
	if s.cfg.MaxPrincipalUploads > 0 {
		s.principalMu.Lock()
		if s.principalUploads == nil {
			s.principalUploads = map[string]int{}
		}
		if s.principalUploads[principal] >= s.cfg.MaxPrincipalUploads {
			s.principalMu.Unlock()
			if s.cfg.MaxActiveUploads > 0 {
				s.uploads.Add(-1)
			}
			return nil, false, "principal_upload_limit_exceeded"
		}
		s.principalUploads[principal]++
		principalAcquired = true
		s.principalMu.Unlock()
	}
	return func() {
		if s.cfg.MaxActiveUploads > 0 {
			s.uploads.Add(-1)
		}
		if principalAcquired {
			s.principalMu.Lock()
			if s.principalUploads[principal] <= 1 {
				delete(s.principalUploads, principal)
			} else {
				s.principalUploads[principal]--
			}
			s.principalMu.Unlock()
		}
	}, true, ""
}

func (s *Server) preAuthPrincipal(r *http.Request) string {
	switch s.cfg.InboundAuth.Mode {
	case config.InboundAuthBearer:
		value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(value))
		return "bearer-claim:" + hex.EncodeToString(sum[:8])
	case config.InboundAuthPlatform:
		value := strings.TrimSpace(r.Header.Get(platformPrincipalHeader))
		sum := sha256.Sum256([]byte(value))
		return "platform-claim:" + hex.EncodeToString(sum[:8])
	default:
		return anonymousPrincipal(r)
	}
}

func anonymousPrincipal(r *http.Request) string {
	principal := "anonymous:"
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return principal + host
	}
	if r.RemoteAddr != "" {
		return principal + r.RemoteAddr
	}
	return principal + "unknown"
}

func (s *Server) acquireStream(principal string) (func(), bool, string) {
	if s.cfg.MaxActiveStreams <= 0 {
		if s.cfg.MaxPrincipalStreams <= 0 {
			return func() {}, true, ""
		}
	} else {
		active := s.streams.Add(1)
		if active > int64(s.cfg.MaxActiveStreams) {
			s.streams.Add(-1)
			return nil, false, "stream_limit_exceeded"
		}
	}
	principalAcquired := false
	if s.cfg.MaxPrincipalStreams > 0 {
		s.principalMu.Lock()
		if s.principalStreams == nil {
			s.principalStreams = map[string]int{}
		}
		if s.principalStreams[principal] >= s.cfg.MaxPrincipalStreams {
			s.principalMu.Unlock()
			if s.cfg.MaxActiveStreams > 0 {
				s.streams.Add(-1)
			}
			return nil, false, "principal_stream_limit_exceeded"
		}
		s.principalStreams[principal]++
		principalAcquired = true
		s.principalMu.Unlock()
	}
	return func() {
		if s.cfg.MaxActiveStreams > 0 {
			s.streams.Add(-1)
		}
		if principalAcquired {
			s.principalMu.Lock()
			if s.principalStreams[principal] <= 1 {
				delete(s.principalStreams, principal)
			} else {
				s.principalStreams[principal]--
			}
			s.principalMu.Unlock()
		}
	}, true, ""
}

func (s *Server) allowPrincipalRequest(principal string, now time.Time) bool {
	if s.cfg.MaxPrincipalRequestsPerMinute <= 0 {
		return true
	}
	s.principalMu.Lock()
	defer s.principalMu.Unlock()
	if s.principalRates == nil {
		s.principalRates = map[string]principalRateWindow{}
	}
	if s.rateLastCleanup.IsZero() || now.Sub(s.rateLastCleanup) >= time.Minute {
		for key, tracked := range s.principalRates {
			if !tracked.start.IsZero() && now.Sub(tracked.start) >= time.Minute {
				delete(s.principalRates, key)
			}
		}
		s.rateLastCleanup = now
	}
	window := s.principalRates[principal]
	if window.start.IsZero() || now.Sub(window.start) >= time.Minute {
		s.principalRates[principal] = principalRateWindow{start: now, count: 1}
		return true
	}
	if window.count >= s.cfg.MaxPrincipalRequestsPerMinute {
		return false
	}
	window.count++
	s.principalRates[principal] = window
	return true
}

func writeJSONError(w http.ResponseWriter, status int, code string) {
	writeJSONErrorMessage(w, status, code, code)
}

func writeJSONErrorMessage(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"error": code}
	if message != "" {
		body["message"] = message
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch s.cfg.InboundAuth.Mode {
		case "", config.InboundAuthNone:
			principal := anonymousPrincipal(r)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
			return
		case config.InboundAuthPlatform:
			principal := ""
			value := strings.TrimSpace(r.Header.Get(platformPrincipalHeader))
			if s.cfg.InboundAuth.PlatformPrincipalHMACSecret != "" {
				signature := strings.TrimSpace(r.Header.Get(platformPrincipalSignatureHeader))
				if value == "" || !strings.HasPrefix(signature, "v4=") {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				timestamp := strings.TrimSpace(r.Header.Get(platformPrincipalTimestampHeader))
				nonce := strings.TrimSpace(r.Header.Get(platformPrincipalNonceHeader))
				parsedTimestamp, err := time.Parse(time.RFC3339, timestamp)
				now := time.Now()
				rawSignature := strings.TrimPrefix(signature, "v4=")
				decodedSignature, decodeErr := hex.DecodeString(rawSignature)
				decodedNonce, nonceErr := hex.DecodeString(nonce)
				if err != nil ||
					now.Sub(parsedTimestamp) > platformPrincipalSignatureSkew ||
					parsedTimestamp.Sub(now) > platformPrincipalSignatureSkew ||
					nonceErr != nil ||
					len(decodedNonce) != 16 ||
					nonce != strings.ToLower(nonce) ||
					decodeErr != nil ||
					len(decodedSignature) != sha256.Size {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				if s.cfg.TrustedPlatformHeaders && len(s.cfg.TrustedPlatformHeaderCIDRs) > 0 && !s.trustedPlatformSource(r) {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				releaseHashSlot, ok := s.acquirePlatformBodyHashSlot()
				if !ok {
					writeJSONError(w, http.StatusTooManyRequests, "platform_body_hash_limit_exceeded")
					return
				}
				bodyHash, err := hashAndRestoreRequestBody(w, r)
				releaseHashSlot()
				if err != nil {
					status := http.StatusBadRequest
					var maxErr *http.MaxBytesError
					if errors.As(err, &maxErr) {
						status = http.StatusRequestEntityTooLarge
					}
					writeJSONErrorMessage(w, status, "invalid_request_body", "invalid request body")
					return
				}
				verifiedAt := time.Now()
				if !verifyPlatformPrincipalSignature(s.cfg.InboundAuth.PlatformPrincipalHMACSecret, r.Method, r.URL.RequestURI(), value, timestamp, nonce, signature, bodyHash, verifiedAt) {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				var replayNonce [16]byte
				copy(replayNonce[:], decodedNonce)
				switch s.platformReplayCache.admit(value, replayNonce, parsedTimestamp.Add(platformPrincipalSignatureSkew), verifiedAt) {
				case platformReplayDuplicate:
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
					return
				case platformReplayCapacity:
					writeJSONError(w, http.StatusTooManyRequests, "platform_replay_cache_limit_exceeded")
					return
				}
			} else if s.cfg.TrustedPlatformHeaders && len(s.cfg.TrustedPlatformHeaderCIDRs) > 0 && s.cfg.Execution.Platform.DeploymentTarget == platform.DeploymentTargetDev {
				if value == "" || !s.trustedPlatformSource(r) {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
			} else if s.cfg.Execution.Platform.DeploymentTarget != platform.DeploymentTargetDev {
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if value != "" {
				principal = "platform:" + value
			}
			if principal == "" {
				principal = "platform:"
				if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
					principal += host
				} else if r.RemoteAddr != "" {
					principal += r.RemoteAddr
				} else {
					principal += "unknown"
				}
			}
			if len(principal) > 240 {
				principal = principal[:240]
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
			return
		case config.InboundAuthBearer:
			if s.cfg.InboundAuth.BearerToken == "" {
				writeJSONErrorMessage(w, http.StatusInternalServerError, "server_auth_misconfigured", "server auth misconfigured")
				return
			}
			const prefix = "Bearer "
			got := r.Header.Get("Authorization")
			if !strings.HasPrefix(got, prefix) || !constantTimeEqual(strings.TrimPrefix(got, prefix), s.cfg.InboundAuth.BearerToken) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="aonohako"`)
				writeJSONError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			sum := sha256.Sum256([]byte(s.cfg.InboundAuth.BearerToken))
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, "bearer:"+hex.EncodeToString(sum[:8]))))
			return
		default:
			writeJSONErrorMessage(w, http.StatusInternalServerError, "server_auth_misconfigured", "server auth misconfigured")
			return
		}
	})
}

func (s *Server) trustedPlatformSource(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remoteIP := net.ParseIP(strings.TrimSpace(host))
	if remoteIP == nil {
		return false
	}
	for _, cidr := range s.cfg.TrustedPlatformHeaderCIDRs {
		if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(remoteIP) {
			return true
		}
	}
	return false
}

func (s *Server) acquirePlatformBodyHashSlot() (func(), bool) {
	if s.platformBodyHashSlots == nil {
		return func() {}, true
	}
	select {
	case s.platformBodyHashSlots <- struct{}{}:
		return func() { <-s.platformBodyHashSlots }, true
	default:
		return nil, false
	}
}

func (s *Server) acquirePayloadURLFetch() (func(), bool) {
	if s.payloadURLFetchSlots == nil {
		return func() {}, true
	}
	select {
	case s.payloadURLFetchSlots <- struct{}{}:
		return func() { <-s.payloadURLFetchSlots }, true
	default:
		return nil, false
	}
}

func hashAndRestoreRequestBody(w http.ResponseWriter, r *http.Request) (string, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	if err != nil {
		return "", err
	}
	if err := r.Body.Close(); err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return hex.EncodeToString(sum[:]), nil
}

func verifyPlatformPrincipalSignature(secret, method, requestURI, principal, timestamp, nonce, signature, bodyHash string, now time.Time) bool {
	timestamp = strings.TrimSpace(timestamp)
	parsedTimestamp, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return false
	}
	if now.Sub(parsedTimestamp) > platformPrincipalSignatureSkew || parsedTimestamp.Sub(now) > platformPrincipalSignatureSkew {
		return false
	}
	signature = strings.TrimSpace(signature)
	if strings.HasPrefix(signature, "v4=") {
		signature = strings.TrimPrefix(signature, "v4=")
	} else {
		return false
	}
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(method + "\n" + requestURI + "\n" + principal + "\n" + timestamp + "\n" + nonce + "\n" + bodyHash))
	want := hex.EncodeToString(mac.Sum(nil))
	return constantTimeEqual(strings.ToLower(signature), want)
}

func constantTimeEqual(a, b string) bool {
	aSum := sha256.Sum256([]byte(a))
	bSum := sha256.Sum256([]byte(b))
	sameDigest := subtle.ConstantTimeCompare(aSum[:], bSum[:])
	sameLength := subtle.ConstantTimeEq(int32(len(a)), int32(len(b)))
	return sameDigest&sameLength == 1
}

func (s *Server) validateRuntimeProfileAllowed(profile string) error {
	if profile == "" {
		return nil
	}
	if !s.cfg.AllowRequestRuntimeProfile {
		return fmt.Errorf("runtime_profile is not allowed by server policy")
	}
	if _, ok := s.cfg.Execution.RuntimeTuningProfiles[profile]; !ok {
		return fmt.Errorf("unknown runtime_profile: %s", profile)
	}
	return nil
}

func (s *Server) applyRuntimeProfilePolicy(problemID string, runtimeProfile *string) error {
	if err := runtimepolicy.ValidateProblemID(problemID); err != nil {
		return fmt.Errorf("invalid problem_id: %w", err)
	}
	if err := runtimepolicy.ValidateProfileName(*runtimeProfile); err != nil {
		return fmt.Errorf("invalid runtime_profile: %w", err)
	}
	mappedProfile := ""
	if problemID != "" {
		mappedProfile = s.cfg.Execution.ProblemRuntimeProfiles[problemID]
	}
	if mappedProfile != "" {
		if *runtimeProfile != "" {
			if *runtimeProfile != mappedProfile {
				return fmt.Errorf("runtime_profile conflicts with problem policy")
			}
		}
		if _, ok := s.cfg.Execution.RuntimeTuningProfiles[mappedProfile]; !ok {
			return fmt.Errorf("problem_id %s maps to unknown runtime_profile: %s", problemID, mappedProfile)
		}
		*runtimeProfile = mappedProfile
		return nil
	}
	return s.validateRuntimeProfileAllowed(*runtimeProfile)
}

func (s *Server) applyPythonLibraryPolicy(req *model.RunRequest) error {
	if req == nil {
		return fmt.Errorf("request is required")
	}
	if err := pythonpolicy.ValidateOptionalLibraryMode(req.PythonLibraryMode); err != nil {
		return fmt.Errorf("invalid python_library_mode: %w", err)
	}
	if !runvalidation.UsesPython(req) {
		if req.PythonLibraryMode != "" {
			return fmt.Errorf("python_library_mode requires a Python contestant, step program, interactor, or spj")
		}
		return nil
	}

	if mappedMode := s.cfg.ProblemPythonLibraryModes[req.ProblemID]; mappedMode != "" {
		if err := pythonpolicy.ValidateOptionalLibraryMode(mappedMode); err != nil {
			return fmt.Errorf("problem_id %s maps to invalid python_library_mode: %w", req.ProblemID, err)
		}
		if req.PythonLibraryMode != "" && req.PythonLibraryMode != mappedMode {
			return fmt.Errorf("python_library_mode conflicts with problem policy")
		}
		req.PythonLibraryMode = mappedMode
		return nil
	}

	defaultMode := s.cfg.DefaultPythonLibraryMode
	if defaultMode == "" {
		defaultMode = pythonpolicy.LibraryModeStdlib
	}
	if err := pythonpolicy.ValidateOptionalLibraryMode(defaultMode); err != nil {
		return fmt.Errorf("server default python_library_mode is invalid: %w", err)
	}
	if req.PythonLibraryMode == "" {
		req.PythonLibraryMode = defaultMode
		return nil
	}
	if req.PythonLibraryMode == pythonpolicy.LibraryModeInstalled &&
		defaultMode != pythonpolicy.LibraryModeInstalled &&
		!s.cfg.AllowRequestPythonInstalledLibraries {
		return fmt.Errorf("python_library_mode %q is not allowed by server policy", pythonpolicy.LibraryModeInstalled)
	}
	return nil
}

func (s *Server) applyRustCratePolicy(req *model.CompileRequest) error {
	if req == nil {
		return fmt.Errorf("request is required")
	}
	if err := rustpolicy.ValidateOptionalCrateMode(req.RustCrateMode); err != nil {
		return fmt.Errorf("invalid rust_crate_mode: %w", err)
	}
	if !rustpolicy.IsRustLanguage(req.Lang) {
		if req.RustCrateMode != "" {
			return fmt.Errorf("rust_crate_mode requires a Rust compile language")
		}
		return nil
	}

	if mappedMode := s.cfg.ProblemRustCrateModes[req.ProblemID]; mappedMode != "" {
		if err := rustpolicy.ValidateOptionalCrateMode(mappedMode); err != nil {
			return fmt.Errorf("problem_id %s maps to invalid rust_crate_mode: %w", req.ProblemID, err)
		}
		if req.RustCrateMode != "" && req.RustCrateMode != mappedMode {
			return fmt.Errorf("rust_crate_mode conflicts with problem policy")
		}
		req.RustCrateMode = mappedMode
		return nil
	}

	defaultMode := s.cfg.DefaultRustCrateMode
	if defaultMode == "" {
		defaultMode = rustpolicy.CrateModeStdlib
	}
	if err := rustpolicy.ValidateOptionalCrateMode(defaultMode); err != nil {
		return fmt.Errorf("server default rust_crate_mode is invalid: %w", err)
	}
	if req.RustCrateMode == "" {
		req.RustCrateMode = defaultMode
		return nil
	}
	if req.RustCrateMode == rustpolicy.CrateModeInstalled &&
		defaultMode != rustpolicy.CrateModeInstalled &&
		!s.cfg.AllowRequestRustInstalledCrates {
		return fmt.Errorf("rust_crate_mode %q is not allowed by server policy", rustpolicy.CrateModeInstalled)
	}
	return nil
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	body := http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing data")
		}
		return err
	}
	if err := body.Close(); err != nil {
		return err
	}
	r.Body = http.NoBody
	r.ContentLength = 0
	return nil
}
