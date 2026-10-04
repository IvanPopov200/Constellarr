package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/api"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

func main() {
	if err := run(); err != nil {
		slog.Error("constellarr failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}

	ctx := context.Background()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return errors.New("DATABASE_URL is not a valid PostgreSQL connection string")
	}
	config.MaxConns = 10
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("cannot create the PostgreSQL connection pool")
	}
	defer pool.Close()
	shutdown, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	settings, err := downloads.FromEnv()
	if err != nil {
		return err
	}
	manager, err := downloads.New(shutdown, pool, settings)
	if err != nil {
		return err
	}
	manager.Start(shutdown)
	defer manager.Close()

	server := &http.Server{
		Handler:           api.New(pool, api.Services{Downloads: manager, Config: settings}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	slog.Info("constellarr listening", "addr", listener.Addr().String())

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-shutdown.Done():
	}

	slog.Info("constellarr shutting down")
	shutdownCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
