package torrents

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/jackc/pgx/v5/pgxpool"
)

// productionService builds a service with DHT and PEX enabled and an ephemeral listen port.
func productionService(t *testing.T, pool *pgxpool.Pool, directory string) *Service {
	t.Helper()
	ctx := context.Background()
	service, err := New(ctx, pool, Options{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSettings(ctx, SettingsUpdate{
		ListenPort: 0, DHTEnabled: true, PEXEnabled: true, MaxActiveJobs: 3,
		SeedRatioLimit: 0, SeedTimeLimitMinutes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

// seedTorrent serves a local two-peer fixture and returns its torrent file, hash and port.
func seedTorrent(t *testing.T, payload []byte) ([]byte, string, int) {
	t.Helper()
	parent := t.TempDir()
	dataDir := filepath.Join(parent, "Seed")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "payload.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(dataDir); err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	meta := metainfo.MetaInfo{InfoBytes: raw}
	if err := meta.Write(&buf); err != nil {
		t.Fatal(err)
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = parent
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
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	seeded, err := client.AddTorrent(&meta)
	if err != nil {
		t.Fatal(err)
	}
	<-seeded.GotInfo()
	seeded.DownloadAll()
	waitUntil(t, 20*time.Second, "the fixture seeder to verify its data", func() bool { return seeded.Complete().Bool() })
	return buf.Bytes(), meta.HashInfoBytes().HexString(), client.LocalPort()
}

// driveMetadata exchanges metadata with the fixture seeder without running the coordinator.
func driveMetadata(t *testing.T, service *Service, pool *pgxpool.Pool, id string, run *jobRun, port int) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_ = service.AddPeers(ctx, id, []string{mustAddrPort(t, port).String()})
		service.advance(ctx, run)
		stored, err := service.storedByID(ctx, pool, id)
		if err == nil && len(stored.metainfo) != 0 {
			service.advance(ctx, run)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the metadata exchange never completed")
}

// runClient reads the attached client under the run lock.
func runClient(run *jobRun) (*torrent.Client, bool) {
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.client, run.private
}

func TestPrivateHintSurvivesMetadataAndRestart(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	directory := t.TempDir()
	service := productionService(t, pool, directory)

	_, hash, port := seedTorrent(t, []byte("hinted-private-payload"))
	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=Hinted.Private&tr=http%3A%2F%2F127.0.0.1%3A1%2Fannounce"
	job, err := service.Add(ctx, AddInput{Magnet: magnet, Source: sourceTorznab, ReleaseID: "opaque-result", Private: true})
	if err != nil {
		t.Fatal(err)
	}
	if !job.Private || job.ReleaseID != "opaque-result" {
		t.Fatalf("the private hint must reach the job: %+v", job)
	}
	stored, err := service.storedByID(ctx, pool, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	service.startRun(ctx, stored)
	run := service.run(job.ID)
	if run == nil {
		t.Fatal("the hinted magnet was not attached")
	}
	driveMetadata(t, service, pool, job.ID, run, port)

	// Metadata without a private flag must not move the hinted job onto the DHT client.
	client, isPrivate := runClient(run)
	if !isPrivate || client != service.privateClient {
		t.Fatal("the private hint must survive metadata that carries no private flag")
	}
	if service.publicClient != nil {
		t.Fatal("a hinted private magnet must never create the public DHT client")
	}
	stored, err = service.storedByID(ctx, pool, job.ID)
	if err != nil || !stored.Private || stored.ReleaseID != "opaque-result" {
		t.Fatalf("the persisted job must keep its private flag and release id: %+v (%v)", stored, err)
	}

	// A restart re-attaches from the persisted state without touching DHT.
	service.Close()
	restarted := productionService(t, pool, directory)
	restartedStored, err := restarted.storedByID(ctx, pool, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	restarted.startRun(ctx, restartedStored)
	restartedRun := restarted.run(job.ID)
	if restartedRun == nil {
		t.Fatal("the restarted service did not attach the job")
	}
	restarted.advance(ctx, restartedRun)
	client, isPrivate = runClient(restartedRun)
	if !isPrivate || client != restarted.privateClient || restarted.publicClient != nil {
		t.Fatal("a restart must not lose the private hint")
	}
}

func TestPublicTrackerMagnetMovesToPublicAfterMetadata(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	service := productionService(t, pool, t.TempDir())

	_, hash, port := seedTorrent(t, []byte("public-payload"))
	magnet := "magnet:?xt=urn:btih:" + hash + "&dn=Public.Release&tr=http%3A%2F%2F127.0.0.1%3A1%2Fannounce"
	job, err := service.Add(ctx, AddInput{Magnet: magnet, Source: sourceMagnet})
	if err != nil {
		t.Fatal(err)
	}
	if job.Private {
		t.Fatalf("a pasted magnet without a hint stays public: %+v", job)
	}
	stored, err := service.storedByID(ctx, pool, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	service.startRun(ctx, stored)
	run := service.run(job.ID)
	if run == nil {
		t.Fatal("the public magnet was not attached")
	}
	if client, isPrivate := runClient(run); !isPrivate || client != service.privateClient {
		t.Fatal("metadata must be quarantined without DHT until the torrent is known public")
	}
	driveMetadata(t, service, pool, job.ID, run, port)

	client, isPrivate := runClient(run)
	if isPrivate || client != service.publicClient {
		t.Fatal("a metadata-confirmed public torrent must continue on the public client")
	}
	if servers := service.publicClient.DhtServers(); len(servers) == 0 {
		t.Fatal("the public control must run DHT for the routing assertion to mean anything")
	}
	stored, err = service.storedByID(ctx, pool, job.ID)
	if err != nil || stored.Private {
		t.Fatalf("a public torrent must stay public after metadata: %+v (%v)", stored, err)
	}
}
