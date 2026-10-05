package music_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/music"
)

// TestMusicAutomationKeepsConcurrentImportAndMonitorChanges overlaps a slow search with an import and a monitor edit.
func TestMusicAutomationKeepsConcurrentImportAndMonitorChanges(t *testing.T) {
	gate := newSearchGate()
	// No feed releases: the album search is the only provider call, and it is gated.
	env := newEnv(t, nil, "Muse", "Absolution", gate)
	t.Cleanup(gate.letGo)
	ctx := context.Background()
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)
	gate.arm()

	done := make(chan music.SyncResult, 1)
	failures := make(chan error, 1)
	go func() {
		result, err := env.service.Sync(ctx, true)
		if err != nil {
			failures <- err
			return
		}
		done <- result
	}()
	select {
	case <-gate.entered:
	case err := <-failures:
		t.Fatalf("Sync finished before the search was held: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("the album search never reached the provider")
	}

	// While the search is in flight: files arrive and the user changes monitoring.
	source := filepath.Join(env.dir, "overlap-import")
	writeTrackFile(t, filepath.Join(source, "01 - Intro.flac"), 1)
	writeTrackFile(t, filepath.Join(source, "02 - Apocalypse Please.flac"), 2)
	if _, err := env.service.Import(ctx, music.ImportInput{AlbumID: album.ID, Path: source}); err != nil {
		t.Fatalf("concurrent Import: %v", err)
	}
	if _, err := env.service.MonitorAlbum(ctx, album.ID, music.AlbumMonitorInput{Monitored: false, ProfileID: "lossless"}); err != nil {
		t.Fatalf("concurrent MonitorAlbum: %v", err)
	}
	gate.letGo()
	select {
	case result := <-done:
		if result.Searched != 1 {
			t.Fatalf("the automation did not run the album search: %+v", result)
		}
	case err := <-failures:
		t.Fatalf("Sync: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("Sync did not finish after the search was released")
	}

	saved, err := env.service.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if len(saved.Files) != 2 {
		t.Fatalf("concurrent import was lost: %d files", len(saved.Files))
	}
	if saved.Monitored || saved.ProfileID != "lossless" {
		t.Fatalf("concurrent monitor edit was lost: monitored=%v profile=%q", saved.Monitored, saved.ProfileID)
	}
	// The import cleared the throttle; a stale snapshot must not restore the earlier stamp.
	if saved.LastSearchAt != nil {
		t.Fatalf("automation snapshot replaced the concurrent import state: %+v", saved.LastSearchAt)
	}
	for _, file := range saved.Files {
		if _, err := os.Stat(filepath.Join(env.libraryRoot(t), filepath.FromSlash(file.Path))); err != nil {
			t.Fatalf("imported file missing on disk: %v", err)
		}
	}
}

// TestMusicImportKeepsConcurrentMonitorChange pauses the import inside its probe with a gated ffprobe helper.
func TestMusicImportKeepsConcurrentMonitorChange(t *testing.T) {
	realFFprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is unavailable")
	}
	gateDir := t.TempDir()
	enteredPath := filepath.Join(gateDir, "entered.fifo")
	releasePath := filepath.Join(gateDir, "release.fifo")
	for _, path := range []string{enteredPath, releasePath} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Skipf("named pipes are unavailable: %v", err)
		}
	}
	script := filepath.Join(gateDir, "ffprobe-gate.sh")
	scriptBody := "#!/bin/sh\nprintf . > \"$MUSIC_TEST_GATE_ENTERED\"\nread -r _ < \"$MUSIC_TEST_GATE_RELEASE\"\nexec \"$MUSIC_TEST_FFPROBE_REAL\" \"$@\"\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write gate helper: %v", err)
	}
	t.Setenv("MUSIC_TEST_GATE_ENTERED", enteredPath)
	t.Setenv("MUSIC_TEST_GATE_RELEASE", releasePath)
	t.Setenv("MUSIC_TEST_FFPROBE_REAL", realFFprobe)

	env := newEnv(t, nil, "Muse", "Absolution")
	ctx := context.Background()
	cfg := env.libraryConfig(t)
	cfg.FFprobePath = script
	if _, err := env.service.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	album := env.addAlbum(t, "Muse", "Absolution", true)

	source := filepath.Join(env.dir, "gated-import")
	writeTrackFile(t, filepath.Join(source, "01 - Intro.flac"), 1)
	imported := make(chan error, 1)
	go func() {
		_, err := env.service.Import(ctx, music.ImportInput{AlbumID: album.ID, Path: source})
		imported <- err
	}()

	// The helper signals from the pipe, so the import is provably paused before the edit.
	entered, err := os.OpenFile(enteredPath, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open entered pipe: %v", err)
	}
	_ = entered.Close()
	if _, err := env.service.MonitorAlbum(ctx, album.ID, music.AlbumMonitorInput{Monitored: false, ProfileID: "lossless"}); err != nil {
		t.Fatalf("concurrent MonitorAlbum: %v", err)
	}
	release, err := os.OpenFile(releasePath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open release pipe: %v", err)
	}
	_, _ = release.Write([]byte("go"))
	_ = release.Close()
	if err := <-imported; err != nil {
		t.Fatalf("Import: %v", err)
	}

	saved, err := env.service.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if len(saved.Files) != 1 {
		t.Fatalf("import did not record its file: %+v", saved.Files)
	}
	if saved.Monitored || saved.ProfileID != "lossless" {
		t.Fatalf("import overwrote the monitoring edit: monitored=%v profile=%q", saved.Monitored, saved.ProfileID)
	}
	if saved.Error != "" {
		t.Fatalf("album error was not cleared by the import: %q", saved.Error)
	}
}

// TestMusicAlbumRefreshKeepsConcurrentChanges overlaps a slow refresh with an import and a monitor edit.
func TestMusicAlbumRefreshKeepsConcurrentChanges(t *testing.T) {
	gate := newSearchGate()
	env := newEnv(t, nil, "Muse", "Absolution")
	env.musicBrain = newBrainzFixture(t, gate).URL + "/ws/2/"
	t.Cleanup(gate.letGo)
	ctx := context.Background()
	env.libraryConfig(t)

	album, err := env.service.AddReleaseGroup(ctx, music.AddReleaseGroupInput{MusicBrainzID: fixtureGroupMBID, Title: "Absolution"})
	if err != nil {
		t.Fatalf("AddReleaseGroup: %v", err)
	}
	gate.arm()

	refreshed := make(chan error, 1)
	go func() {
		_, err := env.service.RefreshAlbum(ctx, album.ID)
		refreshed <- err
	}()
	select {
	case <-gate.entered:
	case err := <-refreshed:
		t.Fatalf("the refresh finished before the provider was held: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("the album refresh never reached the provider")
	}

	source := filepath.Join(env.dir, "refresh-overlap")
	writeTrackFile(t, filepath.Join(source, "01 - Intro.flac"), 1)
	writeTrackFile(t, filepath.Join(source, "02 - Apocalypse Please.flac"), 2)
	if _, err := env.service.Import(ctx, music.ImportInput{AlbumID: album.ID, Path: source}); err != nil {
		t.Fatalf("concurrent Import: %v", err)
	}
	if _, err := env.service.MonitorAlbum(ctx, album.ID, music.AlbumMonitorInput{Monitored: false, ProfileID: "lossless"}); err != nil {
		t.Fatalf("concurrent MonitorAlbum: %v", err)
	}
	gate.letGo()
	select {
	case err := <-refreshed:
		if err != nil {
			t.Fatalf("RefreshAlbum: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the refresh did not finish after the provider was released")
	}

	saved, err := env.service.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if len(saved.Files) != 2 {
		t.Fatalf("the refresh replaced concurrent import files: %d files", len(saved.Files))
	}
	if saved.Monitored || saved.ProfileID != "lossless" {
		t.Fatalf("the refresh replaced the monitoring edit: monitored=%v profile=%q", saved.Monitored, saved.ProfileID)
	}
	if len(saved.Tracks) != 2 || saved.Title != "Absolution" {
		t.Fatalf("the refresh did not apply its own metadata: %+v", saved)
	}
	for _, file := range saved.Files {
		if _, err := os.Stat(filepath.Join(env.libraryRoot(t), filepath.FromSlash(file.Path))); err != nil {
			t.Fatalf("imported file missing on disk: %v", err)
		}
	}
}

// TestMusicSyncRefreshKeepsConcurrentChanges overlaps the automation refresh with an import and a monitor edit.
func TestMusicSyncRefreshKeepsConcurrentChanges(t *testing.T) {
	gate := newSearchGate()
	env := newEnv(t, nil, "Muse", "Absolution")
	env.musicBrain = newBrainzFixture(t, gate).URL + "/ws/2/"
	t.Cleanup(gate.letGo)
	ctx := context.Background()
	env.libraryConfig(t)

	album, err := env.service.AddReleaseGroup(ctx, music.AddReleaseGroupInput{MusicBrainzID: fixtureGroupMBID, Title: "Absolution"})
	if err != nil {
		t.Fatalf("AddReleaseGroup: %v", err)
	}
	gate.arm()

	done := make(chan error, 1)
	go func() {
		_, err := env.service.Sync(ctx, true)
		done <- err
	}()
	select {
	case <-gate.entered:
	case err := <-done:
		t.Fatalf("Sync finished before the provider was held: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("the automation refresh never reached the provider")
	}

	source := filepath.Join(env.dir, "sync-overlap")
	writeTrackFile(t, filepath.Join(source, "01 - Intro.flac"), 1)
	writeTrackFile(t, filepath.Join(source, "02 - Apocalypse Please.flac"), 2)
	if _, err := env.service.Import(ctx, music.ImportInput{AlbumID: album.ID, Path: source}); err != nil {
		t.Fatalf("concurrent Import: %v", err)
	}
	if _, err := env.service.MonitorAlbum(ctx, album.ID, music.AlbumMonitorInput{Monitored: false, ProfileID: "lossless"}); err != nil {
		t.Fatalf("concurrent MonitorAlbum: %v", err)
	}
	gate.letGo()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Sync did not finish after the provider was released")
	}

	saved, err := env.service.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if len(saved.Files) != 2 {
		t.Fatalf("the automation refresh replaced concurrent import files: %d files", len(saved.Files))
	}
	if saved.Monitored || saved.ProfileID != "lossless" {
		t.Fatalf("the automation refresh replaced the monitoring edit: monitored=%v profile=%q", saved.Monitored, saved.ProfileID)
	}
	if len(saved.Tracks) != 2 {
		t.Fatalf("tracks after the merged refresh = %+v", saved.Tracks)
	}
	for _, track := range saved.Tracks {
		if track.File == nil {
			t.Fatalf("the merged refresh dropped an imported track link: %+v", track)
		}
	}
}

// TestMusicPatchAlbumPreservesOtherFields covers the store primitive directly.
func TestMusicPatchAlbumPreservesOtherFields(t *testing.T) {
	env := newEnv(t, nil, "Muse", "Absolution")
	ctx := context.Background()
	env.libraryConfig(t)
	album := env.addAlbum(t, "Muse", "Absolution", true)

	if err := env.service.Store.PatchAlbumData(ctx, album.ID, map[string]any{"monitored": false, "profileId": "lossless"}); err != nil {
		t.Fatalf("patch monitored: %v", err)
	}
	files := []music.File{{RootID: album.RootID, Path: "Muse/Absolution (2003)/01 Intro.flac", Size: 128, Format: "flac", Score: 100, Disc: 1, Number: 1, TrackTitle: "Intro"}}
	if err := env.service.Store.PatchAlbum(ctx, album.ID,
		map[string]any{"files": files, "error": "", "lastSearchAt": nil}, nil); err != nil {
		t.Fatalf("patch files: %v", err)
	}
	if err := env.service.Store.PatchAlbumData(ctx, album.ID, map[string]any{"lastSearchAt": time.Now().UTC()}); err != nil {
		t.Fatalf("patch search stamp: %v", err)
	}
	saved, err := env.service.Album(ctx, album.ID)
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if saved.Monitored || saved.ProfileID != "lossless" || saved.Title != "Absolution" || saved.MusicBrainzID != "" {
		t.Fatalf("patch replaced unrelated fields: %+v", saved)
	}
	if len(saved.Files) != 1 || saved.Files[0].Path != files[0].Path {
		t.Fatalf("files patch = %+v", saved.Files)
	}
	if saved.LastSearchAt == nil {
		t.Fatal("search stamp was not stored")
	}
	if err := env.service.Store.PatchAlbumData(ctx, "missing-album", map[string]any{"error": "x"}); err != music.ErrNotFound {
		t.Fatalf("patching a missing album error = %v, want ErrNotFound", err)
	}

	artist, err := env.service.Artist(ctx, album.ArtistID)
	if err != nil {
		t.Fatalf("Artist: %v", err)
	}
	if err := env.service.Store.PatchArtistData(ctx, artist.ID, map[string]any{"monitored": false, "monitorOption": "none"}); err != nil {
		t.Fatalf("patch artist monitored: %v", err)
	}
	stamp := time.Now().UTC()
	if err := env.service.Store.PatchArtistData(ctx, artist.ID, map[string]any{"lastRefreshAt": stamp}); err != nil {
		t.Fatalf("patch artist refresh stamp: %v", err)
	}
	refreshed, err := env.service.Artist(ctx, artist.ID)
	if err != nil {
		t.Fatalf("Artist after patch: %v", err)
	}
	if refreshed.Monitored || refreshed.MonitorOption != "none" || refreshed.Name != "Muse" || refreshed.LastRefreshAt == nil {
		t.Fatalf("artist patch lost fields: %+v", refreshed)
	}
}
