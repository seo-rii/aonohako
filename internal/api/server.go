package api

import (
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
	"aonohako/internal/queue"
	"aonohako/internal/remoteio"
	"aonohako/internal/sse"
)

const (
	maxRunTextFieldBytes              = 16 << 20
	maxRunTimeMs                      = 60_000
	maxRunMemoryMB                    = 4096
	maxRunOutputBytes                 = 8 << 20
	maxRunWorkspaceBytes              = 1 << 30
	maxCompileSourceFiles             = 512
	maxCompileDecodedSourceBytes      = 16 << 20
	maxCompileDecodedSourceTotalBytes = 48 << 20
	platformPrincipalHeader           = "X-Aonohako-Principal"
	platformPrincipalSignatureHeader  = "X-Aonohako-Principal-Signature"
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
	if err := validateRunRequestFields(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.EnableNetwork && !s.cfg.AllowRequestNetwork {
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
				if value == "" || !verifyPlatformPrincipalSignature(s.cfg.InboundAuth.PlatformPrincipalHMACSecret, value, r.Header.Get(platformPrincipalSignatureHeader)) {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			} else if len(s.cfg.TrustedPlatformHeaderCIDRs) > 0 {
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

func verifyPlatformPrincipalSignature(secret, principal, signature string) bool {
	signature = strings.TrimSpace(signature)
	if strings.HasPrefix(signature, "v1=") {
		signature = strings.TrimPrefix(signature, "v1=")
	}
	if signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(principal))
	want := hex.EncodeToString(mac.Sum(nil))
	return constantTimeEqual(strings.ToLower(signature), want)
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func validateRunRequestFields(req *model.RunRequest) error {
	if len(req.Stdin) > maxRunTextFieldBytes {
		return fmt.Errorf("stdin too large: max %d bytes", maxRunTextFieldBytes)
	}
	if len(req.ExpectedStdout) > maxRunTextFieldBytes {
		return fmt.Errorf("expected_stdout too large: max %d bytes", maxRunTextFieldBytes)
	}
	if req.Limits.TimeMs <= 0 || req.Limits.TimeMs > maxRunTimeMs {
		return fmt.Errorf("limits.time_ms must be between 1 and %d", maxRunTimeMs)
	}
	if req.Limits.MemoryMB <= 0 || req.Limits.MemoryMB > maxRunMemoryMB {
		return fmt.Errorf("limits.memory_mb must be between 1 and %d", maxRunMemoryMB)
	}
	if req.Limits.OutputBytes < 0 || req.Limits.OutputBytes > maxRunOutputBytes {
		return fmt.Errorf("limits.output_bytes must be between 0 and %d", maxRunOutputBytes)
	}
	if req.Limits.WorkspaceBytes < 0 || req.Limits.WorkspaceBytes > maxRunWorkspaceBytes {
		return fmt.Errorf("limits.workspace_bytes must be between 0 and %d", maxRunWorkspaceBytes)
	}
	if req.SPJ != nil && req.SPJ.Limits != nil {
		if req.SPJ.Limits.TimeMs < 0 || req.SPJ.Limits.TimeMs > maxRunTimeMs {
			return fmt.Errorf("spj.limits.time_ms must be between 0 and %d", maxRunTimeMs)
		}
		if req.SPJ.Limits.MemoryMB < 0 || req.SPJ.Limits.MemoryMB > maxRunMemoryMB {
			return fmt.Errorf("spj.limits.memory_mb must be between 0 and %d", maxRunMemoryMB)
		}
		if req.SPJ.Limits.OutputBytes < 0 || req.SPJ.Limits.OutputBytes > maxRunOutputBytes {
			return fmt.Errorf("spj.limits.output_bytes must be between 0 and %d", maxRunOutputBytes)
		}
		if req.SPJ.Limits.WorkspaceBytes < 0 || req.SPJ.Limits.WorkspaceBytes > maxRunWorkspaceBytes {
			return fmt.Errorf("spj.limits.workspace_bytes must be between 0 and %d", maxRunWorkspaceBytes)
		}
	}
	return nil
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20))
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
