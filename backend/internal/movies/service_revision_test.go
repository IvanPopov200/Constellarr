package movies_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
)

func TestSupersededFailuresFallBackToAvailable(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")

	movie := env.manualMovie(t, "Superseded Film", 2001, true)
	file := env.writeLibraryFile(t, "Superseded Film (2001)/Superseded Film (2001) [Bluray-1080p].mkv", "bytes")
	movie.Files = []movies.File{file}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save movie: %v", err)
	}
	env.completedJob(t, "job-old", "rel-old", "Superseded Film 2001 1080p BluRay", nil)
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: "job-old", ReleaseID: "rel-old", Title: "Superseded Film 2001 1080p BluRay",
		Status: "failed", Error: "download failed: old",
	}); err != nil {
		t.Fatalf("SaveAcquisition failed: %v", err)
	}
	if status := statusOf(t, env, movie.ID); status.Status != "failed" {
		t.Fatalf("movie status with a failed download = %q, want failed", status.Status)
	}

	// A newer completed import supersedes the older failure.
	env.completedJob(t, "job-new", "rel-new", "Superseded Film 2001 1080p PROPER", nil)
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: "job-new", ReleaseID: "rel-new", Title: "Superseded Film 2001 1080p PROPER",
		Status: "imported",
	}); err != nil {
		t.Fatalf("SaveAcquisition imported: %v", err)
	}
	reloaded := statusOf(t, env, movie.ID)
	if reloaded.Status != "available" || reloaded.Error != "" {
		t.Fatalf("movie after a newer import = %q/%q, want available without an error", reloaded.Status, reloaded.Error)
	}
	if len(reloaded.Files) != 1 || reloaded.Files[0].Missing {
		t.Fatalf("movie files after a newer import = %+v", reloaded.Files)
	}

	// A superseded acquisition never drives the state even when it is the newest row.
	abandoned := env.manualMovie(t, "Abandoned Film", 2002, true)
	abandonedFile := env.writeLibraryFile(t, "Abandoned Film (2002)/Abandoned Film (2002) [Bluray-1080p].mkv", "bytes")
	abandoned.Files = []movies.File{abandonedFile}
	if _, err := env.service.Store.Save(ctx, abandoned); err != nil {
		t.Fatalf("Save abandoned movie: %v", err)
	}
	env.completedJob(t, "job-abandoned", "rel-abandoned", "Abandoned Film 2002 1080p BluRay", nil)
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: abandoned.ID, JobID: "job-abandoned", ReleaseID: "rel-abandoned",
		Title: "Abandoned Film 2002 1080p BluRay", Status: "superseded", Error: "superseded by a newer release grab",
	}); err != nil {
		t.Fatalf("SaveAcquisition superseded: %v", err)
	}
	reloaded = statusOf(t, env, abandoned.ID)
	if reloaded.Status != "available" || reloaded.Error != "" {
		t.Fatalf("movie with a superseded acquisition = %q/%q, want available without an error", reloaded.Status, reloaded.Error)
	}
}

func TestRenamePreviewAndApplySingleFile(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	movie := env.manualMovie(t, "Rename Film", 2003, true)
	const original = "incoming/Rename.Film.2003.mkv"
	env.writeLibraryFile(t, original, "rename-bytes")
	movie.Files = []movies.File{{RootID: env.rootID, Path: original, Size: int64(len("rename-bytes")), Quality: "Bluray-1080p", ImportedAt: time.Now().UTC()}}
	movie.Tags = []string{"keep"}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save movie: %v", err)
	}

	preview, err := env.service.Rename(ctx, movie.ID, true)
	if err != nil {
		t.Fatalf("Rename preview: %v", err)
	}
	if preview.Applied || len(preview.Files) != 1 || preview.Files[0].From != original ||
		preview.Files[0].To == original || !strings.Contains(preview.Files[0].To, "Rename Film (2003)") {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(original))); err != nil {
		t.Fatalf("preview moved the file: %v", err)
	}
	if stored, err := env.service.Store.Get(ctx, movie.ID); err != nil || len(stored.Files) != 1 || stored.Files[0].Path != original {
		t.Fatalf("preview rewrote the stored files: %+v, %v", stored.Files, err)
	}

	applied, err := env.service.Rename(ctx, movie.ID, false)
	if err != nil {
		t.Fatalf("Rename apply: %v", err)
	}
	if !applied.Applied || len(applied.Files) != 1 || applied.Files[0].To != preview.Files[0].To {
		t.Fatalf("apply = %+v, want the previewed plan", applied)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(applied.Files[0].To))); err != nil {
		t.Fatalf("renamed file is missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(original))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original path still exists after rename: %v", err)
	}
	stored, err := env.service.Store.Get(ctx, movie.ID)
	if err != nil || len(stored.Files) != 1 || stored.Files[0].Path != applied.Files[0].To || stored.Files[0].Size != int64(len("rename-bytes")) {
		t.Fatalf("stored files after rename = %+v, %v", stored.Files, err)
	}
	if len(stored.Tags) != 1 || stored.Tags[0] != "keep" {
		t.Fatalf("rename dropped editable fields: %+v", stored)
	}
	listed := statusOf(t, env, movie.ID)
	if listed.Status != "available" || len(listed.Files) != 1 || listed.Files[0].Missing {
		t.Fatalf("movie after rename = %q, %+v", listed.Status, listed.Files)
	}
}

func TestRenameAppliesMultipartSiblingsTogether(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	movie := env.manualMovie(t, "Multipart Film", 2004, true)
	first, second := "incoming/Multipart.Film.part1.mkv", "incoming/Multipart.Film.part2.mkv"
	env.writeLibraryFile(t, first, "part-one")
	env.writeLibraryFile(t, second, "part-two")
	movie.Files = []movies.File{
		{RootID: env.rootID, Path: first, Size: int64(len("part-one")), Quality: "Bluray-1080p", ImportedAt: time.Now().UTC()},
		{RootID: env.rootID, Path: second, Size: int64(len("part-two")), Quality: "Bluray-1080p", ImportedAt: time.Now().UTC()},
	}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save movie: %v", err)
	}

	result, err := env.service.Rename(ctx, movie.ID, false)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if !result.Applied || len(result.Files) != 2 {
		t.Fatalf("rename result = %+v, want two renamed files", result)
	}
	parts := map[string]string{}
	for _, renamed := range result.Files {
		if renamed.From == renamed.To || !strings.Contains(renamed.To, "Multipart Film (2004)") {
			t.Fatalf("planned rename = %+v", renamed)
		}
		parts[renamed.To] = renamed.From
	}
	if len(parts) != 2 {
		t.Fatalf("siblings were mapped onto the same destination: %+v", result.Files)
	}
	for to, from := range parts {
		handle, err := env.service.OpenFile(ctx, movie.ID, to)
		if err != nil {
			t.Fatalf("renamed part %s is not available: %v", to, err)
		}
		content, readErr := io.ReadAll(handle)
		handle.Close()
		want := "part-two"
		if from == first {
			want = "part-one"
		}
		if readErr != nil || string(content) != want {
			t.Fatalf("content of %s (from %s) = %q, %v; want %q", to, from, content, readErr, want)
		}
		if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(from))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source %s still exists after rename", from)
		}
	}
	stored, err := env.service.Store.Get(ctx, movie.ID)
	if err != nil || len(stored.Files) != 2 {
		t.Fatalf("stored files after multipart rename = %+v, %v", stored.Files, err)
	}
	for _, file := range stored.Files {
		if _, ok := parts[file.Path]; !ok {
			t.Fatalf("stored file %s is not one of the renamed destinations %+v", file.Path, parts)
		}
	}
}

func TestRenamePreservesConcurrentEditableFields(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	movie := env.manualMovie(t, "Concurrent Rename", 2005, true)
	const original = "incoming/Concurrent.Rename.2005.mkv"
	env.writeLibraryFile(t, original, "concurrent-bytes")
	movie.Files = []movies.File{{RootID: env.rootID, Path: original, Size: int64(len("concurrent-bytes")), Quality: "Bluray-1080p", ImportedAt: time.Now().UTC()}}
	movie.Tags = []string{"stale"}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save movie: %v", err)
	}

	stop := make(chan struct{})
	var edits sync.WaitGroup
	edits.Add(1)
	go func() {
		defer edits.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := env.service.Update(ctx, movie.ID, movies.Movie{
				Monitored: false, ProfileID: "hd", RootID: env.rootID,
				Tags: []string{"keep"}, Collection: "Kept",
			}); err != nil {
				return
			}
		}
	}()
	result, err := env.service.Rename(ctx, movie.ID, false)
	close(stop)
	edits.Wait()
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if !result.Applied || len(result.Files) != 1 {
		t.Fatalf("rename result = %+v", result)
	}
	stored, err := env.service.Store.Get(ctx, movie.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Monitored || stored.Collection != "Kept" || strings.Join(stored.Tags, ",") != "keep" {
		t.Fatalf("rename reverted concurrent edits: %+v", stored)
	}
	if len(stored.Files) != 1 || stored.Files[0].Path != result.Files[0].To {
		t.Fatalf("rename did not persist its own field: %+v", stored.Files)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(result.Files[0].To))); err != nil {
		t.Fatalf("renamed file is missing: %v", err)
	}
}

func TestGrabRejectsPrefixTitleMismatch(t *testing.T) {
	ctx := context.Background()
	indexer := indexerServer(t, map[string][]releaseFixture{
		"": {
			{title: "Grown Ups 2009 1080p BluRay x264-GRP", guid: "rel-grownups", size: 8 << 30},
			{title: "Up 2009 1080p BluRay x264-GRP", guid: "rel-up", size: 8 << 30},
		},
	})
	env := newTestEnv(t, indexer.URL)
	movie := env.manualMovie(t, "Up", 2009, true)

	if _, err := env.service.Grab(ctx, movie.ID, "rel-grownups", false); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("prefix title grab error = %v, want ErrInvalid", err)
	}
	job, err := env.service.Grab(ctx, movie.ID, "rel-up", false)
	if err != nil || job.ID == "" {
		t.Fatalf("matching grab = %+v, %v", job, err)
	}
}

func TestGrabOverrideSupersedesStuckImport(t *testing.T) {
	ctx := context.Background()
	indexer := indexerServer(t, map[string][]releaseFixture{
		"": {
			{title: "Stuck Film 2006 1080p BluRay x264-GRP", guid: "rel-stuck", size: 8 << 30},
			{title: "Stuck Film 2006 1080p WEB-DL x264-GRP", guid: "rel-fresh", size: 4 << 30},
		},
	})
	env := newTestEnv(t, indexer.URL)
	movie := env.manualMovie(t, "Stuck Film", 2006, true)
	env.completedJob(t, "job-stuck", "rel-stuck", "Stuck Film 2006 1080p BluRay", nil)
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: "job-stuck", ReleaseID: "rel-stuck",
		Title: "Stuck Film 2006 1080p BluRay", Status: "import-failed", Error: "import failed: synthetic",
	}); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}

	if _, err := env.service.Grab(ctx, movie.ID, "rel-fresh", false); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("grab without override error = %v, want ErrConflict", err)
	}
	job, err := env.service.Grab(ctx, movie.ID, "rel-fresh", true)
	if err != nil || job.ID == "" {
		t.Fatalf("override grab = %+v, %v", job, err)
	}
	statuses := map[string]string{}
	acquisitions, err := env.service.Store.Acquisitions(ctx)
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	for _, acquisition := range acquisitions {
		statuses[acquisition.ReleaseID] = acquisition.Status
	}
	if statuses["rel-stuck"] != "superseded" || statuses["rel-fresh"] != "queued" {
		t.Fatalf("acquisition statuses = %+v, want the stuck import superseded", statuses)
	}
	if status := statusOf(t, env, movie.ID); status.Status != "downloading" {
		t.Fatalf("movie status after an override grab = %q, want downloading", status.Status)
	}
}

func TestSetConfigValidatesBeforeCreatingRoots(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	cfg := env.config(t)

	absent := filepath.Join(env.directory, "never-created")
	cfg.RootFolders = []movies.RootFolder{{ID: "first", Path: absent}, {ID: "second", Path: absent}}
	if _, err := env.service.SetConfig(ctx, cfg); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("duplicate root config error = %v, want ErrInvalid", err)
	}
	if _, err := os.Stat(absent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid root config created %s", absent)
	}

	relative := env.config(t)
	relative.RootFolders = []movies.RootFolder{{ID: "relative", Path: "relative-movie-root"}}
	if _, err := env.service.SetConfig(ctx, relative); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("relative root config error = %v, want ErrInvalid", err)
	}
	if _, err := os.Stat("relative-movie-root"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("relative root config created a working directory")
	}
}
