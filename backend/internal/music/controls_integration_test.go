package music_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/music"
)

// Paused and cancelled albums keep their state and only an explicit grab may restart them.
func TestMusicPausedAndCancelledDownloadsStayStopped(t *testing.T) {
	ctx := context.Background()
	releases := []fakeRelease{
		{ID: "rel-flac-1", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20},
		{ID: "rel-flac-2", Title: "Muse - Absolution (2003) [FLAC] [Deluxe]", Size: 500 << 20},
	}
	env := newEnv(t, releases, "Muse", "Absolution")
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	job, err := env.service.Grab(ctx, album.ID, "rel-flac-1", false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if _, err := env.manager.Pause(ctx, job.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if saved := musicAlbum(t, env, album.ID); saved.Status != "paused" {
		t.Fatalf("album after pause = %q, want paused", saved.Status)
	}
	if artist, err := env.service.Artist(ctx, album.ArtistID); err != nil || artist.Status != "paused" {
		t.Fatalf("artist after pause = %q, %v, want paused", artist.Status, err)
	}
	if result, err := env.service.Sync(ctx, true); err != nil || result.Queued != 0 {
		t.Fatalf("automation restarted a paused album: %+v, %v", result, err)
	}
	if _, err := env.service.Grab(ctx, album.ID, "rel-flac-2", false); !errors.Is(err, music.ErrConflict) {
		t.Fatalf("paused album accepted another release: %v, want ErrConflict", err)
	}

	if _, err := env.manager.Resume(ctx, job.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if saved := musicAlbum(t, env, album.ID); saved.Status != "downloading" {
		t.Fatalf("album after resume = %q, want downloading", saved.Status)
	}

	if _, err := env.manager.Cancel(ctx, job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if saved := musicAlbum(t, env, album.ID); saved.Status != "cancelled" {
		t.Fatalf("album after cancellation = %q, want cancelled", saved.Status)
	}
	if artist, err := env.service.Artist(ctx, album.ArtistID); err != nil || artist.Status != "cancelled" {
		t.Fatalf("artist after cancellation = %q, %v, want cancelled", artist.Status, err)
	}
	if result, err := env.service.Sync(ctx, true); err != nil || result.Queued != 0 {
		t.Fatalf("automation requeued a cancelled album: %+v, %v", result, err)
	}

	retried, err := env.service.Grab(ctx, album.ID, "rel-flac-1", false)
	if err != nil || retried.ID != job.ID || retried.Status != "queued" {
		t.Fatalf("explicit retry of a cancelled release = %+v, %v", retried, err)
	}
}

// A paused or cancelled upgrade download keeps the album stopped instead of scheduling another release.
func TestMusicPausedUpgradeStaysStopped(t *testing.T) {
	ctx := context.Background()
	env := newEnv(t, []fakeRelease{{ID: "rel-flac-1", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20}}, "Muse", "Absolution")
	cfg := env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	root := cfg.RootFolders[0]
	path := "Muse/Absolution (2003)/01 - Intro.mp3"
	writeTrackFile(t, filepath.Join(root.Path, filepath.FromSlash(path)), 1)
	if err := env.service.Store.PatchAlbumData(ctx, album.ID, map[string]any{"files": []music.File{{
		RootID: root.ID, Path: path, Size: 1024, Format: "mp3", BitrateKbps: 320, ImportedAt: time.Now().UTC(),
	}}}); err != nil {
		t.Fatalf("PatchAlbumData: %v", err)
	}
	if saved := musicAlbum(t, env, album.ID); saved.Status != "cutoff-unmet" {
		t.Fatalf("album with a below-cutoff file = %q, want cutoff-unmet", saved.Status)
	}

	job, err := env.service.Grab(ctx, album.ID, "rel-flac-1", false)
	if err != nil {
		t.Fatalf("Grab upgrade: %v", err)
	}
	if _, err := env.manager.Pause(ctx, job.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if saved := musicAlbum(t, env, album.ID); saved.Status != "paused" {
		t.Fatalf("album after pausing an upgrade = %q, want paused", saved.Status)
	}
	if result, err := env.service.Sync(ctx, true); err != nil || result.Queued != 0 || result.Searched != 0 {
		t.Fatalf("automation scheduled another upgrade while paused: %+v, %v", result, err)
	}
	if _, err := env.manager.Cancel(ctx, job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if result, err := env.service.Sync(ctx, true); err != nil || result.Queued != 0 || result.Searched != 0 {
		t.Fatalf("automation rescheduled a cancelled upgrade: %+v, %v", result, err)
	}
}

// A newer import outranks an older cancelled acquisition instead of leaving the album cancelled.
func TestMusicCancelledAcquisitionLosesToNewerImport(t *testing.T) {
	ctx := context.Background()
	releases := []fakeRelease{
		{ID: "rel-flac-1", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20},
		{ID: "rel-flac-2", Title: "Muse - Absolution (2003) [FLAC] [24bit]", Size: 900 << 20},
	}
	env := newEnv(t, releases, "Muse", "Absolution")
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	cancelled, err := env.service.Grab(ctx, album.ID, "rel-flac-1", false)
	if err != nil {
		t.Fatalf("Grab cancelled release: %v", err)
	}
	if _, err := env.manager.Cancel(ctx, cancelled.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if saved := musicAlbum(t, env, album.ID); saved.Status != "cancelled" {
		t.Fatalf("album before replacement = %q, want cancelled", saved.Status)
	}

	replacement, err := env.service.Grab(ctx, album.ID, "rel-flac-2", false)
	if err != nil {
		t.Fatalf("Grab replacement: %v", err)
	}
	writeTrackFile(t, filepath.Join(env.dir, "downloads", replacement.ID, "output", "01 - Intro.flac"), 1)
	if _, err := env.pool.Exec(ctx, `UPDATE downloads SET status = 'completed' WHERE id = $1`, replacement.ID); err != nil {
		t.Fatalf("complete replacement download: %v", err)
	}
	if imported, err := env.service.SyncDownloads(ctx); err != nil || imported != 1 {
		t.Fatalf("SyncDownloads = %d, %v, want the newer release imported", imported, err)
	}

	saved := musicAlbum(t, env, album.ID)
	if saved.Status == "cancelled" || len(saved.Files) == 0 {
		t.Fatalf("album after newer import = %q with %d files, want the import to win", saved.Status, len(saved.Files))
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 2 {
		t.Fatalf("acquisitions = %+v, %v", acquisitions, err)
	}
	if acquisitions[0].JobID != replacement.ID || acquisitions[0].Status != "imported" {
		t.Fatalf("newest acquisition = %+v, want the imported replacement", acquisitions[0])
	}
	if acquisitions[1].JobID != cancelled.ID || acquisitions[1].Status != "cancelled" {
		t.Fatalf("older acquisition = %+v, want the cancelled release preserved", acquisitions[1])
	}
}

// A cancelled album stays visible and stopped even when newer acquisitions crowd the recent window.
func TestMusicCancelledAlbumSurvivesAcquisitionWindow(t *testing.T) {
	ctx := context.Background()
	env := newEnv(t, []fakeRelease{{ID: "rel-flac-1", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20}}, "Muse", "Absolution")
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	job, err := env.service.Grab(ctx, album.ID, "rel-flac-1", false)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	if _, err := env.manager.Cancel(ctx, job.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	seedRecentAcquisitions(t, env, 200)

	albums, err := env.service.Albums(ctx)
	if err != nil {
		t.Fatalf("Albums: %v", err)
	}
	if saved := albumByID(albums, album.ID); saved.Status != "cancelled" {
		t.Fatalf("album status outside the recent window = %q, want cancelled", saved.Status)
	}
	if result, err := env.service.Sync(ctx, true); err != nil || result.Queued != 0 {
		t.Fatalf("automation requeued a cancelled album outside the window: %+v, %v", result, err)
	}
}

// seedRecentAcquisitions pushes one album's acquisitions out of the 200 newest rows in the library.
func seedRecentAcquisitions(t *testing.T, env *env, count int) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`INSERT INTO downloads (id, release_id, title, nzb, status)
		 SELECT 'pad-job-' || g, 'pad-release-' || g, 'Pad', '\x00'::bytea, 'queued' FROM generate_series(1, $1) g`,
		`INSERT INTO music_artists (id, name, data)
		 SELECT 'pad-artist-' || g, 'Pad', '{}'::jsonb FROM generate_series(1, $1) g`,
		`INSERT INTO music_albums (id, artist_id, title, data)
		 SELECT 'pad-album-' || g, 'pad-artist-' || g, 'Pad', '{}'::jsonb FROM generate_series(1, $1) g`,
		`INSERT INTO music_acquisitions (job_id, album_id, release, status, updated_at)
		 SELECT 'pad-job-' || g, 'pad-album-' || g, '{}'::jsonb, 'queued', now() + (g * interval '1 second')
		 FROM generate_series(1, $1) g`,
	}
	for _, statement := range statements {
		if _, err := env.pool.Exec(ctx, statement, count); err != nil {
			t.Fatalf("seed recent acquisitions: %v", err)
		}
	}
}

func musicAlbum(t *testing.T, env *env, albumID string) music.Album {
	t.Helper()
	album, err := env.service.Album(context.Background(), albumID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	return album
}

func albumByID(albums []music.Album, albumID string) music.Album {
	for _, album := range albums {
		if album.ID == albumID {
			return album
		}
	}
	return music.Album{}
}
