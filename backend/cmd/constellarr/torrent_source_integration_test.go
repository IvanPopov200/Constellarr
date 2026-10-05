package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
)

const fixtureAPIKey = "fixture-indexer-key"

func testPool(t *testing.T) *pgxpool.Pool {
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
	schema := "torrent_source_test_" + strings.ToLower(rand.Text())
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
	return pool
}

// importableVideo mirrors the library video extensions so the gate can be asserted locally.
func importableVideo(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".m4v", ".webm", ".mpeg", ".mpg", ".ts", ".wmv":
		return true
	}
	return false
}

func peerAddr(port int) string {
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)).String()
}

// indexerFixture serves Torznab search results and torrent downloads on one host.
type indexerFixture struct {
	server *httptest.Server
	items  []fixtureItem
}

type fixtureItem struct {
	guid    string
	title   string
	size    int64
	file    string
	payload []byte
}

func newIndexerFixture(t *testing.T) *indexerFixture {
	t.Helper()
	fixture := &indexerFixture{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != fixtureAPIKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("t") == "caps" {
			fmt.Fprint(w, `<?xml version="1.0"?><caps><searching><search available="yes"/></searching></caps>`)
			return
		}
		type item struct {
			Title     string `xml:"title"`
			GUID      string `xml:"guid"`
			PubDate   string `xml:"pubDate"`
			Size      int64  `xml:"size"`
			Enclosure struct {
				URL    string `xml:"url,attr"`
				Length int64  `xml:"length,attr"`
			} `xml:"enclosure"`
		}
		feed := struct {
			XMLName xml.Name `xml:"rss"`
			Channel struct {
				Items []item `xml:"item"`
			} `xml:"channel"`
		}{}
		for _, entry := range fixture.items {
			feed.Channel.Items = append(feed.Channel.Items, item{
				Title: entry.title, GUID: entry.guid, PubDate: "Mon, 02 Jan 2024 15:04:05 +0000", Size: entry.size,
				Enclosure: struct {
					URL    string `xml:"url,attr"`
					Length int64  `xml:"length,attr"`
				}{URL: fixture.server.URL + entry.file, Length: entry.size},
			})
		}
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(feed)
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != fixtureAPIKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		for _, entry := range fixture.items {
			if entry.file == r.URL.Path {
				w.Header().Set("Content-Type", "application/x-bittorrent")
				_, _ = w.Write(entry.payload)
				return
			}
		}
		http.NotFound(w, r)
	})
	fixture.server = httptest.NewServer(mux)
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *indexerFixture) add(guid, title string, torrentBytes, payload []byte) {
	f.items = append(f.items, fixtureItem{
		guid: guid, title: title, size: int64(len(payload)), file: "/download/" + guid + ".torrent", payload: torrentBytes,
	})
}

// localSeeder serves a torrent from the local filesystem without external network access.
func localSeeder(t *testing.T, dataDir string, torrentBytes []byte) int {
	t.Helper()
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
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
	seeded, err := client.AddTorrent(meta)
	if err != nil {
		t.Fatalf("cannot add the seeder torrent: %v", err)
	}
	<-seeded.GotInfo()
	seeded.DownloadAll()
	waitFor(t, 20*time.Second, func() bool { return seeded.Complete().Bool() }, "the seeder never verified its data")
	return client.LocalPort()
}

func fileTorrent(t *testing.T, dir, name string, payload []byte) ([]byte, string) {
	t.Helper()
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(target); err != nil {
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
	return buf.Bytes(), meta.HashInfoBytes().HexString()
}

func dirTorrent(t *testing.T, dir string) ([]byte, string) {
	t.Helper()
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(dir); err != nil {
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
	return buf.Bytes(), meta.HashInfoBytes().HexString()
}

func writeArchive(t *testing.T, path string, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
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
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// torrentPipeline wires the real adapter, download manager and movie library.
type torrentPipeline struct {
	pool      *pgxpool.Pool
	directory string
	manager   *downloads.Manager
	library   *movies.Service
	service   *torrents.Service
	rootPath  string
}

func newTorrentPipeline(t *testing.T) *torrentPipeline {
	t.Helper()
	ctx := context.Background()
	pool := testPool(t)
	directory := t.TempDir()
	manager, err := downloads.New(ctx, pool, downloads.Config{Directory: directory})
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	movieLibrary, err := movies.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	rootPath := filepath.Join(directory, "library")
	if _, err := movieLibrary.SetConfig(ctx, movies.Config{
		RootFolders:         []movies.RootFolder{{ID: "root", Path: rootPath}},
		FolderTemplate:      "{title} ({year}) [{quality}]",
		FileTemplate:        "{title} ({year}) [{quality}]",
		ImportMode:          library.ModeLink,
		PollMinutes:         15,
		SearchHours:         6,
		MinimumAvailability: "released",
		RetryFailed:         true,
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	pipeline := &torrentPipeline{pool: pool, directory: directory, manager: manager, library: movieLibrary, rootPath: rootPath}
	pipeline.startService(t)
	return pipeline
}

func (p *torrentPipeline) startService(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	service, err := torrents.New(ctx, p.pool, torrents.Options{Directory: p.directory, Testing: true})
	if err != nil {
		t.Fatalf("torrents.New: %v", err)
	}
	if _, err := service.UpdateSettings(ctx, torrents.SettingsUpdate{
		ListenPort: 0, DHTEnabled: true, PEXEnabled: true, MaxActiveJobs: 3,
		DownloadLimitKBps: 0, UploadLimitKBps: 0, SeedRatioLimit: 0, SeedTimeLimitMinutes: 0,
	}); err != nil {
		t.Fatalf("torrent settings: %v", err)
	}
	service.Start(ctx)
	p.service = service
	p.manager.SetTorrents(torrentSource{service: service})
	t.Cleanup(service.Close)
}

func (p *torrentPipeline) addIndexer(t *testing.T, url string) {
	t.Helper()
	if _, err := p.service.CreateSource(context.Background(), torrents.SourceInput{
		Name: "Fixture", URL: url, APIKey: fixtureAPIKey, Enabled: true,
	}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
}

// grab searches the fixture indexer and adds the first result through the adapter.
func (p *torrentPipeline) grab(t *testing.T, query string) (downloads.Job, movies.Movie) {
	t.Helper()
	ctx := context.Background()
	releases, err := p.manager.SearchTorrents(ctx, query)
	if err != nil || len(releases) == 0 {
		t.Fatalf("torrent search = %+v (%v)", releases, err)
	}
	release := releases[0]
	if release.Protocol != "torrent" || !strings.HasPrefix(release.ID, downloads.TorrentPrefix) {
		t.Fatalf("unexpected release: %+v", release)
	}
	job, err := p.manager.Add(ctx, release.ID, release.Title)
	if err != nil {
		t.Fatalf("manager.Add: %v", err)
	}
	if job.Protocol != "torrent" || job.ID == "" {
		t.Fatalf("unexpected download job: %+v", job)
	}
	movie, err := p.library.Add(ctx, movies.AddInput{
		Metadata: metadata.Title{Title: query, Year: 2021}, ProfileID: "hd", RootID: "root",
	})
	if err != nil {
		t.Fatalf("movie add: %v", err)
	}
	if err := p.library.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: job.ID, ReleaseID: release.ID, Title: release.Title, Status: "queued",
	}); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}
	return job, movie
}

func (p *torrentPipeline) waitForCompletion(t *testing.T, id string, port int, observe func(downloads.Job)) downloads.Job {
	t.Helper()
	ctx := context.Background()
	var last downloads.Job
	waitFor(t, 90*time.Second, func() bool {
		_ = p.service.AddPeers(ctx, id, []string{peerAddr(port)})
		job, err := p.manager.Get(ctx, id)
		if err != nil {
			t.Fatalf("manager.Get: %v", err)
		}
		last = job
		if job.Status == "failed" {
			t.Fatalf("download failed: %s", job.Error)
		}
		if observe != nil {
			observe(job)
		}
		return job.Status == "completed"
	}, "the torrent download never completed")
	return last
}

func readImported(t *testing.T, pipeline *torrentPipeline, movie movies.Movie) []byte {
	t.Helper()
	stored, err := pipeline.library.Store.Get(context.Background(), movie.ID)
	if err != nil || len(stored.Files) == 0 {
		t.Fatalf("imported movie files = %+v (%v)", stored.Files, err)
	}
	content, err := os.ReadFile(filepath.Join(pipeline.rootPath, filepath.FromSlash(stored.Files[0].Path)))
	if err != nil {
		t.Fatalf("cannot read the imported file: %v", err)
	}
	return content
}

func TestTorrentSourceImportsThroughManager(t *testing.T) {
	ctx := context.Background()
	pipeline := newTorrentPipeline(t)

	seedDir := t.TempDir()
	payload := make([]byte, 128<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	torrentBytes, hash := fileTorrent(t, seedDir, "loose.mkv", payload)
	port := localSeeder(t, seedDir, torrentBytes)
	fixture := newIndexerFixture(t)
	fixture.add("loose", "Loose Film", torrentBytes, payload)
	pipeline.addIndexer(t, fixture.server.URL+"/api")

	job, movie := pipeline.grab(t, "Loose Film")
	completed := pipeline.waitForCompletion(t, job.ID, port, nil)
	if completed.BytesDone != completed.BytesTotal || completed.SegmentsDone != completed.SegmentsTotal {
		t.Fatalf("incomplete download reported completed: %+v", completed)
	}
	if mode, err := pipeline.manager.ImportMode(ctx, job.ID, ""); err != nil || mode != "hardlink" {
		t.Fatalf("torrent imports must not move seeding files: %q %v", mode, err)
	}

	payloadPath := filepath.Join(pipeline.directory, "torrents", hash, "loose.mkv")
	before, err := os.Stat(payloadPath)
	if err != nil {
		t.Fatalf("payload file missing: %v", err)
	}
	imported, err := pipeline.library.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("SyncDownloads = %d, %v", imported, err)
	}
	if content := readImported(t, pipeline, movie); !bytes.Equal(content, payload) {
		t.Fatal("the imported movie does not match the downloaded payload")
	}
	after, err := os.Stat(payloadPath)
	if err != nil {
		t.Fatalf("the seeding payload was removed: %v", err)
	}
	if after.ModTime() != before.ModTime() || after.Size() != before.Size() {
		t.Fatalf("the seeding payload changed during import: %+v vs %+v", after, before)
	}
	payloadBytes, err := os.ReadFile(payloadPath)
	if err != nil || !bytes.Equal(payloadBytes, payload) {
		t.Fatalf("the seeding payload content changed: %v", err)
	}
	if imported, err := pipeline.library.SyncDownloads(ctx); err != nil || imported != 0 {
		t.Fatalf("second SyncDownloads = %d, %v", imported, err)
	}

	// Restarting the engine keeps verified pieces, the payload and the import.
	pipeline.service.Close()
	pipeline.startService(t)
	resumed, err := pipeline.manager.Get(ctx, job.ID)
	if err != nil || resumed.Status != "completed" || resumed.SegmentsDone != resumed.SegmentsTotal {
		t.Fatalf("resumed job = %+v (%v)", resumed, err)
	}
	restarted, err := os.Stat(payloadPath)
	if err != nil || restarted.ModTime() != before.ModTime() {
		t.Fatalf("restart rewrote the payload: %v %+v", err, restarted)
	}
	if content := readImported(t, pipeline, movie); !bytes.Equal(content, payload) {
		t.Fatal("the import changed after a restart")
	}
	if imported, err := pipeline.library.SyncDownloads(ctx); err != nil || imported != 0 {
		t.Fatalf("SyncDownloads after restart = %d, %v", imported, err)
	}
}

func TestTorrentSourceExtractsArchiveForImport(t *testing.T) {
	ctx := context.Background()
	pipeline := newTorrentPipeline(t)

	seedParent := t.TempDir()
	seedDir := filepath.Join(seedParent, "Archive.Release")
	if err := os.MkdirAll(seedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 96<<10)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(seedDir, "archive-release.zip")
	archiveBytes := writeArchive(t, archivePath, map[string][]byte{
		"Archive.Release/archive.mkv": payload,
		"Archive.Release/archive.srt": []byte("1\n00:00:00,000 --> 00:00:01,000\nhi\n"),
	})
	torrentBytes, hash := dirTorrent(t, seedDir)
	port := localSeeder(t, seedParent, torrentBytes)
	fixture := newIndexerFixture(t)
	fixture.add("archive", "Archived Film", torrentBytes, archiveBytes)
	pipeline.addIndexer(t, fixture.server.URL+"/api")

	job, movie := pipeline.grab(t, "Archived Film")
	// While extraction runs the bridge must advertise the raw payload, which the importers cannot use.
	var extracting []downloads.OutputFile
	pipeline.waitForCompletion(t, job.ID, port, func(current downloads.Job) {
		if current.Status == "extracting" && extracting == nil {
			extracting = current.Files
		}
	})
	if extracting != nil {
		if len(extracting) != 1 || extracting[0].Name != "archive-release.zip" {
			t.Fatalf("an extracting job must advertise the raw payload: %+v", extracting)
		}
		if importableVideo(extracting[0].Name) {
			t.Fatalf("archive payloads must expose no importable media while extracting: %+v", extracting)
		}
	}
	payloadEntries, err := os.ReadDir(filepath.Join(pipeline.directory, "torrents", hash))
	if err != nil || len(payloadEntries) != 1 || payloadEntries[0].Name() != "archive-release.zip" {
		t.Fatalf("the raw payload must stay archive-only: %v %v", payloadEntries, err)
	}

	output, err := pipeline.manager.OutputDirectory(job.ID)
	if err != nil || filepath.Base(filepath.Dir(output)) != "processed" {
		t.Fatalf("imports must read the extracted directory, got %q (%v)", output, err)
	}
	imported, err := pipeline.library.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("SyncDownloads = %d, %v", imported, err)
	}
	if content := readImported(t, pipeline, movie); !bytes.Equal(content, payload) {
		t.Fatal("the imported movie does not match the archived payload")
	}

	payloadDir := filepath.Join(pipeline.directory, "torrents", hash)
	kept, err := os.ReadFile(filepath.Join(payloadDir, "archive-release.zip"))
	if err != nil || !bytes.Equal(kept, archiveBytes) {
		t.Fatalf("the payload archive changed during extraction: %v", err)
	}
	detail, err := pipeline.service.Get(ctx, job.ID)
	if err != nil || detail.Job.Processing == nil || detail.Job.Processing.State != "completed" {
		t.Fatalf("processing state = %+v (%v)", detail.Job.Processing, err)
	}
}
