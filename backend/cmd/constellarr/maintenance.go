package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	workerDrainTimeout  = 10 * time.Second
	opsCloseTimeout     = 10 * time.Second
	poolCloseTimeout    = 5 * time.Second
	httpShutdownTimeout = 30 * time.Second
)

type requestGate struct {
	mu     sync.Mutex
	paused bool
	active int
	idle   chan struct{}
}

func (g *requestGate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isRestore := r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/operations/backups/") && strings.HasSuffix(r.URL.Path, "/restore")
		if !strings.HasPrefix(r.URL.Path, "/api") && r.URL.Path != "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		g.mu.Lock()
		if g.paused {
			g.mu.Unlock()
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"A database restore is in progress. Try again shortly."}`))
			return
		}
		if isRestore {
			g.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}
		if g.active == 0 {
			g.idle = make(chan struct{})
		}
		g.active++
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			g.active--
			if g.active == 0 {
				close(g.idle)
			}
			g.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}

func (g *requestGate) pause(ctx context.Context) error {
	g.mu.Lock()
	g.paused = true
	idle, active := g.idle, g.active
	g.mu.Unlock()
	if active == 0 {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		g.mu.Lock()
		g.paused = false
		g.mu.Unlock()
		return ctx.Err()
	}
}

// automation pauses admission and runs every worker close exactly once; a failed drain is terminal.
type automation struct {
	gate   *requestGate
	drain  workerDrain
	fatal  chan error
	cancel context.CancelFunc
	stops  []func() error
}

func newAutomation(cancel context.CancelFunc) *automation {
	return &automation{gate: &requestGate{}, fatal: make(chan error, 1), cancel: cancel}
}

func (a *automation) add(stop func()) {
	a.stops = append(a.stops, func() error {
		stop()
		return nil
	})
}

func (a *automation) start() {
	a.cancel()
	a.drain.start(a.stops)
}

// quiesce pauses admission and waits for the drain; a failed drain must end the server instead of resuming.
func (a *automation) quiesce(ctx context.Context) error {
	if err := a.gate.pause(ctx); err != nil {
		return err
	}
	a.start()
	if err := a.drain.wait(ctx); err != nil {
		err = fmt.Errorf("automation workers did not stop: %w", err)
		select {
		case a.fatal <- err:
		default:
		}
		return err
	}
	return nil
}

// stop bounds the final drain wait and reports every worker that did not stop.
func (a *automation) stop() error {
	a.start()
	ctx, cancel := context.WithTimeout(context.Background(), workerDrainTimeout)
	defer cancel()
	if err := a.drain.wait(ctx); err != nil {
		return fmt.Errorf("automation workers did not stop: %w", err)
	}
	return nil
}

// shutdownServer stops the HTTP server within a fixed budget, then force-closes leftovers.
func shutdownServer(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}

// closeBounded runs one close in the background; a timeout or recovered panic becomes an error.
func closeBounded(name string, stop func() error, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- closeWorker(stop) }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("%s did not stop in time", name)
	}
}

// cleanupStep is one bounded close executed while the server shuts down.
type cleanupStep struct {
	name    string
	stop    func() error
	timeout time.Duration
}

// runCleanup runs every bounded close; a failure forbids an in-process restart and is joined into err.
func runCleanup(restart bool, err error, steps ...cleanupStep) (bool, error) {
	for _, step := range steps {
		if closeErr := closeBounded(step.name, step.stop, step.timeout); closeErr != nil {
			restart = false
			err = errors.Join(err, closeErr)
		}
	}
	return restart, err
}

// workerDrain runs the worker close sequence exactly once in the background.
type workerDrain struct {
	once sync.Once
	done chan struct{}
	err  error
}

func (d *workerDrain) start(stops []func() error) {
	d.once.Do(func() {
		d.done = make(chan struct{})
		go func() {
			defer close(d.done)
			d.err = closeWorkers(stops)
		}()
	})
}

// wait waits for the drain up to ctx; a timed-out sequence keeps closing and is never restarted.
func (d *workerDrain) wait(ctx context.Context) error {
	select {
	case <-d.done:
		return d.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// closeWorkers closes in reverse construction order and keeps going after a panic.
func closeWorkers(stops []func() error) error {
	var first error
	for i := len(stops) - 1; i >= 0; i-- {
		if err := closeWorker(stops[i]); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func closeWorker(stop func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("worker close panicked: %v", recovered)
		}
	}()
	return stop()
}
