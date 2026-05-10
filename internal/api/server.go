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
	}
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
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	principal := principalFromContext(r.Context())
	if !s.allowPrincipalRequest(principal, time.Now()) {
		writeJSONError(w, http.StatusTooManyRequests, "principal_rate_limited")
		return
	}

	var req model.CompileRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.applyRuntimeProfilePolicy(req.ProblemID, &req.RuntimeProfile); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Sources) == 0 {
		http.Error(w, "no sources", http.StatusBadRequest)
		return
	}
	if len(req.Sources) > maxCompileSourceFiles {
		http.Error(w, fmt.Sprintf("too many sources: max %d", maxCompileSourceFiles), http.StatusBadRequest)
		return
	}
	totalDecodedSourceBytes := 0
	for i, src := range req.Sources {
		if len(src.DataB64)%4 != 0 {
			http.Error(w, fmt.Sprintf("sources[%d].data_b64 invalid base64 length", i), http.StatusBadRequest)
			return
		}
		padding := 0
		if strings.HasSuffix(src.DataB64, "==") {
			padding = 2
		} else if strings.HasSuffix(src.DataB64, "=") {
			padding = 1
		}
		decodedLen := base64.StdEncoding.DecodedLen(len(src.DataB64)) - padding
		if decodedLen > maxCompileDecodedSourceBytes {
			http.Error(w, fmt.Sprintf("source too large: max %d bytes decoded", maxCompileDecodedSourceBytes), http.StatusBadRequest)
			return
		}
		totalDecodedSourceBytes += decodedLen
		if totalDecodedSourceBytes > maxCompileDecodedSourceTotalBytes {
			http.Error(w, fmt.Sprintf("sources total size exceeded: max %d bytes decoded", maxCompileDecodedSourceTotalBytes), http.StatusBadRequest)
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
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "queue_full"})
			return
		}
		http.Error(w, "queue error", http.StatusInternalServerError)
		return
	}

	w.Header().Set(remoteio.ProtocolVersionHeader, remoteio.ProtocolVersion)
	stream, err := sse.New(w)
	if err != nil {
		permit.Cancel()
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	principal := principalFromContext(r.Context())
	if !s.allowPrincipalRequest(principal, time.Now()) {
		writeJSONError(w, http.StatusTooManyRequests, "principal_rate_limited")
		return
	}

	var req model.RunRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := runvalidation.Validate(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.applyRuntimeProfilePolicy(req.ProblemID, &req.RuntimeProfile); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if (req.EnableNetwork || runvalidation.ProgramsEnableNetwork(&req)) && !s.cfg.AllowRequestNetwork {
		http.Error(w, "enable_network is not allowed by server policy", http.StatusBadRequest)
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
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "queue_full"})
			return
		}
		http.Error(w, "queue error", http.StatusInternalServerError)
		return
	}

	w.Header().Set(remoteio.ProtocolVersionHeader, remoteio.ProtocolVersion)
	stream, err := sse.New(w)
	if err != nil {
		permit.Cancel()
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": code})
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
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				bodyHash, err := hashAndRestoreRequestBody(w, r)
				if err != nil {
					status := http.StatusBadRequest
					var maxErr *http.MaxBytesError
					if errors.As(err, &maxErr) {
						status = http.StatusRequestEntityTooLarge
					}
					http.Error(w, "invalid request body", status)
					return
				}
				if !verifyPlatformPrincipalSignature(s.cfg.InboundAuth.PlatformPrincipalHMACSecret, r.Method, r.URL.Path, value, r.Header.Get(platformPrincipalTimestampHeader), signature, bodyHash, time.Now()) {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			} else if len(s.cfg.TrustedPlatformHeaderCIDRs) > 0 && s.cfg.Execution.Platform.DeploymentTarget == platform.DeploymentTargetDev {
				host, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					host = r.RemoteAddr
				}
				remoteIP := net.ParseIP(strings.TrimSpace(host))
				trustedSource := false
				if remoteIP != nil {
					for _, cidr := range s.cfg.TrustedPlatformHeaderCIDRs {
						if _, network, err := net.ParseCIDR(cidr); err == nil && network.Contains(remoteIP) {
							trustedSource = true
							break
						}
					}
				}
				if value == "" || !trustedSource {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			} else if s.cfg.Execution.Platform.DeploymentTarget != platform.DeploymentTargetDev {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
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
				http.Error(w, "server auth misconfigured", http.StatusInternalServerError)
				return
			}
			const prefix = "Bearer "
			got := r.Header.Get("Authorization")
			if !strings.HasPrefix(got, prefix) || !constantTimeEqual(strings.TrimPrefix(got, prefix), s.cfg.InboundAuth.BearerToken) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="aonohako"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			sum := sha256.Sum256([]byte(s.cfg.InboundAuth.BearerToken))
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, "bearer:"+hex.EncodeToString(sum[:8]))))
			return
		default:
			http.Error(w, "server auth misconfigured", http.StatusInternalServerError)
			return
		}
	})
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

func verifyPlatformPrincipalSignature(secret, method, path, principal, timestamp, signature, bodyHash string, now time.Time) bool {
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
	_, _ = mac.Write([]byte(method + "\n" + path + "\n" + principal + "\n" + timestamp + "\n" + bodyHash))
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
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
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
	return nil
}
