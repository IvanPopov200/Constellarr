package tv_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// tvTestSchema creates an isolated schema so TV tests never touch real tables.
func tvTestSchema(t *testing.T) *pgxpool.Pool {
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
	schema := "tv_test_" + strings.ToLower(rand.Text())
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

type tvTestEnv struct {
	service     *tv.Service
	movie       *movies.Service
	manager     *downloads.Manager
	pool        *pgxpool.Pool
	directory   string
	metadataURL string
	metadataKey string
}

// tvNewTestEnv wires the real download manager, the shared movie store, and the TV service.
func tvNewTestEnv(t *testing.T, indexerURL, metadataURL string) *tvTestEnv {
	t.Helper()
	for _, name := range []string{"OMDB_URL", "OMDB_API_KEY", "JELLYFIN_URL", "JELLYFIN_API_KEY", "IMPORT_WEBHOOK_URL"} {
		t.Setenv(name, "")
	}
	pool := tvTestSchema(t)
	directory := t.TempDir()
	cfg := downloads.Config{Directory: directory}
	if indexerURL != "" {
		cfg.IndexerURL = indexerURL
		cfg.APIKey = "synthetic-key"
	}
	ctx := context.Background()
	manager, err := downloads.New(ctx, pool, cfg)
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	movieService, err := movies.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	env := &tvTestEnv{
		service: nil, movie: movieService, manager: manager, pool: pool,
		directory: directory, metadataURL: metadataURL, metadataKey: "synthetic-key",
	}
	env.saveMovieConfig(t, movies.Config{
		RootFolders:         []movies.RootFolder{{ID: "movies", Path: filepath.Join(directory, "library", "movies")}},
		FolderTemplate:      "{title} ({year}) [imdb-{imdbId}]",
		FileTemplate:        "{title} ({year}) [{quality}]",
		ImportMode:          library.ModeLink,
		WriteNFO:            true,
		PollMinutes:         15,
		SearchHours:         6,
		MinimumAvailability: "released",
		RetryFailed:         true,
		MetadataURL:         metadataURL,
		MetadataAPIKey:      env.metadataKey,
	})
	service, err := tv.New(ctx, pool, manager, movieService.Store)
	if err != nil {
		t.Fatalf("tv.New: %v", err)
	}
	env.service = service
	env.saveTVConfig(t, tv.Config{
		RootFolders:    []movies.RootFolder{{ID: "tv", Path: filepath.Join(directory, "library", "tv")}},
		FolderTemplate: "{title} ({year}) [imdb-{imdbId}]/Season {season}",
		FileTemplate:   "{title} - {episodeCode} - {episodeTitle} [{quality}]",
		ImportMode:     library.ModeLink,
		WriteNFO:       true,
		PollMinutes:    15,
		SearchHours:    6,
		RetryFailed:    true,
	})
	return env
}

func (e *tvTestEnv) saveTVConfig(t *testing.T, cfg tv.Config) tv.Config {
	t.Helper()
	saved, err := e.service.SetConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	return saved
}

func (e *tvTestEnv) saveMovieConfig(t *testing.T, cfg movies.Config) {
	t.Helper()
	if _, err := e.movie.SetConfig(context.Background(), cfg); err != nil {
		t.Fatalf("movies SetConfig: %v", err)
	}
}

func (e *tvTestEnv) movieConfig(t *testing.T) movies.Config {
	t.Helper()
	cfg, err := e.movie.Store.Config(context.Background())
	if err != nil {
		t.Fatalf("movies Store.Config: %v", err)
	}
	return cfg
}

func (e *tvTestEnv) rootPath() string {
	return filepath.Join(e.directory, "library", "tv")
}

func (e *tvTestEnv) writeEpisodeFile(t *testing.T, rel, content string) movies.File {
	t.Helper()
	target := filepath.Join(e.rootPath(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create library directory: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write library file: %v", err)
	}
	return movies.File{
		RootID: "tv", Path: rel, Size: int64(len(content)),
		Quality: "WEB-1080p", ImportedAt: time.Now().UTC(),
	}
}

func (e *tvTestEnv) setEpisodeFiles(t *testing.T, episodeID string, files []movies.File) {
	t.Helper()
	if _, err := e.service.Store.PatchEpisode(context.Background(), episodeID, map[string]any{"files": files}); err != nil {
		t.Fatalf("PatchEpisode files: %v", err)
	}
}

func (e *tvTestEnv) episode(t *testing.T, seriesID string, season, number int) tv.Episode {
	t.Helper()
	episodes, err := e.service.Store.Episodes(context.Background(), seriesID)
	if err != nil {
		t.Fatalf("Episodes: %v", err)
	}
	return tvEpisodeByNumber(t, episodes, season, number)
}

type tvOMDbEpisode struct {
	number  int
	title   string
	airDate string
	imdb    string
	rating  string
}

type tvOMDbSeries struct {
	id             string
	title          string
	year           int
	total          int
	seasons        map[int][]tvOMDbEpisode
	missingSeasons map[int]bool
}

// tvOMDbFixtures is mutable so tests can simulate a season appearing after a not-found refresh.
type tvOMDbFixtures struct {
	mu          sync.Mutex
	series      map[string]tvOMDbSeries
	seasonCalls map[string]int
}

func tvNewOMDbFixtures(series map[string]tvOMDbSeries) *tvOMDbFixtures {
	return &tvOMDbFixtures{series: series, seasonCalls: map[string]int{}}
}

func (f *tvOMDbFixtures) set(id string, fixture tvOMDbSeries) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.series[id] = fixture
}

func (f *tvOMDbFixtures) seasonCallsFor(id string, season int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seasonCalls[id+":"+strconv.Itoa(season)]
}

// tvOMDbServer serves a fixed fixture set; stateful tests use tvOMDbServerWith.
func tvOMDbServer(t *testing.T, series map[string]tvOMDbSeries) *httptest.Server {
	return tvOMDbServerWith(t, tvNewOMDbFixtures(series))
}

// tvOMDbServerWith serves the OMDb search, series lookup, and season payload shapes.
func tvOMDbServerWith(t *testing.T, fixtures *tvOMDbFixtures) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("apikey") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		if text := strings.ToLower(r.URL.Query().Get("s")); text != "" {
			fixtures.mu.Lock()
			ids := make([]string, 0, len(fixtures.series))
			for id := range fixtures.series {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			results := []map[string]string{}
			for _, id := range ids {
				fixture := fixtures.series[id]
				if !strings.Contains(strings.ToLower(fixture.title), text) {
					continue
				}
				results = append(results, map[string]string{
					"Title": fixture.title, "Year": strconv.Itoa(fixture.year), "Type": "series", "imdbID": id,
				})
			}
			fixtures.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"Search": results, "Response": "True"})
			return
		}
		id := strings.ToLower(r.URL.Query().Get("i"))
		raw := r.URL.Query().Get("Season")
		seasonNumber := 0
		if raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				http.Error(w, "bad season", http.StatusBadRequest)
				return
			}
			seasonNumber = parsed
		}
		fixtures.mu.Lock()
		fixture, ok := fixtures.series[id]
		if ok && raw != "" {
			fixtures.seasonCalls[id+":"+raw]++
			if fixture.missingSeasons[seasonNumber] {
				fixtures.mu.Unlock()
				_, _ = io.WriteString(w, `{"Response":"False","Error":"Series or episode not found!"}`)
				return
			}
		}
		fixtures.mu.Unlock()
		if !ok {
			_, _ = io.WriteString(w, `{"Response":"False","Error":"Series not found!"}`)
			return
		}
		if raw != "" {
			episodes := make([]map[string]string, 0, len(fixture.seasons))
			for _, episode := range fixture.seasons[seasonNumber] {
				payload := map[string]string{
					"Title": episode.title, "Released": episode.airDate,
					"Episode": strconv.Itoa(episode.number), "imdbID": episode.imdb,
				}
				if episode.rating != "" {
					payload["imdbRating"] = episode.rating
				}
				episodes = append(episodes, payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Title": fixture.title, "Season": raw, "Episodes": episodes, "Response": "True",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"Title": fixture.title, "Year": strconv.Itoa(fixture.year), "Type": "series",
			"imdbID": fixture.id, "totalSeasons": strconv.Itoa(fixture.total), "Response": "True",
		})
	}))
	t.Cleanup(server.Close)
	return server
}

type tvTestRelease struct {
	title   string
	guid    string
	size    int64
	imdb    string
	season  int
	episode int
}

// tvIndexerServer serves newznab searches keyed by title, the TV RSS feed, and NZB downloads.
func tvIndexerServer(t *testing.T, search map[string][]tvTestRelease, rss []tvTestRelease) *httptest.Server {
	t.Helper()
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	writeFeed := func(w http.ResponseWriter, releases []tvTestRelease) {
		var body strings.Builder
		body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>`)
		for _, item := range releases {
			fmt.Fprintf(&body, `<item><title>%s</title><guid isPermaLink="false">%s</guid><pubDate>Fri, 02 Jan 2026 10:00:00 +0000</pubDate><enclosure url="https://example.invalid/%s.nzb" length="%d"/>`,
				escape(item.title), escape(item.guid), escape(item.guid), item.size)
			if item.size > 0 {
				fmt.Fprintf(&body, `<newznab:attr name="size" value="%d"/>`, item.size)
			}
			if item.imdb != "" {
				fmt.Fprintf(&body, `<newznab:attr name="imdb" value="%s"/>`, escape(item.imdb))
			}
			if item.season > 0 {
				fmt.Fprintf(&body, `<newznab:attr name="season" value="%d"/>`, item.season)
			}
			if item.episode > 0 {
				fmt.Fprintf(&body, `<newznab:attr name="episode" value="%d"/>`, item.episode)
			}
			body.WriteString(`</item>`)
		}
		body.WriteString(`</channel></rss>`)
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, body.String())
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("t") {
		case "tvsearch":
			writeFeed(w, search[r.URL.Query().Get("q")])
		case "search":
			writeFeed(w, rss)
		case "get":
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = io.WriteString(w, `<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><file subject="[1/1] - &quot;episode.mkv&quot; yEnc (1/1)" date="1136214245" poster="poster &lt;poster@example.com&gt;"><groups><group>alt.binaries.test</group></groups><segments><segment bytes="1024" number="1">synthetic-part@example.com</segment></segments></file></nzb>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func tvEpisodeByNumber(t *testing.T, episodes []tv.Episode, season, number int) tv.Episode {
	t.Helper()
	for _, episode := range episodes {
		if episode.Season == season && episode.Number == number {
			return episode
		}
	}
	t.Fatalf("episode S%02dE%02d is missing from the catalog", season, number)
	return tv.Episode{}
}

func tvReleaseByID(t *testing.T, releases []tv.Release, id string) tv.Release {
	t.Helper()
	for _, release := range releases {
		if release.ID == id {
			return release
		}
	}
	t.Fatalf("release %q is missing from the results", id)
	return tv.Release{}
}

func tvReasonContains(release tv.Release, want string) bool {
	for _, reason := range release.Decision.Reasons {
		if strings.Contains(reason, want) {
			return true
		}
	}
	return false
}

func TestAddRefreshPreservesEpisodeMonitoring(t *testing.T) {
	ctx := context.Background()
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt1111111": {id: "tt1111111", title: "Pilot Show", year: 2011, total: 2, seasons: map[int][]tvOMDbEpisode{
			1: {
				{number: 1, title: "Pilot", airDate: "2011-01-01", imdb: "tt2000001"},
				{number: 2, title: "Second", airDate: "2011-01-08", imdb: "tt2000002"},
			},
			2: {
				{number: 1, title: "Return", airDate: "2012-01-01", imdb: "tt2000003"},
				{number: 2, title: "Finale", airDate: "2012-01-08", imdb: "tt2000004"},
			},
		}},
	})
	env := tvNewTestEnv(t, "", omdb.URL)
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt1111111", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if series.Metadata.Title != "Pilot Show" || series.Metadata.TotalSeasons != 2 || series.MonitorMode != "all" {
		t.Fatalf("Add returned %+v, want the looked-up series", series.Metadata)
	}
	if series.Error != "Episode metadata is still loading" || series.LastRefreshAt != nil {
		t.Fatalf("Add = error %q lastRefresh %v, want a partial catalog", series.Error, series.LastRefreshAt)
	}
	loaded, err := env.service.Get(ctx, series.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Episodes) != 2 {
		t.Fatalf("Add loaded %d episodes, want the bounded first season only", len(loaded.Episodes))
	}
	second := tvEpisodeByNumber(t, loaded.Episodes, 1, 2)
	if !second.Monitored || second.Status != "wanted" {
		t.Fatalf("S01E02 = monitored %v status %q, want wanted", second.Monitored, second.Status)
	}
	if _, err := env.service.Monitor(ctx, series.ID, tv.MonitorInput{EpisodeIDs: []string{second.ID}, Monitored: false}); err != nil {
		t.Fatalf("Monitor: %v", err)
	}
	refreshed, err := env.service.Refresh(ctx, series.ID)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.Error != "" || refreshed.LastRefreshAt == nil {
		t.Fatalf("Refresh = error %q lastRefresh %v, want a complete catalog", refreshed.Error, refreshed.LastRefreshAt)
	}
	if len(refreshed.Episodes) != 4 {
		t.Fatalf("Refresh loaded %d episodes, want all known seasons", len(refreshed.Episodes))
	}
	kept := tvEpisodeByNumber(t, refreshed.Episodes, 1, 2)
	if kept.Monitored {
		t.Fatal("Refresh cleared the user's episode monitoring change")
	}
	if newEpisode := tvEpisodeByNumber(t, refreshed.Episodes, 2, 1); !newEpisode.Monitored || newEpisode.Status != "wanted" {
		t.Fatalf("S02E01 = monitored %v status %q, want wanted under the all mode", newEpisode.Monitored, newEpisode.Status)
	}
	list, err := env.service.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d series, want 1", len(list))
	}
	if list[0].Episodes != nil {
		t.Fatal("List must not include episode details")
	}
	if list[0].Total != 4 || list[0].Wanted != 3 || list[0].Status != "wanted" {
		t.Fatalf("List summary = total %d wanted %d status %q, want 4/3/wanted", list[0].Total, list[0].Wanted, list[0].Status)
	}
}

func TestAddAppliesMonitorModes(t *testing.T) {
	ctx := context.Background()
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt2222222": {id: "tt2222222", title: "Future Show", year: 2020, total: 1, seasons: map[int][]tvOMDbEpisode{
			1: {
				{number: 1, title: "Aired", airDate: "2000-01-01", imdb: "tt2000011"},
				{number: 2, title: "Upcoming", airDate: "2999-01-01", imdb: "tt2000012"},
				{number: 3, title: "Unknown", airDate: "", imdb: "tt2000013"},
			},
		}},
	})
	env := tvNewTestEnv(t, "", omdb.URL)
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt2222222", Monitored: true, MonitorMode: "future"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if series.Error != "" || series.LastRefreshAt == nil {
		t.Fatalf("Add = error %q lastRefresh %v, want a complete single-season catalog", series.Error, series.LastRefreshAt)
	}
	if !tvEpisodeByNumber(t, series.Episodes, 1, 2).Monitored {
		t.Fatal("future mode must monitor an upcoming episode")
	}
	for _, number := range []int{1, 3} {
		if tvEpisodeByNumber(t, series.Episodes, 1, number).Monitored {
			t.Fatalf("future mode must not monitor episode %d", number)
		}
	}
	// An explicit mode change re-applies the selection to existing episodes.
	updated, err := env.service.Update(ctx, series.ID, tv.Series{Monitored: true, MonitorMode: "all", ProfileID: series.ProfileID, RootID: series.RootID, Tags: series.Tags})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	for _, episode := range updated.Episodes {
		if !episode.Monitored {
			t.Fatalf("all mode must monitor S%02dE%02d", episode.Season, episode.Number)
		}
	}
	calendar, err := env.service.Calendar(ctx)
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if len(calendar) != 1 || calendar[0].SeriesTitle != "Future Show" || calendar[0].Number != 2 {
		t.Fatalf("Calendar = %+v, want the upcoming episode", calendar)
	}
}

func TestSearchRequiresStrictReleaseIdentity(t *testing.T) {
	ctx := context.Background()
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt1111111": {id: "tt1111111", title: "Pilot Show", year: 2011, total: 1, seasons: map[int][]tvOMDbEpisode{
			1: {
				{number: 1, title: "Pilot", airDate: "2011-01-01", imdb: "tt2000001"},
				{number: 2, title: "Second", airDate: "2011-01-08", imdb: "tt2000002"},
				{number: 3, title: "Third", airDate: "2011-01-15", imdb: "tt2000003"},
			},
		}},
	})
	indexer := tvIndexerServer(t, map[string][]tvTestRelease{
		"Pilot Show": {
			{title: "Pilot.Show.S01E02.1080p.WEB-DL", guid: "r-valid", size: 2 << 30, imdb: "tt1111111"},
			{title: "Pilot Show 2011 S01E02 1080p WEB", guid: "r-ttyear", size: 2 << 30},
			{title: "Pilot.Show.S01E02.1080p", guid: "r-wrongimdb", size: 2 << 30, imdb: "tt9999999"},
			{title: "Pilot.Showing.S01E02.1080p", guid: "r-substring", size: 2 << 30},
			{title: "Pilot.Show.2012.S01E02.1080p", guid: "r-wrongyear", size: 2 << 30},
			{title: "Pilot.Show.S02E02.1080p", guid: "r-wrongseason", size: 2 << 30},
		},
	}, nil)
	env := tvNewTestEnv(t, indexer.URL, omdb.URL)
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt1111111", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	target := tv.Target{Season: 1, Episode: 2}
	releases, err := env.service.Search(ctx, series.ID, target)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) != 6 {
		t.Fatalf("Search returned %d releases, want all six fixtures", len(releases))
	}
	second := tvEpisodeByNumber(t, series.Episodes, 1, 2).ID
	valid := tvReleaseByID(t, releases, "r-valid")
	if !valid.Decision.Allowed || len(valid.EpisodeIDs) != 1 || valid.EpisodeIDs[0] != second || valid.Pack {
		t.Fatalf("valid release = %+v, want S01E02 only", valid)
	}
	if !tvReleaseByID(t, releases, "r-ttyear").Decision.Allowed {
		t.Fatal("a title and year match must be accepted without an IMDb attribute")
	}
	checks := []struct {
		id     string
		reason string
	}{
		{"r-wrongimdb", "different series"},
		{"r-substring", "title does not match"},
		{"r-wrongyear", "year does not match"},
		{"r-wrongseason", "no catalog episodes"},
	}
	for _, check := range checks {
		release := tvReleaseByID(t, releases, check.id)
		if release.Decision.Allowed || !tvReasonContains(release, check.reason) {
			t.Fatalf("release %s = allowed %v reasons %v, want rejection %q", check.id, release.Decision.Allowed, release.Decision.Reasons, check.reason)
		}
		if len(release.EpisodeIDs) != 0 {
			t.Fatalf("release %s mapped %v, want no covered episodes", check.id, release.EpisodeIDs)
		}
	}
	// Override never bypasses series or target identity.
	if _, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: target, ReleaseID: "r-wrongimdb", Override: true}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Grab wrong series = %v, want ErrInvalid even with override", err)
	}
	if _, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: target, ReleaseID: "r-substring", Override: true}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Grab substring title = %v, want ErrInvalid even with override", err)
	}
	job, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: target, ReleaseID: "r-valid"})
	if err != nil {
		t.Fatalf("Grab valid: %v", err)
	}
	if job.ReleaseID != "r-valid" {
		t.Fatalf("Grab job = %+v, want the valid release", job)
	}
	acquisitions, err := env.service.Store.Acquisitions(ctx)
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	if len(acquisitions) != 1 || acquisitions[0].JobID != job.ID || len(acquisitions[0].EpisodeIDs) != 1 || acquisitions[0].EpisodeIDs[0] != second {
		t.Fatalf("acquisition = %+v, want the grabbed episode only", acquisitions)
	}
}

func TestGrabRejectsOverlappingDownloads(t *testing.T) {
	ctx := context.Background()
	episodes := map[int][]tvOMDbEpisode{1: {}}
	for number := 1; number <= 5; number++ {
		episodes[1] = append(episodes[1], tvOMDbEpisode{
			number: number, title: fmt.Sprintf("Episode %d", number),
			airDate: fmt.Sprintf("2020-01-%02d", number), imdb: fmt.Sprintf("tt300%04d", number),
		})
	}
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt3333333": {id: "tt3333333", title: "Overlap Show", year: 2020, total: 1, seasons: episodes},
	})
	indexer := tvIndexerServer(t, map[string][]tvTestRelease{
		"Overlap Show": {
			{title: "Overlap.Show.S01E01.1080p.WEB-DL", guid: "r-e01", size: 2 << 30, imdb: "tt3333333"},
			{title: "Overlap.Show.S01E02.1080p.WEB-DL", guid: "r-e02", size: 2 << 30, imdb: "tt3333333"},
			{title: "Overlap.Show.S01.1080p.WEB-DL", guid: "r-pack", size: 10 << 30, imdb: "tt3333333"},
		},
	}, nil)
	env := tvNewTestEnv(t, indexer.URL, omdb.URL)
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt3333333", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	first := tvEpisodeByNumber(t, series.Episodes, 1, 1)
	packSearch, err := env.service.Search(ctx, series.ID, tv.Target{Season: 1})
	if err != nil {
		t.Fatalf("Search pack: %v", err)
	}
	if pack := tvReleaseByID(t, packSearch, "r-pack"); !pack.Pack || !pack.Decision.Allowed || len(pack.EpisodeIDs) != 5 {
		t.Fatalf("pack = %+v, want all five covered season episodes", pack)
	}
	firstJob, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: tv.Target{Season: 1, Episode: 1}, ReleaseID: "r-e01"})
	if err != nil {
		t.Fatalf("Grab E01: %v", err)
	}
	var adopted bool
	if err := env.pool.QueryRow(ctx, `SELECT movie_adopted FROM downloads WHERE id = $1`, firstJob.ID).Scan(&adopted); err != nil {
		t.Fatalf("read adoption: %v", err)
	}
	if !adopted {
		t.Fatal("a TV acquisition must claim its download so movie automation leaves it alone")
	}
	releases, err := env.service.Search(ctx, series.ID, tv.Target{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if release := tvReleaseByID(t, releases, "r-e01"); release.Decision.Allowed || !tvReasonContains(release, "already downloading") {
		t.Fatalf("active release decision = %+v, want an already downloading reason", release.Decision)
	}
	if _, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: tv.Target{Season: 1, Episode: 1}, ReleaseID: "r-e01"}); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("duplicate grab = %v, want ErrConflict", err)
	}
	// Override relaxes quality and blocklist, never an overlapping active download.
	if _, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: tv.Target{Season: 1}, ReleaseID: "r-pack", Override: true}); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("overlapping pack grab = %v, want ErrConflict even with override", err)
	}
	secondJob, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: tv.Target{Season: 1, Episode: 2}, ReleaseID: "r-e02"})
	if err != nil {
		t.Fatalf("Grab E02: %v", err)
	}
	if secondJob.ID == firstJob.ID {
		t.Fatal("non-overlapping episodes must use separate downloads")
	}
	releases, err = env.service.Search(ctx, series.ID, tv.Target{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("Search after grabs: %v", err)
	}
	if release := tvReleaseByID(t, releases, "r-pack"); release.Decision.Allowed || !tvReasonContains(release, "already in progress") {
		t.Fatalf("pack decision = %+v, want an overlap reason", release.Decision)
	}
	// A failed acquisition retries only with an explicit override and is superseded by the retry.
	if _, err := env.pool.Exec(ctx, `UPDATE downloads SET status = 'failed' WHERE id = $1`, firstJob.ID); err != nil {
		t.Fatalf("fail job: %v", err)
	}
	if err := env.service.Store.SaveAcquisition(ctx, tv.Acquisition{
		SeriesID: series.ID, EpisodeIDs: []string{first.ID}, JobID: firstJob.ID,
		ReleaseID: "r-e01", Title: "Overlap.Show.S01E01.1080p.WEB-DL", Status: "failed", Error: "download failed",
	}); err != nil {
		t.Fatalf("mark acquisition failed: %v", err)
	}
	if _, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: tv.Target{Season: 1, Episode: 1}, ReleaseID: "r-e01"}); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("failed release retry without override = %v, want ErrConflict", err)
	}
	retried, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: tv.Target{Season: 1, Episode: 1}, ReleaseID: "r-e01", Override: true})
	if err != nil {
		t.Fatalf("override retry: %v", err)
	}
	if retried.Status != "queued" || retried.ID != firstJob.ID {
		t.Fatalf("override retry = %+v, want the same job requeued", retried)
	}
	history, err := env.service.Store.History(ctx, series.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	grabbed := 0
	for _, event := range history {
		if event.Type == "grabbed" {
			grabbed++
		}
	}
	if grabbed < 3 {
		t.Fatalf("history has %d grab events, want every grab recorded", grabbed)
	}
}

func TestSharedMetadataProfileAndConfigSafety(t *testing.T) {
	ctx := context.Background()
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt1111111": {id: "tt1111111", title: "Pilot Show", year: 2011, total: 1, seasons: map[int][]tvOMDbEpisode{
			1: {{number: 1, title: "Pilot", airDate: "2011-01-01", imdb: "tt2000001"}},
		}},
	})
	env := tvNewTestEnv(t, "", omdb.URL)
	titles, err := env.service.Discover(ctx, "pilot", 1)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(titles) != 1 || titles[0].IMDbID != "tt1111111" {
		t.Fatalf("Discover = %+v, want the shared OMDb settings to be used", titles)
	}
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt1111111", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	profiles, err := env.movie.Store.Profiles(ctx)
	if err != nil || len(profiles) == 0 {
		t.Fatalf("shared profiles: %v (%d)", err, len(profiles))
	}
	if series.ProfileID != profiles[0].ID {
		t.Fatalf("Add chose profile %q, want the first shared profile %q", series.ProfileID, profiles[0].ID)
	}
	cfg, err := env.service.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.FolderTemplate == "" || cfg.FileTemplate == "" || len(cfg.RootFolders) != 1 {
		t.Fatalf("Config = %+v, want the saved TV naming settings", cfg)
	}
	// A root path change is refused while the series still owns it.
	changed := cfg
	changed.RootFolders = []movies.RootFolder{{ID: cfg.RootFolders[0].ID, Path: filepath.Join(env.directory, "moved")}}
	if _, err := env.service.SetConfig(ctx, changed); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("root change = %v, want ErrConflict", err)
	}
	// Validation runs before any root directory is created.
	invalid := cfg
	invalid.RootFolders = []movies.RootFolder{{ID: "new-root", Path: filepath.Join(env.directory, "invalid-root")}}
	invalid.ImportMode = "teleport"
	if _, err := env.service.SetConfig(ctx, invalid); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("invalid config = %v, want ErrInvalid", err)
	}
	if _, err := os.Stat(filepath.Join(env.directory, "invalid-root")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("an invalid configuration must not create root folders")
	}
	// Offline manual series keep working without the provider; IMDb-only adds report it.
	shared := env.movieConfig(t)
	shared.MetadataURL = ""
	env.saveMovieConfig(t, shared)
	if _, err := env.service.Discover(ctx, "pilot", 1); !errors.Is(err, downloads.ErrNotConfigured) {
		t.Fatalf("Discover without a key = %v, want ErrNotConfigured", err)
	}
	if _, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt4444444"}); !errors.Is(err, downloads.ErrNotConfigured) {
		t.Fatalf("IMDb-only add without a key = %v, want ErrNotConfigured", err)
	}
	offline, err := env.service.Add(ctx, tv.AddInput{Metadata: metadata.Title{Title: "Offline Show", Year: 2021, Type: "series"}})
	if err != nil {
		t.Fatalf("offline Add: %v", err)
	}
	if offline.Metadata.Title != "Offline Show" || offline.Metadata.Type != "series" || offline.LastRefreshAt != nil {
		t.Fatalf("offline series = %+v, want manual metadata without provider state", offline)
	}
}

func tvStringPtr(value string) *string { return &value }

func tvBoolPtr(value bool) *bool { return &value }

func TestUpdateAndBulkEditAtomically(t *testing.T) {
	ctx := context.Background()
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt1111111": {id: "tt1111111", title: "Pilot Show", year: 2011, total: 1, seasons: map[int][]tvOMDbEpisode{
			1: {
				{number: 1, title: "Pilot", airDate: "2011-01-01", imdb: "tt2000001"},
				{number: 2, title: "Second", airDate: "2011-01-08", imdb: "tt2000002"},
				{number: 3, title: "Third", airDate: "2011-01-15", imdb: "tt2000003"},
			},
		}},
		"tt2222222": {id: "tt2222222", title: "Other Show", year: 2012, total: 1, seasons: map[int][]tvOMDbEpisode{
			1: {{number: 1, title: "Pilot", airDate: "2012-01-01", imdb: "tt2000011"}},
		}},
	})
	env := tvNewTestEnv(t, "", omdb.URL)
	first, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt1111111", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add first: %v", err)
	}
	second, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt2222222", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add second: %v", err)
	}
	episode2 := tvEpisodeByNumber(t, first.Episodes, 1, 2)
	if _, err := env.service.Monitor(ctx, first.ID, tv.MonitorInput{EpisodeIDs: []string{episode2.ID}, Monitored: false}); err != nil {
		t.Fatalf("Monitor: %v", err)
	}
	// An unrelated edit must not reapply the existing mode or clobber the manual flag.
	updated, err := env.service.Update(ctx, first.ID, tv.Series{
		Monitored: true, MonitorMode: "all", ProfileID: first.ProfileID, RootID: first.RootID, Tags: []string{"favorite"},
	})
	if err != nil {
		t.Fatalf("Update unrelated: %v", err)
	}
	if tvEpisodeByNumber(t, updated.Episodes, 1, 2).Monitored {
		t.Fatal("an unrelated update reapplied the monitor mode")
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "favorite" {
		t.Fatalf("tags = %v, want the edited tags", updated.Tags)
	}
	// An explicit mode change persists the mode and recomputes the flags atomically.
	updated, err = env.service.Update(ctx, first.ID, tv.Series{
		Monitored: true, MonitorMode: "none", ProfileID: first.ProfileID, RootID: first.RootID, Tags: updated.Tags,
	})
	if err != nil {
		t.Fatalf("Update mode: %v", err)
	}
	if updated.MonitorMode != "none" {
		t.Fatalf("monitor mode = %q, want none", updated.MonitorMode)
	}
	for _, episode := range updated.Episodes {
		if episode.Monitored {
			t.Fatalf("none mode left S%02dE%02d monitored", episode.Season, episode.Number)
		}
	}
	bulk, err := env.service.Bulk(ctx, tv.BulkInput{
		IDs: []string{first.ID, second.ID}, Monitored: tvBoolPtr(true), MonitorMode: tvStringPtr("all"),
	})
	if err != nil {
		t.Fatalf("Bulk: %v", err)
	}
	if len(bulk) != 2 || bulk[0].ID != first.ID || bulk[1].ID != second.ID {
		t.Fatalf("bulk order = %+v, want the requested order", bulk)
	}
	for _, series := range bulk {
		if series.MonitorMode != "all" {
			t.Fatalf("bulk monitor mode = %q, want all", series.MonitorMode)
		}
		if len(series.Episodes) != 0 {
			t.Fatal("bulk must return summaries without episodes")
		}
	}
	firstAgain, err := env.service.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	for _, episode := range firstAgain.Episodes {
		if !episode.Monitored {
			t.Fatalf("bulk all mode left S%02dE%02d unmonitored", episode.Season, episode.Number)
		}
	}
	// A failing bulk writes nothing, so no partial edit survives.
	if _, err := env.service.Bulk(ctx, tv.BulkInput{IDs: []string{first.ID, "missing-series"}, Tags: &[]string{"changed"}}); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("bulk with a missing series = %v, want ErrNotFound", err)
	}
	restored, err := env.service.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get after failed bulk: %v", err)
	}
	if len(restored.Tags) != 1 || restored.Tags[0] != "favorite" {
		t.Fatalf("failed bulk changed tags to %v", restored.Tags)
	}
	if _, err := env.service.Bulk(ctx, tv.BulkInput{IDs: []string{first.ID}}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("empty bulk = %v, want ErrInvalid", err)
	}
}

func TestReleaseIdentityAndMultiEpisodeSizing(t *testing.T) {
	ctx := context.Background()
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt1111111": {id: "tt1111111", title: "Pilot Show", year: 2011, total: 1, seasons: map[int][]tvOMDbEpisode{
			1: {
				{number: 1, title: "Pilot", airDate: "2011-01-01", imdb: "tt2000001"},
				{number: 2, title: "Second", airDate: "2011-01-08", imdb: "tt2000002"},
				{number: 3, title: "Third", airDate: "2011-01-15", imdb: "tt2000003"},
			},
		}},
	})
	indexer := tvIndexerServer(t, map[string][]tvTestRelease{
		"Pilot Show": {
			{title: "S01E02 1080p WEB-DL", guid: "r-notitle", size: 2 << 30},
			{title: "Pilot.Show.S01E01E02.1080p.WEB-DL", guid: "r-multi", size: 10 << 30, imdb: "tt1111111"},
			{title: "Pilot.Show.S01E03.1080p.WEB-DL", guid: "r-huge", size: 11 << 30, imdb: "tt1111111"},
		},
	}, nil)
	env := tvNewTestEnv(t, indexer.URL, omdb.URL)
	profiles, err := env.movie.Store.Profiles(ctx)
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	profile := profiles[0]
	profile.MaxMB = 6000
	if _, err := env.movie.Store.SaveProfile(ctx, profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt1111111", Monitored: true, MonitorMode: "all", ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	first := tvEpisodeByNumber(t, series.Episodes, 1, 1)
	second := tvEpisodeByNumber(t, series.Episodes, 1, 2)
	releases, err := env.service.Search(ctx, series.ID, tv.Target{Season: 1, Episode: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	multi := tvReleaseByID(t, releases, "r-multi")
	if !multi.Decision.Allowed || multi.Pack || len(multi.EpisodeIDs) != 2 {
		t.Fatalf("multi-episode release = allowed %v pack %v ids %v, want one per-episode-sized release", multi.Decision.Allowed, multi.Pack, multi.EpisodeIDs)
	}
	if multi.EpisodeIDs[0] != first.ID || multi.EpisodeIDs[1] != second.ID {
		t.Fatalf("multi-episode ids = %v, want S01E01 and S01E02", multi.EpisodeIDs)
	}
	if release := tvReleaseByID(t, releases, "r-notitle"); release.Decision.Allowed || !tvReasonContains(release, "no series title") {
		t.Fatalf("title-less release = %+v, want a no series title rejection", release.Decision)
	}
	if release := tvReleaseByID(t, releases, "r-huge"); release.Decision.Allowed || !tvReasonContains(release, "above the maximum") {
		t.Fatalf("single-episode release = %+v, want the whole size checked against the limit", release.Decision)
	}
	// The multi-episode file does not cover a different requested episode.
	releases, err = env.service.Search(ctx, series.ID, tv.Target{Season: 1, Episode: 3})
	if err != nil {
		t.Fatalf("Search E03: %v", err)
	}
	if release := tvReleaseByID(t, releases, "r-multi"); release.Decision.Allowed || !tvReasonContains(release, "does not match the requested episode") {
		t.Fatalf("multi-episode release for E03 = %+v, want a target rejection", release.Decision)
	}
}

func TestSearchWholeSeriesTarget(t *testing.T) {
	ctx := context.Background()
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt3333333": {id: "tt3333333", title: "Big Show", year: 2015, total: 2, seasons: map[int][]tvOMDbEpisode{
			1: {{number: 1, title: "Start", airDate: "2015-01-01", imdb: "tt3000001"}},
			2: {{number: 1, title: "Return", airDate: "2016-01-01", imdb: "tt3000002"}},
		}},
	})
	indexer := tvIndexerServer(t, map[string][]tvTestRelease{
		"Big Show": {
			{title: "Big.Show.S01E01.1080p.WEB-DL", guid: "r-s1", size: 2 << 30, imdb: "tt3333333"},
			{title: "Big.Show.S02E01.1080p.WEB-DL", guid: "r-s2", size: 2 << 30, imdb: "tt3333333"},
		},
	}, nil)
	env := tvNewTestEnv(t, indexer.URL, omdb.URL)
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt3333333", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := env.service.Refresh(ctx, series.ID); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	first := tvEpisodeByNumber(t, series.Episodes, 1, 1)
	releases, err := env.service.Search(ctx, series.ID, tv.Target{Season: -1, Episode: 0})
	if err != nil {
		t.Fatalf("whole series search: %v", err)
	}
	seasonOne := tvReleaseByID(t, releases, "r-s1")
	seasonTwo := tvReleaseByID(t, releases, "r-s2")
	if !seasonOne.Decision.Allowed || len(seasonOne.EpisodeIDs) != 1 || seasonOne.EpisodeIDs[0] != first.ID {
		t.Fatalf("season one release = %+v, want the known episode across seasons", seasonOne)
	}
	if !seasonTwo.Decision.Allowed || len(seasonTwo.EpisodeIDs) != 1 {
		t.Fatalf("season two release = %+v, want the known episode across seasons", seasonTwo)
	}
	if _, err := env.service.Search(ctx, series.ID, tv.Target{Season: -1, Episode: 1}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("whole series with an episode = %v, want ErrInvalid", err)
	}
	job, err := env.service.Grab(ctx, series.ID, tv.GrabInput{Target: tv.Target{Season: -1}, ReleaseID: "r-s2"})
	if err != nil {
		t.Fatalf("whole series grab: %v", err)
	}
	if job.ReleaseID != "r-s2" {
		t.Fatalf("grab job = %+v, want the season two release", job)
	}
}

func TestUnmonitoredSeriesWantedAndCalendar(t *testing.T) {
	ctx := context.Background()
	omdb := tvOMDbServer(t, map[string]tvOMDbSeries{
		"tt4444444": {id: "tt4444444", title: "Firefly", year: 2002, total: 1, seasons: map[int][]tvOMDbEpisode{
			1: {
				{number: 1, title: "Serenity", airDate: "2002-01-01", imdb: "tt4000001"},
				{number: 2, title: "The Train Job", airDate: "2002-01-02", imdb: "tt4000002"},
				{number: 3, title: "Bushwhacked", airDate: "2002-01-03", imdb: "tt4000003"},
				{number: 4, title: "Upcoming", airDate: "2999-01-01", imdb: "tt4000004"},
				{number: 5, title: "Unknown", airDate: "", imdb: "tt4000005"},
			},
		}},
	})
	env := tvNewTestEnv(t, "", omdb.URL)
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt4444444", Monitored: false, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	list, err := env.service.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d series, want 1", len(list))
	}
	if list[0].Wanted != 0 || list[0].Status != "unmonitored" {
		t.Fatalf("unmonitored series = wanted %d status %q, want 0 and unmonitored", list[0].Wanted, list[0].Status)
	}
	loaded, err := env.service.Get(ctx, series.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.Wanted != 0 {
		t.Fatalf("unmonitored detail wanted = %d, want 0", loaded.Wanted)
	}
	for _, episode := range loaded.Episodes {
		if episode.Status != "unmonitored" {
			t.Fatalf("S%02dE%02d status = %q, want unmonitored", episode.Season, episode.Number, episode.Status)
		}
	}
	calendar, err := env.service.Calendar(ctx)
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if len(calendar) != 1 || calendar[0].Number != 4 || calendar[0].SeriesIMDbID != "tt4444444" {
		t.Fatalf("calendar = %+v, want the upcoming episode with its series IMDb ID", calendar)
	}
	// Monitoring the series counts aired and unknown-date missing episodes, never future ones.
	updated, err := env.service.Update(ctx, series.ID, tv.Series{
		Monitored: true, MonitorMode: "all", ProfileID: series.ProfileID, RootID: series.RootID, Tags: series.Tags,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Wanted != 4 || updated.Status != "wanted" {
		t.Fatalf("monitored series = wanted %d status %q, want 4 and wanted", updated.Wanted, updated.Status)
	}
	if future := tvEpisodeByNumber(t, updated.Episodes, 1, 4); future.Status != "missing" {
		t.Fatalf("future episode status = %q, want missing", future.Status)
	}
	if unknown := tvEpisodeByNumber(t, updated.Episodes, 1, 5); unknown.Status != "wanted" {
		t.Fatalf("unknown-date episode status = %q, want wanted", unknown.Status)
	}
}

func TestNeededNeverAutoUpgradesFutureOrUnknown(t *testing.T) {
	profile := quality.Profile{
		ID: "hd", Name: "HD", Qualities: []string{"Bluray-1080p", "WEB-1080p"},
		Cutoff: "Bluray-1080p", Upgrade: true,
	}
	upgrade := movies.File{RootID: "tv", Path: "Show/Season 01/ep.mkv", Size: 1 << 30, Quality: "WEB-1080p"}
	atCutoff := upgrade
	atCutoff.Quality = "Bluray-1080p"
	cases := []struct {
		name    string
		episode tv.Episode
		want    bool
	}{
		{"aired missing", tv.Episode{Monitored: true, AirDate: "2020-01-01"}, true},
		{"future missing", tv.Episode{Monitored: true, AirDate: "2999-01-01"}, false},
		{"unknown missing", tv.Episode{Monitored: true}, false},
		{"aired upgrade", tv.Episode{Monitored: true, AirDate: "2020-01-01", Files: []movies.File{upgrade}}, true},
		{"future upgrade", tv.Episode{Monitored: true, AirDate: "2999-01-01", Files: []movies.File{upgrade}}, false},
		{"unknown upgrade", tv.Episode{Monitored: true, Files: []movies.File{upgrade}}, false},
		{"aired at cutoff", tv.Episode{Monitored: true, AirDate: "2020-01-01", Files: []movies.File{atCutoff}}, false},
		{"unmonitored missing", tv.Episode{Monitored: false, AirDate: "2020-01-01"}, false},
	}
	for _, test := range cases {
		if got := tv.Needed(profile, test.episode); got != test.want {
			t.Fatalf("Needed(%s) = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestRefreshRetriesNotFoundSeason(t *testing.T) {
	ctx := context.Background()
	fixtures := tvNewOMDbFixtures(map[string]tvOMDbSeries{
		"tt5555555": {id: "tt5555555", title: "Slow Show", year: 2020, total: 2, missingSeasons: map[int]bool{2: true},
			seasons: map[int][]tvOMDbEpisode{
				1: {{number: 1, title: "Pilot", airDate: "2020-01-01", imdb: "tt5000001"}},
				2: {{number: 1, title: "Return", airDate: "2021-01-01", imdb: "tt5000002"}},
			}},
	})
	omdb := tvOMDbServerWith(t, fixtures)
	env := tvNewTestEnv(t, "", omdb.URL)
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt5555555", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if series.Error != "Episode metadata is still loading" {
		t.Fatalf("Add error = %q, want the pending season hint", series.Error)
	}
	// The first refresh sees a definitive not-found and records a retry instead of a load.
	if _, err := env.service.Refresh(ctx, series.ID); err != nil {
		t.Fatalf("Refresh missing season: %v", err)
	}
	if calls := fixtures.seasonCallsFor("tt5555555", 2); calls != 1 {
		t.Fatalf("season 2 calls = %d, want 1", calls)
	}
	missName := "season-miss-" + series.ID + "-2"
	loadedName := "season-" + series.ID + "-2"
	var missCount, loadedCount int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM tv_automation WHERE name = $1`, missName).Scan(&missCount); err != nil {
		t.Fatalf("miss marker: %v", err)
	}
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM tv_automation WHERE name = $1`, loadedName).Scan(&loadedCount); err != nil {
		t.Fatalf("loaded marker: %v", err)
	}
	if missCount != 1 || loadedCount != 0 {
		t.Fatalf("markers = miss %d loaded %d, want a retry marker only", missCount, loadedCount)
	}
	// A fresh retry marker keeps the next refresh from hammering the provider.
	if _, err := env.service.Refresh(ctx, series.ID); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if calls := fixtures.seasonCallsFor("tt5555555", 2); calls != 1 {
		t.Fatalf("season 2 calls after a fresh miss = %d, want no hammering", calls)
	}
	pending, err := env.service.Get(ctx, series.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pending.Error == "" || pending.LastRefreshAt != nil {
		t.Fatalf("pending catalog = error %q lastRefresh %v, want still loading", pending.Error, pending.LastRefreshAt)
	}
	// When the provider publishes the season, the daily window attempts it again and completes.
	fixtures.set("tt5555555", tvOMDbSeries{id: "tt5555555", title: "Slow Show", year: 2020, total: 2,
		seasons: map[int][]tvOMDbEpisode{
			1: {{number: 1, title: "Pilot", airDate: "2020-01-01", imdb: "tt5000001"}},
			2: {{number: 1, title: "Return", airDate: "2021-01-01", imdb: "tt5000002"}},
		}})
	if _, err := env.pool.Exec(ctx, `UPDATE tv_automation SET last_run = now() - interval '25 hours' WHERE name = $1`, missName); err != nil {
		t.Fatalf("age retry marker: %v", err)
	}
	refreshed, err := env.service.Refresh(ctx, series.ID)
	if err != nil {
		t.Fatalf("refresh after publish: %v", err)
	}
	if refreshed.Error != "" || refreshed.LastRefreshAt == nil || len(refreshed.Episodes) != 2 {
		t.Fatalf("complete catalog = error %q lastRefresh %v episodes %d, want a finished refresh", refreshed.Error, refreshed.LastRefreshAt, len(refreshed.Episodes))
	}
}

func TestRemoveArchivesOwnedFilesAndClearsJournal(t *testing.T) {
	ctx := context.Background()
	env := tvNewTestEnv(t, "", "")
	series, err := env.service.Add(ctx, tv.AddInput{
		Metadata: metadata.Title{Title: "Offline Show", Year: 2020, Type: "series"}, Monitored: true, MonitorMode: "all",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	episode, err := env.service.AddEpisode(ctx, series.ID, tv.Episode{Title: "Pilot", Season: 1, Number: 1, AirDate: "2020-01-01"})
	if err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}
	seriesDir := "Offline Show (2020)"
	videoRel := seriesDir + "/Season 01/Offline Show - S01E01 - Pilot [WEB-1080p].mkv"
	episodeNFORel := strings.TrimSuffix(videoRel, ".mkv") + ".nfo"
	showNFORel := seriesDir + "/tvshow.nfo"
	unrelatedRel := seriesDir + "/Season 01/readme.txt"
	videoFile := env.writeEpisodeFile(t, videoRel, "video-bytes")
	env.writeEpisodeFile(t, episodeNFORel, "episode-nfo")
	env.writeEpisodeFile(t, showNFORel, "show-nfo")
	env.writeEpisodeFile(t, unrelatedRel, "keep me")
	env.setEpisodeFiles(t, episode.ID, []movies.File{videoFile})
	tvTestJournal(t, env, "job-remove", "rel-remove", videoRel, int64(len("video-bytes")))
	if err := env.service.Remove(ctx, series.ID, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := env.service.Store.Get(ctx, series.ID); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("series after remove = %v, want ErrNotFound", err)
	}
	for _, rel := range []string{videoRel, episodeNFORel, showNFORel} {
		if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists after Remove(deleteFiles)", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(unrelatedRel))); err != nil {
		t.Fatalf("unrelated file was touched: %v", err)
	}
	var journal int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM download_library_files WHERE job_id = 'job-remove'`).Scan(&journal); err != nil {
		t.Fatalf("journal: %v", err)
	}
	if journal != 0 {
		t.Fatalf("journal rows = %d, want the stale download links cleared", journal)
	}
	archived := map[string]bool{}
	if err := filepath.WalkDir(env.rootPath(), func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			archived[entry.Name()] = true
		}
		return nil
	}); err != nil {
		t.Fatalf("walk library: %v", err)
	}
	for _, name := range []string{filepath.Base(videoRel), filepath.Base(episodeNFORel), "tvshow.nfo"} {
		if !archived[name] {
			t.Fatalf("%s was not archived into the recycle folder", name)
		}
	}
	// deleteFiles=false keeps the files and their journal rows.
	keptSeries, err := env.service.Add(ctx, tv.AddInput{
		Metadata: metadata.Title{Title: "Keep Show", Year: 2021, Type: "series"}, Monitored: true, MonitorMode: "all",
	})
	if err != nil {
		t.Fatalf("Add kept: %v", err)
	}
	keptEpisode, err := env.service.AddEpisode(ctx, keptSeries.ID, tv.Episode{Title: "Pilot", Season: 1, Number: 1, AirDate: "2021-01-01"})
	if err != nil {
		t.Fatalf("AddEpisode kept: %v", err)
	}
	keptRel := "Keep Show (2021)/Season 01/Keep Show - S01E01 - Pilot [WEB-1080p].mkv"
	keptFile := env.writeEpisodeFile(t, keptRel, "kept-bytes")
	env.setEpisodeFiles(t, keptEpisode.ID, []movies.File{keptFile})
	tvTestJournal(t, env, "job-keep", "rel-keep", keptRel, int64(len("kept-bytes")))
	if err := env.service.Remove(ctx, keptSeries.ID, false); err != nil {
		t.Fatalf("Remove kept: %v", err)
	}
	if _, err := env.service.Store.Get(ctx, keptSeries.ID); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("kept series = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(keptRel))); err != nil {
		t.Fatalf("preserved file was removed: %v", err)
	}
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM download_library_files WHERE job_id = 'job-keep'`).Scan(&journal); err != nil {
		t.Fatalf("kept journal: %v", err)
	}
	if journal != 1 {
		t.Fatalf("kept journal rows = %d, want the preserved file's link", journal)
	}
}

func tvTestJournal(t *testing.T, env *tvTestEnv, jobID, releaseID, path string, size int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb) VALUES ($1, $2, 'Offline Show', ''::bytea)`, jobID, releaseID); err != nil {
		t.Fatalf("insert download: %v", err)
	}
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO download_library_files (job_id, name, root_path, path, size, sha256, ready) VALUES ($1, 'source.mkv', $2, $3, $4, '', true)`,
		jobID, env.rootPath(), path, size); err != nil {
		t.Fatalf("insert journal row: %v", err)
	}
}

func TestTVMutationsCannotRaceMetadataRefresh(t *testing.T) {
	ctx := context.Background()
	env := tvNewTestEnv(t, "", "")
	series, err := env.service.Add(ctx, tv.AddInput{Metadata: metadata.Title{IMDbID: "tt1234567", Title: "Fixture Series", Year: 2020}, Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("Season") != "" {
			fmt.Fprint(w, `{"Title":"Fixture Series","Season":"1","seriesID":"tt1234567","Episodes":[{"Title":"Pilot","Episode":"1","Released":"2020-01-02","imdbID":"tt1234568","imdbRating":"8.2"}],"Response":"True"}`)
			return
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"Title":"Fixture Series","Year":"2020","imdbID":"tt1234567","Type":"series","totalSeasons":"1","Response":"True"}`)
	}))
	defer provider.Close()
	defer releaseOnce.Do(func() { close(release) })
	cfg := env.movieConfig(t)
	cfg.MetadataURL, cfg.MetadataAPIKey = provider.URL, "fixture-key"
	env.saveMovieConfig(t, cfg)
	finished := make(chan error, 1)
	go func() { _, err := env.service.Refresh(ctx, series.ID); finished <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("metadata refresh did not start")
	}
	checks := []func() error{
		func() error {
			_, err := env.service.Bulk(ctx, tv.BulkInput{IDs: []string{series.ID}, MonitorMode: tvStringPtr("future")})
			return err
		},
		func() error {
			_, err := env.service.Monitor(ctx, series.ID, tv.MonitorInput{Monitored: false})
			return err
		},
		func() error {
			_, err := env.service.AddEpisode(ctx, series.ID, tv.Episode{Season: 1, Number: 2, Title: "Concurrent episode"})
			return err
		},
		func() error { _, err := env.service.Update(ctx, series.ID, tv.Series{Monitored: false}); return err },
	}
	for _, check := range checks {
		if err := check(); !errors.Is(err, tv.ErrConflict) {
			t.Fatalf("concurrent edit = %v, want conflict", err)
		}
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	saved, err := env.service.Get(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Monitored || saved.MonitorMode != "all" || len(saved.Episodes) != 1 || !saved.Episodes[0].Monitored {
		t.Fatalf("refresh and rejected edits disagreed: %+v", saved)
	}
}

func TestRefreshRevisitsOldSeasonsWithoutStarvation(t *testing.T) {
	ctx := context.Background()
	fixture := tvOMDbSeries{id: "tt1111111", title: "Long Series", year: 2020, total: 8, seasons: map[int][]tvOMDbEpisode{}}
	for season := 1; season <= 8; season++ {
		fixture.seasons[season] = []tvOMDbEpisode{{number: 1, title: "Old title", airDate: "2020-01-01", imdb: fmt.Sprintf("tt200000%d", season)}}
	}
	provider := tvOMDbServer(t, map[string]tvOMDbSeries{"tt1111111": fixture})
	env := tvNewTestEnv(t, "", provider.URL)
	series, err := env.service.Add(ctx, tv.AddInput{IMDbID: "tt1111111", Monitored: true, MonitorMode: "all"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := env.service.Refresh(ctx, series.ID); err != nil {
			t.Fatal(err)
		}
	}
	for season := 1; season <= 8; season++ {
		fixture.seasons[season][0].title = "Updated title"
		if _, err := env.pool.Exec(ctx, `UPDATE tv_automation SET last_run=now()-($2::int * interval '1 day') WHERE name=$1`, fmt.Sprintf("season-%s-%d", series.ID, season), season+1); err != nil {
			t.Fatal(err)
		}
	}
	refreshed, err := env.service.Refresh(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tvEpisodeByNumber(t, refreshed.Episodes, 8, 1).Title != "Updated title" || tvEpisodeByNumber(t, refreshed.Episodes, 1, 1).Title != "Old title" {
		t.Fatal("refresh did not prioritize the oldest seasons within its batch limit")
	}
	refreshed, err = env.service.Refresh(ctx, series.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, episode := range refreshed.Episodes {
		if episode.Title != "Updated title" {
			t.Fatalf("season %d never refreshed", episode.Season)
		}
	}
}
