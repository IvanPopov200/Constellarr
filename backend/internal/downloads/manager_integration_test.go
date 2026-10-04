package downloads_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

const testAPIKey = "synthetic-key-123"

const testNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head><meta type="title">Big Buck Bunny</meta></head>
  <file poster="poster &lt;poster@example.com&gt;" date="1136214245" subject="[1/1] - &quot;big.buck.bunny.mkv&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="1024" number="1">synthetic-part-1@example.com</segment></segments>
  </file>
</nzb>`

// testSchema creates an isolated schema and a pool scoped to it, leaving real tables untouched.
func testSchema(t *testing.T) *pgxpool.Pool {
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
	schema := "downloads_test_" + strings.ToLower(rand.Text())
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

func TestManagerMigrationsAndDuplicateRelease(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()

	// Concurrent startup on a fresh schema must apply every migration exactly once.
	var starts sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		starts.Add(1)
		go func() {
			defer starts.Done()
			_, err := downloads.New(ctx, pool, downloads.Config{Directory: directory})
			errs <- err
		}()
	}
	starts.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent New: %v", err)
		}
	}
	if _, err := downloads.New(ctx, pool, downloads.Config{Directory: directory}); err != nil {
		t.Fatalf("New after migrations (idempotence): %v", err)
	}
	var migrations, version int
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(max(version), 0) FROM schema_migrations`).Scan(&migrations, &version); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if migrations != 6 || version != 6 {
		t.Fatalf("schema_migrations has %d rows and version %d, want migrations 1-6 applied once", migrations, version)
	}
	if _, err := pool.Exec(ctx, `SELECT 1 FROM downloads LIMIT 1`); err != nil {
		t.Fatalf("downloads table is missing: %v", err)
	}
	if _, err := pool.Exec(ctx, `SELECT 1 FROM settings LIMIT 1`); err != nil {
		t.Fatalf("settings table is missing: %v", err)
	}

	// An unconfigured source still starts, lists, and reports ErrNotConfigured.
	idle, err := downloads.New(ctx, pool, downloads.Config{Directory: directory})
	if err != nil {
		t.Fatalf("New without sources: %v", err)
	}
	if _, err := idle.Search(ctx, "dune"); !errors.Is(err, downloads.ErrNotConfigured) {
		t.Fatalf("Search without an indexer: got %v, want ErrNotConfigured", err)
	}
	if err := idle.TestIndexer(ctx); !errors.Is(err, downloads.ErrNotConfigured) {
		t.Fatalf("TestIndexer without an indexer: got %v, want ErrNotConfigured", err)
	}
	if err := idle.TestUsenet(ctx); !errors.Is(err, downloads.ErrNotConfigured) {
		t.Fatalf("TestUsenet without a provider: got %v, want ErrNotConfigured", err)
	}
	if _, err := idle.Add(ctx, "release-one", "Title"); !errors.Is(err, downloads.ErrNotConfigured) {
		t.Fatalf("Add without an indexer: got %v, want ErrNotConfigured", err)
	}
	if jobs, err := idle.List(ctx); err != nil || jobs == nil || len(jobs) != 0 {
		t.Fatalf("List without sources: got %+v, %v", jobs, err)
	}
	if _, err := idle.Get(ctx, "missing-job"); !errors.Is(err, downloads.ErrNotFound) {
		t.Fatalf("Get of a missing job: got %v, want ErrNotFound", err)
	}

	// Storage failures never expose the connection string.
	broken, err := pgxpool.New(ctx, "postgres://tester:supersecret@127.0.0.1:1/downloads")
	if err != nil {
		t.Fatalf("cannot build an unreachable pool: %v", err)
	}
	defer broken.Close()
	if _, err := downloads.New(ctx, broken, downloads.Config{Directory: t.TempDir()}); err == nil {
		t.Fatal("New with an unreachable database succeeded")
	} else {
		for _, leak := range []string{"supersecret", "127.0.0.1", "postgres://"} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("database error leaks %q: %v", leak, err)
			}
		}
	}

	var nzbRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nzbRequests++
		query := r.URL.Query()
		if query.Get("t") != "get" || query.Get("id") != "duplicate-release_1" || query.Get("apikey") != testAPIKey {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZB)
	}))
	defer server.Close()
	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: directory,
	})
	if err != nil {
		t.Fatalf("New with a synthetic indexer: %v", err)
	}

	first, err := manager.Add(ctx, "duplicate-release_1", "Big Buck Bunny 2008 1080p")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	second, err := manager.Add(ctx, "duplicate-release_1", "Big Buck Bunny 2008 1080p")
	if err != nil {
		t.Fatalf("Add duplicate: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("duplicate add created %s and %s, want one job", first.ID, second.ID)
	}
	if nzbRequests != 1 {
		t.Fatalf("the NZB was fetched %d times, want 1 for a duplicate release", nzbRequests)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM downloads WHERE release_id = $1`, "duplicate-release_1").Scan(&rows); err != nil {
		t.Fatalf("count duplicate rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("release stored %d times, want 1", rows)
	}
	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT nzb FROM downloads WHERE id = $1`, first.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored NZB: %v", err)
	}
	if string(stored) != testNZB {
		t.Fatalf("stored NZB differs from the fetched document")
	}

	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal job: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("job JSON is invalid: %s", encoded)
	}
	for _, key := range []string{"id", "releaseId", "title", "status", "bytesDone", "bytesTotal",
		"segmentsDone", "segmentsTotal", "missingSegments", "createdAt", "updatedAt", "files"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("job JSON is missing %q: %s", key, encoded)
		}
	}
	if _, ok := fields["error"]; ok {
		t.Fatalf("job JSON includes an empty error: %s", encoded)
	}
	if string(fields["files"]) != "[]" {
		t.Fatalf("empty files = %s, want []", fields["files"])
	}
	if first.Status != "queued" || first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Fatalf("new job = %+v, want a queued job with timestamps", first)
	}

	for name, input := range map[string][2]string{
		"unsafe release ID":    {"bad id!", "Title"},
		"blank title":          {"safe-release", "   "},
		"oversized title":      {"safe-release", strings.Repeat("x", 301)},
		"oversized release ID": {strings.Repeat("a", 129), "Title"},
	} {
		if _, err := manager.Add(ctx, input[0], input[1]); !errors.Is(err, downloads.ErrInvalid) {
			t.Fatalf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	if nzbRequests != 1 {
		t.Fatalf("an invalid add reached the indexer: %d requests", nzbRequests)
	}

	// A restart keeps the job and its persisted NZB.
	restarted, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: directory,
	})
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	reloaded, err := restarted.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if reloaded.ID != first.ID || reloaded.ReleaseID != first.ReleaseID || reloaded.Status != "queued" || reloaded.Title != first.Title {
		t.Fatalf("reloaded job = %+v, want the original queued job", reloaded)
	}
	listed, err := restarted.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != first.ID {
		t.Fatalf("List after restart = %+v, %v", listed, err)
	}

	// List returns only the newest 50 jobs.
	const extra = 55
	for i := 0; i < extra; i++ {
		id := fmt.Sprintf("extra-job-%02d", i)
		if _, err := pool.Exec(ctx,
			`INSERT INTO downloads (id, release_id, title, nzb, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, now() + ($5 * interval '1 second'), now())`,
			id, "release-"+id, "Extra "+id, []byte(testNZB), i); err != nil {
			t.Fatalf("insert extra job: %v", err)
		}
	}
	listed, err = restarted.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 50 || listed[0].ID != fmt.Sprintf("extra-job-%02d", extra-1) {
		t.Fatalf("List returned %d jobs starting at %s, want the newest 50", len(listed), listed[0].ID)
	}
}

func TestManagerRetryAndInterruptedRequeue(t *testing.T) {
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
	job, err := manager.Add(ctx, "interrupted-release", "Interrupted release")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := manager.Retry(ctx, job.ID); !errors.Is(err, downloads.ErrConflict) {
		t.Fatalf("Retry of a queued job: got %v, want ErrConflict", err)
	}
	completed, err := manager.Add(ctx, "completed-release", "Completed release")
	if err != nil {
		t.Fatalf("Add completed: %v", err)
	}
	files, err := json.Marshal([]downloads.OutputFile{{
		Name: "movie.mkv", Size: 4,
		URL: "/api/v1/downloads/" + completed.ID + "/file?name=movie.mkv",
	}})
	if err != nil {
		t.Fatalf("marshal output files: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE downloads SET status = 'completed', files = $2::jsonb WHERE id = $1`,
		completed.ID, string(files)); err != nil {
		t.Fatalf("mark completed: %v", err)
	}
	// Simulate a download interrupted mid-transfer with persisted progress.
	if _, err := pool.Exec(ctx,
		`UPDATE downloads SET status = 'downloading', bytes_done = 4096, bytes_total = 8192,
		 segments_done = 3, segments_total = 10, missing_segments = 1 WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("simulate interruption: %v", err)
	}

	// A restarted manager requeues interrupted work and fails it without Usenet configured.
	restarted, err := downloads.New(ctx, pool, downloads.Config{Directory: directory})
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}
	t.Cleanup(restarted.Close)
	restarted.Start(context.Background())
	waitForStatus(t, ctx, pool, job.ID, "failed")
	closed := make(chan struct{})
	go func() {
		restarted.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not stop the coordinator")
	}

	failed, err := restarted.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("Get after requeue: %v", err)
	}
	if failed.Status != "failed" || failed.Error == "" {
		t.Fatalf("interrupted job = %+v, want a failed job with an error", failed)
	}
	if !strings.Contains(failed.Error, "not configured") {
		t.Fatalf("failure message %q is not actionable", failed.Error)
	}
	if failed.BytesDone != 4096 || failed.BytesTotal != 8192 || failed.SegmentsDone != 3 ||
		failed.SegmentsTotal != 10 || failed.MissingSegments != 1 {
		t.Fatalf("interrupted progress was not preserved: %+v", failed)
	}
	untouched, err := restarted.Get(ctx, completed.ID)
	if err != nil {
		t.Fatalf("Get completed job: %v", err)
	}
	if untouched.Status != "completed" || len(untouched.Files) != 1 {
		t.Fatalf("completed job was disturbed: %+v", untouched)
	}

	retried, err := restarted.Retry(ctx, job.ID)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if retried.Status != "queued" || retried.Error != "" || retried.ID != job.ID || retried.ReleaseID != job.ReleaseID {
		t.Fatalf("retried job = %+v, want the same queued job without an error", retried)
	}
	if _, err := restarted.Retry(ctx, job.ID); !errors.Is(err, downloads.ErrConflict) {
		t.Fatalf("Retry of a queued job: got %v, want ErrConflict", err)
	}
	if _, err := restarted.Retry(ctx, completed.ID); !errors.Is(err, downloads.ErrConflict) {
		t.Fatalf("Retry of a completed job: got %v, want ErrConflict", err)
	}
	if _, err := restarted.Retry(ctx, "missing-job"); !errors.Is(err, downloads.ErrNotFound) {
		t.Fatalf("Retry of a missing job: got %v, want ErrNotFound", err)
	}
	var nzb []byte
	if err := pool.QueryRow(ctx, `SELECT nzb FROM downloads WHERE id = $1`, job.ID).Scan(&nzb); err != nil {
		t.Fatalf("read retried NZB: %v", err)
	}
	if string(nzb) != testNZB {
		t.Fatal("retry changed the stored NZB")
	}
}

func TestManagerOpenFileGuards(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	manager, err := downloads.New(ctx, pool, downloads.Config{Directory: directory})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const jobID = "completed-file-job"
	outputDir := filepath.Join(directory, "downloads", jobID, "output")
	if err := os.MkdirAll(filepath.Join(outputDir, "Movie"), 0o700); err != nil {
		t.Fatalf("create output: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "Movie", "movie.mkv"), []byte("data"), 0o600); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "empty.mkv"), nil, 0o600); err != nil {
		t.Fatalf("write empty media: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "secret.mkv"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	files, err := json.Marshal([]downloads.OutputFile{
		{Name: "Movie/movie.mkv", Size: 4, URL: "/api/v1/downloads/" + jobID + "/file?name=Movie%2Fmovie.mkv"},
		{Name: "empty.mkv", Size: 0, URL: "/api/v1/downloads/" + jobID + "/file?name=empty.mkv"},
		{Name: "missing.mkv", Size: 10, URL: "/api/v1/downloads/" + jobID + "/file?name=missing.mkv"},
	})
	if err != nil {
		t.Fatalf("marshal output files: %v", err)
	}
	insertJob(t, ctx, pool, jobID, "file-release", "completed", string(files))
	insertJob(t, ctx, pool, "queued-file-job", "queued-file-release", "queued", "[]")

	file, err := manager.OpenFile(ctx, jobID, "Movie/movie.mkv")
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	content, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(content) != "data" {
		t.Fatalf("OpenFile content = %q, %v; want the declared media", content, err)
	}

	for name, tc := range map[string]struct {
		job  string
		file string
		want error
	}{
		"traversal":            {jobID, "../secret.mkv", downloads.ErrNotFound},
		"cleaned traversal":    {jobID, "Movie/../../secret.mkv", downloads.ErrNotFound},
		"absolute path":        {jobID, "/etc/hosts", downloads.ErrNotFound},
		"unknown entry":        {jobID, "other.mkv", downloads.ErrNotFound},
		"empty name":           {jobID, "", downloads.ErrNotFound},
		"declared but missing": {jobID, "missing.mkv", downloads.ErrNotFound},
		"declared but empty":   {jobID, "empty.mkv", downloads.ErrNotFound},
		"active job":           {"queued-file-job", "Movie/movie.mkv", downloads.ErrConflict},
		"unknown job":          {"no-such-job", "Movie/movie.mkv", downloads.ErrNotFound},
	} {
		handle, err := manager.OpenFile(ctx, tc.job, tc.file)
		if handle != nil {
			handle.Close()
		}
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestManagerLockBackendLossRecovery(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	server := newNZBServer(t)
	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: server.URL + "/api", APIKey: testAPIKey, Directory: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(manager.Close)
	first, err := manager.Add(ctx, "lock-loss-first", "First release")
	if err != nil {
		t.Fatalf("Add first: %v", err)
	}
	manager.Start(context.Background())
	waitForStatus(t, ctx, pool, first.ID, "failed")
	firstPID := waitForLockBackend(t, ctx, pool, 0)

	interrupted, err := manager.Add(ctx, "lock-loss-interrupted", "Interrupted release")
	if err != nil {
		t.Fatalf("Add interrupted: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE downloads SET status = 'downloading', bytes_done = 2048, bytes_total = 4096,
		 segments_done = 2, segments_total = 8 WHERE id = $1`, interrupted.ID); err != nil {
		t.Fatalf("simulate interruption: %v", err)
	}

	// Forcing the lock backend down must not strand the coordinator on a dead connection.
	var terminated bool
	if err := pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, firstPID).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate lock backend %d: %v (terminated %v)", firstPID, err, terminated)
	}
	secondPID := waitForLockBackend(t, ctx, pool, firstPID)
	if secondPID == firstPID {
		t.Fatalf("lock backend PID stayed %d", firstPID)
	}
	// A stuck active job can only fail here after the new session requeues it.
	waitForStatus(t, ctx, pool, interrupted.ID, "failed")
	job, err := manager.Get(ctx, interrupted.ID)
	if err != nil {
		t.Fatalf("Get interrupted: %v", err)
	}
	if job.BytesDone != 2048 || job.BytesTotal != 4096 || job.SegmentsDone != 2 || job.SegmentsTotal != 8 {
		t.Fatalf("interrupted progress was not preserved after recovery: %+v", job)
	}
	reloaded, err := manager.Get(ctx, first.ID)
	if err != nil || reloaded.Status != "failed" {
		t.Fatalf("first job changed after recovery: %+v, %v", reloaded, err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM downloads`).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("job rows = %d, %v; want 2 without duplicates", rows, err)
	}

	closed := make(chan struct{})
	go func() {
		manager.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not stop the coordinator")
	}
	var locks int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks JOIN pg_stat_activity USING (pid) WHERE locktype = 'advisory'
	 AND application_name = current_setting('application_name')
	 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&locks); err != nil {
		t.Fatalf("count advisory locks: %v", err)
	}
	if locks != 0 {
		t.Fatalf("%d advisory locks remain after Close", locks)
	}
}

func waitForLockBackend(t *testing.T, ctx context.Context, pool *pgxpool.Pool, exclude int) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var pid int
		err := pool.QueryRow(ctx,
			`SELECT pid FROM pg_locks JOIN pg_stat_activity USING (pid) WHERE locktype = 'advisory' AND pid <> $1
			 AND application_name = current_setting('application_name')
			 AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
			 ORDER BY pid LIMIT 1`, exclude).Scan(&pid)
		if err == nil {
			return pid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("read the lock backend: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the coordinator never held an advisory lock")
	return 0
}

func insertJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, releaseID, status, files string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb, status, files) VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		id, releaseID, "Synthetic "+id, []byte(testNZB), status, files); err != nil {
		t.Fatalf("insert job %s: %v", id, err)
	}
}

func newNZBServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("t") != "get" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, testNZB)
	}))
	t.Cleanup(server.Close)
	return server
}

func waitForStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, status string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var current string
		if err := pool.QueryRow(ctx, `SELECT status FROM downloads WHERE id = $1`, id).Scan(&current); err != nil {
			t.Fatalf("read job status: %v", err)
		}
		if current == status {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("job %s never reached status %q", id, status)
}
