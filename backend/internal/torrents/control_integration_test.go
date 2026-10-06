package torrents_test

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/time/rate"

	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
	"github.com/IvanPopov200/Constellarr/backend/internal/transferpolicy"
)

// The shared controller must satisfy the engine's policy surface.
var _ torrents.Policy = (*transferpolicy.Controller)(nil)

// fakePolicy drives holds and throttling without the persisted policy table.
type fakePolicy struct {
	mu      sync.Mutex
	allowed bool
	limiter *rate.Limiter
}

func newFakePolicy(allowed bool, limit rate.Limit) *fakePolicy {
	return &fakePolicy{allowed: allowed, limiter: rate.NewLimiter(limit, 32<<10)}
}

func (p *fakePolicy) Allowed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allowed
}

func (p *fakePolicy) Limiter() *rate.Limiter { return p.limiter }

func (p *fakePolicy) Snapshot() transferpolicy.Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	reason := "test window allows transfers"
	if !p.allowed {
		reason = "test window is paused"
	}
	return transferpolicy.Snapshot{Effective: transferpolicy.Effective{Paused: !p.allowed, Reason: reason}}
}

func (p *fakePolicy) setAllowed(allowed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowed = allowed
}

func newPolicyService(t *testing.T, pool *pgxpool.Pool, directory string, policy torrents.Policy) *torrents.Service {
	t.Helper()
	service, err := torrents.New(context.Background(), pool,
		torrents.Options{Directory: directory, Testing: true, Policy: policy})
	if err != nil {
		t.Fatalf("cannot create the torrent service: %v", err)
	}
	service.Start(context.Background())
	t.Cleanup(service.Close)
	return service
}

// newRealController builds the shared policy controller over the test schema's policy table.
func newRealController(t *testing.T, pool *pgxpool.Pool, ctx context.Context) *transferpolicy.Controller {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "downloads", "migrations", "016_download_policy.sql"))
	if err != nil {
		t.Fatalf("cannot read the policy migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("cannot create the policy table: %v", err)
	}
	controller, err := transferpolicy.New(ctx, pool)
	if err != nil {
		t.Fatalf("cannot create the policy controller: %v", err)
	}
	go controller.Run(ctx)
	return controller
}

func assertHeld(t *testing.T, service *torrents.Service, id string) {
	t.Helper()
	current := jobByID(t, service, id)
	if current.Status != "queued" || !strings.HasPrefix(current.Error, "paused by the transfer policy") {
		t.Fatalf("job %s must be queued with a visible hold reason, got %s %q", id, current.Status, current.Error)
	}
}

func peerFor(t *testing.T, port int) string {
	t.Helper()
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)).String()
}

// TestPolicyHoldQueuesAndReleases covers a hold from queueing through a mid-transfer detach to seeding.
func TestPolicyHoldQueuesAndReleases(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	policy := newFakePolicy(false, 128<<10)
	service := newPolicyService(t, pool, t.TempDir(), policy)
	updateSeedPolicy(t, service, 1, 0)

	seedDir := t.TempDir()
	torrentBytes, payload := buildTorrent(t, seedDir, "tiny.bin", 512<<10)
	port := seeder(t, seedDir, torrentBytes)
	peer := peerFor(t, port)

	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatalf("cannot add torrent: %v", err)
	}
	// Held jobs stay queued with the policy reason and never attach, so no metadata or data is fetched.
	waitFor(t, 10*time.Second, "the hold reason on the new job", func() bool {
		current := jobByID(t, service, job.ID)
		return current.Status == "queued" && strings.HasPrefix(current.Error, "paused by the transfer policy")
	})
	if health := service.Health(ctx); health.ActiveJobs != 0 {
		t.Fatalf("a held job must not attach, health = %+v", health)
	}

	policy.setAllowed(true)
	waitFor(t, 60*time.Second, "a partial download", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peer})
		current := jobByID(t, service, job.ID)
		if current.Status == "failed" {
			t.Fatalf("released transfer failed: %s", current.Error)
		}
		return current.BytesDone > 0 && current.BytesDone < current.BytesTotal
	})

	// A hold during the download detaches the run and requeues it without losing progress.
	policy.setAllowed(false)
	waitFor(t, 10*time.Second, "the transfer to be held", func() bool {
		current := jobByID(t, service, job.ID)
		return current.Status == "queued" && strings.HasPrefix(current.Error, "paused by the transfer policy")
	})
	var heldBytes int64
	if err := pool.QueryRow(ctx, `SELECT bytes_done FROM torrent_jobs WHERE id = $1`, job.ID).Scan(&heldBytes); err != nil {
		t.Fatal(err)
	}
	if heldBytes == 0 {
		t.Fatalf("held job must keep its progress, got %d", heldBytes)
	}
	if health := service.Health(ctx); health.ActiveJobs != 0 {
		t.Fatalf("held transfer must detach, health = %+v", health)
	}

	// Releasing the policy finishes the transfer and clears the hold reason.
	policy.setAllowed(true)
	waitFor(t, 60*time.Second, "the released transfer to seed", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peer})
		current := jobByID(t, service, job.ID)
		if current.Status == "failed" {
			t.Fatalf("released transfer failed: %s", current.Error)
		}
		return current.Status == "seeding"
	})
	if current := jobByID(t, service, job.ID); current.Error != "" {
		t.Fatalf("starting the job must clear the hold reason, got %q", current.Error)
	}

	// A hold while seeding keeps the completed payload seeding under the upload cap.
	policy.setAllowed(false)
	time.Sleep(2500 * time.Millisecond)
	current := jobByID(t, service, job.ID)
	if current.Status != "seeding" {
		t.Fatalf("already complete seeding must survive a hold, got %s %q", current.Status, current.Error)
	}
	if health := service.Health(ctx); health.ActiveJobs != 1 {
		t.Fatalf("the seeding run must stay attached during a hold, health = %+v", health)
	}
	policy.setAllowed(true)

	// The payload stays importable through the whole cycle.
	assertDownloaded(t, service, job.ID, "tiny.bin", payload)
}

// TestManualPauseSurvivesPolicyAndKeepsCapacity covers pause/hold/resume ordering and queue capacity.
func TestManualPauseSurvivesPolicyAndKeepsCapacity(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	policy := newFakePolicy(true, rate.Inf)
	service := newPolicyService(t, pool, t.TempDir(), policy)
	updateSeedPolicy(t, service, 1, 0)
	if _, err := service.UpdateSettings(ctx, torrents.SettingsUpdate{
		MaxActiveJobs: 1, DHTEnabled: true, PEXEnabled: true, SeedRatioLimit: 1,
	}); err != nil {
		t.Fatalf("cannot limit active jobs: %v", err)
	}

	seedDir := t.TempDir()
	first, _ := buildTorrent(t, seedDir, "first.bin", 128<<10)
	second, _ := buildTorrent(t, seedDir, "second.bin", 128<<10)
	port := seeder(t, seedDir, first)
	firstPeer := peerFor(t, port)

	paused, err := service.Add(ctx, torrents.AddInput{Torrent: first, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the first job to attach", func() bool {
		_ = service.AddPeers(ctx, paused.ID, []string{firstPeer})
		return service.Health(ctx).ActiveJobs == 1
	})
	if _, err := service.Pause(ctx, paused.ID); err != nil {
		t.Fatalf("cannot pause: %v", err)
	}
	if bridge, err := service.DownloadJob(ctx, paused.ID); err != nil || bridge.Status != "paused" {
		t.Fatalf("the bridge must report paused, got %+v (%v)", bridge, err)
	}

	// The paused run must not consume the single active slot, so the second job starts.
	active, err := service.Add(ctx, torrents.AddInput{Torrent: second, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "the second job to start past the paused first", func() bool {
		return service.Health(ctx).ActiveJobs == 2
	})

	// A hold and release must not convert the manual pause into an automatic resume.
	policy.setAllowed(false)
	waitFor(t, 10*time.Second, "the hold to queue the active job", func() bool {
		current := jobByID(t, service, active.ID)
		return current.Status == "queued" && strings.HasPrefix(current.Error, "paused by the transfer policy")
	})
	policy.setAllowed(true)
	waitFor(t, 30*time.Second, "the active job to restart", func() bool {
		return jobByID(t, service, active.ID).Status != "queued"
	})
	if current := jobByID(t, service, paused.ID); current.Status != "paused" {
		t.Fatalf("manual pause must survive the policy cycle, got %s", current.Status)
	}

	// The explicit resume is the only way back.
	resumed, err := service.Resume(ctx, paused.ID)
	if err != nil {
		t.Fatalf("cannot resume a paused job: %v", err)
	}
	if resumed.Status != "downloading" && resumed.Status != "metadata" && resumed.Status != "checking" {
		t.Fatalf("unexpected resumed status %s", resumed.Status)
	}
	waitFor(t, 60*time.Second, "the resumed job to finish", func() bool {
		_ = service.AddPeers(ctx, paused.ID, []string{firstPeer})
		current := jobByID(t, service, paused.ID)
		return current.Status == "seeding" || current.Status == "completed"
	})
}

// TestCancelStopsTransferAndSurvivesRestart covers cancel, idempotency, restart and explicit resume.
func TestCancelStopsTransferAndSurvivesRestart(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	policy := newFakePolicy(true, 192<<10)
	service := newPolicyService(t, pool, directory, policy)
	updateSeedPolicy(t, service, 1, 0)

	seedDir := t.TempDir()
	torrentBytes, payload := buildTorrent(t, seedDir, "tiny.bin", 1<<20)
	port := seeder(t, seedDir, torrentBytes)
	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	peer := peerFor(t, port)
	waitFor(t, 60*time.Second, "a partial download", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peer})
		current := jobByID(t, service, job.ID)
		return current.BytesDone > 0 && current.BytesDone < current.BytesTotal
	})

	cancelled, err := service.Cancel(ctx, job.ID)
	if err != nil {
		t.Fatalf("cannot cancel: %v", err)
	}
	if cancelled.Status != "cancelled" || cancelled.Error != "" {
		t.Fatalf("unexpected cancelled job: %+v", cancelled)
	}
	bridge, err := service.DownloadJob(ctx, job.ID)
	if err != nil || bridge.Status != "cancelled" {
		t.Fatalf("the bridge must report cancelled, got %+v (%v)", bridge, err)
	}
	again, err := service.Cancel(ctx, job.ID)
	if err != nil || again.Status != "cancelled" {
		t.Fatalf("cancel must be idempotent, got %+v (%v)", again, err)
	}
	waitFor(t, 10*time.Second, "the engine to detach the cancelled job", func() bool {
		return service.Health(ctx).ActiveJobs == 0
	})

	// Data and resume history survive the cancellation.
	dataDir := filepath.Join(directory, "torrents", job.InfoHash)
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("cancelled data directory missing: %v", err)
	}
	var bytesDone int64
	var storedStatus string
	if err := pool.QueryRow(ctx, `SELECT bytes_done, status FROM torrent_jobs WHERE id = $1`, job.ID).
		Scan(&bytesDone, &storedStatus); err != nil {
		t.Fatalf("cannot read the cancelled row: %v", err)
	}
	if storedStatus != "cancelled" || bytesDone == 0 {
		t.Fatalf("cancel must keep history, got %s %d", storedStatus, bytesDone)
	}

	// A restart never resumes a cancelled job on its own.
	service.Close()
	restarted := newPolicyService(t, pool, directory, policy)
	time.Sleep(2500 * time.Millisecond)
	if current := jobByID(t, restarted, job.ID); current.Status != "cancelled" {
		t.Fatalf("restart must not resume a cancelled job, got %s", current.Status)
	}
	if health := restarted.Health(ctx); health.ActiveJobs != 0 {
		t.Fatalf("cancelled job attached after restart: %+v", health)
	}

	// Only the explicit resume requeues it, and the transfer completes from the preserved payload.
	resumed, err := restarted.Resume(ctx, job.ID)
	if err != nil {
		t.Fatalf("cannot resume a cancelled job: %v", err)
	}
	if resumed.Status != "queued" {
		t.Fatalf("resume must requeue, got %s", resumed.Status)
	}
	waitFor(t, 60*time.Second, "the resumed job to complete", func() bool {
		_ = restarted.AddPeers(ctx, job.ID, []string{peer})
		current := jobByID(t, restarted, job.ID)
		if current.Status == "failed" {
			t.Fatalf("resumed job failed: %s", current.Error)
		}
		return current.Status == "seeding" || current.Status == "completed"
	})
	assertDownloaded(t, restarted, job.ID, "tiny.bin", payload)
}

// TestRealPolicyControllerHoldsDownloads exercises the shared controller itself, not a fake.
func TestRealPolicyControllerHoldsDownloads(t *testing.T) {
	pool := testSchema(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := newRealController(t, pool, ctx)
	if _, err := controller.SetPaused(ctx, true); err != nil {
		t.Fatalf("cannot pause the policy: %v", err)
	}

	seedDir := t.TempDir()
	torrentBytes, payload := buildTorrent(t, seedDir, "tiny.bin", 64<<10)
	port := seeder(t, seedDir, torrentBytes)
	service := newPolicyService(t, pool, t.TempDir(), controller)
	updateSeedPolicy(t, service, 0, 0)

	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	// The controller's own reason must reach the queued job.
	waitFor(t, 10*time.Second, "the controller reason on the queued job", func() bool {
		current := jobByID(t, service, job.ID)
		return current.Status == "queued" && strings.Contains(current.Error, "paused manually")
	})

	if _, err := controller.SetPaused(ctx, false); err != nil {
		t.Fatalf("cannot release the policy: %v", err)
	}
	peer := peerFor(t, port)
	waitFor(t, 60*time.Second, "the released transfer to finish", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peer})
		current := jobByID(t, service, job.ID)
		if current.Status == "failed" {
			t.Fatalf("released transfer failed: %s", current.Error)
		}
		return current.Status == "completed"
	})
	assertDownloaded(t, service, job.ID, "tiny.bin", payload)
}

// TestRealPolicyPausedStartKeepsEngineReady covers a process that starts under a paused policy.
func TestRealPolicyPausedStartKeepsEngineReady(t *testing.T) {
	pool := testSchema(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := newRealController(t, pool, ctx)
	if _, err := controller.SetPaused(ctx, true); err != nil {
		t.Fatalf("cannot pause the policy: %v", err)
	}

	seedDir := t.TempDir()
	torrentBytes, payload := buildTorrent(t, seedDir, "tiny.bin", 64<<10)
	port := seeder(t, seedDir, torrentBytes)
	service := newPolicyService(t, pool, t.TempDir(), controller)
	updateSeedPolicy(t, service, 1, 0)

	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	// A paused start must still build the clients and leave the engine healthy and ticking.
	waitFor(t, 10*time.Second, "the engine to be ready while paused", func() bool {
		health := service.Health(ctx)
		return health.OK && health.ListenPort != 0
	})
	if rate := controller.Limiter().Limit(); rate <= 0 {
		t.Fatalf("paused policy left an unusable limiter rate: %v", rate)
	}
	waitFor(t, 10*time.Second, "the controller reason on the queued job", func() bool {
		current := jobByID(t, service, job.ID)
		return current.Status == "queued" && strings.Contains(current.Error, "paused manually")
	})

	if _, err := controller.SetPaused(ctx, false); err != nil {
		t.Fatalf("cannot release the policy: %v", err)
	}
	peer := peerFor(t, port)
	waitFor(t, 60*time.Second, "the released transfer to seed", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peer})
		current := jobByID(t, service, job.ID)
		if current.Status == "failed" {
			t.Fatalf("released transfer failed: %s", current.Error)
		}
		return current.Status == "seeding"
	})

	// A hold while seeding keeps the run attached and the engine ticking with a usable rate.
	before := jobByID(t, service, job.ID).UpdatedAt
	if _, err := controller.SetPaused(ctx, true); err != nil {
		t.Fatalf("cannot hold the policy: %v", err)
	}
	waitFor(t, 10*time.Second, "the seeding run to survive the hold", func() bool {
		return jobByID(t, service, job.ID).Status == "seeding" && service.Health(ctx).ActiveJobs == 1
	})
	if rate := controller.Limiter().Limit(); rate <= 0 {
		t.Fatalf("paused policy left an unusable limiter rate: %v", rate)
	}
	waitFor(t, 10*time.Second, "the engine to keep persisting seeding progress", func() bool {
		return jobByID(t, service, job.ID).UpdatedAt.After(before)
	})
	if health := service.Health(ctx); !health.OK {
		t.Fatalf("engine reported an error under a paused policy: %+v", health)
	}
	assertDownloaded(t, service, job.ID, "tiny.bin", payload)
}

// TestMetadataDeadlineSurvivesHold covers a magnet that outlived added_at while a hold was active.
func TestMetadataDeadlineSurvivesHold(t *testing.T) {
	pool := testSchema(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	controller := newRealController(t, pool, ctx)
	if _, err := controller.SetPaused(ctx, true); err != nil {
		t.Fatalf("cannot pause the policy: %v", err)
	}
	service := newPolicyService(t, pool, t.TempDir(), controller)

	job, err := service.Add(ctx, torrents.AddInput{
		Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("1b", 20), Source: "magnet",
	})
	if err != nil {
		t.Fatalf("cannot add the magnet: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE torrent_jobs SET added_at = now() - interval '2 hours' WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("cannot age the job: %v", err)
	}
	if _, err := controller.SetPaused(ctx, false); err != nil {
		t.Fatalf("cannot release the policy: %v", err)
	}
	// The wait belongs to this run, so an aged row must still wait rather than fail on release.
	waitFor(t, 30*time.Second, "the released magnet to wait for metadata", func() bool {
		current := jobByID(t, service, job.ID)
		if current.Status == "failed" {
			t.Fatalf("an aged magnet failed on release: %s", current.Error)
		}
		return current.Status == "metadata"
	})
	time.Sleep(2 * time.Second)
	if current := jobByID(t, service, job.ID); current.Status != "metadata" {
		t.Fatalf("a held metadata wait must keep waiting, got %s %q", current.Status, current.Error)
	}
}

// TestCancelRacesAttach cancels jobs while the engine is attaching them; cancelled stays terminal.
func TestCancelRacesAttach(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	policy := newFakePolicy(true, rate.Inf)
	service := newPolicyService(t, pool, t.TempDir(), policy)
	updateSeedPolicy(t, service, 0, 0)

	seedDir := t.TempDir()
	ids := make([]string, 0, 4)
	for index := 0; index < 4; index++ {
		torrentBytes, _ := buildTorrent(t, seedDir, "race"+string(rune('a'+index))+".bin", 64<<10)
		port := seeder(t, seedDir, torrentBytes)
		job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, job.ID)
		peer := peerFor(t, port)
		var wg sync.WaitGroup
		wg.Add(2)
		go func(id string) {
			defer wg.Done()
			if _, err := service.Cancel(ctx, id); err != nil {
				t.Errorf("cancel failed: %v", err)
			}
		}(job.ID)
		go func(id, peer string) {
			defer wg.Done()
			_ = service.AddPeers(ctx, id, []string{peer})
		}(job.ID, peer)
		wg.Wait()
	}

	waitFor(t, 15*time.Second, "every cancelled job to detach", func() bool {
		return service.Health(ctx).ActiveJobs == 0
	})
	time.Sleep(2 * time.Second)
	for _, id := range ids {
		current := jobByID(t, service, id)
		if current.Status != "cancelled" {
			t.Fatalf("job %s revived after cancel: %s %q", id, current.Status, current.Error)
		}
		if bridge, err := service.DownloadJob(ctx, id); err != nil || bridge.Status != "cancelled" {
			t.Fatalf("bridge status for %s = %+v (%v)", id, bridge, err)
		}
	}
}
