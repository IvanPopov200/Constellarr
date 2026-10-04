package movies_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

func nestedOutputFile(t *testing.T, env *testEnv, jobID, name, content string) downloads.OutputFile {
	t.Helper()
	target := filepath.Join(env.directory, "downloads", jobID, "output", filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}
	return downloads.OutputFile{Name: name, Size: int64(len(content)), URL: "/api/v1/downloads/" + jobID + "/file?name=" + name}
}

func statusOf(t *testing.T, env *testEnv, id string) movies.Movie {
	t.Helper()
	for _, movie := range mustList(t, env) {
		if movie.ID == id {
			return movie
		}
	}
	t.Fatalf("movie %s is missing from the list", id)
	return movies.Movie{}
}

func acquisitionOf(t *testing.T, env *testEnv, jobID string) movies.Acquisition {
	t.Helper()
	acquisitions, err := env.service.Store.Acquisitions(context.Background())
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	for _, acquisition := range acquisitions {
		if acquisition.JobID == jobID {
			return acquisition
		}
	}
	t.Fatalf("acquisition %s is missing", jobID)
	return movies.Acquisition{}
}

func TestImportUpdatesQualityScoreAtSamePath(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	if _, err := env.service.Store.SaveProfile(ctx, quality.Profile{
		ID: "score-gate", Name: "Score Gate", Qualities: []string{"WEB-1080p", "WEB-720p"},
		Cutoff: "WEB-1080p", CutoffScore: 10, Upgrade: true,
		Rules: []quality.Rule{{Name: "web-dl", Pattern: "WEB-DL", Score: 50}},
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	movie, err := env.service.Add(ctx, movies.AddInput{
		Metadata:  metadata.Title{Title: "Score Film", Year: 2020, Type: "movie", Released: "2020-06-01"},
		Monitored: true, ProfileID: "score-gate", RootID: env.rootID,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	existing := env.writeLibraryFile(t, "Score Film (2020) [imdb-]/Score Film (2020) [WEB-1080p].mkv", "old-bytes")
	existing.Quality, existing.Score = "WEB-1080p", 0
	movie.Files = []movies.File{existing}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save movie: %v", err)
	}
	if status := statusOf(t, env, movie.ID); status.Status != "cutoff-unmet" {
		t.Fatalf("status before import = %q, want cutoff-unmet", status.Status)
	}

	source := env.outputFile(t, "job-score", "Score.Film.2020.1080p.WEB-DL.x264-GRP.mkv", "new-bytes")
	env.completedJob(t, "job-score", "rel-score", "Score Film 2020 1080p WEB-DL x264-GRP", []downloads.OutputFile{source})
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: "job-score", ReleaseID: "rel-score", Title: "Score Film 2020 1080p WEB-DL x264-GRP",
		Decision: quality.Decision{Details: quality.Details{Quality: "WEB-1080p"}, Score: 50}, Status: "queued",
	}); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}

	imported, err := env.service.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("SyncDownloads = %d, %v", imported, err)
	}
	stored, err := env.service.Store.Get(ctx, movie.ID)
	if err != nil || len(stored.Files) != 1 {
		t.Fatalf("stored files = %+v, %v", stored.Files, err)
	}
	file := stored.Files[0]
	if file.Path != existing.Path || file.Quality != "WEB-1080p" || file.Score != 50 || file.Size != int64(len("new-bytes")) {
		t.Fatalf("merged file = %+v, want the same path with the release score and quality", file)
	}
	if file.ImportedAt.Before(existing.ImportedAt) {
		t.Fatalf("merged file kept a stale import time: %v", file.ImportedAt)
	}
	if status := statusOf(t, env, movie.ID); status.Status != "available" {
		t.Fatalf("status after import = %q, want available once the cutoff score is met", status.Status)
	}
}

func TestSupersededDownloadKeepsBetterFile(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	if _, err := env.service.Store.SaveProfile(ctx, quality.Profile{
		ID: "supersede", Name: "Supersede", Qualities: []string{"Bluray-1080p", "WEB-720p"},
		Cutoff: "Bluray-1080p", Upgrade: true,
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	movie, err := env.service.Add(ctx, movies.AddInput{
		Metadata:  metadata.Title{Title: "Superseded Film", Year: 2020, Type: "movie", Released: "2020-06-01"},
		Monitored: true, ProfileID: "supersede", RootID: env.rootID,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	existing := env.writeLibraryFile(t, "Superseded Film (2020) [imdb-]/Superseded Film (2020) [Bluray-1080p].mkv", "better-bytes")
	existing.Quality = "Bluray-1080p"
	movie.Files = []movies.File{existing}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save movie: %v", err)
	}

	source := env.outputFile(t, "job-old", "Superseded.Film.2020.720p.WEB-DL.x264-GRP.mkv", "worse-bytes")
	env.completedJob(t, "job-old", "rel-old", "Superseded Film 2020 720p WEB-DL x264-GRP", []downloads.OutputFile{source})
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: "job-old", ReleaseID: "rel-old", Title: "Superseded Film 2020 720p WEB-DL x264-GRP",
		Decision: quality.Decision{Details: quality.Details{Quality: "WEB-720p"}}, Status: "import-failed", Error: "the previous import failed",
	}); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}

	imported, err := env.service.SyncDownloads(ctx)
	if err != nil || imported != 0 {
		t.Fatalf("SyncDownloads = %d, %v, want the download superseded", imported, err)
	}
	acquisition := acquisitionOf(t, env, "job-old")
	if acquisition.Status != "superseded" {
		t.Fatalf("acquisition status = %q, want superseded", acquisition.Status)
	}
	stored, err := env.service.Store.Get(ctx, movie.ID)
	if err != nil || len(stored.Files) != 1 || stored.Files[0].Path != existing.Path || stored.Files[0].Size != existing.Size {
		t.Fatalf("existing file was disturbed: %+v, %v", stored.Files, err)
	}
	history, err := env.service.Store.History(ctx, movie.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	found := false
	for _, event := range history {
		if event.Type == "superseded" {
			found = true
		}
	}
	if !found {
		t.Fatalf("history has no superseded event: %+v", history)
	}
	if !sourceExists(t, env, "job-old", source.Name) {
		t.Fatalf("superseded download lost its source file")
	}
	acquisition.Status, acquisition.Override = "queued", true
	if err := env.service.Store.SaveAcquisition(ctx, acquisition); err != nil {
		t.Fatalf("save explicit override: %v", err)
	}
	imported, err = env.service.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("explicit override import = %d, %v", imported, err)
	}
	acquisition = acquisitionOf(t, env, "job-old")
	if acquisition.Status != "imported" || !acquisition.Override {
		t.Fatalf("explicit override did not survive import: %+v", acquisition)
	}
	stored, err = env.service.Store.Get(ctx, movie.ID)
	if err != nil || len(stored.Files) != 1 || stored.Files[0].Quality != "WEB-720p" {
		t.Fatalf("explicit override did not replace the file: %+v, %v", stored.Files, err)
	}
}

func TestLegacyMultiMoviePackNeedsManualImport(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	first := nestedOutputFile(t, env, "job-pack", "Pack Movie A (2019)/Pack.Movie.A.2019.1080p.mkv", "pack-a-bytes")
	second := nestedOutputFile(t, env, "job-pack", "Pack Movie B (2020)/Pack.Movie.B.2020.1080p.mkv", "pack-b-bytes")
	env.completedJob(t, "job-pack", "rel-pack", "Pack Movies 2019 1080p BluRay", []downloads.OutputFile{first, second})

	imported, err := env.service.SyncDownloads(ctx)
	if err != nil || imported != 0 {
		t.Fatalf("SyncDownloads = %d, %v, want the pack rejected", imported, err)
	}
	list := mustList(t, env)
	if len(list) != 1 {
		t.Fatalf("movies = %+v, want a single flagged record", list)
	}
	flagged := list[0]
	if len(flagged.Files) != 0 {
		t.Fatalf("ambiguous pack was imported: %+v", flagged.Files)
	}
	acquisition := acquisitionOf(t, env, "job-pack")
	if acquisition.Status != "import-failed" || !strings.Contains(acquisition.Error, "multiple movies") {
		t.Fatalf("acquisition = %+v, want a manual-import failure", acquisition)
	}
	history, err := env.service.Store.History(ctx, flagged.ID)
	if err != nil || len(history) == 0 || history[0].Type != "import-failed" {
		t.Fatalf("history = %+v, %v", history, err)
	}
	// A later sync retries the association, not the mislabeled pack.
	if imported, err := env.service.SyncDownloads(ctx); err != nil || imported != 0 {
		t.Fatalf("retry SyncDownloads = %d, %v", imported, err)
	}

	incoming := env.config(t)
	incoming.RootFolders = append(incoming.RootFolders, movies.RootFolder{
		ID: "incoming", Path: filepath.Join(env.directory, "downloads", "job-pack", "output"),
	})
	env.saveConfig(t, incoming)
	candidates, err := env.service.Scan(ctx, "incoming")
	if err != nil || len(candidates) != 2 {
		t.Fatalf("scan = %+v, %v", candidates, err)
	}
	if _, err := env.service.Import(ctx, movies.ImportInput{RootID: "incoming", Path: first.Name, MovieID: flagged.ID}); err != nil {
		t.Fatalf("manual import: %v", err)
	}
	stored, err := env.service.Store.Get(ctx, flagged.ID)
	if err != nil || len(stored.Files) != 1 {
		t.Fatalf("manual import files = %+v, %v", stored.Files, err)
	}
	if _, err := env.service.Import(ctx, movies.ImportInput{RootID: "incoming", Path: second.Name, MovieID: flagged.ID}); err != nil {
		t.Fatalf("second manual import: %v", err)
	}
}

func TestLegacyImportHydratesMetadataFromProvider(t *testing.T) {
	ctx := context.Background()
	omdb := omdbServer(t, map[string]omdbFixture{
		"tt7000001": {title: "Legacy Film", year: 2015, released: "01 May 2015", rating: "7.7"},
		"tt7000002": {title: "Twin Film", year: 2016, released: "01 Jun 2016"},
		"tt7000003": {title: "Twin Film", year: 2016, released: "01 Jun 2016"},
	})
	env := newTestEnv(t, "")
	cfg := env.config(t)
	cfg.MetadataURL = omdb.URL
	cfg.MetadataAPIKey = "omdb-secret"
	env.saveConfig(t, cfg)

	legacy := nestedOutputFile(t, env, "job-legacy", "Legacy Film (2015)/Legacy.Film.2015.1080p.BluRay.x264.mkv", "legacy-bytes")
	env.completedJob(t, "job-legacy", "rel-legacy", "Legacy Film 2015 1080p BluRay", []downloads.OutputFile{legacy})
	twin := nestedOutputFile(t, env, "job-twin", "Twin Film (2016)/Twin.Film.2016.1080p.BluRay.x264.mkv", "twin-bytes")
	env.completedJob(t, "job-twin", "rel-twin", "Twin Film 2016 1080p BluRay", []downloads.OutputFile{twin})

	imported, err := env.service.SyncDownloads(ctx)
	if err != nil || imported != 2 {
		t.Fatalf("SyncDownloads = %d, %v", imported, err)
	}
	var hydrated, ambiguous *movies.Movie
	for _, movie := range mustList(t, env) {
		switch movie.Metadata.Title {
		case "Legacy Film":
			hydrated = &movie
		case "Twin Film":
			ambiguous = &movie
		}
	}
	if hydrated == nil || hydrated.Metadata.IMDbID != "tt7000001" || hydrated.Metadata.Rating == nil {
		t.Fatalf("provider-confirmed movie = %+v", hydrated)
	}
	if hydrated.Monitored || len(hydrated.Files) != 1 {
		t.Fatalf("legacy adoption must stay unmonitored with its file: %+v", hydrated)
	}
	if ambiguous == nil || ambiguous.Metadata.IMDbID != "" || ambiguous.Metadata.Year != 2016 {
		t.Fatalf("ambiguous movie = %+v, want a manual record without a fabricated IMDb ID", ambiguous)
	}
}

func TestSyncDownloadsContinuesAfterFailedAcquisition(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	failing := env.manualMovie(t, "Failing Film", 2013, true)
	good := env.manualMovie(t, "Good Film", 2014, true)

	source := env.outputFile(t, "job-good", "Good.Film.2014.1080p.BluRay.x264.mkv", "good-bytes")
	env.completedJob(t, "job-good", "rel-good", "Good Film 2014 1080p BluRay", []downloads.OutputFile{source})
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: good.ID, JobID: "job-good", ReleaseID: "rel-good", Title: "Good Film 2014 1080p BluRay", Status: "queued",
	}); err != nil {
		t.Fatalf("SaveAcquisition good: %v", err)
	}
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb, status, error) VALUES ('job-failed', 'rel-failed', 'Failing Film 2013 1080p BluRay', $1, 'failed', 'source error')`,
		[]byte("<nzb/>")); err != nil {
		t.Fatalf("insert failed download: %v", err)
	}
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: failing.ID, JobID: "job-failed", ReleaseID: "rel-failed", Title: "Failing Film 2013 1080p BluRay", Status: "queued",
	}); err != nil {
		t.Fatalf("SaveAcquisition failing: %v", err)
	}
	// Process the failing acquisition first so a failure could stop the loop.
	if _, err := env.pool.Exec(ctx,
		`UPDATE movie_acquisitions SET updated_at = now() + interval '1 second' WHERE job_id = 'job-failed'`); err != nil {
		t.Fatalf("order acquisitions: %v", err)
	}

	imported, err := env.service.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("SyncDownloads = %d, %v", imported, err)
	}
	if acquisition := acquisitionOf(t, env, "job-failed"); acquisition.Status != "failed" || acquisition.Error == "" {
		t.Fatalf("failed acquisition = %+v", acquisition)
	}
	if status := statusOf(t, env, failing.ID); status.Status != "failed" || status.Error == "" {
		t.Fatalf("failing movie = %+v, want a visible download failure", status)
	}
	if status := statusOf(t, env, good.ID); status.Status != "available" {
		t.Fatalf("good movie status = %q, want available", status.Status)
	}
}

func sourceExists(t *testing.T, env *testEnv, jobID, name string) bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(env.directory, "downloads", jobID, "output", filepath.FromSlash(name)))
	if err != nil || len(matches) == 0 {
		return false
	}
	return true
}

func TestCatalogUsesPreferredQualityBeforeScore(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	movie := env.manualMovie(t, "Multiple Versions", 2020, true)
	movie.ProfileID = "hd"
	preferred := env.writeLibraryFile(t, "Multiple.Versions.2020.1080p.BluRay.mkv", "preferred-bytes")
	preferred.Quality = "Bluray-1080p"
	lower := env.writeLibraryFile(t, "Multiple.Versions.2020.720p.WEB-DL.mkv", "lower-bytes")
	lower.Quality, lower.Score = "WEB-720p", 100
	movie.Files = []movies.File{preferred, lower}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatal(err)
	}
	if state := statusOf(t, env, movie.ID); state.Status != "available" {
		t.Fatalf("lower quality with a higher score hid the cutoff file: %s", state.Status)
	}
}
