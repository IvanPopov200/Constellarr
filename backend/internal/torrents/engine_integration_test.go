package torrents_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
)

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
	schema := "torrents_test_" + strings.ToLower(rand.Text())
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
	applySchema(t, ctx, pool)
	return pool
}

// applySchema runs the platform migrations that own the torrent tables.
func applySchema(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, name := range []string{"011_torrents.sql", "015_torrent_processing.sql"} {
		body, err := os.ReadFile(filepath.Join("..", "downloads", "migrations", name))
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("cannot apply %s: %v", name, err)
		}
	}
}

func newService(t *testing.T, pool *pgxpool.Pool, directory string) *torrents.Service {
	t.Helper()
	ctx := context.Background()
	service, err := torrents.New(ctx, pool, torrents.Options{Directory: directory, Testing: true})
	if err != nil {
		t.Fatalf("cannot create the torrent service: %v", err)
	}
	service.Start(context.Background())
	t.Cleanup(service.Close)
	return service
}

func updateSeedPolicy(t *testing.T, service *torrents.Service, ratio float64, minutes int) {
	t.Helper()
	if _, err := service.UpdateSettings(context.Background(), torrents.SettingsUpdate{
		ListenPort: 0, DHTEnabled: true, PEXEnabled: true, MaxActiveJobs: 3,
		SeedRatioLimit: ratio, SeedTimeLimitMinutes: minutes,
	}); err != nil {
		t.Fatalf("cannot update settings: %v", err)
	}
}

// buildTorrent writes payload into dir and returns a single-file torrent and its content.
func buildTorrent(t *testing.T, dir, name string, size int) ([]byte, []byte) {
	t.Helper()
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(path); err != nil {
		t.Fatalf("cannot build torrent info: %v", err)
	}
	return writeTorrent(t, info), payload
}

func writeTorrent(t *testing.T, info metainfo.Info) []byte {
	t.Helper()
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("cannot encode torrent info: %v", err)
	}
	var buf bytes.Buffer
	meta := metainfo.MetaInfo{InfoBytes: raw}
	if err := meta.Write(&buf); err != nil {
		t.Fatalf("cannot write torrent: %v", err)
	}
	return buf.Bytes()
}

func infoHashOf(t *testing.T, torrentBytes []byte) string {
	t.Helper()
	meta, err := metainfo.Load(bytes.NewReader(torrentBytes))
	if err != nil {
		t.Fatal(err)
	}
	return meta.HashInfoBytes().HexString()
}

// seeder starts a local-only client serving torrentBytes from dir and returns its port.
func seeder(t *testing.T, dir string, torrentBytes []byte) int {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dir
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.DisablePEX = true
	cfg.Seed = true
	cfg.ListenPort = 0
	cfg.ListenHost = torrent.LoopbackListenHost
	cfg.DisableIPv6 = true
	cfg.NoDefaultPortForwarding = true
	client, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatalf("cannot create the seeder client: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	meta, err := metainfo.Load(bytes.NewReader(torrentBytes))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := client.AddTorrent(meta)
	if err != nil {
		t.Fatalf("cannot add the seeder torrent: %v", err)
	}
	<-loaded.GotInfo()
	loaded.DownloadAll()
	deadline := time.Now().Add(20 * time.Second)
	for !loaded.Complete().Bool() {
		if time.Now().After(deadline) {
			t.Fatal("the seeder never verified its own data")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return client.LocalPort()
}

func waitFor(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func jobByID(t *testing.T, service *torrents.Service, id string) torrents.Job {
	t.Helper()
	detail, err := service.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("cannot load job: %v", err)
	}
	return detail.Job
}

func TestTwoPeerTransferSeedingAndRestart(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	service := newService(t, pool, directory)
	updateSeedPolicy(t, service, 1, 0)

	seedDir := t.TempDir()
	torrentBytes, payload := buildTorrent(t, seedDir, "tiny.bin", 128<<10)
	port := seeder(t, seedDir, torrentBytes)

	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatalf("cannot add torrent: %v", err)
	}
	if job.InfoHash != infoHashOf(t, torrentBytes) {
		t.Fatalf("unexpected info hash %q", job.InfoHash)
	}
	peer := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)).String()
	waitFor(t, 60*time.Second, "the transfer to complete", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peer})
		current := jobByID(t, service, job.ID)
		switch current.Status {
		case "failed":
			t.Fatalf("transfer failed: %s", current.Error)
		case "seeding", "completed":
			return true
		}
		return false
	})

	complete := jobByID(t, service, job.ID)
	if complete.Status != "seeding" {
		t.Fatalf("expected seeding with a ratio limit, got %s", complete.Status)
	}
	importJob, err := service.DownloadJob(ctx, job.ID)
	if err != nil || importJob.Status != "completed" {
		t.Fatalf("seeding job must be importable: %v %+v", err, importJob)
	}
	if len(importJob.Files) != 1 || importJob.Files[0].Name != "tiny.bin" || importJob.Files[0].URL == "" {
		t.Fatalf("unexpected import files: %+v", importJob.Files)
	}

	assertDownloaded(t, service, job.ID, "tiny.bin", payload)

	// Stopping seeding through the limit turns the job into a completed import source.
	if _, err := service.UpdateLimits(ctx, job.ID, 0, 0); err != nil {
		t.Fatalf("cannot update limits: %v", err)
	}
	waitFor(t, 15*time.Second, "seeding to stop", func() bool {
		return jobByID(t, service, job.ID).Status == "completed"
	})
	if stopped := jobByID(t, service, job.ID); stopped.SeedingElapsed < 0 || stopped.SeedingElapsed > 600 {
		t.Fatalf("unexpected seeding duration %d", stopped.SeedingElapsed)
	}

	// Restarting the engine must keep the verified pieces and never re-download the payload.
	service.Close()
	restarted := newService(t, pool, directory)
	updateSeedPolicy(t, restarted, 0, 0)
	resumed := jobByID(t, restarted, job.ID)
	if resumed.Status != "completed" || resumed.PiecesDone != resumed.PiecesTotal || resumed.PiecesTotal == 0 {
		t.Fatalf("unexpected resumed job: %+v", resumed)
	}
	waitFor(t, 10*time.Second, "the restarted engine to settle", func() bool {
		current := jobByID(t, restarted, job.ID)
		return current.Status == "completed" && current.BytesDone == current.BytesTotal
	})

	// A recheck hashes the data on disk again and resumes without any peer.
	rechecked, err := restarted.Recheck(ctx, job.ID)
	if err != nil {
		t.Fatalf("cannot recheck: %v", err)
	}
	if rechecked.PiecesDone != 0 {
		t.Fatalf("recheck did not clear the verified pieces: %+v", rechecked)
	}
	waitFor(t, 30*time.Second, "the recheck to finish", func() bool {
		return jobByID(t, restarted, job.ID).Status == "completed"
	})
	assertDownloaded(t, restarted, job.ID, "tiny.bin", payload)
}

func assertDownloaded(t *testing.T, service *torrents.Service, id, name string, payload []byte) {
	t.Helper()
	handle, err := service.OpenFile(context.Background(), id, name)
	if err != nil {
		t.Fatalf("cannot open %s: %v", name, err)
	}
	defer handle.Close()
	content := make([]byte, len(payload))
	if _, err := handle.Read(content); err != nil {
		t.Fatalf("cannot read %s: %v", name, err)
	}
	if !bytes.Equal(content, payload) {
		t.Fatalf("%s content differs from the seed", name)
	}
	detail, err := service.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	outputDir, err := service.OutputDirectory(id)
	if err != nil || !strings.HasSuffix(outputDir, detail.Job.InfoHash) {
		t.Fatalf("unexpected output directory %q: %v", outputDir, err)
	}
}

func TestPathEscapeAndSymlinkAreRefused(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	service := newService(t, pool, directory)

	malicious := metainfo.Info{
		Name: "pack", PieceLength: 16 << 10, Pieces: make([]byte, 20),
		Files: []metainfo.FileInfo{{Length: 32, Path: []string{"..", "escape.bin"}}},
	}
	if _, err := service.Add(ctx, torrents.AddInput{Torrent: writeTorrent(t, malicious), Source: "file"}); !errors.Is(err, torrents.ErrInvalid) {
		t.Fatalf("traversal torrent must be rejected, got %v", err)
	}
	jobs, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("rejected torrent created jobs: %+v", jobs)
	}
	if _, err := os.Stat(filepath.Join(directory, "escape.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("traversal torrent escaped the download root")
	}

	seedDir := t.TempDir()
	torrentBytes, _ := buildTorrent(t, seedDir, "tiny.bin", 8<<10)
	hash := infoHashOf(t, torrentBytes)
	outside := t.TempDir()
	jobDir := filepath.Join(directory, "torrents", hash)
	if err := os.MkdirAll(filepath.Dir(jobDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, jobDir); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "the symlinked job to fail", func() bool {
		return jobByID(t, service, job.ID).Status == "failed"
	})
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlinked job wrote outside its root: %v %v", entries, err)
	}
}

func TestDuplicateAndInvalidInputs(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	service := newService(t, pool, t.TempDir())
	updateSeedPolicy(t, service, 0, 0)

	seedDir := t.TempDir()
	torrentBytes, _ := buildTorrent(t, seedDir, "tiny.bin", 4<<10)
	first, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file", Title: "Example"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("duplicate torrent created a second job: %s %s", first.ID, second.ID)
	}
	jobs, err := service.List(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("expected one job, got %+v (%v)", jobs, err)
	}

	hash := strings.Repeat("cd", 20)
	byMagnet, err := service.Add(ctx, torrents.AddInput{Magnet: "magnet:?xt=urn:btih:" + hash})
	if err != nil {
		t.Fatalf("valid magnet rejected: %v", err)
	}
	again, err := service.Add(ctx, torrents.AddInput{Magnet: "magnet:?xt=urn:btih:" + strings.ToUpper(hash)})
	if err != nil || again.ID != byMagnet.ID {
		t.Fatalf("magnet duplicate not detected: %v %s", err, again.ID)
	}

	for name, input := range map[string]torrents.AddInput{
		"empty":         {},
		"both inputs":   {Magnet: "magnet:?xt=urn:btih:" + hash, Torrent: torrentBytes},
		"bad magnet":    {Magnet: "not-a-magnet"},
		"bad torrent":   {Torrent: []byte("no bencode here")},
		"empty torrent": {Torrent: []byte("")},
	} {
		if _, err := service.Add(ctx, input); !errors.Is(err, torrents.ErrInvalid) {
			t.Errorf("%s: expected ErrInvalid, got %v", name, err)
		}
	}

	// Pause and resume are idempotent, and deleting keeps files by default.
	waitFor(t, 15*time.Second, "the magnet job to start", func() bool {
		return service.AddPeers(ctx, byMagnet.ID, []string{"127.0.0.1:1"}) == nil
	})
	if _, err := service.Pause(ctx, byMagnet.ID); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	paused := jobByID(t, service, byMagnet.ID)
	if paused.Status != "paused" {
		t.Fatalf("expected paused, got %s", paused.Status)
	}
	if _, err := service.Pause(ctx, byMagnet.ID); err != nil {
		t.Fatalf("pausing twice failed: %v", err)
	}
	if _, err := service.Resume(ctx, byMagnet.ID); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if _, err := service.Resume(ctx, byMagnet.ID); !errors.Is(err, torrents.ErrConflict) {
		t.Fatalf("resuming an active job must conflict, got %v", err)
	}
	if err := service.AddPeers(ctx, byMagnet.ID, []string{"not-an-address"}); !errors.Is(err, torrents.ErrInvalid) {
		t.Fatalf("invalid peer address must be rejected, got %v", err)
	}

	removed, err := service.Delete(ctx, first.ID, false)
	if err != nil || removed {
		t.Fatalf("delete without files failed: %v %v", err, removed)
	}
	if _, err := service.Delete(ctx, first.ID, false); !errors.Is(err, torrents.ErrNotFound) {
		t.Fatalf("deleting twice must report not found, got %v", err)
	}
}

func TestSettingsValidationAndHealth(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	service := newService(t, pool, t.TempDir())
	if _, err := service.UpdateSettings(ctx, torrents.SettingsUpdate{ListenPort: 70000, MaxActiveJobs: 3}); !errors.Is(err, torrents.ErrInvalid) {
		t.Fatalf("invalid port must be rejected, got %v", err)
	}
	settings, err := service.UpdateSettings(ctx, torrents.SettingsUpdate{
		ListenPort: 0, DHTEnabled: false, PEXEnabled: false, MaxActiveJobs: 2,
		DownloadLimitKBps: 128, UploadLimitKBps: 64, SeedRatioLimit: 0.5, SeedTimeLimitMinutes: 30,
	})
	if err != nil {
		t.Fatalf("cannot update settings: %v", err)
	}
	if settings.MaxActiveJobs != 2 || settings.DownloadLimitKBps != 128 || settings.SeedRatioLimit != 0.5 {
		t.Fatalf("settings were not saved: %+v", settings)
	}
	health := service.Health(ctx)
	if !health.OK || health.ListenPort <= 0 {
		t.Fatalf("unexpected health: %+v", health)
	}
}

func TestBridgeListsOnlyCompleteJobs(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	service := newService(t, pool, t.TempDir())
	updateSeedPolicy(t, service, 0, 0)

	seedDir := t.TempDir()
	torrentBytes, payload := buildTorrent(t, seedDir, "tiny.bin", 16<<10)
	port := seeder(t, seedDir, torrentBytes)
	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompletedJobs(ctx)
	if err != nil || len(completed) != 0 {
		t.Fatalf("active transfer must not be importable: %+v (%v)", completed, err)
	}
	peer := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)).String()
	waitFor(t, 60*time.Second, "the transfer to complete", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peer})
		return jobByID(t, service, job.ID).Status == "completed"
	})
	completed, err = service.CompletedJobs(ctx)
	if err != nil || len(completed) != 1 {
		t.Fatalf("completed jobs: %+v (%v)", completed, err)
	}
	entry := completed[0]
	if entry.Status != "completed" || entry.ID != job.ID || len(entry.Files) != 1 ||
		entry.Files[0].Size != int64(len(payload)) {
		t.Fatalf("unexpected bridge job: %+v", entry)
	}
	if service.ImportMode() != "hardlink" {
		t.Fatalf("imports must not move seeding files, got %q", service.ImportMode())
	}
	if _, err := service.OpenFile(ctx, job.ID, filepath.Join("..", "tiny.bin")); !errors.Is(err, torrents.ErrNotFound) {
		t.Fatalf("traversal file name must be refused, got %v", err)
	}
	if _, err := service.OpenFile(ctx, job.ID, "not-listed.bin"); !errors.Is(err, torrents.ErrNotFound) {
		t.Fatalf("undeclared file must be refused, got %v", err)
	}
	directory, err := service.OutputDirectory(job.ID)
	if err != nil || !strings.Contains(directory, job.InfoHash) {
		t.Fatalf("unexpected output directory: %q %v", directory, err)
	}
}

func TestDeleteRemovesDataDirectoryOnly(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	service := newService(t, pool, directory)
	seedDir := t.TempDir()
	torrentBytes, _ := buildTorrent(t, seedDir, "tiny.bin", 8<<10)
	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 15*time.Second, "the job to start", func() bool {
		return service.AddPeers(ctx, job.ID, []string{"127.0.0.1:1"}) == nil
	})
	dataDir := filepath.Join(directory, "torrents", job.InfoHash)
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("job directory missing: %v", err)
	}
	keep := filepath.Join(directory, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := service.Delete(ctx, job.ID, true)
	if err != nil || !removed {
		t.Fatalf("delete with files failed: %v %v", err, removed)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("job directory still exists: %v", err)
	}
	if content, err := os.ReadFile(keep); err != nil || string(content) != "keep" {
		t.Fatalf("unrelated file was removed: %v %q", err, content)
	}
	if _, err := service.Get(ctx, job.ID); !errors.Is(err, torrents.ErrNotFound) {
		t.Fatalf("deleted job is still readable: %v", err)
	}
}

func TestPrivateMagnetMovesToPrivateClient(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	service := newService(t, pool, t.TempDir())
	updateSeedPolicy(t, service, 0, 0)

	seedDir := t.TempDir()
	payload := make([]byte, 32<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(seedDir, "private.bin")
	if err := os.WriteFile(seedPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(seedPath); err != nil {
		t.Fatal(err)
	}
	private := true
	info.Private = &private
	torrentBytes := writeTorrent(t, info)
	port := seeder(t, seedDir, torrentBytes)
	hash := infoHashOf(t, torrentBytes)

	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=private.bin"
	job, err := service.Add(ctx, torrents.AddInput{Magnet: magnet})
	if err != nil {
		t.Fatal(err)
	}
	peer := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)).String()
	waitFor(t, 60*time.Second, "the private magnet to download", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peer})
		current := jobByID(t, service, job.ID)
		if current.Status == "failed" {
			t.Fatalf("private magnet failed: %s", current.Error)
		}
		return current.Status == "completed"
	})
	moved := jobByID(t, service, job.ID)
	if !moved.Private {
		t.Fatalf("private flag was not recorded: %+v", moved)
	}
	assertDownloaded(t, service, job.ID, "private.bin", payload)
}
