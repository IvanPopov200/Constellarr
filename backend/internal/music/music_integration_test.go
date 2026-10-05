package music_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
)

const testNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head><meta type="title">Muse - Absolution</meta></head>
  <file poster="poster &lt;poster@example.com&gt;" date="1136214245" subject="[1/1] - &quot;01.flac&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="1024" number="1">synthetic-part-1@example.com</segment></segments>
  </file>
</nzb>`

type fakeRelease struct {
	ID    string
	Title string
	Size  int64
}

type env struct {
	pool       *pgxpool.Pool
	manager    *downloads.Manager
	service    *music.Service
	dir        string
	indexer    *httptest.Server
	musicBrain string
}

// searchGate holds one album search open so a test can overlap it with other work.
type searchGate struct {
	armed    atomic.Bool
	entered  chan struct{}
	release  chan struct{}
	released sync.Once
}

func newSearchGate() *searchGate {
	return &searchGate{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (g *searchGate) arm() {
	g.armed.Store(true)
}

func (g *searchGate) holding() bool {
	return g.armed.Load()
}

func (g *searchGate) enter() {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
}

func (g *searchGate) letGo() {
	g.released.Do(func() { close(g.release) })
}

// newEnv builds an isolated schema, manager, and service; a gate can hold one album search.
func newEnv(t *testing.T, releases []fakeRelease, artist, album string, gates ...*searchGate) *env {
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
	schema := "music_test_" + strings.ToLower(rand.Text())
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

	indexer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch query.Get("t") {
		case "get":
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = w.Write([]byte(testNZB))
		default:
			// Only the album search is gated; feeds and category searches pass through.
			if len(gates) > 0 && gates[0] != nil && gates[0].holding() && query.Get("album") != "" {
				gates[0].enter()
			}
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(feedFor(releases, artist, album)))
		}
	}))
	t.Cleanup(indexer.Close)

	dir := t.TempDir()
	manager, err := downloads.New(ctx, pool, downloads.Config{
		IndexerURL: indexer.URL + "/api", APIKey: "synthetic-key", Directory: dir,
	})
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	service, err := music.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("music.New: %v", err)
	}
	return &env{pool: pool, manager: manager, service: service, dir: dir, indexer: indexer}
}

func feedFor(releases []fakeRelease, artist, album string) string {
	var items strings.Builder
	for _, release := range releases {
		fmt.Fprintf(&items, `<item><title>%s</title><guid>%s</guid>
			<pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate>
			<newznab:attr name="category" value="3040"/>
			<newznab:attr name="size" value="%d"/>
			<newznab:attr name="artist" value="%s"/>
			<newznab:attr name="album" value="%s"/>
			</item>`, release.Title, release.ID, release.Size, artist, album)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel>` + items.String() + `</channel></rss>`
}

// libraryConfig points music at a temporary library root.
func (e *env) libraryConfig(t *testing.T) music.Config {
	t.Helper()
	cfg, err := e.service.ConfigView(context.Background())
	if err != nil {
		t.Fatalf("ConfigView: %v", err)
	}
	cfg.RootFolders = []music.RootFolder{{ID: "music", Path: t.TempDir()}}
	cfg.FolderTemplate = "{artist}/{album} ({year})"
	cfg.FileTemplate = "{track:02} {title}"
	cfg.FFprobePath = "ffprobe"
	if e.musicBrain != "" {
		cfg.MusicBrainzURL = e.musicBrain
		cfg.MusicBrainzRateMs = 0
	}
	if _, err := e.service.SetConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	return cfg
}

func (e *env) addAlbum(t *testing.T, artist, album string, monitored bool) music.Album {
	t.Helper()
	ctx := context.Background()
	created, err := e.service.AddArtist(ctx, music.AddArtistInput{Name: artist, Monitored: monitored, MonitorOption: "all"})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	release, err := e.service.AddAlbum(ctx, music.AddAlbumInput{ArtistID: created.ID, Title: album, Year: 2003, Monitored: monitored})
	if err != nil {
		t.Fatalf("AddAlbum: %v", err)
	}
	return release
}

func (e *env) downloadRow(t *testing.T, jobID string) (bool, string) {
	t.Helper()
	var adopted bool
	var mediaType string
	if err := e.pool.QueryRow(context.Background(),
		`SELECT movie_adopted, media_type FROM downloads WHERE id = $1`, jobID).Scan(&adopted, &mediaType); err != nil {
		t.Fatalf("read download: %v", err)
	}
	return adopted, mediaType
}

func TestMusicGrabClaimsDownloadOwnership(t *testing.T) {
	env := newEnv(t, []fakeRelease{{ID: "rel-flac-1", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20}}, "Muse", "Absolution")
	ctx := context.Background()
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	job, err := env.service.Grab(ctx, album.ID, "rel-flac-1", false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if job.Status != "queued" {
		t.Fatalf("job status = %q", job.Status)
	}
	adopted, mediaType := env.downloadRow(t, job.ID)
	if !adopted || mediaType != "music" {
		t.Fatalf("download ownership = adopted %v, media type %q; want true/music", adopted, mediaType)
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].ReleaseID != "rel-flac-1" {
		t.Fatalf("acquisitions = %+v, %v", acquisitions, err)
	}

	// Movie and TV adoption must not claim a music download.
	if _, err := env.pool.Exec(ctx, `INSERT INTO movies (id, imdb_id, data) VALUES ('m1', 'tt1234567', '{}'::jsonb)`); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO movie_acquisitions (movie_id, job_id) VALUES ('m1', $1)`, job.ID); err == nil {
		t.Fatal("a movie claimed a music download")
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO tv_series (id, data) VALUES ('s1', '{}'::jsonb)`); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO tv_acquisitions (job_id, series_id) VALUES ($1, 's1')`, job.ID); err == nil {
		t.Fatal("a series claimed a music download")
	}

	// Music must never take over a download another library already owns.
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb, status, files, media_type, movie_adopted)
		 VALUES ('movie-job', 'movie-release', 'A Movie', '\x00'::bytea, 'completed', '[]', 'movie', true)`); err != nil {
		t.Fatalf("seed movie download: %v", err)
	}
	other := env.addAlbum(t, "Muse", "Origin of Symmetry", true)
	err = env.service.Store.SaveAcquisition(ctx, music.Acquisition{AlbumID: other.ID, JobID: "movie-job", ReleaseID: "movie-release", Status: "queued"})
	if !errors.Is(err, music.ErrConflict) {
		t.Fatalf("claiming a movie download error = %v, want ErrConflict", err)
	}
	if _, mediaType := env.downloadRow(t, "movie-job"); mediaType != "movie" {
		t.Fatalf("original ownership changed to %q", mediaType)
	}
}

func TestMusicImportReconcileSurvivesRestart(t *testing.T) {
	env := newEnv(t, []fakeRelease{{ID: "rel-flac-1", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20}}, "Muse", "Absolution")
	ctx := context.Background()
	cfg := env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)
	job, err := env.service.Grab(ctx, album.ID, "rel-flac-1", false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	writeTrackFile(t, filepath.Join(env.dir, "downloads", job.ID, "output", "01 - Intro.flac"), 1)
	writeTrackFile(t, filepath.Join(env.dir, "downloads", job.ID, "output", "02 - Apocalypse Please.flac"), 2)
	if _, err := env.pool.Exec(ctx, `UPDATE downloads SET status = 'completed' WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("complete download: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE music_acquisitions SET status = 'importing' WHERE job_id = $1`, job.ID); err != nil {
		t.Fatalf("simulate interrupted import: %v", err)
	}

	// A fresh service instance must pick the interrupted import up again.
	restarted, err := music.New(ctx, env.pool, env.manager)
	if err != nil {
		t.Fatalf("music.New after restart: %v", err)
	}
	imported, err := restarted.SyncDownloads(ctx)
	if err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}
	saved, err := restarted.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if len(saved.Files) != 2 || saved.Status == "wanted" {
		t.Fatalf("album after import = %d files, status %q", len(saved.Files), saved.Status)
	}
	root := cfg.RootFolders[0]
	for _, file := range saved.Files {
		if _, err := os.Stat(filepath.Join(root.Path, filepath.FromSlash(file.Path))); err != nil {
			t.Fatalf("imported file %s: %v", file.Path, err)
		}
		if file.Format != "flac" && file.Format != "wav" {
			t.Fatalf("imported format = %q", file.Format)
		}
	}
	acquisitions, err := restarted.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].Status != "imported" {
		t.Fatalf("acquisition after import = %+v, %v", acquisitions, err)
	}

	// A second reconcile must not duplicate work or files.
	if _, err := restarted.SyncDownloads(ctx); err != nil {
		t.Fatalf("second SyncDownloads: %v", err)
	}
	again, err := restarted.Album(ctx, album.ID)
	if err != nil || len(again.Files) != 2 {
		t.Fatalf("album after second reconcile = %d files, %v", len(again.Files), err)
	}
}

func TestMusicImportRejectsMixedAlbums(t *testing.T) {
	env := newEnv(t, []fakeRelease{{ID: "rel-flac-1", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20}}, "Muse", "Absolution")
	ctx := context.Background()
	cfg := env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)
	job, err := env.service.Grab(ctx, album.ID, "rel-flac-1", false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	output := filepath.Join(env.dir, "downloads", job.ID, "output")
	writeTrackFile(t, filepath.Join(output, "Muse - Absolution (2003)", "01 - Intro.flac"), 1)
	writeTrackFile(t, filepath.Join(output, "Other Band - Another Album (2011)", "01 - Song.flac"), 2)
	if _, err := env.pool.Exec(ctx, `UPDATE downloads SET status = 'completed' WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("complete download: %v", err)
	}
	if _, err := env.service.SyncDownloads(ctx); err == nil {
		t.Fatal("mixed albums were imported")
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].Status != "import-failed" {
		t.Fatalf("acquisition = %+v, %v; want import-failed", acquisitions, err)
	}
	root := cfg.RootFolders[0]
	if entries, err := os.ReadDir(root.Path); err == nil && len(entries) > 0 {
		t.Fatalf("a failed import published files: %v", entries)
	}
}

func TestMusicAutomationSearchesAndUpgrades(t *testing.T) {
	env := newEnv(t, []fakeRelease{
		{ID: "rel-flac-1", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20},
		{ID: "rel-mp3-1", Title: "Muse - Absolution (2003) [MP3 320]", Size: 120 << 20},
	}, "Muse", "Absolution")
	ctx := context.Background()
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	// The first sync grabs a release for the wanted album.
	result, err := env.service.Sync(ctx, true)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Queued != 1 {
		t.Fatalf("sync result = %+v", result)
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 {
		t.Fatalf("acquisitions = %+v, %v", acquisitions, err)
	}
	if acquisitions[0].ReleaseID != "rel-flac-1" && acquisitions[0].ReleaseID != "rel-mp3-1" {
		t.Fatalf("unexpected release %q", acquisitions[0].ReleaseID)
	}
}

// TestMusicImportFailureStopsAfterBoundedRetries covers releases that must not retry forever.
func TestMusicImportFailureStopsAfterBoundedRetries(t *testing.T) {
	env := newEnv(t, []fakeRelease{{ID: "rel-flac-3", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20}}, "Muse", "Absolution")
	ctx := context.Background()
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)
	job, err := env.service.Grab(ctx, album.ID, "rel-flac-3", false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	output := filepath.Join(env.dir, "downloads", job.ID, "output")
	writeTrackFile(t, filepath.Join(output, "Muse - Absolution (2003)", "01 - Intro.flac"), 1)
	writeTrackFile(t, filepath.Join(output, "Other Band - Another Album (2011)", "01 - Song.flac"), 2)
	if _, err := env.pool.Exec(ctx, `UPDATE downloads SET status = 'completed' WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("complete download: %v", err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := env.service.SyncDownloads(ctx); err == nil {
			t.Fatalf("attempt %d imported an ambiguous release", attempt+1)
		}
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 {
		t.Fatalf("acquisitions = %+v, %v", acquisitions, err)
	}
	if acquisitions[0].Status != "failed" || acquisitions[0].Attempts < 3 {
		t.Fatalf("acquisition after repeated failures = %+v", acquisitions[0])
	}
	blocked, err := env.service.Store.Blocked(ctx, album.ID, "rel-flac-3")
	if err != nil || !blocked {
		t.Fatalf("failed import was not blocklisted: %v, %v", blocked, err)
	}
	view, err := env.service.Album(ctx, album.ID)
	if err != nil || view.Status != "failed" {
		t.Fatalf("album status = %q, %v; want failed", view.Status, err)
	}
}

// TestMusicQualityUpgradeGrabsBetterRelease covers automatic upgrades for lossy files.
func TestMusicQualityUpgradeGrabsBetterRelease(t *testing.T) {
	env := newEnv(t, []fakeRelease{
		{ID: "rel-flac-2", Title: "Muse - Absolution (2003) [FLAC] [24bit]", Size: 500 << 20},
		{ID: "rel-mp3-2", Title: "Muse - Absolution (2003) [MP3 320]", Size: 120 << 20},
	}, "Muse", "Absolution")
	ctx := context.Background()
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	// Existing lossy files with an upgrade profile make the album eligible for a better release.
	current, err := env.service.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	current.Files = []music.File{{
		RootID: current.RootID, Path: "Muse/Absolution (2003)/01 Intro.flac", Size: 1024,
		Format: "mp3", BitrateKbps: 320, Score: 100, Disc: 1, Number: 1, TrackTitle: "Intro",
		ImportedAt: time.Now().UTC(),
	}}
	current.Monitored = true
	if _, err := env.service.Store.SaveAlbum(ctx, current); err != nil {
		t.Fatalf("SaveAlbum: %v", err)
	}
	writeTrackFile(t, filepath.Join(env.libraryRoot(t), "Muse", "Absolution (2003)", "01 Intro.flac"), 1)
	upgraded, err := env.service.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if upgraded.Status != "cutoff-unmet" {
		t.Fatalf("album status with lossy files = %q, want cutoff-unmet", upgraded.Status)
	}
	releases, err := env.service.Search(ctx, album.ID)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) == 0 || releases[0].Decision.Format != "flac" || !releases[0].Decision.Upgrade {
		t.Fatalf("best release = %+v", releases)
	}
	result, err := env.service.Sync(ctx, true)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Queued != 1 {
		t.Fatalf("sync result = %+v", result)
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].ReleaseID != "rel-flac-2" {
		t.Fatalf("upgrade acquisition = %+v, %v", acquisitions, err)
	}
}

func TestMusicFailedDownloadIsBlocked(t *testing.T) {
	env := newEnv(t, []fakeRelease{
		{ID: "rel-a", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20},
		{ID: "rel-b", Title: "Muse - Absolution (2003) [FLAC] Repack", Size: 401 << 20},
	}, "Muse", "Absolution")
	ctx := context.Background()
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)
	job, err := env.service.Grab(ctx, album.ID, "rel-a", false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE downloads SET status = 'failed', error = 'verification failed' WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("fail download: %v", err)
	}
	if _, err := env.service.SyncDownloads(ctx); err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].Status != "failed" {
		t.Fatalf("acquisition after failure = %+v, %v", acquisitions, err)
	}
	blocked, err := env.service.Store.Blocked(ctx, album.ID, "rel-a")
	if err != nil || !blocked {
		t.Fatalf("failed release blocked = %v, %v", blocked, err)
	}
	releases, err := env.service.Search(ctx, album.ID)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, release := range releases {
		if release.ID == "rel-a" && release.Decision.Allowed {
			t.Fatalf("blocked release was offered again: %+v", release)
		}
	}
	if _, err := env.service.Grab(ctx, album.ID, "rel-a", false); !errors.Is(err, music.ErrConflict) {
		t.Fatalf("grabbing a blocked release error = %v, want ErrConflict", err)
	}
}

func TestMusicManualImportAndRename(t *testing.T) {
	env := newEnv(t, nil, "Muse", "Absolution")
	ctx := context.Background()
	cfg := env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	source := filepath.Join(env.dir, "manual")
	writeTrackFile(t, filepath.Join(source, "Muse - Absolution (2003)", "01 - Intro.flac"), 1)
	writeTrackFile(t, filepath.Join(source, "Muse - Absolution (2003)", "02 - Apocalypse Please.flac"), 2)

	imported, err := env.service.Import(ctx, music.ImportInput{AlbumID: album.ID, Path: filepath.Join(source, "Muse - Absolution (2003)")})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(imported.Files) != 2 {
		t.Fatalf("imported files = %d", len(imported.Files))
	}
	preview, err := env.service.Rename(ctx, album.ID, true)
	if err != nil {
		t.Fatalf("Rename preview: %v", err)
	}
	if len(preview.Files) != 0 || preview.Applied {
		t.Fatalf("preview = %+v, want no changes for already named files", preview)
	}
	updated, err := env.service.UpdateAlbum(ctx, album.ID, music.Album{Title: "Absolution (Deluxe)", Monitored: true})
	if err != nil {
		t.Fatalf("UpdateAlbum: %v", err)
	}
	plan, err := env.service.Rename(ctx, updated.ID, true)
	if err != nil {
		t.Fatalf("Rename preview after rename: %v", err)
	}
	if len(plan.Files) == 0 {
		t.Fatal("rename preview did not notice the new album title")
	}
	applied, err := env.service.Rename(ctx, updated.ID, false)
	if err != nil || !applied.Applied {
		t.Fatalf("Rename = %+v, %v", applied, err)
	}
	after, err := env.service.Album(ctx, updated.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	for _, file := range after.Files {
		if !strings.Contains(file.Path, "Absolution (Deluxe)") {
			t.Fatalf("file was not renamed: %s", file.Path)
		}
		if _, err := os.Stat(filepath.Join(cfg.RootFolders[0].Path, filepath.FromSlash(file.Path))); err != nil {
			t.Fatalf("renamed file missing: %v", err)
		}
	}
}

func TestMusicRoutesAndConfig(t *testing.T) {
	env := newEnv(t, nil, "Muse", "Absolution")
	ctx := context.Background()
	cfg := env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	mux := http.NewServeMux()
	env.service.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(resp.Body)
		return resp, body.String()
	}
	resp, body := get("/api/v1/music/albums")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Absolution") {
		t.Fatalf("albums response = %d %s", resp.StatusCode, body)
	}
	if resp, _ := get("/api/v1/music/albums/missing-album"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing album status = %d", resp.StatusCode)
	}
	if resp, _ := get("/api/v1/music/search"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty search status = %d", resp.StatusCode)
	}
	if resp, body := get("/api/v1/music/config"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "folderTemplate") {
		t.Fatalf("config response = %d %s", resp.StatusCode, body)
	}

	post := func(path, body string) *http.Response {
		t.Helper()
		resp, err := http.Post(server.URL+path, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp
	}
	if resp := post("/api/v1/music/albums/"+album.ID+"/grab", `{"releaseId":"rel-1","unknown":true}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", resp.StatusCode)
	}
	if resp := post("/api/v1/music/albums/"+album.ID+"/grab", `{"releaseId":"rel-1","unknown":true}{"second":1}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("trailing document status = %d", resp.StatusCode)
	}
	if resp := post("/api/v1/music/albums/"+album.ID+"/refresh", `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("album without a MusicBrainz ID status = %d", resp.StatusCode)
	}
	if resp := post("/api/v1/music/scan", `{"rootId":"missing"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown root status = %d", resp.StatusCode)
	}

	// Invalid configuration is rejected without touching the stored one.
	broken := cfg
	broken.FolderTemplate = "{title}/{track}"
	if _, err := env.service.SetConfig(ctx, broken); !errors.Is(err, music.ErrTemplate) && !errors.Is(err, music.ErrInvalid) {
		t.Fatalf("invalid template error = %v", err)
	}
	broken = cfg
	broken.RootFolders = nil
	if _, err := env.service.SetConfig(ctx, broken); !errors.Is(err, music.ErrInvalid) {
		t.Fatalf("empty roots error = %v", err)
	}
	view, err := env.service.ConfigView(ctx)
	if err != nil || view.FolderTemplate != cfg.FolderTemplate {
		t.Fatalf("config after rejected writes = %+v, %v", view, err)
	}
	if !view.FFprobeAvailable {
		t.Fatal("ffprobe was reported unavailable in the test environment")
	}
}

func (e *env) libraryRoot(t *testing.T) string {
	t.Helper()
	cfg, err := e.service.ConfigView(context.Background())
	if err != nil || len(cfg.RootFolders) == 0 {
		t.Fatalf("ConfigView: %v", err)
	}
	roots := append([]music.RootFolder(nil), cfg.RootFolders...)
	sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
	return roots[0].Path
}

// writeTrackFile writes a real silent WAV so ffprobe accepts the imported file as audio.
func writeTrackFile(t *testing.T, path string, seed int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const sampleRate = 8000
	samples := sampleRate / 20
	dataSize := samples * 2
	var buffer bytes.Buffer
	buffer.WriteString("RIFF")
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(36+dataSize))
	buffer.WriteString("WAVE")
	buffer.WriteString("fmt ")
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(sampleRate))
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(sampleRate*2))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(2))
	_ = binary.Write(&buffer, binary.LittleEndian, uint16(16))
	buffer.WriteString("data")
	_ = binary.Write(&buffer, binary.LittleEndian, uint32(dataSize))
	payload := make([]byte, dataSize)
	for i := range payload {
		payload[i] = byte(seed)
	}
	buffer.Write(payload)
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}
