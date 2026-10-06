package movies_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestManualMovieDoesNotAuthorizeAutomaticDownload(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	indexer := indexerServer(t, map[string][]releaseFixture{
		"": {{title: "Manual Film 2005 1080p BluRay", guid: "manual-release", size: 2 << 30}},
	})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		indexer.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(proxy.Close)
	env := newTestEnv(t, proxy.URL)
	movie := env.manualMovie(t, "Manual Film", 2005, false)
	for _, force := range []bool{false, true} {
		result, err := env.service.Sync(ctx, force)
		if err != nil || result.Searched != 0 || result.Queued != 0 {
			t.Fatalf("automatic sync of a manual movie = %+v, %v", result, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("adding a manual movie made %d indexer requests", calls.Load())
	}
	releases, err := env.service.Search(ctx, movie.ID)
	if err != nil || len(releases) != 1 {
		t.Fatalf("manual release search = %+v, %v", releases, err)
	}
	jobs, err := env.manager.List(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("searching releases queued a download: %+v, %v", jobs, err)
	}
	job, err := env.service.Grab(ctx, movie.ID, releases[0].ID, false)
	if err != nil || job.Status != "queued" {
		t.Fatalf("explicit release selection = %+v, %v", job, err)
	}
}

func TestPausedAndCancelledMovieDownloadsStayStopped(t *testing.T) {
	ctx := context.Background()
	indexer := indexerServer(t, map[string][]releaseFixture{
		"": {{title: "Controlled Film 2005 1080p BluRay", guid: "controlled-release", size: 2 << 30}},
	})
	env := newTestEnv(t, indexer.URL)
	movie := env.manualMovie(t, "Controlled Film", 2005, true)
	job, err := env.service.Grab(ctx, movie.ID, "controlled-release", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.manager.Pause(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if movie := statusOf(t, env, movie.ID); movie.Status != "paused" {
		t.Fatalf("movie after pause = %q", movie.Status)
	}
	result, err := env.service.Sync(ctx, true)
	if err != nil || result.Searched != 0 || result.Queued != 0 {
		t.Fatalf("automation restarted a paused download: %+v, %v", result, err)
	}
	if _, err := env.manager.Resume(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if movie := statusOf(t, env, movie.ID); movie.Status != "downloading" {
		t.Fatalf("movie after resume = %q", movie.Status)
	}
	if _, err := env.manager.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if movie := statusOf(t, env, movie.ID); movie.Status != "cancelled" {
		t.Fatalf("movie after cancellation = %q", movie.Status)
	}
	result, err = env.service.Sync(ctx, true)
	if err != nil || result.Searched != 0 || result.Queued != 0 {
		t.Fatalf("automation restarted a cancelled download: %+v, %v", result, err)
	}
	releases, err := env.service.Search(ctx, movie.ID)
	if err != nil || len(releases) != 1 || !releases[0].Decision.Allowed {
		t.Fatalf("cancelled release cannot be selected again: %+v, %v", releases, err)
	}
	retried, err := env.service.Grab(ctx, movie.ID, releases[0].ID, false)
	if err != nil || retried.ID != job.ID || retried.Status != "queued" {
		t.Fatalf("explicitly retrying a cancelled release = %+v, %v", retried, err)
	}
}
