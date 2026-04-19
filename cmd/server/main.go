package main

import (
	"log/slog"
	"net/http"
	"time"

	"aonohako/internal/api"
	"aonohako/internal/config"
	"aonohako/internal/sandbox"
)

func main() {
	if sandbox.MaybeRunFromEnv() {
		return
	}

	cfg := config.Load()
	server := api.New(cfg)

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("aonohako listening", "addr", httpServer.Addr, "active", cfg.MaxActiveRuns, "pending", cfg.MaxPendingQueue)
	if err := httpServer.ListenAndServe(); err != nil {
		slog.Error("aonohako server stopped", "err", err)
	}
}
