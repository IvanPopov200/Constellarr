package downloads

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

const controlTestNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="poster &lt;poster@example.com&gt;" date="1136214245" subject="[1/1] - &quot;movie.mkv&quot; yEnc (1/1)">
    <segments><segment bytes="1024" number="1">synthetic-part-1@example.com</segment></segments>
  </file>
</nzb>`

// controlPool creates an isolated schema so controls tests never touch real tables.
func controlPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a reachable PostgreSQL server to run this test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("cannot create a pool from TEST_DATABASE_URL: %v", err)
	}
	schema := "downloads_controls_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("cannot create an isolated test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatalf("TEST_DATABASE_URL is invalid: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatalf("cannot create a pool for the test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("cannot drop the test schema: %v", err)
		}
		admin.Close()
	})
	return pool
}

func controlManager(t *testing.T, pool *pgxpool.Pool) *Manager {
	t.Helper()
	manager, err := New(context.Background(), pool, Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return manager
}

func controlJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, status, reason string, bytesDone int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb, status, pause_reason, bytes_done, bytes_total)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 8192)`,
		id, "release-"+id, "Synthetic "+id, []byte(controlTestNZB), status, reason, bytesDone); err != nil {
		t.Fatalf("insert job %s: %v", id, err)
	}
}

func controlStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) (status, reason string) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT status, pause_reason FROM downloads WHERE id = $1`, id).Scan(&status, &reason); err != nil {
		t.Fatalf("read job %s: %v", id, err)
	}
	return status, reason
}

// Every worker write must stop at a row the user owns.
func TestGuardedWritesRespectControlStates(t *testing.T) {
	pool := controlPool(t)
	ctx := context.Background()
	manager := controlManager(t, pool)
	const id = "guarded-job"
	controlJob(t, ctx, pool, id, statusDownloading, "", 0)
	progress := usenet.Progress{DownloadedBytes: 512, TotalBytes: 8192, CompletedSegments: 1, TotalSegments: 8}
	outputs := []OutputFile{{Name: "movie.mkv", Size: 3}}

	if err := manager.saveProgress(ctx, pool, id, progress); err != nil {
		t.Fatalf("progress on a running job: %v", err)
	}
	if err := manager.setStatus(ctx, pool, id, "extracting"); err != nil {
		t.Fatalf("stage change on a running job: %v", err)
	}

	for _, tc := range []struct {
		state string
		write func() error
	}{
		{statusPaused, func() error { return manager.saveProgress(ctx, pool, id, progress) }},
		{statusPaused, func() error { return manager.setStatus(ctx, pool, id, "verifying") }},
		{statusPaused, func() error { return manager.complete(ctx, pool, id, outputs) }},
		{statusPaused, func() error { return manager.failJob(ctx, pool, id, "late") }},
		{statusPaused, func() error { return manager.requeueHeld(ctx, pool, id) }},
		{statusCancelled, func() error { return manager.saveProgress(ctx, pool, id, progress) }},
		{statusCancelled, func() error { return manager.setStatus(ctx, pool, id, "verifying") }},
		{statusCancelled, func() error { return manager.complete(ctx, pool, id, outputs) }},
		{statusCancelled, func() error { return manager.failJob(ctx, pool, id, "late") }},
		{statusCancelled, func() error { return manager.requeueHeld(ctx, pool, id) }},
	} {
		if _, err := pool.Exec(ctx, `UPDATE downloads SET status = $2, pause_reason = '' WHERE id = $1`, id, tc.state); err != nil {
			t.Fatalf("set %s: %v", tc.state, err)
		}
		if err := tc.write(); !errors.Is(err, ErrConflict) {
			t.Fatalf("worker write against a %s job: got %v, want ErrConflict", tc.state, err)
		}
		if status, _ := controlStatus(t, ctx, pool, id); status != tc.state {
			t.Fatalf("worker write changed a %s job to %s", tc.state, status)
		}
	}

	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'completed' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := manager.failJob(ctx, pool, id, "late"); !errors.Is(err, ErrConflict) {
		t.Fatalf("failure against a completed job: got %v, want ErrConflict", err)
	}

	// A running job still completes normally.
	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'downloading', bytes_done = 0 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := manager.complete(ctx, pool, id, outputs); err != nil {
		t.Fatalf("complete a running job: %v", err)
	}
	job, err := manager.jobByID(ctx, pool, id)
	if err != nil || job.Status != statusCompleted || len(job.Files) != 1 || job.Files[0].Name != "movie.mkv" {
		t.Fatalf("completed job = %+v, %v", job, err)
	}
}

// Pause must stop a claimed job immediately and must not be undone by worker bookkeeping.
func TestControlIntentStopsActiveJob(t *testing.T) {
	pool := controlPool(t)
	ctx := context.Background()
	manager := controlManager(t, pool)
	const id = "active-job"
	controlJob(t, ctx, pool, id, statusDownloading, "", 0)

	jobCtx, control := manager.beginJob(ctx, id)
	stopped := make(chan struct{})
	go func() {
		<-jobCtx.Done()
		close(stopped)
	}()

	paused, err := manager.Pause(ctx, id)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if paused.Status != statusPaused || paused.PauseReason != pauseReasonManual {
		t.Fatalf("paused job = %+v, want a manual paused row", paused)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Pause did not cancel the active job context")
	}
	if intent := control.intentValue(); intent != statusPaused {
		t.Fatalf("intent = %q, want %q", intent, statusPaused)
	}
	if err := manager.settleStop(ctx, pool, id, control); err != nil {
		t.Fatalf("settleStop after a manual pause: %v", err)
	}
	if status, reason := controlStatus(t, ctx, pool, id); status != statusPaused || reason != pauseReasonManual {
		t.Fatalf("settleStop changed a manual pause to %s/%s", status, reason)
	}
	manager.endJob(control)
}

// A policy hold stops a worker and returns the job to the queue; the gate must not re-claim it while paused.
func TestGlobalHoldRequeuesAndGatesClaims(t *testing.T) {
	pool := controlPool(t)
	ctx := context.Background()
	manager := controlManager(t, pool)
	const id = "held-job"
	controlJob(t, ctx, pool, id, statusDownloading, "", 4096)

	jobCtx, control := manager.beginJob(ctx, id)
	if _, err := manager.SetPaused(ctx, true); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	select {
	case <-jobCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("SetPaused did not cancel the active job context")
	}
	if err := manager.settleStop(ctx, pool, id, control); err != nil {
		t.Fatalf("settleStop: %v", err)
	}
	status, _ := controlStatus(t, ctx, pool, id)
	if status != statusQueued {
		t.Fatalf("held job status = %s, want %s", status, statusQueued)
	}
	var done int64
	if err := pool.QueryRow(ctx, `SELECT bytes_done FROM downloads WHERE id = $1`, id).Scan(&done); err != nil || done != 4096 {
		t.Fatalf("held job lost progress: %d, %v", done, err)
	}
	if _, _, err := manager.claimNext(ctx, pool); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claimNext under a paused policy: got %v, want ErrNotFound", err)
	}

	if _, err := manager.SetPaused(ctx, false); err != nil {
		t.Fatalf("resume policy: %v", err)
	}
	claimed, _, err := manager.claimNext(ctx, pool)
	if err != nil || claimed.ID != id || claimed.Status != statusDownloading {
		t.Fatalf("claim after the policy resumed = %+v, %v", claimed, err)
	}
	manager.endJob(control)
}

// Restarts must leave user-owned rows alone, including their cached parts.
func TestRequeueInterruptedPreservesControls(t *testing.T) {
	pool := controlPool(t)
	ctx := context.Background()
	manager := controlManager(t, pool)
	controlJob(t, ctx, pool, "paused-job", statusPaused, pauseReasonManual, 2048)
	controlJob(t, ctx, pool, "cancelled-job", statusCancelled, "", 1024)
	controlJob(t, ctx, pool, "running-job", "verifying", "", 512)

	if err := manager.requeueInterrupted(ctx, pool); err != nil {
		t.Fatalf("requeueInterrupted: %v", err)
	}
	if status, reason := controlStatus(t, ctx, pool, "paused-job"); status != statusPaused || reason != pauseReasonManual {
		t.Fatalf("paused job after requeue = %s/%s", status, reason)
	}
	if status, _ := controlStatus(t, ctx, pool, "cancelled-job"); status != statusCancelled {
		t.Fatalf("cancelled job after requeue = %s", status)
	}
	if status, _ := controlStatus(t, ctx, pool, "running-job"); status != statusQueued {
		t.Fatalf("interrupted job after requeue = %s, want %s", status, statusQueued)
	}
}

// A schedule-driven policy change must reach the running job without an API call.
func TestPolicyWatcherStopsActiveJobs(t *testing.T) {
	pool := controlPool(t)
	ctx := context.Background()
	manager := controlManager(t, pool)
	manager.Start(ctx)
	defer manager.Close()

	jobCtx, control := manager.beginJob(ctx, "watch-job")
	if _, err := manager.Policy().SetPaused(ctx, true); err != nil {
		t.Fatalf("pause the policy directly: %v", err)
	}
	select {
	case <-jobCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the policy watcher did not stop the active job")
	}
	manager.endJob(control)
}

// Rapid pause/resume churn must leave a runnable row and no stale intent behind.
func TestRapidPauseResumeLeavesJobRunnable(t *testing.T) {
	pool := controlPool(t)
	ctx := context.Background()
	manager := controlManager(t, pool)
	const id = "churn-job"
	controlJob(t, ctx, pool, id, statusDownloading, "", 0)

	_, control := manager.beginJob(ctx, id)
	for range 10 {
		if _, err := manager.Pause(ctx, id); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		if _, err := manager.Resume(ctx, id); err != nil {
			t.Fatalf("Resume: %v", err)
		}
	}
	if status, reason := controlStatus(t, ctx, pool, id); status != statusQueued || reason != "" {
		t.Fatalf("after churn = %s/%s, want a clean queued row", status, reason)
	}
	if intent := control.intentValue(); intent != "" {
		t.Fatalf("stale intent %q after resume", intent)
	}
	if err := manager.settleStop(ctx, pool, id, control); err != nil {
		t.Fatalf("settleStop: %v", err)
	}
	if status, _ := controlStatus(t, ctx, pool, id); status != statusQueued {
		t.Fatalf("settleStop left the resumed job %s", status)
	}
	claimed, _, err := manager.claimNext(ctx, pool)
	if err != nil || claimed.ID != id {
		t.Fatalf("claim after churn = %+v, %v", claimed, err)
	}
	manager.endJob(control)
}

// Cancelling must keep files, cache, and history exactly where they were.
func TestCancelKeepsFilesAndHistory(t *testing.T) {
	pool := controlPool(t)
	ctx := context.Background()
	manager := controlManager(t, pool)
	const id = "cancel-files"
	controlJob(t, ctx, pool, id, statusQueued, "", 3072)

	cacheDir := filepath.Join(manager.root, id, "input", ".parts")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cacheFile := filepath.Join(cacheDir, "seg-kept.part")
	if err := os.WriteFile(cacheFile, []byte("cached"), 0o600); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(manager.root, id, "output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outputFile := filepath.Join(outputDir, "movie.mkv")
	if err := os.WriteFile(outputFile, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}

	cancelled, err := manager.Cancel(ctx, id)
	if err != nil || cancelled.Status != statusCancelled {
		t.Fatalf("Cancel = %+v, %v", cancelled, err)
	}
	if cancelled.BytesDone != 3072 || cancelled.BytesTotal != 8192 {
		t.Fatalf("Cancel dropped progress: %+v", cancelled)
	}
	for _, path := range []string{cacheFile, outputFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Cancel removed %s: %v", path, err)
		}
	}
	if again, err := manager.Cancel(ctx, id); err != nil || again.Status != statusCancelled {
		t.Fatalf("repeated Cancel = %+v, %v", again, err)
	}
	retried, err := manager.Retry(ctx, id)
	if err != nil || retried.Status != statusQueued || retried.Error != "" {
		t.Fatalf("Retry of a cancelled job = %+v, %v", retried, err)
	}
	for _, path := range []string{cacheFile, outputFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Retry removed %s: %v", path, err)
		}
	}
}
