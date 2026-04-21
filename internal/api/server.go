package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"aonohako/internal/compile"
	"aonohako/internal/config"
	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/queue"
	"aonohako/internal/sse"
)

type Server struct {
	cfg     config.Config
	compile interface {
		Run(context.Context, *model.CompileRequest) model.CompileResponse
	}
	execute execute.Runner
	queue   *queue.Manager
	seq     atomic.Uint64
}

func New(cfg config.Config) (*Server, error) {
	runner, err := execute.Build(cfg)
	if err != nil {
		return nil, err
	}
	return NewWithServices(cfg, compile.New(), runner), nil
}

func NewWithServices(cfg config.Config, compileService interface {
	Run(context.Context, *model.CompileRequest) model.CompileResponse
}, executeRunner execute.Runner) *Server {
	return &Server{
		cfg:     cfg,
		compile: compileService,
		execute: executeRunner,
		queue:   queue.New(cfg.MaxActiveRuns, cfg.MaxPendingQueue),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.healthz)
	mux.HandleFunc("/compile", s.compileHandler)
	mux.HandleFunc("/execute", s.executeHandler)
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
	var req model.CompileRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

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

	stream, err := sse.New(w)
	if err != nil {
		permit.Cancel()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go stream.Heartbeat(r.Context(), s.cfg.HeartbeatInterval)

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
	var req model.RunRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

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

	stream, err := sse.New(w)
	if err != nil {
		permit.Cancel()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go stream.Heartbeat(r.Context(), s.cfg.HeartbeatInterval)

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
