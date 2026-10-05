package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkerDrainTimesOutThenFinishesExactlyOnce(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	record := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name)
	}
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(calls, ",")
	}
	release := make(chan struct{})
	closes := []func() error{
		func() error { record("oldest"); return nil },
		func() error { record("blocked"); <-release; return nil },
		func() error { record("newest"); return nil },
	}
	var drain workerDrain
	drain.start(closes)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := drain.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait = %v, want a deadline error", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("wait blocked for %v; a drain wait must be bounded", elapsed)
	}
	if got := snapshot(); got != "newest,blocked" {
		t.Fatalf("closes before release = %q, want %q", got, "newest,blocked")
	}
	close(release)
	if err := drain.wait(context.Background()); err != nil {
		t.Fatalf("wait after release = %v, want nil", err)
	}
	if got := snapshot(); got != "newest,blocked,oldest" {
		t.Fatalf("closes = %q, want every close once in reverse construction order", got)
	}
	drain.start(closes)
	if err := drain.wait(context.Background()); err != nil {
		t.Fatalf("second wait = %v, want nil", err)
	}
	if got := snapshot(); got != "newest,blocked,oldest" {
		t.Fatalf("closes after a second start = %q; the sequence must run exactly once", got)
	}
}

func TestWorkerDrainRecoversClosePanicAndKeepsClosing(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	record := func(name string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, name)
	}
	closes := []func() error{
		func() error { record("first"); return nil },
		func() error { panic("boom") },
		func() error { record("last"); return nil },
	}
	var drain workerDrain
	drain.start(closes)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := drain.wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("wait = %v, want the recovered close panic", err)
	}
	mu.Lock()
	got := strings.Join(calls, ",")
	mu.Unlock()
	if got != "last,first" {
		t.Fatalf("closes = %q; a panic must not stop the remaining closes", got)
	}
}

func TestFailedDrainStaysPausedAndBecomesFatal(t *testing.T) {
	cancelled := false
	blocked := make(chan struct{})
	automation := newAutomation(func() { cancelled = true })
	automation.add(func() { <-blocked })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := automation.quiesce(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quiesce = %v, want the drain deadline error", err)
	}
	if !cancelled {
		t.Fatal("quiesce must stop the worker context")
	}
	automation.gate.mu.Lock()
	paused := automation.gate.paused
	automation.gate.mu.Unlock()
	if !paused {
		t.Fatal("a failed drain must not resume admission")
	}
	select {
	case fatal := <-automation.fatal:
		if !errors.Is(fatal, context.DeadlineExceeded) {
			t.Fatalf("fatal = %v, want the drain deadline error", fatal)
		}
	default:
		t.Fatal("a failed drain must be reported as fatal")
	}
	close(blocked)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := automation.drain.wait(waitCtx); err != nil {
		t.Fatalf("drain after release = %v, want nil", err)
	}
}

func TestQuiesceFailureBeforeDrainStaysAvailable(t *testing.T) {
	cancelled := false
	automation := newAutomation(func() { cancelled = true })
	entered, release := make(chan struct{}), make(chan struct{})
	handler := automation.gate.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	}))
	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/movies", nil))
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := automation.quiesce(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quiesce = %v, want the pause deadline error", err)
	}
	if cancelled {
		t.Fatal("a failed pause must not stop workers")
	}
	select {
	case fatal := <-automation.fatal:
		t.Fatalf("unexpected fatal %v", fatal)
	default:
	}
	automation.gate.mu.Lock()
	paused := automation.gate.paused
	automation.gate.mu.Unlock()
	if paused {
		t.Fatal("a failed pause must resume admission")
	}
	close(release)
}

func TestSuccessfulQuiesceClosesOnceWithoutFatal(t *testing.T) {
	var mu sync.Mutex
	count := 0
	automation := newAutomation(func() {})
	automation.add(func() {
		mu.Lock()
		defer mu.Unlock()
		count++
	})
	if err := automation.quiesce(context.Background()); err != nil {
		t.Fatalf("quiesce = %v, want nil", err)
	}
	if err := automation.stop(); err != nil {
		t.Fatalf("stop = %v, want nil", err)
	}
	mu.Lock()
	got := count
	mu.Unlock()
	if got != 1 {
		t.Fatalf("close ran %d times, want once", got)
	}
	select {
	case fatal := <-automation.fatal:
		t.Fatalf("unexpected fatal %v", fatal)
	default:
	}
}

func TestCleanupFailuresForbidInProcessRestart(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	started := time.Now()
	restart, err := runCleanup(true, nil,
		cleanupStep{"operations service", func() error { panic("ops close panicked") }, time.Second},
		cleanupStep{"automation workers", func() error { return errors.New("worker close failed") }, time.Second},
		cleanupStep{"PostgreSQL pool", func() error { <-blocked; return nil }, 20 * time.Millisecond},
	)
	if restart {
		t.Fatal("a cleanup failure must not allow an in-process restart")
	}
	for _, want := range []string{"ops close panicked", "worker close failed", "PostgreSQL pool did not stop in time"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want %q", err, want)
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cleanup took %v; every close must stay bounded", elapsed)
	}
	fatal := errors.New("automation workers did not stop: deadline exceeded")
	restart, err = runCleanup(true, fatal, cleanupStep{"PostgreSQL pool", func() error { <-blocked; return nil }, 20 * time.Millisecond})
	if restart || !errors.Is(err, fatal) {
		t.Fatalf("runCleanup = (%v, %v), want the original fatal error preserved without a restart", restart, err)
	}
}

func TestWaitForStopNeverRestartsAfterFailedDrain(t *testing.T) {
	drainErr := errors.New("automation workers did not stop")
	fatal := make(chan error, 1)
	fatal <- drainErr
	restart, err := waitForStop(&http.Server{}, make(chan error), fatal, make(chan struct{}), context.Background())
	if restart {
		t.Fatal("a failed drain must not restart services")
	}
	if !errors.Is(err, drainErr) {
		t.Fatalf("err = %v, want the drain error", err)
	}
}

func TestWaitForStopRestartsOnlyAfterReload(t *testing.T) {
	reload := make(chan struct{}, 1)
	reload <- struct{}{}
	restart, err := waitForStop(&http.Server{}, make(chan error), make(chan error), reload, context.Background())
	if err != nil || !restart {
		t.Fatalf("waitForStop = (%v, %v), want a clean restart", restart, err)
	}
	serveErr := make(chan error, 1)
	serveErr <- http.ErrServerClosed
	if restart, err := waitForStop(&http.Server{}, serveErr, make(chan error), make(chan struct{}), context.Background()); err != nil || restart {
		t.Fatalf("waitForStop on a closed server = (%v, %v), want no restart and no error", restart, err)
	}
	serveErr = make(chan error, 1)
	serveErr <- errors.New("boom")
	if _, err := waitForStop(&http.Server{}, serveErr, make(chan error), make(chan struct{}), context.Background()); err == nil {
		t.Fatal("waitForStop must report a serving failure")
	}
}
