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
	"aonohako/internal/queue"
	"aonohako/internal/remoteio"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/runvalidation"
	"aonohako/internal/sse"
	"aonohako/internal/util"
)

const (
	maxRunTextFieldBytes              = runvalidation.MaxTextFieldBytes
	maxRunTimeMs                      = runvalidation.MaxTimeMs
	maxRunMemoryMB                    = runvalidation.MaxMemoryMB
	maxRunOutputBytes                 = runvalidation.MaxOutputBytes
	maxRunWorkspaceBytes              = runvalidation.MaxWorkspaceBytes
	maxCompileSourceFiles             = 512
	maxCompileDecodedSourceBytes      = 16 << 20
	maxCompileDecodedSourceTotalBytes = 48 << 20
	maxJSONBodyBytes                  = 64 << 20
	defaultPlatformBodyHashSlots      = 4
	platformPrincipalHeader           = "X-Aonohako-Principal"
	platformPrincipalSignatureHeader  = "X-Aonohako-Principal-Signature"
	platformPrincipalTimestampHeader  = "X-Aonohako-Principal-Timestamp"
	platformPrincipalSignatureSkew    = 5 * time.Minute
)

type principalContextKey struct{}

type principalRateWindow struct {
	start time.Time
	count int
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

	principalMu      sync.Mutex
	principalStreams map[string]int
	principalRates   map[string]principalRateWindow
	rateLastCleanup  time.Time

	platformBodyHashSlots chan struct{}
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
	return &Server{
		cfg:              cfg,
		compile:          compileService,
		execute:          executeRunner,
		queue:            queue.New(cfg.MaxActiveRuns, cfg.MaxPendingQueue),
		principalStreams: map[string]int{},
		principalRates:   map[string]principalRateWindow{},
		platformBodyHashSlots: make(
			chan struct{},
			platformBodyHashConcurrency(cfg),
		),
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
	mux.HandleFunc("/healthz", s.healthz)
	mux.Handle("/compile", s.requireAuth(http.HandlerFunc(s.compileHandler)))
	mux.Handle("/execute", s.requireAuth(http.HandlerFunc(s.executeHandler)))
	return mux
}

func (s *Server) nextID(prefix string) string {
	n := s.seq.Add(1)
	return prefix + "-" + strconv.FormatUint(n, 10)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
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

	w.Header().Set(remoteio.ProtocolVersionHeader, remoteio.ProtocolVersion)
	stream, err := sse.New(w)
	if err != nil {
		permit.Cancel()
		writeJSONErrorMessage(w, http.StatusInternalServerError, "stream_init_failed", err.Error())
		return
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(r.Context())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		stream.Heartbeat(heartbeatCtx, s.cfg.HeartbeatInterval)
	}()
	defer func() {
		stopHeartbeat()
		<-heartbeatDone
	}()

	reqID := s.nextID("compile")
	active, pending := s.queue.Snapshot()
	_ = stream.Event("progress", map[string]any{
		"stage":          "accepted",
		"request_id":     reqID,
		"queue_position": permit.Position(),
		"active_runs":    active,
		"queue_pending":  pending,
		"ts":             time.Now().UnixMilli(),
	})

	if err := permit.Wait(r.Context()); err != nil {
		if r.Context().Err() == nil {
			_ = stream.Event("error", map[string]any{"message": err.Error()})
		}
		return
	}
	defer permit.Release()

	_ = stream.Event("progress", map[string]any{"stage": "start", "request_id": reqID, "ts": time.Now().UnixMilli()})

	resp := s.compile.Run(r.Context(), &req)
	if resp.Stdout != "" {
		_ = stream.Event("log", map[string]any{"stream": "stdout", "chunk": resp.Stdout})
	}
	if resp.Stderr != "" {
		_ = stream.Event("log", map[string]any{"stream": "stderr", "chunk": resp.Stderr})
	}
	if resp.Status != model.CompileStatusOK {
		_ = stream.Event("error", map[string]any{"message": firstNonEmpty(resp.Reason, resp.Stderr, resp.Stdout, "compile failed")})
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
	if err := runvalidation.Validate(&req); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.applyRuntimeProfilePolicy(req.ProblemID, &req.RuntimeProfile); err != nil {
		writeJSONErrorMessage(w, http.StatusBadRequest, "invalid_runtime_profile", err.Error())
		return
	}
	if (req.EnableNetwork || runvalidation.ProgramsEnableNetwork(&req)) && !s.cfg.AllowRequestNetwork {
		writeJSONErrorMessage(w, http.StatusBadRequest, "network_not_allowed", "enable_network is not allowed by server policy")
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

	w.Header().Set(remoteio.ProtocolVersionHeader, remoteio.ProtocolVersion)
	stream, err := sse.New(w)
	if err != nil {
		permit.Cancel()
		writeJSONErrorMessage(w, http.StatusInternalServerError, "stream_init_failed", err.Error())
		return
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(r.Context())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		stream.Heartbeat(heartbeatCtx, s.cfg.HeartbeatInterval)
	}()
	defer func() {
		stopHeartbeat()
		<-heartbeatDone
	}()

	reqID := s.nextID("execute")
	active, pending := s.queue.Snapshot()
	_ = stream.Event("progress", map[string]any{
		"stage":          "accepted",
		"request_id":     reqID,
		"queue_position": permit.Position(),
		"active_runs":    active,
		"queue_pending":  pending,
		"ts":             time.Now().UnixMilli(),
	})

	if err := permit.Wait(r.Context()); err != nil {
		if r.Context().Err() == nil {
			_ = stream.Event("error", map[string]any{"message": err.Error()})
		}
		return
	}
	defer permit.Release()

	_ = stream.Event("progress", map[string]any{"stage": "start", "request_id": reqID, "ts": time.Now().UnixMilli()})

	resp := s.execute.Run(r.Context(), &req, execute.Hooks{
		OnImage: func(mime, b64 string, ts int64) {
			_ = stream.Event("image", map[string]any{"mime": mime, "b64": b64, "ts": ts})
		},
		OnLog: func(streamName, msg string) {
			_ = stream.Event("log", map[string]any{"stream": streamName, "chunk": msg})
		},
	})

	if resp.Status == model.RunStatusInitFail {
		_ = stream.Event("error", map[string]any{"message": firstNonEmpty(resp.Reason, resp.Stderr, resp.Stdout, "execution failed")})
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
			principal := "anonymous:"
			if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
				principal += host
			} else if r.RemoteAddr != "" {
				principal += r.RemoteAddr
			} else {
				principal += "unknown"
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
			return
		case config.InboundAuthPlatform:
			principal := ""
			value := strings.TrimSpace(r.Header.Get(platformPrincipalHeader))
			if s.cfg.InboundAuth.PlatformPrincipalHMACSecret != "" {
				signature := strings.TrimSpace(r.Header.Get(platformPrincipalSignatureHeader))
				if value == "" || !strings.HasPrefix(signature, "v3=") {
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
				if !verifyPlatformPrincipalSignature(s.cfg.InboundAuth.PlatformPrincipalHMACSecret, r.Method, r.URL.RequestURI(), value, r.Header.Get(platformPrincipalTimestampHeader), signature, bodyHash, time.Now()) {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				if s.cfg.TrustedPlatformHeaders && len(s.cfg.TrustedPlatformHeaderCIDRs) > 0 && !s.trustedPlatformSource(r) {
					writeJSONError(w, http.StatusUnauthorized, "unauthorized")
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

func verifyPlatformPrincipalSignature(secret, method, requestURI, principal, timestamp, signature, bodyHash string, now time.Time) bool {
	timestamp = strings.TrimSpace(timestamp)
	parsedTimestamp, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return false
	}
	if now.Sub(parsedTimestamp) > platformPrincipalSignatureSkew || parsedTimestamp.Sub(now) > platformPrincipalSignatureSkew {
		return false
	}
	signature = strings.TrimSpace(signature)
	if strings.HasPrefix(signature, "v3=") {
		signature = strings.TrimPrefix(signature, "v3=")
	} else {
		return false
	}
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(method + "\n" + requestURI + "\n" + principal + "\n" + timestamp + "\n" + bodyHash))
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
