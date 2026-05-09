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

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("aonohako server stopped", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		stop()
		slog.Info("aonohako shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("aonohako graceful shutdown failed", "err", err)
			_ = httpServer.Close()
			os.Exit(1)
		}
		if err := <-serverErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("aonohako server stopped", "err", err)
			os.Exit(1)
		}
	}
}
