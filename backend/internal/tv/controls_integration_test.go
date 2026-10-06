package tv_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

// Paused and cancelled TV downloads keep their state and only an explicit grab may restart them.
func TestTVPausedAndCancelledDownloadsStayStopped(t *testing.T) {
	ctx := context.Background()
	release := tvReleaseFixture{
		title: "Big Buck Series 2012 S01E01 1080p WEB-DL-GRP", guid: "release-e01",
		size: 2 << 30, season: 1, episode: 1, imdb: "tt1111111",
	}
	alternative := release
	alternative.guid, alternative.title = "release-e01-alt", "Big Buck Series 2012 S01E01 720p WEB-DL-ALT"
	feed := []tvReleaseFixture{release, alternative}
	indexer := newTVFakeIndexer(t, feed, feed)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, series.ID, 1, 1)

	if result := env.sync(t, true); result.Queued != 1 {
		t.Fatalf("first sync = %+v, want one queued episode", result)
	}
	acquisitions, err := env.service.Store.Acquisitions(ctx)
	if err != nil || len(acquisitions) != 1 {
		t.Fatalf("acquisitions = %+v, %v", acquisitions, err)
	}
	jobID := acquisitions[0].JobID
	grabbedRelease := acquisitions[0].ReleaseID
	otherRelease := release.guid
	if grabbedRelease == release.guid {
		otherRelease = alternative.guid
	}

	if _, err := env.manager.Pause(ctx, jobID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if episode := tvDecoratedEpisode(t, env, series.ID, 1, 1); episode.Status != "paused" {
		t.Fatalf("episode after pause = %q, want paused", episode.Status)
	}
	if result := env.sync(t, true); result.Searched != 0 || result.Queued != 0 {
		t.Fatalf("automation restarted a paused download: %+v", result)
	}

	if _, err := env.manager.Resume(ctx, jobID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if episode := tvDecoratedEpisode(t, env, series.ID, 1, 1); episode.Status != "downloading" {
		t.Fatalf("episode after resume = %q, want downloading", episode.Status)
	}

	if _, err := env.manager.Cancel(ctx, jobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if episode := tvDecoratedEpisode(t, env, series.ID, 1, 1); episode.Status != "cancelled" {
		t.Fatalf("episode after cancellation = %q, want cancelled", episode.Status)
	}
	searches := len(indexer.queries())
	if result := env.sync(t, true); result.Searched != 0 || result.Queued != 0 {
		t.Fatalf("automation requeued a cancelled download: %+v", result)
	}
	if after := len(indexer.queries()); after != searches {
		t.Fatalf("automation searched a cancelled episode: %+v", indexer.queries()[searches:])
	}
	if acquisition := env.acquisition(t, series.ID, jobID); acquisition.Status != "cancelled" {
		t.Fatalf("acquisition after automation = %q, want cancelled", acquisition.Status)
	}

	releases, err := env.service.Search(ctx, series.ID, tv.Target{Season: 1, Episode: 1})
	if err != nil || len(releases) != 2 {
		t.Fatalf("cancelled releases cannot be selected again: %+v, %v", releases, err)
	}
	for _, item := range releases {
		if !item.Decision.Allowed {
			t.Fatalf("cancelled release %s is not selectable: %+v", item.ID, item.Decision)
		}
	}
	retried, err := env.service.Grab(ctx, series.ID, tv.GrabInput{
		Target: tv.Target{Season: 1, Episode: 1}, ReleaseID: grabbedRelease,
	})
	if err != nil || retried.ID != jobID || retried.Status != "queued" {
		t.Fatalf("explicit retry of a cancelled release = %+v, %v", retried, err)
	}

	// Selecting a different release supersedes the cancelled download and starts a new job.
	if _, err := env.manager.Cancel(ctx, jobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	replacement, err := env.service.Grab(ctx, series.ID, tv.GrabInput{
		Target: tv.Target{Season: 1, Episode: 1}, ReleaseID: otherRelease,
	})
	if err != nil || replacement.ID == jobID || replacement.Status != "queued" {
		t.Fatalf("explicit selection of another release = %+v, %v", replacement, err)
	}
	if acquisition := env.acquisition(t, series.ID, jobID); acquisition.Status != "superseded" {
		t.Fatalf("cancelled acquisition after replacement = %q, want superseded", acquisition.Status)
	}
	if acquisition := env.acquisition(t, series.ID, replacement.ID); acquisition.Status != "queued" {
		t.Fatalf("replacement acquisition = %q, want queued", acquisition.Status)
	}
}

// A paused download blocks another release for the same episodes until it is resumed or cancelled.
func TestTVPausedDownloadBlocksOtherReleases(t *testing.T) {
	ctx := context.Background()
	first := tvReleaseFixture{
		title: "Big Buck Series 2012 S01E01 1080p WEB-DL-GRP", guid: "release-e01-a",
		size: 2 << 30, season: 1, episode: 1, imdb: "tt1111111",
	}
	second := first
	second.guid, second.title = "release-e01-b", "Big Buck Series 2012 S01E01 720p WEB-DL-GRP"
	indexer := newTVFakeIndexer(t, []tvReleaseFixture{first, second}, []tvReleaseFixture{first, second})
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, series.ID, 1, 1)
	if result := env.sync(t, true); result.Queued != 1 {
		t.Fatalf("first sync = %+v, want one queued episode", result)
	}
	acquisitions, err := env.service.Store.Acquisitions(ctx)
	if err != nil || len(acquisitions) != 1 {
		t.Fatalf("acquisitions = %+v, %v", acquisitions, err)
	}
	if _, err := env.manager.Pause(ctx, acquisitions[0].JobID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	grabbed := acquisitions[0].ReleaseID
	other := first.guid
	if grabbed == first.guid {
		other = second.guid
	}
	target := tv.Target{Season: 1, Episode: 1}
	if _, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: target, ReleaseID: other}); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("a paused download did not conflict with another release: %v", err)
	}
	if _, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: target, ReleaseID: grabbed}); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("a paused download did not conflict with its own release: %v", err)
	}
}

func tvDecoratedEpisode(t *testing.T, env *tvEnv, seriesID string, season, number int) tv.Episode {
	t.Helper()
	series, err := env.service.Get(context.Background(), seriesID)
	if err != nil {
		t.Fatalf("Get series: %v", err)
	}
	return tvEpisodeByNumber(t, series.Episodes, season, number)
}
