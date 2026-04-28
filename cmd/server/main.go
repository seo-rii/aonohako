package main

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"aonohako/internal/api"
	"aonohako/internal/config"
	"aonohako/internal/processhardening"
	"aonohako/internal/sandbox"
)

func main() {
	if sandbox.MaybeRunFromEnv() {
		return
	}
	if err := processhardening.DisableDumpability(); err != nil {
		slog.Error("aonohako process hardening failed", "err", err)
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("aonohako startup validation failed", "err", err)
		os.Exit(1)
	}
	server, err := api.New(cfg)
	if err != nil {
		slog.Error("aonohako executor initialization failed", "err", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.BodyReadTimeout,
	}
	slog.Info("aonohako listening", "addr", httpServer.Addr, "active", cfg.MaxActiveRuns, "pending", cfg.MaxPendingQueue)
	if err := httpServer.ListenAndServe(); err != nil {
		slog.Error("aonohako server stopped", "err", err)
	}
}
