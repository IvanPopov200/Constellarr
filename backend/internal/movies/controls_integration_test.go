package movies_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
)

// A newer import outranks an older cancelled acquisition instead of leaving the movie cancelled.
func TestCancelledMovieAcquisitionLosesToNewerImport(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	movie := env.manualMovie(t, "Newer Import Film", 2018, true)
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb, status) VALUES ('job-old', 'rel-old', 'Newer Import Film 2018 720p WEB-DL', '\x00'::bytea, 'cancelled')`); err != nil {
		t.Fatalf("insert cancelled download: %v", err)
	}
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO movie_acquisitions (movie_id, job_id, release, status, updated_at)
		 VALUES ($1, 'job-old', '{}'::jsonb, 'queued', now() - interval '1 hour')`, movie.ID); err != nil {
		t.Fatalf("insert cancelled acquisition: %v", err)
	}
	if got := statusOf(t, env, movie.ID); got.Status != "cancelled" {
		t.Fatalf("movie before import = %q, want cancelled", got.Status)
	}

	source := env.outputFile(t, "job-new", "Newer.Import.Film.2018.1080p.BluRay.x264-GRP.mkv", "film-bytes")
	env.completedJob(t, "job-new", "rel-new", "Newer Import Film 2018 1080p BluRay", []downloads.OutputFile{source})
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: "job-new", ReleaseID: "rel-new", Title: "Newer Import Film 2018 1080p BluRay", Status: "queued",
	}); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}
	if imported, err := env.service.SyncDownloads(ctx); err != nil || imported != 1 {
		t.Fatalf("SyncDownloads = %d, %v, want the newer release imported", imported, err)
	}

	saved := statusOf(t, env, movie.ID)
	if saved.Status == "cancelled" || len(saved.Files) == 0 {
		t.Fatalf("movie after newer import = %q with %d files, want the import to win", saved.Status, len(saved.Files))
	}
	if acquisition := acquisitionOf(t, env, "job-new"); acquisition.Status != "imported" {
		t.Fatalf("newer acquisition = %+v, want imported", acquisition)
	}
	if acquisition := acquisitionOf(t, env, "job-old"); acquisition.Status != "cancelled" {
		t.Fatalf("cancelled acquisition = %+v, want its state preserved", acquisition)
	}
}

// A paused download blocks other releases for the movie until it is resumed or cancelled.
func TestPausedMovieDownloadBlocksOtherReleases(t *testing.T) {
	ctx := context.Background()
	indexer := indexerServer(t, map[string][]releaseFixture{
		"": {
			{title: "Blocked Film 2005 1080p BluRay x264-GRP", guid: "blocked-release-a", size: 2 << 30},
			{title: "Blocked Film 2005 1080p BluRay x265-OTH", guid: "blocked-release-b", size: 2 << 30},
		},
	})
	env := newTestEnv(t, indexer.URL)
	movie := env.manualMovie(t, "Blocked Film", 2005, true)
	job, err := env.service.Grab(ctx, movie.ID, "blocked-release-a", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.manager.Pause(ctx, job.ID); err != nil {
		t.Fatal(err)
	}

	releases, err := env.service.Search(ctx, movie.ID)
	if err != nil || len(releases) != 2 {
		t.Fatalf("search while paused = %+v, %v", releases, err)
	}
	for _, release := range releases {
		if release.Decision.Allowed {
			t.Fatalf("paused download left %s selectable: %+v", release.ID, release.Decision)
		}
	}
	if _, err := env.service.Grab(ctx, movie.ID, "blocked-release-b", false); !errors.Is(err, movies.ErrConflict) ||
		!strings.Contains(err.Error(), "another release for this movie is already being processed") {
		t.Fatalf("grabbing another release while paused = %v, want a paused conflict", err)
	}
	if _, err := env.service.Grab(ctx, movie.ID, "blocked-release-a", false); !errors.Is(err, movies.ErrConflict) ||
		!strings.Contains(err.Error(), "this release is already being processed") {
		t.Fatalf("grabbing the paused release = %v, want a paused conflict", err)
	}
}
