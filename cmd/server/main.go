package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpServer.ListenAndServe()
	}()

	var serveErr error
	serverStopped := false
	select {
	case serveErr = <-serverErr:
		serverStopped = true
	case <-ctx.Done():
		stop()
		slog.Info("aonohako shutdown requested")
	}
	shutdownErr := shutdownServer(httpServer, server, 10*time.Second)
	if !serverStopped {
		serveErr = <-serverErr
	}
	if shutdownErr != nil {
		slog.Error("aonohako shutdown completed after forced cancellation", "err", shutdownErr)
		os.Exit(1)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		slog.Error("aonohako server stopped", "err", serveErr)
		os.Exit(1)
	}
}
