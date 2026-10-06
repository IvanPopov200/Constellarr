package downloads_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/transferpolicy"
	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
	"golang.org/x/time/rate"
)

func TestManagerPerJobControls(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	server := newNZBServer(t)
	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: directory,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	job, err := manager.Add(ctx, "controls-release", "Controls release")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	paused, err := manager.Pause(ctx, job.ID)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused.Status != "paused" || paused.PauseReason != "manual" {
		t.Fatalf("paused job = %+v, want a manual paused row", paused)
	}
	if again, err := manager.Pause(ctx, job.ID); err != nil || again.Status != "paused" {
		t.Fatalf("repeated Pause = %+v, %v", again, err)
	}
	if _, err := manager.Retry(ctx, job.ID); !errors.Is(err, downloads.ErrConflict) {
		t.Fatalf("Retry of a paused job: got %v, want ErrConflict", err)
	}

	// Cache, output, and history survive a cancel.
	partsDir := filepath.Join(directory, "downloads", job.ID, "input", ".parts")
	if err := os.MkdirAll(partsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(partsDir, "seg-cached.part")
	if err := os.WriteFile(cacheFile, []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(directory, "downloads", job.ID, "output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outputFile := filepath.Join(outputDir, "movie.mkv")
	if err := os.WriteFile(outputFile, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}

	resumed, err := manager.Resume(ctx, job.ID)
	if err != nil || resumed.Status != "queued" || resumed.PauseReason != "" {
		t.Fatalf("Resume = %+v, %v", resumed, err)
	}
	if again, err := manager.Resume(ctx, job.ID); err != nil || again.Status != "queued" {
		t.Fatalf("repeated Resume = %+v, %v", again, err)
	}

	cancelled, err := manager.Cancel(ctx, job.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("Cancel = %+v, %v", cancelled, err)
	}
	if again, err := manager.Cancel(ctx, job.ID); err != nil || again.Status != "cancelled" {
		t.Fatalf("repeated Cancel = %+v, %v", again, err)
	}
	for _, path := range []string{cacheFile, outputFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Cancel removed %s: %v", path, err)
		}
	}
	retried, err := manager.Retry(ctx, job.ID)
	if err != nil || retried.Status != "queued" || retried.Error != "" {
		t.Fatalf("Retry of a cancelled job = %+v, %v", retried, err)
	}
	if _, err := manager.Retry(ctx, job.ID); !errors.Is(err, downloads.ErrConflict) {
		t.Fatalf("Retry of a queued job: got %v, want ErrConflict", err)
	}

	// Completed jobs refuse every control.
	completed, err := manager.Add(ctx, "controls-completed", "Completed release")
	if err != nil {
		t.Fatalf("Add completed: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'completed' WHERE id = $1`, completed.ID); err != nil {
		t.Fatal(err)
	}
	failed, err := manager.Add(ctx, "controls-failed", "Failed release")
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'failed', error = 'boom' WHERE id = $1`, failed.ID); err != nil {
		t.Fatal(err)
	}

	for name, tc := range map[string]struct {
		job  string
		call func() (downloads.Job, error)
		want error
	}{
		"pause completed":  {completed.ID, func() (downloads.Job, error) { return manager.Pause(ctx, completed.ID) }, downloads.ErrConflict},
		"resume completed": {completed.ID, func() (downloads.Job, error) { return manager.Resume(ctx, completed.ID) }, downloads.ErrConflict},
		"cancel completed": {completed.ID, func() (downloads.Job, error) { return manager.Cancel(ctx, completed.ID) }, downloads.ErrConflict},
		"retry completed":  {completed.ID, func() (downloads.Job, error) { return manager.Retry(ctx, completed.ID) }, downloads.ErrConflict},
		"pause failed":     {failed.ID, func() (downloads.Job, error) { return manager.Pause(ctx, failed.ID) }, downloads.ErrConflict},
		"resume failed":    {failed.ID, func() (downloads.Job, error) { return manager.Resume(ctx, failed.ID) }, downloads.ErrConflict},
		"cancel failed":    {failed.ID, func() (downloads.Job, error) { return manager.Cancel(ctx, failed.ID) }, downloads.ErrConflict},
		"pause missing":    {"missing-job", func() (downloads.Job, error) { return manager.Pause(ctx, "missing-job") }, downloads.ErrNotFound},
		"resume missing":   {"missing-job", func() (downloads.Job, error) { return manager.Resume(ctx, "missing-job") }, downloads.ErrNotFound},
		"cancel missing":   {"missing-job", func() (downloads.Job, error) { return manager.Cancel(ctx, "missing-job") }, downloads.ErrNotFound},
		"retry missing":    {"missing-job", func() (downloads.Job, error) { return manager.Retry(ctx, "missing-job") }, downloads.ErrNotFound},
	} {
		if _, err := tc.call(); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", name, err, tc.want)
		}
	}
	retriedFailure, err := manager.Retry(ctx, failed.ID)
	if err != nil || retriedFailure.Status != "queued" || retriedFailure.Error != "" {
		t.Fatalf("Retry of a failed job = %+v, %v", retriedFailure, err)
	}
}

func TestManagerRestartPreservesControlsAndCache(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	manager, err := downloads.New(ctx, pool, downloads.Config{Directory: directory})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Paused and cancelled jobs with progress and cached parts of their own.
	const pausedID, cancelledID, queuedID = "restart-paused", "restart-cancelled", "restart-queued"
	if _, err := pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb, status, pause_reason, bytes_done, bytes_total, segments_done, segments_total)
		 VALUES ($1, 'restart-paused-release', 'Paused', $2, 'paused', 'manual', 4096, 8192, 3, 8),
		        ($3, 'restart-cancelled-release', 'Cancelled', $2, 'cancelled', '', 1024, 8192, 1, 8),
		        ($4, 'restart-queued-release', 'Queued', $2, 'queued', '', 0, 0, 0, 0)`,
		pausedID, []byte(testNZB), cancelledID, queuedID); err != nil {
		t.Fatalf("insert restart jobs: %v", err)
	}
	cacheDir := filepath.Join(directory, "downloads", pausedID, "input", ".parts")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(cacheDir, "seg-kept.part")
	if err := os.WriteFile(cacheFile, []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Only the queued job may be claimed; the user-owned rows must survive untouched.
	manager.Start(ctx)
	waitForStatus(t, ctx, pool, queuedID, "failed")
	paused, err := manager.Get(ctx, pausedID)
	if err != nil {
		t.Fatalf("Get paused: %v", err)
	}
	if paused.Status != "paused" || paused.PauseReason != "manual" || paused.BytesDone != 4096 ||
		paused.BytesTotal != 8192 || paused.SegmentsDone != 3 || paused.SegmentsTotal != 8 {
		t.Fatalf("paused job after restart = %+v", paused)
	}
	cancelled, err := manager.Get(ctx, cancelledID)
	if err != nil || cancelled.Status != "cancelled" || cancelled.BytesDone != 1024 {
		t.Fatalf("cancelled job after restart = %+v, %v", cancelled, err)
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("restart removed cached parts: %v", err)
	}
	manager.Close()

	restarted, err := downloads.New(ctx, pool, downloads.Config{Directory: directory})
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	t.Cleanup(restarted.Close)
	time.Sleep(200 * time.Millisecond)
	if reloaded, err := restarted.Get(ctx, pausedID); err != nil || reloaded.Status != "paused" {
		t.Fatalf("paused job after a second restart = %+v, %v", reloaded, err)
	}
	if resumed, err := restarted.Resume(ctx, pausedID); err != nil || resumed.Status != "queued" {
		t.Fatalf("Resume after restart = %+v, %v", resumed, err)
	}
	if _, err := os.Stat(cacheFile); err != nil {
		t.Fatalf("Resume removed cached parts: %v", err)
	}
}

func TestManagerGlobalPauseHoldsQueue(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	server := newNZBServer(t)
	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if snapshot := manager.PolicySnapshot(); snapshot.Effective.Paused {
		t.Fatalf("a fresh policy is paused: %+v", snapshot)
	}
	snapshot, err := manager.SetPaused(ctx, true)
	if err != nil || !snapshot.Effective.Paused || snapshot.Effective.Reason == "" {
		t.Fatalf("SetPaused(true) = %+v, %v", snapshot, err)
	}
	// Pausing must leave a usable limiter for completed torrents that continue seeding.
	if manager.Policy().Allowed() {
		t.Fatal("a paused policy still allows transfers")
	}
	limiter := manager.Policy().Limiter()
	if limiter.Limit() == 0 {
		t.Fatal("pausing zeroed the shared limiter")
	}
	grant, grantCancel := context.WithTimeout(ctx, time.Second)
	defer grantCancel()
	if err := limiter.WaitN(grant, 1); err != nil {
		t.Fatalf("the paused policy left the shared limiter unusable: %v", err)
	}
	if cfg := manager.Config(); cfg.Usenet.Limiter != limiter {
		t.Fatal("Config does not carry the shared policy limiter")
	}
	job, err := manager.Add(ctx, "held-release", "Held release")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	manager.Start(ctx)
	t.Cleanup(manager.Close)

	// The queue is gated: nothing is claimed while the policy is paused.
	time.Sleep(600 * time.Millisecond)
	held, err := manager.Get(ctx, job.ID)
	if err != nil || held.Status != "queued" {
		t.Fatalf("job under a paused policy = %+v, %v; want it queued", held, err)
	}

	resumed, err := manager.SetPaused(ctx, false)
	if err != nil || resumed.Effective.Paused {
		t.Fatalf("SetPaused(false) = %+v, %v", resumed, err)
	}
	// Once the gate lifts the job is claimed and fails only because Usenet is unconfigured.
	waitForStatus(t, ctx, pool, job.ID, "failed")
}

// A global hold must stop a transfer that is already running, requeue it, and let it run again.
func TestManagerGlobalHoldStopsActiveUsenetJob(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	server := newNZBServer(t)
	port := silentTCPServer(t)

	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: t.TempDir(),
		Usenet: usenet.Config{Host: "127.0.0.1", Port: port, Username: "user", Password: "secret", Connections: 1},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Close)
	job, err := manager.Add(ctx, "hold-active-release", "Held active download")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	manager.Start(ctx)
	waitForStatus(t, ctx, pool, job.ID, "downloading")

	if _, err := manager.SetPaused(ctx, true); err != nil {
		t.Fatalf("SetPaused(true): %v", err)
	}
	// The running job must return to the queue instead of failing, and stay there while held.
	waitForStatus(t, ctx, pool, job.ID, "queued")
	time.Sleep(300 * time.Millisecond)
	heldJob, err := manager.Get(ctx, job.ID)
	if err != nil || heldJob.Status != "queued" {
		t.Fatalf("held job = %+v, %v; want it queued", heldJob, err)
	}

	if _, err := manager.SetPaused(ctx, false); err != nil {
		t.Fatalf("SetPaused(false): %v", err)
	}
	// Releasing the hold claims the job again.
	waitForStatus(t, ctx, pool, job.ID, "downloading")
}

// A cancelled transfer must stop immediately and keep its status while the worker winds down.
func TestManagerCancelStopsActiveUsenetJob(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	server := newNZBServer(t)
	port := silentTCPServer(t)

	directory := t.TempDir()
	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: directory,
		Usenet: usenet.Config{Host: "127.0.0.1", Port: port, Username: "user", Password: "secret", Connections: 1},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Close)
	job, err := manager.Add(ctx, "cancel-active-release", "Cancelled active download")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	manager.Start(ctx)
	waitForStatus(t, ctx, pool, job.ID, "downloading")

	cancelled, err := manager.Cancel(ctx, job.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("Cancel of a running job = %+v, %v", cancelled, err)
	}
	// The stopping worker must not overwrite the cancelled row, and the job must stay out of the queue.
	time.Sleep(600 * time.Millisecond)
	current, err := manager.Get(ctx, job.ID)
	if err != nil || current.Status != "cancelled" || current.Error != "" {
		t.Fatalf("cancelled job after the worker stopped = %+v, %v", current, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "downloads", job.ID, "input")); err != nil {
		t.Fatalf("Cancel removed the job's cache directory: %v", err)
	}
	// An explicit retry puts it back in the queue.
	if retried, err := manager.Retry(ctx, job.ID); err != nil || retried.Status != "queued" {
		t.Fatalf("Retry after cancel = %+v, %v", retried, err)
	}
	waitForStatus(t, ctx, pool, job.ID, "downloading")
}

// silentTCPServer accepts connections and never speaks, keeping a job in its transfer phase.
func silentTCPServer(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		held   []net.Conn
		heldMu sync.Mutex
	)
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			heldMu.Lock()
			held = append(held, conn)
			heldMu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		heldMu.Lock()
		defer heldMu.Unlock()
		for _, conn := range held {
			_ = conn.Close()
		}
	})
	return listener.Addr().(*net.TCPAddr).Port
}

func TestManagerPolicyUpdateAppliesLimit(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	manager, err := downloads.New(ctx, pool, downloads.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Close)

	for name, cfg := range map[string]transferpolicy.Config{
		"zero kbps":       {Limit: transferpolicy.Limit{Mode: transferpolicy.ModeKbps, Value: 0}},
		"unknown zone":    {Timezone: "Nowhere/Invalid", Limit: transferpolicy.Limit{Mode: transferpolicy.ModeUnlimited}},
		"bad action":      {Limit: transferpolicy.Limit{Mode: transferpolicy.ModeUnlimited}, OutsideSchedule: "whatever"},
		"percent no mbps": {Limit: transferpolicy.Limit{Mode: transferpolicy.ModePercent, Value: 50}},
	} {
		if _, err := manager.UpdatePolicy(ctx, cfg); !errors.Is(err, downloads.ErrInvalid) {
			t.Fatalf("%s: got %v, want ErrInvalid", name, err)
		}
	}

	snapshot, err := manager.UpdatePolicy(ctx, transferpolicy.Config{
		Timezone: "UTC", Limit: transferpolicy.Limit{Mode: transferpolicy.ModeKbps, Value: 512},
		OutsideSchedule: transferpolicy.OutsidePaused, Windows: []transferpolicy.Window{},
	})
	if err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	if snapshot.Effective.Paused {
		t.Fatalf("outside schedule paused unexpectedly: %+v", snapshot.Effective)
	}
	if want := int64(512 * 1024); snapshot.Effective.LimitBytesPerSecond != want {
		t.Fatalf("limit = %d, want %d", snapshot.Effective.LimitBytesPerSecond, want)
	}
	if got := manager.Policy().Limiter().Limit(); got != rate.Limit(512*1024) {
		t.Fatalf("shared limiter = %v, want %d", got, 512*1024)
	}

	snapshot, err = manager.UpdatePolicy(ctx, transferpolicy.Config{
		ConnectionMbps: 100, Limit: transferpolicy.Limit{Mode: transferpolicy.ModePercent, Value: 50},
		OutsideSchedule: transferpolicy.OutsideNormal, Windows: []transferpolicy.Window{},
	})
	if err != nil {
		t.Fatalf("UpdatePolicy percent: %v", err)
	}
	if want := int64(100 * 1e6 / 8 * 50 / 100); snapshot.Effective.LimitBytesPerSecond != want {
		t.Fatalf("percent limit = %d, want %d", snapshot.Effective.LimitBytesPerSecond, want)
	}
	// Outside-schedule handling stays part of the resolved policy, not the manual flag.
	if snapshot.Config.ScheduleEnabled {
		t.Fatalf("schedule unexpectedly enabled: %+v", snapshot.Config)
	}
}

func TestManagerControlRaceKeepsOneRunnableJob(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	server := newNZBServer(t)
	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	job, err := manager.Add(ctx, "race-release", "Race release")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Hold the queue so every control races a job that is never claimed.
	if _, err := manager.SetPaused(ctx, true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	manager.Start(ctx)
	t.Cleanup(manager.Close)

	var churn sync.WaitGroup
	for worker := range 8 {
		churn.Add(1)
		go func(worker int) {
			defer churn.Done()
			for round := range 20 {
				switch (worker + round) % 3 {
				case 0:
					_, _ = manager.Pause(ctx, job.ID)
				case 1:
					_, _ = manager.Resume(ctx, job.ID)
				default:
					_, _ = manager.Cancel(ctx, job.ID)
				}
			}
		}(worker)
	}
	churn.Wait()

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM downloads WHERE id = $1`, job.ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("job rows = %d, %v; want exactly one", rows, err)
	}
	current, err := manager.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get after churn: %v", err)
	}
	switch current.Status {
	case "queued", "paused", "cancelled":
	default:
		t.Fatalf("job ended in %q after churn", current.Status)
	}
	if current.Status == "paused" && current.PauseReason != "manual" {
		t.Fatalf("paused job lost its reason: %+v", current)
	}

	// A final cancel must stick, and the job must stay retryable.
	cancelled, err := manager.Cancel(ctx, job.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("final Cancel = %+v, %v", cancelled, err)
	}
	time.Sleep(400 * time.Millisecond)
	if current, err := manager.Get(ctx, job.ID); err != nil || current.Status != "cancelled" {
		t.Fatalf("cancelled job changed after churn: %+v, %v", current, err)
	}
	if retried, err := manager.Retry(ctx, job.ID); err != nil || retried.Status != "queued" {
		t.Fatalf("Retry after churn = %+v, %v", retried, err)
	}
	// The policy is still paused, so the queue holds the retried job.
	time.Sleep(400 * time.Millisecond)
	if current, err := manager.Get(ctx, job.ID); err != nil || current.Status != "queued" {
		t.Fatalf("retried job was claimed under a paused policy: %+v, %v", current, err)
	}
	// Releasing the policy lets it run (and fail without Usenet configured).
	if _, err := manager.SetPaused(ctx, false); err != nil {
		t.Fatalf("SetPaused(false): %v", err)
	}
	waitForStatus(t, ctx, pool, job.ID, "failed")
}
