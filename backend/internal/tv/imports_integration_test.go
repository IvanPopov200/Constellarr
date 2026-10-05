package tv_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

type tvEnv struct {
	service   *tv.Service
	manager   *downloads.Manager
	pool      *pgxpool.Pool
	directory string
	rootPath  string
}

func tvTestPool(t *testing.T) *pgxpool.Pool {
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
	schema := "tv_import_test_" + strings.ToLower(rand.Text())
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

func newTVEnv(t *testing.T, mode string, indexerURL ...string) *tvEnv {
	t.Helper()
	for _, name := range []string{"OMDB_URL", "OMDB_API_KEY", "JELLYFIN_URL", "JELLYFIN_API_KEY", "IMPORT_WEBHOOK_URL"} {
		t.Setenv(name, "")
	}
	pool := tvTestPool(t)
	directory := t.TempDir()
	ctx := context.Background()
	downloadConfig := downloads.Config{Directory: directory}
	if len(indexerURL) > 0 && indexerURL[0] != "" {
		downloadConfig.IndexerURL = indexerURL[0]
		downloadConfig.APIKey = "synthetic-key"
	}
	manager, err := downloads.New(ctx, pool, downloadConfig)
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	movieLibrary, err := movies.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	service, err := tv.New(ctx, pool, manager, movieLibrary.Store)
	if err != nil {
		t.Fatalf("tv.New: %v", err)
	}
	env := &tvEnv{
		service: service, manager: manager, pool: pool, directory: directory,
		rootPath: filepath.Join(directory, "library", "tv"),
	}
	if _, err := service.SetConfig(ctx, tv.Config{
		RootFolders:    []movies.RootFolder{{ID: "tv", Path: env.rootPath}},
		FolderTemplate: "{title} ({year})/Season {season}",
		FileTemplate:   "{title} - {episodeCode} [{quality}]",
		ImportMode:     mode,
		WriteNFO:       true,
		PollMinutes:    15,
		SearchHours:    6,
		RetryFailed:    true,
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	return env
}

func (e *tvEnv) addSeries(t *testing.T, title string, year int, imdbID string) tv.Series {
	t.Helper()
	series, err := e.service.Add(context.Background(), tv.AddInput{
		Metadata:  metadata.Title{IMDbID: imdbID, Title: title, Year: year, Type: "series"},
		Monitored: true, MonitorMode: "all", ProfileID: "hd", RootID: "tv",
	})
	if err != nil {
		t.Fatalf("Add(%s): %v", title, err)
	}
	return series
}

func (e *tvEnv) addEpisode(t *testing.T, seriesID string, season, number int) tv.Episode {
	t.Helper()
	episode, err := e.service.AddEpisode(context.Background(), seriesID, tv.Episode{
		Title: fmt.Sprintf("Episode %d", number), Season: season, Number: number,
		AirDate: fmt.Sprintf("2012-01-%02d", number), Monitored: true,
	})
	if err != nil {
		t.Fatalf("AddEpisode(S%02dE%02d): %v", season, number, err)
	}
	return episode
}

func (e *tvEnv) episode(t *testing.T, seriesID string, season, number int) tv.Episode {
	t.Helper()
	episodes, err := e.service.Store.Episodes(context.Background(), seriesID)
	if err != nil {
		t.Fatalf("Episodes: %v", err)
	}
	for _, episode := range episodes {
		if episode.Season == season && episode.Number == number {
			return episode
		}
	}
	t.Fatalf("episode S%02dE%02d not found", season, number)
	return tv.Episode{}
}

func (e *tvEnv) patchFiles(t *testing.T, episodeID string, files []movies.File) {
	t.Helper()
	if _, err := e.service.Store.PatchEpisode(context.Background(), episodeID, map[string]any{"files": files}); err != nil {
		t.Fatalf("PatchEpisode: %v", err)
	}
}

func (e *tvEnv) libraryFile(t *testing.T, rel, content string, quality string) movies.File {
	t.Helper()
	target := filepath.Join(e.rootPath, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create library directory: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write library file: %v", err)
	}
	return movies.File{RootID: "tv", Path: rel, Size: int64(len(content)), Quality: quality, ImportedAt: time.Now().UTC()}
}

func (e *tvEnv) outputFile(t *testing.T, jobID, name, content string) downloads.OutputFile {
	t.Helper()
	outputDir := filepath.Join(e.directory, "downloads", jobID, "output")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(outputDir, name)), 0o755); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}
	return downloads.OutputFile{Name: name, Size: int64(len(content)), URL: "/api/v1/downloads/" + jobID + "/file?name=" + name}
}

func (e *tvEnv) completedJob(t *testing.T, jobID, releaseID, title string, files []downloads.OutputFile) {
	t.Helper()
	e.insertJob(t, jobID, releaseID, title, "completed", "", files)
}

func (e *tvEnv) failedJob(t *testing.T, jobID, releaseID, title, message string) {
	t.Helper()
	e.insertJob(t, jobID, releaseID, title, "failed", message, nil)
}

func (e *tvEnv) insertJob(t *testing.T, jobID, releaseID, title, status, message string, files []downloads.OutputFile) {
	t.Helper()
	e.insertJobAt(t, jobID, releaseID, title, status, message, files, time.Now().UTC())
}

func (e *tvEnv) insertJobAt(t *testing.T, jobID, releaseID, title, status, message string, files []downloads.OutputFile, createdAt time.Time) {
	t.Helper()
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatalf("encode output files: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(),
		`INSERT INTO downloads (id, release_id, title, nzb, status, error, files, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)`,
		jobID, releaseID, title, []byte("<nzb/>"), status, message, string(encoded), createdAt); err != nil {
		t.Fatalf("insert download job: %v", err)
	}
}

func (e *tvEnv) setJobFiles(t *testing.T, jobID string, files []downloads.OutputFile) {
	t.Helper()
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatalf("encode output files: %v", err)
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE downloads SET files = $2::jsonb WHERE id = $1`, jobID, string(encoded)); err != nil {
		t.Fatalf("update output files: %v", err)
	}
}

func (e *tvEnv) saveAcquisition(t *testing.T, acquisition tv.Acquisition) {
	t.Helper()
	if err := e.service.Store.SaveAcquisition(context.Background(), acquisition); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}
}

func (e *tvEnv) acquisition(t *testing.T, seriesID, jobID string) tv.Acquisition {
	t.Helper()
	acquisitions, err := e.service.Store.Acquisitions(context.Background())
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	for _, acquisition := range acquisitions {
		if acquisition.JobID == jobID && acquisition.SeriesID == seriesID {
			return acquisition
		}
	}
	t.Fatalf("acquisition for job %s not found", jobID)
	return tv.Acquisition{}
}

func (e *tvEnv) historyCount(t *testing.T, seriesID, kind string) int {
	t.Helper()
	history, err := e.service.Store.History(context.Background(), seriesID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	count := 0
	for _, event := range history {
		if event.Type == kind {
			count++
		}
	}
	return count
}

func (e *tvEnv) videoFiles(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(e.rootPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".recycle" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(entry.Name()), ".mkv") {
			rel, relErr := filepath.Rel(e.rootPath, path)
			if relErr != nil {
				return relErr
			}
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk library: %v", err)
	}
	sort.Strings(found)
	return found
}

func (e *tvEnv) readFile(t *testing.T, rel string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(e.rootPath, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(content)
}

func (e *tvEnv) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(e.rootPath, filepath.FromSlash(rel)))
	return err == nil
}

func syncTV(t *testing.T, env *tvEnv) int {
	t.Helper()
	imported, err := env.service.SyncDownloads(context.Background())
	if err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	return imported
}

func TestTVImportSeasonPackMultiEpisodeAndUnrelatedFiles(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	e1 := env.addEpisode(t, series.ID, 1, 1)
	e2 := env.addEpisode(t, series.ID, 1, 2)
	e3 := env.addEpisode(t, series.ID, 1, 3)
	e4 := env.addEpisode(t, series.ID, 1, 4)

	unrelated := env.libraryFile(t, "Big Buck Series (2012)/Season 01/Big Buck Series - S01E04 [Bluray-1080p].mkv", "unrelated-4", "Bluray-1080p")
	shared := env.libraryFile(t, "Big Buck Series (2012)/Season 01/Big Buck Series - S01E02 [Bluray-1080p].mkv", "shared-2-4", "Bluray-1080p")
	env.patchFiles(t, e2.ID, []movies.File{shared})
	env.patchFiles(t, e4.ID, []movies.File{unrelated, shared})

	files := []downloads.OutputFile{
		env.outputFile(t, "job-pack", "Big.Buck.Series.S01E01.1080p.WEB-DL.mkv", "pack-1"),
		env.outputFile(t, "job-pack", "Big.Buck.Series.S01E02E03.1080p.WEB-DL.mkv", "pack-2-3"),
	}
	env.completedJob(t, "job-pack", "release-pack", "Big Buck Series S01E01E03 1080p WEB-DL", files)
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{e1.ID, e2.ID, e3.ID}, JobID: "job-pack",
		ReleaseID: "release-pack", Title: "Big Buck Series S01E01E03 1080p WEB-DL", Status: "queued",
	})

	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("imported %d jobs, want 1", imported)
	}
	one := env.episode(t, series.ID, 1, 1)
	two := env.episode(t, series.ID, 1, 2)
	three := env.episode(t, series.ID, 1, 3)
	if len(one.Files) != 1 || len(three.Files) != 1 || len(two.Files) != 1 || two.Files[0].Path != shared.Path {
		t.Fatalf("episode files = %+v / %+v / %+v", one.Files, two.Files, three.Files)
	}
	multiPath := three.Files[0].Path
	if one.Files[0].Path == multiPath {
		t.Fatalf("single episode shares a file with the multi-episode video")
	}
	if !strings.Contains(multiPath, "S01E02E03") {
		t.Fatalf("multi-episode name lost the full episode code: %s", multiPath)
	}
	nfo := env.readFile(t, strings.TrimSuffix(multiPath, ".mkv")+".nfo")
	if !strings.Contains(nfo, "<title>Episode 2</title>") || !strings.Contains(nfo, "<title>Episode 3</title>") {
		t.Fatalf("multi-episode NFO lacks the physical episode details: %s", nfo)
	}
	if got := env.readFile(t, multiPath); got != "pack-2-3" {
		t.Fatalf("multi-episode file content = %q", got)
	}
	// The old shared file is owned by the unreplaced E04 and must survive the import.
	if !env.exists(shared.Path) || !env.exists(unrelated.Path) {
		t.Fatalf("unrelated or shared old files were removed: %v", env.videoFiles(t))
	}
	four := env.episode(t, series.ID, 1, 4)
	if len(four.Files) != 2 {
		t.Fatalf("unreplaced episode files = %+v, want its two old files", four.Files)
	}
	if twoFiles := env.videoFiles(t); len(twoFiles) != 4 {
		t.Fatalf("library videos = %v, want the two imports plus two preserved files", twoFiles)
	}
	acquisition := env.acquisition(t, series.ID, "job-pack")
	if acquisition.Status != "imported" {
		t.Fatalf("acquisition status = %q, want imported", acquisition.Status)
	}
	if events := env.historyCount(t, series.ID, "imported"); events != 2 {
		t.Fatalf("imported events = %d, want one per replaced episode", events)
	}
	// The same job import must be idempotent and must not duplicate history.
	if imported := syncTV(t, env); imported != 0 {
		t.Fatalf("second sync imported %d jobs, want 0", imported)
	}
	if events := env.historyCount(t, series.ID, "imported"); events != 2 {
		t.Fatalf("imported events after retry = %d, want 2", events)
	}
	if got := env.readFile(t, one.Files[0].Path); got != "pack-1" {
		t.Fatalf("single episode content = %q", got)
	}
}

func TestTVImportPartialPackImportsSubsetAndRecordsGap(t *testing.T) {
	env := newTVEnv(t, library.ModeMove)
	ctx := context.Background()
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	first := env.addEpisode(t, series.ID, 1, 1)
	second := env.addEpisode(t, series.ID, 1, 2)

	file := env.outputFile(t, "job-part", "Big.Buck.Series.2012.S01E01.1080p.WEB-DL.mkv", "partial-1")
	env.completedJob(t, "job-part", "release-part", "Big Buck Series 2012 S01E01E02 1080p WEB-DL", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{first.ID, second.ID}, JobID: "job-part",
		ReleaseID: "release-part", Title: "Big Buck Series 2012 S01E01E02 1080p WEB-DL", Status: "queued",
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("partial import count = %d, want 1", imported)
	}
	importedEpisode := env.episode(t, series.ID, 1, 1)
	if len(importedEpisode.Files) != 1 || env.readFile(t, importedEpisode.Files[0].Path) != "partial-1" {
		t.Fatalf("imported episode files = %+v", importedEpisode.Files)
	}
	missing := env.episode(t, series.ID, 1, 2)
	if len(missing.Files) != 0 || !strings.Contains(missing.Error, "did not include") {
		t.Fatalf("missing episode = %+v, want an empty gap error", missing)
	}
	if count := env.historyCount(t, series.ID, "import-incomplete"); count != 1 {
		t.Fatalf("import-incomplete history = %d, want 1", count)
	}
	if acquisition := env.acquisition(t, series.ID, "job-part"); acquisition.Status != "imported" {
		t.Fatalf("acquisition status = %q, want imported", acquisition.Status)
	}
	blocked, err := env.service.Store.Blocked(ctx, series.ID, "release-part")
	if err != nil || !blocked {
		t.Fatalf("incomplete release blocked = %v (%v), want true", blocked, err)
	}
	decorated, err := env.service.Get(ctx, series.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, episode := range decorated.Episodes {
		if episode.ID == second.ID && episode.Status != "wanted" {
			t.Fatalf("missing episode status = %q, want wanted", episode.Status)
		}
	}
	// A restart must not repeat the imported subset or the gap history.
	if imported := syncTV(t, env); imported != 0 {
		t.Fatalf("second sync imported %d jobs, want 0", imported)
	}
	if count := env.historyCount(t, series.ID, "import-incomplete"); count != 1 {
		t.Fatalf("import-incomplete history after retry = %d, want 1", count)
	}
	if got := env.episode(t, series.ID, 1, 1); len(got.Files) != 1 || env.readFile(t, got.Files[0].Path) != "partial-1" {
		t.Fatalf("imported subset changed on retry: %+v", got.Files)
	}
}

func TestTVImportMovedFilesRecoverAfterRestart(t *testing.T) {
	env := newTVEnv(t, library.ModeMove)
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	e1 := env.addEpisode(t, series.ID, 1, 1)
	e2 := env.addEpisode(t, series.ID, 1, 2)

	first := env.outputFile(t, "job-move", "Big.Buck.Series.2012.S01E01.1080p.WEB-DL.mkv", "moved-1")
	second := env.outputFile(t, "job-move", "Big.Buck.Series.2012.S01E02.1080p.WEB-DL.mkv", "moved-2")
	env.completedJob(t, "job-move", "release-move", "Big Buck Series 2012 S01E01E02 1080p WEB-DL", []downloads.OutputFile{first, second})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{e1.ID, e2.ID}, JobID: "job-move",
		ReleaseID: "release-move", Title: "Big Buck Series 2012 S01E01E02 1080p WEB-DL", Status: "queued",
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("import count = %d, want 1", imported)
	}
	one := env.episode(t, series.ID, 1, 1)
	two := env.episode(t, series.ID, 1, 2)
	if len(one.Files) != 1 || len(two.Files) != 1 {
		t.Fatalf("episode files = %+v / %+v", one.Files, two.Files)
	}
	// Move mode removed the sources; recovery must use the journal.
	if _, err := os.Stat(filepath.Join(env.directory, "downloads", "job-move", "output", first.Name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source file still exists after a move import")
	}
	dest := one.Files[0].Path
	if got := env.readFile(t, dest); got != "moved-1" {
		t.Fatalf("recovered content = %q", got)
	}

	// Simulate a restart between the move and the episode writes: the journal still knows the hashes.
	env.patchFiles(t, e1.ID, nil)
	env.patchFiles(t, e2.ID, nil)
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{e1.ID, e2.ID}, JobID: "job-move",
		ReleaseID: "release-move", Title: "Big Buck Series 2012 S01E01E02 1080p WEB-DL", Status: "importing",
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("journal recovery count = %d, want 1", imported)
	}
	if got := env.readFile(t, env.episode(t, series.ID, 1, 1).Files[0].Path); got != "moved-1" {
		t.Fatalf("journal recovery content = %q", got)
	}

	// Without the ready flag the destination hash must still match.
	if _, err := env.pool.Exec(context.Background(), `UPDATE download_library_files SET ready = false WHERE job_id = 'job-move'`); err != nil {
		t.Fatalf("reset journal: %v", err)
	}
	env.patchFiles(t, e1.ID, nil)
	env.patchFiles(t, e2.ID, nil)
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{e1.ID, e2.ID}, JobID: "job-move",
		ReleaseID: "release-move", Title: "Big Buck Series 2012 S01E01E02 1080p WEB-DL", Status: "importing",
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("hash recovery count = %d, want 1", imported)
	}

	// A changed destination must not be adopted silently.
	if _, err := env.pool.Exec(context.Background(), `UPDATE download_library_files SET ready = false WHERE job_id = 'job-move'`); err != nil {
		t.Fatalf("reset journal: %v", err)
	}
	env.patchFiles(t, e1.ID, nil)
	env.patchFiles(t, e2.ID, nil)
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{e1.ID, e2.ID}, JobID: "job-move",
		ReleaseID: "release-move", Title: "Big Buck Series 2012 S01E01E02 1080p WEB-DL", Status: "importing",
	})
	if err := os.WriteFile(filepath.Join(env.rootPath, filepath.FromSlash(dest)), []byte("corrupted"), 0o644); err != nil {
		t.Fatalf("corrupt destination: %v", err)
	}
	if _, err := env.service.SyncDownloads(context.Background()); err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	corruptedAcquisition := env.acquisition(t, series.ID, "job-move")
	if corruptedAcquisition.Status != "import-failed" || !strings.Contains(corruptedAcquisition.Error, "hash") {
		t.Fatalf("acquisition after corruption = %+v, want a hash mismatch", corruptedAcquisition)
	}
}

func TestTVImportSupersedesObsoleteAndHonoursOverride(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	episode := env.addEpisode(t, series.ID, 1, 1)
	current := env.libraryFile(t, "Big Buck Series (2012)/Season 01/Big Buck Series - S01E01 [Bluray-1080p].mkv", "current-best", "Bluray-1080p")
	env.patchFiles(t, episode.ID, []movies.File{current})

	obsolete := env.outputFile(t, "job-obsolete", "Big.Buck.Series.S01E01.HDTV-480p.mkv", "obsolete")
	env.completedJob(t, "job-obsolete", "release-obsolete", "Big Buck Series S01E01 HDTV-480p", []downloads.OutputFile{obsolete})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-obsolete",
		ReleaseID: "release-obsolete", Title: "Big Buck Series S01E01 HDTV-480p", Status: "queued",
	})
	if imported := syncTV(t, env); imported != 0 {
		t.Fatalf("obsolete release imported %d jobs, want 0", imported)
	}
	if acquisition := env.acquisition(t, series.ID, "job-obsolete"); acquisition.Status != "superseded" {
		t.Fatalf("acquisition status = %q, want superseded", acquisition.Status)
	}
	if files := env.videoFiles(t); len(files) != 1 || files[0] != current.Path {
		t.Fatalf("obsolete import changed the library: %v", files)
	}

	override := env.outputFile(t, "job-override", "Big.Buck.Series.S01E01.HDTV-480p.retry.mkv", "override")
	env.completedJob(t, "job-override", "release-override", "Big Buck Series S01E01 HDTV-480p", []downloads.OutputFile{override})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-override", Override: true,
		ReleaseID: "release-override", Title: "Big Buck Series S01E01 HDTV-480p", Status: "queued",
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("override import count = %d, want 1", imported)
	}
	after := env.episode(t, series.ID, 1, 1)
	if len(after.Files) == 0 || after.Files[0].Path == current.Path {
		t.Fatalf("override did not publish a new file: %+v", after.Files)
	}
	if got := env.readFile(t, after.Files[0].Path); got != "override" {
		t.Fatalf("override content = %q", got)
	}
	if env.exists(current.Path) {
		t.Fatalf("replaced file was not archived: %s", current.Path)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath, ".recycle", filepath.FromSlash(current.Path))); err != nil {
		t.Fatalf("replaced file missing from the recycle folder: %v", err)
	}
}

func TestTVImportRejectsAmbiguousDownloadFiles(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	episode := env.addEpisode(t, series.ID, 1, 1)
	env.addEpisode(t, series.ID, 1, 3)

	other := env.outputFile(t, "job-other", "Another.Show.S01E01.1080p.WEB-DL.mkv", "other")
	env.completedJob(t, "job-other", "release-other", "Another Show S01E01 1080p WEB-DL", []downloads.OutputFile{other})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-other",
		ReleaseID: "release-other", Title: "Another Show S01E01 1080p WEB-DL", Status: "queued",
	})
	if _, err := env.service.SyncDownloads(context.Background()); err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	if acquisition := env.acquisition(t, series.ID, "job-other"); acquisition.Status != "import-failed" || !strings.Contains(acquisition.Error, "manual import") {
		t.Fatalf("mismatched acquisition = %+v, want a manual import hint", acquisition)
	}

	wrongEpisode := env.outputFile(t, "job-wrong", "Big.Buck.Series.S01E03.1080p.WEB-DL.mkv", "wrong-episode")
	env.completedJob(t, "job-wrong", "release-wrong", "Big Buck Series S01E03 1080p WEB-DL", []downloads.OutputFile{wrongEpisode})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-wrong",
		ReleaseID: "release-wrong", Title: "Big Buck Series S01E03 1080p WEB-DL", Status: "queued",
	})
	if _, err := env.service.SyncDownloads(context.Background()); err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	if acquisition := env.acquisition(t, series.ID, "job-wrong"); acquisition.Status != "import-failed" || !strings.Contains(acquisition.Error, "not requested") {
		t.Fatalf("unrequested episode acquisition = %+v, want a rejection", acquisition)
	}
	if files := env.videoFiles(t); len(files) != 0 {
		t.Fatalf("rejected downloads published files: %v", files)
	}
}

func TestTVManualScanImportRenameAndOpenFile(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	ctx := context.Background()
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, series.ID, 1, 1)
	env.addEpisode(t, series.ID, 1, 2)

	source := "incoming/Big Buck Series 2012 S01E01 1080p WEB-DL.mkv"
	target := filepath.Join(env.rootPath, filepath.FromSlash(source))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create incoming folder: %v", err)
	}
	if err := os.WriteFile(target, []byte("manual-scan"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	candidates, err := env.service.Scan(ctx, "tv")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(candidates) != 1 || candidates[0].MatchedSeriesID != series.ID || candidates[0].Season != 1 || len(candidates[0].Episodes) != 1 || candidates[0].Episodes[0] != 1 {
		t.Fatalf("scan candidates = %+v", candidates)
	}

	imported, err := env.service.Import(ctx, tv.ImportInput{RootID: "tv", Path: source, SeriesID: series.ID})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	episode := env.episode(t, series.ID, 1, 1)
	if len(episode.Files) != 1 {
		t.Fatalf("episode files = %+v, want one imported file", episode.Files)
	}
	if got := env.readFile(t, episode.Files[0].Path); got != "manual-scan" {
		t.Fatalf("imported content = %q", got)
	}
	handle, err := env.service.OpenFile(ctx, imported.ID, episode.Files[0].Path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	handle.Close()
	if _, err := env.service.OpenFile(ctx, imported.ID, "not/owned.mkv"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("OpenFile for an unknown path = %v, want ErrNotFound", err)
	}

	mystery := "incoming/mystery.mkv"
	mysteryTarget := filepath.Join(env.rootPath, filepath.FromSlash(mystery))
	if err := os.WriteFile(mysteryTarget, []byte("mystery"), 0o644); err != nil {
		t.Fatalf("write mystery: %v", err)
	}
	if _, err := env.service.Import(ctx, tv.ImportInput{RootID: "tv", Path: mystery, SeriesID: series.ID}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("ambiguous manual import = %v, want ErrInvalid", err)
	}
	if _, err := env.service.Import(ctx, tv.ImportInput{RootID: "tv", Path: mystery, SeriesID: series.ID, Season: 1, Episodes: []int{2}}); err != nil {
		t.Fatalf("explicit manual import: %v", err)
	}
	second := env.episode(t, series.ID, 1, 2)
	if len(second.Files) != 1 || env.readFile(t, second.Files[0].Path) != "mystery" {
		t.Fatalf("explicit episode files = %+v", second.Files)
	}

	// A shared multi-episode file keeps every owner when renamed.
	env.patchFiles(t, second.ID, []movies.File{episode.Files[0], second.Files[0]})
	if _, err := env.service.SetConfig(ctx, tv.Config{
		RootFolders:    []movies.RootFolder{{ID: "tv", Path: env.rootPath}},
		FolderTemplate: "{title} ({year})/Season {season}",
		FileTemplate:   "{title} - {episodeCode} - {episodeTitle} [{quality}]",
		ImportMode:     library.ModeLink,
		WriteNFO:       true,
		PollMinutes:    15,
		SearchHours:    6,
		RetryFailed:    true,
	}); err != nil {
		t.Fatalf("SetConfig for rename: %v", err)
	}
	preview, err := env.service.Rename(ctx, series.ID, true)
	if err != nil {
		t.Fatalf("Rename preview: %v", err)
	}
	if len(preview.Files) != 2 {
		t.Fatalf("rename preview = %+v, want two files", preview)
	}
	var sharedFrom, sharedTo string
	for _, file := range preview.Files {
		if file.From == episode.Files[0].Path {
			sharedFrom, sharedTo = file.From, file.To
		}
	}
	if sharedFrom == "" || sharedTo == "" || sharedTo == sharedFrom {
		t.Fatalf("shared file preview = %+v", preview.Files)
	}
	applied, err := env.service.Rename(ctx, series.ID, false)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if !applied.Applied {
		t.Fatalf("rename did not report an application")
	}
	renamed := env.episode(t, series.ID, 1, 1)
	renamedSecond := env.episode(t, series.ID, 1, 2)
	found := false
	for _, file := range renamed.Files {
		if file.Path == sharedTo {
			found = true
		}
	}
	if !found || !tvContainsPath(renamedSecond.Files, sharedTo) {
		t.Fatalf("rename lost the multi-episode mapping: %+v / %+v", renamed.Files, renamedSecond.Files)
	}
	if got := env.readFile(t, sharedTo); got != "manual-scan" {
		t.Fatalf("renamed content = %q", got)
	}
	if !env.exists(sharedTo) {
		t.Fatalf("renamed file missing at %s", sharedTo)
	}
	if env.exists(sharedFrom) {
		t.Fatalf("rename left a stale source copy at %s", sharedFrom)
	}
}

func tvContainsPath(files []movies.File, path string) bool {
	for _, file := range files {
		if file.Path == path {
			return true
		}
	}
	return false
}

func TestTVLegacyAdoptionClaimsCatalogSeriesOnly(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	ctx := context.Background()
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, series.ID, 1, 1)

	file := env.outputFile(t, "job-legacy", "Big.Buck.Series.2012.S01E01.1080p.WEB-DL.mkv", "legacy")
	env.completedJob(t, "job-legacy", "release-legacy", "Big Buck Series 2012 S01E01 1080p WEB-DL", []downloads.OutputFile{file})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("legacy import count = %d, want 1", imported)
	}
	episode := env.episode(t, series.ID, 1, 1)
	if len(episode.Files) != 1 || env.readFile(t, episode.Files[0].Path) != "legacy" {
		t.Fatalf("legacy episode files = %+v", episode.Files)
	}
	if acquisition := env.acquisition(t, series.ID, "job-legacy"); acquisition.Status != "imported" {
		t.Fatalf("legacy acquisition status = %q", acquisition.Status)
	}
	var adopted bool
	if err := env.pool.QueryRow(ctx, `SELECT movie_adopted FROM downloads WHERE id = 'job-legacy'`).Scan(&adopted); err != nil {
		t.Fatalf("read movie_adopted: %v", err)
	}
	if !adopted {
		t.Fatalf("legacy TV import did not claim the download from movie adoption")
	}
	if imported := syncTV(t, env); imported != 0 {
		t.Fatalf("legacy job imported twice: %d", imported)
	}

	unknown := env.outputFile(t, "job-unknown", "Unknown.Show.2020.S01E01.1080p.WEB-DL.mkv", "unknown")
	env.completedJob(t, "job-unknown", "release-unknown", "Unknown Show 2020 S01E01 1080p WEB-DL", []downloads.OutputFile{unknown})
	if imported := syncTV(t, env); imported != 0 {
		t.Fatalf("unknown series was adopted: %d", imported)
	}
	var claims int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM tv_acquisitions WHERE job_id = 'job-unknown'`).Scan(&claims); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if claims != 0 {
		t.Fatalf("unknown series got %d acquisition rows", claims)
	}
	if err := env.pool.QueryRow(ctx, `SELECT movie_adopted FROM downloads WHERE id = 'job-unknown'`).Scan(&adopted); err != nil {
		t.Fatalf("read movie_adopted: %v", err)
	}
	if adopted {
		t.Fatalf("unknown series download was claimed")
	}
}

func TestTVImportProtectsSatisfiedEpisodeFiles(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	first := env.addEpisode(t, series.ID, 1, 1)
	second := env.addEpisode(t, series.ID, 1, 2)
	current := env.libraryFile(t, "Big Buck Series (2012)/Season 01/Big Buck Series - S01E02 [Bluray-1080p].mkv", "already-best", "Bluray-1080p")
	env.patchFiles(t, second.ID, []movies.File{current})

	file := env.outputFile(t, "job-shared", "Big.Buck.Series.2012.S01E01E02.1080p.WEB-DL.mkv", "shared-new")
	env.completedJob(t, "job-shared", "release-shared", "Big Buck Series 2012 S01E01E02 1080p WEB-DL", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{first.ID, second.ID}, JobID: "job-shared",
		ReleaseID: "release-shared", Title: "Big Buck Series 2012 S01E01E02 1080p WEB-DL", Status: "queued",
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("import count = %d, want 1", imported)
	}
	if !env.exists(current.Path) {
		t.Fatalf("the satisfied episode file was archived by a multi-episode video")
	}
	missing := env.episode(t, series.ID, 1, 1)
	if len(missing.Files) != 1 || env.readFile(t, missing.Files[0].Path) != "shared-new" {
		t.Fatalf("missing episode files = %+v", missing.Files)
	}
	// The physical file still names and documents both episodes even though only one is associated.
	if !strings.Contains(missing.Files[0].Path, "S01E01E02") {
		t.Fatalf("multi-episode name lost the full episode code: %s", missing.Files[0].Path)
	}
	nfo := env.readFile(t, strings.TrimSuffix(missing.Files[0].Path, ".mkv")+".nfo")
	if !strings.Contains(nfo, "<title>Episode 1</title>") || !strings.Contains(nfo, "<title>Episode 2</title>") {
		t.Fatalf("multi-episode NFO lacks distinct episode details: %s", nfo)
	}
	satisfied := env.episode(t, series.ID, 1, 2)
	if !tvContainsPath(satisfied.Files, current.Path) {
		t.Fatalf("satisfied episode lost its better file: %+v", satisfied.Files)
	}
	if len(satisfied.Files) != 1 {
		t.Fatalf("satisfied episode files = %+v, want only its better file", satisfied.Files)
	}
}

func TestTVImportNotifiesJellyfinAndWebhook(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	var mu sync.Mutex
	var payload map[string]any
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("webhook payload is not JSON: %v", err)
		}
		mu.Lock()
		payload = decoded
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(webhook.Close)
	var refreshMu sync.Mutex
	refreshed := false
	jellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Library/Refresh" && r.Method == http.MethodPost && r.Header.Get("X-Emby-Token") == "test-token" {
			refreshMu.Lock()
			refreshed = true
			refreshMu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(jellyfin.Close)
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE movie_config SET data = data || $1::jsonb`,
		`{"webhookURL":"`+webhook.URL+`","jellyfinURL":"`+jellyfin.URL+`","jellyfinAPIKey":"test-token"}`); err != nil {
		t.Fatalf("configure notifications: %v", err)
	}

	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	episode := env.addEpisode(t, series.ID, 1, 1)
	file := env.outputFile(t, "job-notify", "Big.Buck.Series.2012.S01E01.1080p.WEB-DL.mkv", "notify")
	env.completedJob(t, "job-notify", "release-notify", "Big Buck Series 2012 S01E01 1080p WEB-DL", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-notify",
		ReleaseID: "release-notify", Title: "Big Buck Series 2012 S01E01 1080p WEB-DL", Status: "queued",
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("import count = %d, want 1", imported)
	}

	mu.Lock()
	defer mu.Unlock()
	if payload == nil {
		t.Fatalf("webhook was not called")
	}
	if payload["event"] != "tv.imported" || payload["seriesId"] != series.ID || payload["imdbId"] != "tt1111111" || payload["title"] != "Big Buck Series" {
		t.Fatalf("webhook payload = %+v", payload)
	}
	if ids, ok := payload["episodeIds"].([]any); !ok || len(ids) != 1 {
		t.Fatalf("webhook episodeIds = %+v", payload["episodeIds"])
	}
	if files, ok := payload["files"].([]any); !ok || len(files) != 1 {
		t.Fatalf("webhook files = %+v", payload["files"])
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if !refreshed {
		t.Fatalf("Jellyfin was not refreshed with the configured token")
	}
}

func TestTVManualImportAcrossRoots(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	ctx := context.Background()
	inbox := filepath.Join(env.directory, "inbox")
	if _, err := env.service.SetConfig(ctx, tv.Config{
		RootFolders:    []movies.RootFolder{{ID: "tv", Path: env.rootPath}, {ID: "inbox", Path: inbox}},
		FolderTemplate: "{title} ({year})/Season {season}",
		FileTemplate:   "{title} - {episodeCode} [{quality}]",
		ImportMode:     library.ModeLink,
		WriteNFO:       true,
		PollMinutes:    15,
		SearchHours:    6,
		RetryFailed:    true,
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, series.ID, 1, 1)
	if err := os.WriteFile(filepath.Join(inbox, "Big Buck Series 2012 S01E01 1080p WEB-DL.mkv"), []byte("from-inbox"), 0o644); err != nil {
		t.Fatalf("write inbox file: %v", err)
	}
	if _, err := env.service.Import(ctx, tv.ImportInput{RootID: "inbox", Path: "Big Buck Series 2012 S01E01 1080p WEB-DL.mkv", SeriesID: series.ID}); err != nil {
		t.Fatalf("cross-root import: %v", err)
	}
	episode := env.episode(t, series.ID, 1, 1)
	if len(episode.Files) != 1 || env.readFile(t, episode.Files[0].Path) != "from-inbox" {
		t.Fatalf("cross-root episode files = %+v", episode.Files)
	}
	if _, err := env.service.Import(ctx, tv.ImportInput{RootID: "inbox", Path: "../outside.mkv", SeriesID: series.ID}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("import outside the configured root = %v, want ErrInvalid", err)
	}
}

func TestTVImportRejectsConfidentialTitleMatch(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Doctor Who", 2005, "tt0436992")
	episode := env.addEpisode(t, series.ID, 1, 1)

	file := env.outputFile(t, "job-confidential", "Doctor.Who.Confidential.2005.S01E01.1080p.WEB-DL.mkv", "confidential")
	env.completedJob(t, "job-confidential", "release-confidential", "Doctor Who Confidential 2005 S01E01 1080p WEB-DL", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-confidential",
		ReleaseID: "release-confidential", Title: "Doctor Who Confidential 2005 S01E01 1080p WEB-DL", Status: "queued",
	})
	if _, err := env.service.SyncDownloads(context.Background()); err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	if acquisition := env.acquisition(t, series.ID, "job-confidential"); acquisition.Status != "import-failed" || !strings.Contains(acquisition.Error, "manual import") {
		t.Fatalf("confidential acquisition = %+v, want a title mismatch", acquisition)
	}
	if files := env.videoFiles(t); len(files) != 0 {
		t.Fatalf("a differing parsed title was imported anyway: %v", files)
	}
}

func TestTVImportFallsBackToJobTitleForNumberedFiles(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Doctor Who", 2005, "tt0436992")
	episode := env.addEpisode(t, series.ID, 1, 1)

	file := env.outputFile(t, "job-numbered", "S01E01.1080p.WEB-DL.mkv", "numbered")
	env.completedJob(t, "job-numbered", "release-numbered", "Doctor Who 2005 S01E01 1080p WEB-DL", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-numbered",
		ReleaseID: "release-numbered", Title: "Doctor Who 2005 S01E01 1080p WEB-DL", Status: "queued",
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("numbered file import count = %d, want 1", imported)
	}
	if got := env.episode(t, series.ID, 1, 1); len(got.Files) != 1 || env.readFile(t, got.Files[0].Path) != "numbered" {
		t.Fatalf("numbered file episode = %+v", got.Files)
	}

	mismatched := env.outputFile(t, "job-year", "S01E01.1080p.WEB-DL.mkv", "wrong-year")
	env.completedJob(t, "job-year", "release-year", "Doctor Who 1975 S01E01 1080p WEB-DL", []downloads.OutputFile{mismatched})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-year",
		ReleaseID: "release-year", Title: "Doctor Who 1975 S01E01 1080p WEB-DL", Status: "queued",
	})
	if _, err := env.service.SyncDownloads(context.Background()); err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	if acquisition := env.acquisition(t, series.ID, "job-year"); acquisition.Status != "import-failed" || !strings.Contains(acquisition.Error, "manual import") {
		t.Fatalf("year mismatch acquisition = %+v, want a rejection", acquisition)
	}
}

func TestTVImportRejectsSingleVideoSeasonPack(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Doctor Who", 2005, "tt0436992")
	first := env.addEpisode(t, series.ID, 1, 1)
	second := env.addEpisode(t, series.ID, 1, 2)

	file := env.outputFile(t, "job-pack-video", "Doctor.Who.2005.S01.1080p.WEB-DL.mkv", "whole-season")
	env.completedJob(t, "job-pack-video", "release-pack-video", "Doctor Who 2005 S01 1080p WEB-DL", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{first.ID, second.ID}, JobID: "job-pack-video",
		ReleaseID: "release-pack-video", Title: "Doctor Who 2005 S01 1080p WEB-DL", Status: "queued",
	})
	if _, err := env.service.SyncDownloads(context.Background()); err != nil {
		t.Fatalf("SyncDownloads: %v", err)
	}
	acquisition := env.acquisition(t, series.ID, "job-pack-video")
	if acquisition.Status != "import-failed" || !strings.Contains(acquisition.Error, "season pack") {
		t.Fatalf("single-video pack acquisition = %+v, want a season pack rejection", acquisition)
	}
	if files := env.videoFiles(t); len(files) != 0 {
		t.Fatalf("a single pack-named video was imported as a season: %v", files)
	}
}

func TestTVLegacyCursorPagesPastUnknownJobs(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, series.ID, 1, 1)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 21; i++ {
		jobID := fmt.Sprintf("job-unknown-%02d", i)
		name := fmt.Sprintf("Unknown.Show.%02d.2020.S01E01.1080p.WEB-DL.mkv", i)
		file := env.outputFile(t, jobID, name, "unknown")
		env.insertJobAt(t, jobID, "release-"+jobID, "Unknown Show "+name, "completed", "", []downloads.OutputFile{file}, base.Add(time.Duration(i)*time.Minute))
	}
	matched := env.outputFile(t, "job-matched", "Big.Buck.Series.2012.S01E01.1080p.WEB-DL.mkv", "matched")
	env.insertJobAt(t, "job-matched", "release-matched", "Big Buck Series 2012 S01E01 1080p WEB-DL", "completed", "", []downloads.OutputFile{matched}, base.Add(22*time.Minute))

	if imported := syncTV(t, env); imported != 0 {
		t.Fatalf("first legacy page imported %d jobs, want 0", imported)
	}
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("second legacy page imported %d jobs, want the catalog-matched job", imported)
	}
	if got := env.episode(t, series.ID, 1, 1); len(got.Files) != 1 || env.readFile(t, got.Files[0].Path) != "matched" {
		t.Fatalf("matched legacy episode files = %+v", got.Files)
	}
	ctx := context.Background()
	var adopted bool
	if err := env.pool.QueryRow(ctx, `SELECT movie_adopted FROM downloads WHERE id = 'job-matched'`).Scan(&adopted); err != nil {
		t.Fatalf("read movie_adopted: %v", err)
	}
	if !adopted {
		t.Fatalf("matched legacy job was not claimed")
	}
	var claims int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM tv_acquisitions WHERE job_id LIKE 'job-unknown-%'`).Scan(&claims); err != nil {
		t.Fatalf("count unknown claims: %v", err)
	}
	if claims != 0 {
		t.Fatalf("unknown-series jobs were claimed: %d", claims)
	}
}

func TestTVRenameTransactionFailureRestoresDisk(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	ctx := context.Background()
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	episode := env.addEpisode(t, series.ID, 1, 1)

	source := "incoming/Big Buck Series 2012 S01E01 1080p WEB-DL.mkv"
	target := filepath.Join(env.rootPath, filepath.FromSlash(source))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create incoming folder: %v", err)
	}
	if err := os.WriteFile(target, []byte("rename-source"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if _, err := env.service.Import(ctx, tv.ImportInput{RootID: "tv", Path: source, SeriesID: series.ID}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	original := env.episode(t, series.ID, 1, 1)
	if len(original.Files) != 1 {
		t.Fatalf("imported files = %+v", original.Files)
	}
	oldPath := original.Files[0].Path
	subtitle := strings.TrimSuffix(oldPath, ".mkv") + ".bg.srt"
	if err := os.WriteFile(filepath.Join(env.rootPath, subtitle), []byte("episode subtitle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.SetConfig(ctx, tv.Config{
		RootFolders:    []movies.RootFolder{{ID: "tv", Path: env.rootPath}},
		FolderTemplate: "{title} ({year})/Season {season}",
		FileTemplate:   "{title} - {episodeCode} - {episodeTitle} [{quality}]",
		ImportMode:     library.ModeLink,
		WriteNFO:       true,
		PollMinutes:    15,
		SearchHours:    6,
		RetryFailed:    true,
	}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	locked, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock: %v", err)
	}
	defer locked.Rollback(ctx)
	if _, err := locked.Exec(ctx, `SELECT 1 FROM tv_episodes WHERE id = $1 FOR UPDATE`, episode.ID); err != nil {
		t.Fatalf("lock episode row: %v", err)
	}

	renameCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := env.service.Rename(renameCtx, series.ID, false); err == nil {
		t.Fatalf("rename committed while its episode row was locked")
	}
	if got := env.readFile(t, oldPath); got != "rename-source" {
		t.Fatalf("restored content = %q", got)
	}
	if got := env.readFile(t, subtitle); got != "episode subtitle" {
		t.Fatalf("restored subtitle = %q", got)
	}
	after := env.episode(t, series.ID, 1, 1)
	if len(after.Files) != 1 || after.Files[0].Path != oldPath {
		t.Fatalf("catalog changed after a failed rename: %+v", after.Files)
	}
	preview, err := env.service.Rename(ctx, series.ID, true)
	if err != nil {
		t.Fatalf("Rename preview: %v", err)
	}
	for _, file := range preview.Files {
		if file.From == oldPath && env.exists(file.To) {
			t.Fatalf("failed rename left a published copy at %s", file.To)
		}
	}
	if _, err := os.Stat(filepath.Join(env.rootPath, strings.TrimSuffix(oldPath, ".mkv")+".nfo")); err != nil {
		t.Fatalf("original episode sidecar was lost: %v", err)
	}
}

func TestTVImportNamesObfuscatedFileFromDecision(t *testing.T) {
	env := newTVEnv(t, library.ModeLink)
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	episode := env.addEpisode(t, series.ID, 1, 1)

	file := env.outputFile(t, "job-obfuscated", "094febc9704f0a95454d.mkv", "obfuscated")
	env.completedJob(t, "job-obfuscated", "release-obfuscated", "Big Buck Series 2012 S01E01", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-obfuscated",
		ReleaseID: "release-obfuscated", Title: "Big Buck Series 2012 S01E01", Status: "queued",
		Decision: quality.Decision{Details: quality.Details{Quality: "WEB-DL-1080p"}},
	})
	if imported := syncTV(t, env); imported != 1 {
		t.Fatalf("obfuscated import count = %d, want 1", imported)
	}
	got := env.episode(t, series.ID, 1, 1)
	if len(got.Files) != 1 || !strings.Contains(got.Files[0].Path, "WEB-DL-1080p") {
		t.Fatalf("obfuscated file name = %+v, want the acquisition quality", got.Files)
	}
	if env.readFile(t, got.Files[0].Path) != "obfuscated" {
		t.Fatalf("obfuscated content mismatch")
	}
}
