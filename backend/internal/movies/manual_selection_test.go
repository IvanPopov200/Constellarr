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
