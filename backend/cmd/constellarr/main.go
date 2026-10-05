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
	"github.com/IvanPopov200/Constellarr/backend/internal/auth"
	"github.com/IvanPopov200/Constellarr/backend/internal/discovery"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/migration"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
	"github.com/IvanPopov200/Constellarr/backend/internal/operations"
	"github.com/IvanPopov200/Constellarr/backend/internal/subtitles"
	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
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
	shutdown, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		restart, err := runServer(shutdown, addr, databaseURL)
		if err != nil || !restart || shutdown.Err() != nil {
			return err
		}
		slog.Info("restarting services after database maintenance")
	}
}

func runServer(shutdown context.Context, addr, databaseURL string) (restart bool, err error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return false, errors.New("DATABASE_URL is not a valid PostgreSQL connection string")
	}
	config.MaxConns = 20
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(shutdown, config)
	if err != nil {
		return false, errors.New("cannot create the PostgreSQL connection pool")
	}
	settings, err := downloads.FromEnv()
	if err != nil {
		return false, err
	}
	workersCtx, cancelWorkers := context.WithCancel(shutdown)
	automation := newAutomation(cancelWorkers)
	var ops *operations.Service
	opsStop := func() error {
		if ops != nil {
			ops.Close()
		}
		return nil
	}
	poolStop := func() error {
		pool.Close()
		return nil
	}
	// One bounded cleanup runs every close; any failure forbids an in-process restart.
	defer func() {
		restart, err = runCleanup(restart, err,
			cleanupStep{"operations service", opsStop, opsCloseTimeout},
			cleanupStep{"automation workers", automation.stop, workerDrainTimeout},
			cleanupStep{"PostgreSQL pool", poolStop, poolCloseTimeout},
		)
	}()
	manager, err := downloads.New(workersCtx, pool, settings)
	if err != nil {
		return false, err
	}
	automation.add(manager.Close)
	movieLibrary, err := movies.New(workersCtx, pool, manager)
	if err != nil {
		return false, err
	}
	automation.add(movieLibrary.Close)
	tvLibrary, err := tv.New(workersCtx, pool, manager, movieLibrary.Store)
	if err != nil {
		return false, err
	}
	automation.add(tvLibrary.Close)
	musicLibrary, err := music.New(workersCtx, pool, manager)
	if err != nil {
		return false, err
	}
	automation.add(musicLibrary.Close)
	transfers, err := torrents.New(workersCtx, pool, torrents.Options{Directory: settings.Directory})
	if err != nil {
		return false, err
	}
	automation.add(transfers.Close)
	manager.SetTorrents(torrentSource{service: transfers})
	access, err := auth.New(workersCtx, pool)
	if err != nil {
		return false, err
	}
	automation.add(access.Close)
	requests, err := discovery.New(workersCtx, pool, movieLibrary, tvLibrary, discovery.Options{
		Music:    musicSource{service: musicLibrary},
		UserName: access.DisplayName,
		Notify: func(ctx context.Context, request discovery.Request) {
			if ops != nil && request.Delivery.Phase == discovery.PhaseFailed {
				if err := ops.RecordRequestFailure(ctx, request.Delivery.Message); err != nil {
					slog.Warn("record request failure", "error", err)
				}
			}
		},
		Can: func(r *http.Request, permission string) bool {
			principal, _ := auth.FromContext(r.Context())
			return principal.Can(permission)
		},
		Actor: func(r *http.Request) (string, bool) {
			principal, _ := auth.FromContext(r.Context())
			return principal.UserID, principal.Can(auth.PermRequestsApprove)
		},
	})
	if err != nil {
		return false, err
	}
	automation.add(requests.Close)
	captions, err := subtitles.New(workersCtx, pool, subtitles.Options{
		Movies: movieLibrary, TV: tvLibrary, Translator: sharedTranslator{service: requests.AI},
	})
	if err != nil {
		return false, err
	}
	automation.add(captions.Close)
	migrator, err := migration.New(workersCtx, pool, migration.Options{
		Movies: movieLibrary, TV: tvLibrary, Music: musicLibrary,
		Subtitles: captions, Torrents: transfers, Downloads: manager,
	})
	if err != nil {
		return false, err
	}
	automation.add(migrator.Close)
	gate := automation.gate
	reload := make(chan struct{}, 1)
	ops, err = operations.New(shutdown, pool, operations.Options{
		DataDir: settings.Directory, DatabaseURL: databaseURL, AllowLiveRestore: true,
		AutoRestart: true,
		Quiesce:     automation.quiesce,
		Resume: func(context.Context) error {
			select {
			case reload <- struct{}{}:
			default:
			}
			return nil
		},
	})
	if err != nil {
		return false, err
	}
	for _, start := range []func(context.Context){
		manager.Start, movieLibrary.Start, tvLibrary.Start, musicLibrary.Start,
		transfers.Start, access.Start, requests.Start, captions.Start, migrator.Start,
	} {
		start(workersCtx)
	}
	ops.Start(shutdown)
	handler := api.New(pool, api.Services{
		Downloads: manager, Movies: movieLibrary, TV: tvLibrary,
		Modules: []api.Module{access, musicLibrary, transfers, requests, captions, migrator, ops},
		Guard:   access.Middleware,
	})
	server := &http.Server{
		Handler: ops.WrapHTTP(gate.wrap(handler)), ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return false, fmt.Errorf("listen on %s: %w", addr, err)
	}
	defer listener.Close()
	slog.Info("constellarr listening", "addr", listener.Addr().String())
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	return waitForStop(server, serveErr, automation.fatal, reload, shutdown)
}

// waitForStop reports whether services should restart; a failed drain ends the process instead.
func waitForStop(server *http.Server, serveErr, fatal <-chan error, reload <-chan struct{}, shutdown context.Context) (bool, error) {
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return false, nil
		}
		return false, fmt.Errorf("serve HTTP: %w", err)
	case err := <-fatal:
		// Half-closed workers cannot serve again, so stop the server and let Compose restart the process.
		if shutdownErr := shutdownServer(server); shutdownErr != nil {
			slog.Warn("HTTP shutdown after failed drain", "error", shutdownErr)
		}
		return false, err
	case <-reload:
		if err := shutdownServer(server); err != nil {
			return false, err
		}
		return true, nil
	case <-shutdown.Done():
		if err := shutdownServer(server); err != nil {
			return false, err
		}
		return false, nil
	}
}
