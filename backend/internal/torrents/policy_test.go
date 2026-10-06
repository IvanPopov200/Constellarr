package torrents

import (
	"context"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type stubPolicy struct {
	allowed bool
	limiter *rate.Limiter
}

func (p *stubPolicy) Allowed() bool          { return p.allowed }
func (p *stubPolicy) Limiter() *rate.Limiter { return p.limiter }

func TestPolicyLimiterReplacesLocalDownloadLimit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	limiter := rate.NewLimiter(500_000, 1<<20)
	service, err := New(ctx, pool, Options{Directory: t.TempDir(), Testing: true,
		Policy: &stubPolicy{allowed: true, limiter: limiter}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	if service.downloadLimiter != limiter || !service.sharedLimiter {
		t.Fatalf("the shared policy limiter must replace the local download limiter")
	}
	for _, private := range []bool{false, true} {
		if cfg := service.newClientConfig(private, 0); cfg.DownloadRateLimiter != limiter {
			t.Fatalf("private=%v must share the policy limiter", private)
		}
	}
	if _, err := service.UpdateSettings(ctx, SettingsUpdate{
		MaxActiveJobs: 1, DHTEnabled: true, PEXEnabled: true, DownloadLimitKBps: 512, UploadLimitKBps: 256,
	}); err != nil {
		t.Fatal(err)
	}
	if limiter.Limit() != 500_000 {
		t.Fatalf("torrent settings changed the shared limiter: %v", limiter.Limit())
	}
	if service.uploadLimiter.Limit() != rate.Limit(256*1024) {
		t.Fatalf("upload limit must still apply: %v", service.uploadLimiter.Limit())
	}
}

func TestStandaloneServiceKeepsLocalDownloadLimit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	service, err := New(ctx, pool, Options{Directory: t.TempDir(), Testing: true, Policy: (*stubPolicy)(nil)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	if service.policy != nil || service.sharedLimiter {
		t.Fatalf("a nil policy must keep the standalone engine")
	}
	if cfg := service.newClientConfig(false, 0); cfg.DownloadRateLimiter != service.downloadLimiter {
		t.Fatal("standalone clients must share the local download limiter")
	}
	if _, err := service.UpdateSettings(ctx, SettingsUpdate{
		MaxActiveJobs: 1, DHTEnabled: true, PEXEnabled: true, DownloadLimitKBps: 128,
	}); err != nil {
		t.Fatal(err)
	}
	if service.downloadLimiter.Limit() != rate.Limit(128*1024) {
		t.Fatalf("standalone settings must apply: %v", service.downloadLimiter.Limit())
	}
}

// A metadata wait must follow the run's own start and pause time, not the job's added_at.
func TestMetadataDeadlineTracksRunActiveTime(t *testing.T) {
	run := &jobRun{metadataDeadline: time.Now().Add(-time.Minute)}
	if !run.metadataExpired(time.Now()) {
		t.Fatal("a stale metadata deadline must expire")
	}
	run.refreshMetadataDeadline(time.Now())
	if run.metadataExpired(time.Now()) {
		t.Fatal("a refreshed metadata deadline must stay open")
	}
}

// TestTorrentCancelMigration proves 018 extends the 011 status CHECK with cancelled while still refusing junk.
func TestTorrentCancelMigration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var definition string
	err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = 'torrent_jobs'::regclass
		AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%status%'`).Scan(&definition)
	if err != nil {
		t.Fatalf("cannot read the job status constraint: %v", err)
	}
	if !strings.Contains(definition, "cancelled") {
		t.Fatalf("the status constraint must accept cancelled: %s", definition)
	}
	hash := strings.Repeat("a", 40)
	if _, err := pool.Exec(ctx, `INSERT INTO torrent_jobs (id, info_hash, status) VALUES ('status-check', $1, $2)`,
		hash, statusCancelled); err != nil {
		t.Fatalf("cancelled must be insertable: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO torrent_jobs (id, info_hash, status) VALUES ('status-bogus', $1, 'bogus')`,
		strings.Repeat("b", 40)); err == nil {
		t.Fatal("an unknown status must be refused by the constraint")
	}
}
