package music_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
)

// fakeTorrentSource is a torrent indexer plus client used to exercise torrent acquisitions.
type fakeTorrentSource struct {
	mu       sync.Mutex
	root     string
	releases []indexer.Release
	jobs     map[string]downloads.Job
	fail     bool
}

func newFakeTorrents(t *testing.T, releases []indexer.Release) *fakeTorrentSource {
	t.Helper()
	return &fakeTorrentSource{root: t.TempDir(), releases: releases, jobs: map[string]downloads.Job{}}
}

func fold(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func (f *fakeTorrentSource) Search(_ context.Context, query string) ([]indexer.Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("torrents: every indexer failed")
	}
	needle := fold(query)
	items := make([]indexer.Release, 0, len(f.releases))
	for _, release := range f.releases {
		if needle == "" || strings.Contains(fold(release.Title), needle) {
			items = append(items, release)
		}
	}
	return items, nil
}

func (f *fakeTorrentSource) Add(_ context.Context, releaseID, title string) (downloads.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job := downloads.Job{
		ID: "torrent-job-" + strconv.Itoa(len(f.jobs)+1), ReleaseID: releaseID, Title: title,
		Protocol: "torrent", Status: "downloading", BytesTotal: 1024, Files: []downloads.OutputFile{},
	}
	f.jobs[job.ID] = job
	return job, nil
}

func (f *fakeTorrentSource) Get(_ context.Context, id string) (downloads.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return downloads.Job{}, downloads.ErrNotFound
	}
	return job, nil
}

// Retry mirrors the real adapter: failed or cancelled work is requeued through Resume.
func (f *fakeTorrentSource) Retry(_ context.Context, id string) (downloads.Job, error) {
	return f.setStatus(id, "downloading")
}

func (f *fakeTorrentSource) Pause(_ context.Context, id string) (downloads.Job, error) {
	return f.setStatus(id, "paused")
}

func (f *fakeTorrentSource) Resume(_ context.Context, id string) (downloads.Job, error) {
	return f.setStatus(id, "downloading")
}

// Cancel keeps the job and its data, matching the download manager's contract.
func (f *fakeTorrentSource) Cancel(_ context.Context, id string) (downloads.Job, error) {
	return f.setStatus(id, "cancelled")
}

func (f *fakeTorrentSource) setStatus(id, status string) (downloads.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return downloads.Job{}, downloads.ErrNotFound
	}
	job.Status = status
	f.jobs[id] = job
	return job, nil
}

func (f *fakeTorrentSource) OutputDirectory(id string) (string, error) {
	return filepath.Join(f.root, id), nil
}

func (f *fakeTorrentSource) complete(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return
	}
	job.Status = "completed"
	f.jobs[id] = job
}

func (f *fakeTorrentSource) withhold() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = true
}

func torrentIndexerRelease(id, title, source string, seeders int, size int64) indexer.Release {
	return indexer.Release{
		ID: downloads.TorrentPrefix + source + "_" + id, Title: title, Size: size,
		Protocol: "torrent", Source: source, Seeders: seeders,
	}
}

func TestMusicSearchMergesUsenetAndTorrentProviders(t *testing.T) {
	env := newEnv(t, []fakeRelease{{ID: "rel-flac-usenet", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20}}, "Muse", "Absolution")
	torrents := newFakeTorrents(t, []indexer.Release{
		torrentIndexerRelease("aaaaaaaaaaaaaaaaaaaa", "Muse - Absolution (2003) [FLAC] [24bit]", "alpha", 30, 450<<20),
		torrentIndexerRelease("bbbbbbbbbbbbbbbbbbbb", "Some.Movie.2020.1080p.BluRay.x264-GROUP", "alpha", 90, 8<<30),
		torrentIndexerRelease("cccccccccccccccccccc", "Muse - Absolution (2003) [MP3 320]", "alpha", 0, 120<<20),
	})
	env.manager.SetTorrents(torrents)
	ctx := context.Background()
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	releases, err := env.service.Search(ctx, album.ID)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	byID := map[string]music.Release{}
	for _, release := range releases {
		byID[release.ID] = release
	}
	if _, found := byID["rel-flac-usenet"]; !found {
		t.Fatalf("usenet release missing from the merged search: %+v", releases)
	}
	flac, found := byID["torrent_alpha_aaaaaaaaaaaaaaaaaaaa"]
	if !found {
		t.Fatalf("torrent release missing from the merged search: %+v", releases)
	}
	if flac.Protocol != "torrent" || flac.Source != "alpha" || flac.Seeders != 30 {
		t.Fatalf("torrent release fields = %+v", flac)
	}
	if !flac.Decision.Allowed {
		t.Fatalf("healthy torrent was rejected: %+v", flac.Decision)
	}
	if _, found := byID["torrent_alpha_bbbbbbbbbbbbbbbbbbbb"]; found {
		t.Fatal("a video torrent was offered as music")
	}
	dead, found := byID["torrent_alpha_cccccccccccccccccccc"]
	if !found {
		t.Fatalf("seederless torrent was dropped instead of explained: %+v", releases)
	}
	if dead.Decision.Allowed || !strings.Contains(strings.Join(dead.Decision.Reasons, "; "), "no seeders") {
		t.Fatalf("seederless torrent decision = %+v", dead.Decision)
	}

	// The torrent indexers alone still answer when the Newznab provider is unreachable.
	env.indexer.Close()
	partial, err := env.service.Search(ctx, album.ID)
	if err != nil {
		t.Fatalf("partial search failed: %v", err)
	}
	if len(partial) == 0 || partial[0].Protocol != "torrent" {
		t.Fatalf("partial results = %+v", partial)
	}

	// With every provider failing the caller gets the provider errors.
	torrents.withhold()
	if _, err := env.service.Search(ctx, album.ID); err == nil || !strings.Contains(err.Error(), "torrents") {
		t.Fatalf("total failure error = %v", err)
	}
}

// TestMusicTorrentImportKeepsPayloadSeedable proves a completed torrent imports as a hardlink.
func TestMusicTorrentImportKeepsPayloadSeedable(t *testing.T) {
	env := newEnv(t, nil, "Muse", "Absolution")
	releaseID := downloads.TorrentPrefix + "alpha_dddddddddddddddddddd"
	torrents := newFakeTorrents(t, []indexer.Release{
		torrentIndexerRelease("dddddddddddddddddddd", "Muse - Absolution (2003) [FLAC]", "alpha", 25, 400<<20),
	})
	env.manager.SetTorrents(torrents)
	ctx := context.Background()
	cfg := env.libraryConfig(t)
	cfg.ImportMode = "move"
	if _, err := env.service.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	album := env.addAlbum(t, "Muse", "Absolution", true)

	job, err := env.service.Grab(ctx, album.ID, releaseID, false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if job.Protocol != "torrent" {
		t.Fatalf("job protocol = %q", job.Protocol)
	}
	output, err := torrents.OutputDirectory(job.ID)
	if err != nil {
		t.Fatalf("OutputDirectory: %v", err)
	}
	payload := []string{"01 - Intro.flac", "02 - Apocalypse Please.flac"}
	for index, name := range payload {
		writeTrackFile(t, filepath.Join(output, "Muse - Absolution (2003)", name), index+1)
	}
	torrents.complete(job.ID)

	mode, err := env.manager.ImportMode(ctx, job.ID, "move")
	if err != nil || mode != "hardlink" {
		t.Fatalf("ImportMode = %q, %v; want hardlink for a torrent", mode, err)
	}
	imported, err := env.service.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("SyncDownloads = %d, %v", imported, err)
	}
	saved, err := env.service.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if len(saved.Files) != 2 || saved.Status == "wanted" || saved.Status == "failed" {
		t.Fatalf("album after torrent import = %d files, status %q", len(saved.Files), saved.Status)
	}
	libraryRoot := cfg.RootFolders[0].Path
	payloadDir := filepath.Join(output, "Muse - Absolution (2003)")
	payloadEntries, err := os.ReadDir(payloadDir)
	if err != nil || len(payloadEntries) != 2 {
		t.Fatalf("the torrent payload was not preserved: %d entries, %v", len(payloadEntries), err)
	}
	for _, file := range saved.Files {
		dest := filepath.Join(libraryRoot, filepath.FromSlash(file.Path))
		destInfo, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("imported file %s: %v", file.Path, err)
		}
		linked := false
		for _, entry := range payloadEntries {
			payloadInfo, err := os.Stat(filepath.Join(payloadDir, entry.Name()))
			if err == nil && os.SameFile(payloadInfo, destInfo) {
				linked = true
				break
			}
		}
		if !linked {
			t.Fatalf("%s is not a hardlink of the torrent payload", dest)
		}
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].Status != "imported" {
		t.Fatalf("acquisition = %+v, %v", acquisitions, err)
	}
	if adopted, mediaType := env.downloadRow(t, job.ID); !adopted || mediaType != "music" {
		t.Fatalf("torrent download ownership = %v, %q", adopted, mediaType)
	}
}

// TestMusicManualImportProtectsTorrentPayload covers manual imports from a torrent folder.
func TestMusicManualImportProtectsTorrentPayload(t *testing.T) {
	env := newEnv(t, nil, "Muse", "Absolution")
	releaseID := downloads.TorrentPrefix + "alpha_eeeeeeeeeeeeeeeeeeee"
	torrents := newFakeTorrents(t, []indexer.Release{
		torrentIndexerRelease("eeeeeeeeeeeeeeeeeeee", "Muse - Absolution (2003) [FLAC]", "alpha", 12, 400<<20),
	})
	env.manager.SetTorrents(torrents)
	ctx := context.Background()
	cfg := env.libraryConfig(t)
	cfg.ImportMode = "move"
	if _, err := env.service.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	album := env.addAlbum(t, "Muse", "Absolution", true)
	job, err := env.service.Grab(ctx, album.ID, releaseID, false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	output, err := torrents.OutputDirectory(job.ID)
	if err != nil {
		t.Fatalf("OutputDirectory: %v", err)
	}
	source := filepath.Join(output, "Muse - Absolution (2003)")
	writeTrackFile(t, filepath.Join(source, "01 - Intro.flac"), 1)
	torrents.complete(job.ID)

	jobs, err := env.manager.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("the shared download list must stay usenet-only: %+v", jobs)
	}

	imported, err := env.service.Import(ctx, music.ImportInput{AlbumID: album.ID, Path: source})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(imported.Files) != 1 {
		t.Fatalf("imported files = %d", len(imported.Files))
	}
	payloadInfo, err := os.Stat(filepath.Join(source, "01 - Intro.flac"))
	if err != nil {
		t.Fatalf("the torrent payload was removed by a manual import: %v", err)
	}
	destInfo, err := os.Stat(filepath.Join(cfg.RootFolders[0].Path, filepath.FromSlash(imported.Files[0].Path)))
	if err != nil || !os.SameFile(payloadInfo, destInfo) {
		t.Fatalf("manual import did not hardlink the payload: %v", err)
	}
}
