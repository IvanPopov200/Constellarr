package torrents_test

import (
	"archive/zip"
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
)

func writeTestZip(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func torrentFromDir(t *testing.T, dir string) []byte {
	t.Helper()
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatalf("cannot build torrent info: %v", err)
	}
	return writeTorrent(t, info)
}

func peerAddr(port int) string {
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)).String()
}

func TestArchivedPayloadExtractsForImport(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	service := newService(t, pool, directory)
	updateSeedPolicy(t, service, 0, 0)

	payload := make([]byte, 96<<10)
	for index := range payload {
		payload[index] = byte(index * 7)
	}
	seedParent := t.TempDir()
	seedDir := filepath.Join(seedParent, "Release")
	if err := os.MkdirAll(seedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestZip(t, filepath.Join(seedDir, "release.zip"), map[string][]byte{
		"Movie/release.mkv": payload,
		"Movie/release.srt": []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n"),
	})
	originalArchive, err := os.ReadFile(filepath.Join(seedDir, "release.zip"))
	if err != nil {
		t.Fatal(err)
	}
	torrentBytes := torrentFromDir(t, seedDir)
	port := seeder(t, seedParent, torrentBytes)

	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatalf("cannot add torrent: %v", err)
	}
	waitFor(t, 60*time.Second, "the archived payload to be extracted", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peerAddr(port)})
		current := jobByID(t, service, job.ID)
		if current.Status == "failed" {
			t.Fatalf("transfer failed: %s", current.Error)
		}
		return current.Processing != nil && current.Processing.State == "completed"
	})

	bridge, err := service.DownloadJob(ctx, job.ID)
	if err != nil || bridge.Status != "completed" || len(bridge.Files) != 2 {
		t.Fatalf("bridge after extraction = %+v (%v)", bridge, err)
	}
	output, err := service.OutputDirectory(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(output)) != "processed" {
		t.Fatalf("imports must scan the extracted directory, got %q", output)
	}
	extracted, err := os.ReadFile(filepath.Join(output, "Movie", "release.mkv"))
	if err != nil || !bytes.Equal(extracted, payload) {
		t.Fatalf("extracted media mismatch: %v", err)
	}

	payloadDir := filepath.Join(directory, "torrents", job.InfoHash)
	entries, err := os.ReadDir(payloadDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "release.zip" {
		t.Fatalf("the seeding payload must stay unchanged: %v %v", entries, err)
	}
	archive, err := os.ReadFile(filepath.Join(payloadDir, "release.zip"))
	if err != nil || !bytes.Equal(archive, originalArchive) {
		t.Fatalf("payload archive changed during extraction: %v", err)
	}
}

func TestLoosePayloadSkipsExtraction(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	service := newService(t, pool, directory)
	updateSeedPolicy(t, service, 0, 0)

	seedDir := t.TempDir()
	torrentBytes, payload := buildTorrent(t, seedDir, "loose.mkv", 32<<10)
	port := seeder(t, seedDir, torrentBytes)
	job, err := service.Add(ctx, torrents.AddInput{Torrent: torrentBytes, Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the loose payload to complete", func() bool {
		_ = service.AddPeers(ctx, job.ID, []string{peerAddr(port)})
		return jobByID(t, service, job.ID).Status == "completed"
	})
	waitFor(t, 15*time.Second, "the processing decision", func() bool {
		current := jobByID(t, service, job.ID)
		return current.Processing != nil && current.Processing.State == "skipped"
	})
	output, err := service.OutputDirectory(job.ID)
	if err != nil || filepath.Base(output) != job.InfoHash {
		t.Fatalf("loose media must be imported in place, got %q (%v)", output, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "torrents", "processed", job.InfoHash)); !os.IsNotExist(err) {
		t.Fatalf("loose media must not be duplicated into an extraction directory: %v", err)
	}
	bridge, err := service.DownloadJob(ctx, job.ID)
	if err != nil || bridge.Status != "completed" || len(bridge.Files) != 1 || bridge.Files[0].Name != "loose.mkv" {
		t.Fatalf("bridge for loose media = %+v (%v)", bridge, err)
	}
	handle, err := service.OpenFile(ctx, job.ID, "loose.mkv")
	if err != nil {
		t.Fatalf("payload file is not downloadable: %v", err)
	}
	defer handle.Close()
	content := make([]byte, len(payload))
	if _, err := handle.Read(content); err != nil || !bytes.Equal(content, payload) {
		t.Fatalf("payload content changed: %v", err)
	}
}
