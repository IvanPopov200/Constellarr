package tv_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

type tvReleaseFixture struct {
	title   string
	guid    string
	size    int64
	season  int
	episode int
	imdb    string
}

type tvFakeIndexer struct {
	server *httptest.Server

	mu        sync.Mutex
	tvQueries []url.Values
	rssCalls  int
	tvFeed    []tvReleaseFixture
	rssFeed   []tvReleaseFixture
}

func newTVFakeIndexer(t *testing.T, tvFeed, rssFeed []tvReleaseFixture) *tvFakeIndexer {
	t.Helper()
	fake := &tvFakeIndexer{tvFeed: tvFeed, rssFeed: rssFeed}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("t") {
		case "tvsearch":
			fake.mu.Lock()
			fake.tvQueries = append(fake.tvQueries, r.URL.Query())
			items := fake.tvFeed
			fake.mu.Unlock()
			writeTVFeed(w, items)
		case "search":
			if r.URL.Query().Get("cat") != "5000" {
				writeTVFeed(w, nil)
				return
			}
			fake.mu.Lock()
			fake.rssCalls++
			items := fake.rssFeed
			fake.mu.Unlock()
			writeTVFeed(w, items)
		case "get":
			w.Header().Set("Content-Type", "application/x-nzb")
			fmt.Fprint(w, `<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><file subject="[1/1] - &quot;episode.mkv&quot; yEnc (1/1)" date="1136214245" poster="poster &lt;poster@example.com&gt;"><groups><group>alt.binaries.test</group></groups><segments><segment bytes="1024" number="1">synthetic-part@example.com</segment></segments></file></nzb>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *tvFakeIndexer) url() string { return f.server.URL }

func (f *tvFakeIndexer) setFeeds(tvFeed, rssFeed []tvReleaseFixture) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tvFeed, f.rssFeed = tvFeed, rssFeed
}

func (f *tvFakeIndexer) queries() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values{}, f.tvQueries...)
}

func (f *tvFakeIndexer) rssCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rssCalls
}

func writeTVFeed(w http.ResponseWriter, items []tvReleaseFixture) {
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace
	var body strings.Builder
	body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>`)
	for _, item := range items {
		fmt.Fprintf(&body, `<item><title>%s</title><guid isPermaLink="false">%s</guid><pubDate>Fri, 02 Jan 2026 10:00:00 +0000</pubDate><enclosure url="https://example.invalid/%s.nzb" length="%d"/>`,
			escape(item.title), escape(item.guid), escape(item.guid), item.size)
		fmt.Fprintf(&body, `<newznab:attr name="size" value="%d"/>`, item.size)
		fmt.Fprintf(&body, `<newznab:attr name="season" value="%d"/>`, item.season)
		if item.episode > 0 {
			fmt.Fprintf(&body, `<newznab:attr name="episode" value="%d"/>`, item.episode)
		}
		if item.imdb != "" {
			fmt.Fprintf(&body, `<newznab:attr name="imdbid" value="%s"/>`, escape(item.imdb))
		}
		body.WriteString(`</item>`)
	}
	body.WriteString(`</channel></rss>`)
	w.Header().Set("Content-Type", "application/rss+xml")
	fmt.Fprint(w, body.String())
}

func (e *tvEnv) sync(t *testing.T, force bool) movies.SyncResult {
	t.Helper()
	result, err := e.service.Sync(context.Background(), force)
	if err != nil && !errors.Is(err, downloads.ErrNotConfigured) {
		t.Fatalf("Sync: %v", err)
	}
	return result
}

func tvQueriesForEpisode(queries []url.Values, episode int) int {
	count := 0
	for _, query := range queries {
		if query.Get("ep") == fmt.Sprint(episode) {
			count++
		}
	}
	return count
}

func TestTVAutomationSkipsFutureUnknownAndUnmonitored(t *testing.T) {
	release := tvReleaseFixture{title: "Big Buck Series 2012 S01E03 1080p WEB-DL-GRP", guid: "release-e03", size: 2 << 30, season: 1, episode: 3, imdb: "tt1111111"}
	indexer := newTVFakeIndexer(t, []tvReleaseFixture{release}, nil)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisodeWithAirDate(t, series.ID, 1, 1, "2999-01-01")
	env.addEpisodeWithAirDate(t, series.ID, 1, 2, "")
	env.addEpisodeWithAirDate(t, series.ID, 1, 3, "2012-01-03")
	unmonitoredSeries := env.addSeries(t, "Other Show", 2011, "tt2222222")
	env.addEpisode(t, unmonitoredSeries.ID, 1, 1)
	env.patchMonitored(t, unmonitoredSeries.ID, false)

	result := env.sync(t, false)
	if result.Imported != 0 || result.Searched != 1 || result.Queued != 1 {
		t.Fatalf("sync result = %+v, want one search and one grab", result)
	}
	queries := indexer.queries()
	if len(queries) == 0 || tvQueriesForEpisode(queries, 3) == 0 {
		t.Fatalf("searches = %+v, want the past episode", queries)
	}
	if tvQueriesForEpisode(queries, 1) != 0 || tvQueriesForEpisode(queries, 2) != 0 {
		t.Fatalf("future or unknown episodes were searched: %+v", queries)
	}
	if got := env.episode(t, series.ID, 1, 1); got.LastSearchAt != nil {
		t.Fatalf("future episode has a search timestamp")
	}
	if got := env.episode(t, series.ID, 1, 2); got.LastSearchAt != nil {
		t.Fatalf("unknown episode has a search timestamp")
	}
	if got := env.episode(t, series.ID, 1, 3); got.LastSearchAt == nil {
		t.Fatalf("past episode was not marked as searched")
	}
	if got := env.episode(t, unmonitoredSeries.ID, 1, 1); got.LastSearchAt != nil {
		t.Fatalf("unmonitored episode was searched")
	}
	acquisitions, err := env.service.Store.Acquisitions(context.Background())
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	if len(acquisitions) != 1 || acquisitions[0].ReleaseID != "release-e03" || acquisitions[0].SeriesID != series.ID || acquisitions[0].Status == "" {
		t.Fatalf("acquisitions = %+v, want only the past episode grab", acquisitions)
	}
}

func (e *tvEnv) addEpisodeWithAirDate(t *testing.T, seriesID string, season, number int, airDate string) tv.Episode {
	t.Helper()
	episode, err := e.service.AddEpisode(context.Background(), seriesID, tv.Episode{
		Title: fmt.Sprintf("Episode %d", number), Season: season, Number: number,
		AirDate: airDate, Monitored: true,
	})
	if err != nil {
		t.Fatalf("AddEpisode(S%02dE%02d): %v", season, number, err)
	}
	return episode
}

func (e *tvEnv) patchMonitored(t *testing.T, seriesID string, monitored bool) {
	t.Helper()
	if _, err := e.service.Store.Patch(context.Background(), seriesID, map[string]any{"monitored": monitored}); err != nil {
		t.Fatalf("patch monitored: %v", err)
	}
}

func TestTVAutomationSearchWindowAndForce(t *testing.T) {
	indexer := newTVFakeIndexer(t, nil, nil)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, series.ID, 1, 1)

	if result := env.sync(t, false); result.Searched != 1 || result.Queued != 0 {
		t.Fatalf("first sync = %+v, want one search without a grab", result)
	}
	if count := len(indexer.queries()); count != 1 {
		t.Fatalf("tv searches after first sync = %d, want 1", count)
	}
	if result := env.sync(t, false); result.Searched != 0 {
		t.Fatalf("second sync = %+v, want the search window to hold", result)
	}
	if count := len(indexer.queries()); count != 1 {
		t.Fatalf("tv searches after second sync = %d, want 1", count)
	}
	if result := env.sync(t, true); result.Searched != 1 {
		t.Fatalf("forced sync = %+v, want one search", result)
	}
	if count := len(indexer.queries()); count != 2 {
		t.Fatalf("tv searches after forced sync = %d, want 2", count)
	}
}

func TestTVAutomationRSSMatchesStrictSeriesIdentity(t *testing.T) {
	rssFeed := []tvReleaseFixture{
		{title: "Big Buck Series 2012 S01E01 1080p WEB-DL-GRP", guid: "rss-a", size: 2 << 30, season: 1, episode: 1, imdb: "tt1111111"},
		{title: "Other Show 2011 S01E01 1080p WEB-DL-GRP", guid: "rss-b", size: 2 << 30, season: 1, episode: 1, imdb: "tt2222222"},
		{title: "Big Buck Series 2012 S01E05 1080p WEB-DL-GRP", guid: "rss-unknown", size: 2 << 30, season: 1, episode: 5, imdb: "tt1111111"},
	}
	indexer := newTVFakeIndexer(t, rssFeed, rssFeed)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	first := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, first.ID, 1, 1)
	second := env.addSeries(t, "Other Show", 2011, "tt2222222")
	env.addEpisode(t, second.ID, 1, 1)

	result := env.sync(t, false)
	if result.Queued != 2 || result.Searched != 0 {
		t.Fatalf("RSS sync = %+v, rssCalls=%d tvQueries=%+v, want two RSS grabs", result, indexer.rssCount(), indexer.queries())
	}
	if indexer.rssCount() == 0 {
		t.Fatalf("the TV RSS feed was not requested")
	}
	acquisitions, err := env.service.Store.Acquisitions(context.Background())
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	byRelease := map[string]string{}
	for _, acquisition := range acquisitions {
		byRelease[acquisition.ReleaseID] = acquisition.SeriesID
	}
	if byRelease["rss-a"] != first.ID || byRelease["rss-b"] != second.ID {
		t.Fatalf("RSS grabs = %+v, want each series matched to its own release", byRelease)
	}
	if _, ok := byRelease["rss-unknown"]; ok {
		t.Fatalf("an episode outside the catalog was grabbed: %+v", byRelease)
	}
}

func TestTVAutomationBlocksFailedReleaseAndGrabsAlternate(t *testing.T) {
	failedRelease := tvReleaseFixture{title: "Big Buck Series 2012 S01E01 1080p WEB-DL-BAD", guid: "release-bad", size: 2 << 30, season: 1, episode: 1, imdb: "tt1111111"}
	alternate := tvReleaseFixture{title: "Big Buck Series 2012 S01E01 1080p BluRay-GOOD", guid: "release-good", size: 2 << 30, season: 1, episode: 1, imdb: "tt1111111"}
	indexer := newTVFakeIndexer(t, []tvReleaseFixture{failedRelease, alternate}, nil)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	episode := env.addEpisode(t, series.ID, 1, 1)

	env.failedJob(t, "job-bad", "release-bad", "Big Buck Series S01E01 1080p WEB-DL-BAD", "the download failed")
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-bad",
		ReleaseID: "release-bad", Title: "Big Buck Series S01E01 1080p WEB-DL-BAD", Status: "queued",
	})

	result := env.sync(t, false)
	if result.Searched != 1 || result.Queued != 1 {
		t.Fatalf("retry sync = %+v, want one search and one alternate grab", result)
	}
	failed := env.acquisition(t, series.ID, "job-bad")
	if failed.Status != "superseded" || failed.Error == "" {
		t.Fatalf("failed acquisition = %+v, want the alternate grab to supersede it", failed)
	}
	blocked, err := env.service.Store.Blocked(context.Background(), series.ID, "release-bad")
	if err != nil || !blocked {
		t.Fatalf("failed release blocked = %v (%v), want true", blocked, err)
	}
	acquisitions, err := env.service.Store.Acquisitions(context.Background())
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	grabbed := ""
	for _, acquisition := range acquisitions {
		if acquisition.ReleaseID == "release-good" {
			grabbed = acquisition.Status
		}
		if acquisition.ReleaseID == "release-bad" && acquisition.Status != "failed" && acquisition.Status != "superseded" {
			t.Fatalf("blocked release was queued again: %+v", acquisition)
		}
	}
	if grabbed == "" {
		t.Fatalf("alternate release was not grabbed: %+v", acquisitions)
	}
	if got := env.episode(t, series.ID, 1, 1); got.Error == "" {
		t.Fatalf("failed download did not surface an episode error")
	}
}

func TestTVAutomationImportsBeforeProviderFailures(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "indexer down", http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	env := newTVEnv(t, library.ModeLink, failing.URL)
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	episode := env.addEpisode(t, series.ID, 1, 1)
	file := env.outputFile(t, "job-import", "Big.Buck.Series.2012.S01E01.1080p.WEB-DL.mkv", "import-first")
	env.completedJob(t, "job-import", "release-import", "Big Buck Series 2012 S01E01 1080p WEB-DL", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{episode.ID}, JobID: "job-import",
		ReleaseID: "release-import", Title: "Big Buck Series 2012 S01E01 1080p WEB-DL", Status: "queued",
	})

	result, err := env.service.Sync(context.Background(), false)
	if err == nil {
		t.Fatalf("provider failures were not reported")
	}
	if result.Imported != 1 {
		t.Fatalf("import count = %d, want the import before provider work", result.Imported)
	}
	imported := env.episode(t, series.ID, 1, 1)
	if len(imported.Files) != 1 || env.readFile(t, imported.Files[0].Path) != "import-first" {
		t.Fatalf("episode files = %+v, want the completed download imported", imported.Files)
	}
	if acquisition := env.acquisition(t, series.ID, "job-import"); acquisition.Status != "imported" {
		t.Fatalf("acquisition status = %q, want imported", acquisition.Status)
	}
}

func TestTVAutomationSearchesGapAfterPartialImport(t *testing.T) {
	alternate := tvReleaseFixture{title: "Big Buck Series 2012 S01E02 1080p BluRay-GOOD", guid: "release-gap", size: 4 << 30, season: 1, episode: 2, imdb: "tt1111111"}
	indexer := newTVFakeIndexer(t, []tvReleaseFixture{alternate}, nil)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	first := env.addEpisode(t, series.ID, 1, 1)
	second := env.addEpisode(t, series.ID, 1, 2)

	file := env.outputFile(t, "job-gap", "Big.Buck.Series.2012.S01E01.Bluray-1080p.mkv", "gap-1")
	env.completedJob(t, "job-gap", "release-gap-original", "Big Buck Series 2012 S01E01E02 Bluray-1080p", []downloads.OutputFile{file})
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{first.ID, second.ID}, JobID: "job-gap",
		ReleaseID: "release-gap-original", Title: "Big Buck Series 2012 S01E01E02 Bluray-1080p", Status: "queued",
	})
	result := env.sync(t, false)
	if result.Imported != 1 || result.Searched != 1 || result.Queued != 1 {
		t.Fatalf("gap sync = %+v, want one import, one search, and one alternate grab", result)
	}
	if imported := env.episode(t, series.ID, 1, 1); len(imported.Files) != 1 {
		t.Fatalf("imported episode files = %+v", imported.Files)
	}
	blocked, err := env.service.Store.Blocked(context.Background(), series.ID, "release-gap-original")
	if err != nil || !blocked {
		t.Fatalf("incomplete release blocked = %v (%v), want true", blocked, err)
	}
	acquisitions, err := env.service.Store.Acquisitions(context.Background())
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	grabbed := false
	for _, acquisition := range acquisitions {
		if acquisition.ReleaseID == "release-gap" {
			grabbed = true
		}
	}
	if !grabbed {
		t.Fatalf("no alternate acquisition for the gap episode: %+v", acquisitions)
	}
	for _, query := range indexer.queries() {
		if query.Get("ep") == "1" {
			t.Fatalf("the already imported episode was searched: %+v", indexer.queries())
		}
	}
}

func TestTVAutomationPerEpisodeActiveDoesNotWedgeSeries(t *testing.T) {
	release := tvReleaseFixture{title: "Big Buck Series 2012 S01E02 1080p WEB-DL-GRP", guid: "release-e02", size: 2 << 30, season: 1, episode: 2, imdb: "tt1111111"}
	indexer := newTVFakeIndexer(t, []tvReleaseFixture{release}, nil)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	first := env.addEpisode(t, series.ID, 1, 1)
	second := env.addEpisode(t, series.ID, 1, 2)

	env.insertJob(t, "job-active", "release-active", "Big Buck Series 2012 S01E01 1080p WEB-DL", "downloading", "", nil)
	env.saveAcquisition(t, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{first.ID}, JobID: "job-active",
		ReleaseID: "release-active", Title: "Big Buck Series 2012 S01E01 1080p WEB-DL", Status: "queued",
	})
	result := env.sync(t, false)
	if result.Searched != 1 || result.Queued != 1 {
		t.Fatalf("per-episode sync = %+v, want the free episode searched and grabbed", result)
	}
	if tvQueriesForEpisode(indexer.queries(), 2) == 0 {
		t.Fatalf("free episode was not searched: %+v", indexer.queries())
	}
	if tvQueriesForEpisode(indexer.queries(), 1) != 0 {
		t.Fatalf("episode with an in-flight acquisition was searched: %+v", indexer.queries())
	}
	if _, err := env.service.Store.Episode(context.Background(), second.ID); err != nil {
		t.Fatalf("read second episode: %v", err)
	}
}

func TestTVAutomationSearchesSeasonZero(t *testing.T) {
	release := tvReleaseFixture{title: "Big Buck Series 2012 S00E01 1080p WEB-DL-GRP", guid: "release-s00", size: 2 << 30, season: 0, episode: 1, imdb: "tt1111111"}
	indexer := newTVFakeIndexer(t, []tvReleaseFixture{release}, nil)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	env.addEpisode(t, series.ID, 0, 1)

	result := env.sync(t, false)
	if result.Searched != 1 || result.Queued != 1 {
		t.Fatalf("season zero sync = %+v, want one search and one grab", result)
	}
	found := false
	for _, query := range indexer.queries() {
		if query.Get("season") == "0" && query.Get("ep") == "1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("season zero was not searched: %+v", indexer.queries())
	}
}

func TestTVAutomationNeverSearchesFutureOrUnknownUpgrades(t *testing.T) {
	indexer := newTVFakeIndexer(t, nil, nil)
	env := newTVEnv(t, library.ModeLink, indexer.url())
	series := env.addSeries(t, "Big Buck Series", 2012, "tt1111111")
	future := env.addEpisodeWithAirDate(t, series.ID, 1, 1, "2999-01-01")
	unknown := env.addEpisodeWithAirDate(t, series.ID, 1, 2, "")
	for _, episode := range []tv.Episode{future, unknown} {
		rel := fmt.Sprintf("Big Buck Series (2012)/Season 01/Big Buck Series - S01E%02d [HDTV-720p].mkv", episode.Number)
		file := env.libraryFile(t, rel, "upgrade-me", "HDTV-720p")
		env.patchFiles(t, episode.ID, []movies.File{file})
	}
	result := env.sync(t, false)
	if result.Searched != 0 || result.Queued != 0 || len(indexer.queries()) != 0 {
		t.Fatalf("future or unknown upgrades were scheduled: %+v %+v", result, indexer.queries())
	}
}
