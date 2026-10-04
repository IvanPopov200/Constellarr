package tv_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

func testStore(t *testing.T, defaults tv.Config) (*pgxpool.Pool, *tv.Store) {
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
	for _, path := range migrationFiles(t) {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read migration %s: %v", path, err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("cannot apply migration %s: %v", filepath.Base(path), err)
		}
	}
	store, err := tv.NewStore(ctx, pool, defaults)
	if err != nil {
		t.Fatalf("tv.NewStore: %v", err)
	}
	return pool, store
}

func migrationFiles(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "downloads", "migrations", "*.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("cannot find the shared migrations: %v", err)
	}
	sort.Strings(paths)
	return paths
}

func testConfig(t *testing.T) tv.Config {
	t.Helper()
	return tv.Config{
		RootFolders: []movies.RootFolder{
			{ID: "root-a", Path: filepath.Join(t.TempDir(), "shows")},
			{ID: "root-b", Path: filepath.Join(t.TempDir(), "anime")},
		},
		FolderTemplate: "{title} ({year})",
		FileTemplate:   "{title} S{season}E{episode} {quality}",
		ImportMode:     "copy",
		WriteNFO:       true,
		PollMinutes:    60,
		SearchHours:    6,
		RetryFailed:    true,
	}
}

func cloneConfig(cfg tv.Config) tv.Config {
	cfg.RootFolders = append([]movies.RootFolder{}, cfg.RootFolders...)
	return cfg
}

func saveConfig(t *testing.T, ctx context.Context, store *tv.Store, cfg tv.Config) {
	t.Helper()
	if _, err := store.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
}

func seedProfile(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO movie_profiles (id, name, data) VALUES ($1, $2, '{}'::jsonb)`, id, id); err != nil {
		t.Fatalf("seed quality profile: %v", err)
	}
}

func float64Ptr(value float64) *float64 { return &value }

func intPtr(value int) *int { return &value }

func newSeries(id string) tv.Series {
	return tv.Series{
		ID: id,
		Metadata: metadata.Title{
			IMDbID: "tt1234567", Title: "Example Show", Type: "series", Year: 2020,
			Released: "2020-03-01", Rating: float64Ptr(7.5), Genres: []string{"Drama"},
		},
		Monitored:   true,
		MonitorMode: "all",
		ProfileID:   "profile-a",
		RootID:      "root-a",
		Tags:        []string{"favorite"},
	}
}

func withIMDb(series tv.Series, imdbID string) tv.Series {
	series.Metadata.IMDbID = imdbID
	return series
}

func newEpisode(seriesID string, season, number int) tv.Episode {
	return tv.Episode{
		SeriesID: seriesID, IMDbID: "tt7654321", Title: "Pilot", Season: season, Number: number,
		AirDate: "2020-03-01", Rating: float64Ptr(8),
	}
}

func createSeries(t *testing.T, ctx context.Context, store *tv.Store, series tv.Series) tv.Series {
	t.Helper()
	created, err := store.Create(ctx, series)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return created
}

func upsertEpisode(t *testing.T, ctx context.Context, store *tv.Store, episode tv.Episode) tv.Episode {
	t.Helper()
	stored, err := store.UpsertEpisode(ctx, episode)
	if err != nil {
		t.Fatalf("UpsertEpisode: %v", err)
	}
	return stored
}

func insertDownload(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, releaseID string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb) VALUES ($1, $2, $3, ''::bytea)`,
		id, releaseID, "Release "+id); err != nil {
		t.Fatalf("seed download: %v", err)
	}
}

func TestStoreConfigRoundTripAndRootProtection(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	pool, store := testStore(t, cfg)
	loaded, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Fatalf("seeded config = %+v, want %+v", loaded, cfg)
	}
	saved, err := store.SaveConfig(ctx, cfg)
	if err != nil || !reflect.DeepEqual(saved, cfg) {
		t.Fatalf("SaveConfig = %+v, %v; want %+v", saved, err, cfg)
	}
	episodeTokens := cloneConfig(cfg)
	episodeTokens.FileTemplate = "{title} - {episodeCode} - {episodeTitle} [{quality}]"
	saveConfig(t, ctx, store, episodeTokens)
	originalToken := cloneConfig(cfg)
	originalToken.FileTemplate = "{title} - {original}"
	saveConfig(t, ctx, store, originalToken)
	saveConfig(t, ctx, store, cfg)
	invalid := []struct {
		name   string
		mutate func(*tv.Config)
	}{
		{"relative root", func(c *tv.Config) { c.RootFolders[0].Path = "shows" }},
		{"duplicate root ID", func(c *tv.Config) { c.RootFolders[1].ID = c.RootFolders[0].ID }},
		{"duplicate root path", func(c *tv.Config) { c.RootFolders[1].Path = c.RootFolders[0].Path }},
		{"too many roots", func(c *tv.Config) {
			base := t.TempDir()
			c.RootFolders = make([]movies.RootFolder, 33)
			for i := range c.RootFolders {
				c.RootFolders[i] = movies.RootFolder{ID: fmt.Sprintf("root-%d", i), Path: filepath.Join(base, fmt.Sprint(i))}
			}
		}},
		{"bad import mode", func(c *tv.Config) { c.ImportMode = "sync" }},
		{"poll too small", func(c *tv.Config) { c.PollMinutes = 0 }},
		{"poll too large", func(c *tv.Config) { c.PollMinutes = 1441 }},
		{"search too small", func(c *tv.Config) { c.SearchHours = 0 }},
		{"search too large", func(c *tv.Config) { c.SearchHours = 169 }},
		{"missing token", func(c *tv.Config) { c.FileTemplate = "Show" }},
		{"unknown token", func(c *tv.Config) { c.FileTemplate = "{title} {seriesTitle}" }},
		{"episode title alone", func(c *tv.Config) { c.FileTemplate = "{title} ({year}) {episodeTitle}" }},
		{"no episode identity", func(c *tv.Config) { c.FileTemplate = "{title} [{quality}]" }},
		{"template too long", func(c *tv.Config) { c.FileTemplate = strings.Repeat("a", 513) + "{title}" }},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			next := cloneConfig(cfg)
			test.mutate(&next)
			if _, err := store.SaveConfig(ctx, next); !errors.Is(err, tv.ErrInvalid) {
				t.Fatalf("SaveConfig(%s) error = %v, want ErrInvalid", test.name, err)
			}
		})
	}
	seedProfile(t, ctx, pool, "profile-a")
	createSeries(t, ctx, store, newSeries("series-a"))
	next := cloneConfig(cfg)
	next.RootFolders = next.RootFolders[1:]
	if _, err := store.SaveConfig(ctx, next); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("SaveConfig removing a referenced root error = %v, want ErrConflict", err)
	}
	moved := cloneConfig(cfg)
	moved.RootFolders[0].Path = filepath.Join(t.TempDir(), "moved")
	if _, err := store.SaveConfig(ctx, moved); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("SaveConfig moving a referenced root error = %v, want ErrConflict", err)
	}
	unused := cloneConfig(cfg)
	unused.RootFolders[1].Path = filepath.Join(t.TempDir(), "moved")
	saveConfig(t, ctx, store, unused)
	saveConfig(t, ctx, store, cfg)
	upsertEpisode(t, ctx, store, tv.Episode{
		SeriesID: "series-a", Title: "Pilot", Season: 1, Number: 1,
		Files: []movies.File{{RootID: "root-b", Path: "Show/S01E01.mkv", Size: 1024, ImportedAt: time.Now().UTC()}},
	})
	if _, err := store.SaveConfig(ctx, next); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("SaveConfig removing a root used by episode files error = %v, want ErrConflict", err)
	}
	journalCfg := cloneConfig(cfg)
	journalRoot := movies.RootFolder{ID: "root-c", Path: filepath.Join(t.TempDir(), "journal")}
	journalCfg.RootFolders = append(journalCfg.RootFolders, journalRoot)
	saveConfig(t, ctx, store, journalCfg)
	insertDownload(t, ctx, pool, "job-journal", "release-journal")
	if _, err := pool.Exec(ctx,
		`INSERT INTO download_library_files (job_id, name, root_path, ready) VALUES ('job-journal', 'Show/S01E01.mkv', $1, true)`,
		journalRoot.Path); err != nil {
		t.Fatalf("seed import journal: %v", err)
	}
	removedJournal := cloneConfig(journalCfg)
	removedJournal.RootFolders = removedJournal.RootFolders[:2]
	if _, err := store.SaveConfig(ctx, removedJournal); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("SaveConfig removing a root used by the import journal error = %v, want ErrConflict", err)
	}
	movedJournal := cloneConfig(journalCfg)
	movedJournal.RootFolders[2].Path = filepath.Join(t.TempDir(), "moved-journal")
	if _, err := store.SaveConfig(ctx, movedJournal); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("SaveConfig moving a root used by the import journal error = %v, want ErrConflict", err)
	}
}

func TestStoreSeriesLifecycle(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	pool, store := testStore(t, cfg)
	seedProfile(t, ctx, pool, "profile-a")
	created := createSeries(t, ctx, store, newSeries("series-a"))
	if created.AddedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("Create timestamps = %v, %v; want persisted values", created.AddedAt, created.UpdatedAt)
	}
	var added, updated time.Time
	if err := pool.QueryRow(ctx, `SELECT added_at, updated_at FROM tv_series WHERE id = $1`, "series-a").Scan(&added, &updated); err != nil {
		t.Fatalf("load stored timestamps: %v", err)
	}
	if !created.AddedAt.Equal(added) || !created.UpdatedAt.Equal(updated) {
		t.Fatalf("Create returned %v/%v, stored %v/%v; want persisted timestamps", created.AddedAt, created.UpdatedAt, added, updated)
	}
	if created.UpdatedAt.Nanosecond()%1000 != 0 {
		t.Fatalf("Create returned a pre-write nanosecond timestamp %v", created.UpdatedAt)
	}
	var derived bool
	if err := pool.QueryRow(ctx,
		`SELECT data ? 'episodes' OR data ? 'status' OR data ? 'downloaded' OR data ? 'total' OR data ? 'wanted'
		 FROM tv_series WHERE id = $1`, "series-a").Scan(&derived); err != nil {
		t.Fatalf("inspect stored series: %v", err)
	}
	if derived {
		t.Fatal("stored series data contains derived episodes, status, or counts")
	}
	found, err := store.FindIMDb(ctx, "TT1234567")
	if err != nil || found.ID != "series-a" {
		t.Fatalf("FindIMDb = %+v, %v; want series-a", found, err)
	}
	if _, err := store.FindIMDb(ctx, "not-an-imdb-id"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("FindIMDb invalid error = %v, want ErrNotFound", err)
	}
	duplicate := newSeries("series-b")
	if _, err := store.Create(ctx, duplicate); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("Create duplicate IMDb error = %v, want ErrConflict", err)
	}
	duplicate = newSeries("series-a")
	duplicate.Metadata.IMDbID = ""
	if _, err := store.Create(ctx, duplicate); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("Create duplicate ID error = %v, want ErrConflict", err)
	}
	manual := newSeries("series-manual")
	manual.Metadata.IMDbID = ""
	manual.MonitorMode = ""
	createSeries(t, ctx, store, manual)
	if _, err := store.FindIMDb(ctx, ""); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("FindIMDb empty error = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Get missing error = %v, want ErrNotFound", err)
	}
	list, err := store.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("List = %d series, %v; want 2", len(list), err)
	}
	for _, item := range list {
		if len(item.Episodes) != 0 || item.Status != "" || item.Downloaded != 0 || item.Total != 0 || item.Wanted != 0 {
			t.Fatalf("List returned derived fields for %s: %+v", item.ID, item)
		}
	}
	patched, err := store.Patch(ctx, "series-a", map[string]any{
		"monitored": false, "monitorMode": "future", "tags": []string{"x"},
		"lastRefreshAt": "2024-01-02T03:04:05Z", "error": "refresh failed",
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if patched.Monitored || patched.MonitorMode != "future" || patched.Error != "refresh failed" ||
		patched.LastRefreshAt == nil || !patched.LastRefreshAt.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) ||
		!reflect.DeepEqual(patched.Tags, []string{"x"}) {
		t.Fatalf("Patch = %+v; want updated state", patched)
	}
	if !patched.AddedAt.Equal(created.AddedAt) || patched.UpdatedAt.Before(created.UpdatedAt) {
		t.Fatalf("Patch timestamps = %v/%v, want preserved added and refreshed updated", patched.AddedAt, patched.UpdatedAt)
	}
	if _, err := store.Patch(ctx, "series-a", map[string]any{"status": "wanted"}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Patch derived field error = %v, want ErrInvalid", err)
	}
	if _, err := store.Patch(ctx, "series-a", map[string]any{"episodes": []any{}}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Patch episodes error = %v, want ErrInvalid", err)
	}
	if _, err := store.Patch(ctx, "series-a", map[string]any{"monitored": "yes"}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Patch wrong field type error = %v, want ErrInvalid", err)
	}
	reloaded, err := store.Get(ctx, "series-a")
	if err != nil || reloaded.Monitored || reloaded.MonitorMode != "future" {
		t.Fatalf("Get after rejected patch = %+v, %v; want rolled back state", reloaded, err)
	}
	meta := newSeries("series-a").Metadata
	meta.Title = "Renamed Show"
	patched, err = store.Patch(ctx, "series-a", map[string]any{"metadata": meta})
	if err != nil || patched.Metadata.Title != "Renamed Show" || patched.Metadata.IMDbID != "tt1234567" {
		t.Fatalf("Patch metadata = %+v, %v", patched, err)
	}
	if err := store.Delete(ctx, "series-a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "series-a"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Delete missing error = %v, want ErrNotFound", err)
	}
	if _, err := store.Patch(ctx, "series-a", map[string]any{"monitored": true}); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Patch deleted error = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(ctx, "series-a"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Get deleted error = %v, want ErrNotFound", err)
	}
}

func TestStoreSeriesValidation(t *testing.T) {
	ctx := context.Background()
	pool, store := testStore(t, testConfig(t))
	seedProfile(t, ctx, pool, "profile-a")
	cases := []struct {
		name   string
		mutate func(*tv.Series)
	}{
		{"missing title", func(s *tv.Series) { s.Metadata.Title = "" }},
		{"long title", func(s *tv.Series) { s.Metadata.Title = strings.Repeat("t", 513) }},
		{"bad IMDb", func(s *tv.Series) { s.Metadata.IMDbID = "tt12" }},
		{"movie type", func(s *tv.Series) { s.Metadata.Type = "movie" }},
		{"bad release date", func(s *tv.Series) { s.Metadata.Released = "March 1, 2020" }},
		{"padless release date", func(s *tv.Series) { s.Metadata.Released = "2020-3-1" }},
		{"rating out of range", func(s *tv.Series) { s.Metadata.Rating = float64Ptr(10.5) }},
		{"bad monitor mode", func(s *tv.Series) { s.MonitorMode = "sometimes" }},
		{"bad profile", func(s *tv.Series) { s.ProfileID = "profile-missing" }},
		{"bad root", func(s *tv.Series) { s.RootID = "root-missing" }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			series := newSeries("series-bad")
			test.mutate(&series)
			if _, err := store.Create(ctx, series); !errors.Is(err, tv.ErrInvalid) {
				t.Fatalf("Create(%s) error = %v, want ErrInvalid", test.name, err)
			}
		})
	}
}

func TestStoreEpisodeUpsertPreservesUserState(t *testing.T) {
	ctx := context.Background()
	pool, store := testStore(t, testConfig(t))
	seedProfile(t, ctx, pool, "profile-a")
	createSeries(t, ctx, store, newSeries("series-a"))
	lastSearch := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	file := movies.File{RootID: "root-a", Path: "Show/S01E01.mkv", Size: 2048, Quality: "WEB-1080p", Score: 10, ImportedAt: time.Now().UTC()}
	created := upsertEpisode(t, ctx, store, tv.Episode{
		ID: "ep-1", SeriesID: "series-a", IMDbID: "tt7654321", Title: "Pilot", Season: 1, Number: 1,
		AirDate: "2020-03-01", Rating: float64Ptr(8), Monitored: true, Files: []movies.File{file},
		LastSearchAt: &lastSearch, Error: "old failure",
	})
	refresh := newEpisode("series-a", 1, 1)
	refresh.ID = "ep-ephemeral"
	refresh.IMDbID = "tt9999999"
	refresh.Title = "Pilot (Revised)"
	refresh.AirDate = "2020-03-02"
	refresh.Rating = float64Ptr(9)
	updated := upsertEpisode(t, ctx, store, refresh)
	if updated.ID != "ep-1" {
		t.Fatalf("UpsertEpisode conflict ID = %q, want persisted ep-1", updated.ID)
	}
	if updated.IMDbID != "tt9999999" || updated.Title != "Pilot (Revised)" || updated.AirDate != "2020-03-02" ||
		updated.Rating == nil || *updated.Rating != 9 {
		t.Fatalf("UpsertEpisode metadata = %+v; want refreshed metadata", updated)
	}
	if !updated.Monitored || len(updated.Files) != 1 || updated.Files[0].Path != file.Path ||
		updated.LastSearchAt == nil || !updated.LastSearchAt.Equal(lastSearch) || updated.Error != "old failure" {
		t.Fatalf("UpsertEpisode user state = %+v; want preserved monitored, files, search, and error", updated)
	}
	conflict := created
	conflict.Season = 2
	if _, err := store.UpsertEpisode(ctx, conflict); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("UpsertEpisode ID conflict error = %v, want ErrConflict", err)
	}
	if _, err := store.Episode(ctx, "missing"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Episode missing error = %v, want ErrNotFound", err)
	}
	upsertEpisode(t, ctx, store, newEpisode("series-a", 1, 2))
	episodes, err := store.Episodes(ctx, "series-a")
	if err != nil || len(episodes) != 2 || episodes[0].Number != 1 || episodes[1].Number != 2 {
		t.Fatalf("Episodes = %+v, %v; want two ordered episodes", episodes, err)
	}
	if all, err := store.Episodes(ctx, ""); err != nil || len(all) != 2 {
		t.Fatalf("Episodes(all) = %d, %v; want 2", len(all), err)
	}
	if _, err := store.Episodes(ctx, "missing"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Episodes missing series error = %v, want ErrNotFound", err)
	}
	if _, err := store.Episodes(ctx, "bad id!"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Episodes invalid series error = %v, want ErrNotFound", err)
	}
	if stored, err := store.Episode(ctx, "ep-1"); err != nil || stored.SeriesID != "series-a" {
		t.Fatalf("Episode = %+v, %v", stored, err)
	}
	badFiles := []struct {
		name string
		file movies.File
	}{
		{"zero size", movies.File{Path: "Show/S01E01.mkv", Size: 0}},
		{"absolute path", movies.File{Path: "/Show/S01E01.mkv", Size: 1}},
		{"escaping path", movies.File{Path: "../Show/S01E01.mkv", Size: 1}},
		{"bad root", movies.File{RootID: "root-missing", Path: "Show/S01E01.mkv", Size: 1}},
	}
	for _, test := range badFiles {
		t.Run("file "+test.name, func(t *testing.T) {
			episode := newEpisode("series-a", 2, 1)
			episode.Files = []movies.File{test.file}
			if _, err := store.UpsertEpisode(ctx, episode); !errors.Is(err, tv.ErrInvalid) {
				t.Fatalf("UpsertEpisode(%s) error = %v, want ErrInvalid", test.name, err)
			}
		})
	}
	badEpisodes := []struct {
		name   string
		mutate func(*tv.Episode)
	}{
		{"negative season", func(e *tv.Episode) { e.Season = -1 }},
		{"season too high", func(e *tv.Episode) { e.Season = 101 }},
		{"number zero", func(e *tv.Episode) { e.Number = 0 }},
		{"number too high", func(e *tv.Episode) { e.Number = 1001 }},
		{"bad air date", func(e *tv.Episode) { e.AirDate = "01-02-2020" }},
		{"rating out of range", func(e *tv.Episode) { e.Rating = float64Ptr(-1) }},
		{"bad IMDb", func(e *tv.Episode) { e.IMDbID = "7654321" }},
	}
	for _, test := range badEpisodes {
		t.Run(test.name, func(t *testing.T) {
			episode := newEpisode("series-a", 3, 1)
			test.mutate(&episode)
			if _, err := store.UpsertEpisode(ctx, episode); !errors.Is(err, tv.ErrInvalid) {
				t.Fatalf("UpsertEpisode(%s) error = %v, want ErrInvalid", test.name, err)
			}
		})
	}
	manual := newEpisode("series-a", 4, 1)
	manual.IMDbID = ""
	if stored, err := store.UpsertEpisode(ctx, manual); err != nil || stored.IMDbID != "" {
		t.Fatalf("UpsertEpisode manual = %+v, %v; want empty IMDb", stored, err)
	}
	if _, err := store.UpsertEpisode(ctx, newEpisode("series-missing", 1, 1)); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("UpsertEpisode missing series error = %v, want ErrNotFound", err)
	}
}

func TestStorePatchEpisodeWhitelist(t *testing.T) {
	ctx := context.Background()
	pool, store := testStore(t, testConfig(t))
	seedProfile(t, ctx, pool, "profile-a")
	createSeries(t, ctx, store, newSeries("series-a"))
	upsertEpisode(t, ctx, store, tv.Episode{ID: "ep-1", SeriesID: "series-a", Title: "Pilot", Season: 1, Number: 1})
	patched, err := store.PatchEpisode(ctx, "ep-1", map[string]any{
		"monitored":    true,
		"error":        "search failed",
		"lastSearchAt": "2024-02-03T04:05:06Z",
		"files": []movies.File{
			{RootID: "root-a", Path: "Show/S01E01.mkv", Size: 4096, Quality: "Bluray-1080p", ImportedAt: time.Now().UTC()},
		},
	})
	if err != nil {
		t.Fatalf("PatchEpisode: %v", err)
	}
	if !patched.Monitored || patched.Error != "search failed" || len(patched.Files) != 1 ||
		patched.LastSearchAt == nil || !patched.LastSearchAt.Equal(time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC)) {
		t.Fatalf("PatchEpisode = %+v; want updated user state", patched)
	}
	for _, field := range []string{"metadata", "title", "season", "number", "seriesId", "id", "status"} {
		if _, err := store.PatchEpisode(ctx, "ep-1", map[string]any{field: nil}); !errors.Is(err, tv.ErrInvalid) {
			t.Fatalf("PatchEpisode(%s) error = %v, want ErrInvalid", field, err)
		}
	}
	if _, err := store.PatchEpisode(ctx, "ep-1", map[string]any{"monitored": "yes"}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("PatchEpisode wrong type error = %v, want ErrInvalid", err)
	}
	if _, err := store.PatchEpisode(ctx, "missing", map[string]any{"monitored": true}); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("PatchEpisode missing error = %v, want ErrNotFound", err)
	}
	if _, err := store.PatchEpisode(ctx, "ep-1", map[string]any{"files": []movies.File{{Path: "Show/S01E01.mkv", Size: 0}}}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("PatchEpisode invalid files error = %v, want ErrInvalid", err)
	}
	reloaded, err := store.Episode(ctx, "ep-1")
	if err != nil || !reloaded.Monitored || len(reloaded.Files) != 1 || reloaded.Files[0].Size != 4096 {
		t.Fatalf("Episode after rejected patch = %+v, %v; want rolled back state", reloaded, err)
	}
}

func TestStoreMonitor(t *testing.T) {
	ctx := context.Background()
	pool, store := testStore(t, testConfig(t))
	seedProfile(t, ctx, pool, "profile-a")
	createSeries(t, ctx, store, newSeries("series-a"))
	createSeries(t, ctx, store, withIMDb(newSeries("series-b"), "tt2222222"))
	for _, episode := range []tv.Episode{
		{ID: "ep-1", SeriesID: "series-a", Title: "One", Season: 1, Number: 1},
		{ID: "ep-2", SeriesID: "series-a", Title: "Two", Season: 1, Number: 2},
		{ID: "ep-3", SeriesID: "series-a", Title: "Three", Season: 1, Number: 3},
		{ID: "ep-4", SeriesID: "series-a", Title: "Four", Season: 2, Number: 1},
	} {
		upsertEpisode(t, ctx, store, episode)
	}
	upsertEpisode(t, ctx, store, tv.Episode{ID: "ep-other", SeriesID: "series-b", Title: "Other", Season: 1, Number: 1})
	all, err := store.Monitor(ctx, "series-a", tv.MonitorInput{Monitored: true})
	if err != nil || len(all) != 4 {
		t.Fatalf("Monitor all = %d, %v; want 4", len(all), err)
	}
	for _, episode := range all {
		if !episode.Monitored {
			t.Fatalf("Monitor all left %s unmonitored", episode.ID)
		}
	}
	season, err := store.Monitor(ctx, "series-a", tv.MonitorInput{Season: intPtr(1), Monitored: false})
	if err != nil || len(season) != 3 {
		t.Fatalf("Monitor season = %d, %v; want 3", len(season), err)
	}
	for _, episode := range season {
		if episode.Season != 1 || episode.Monitored {
			t.Fatalf("Monitor season returned %+v", episode)
		}
	}
	subset, err := store.Monitor(ctx, "series-a", tv.MonitorInput{EpisodeIDs: []string{"ep-4"}, Monitored: true})
	if err != nil || len(subset) != 1 || subset[0].ID != "ep-4" || !subset[0].Monitored {
		t.Fatalf("Monitor explicit = %+v, %v; want ep-4 monitored", subset, err)
	}
	if _, err := store.Monitor(ctx, "series-a", tv.MonitorInput{EpisodeIDs: []string{"ep-4", "ep-other"}, Monitored: false}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Monitor foreign episode error = %v, want ErrInvalid", err)
	}
	if _, err := store.Monitor(ctx, "series-a", tv.MonitorInput{EpisodeIDs: []string{"ep-4", "ep-missing"}}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Monitor missing episode error = %v, want ErrInvalid", err)
	}
	unchanged, err := store.Episode(ctx, "ep-4")
	if err != nil || !unchanged.Monitored {
		t.Fatalf("Monitor rejection changed ep-4 = %+v, %v; want monitored true", unchanged, err)
	}
	if _, err := store.Monitor(ctx, "series-a", tv.MonitorInput{Season: intPtr(1), EpisodeIDs: []string{"ep-1"}}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Monitor season plus episodes error = %v, want ErrInvalid", err)
	}
	if _, err := store.Monitor(ctx, "series-a", tv.MonitorInput{Season: intPtr(101)}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Monitor season out of range error = %v, want ErrInvalid", err)
	}
	if empty, err := store.Monitor(ctx, "series-a", tv.MonitorInput{Season: intPtr(99)}); err != nil || len(empty) != 0 {
		t.Fatalf("Monitor empty season = %d, %v; want no episodes", len(empty), err)
	}
	if _, err := store.Monitor(ctx, "missing", tv.MonitorInput{Monitored: true}); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Monitor missing series error = %v, want ErrNotFound", err)
	}
}

func TestStoreAcquisitionsOwnershipAndAdoption(t *testing.T) {
	ctx := context.Background()
	pool, store := testStore(t, testConfig(t))
	seedProfile(t, ctx, pool, "profile-a")
	createSeries(t, ctx, store, newSeries("series-a"))
	createSeries(t, ctx, store, withIMDb(newSeries("series-b"), "tt2222222"))
	upsertEpisode(t, ctx, store, tv.Episode{ID: "ep-a1", SeriesID: "series-a", Title: "One", Season: 1, Number: 1})
	upsertEpisode(t, ctx, store, tv.Episode{ID: "ep-b1", SeriesID: "series-b", Title: "Other", Season: 1, Number: 1})
	for _, job := range []string{"job-1", "job-2", "job-3"} {
		insertDownload(t, ctx, pool, job, "release-"+job)
	}
	acquisition := tv.Acquisition{
		SeriesID: "series-a", EpisodeIDs: []string{"ep-a1"}, JobID: "job-1",
		ReleaseID: "release-job-1", Title: "Example Show S01E01", Status: "grabbed",
	}
	if err := store.SaveAcquisition(ctx, acquisition); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}
	acquisitions, err := store.Acquisitions(ctx)
	if err != nil || len(acquisitions) != 1 {
		t.Fatalf("Acquisitions = %d, %v; want 1", len(acquisitions), err)
	}
	if stored := acquisitions[0]; stored.ReleaseID != "release-job-1" || stored.Title != "Example Show S01E01" ||
		!reflect.DeepEqual(stored.EpisodeIDs, []string{"ep-a1"}) || stored.Status != "grabbed" {
		t.Fatalf("Acquisitions[0] = %+v", stored)
	}
	var adopted bool
	var mediaType string
	if err := pool.QueryRow(ctx, `SELECT movie_adopted, media_type FROM downloads WHERE id = $1`, "job-1").Scan(&adopted, &mediaType); err != nil || !adopted {
		t.Fatalf("movie_adopted after SaveAcquisition = %v, %v; want true", adopted, err)
	}
	if mediaType != "tv" {
		t.Fatalf("media_type after SaveAcquisition = %q, want tv", mediaType)
	}
	acquisition.Status = "downloading"
	if err := store.SaveAcquisition(ctx, acquisition); err != nil {
		t.Fatalf("SaveAcquisition update: %v", err)
	}
	reassigned := acquisition
	reassigned.SeriesID = "series-b"
	reassigned.EpisodeIDs = []string{"ep-b1"}
	if err := store.SaveAcquisition(ctx, reassigned); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("SaveAcquisition reassignment error = %v, want ErrConflict", err)
	}
	foreign := acquisition
	foreign.JobID = "job-3"
	foreign.ReleaseID = "release-job-3"
	foreign.EpisodeIDs = []string{"ep-b1"}
	if err := store.SaveAcquisition(ctx, foreign); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("SaveAcquisition foreign episode error = %v, want ErrInvalid", err)
	}
	empty := acquisition
	empty.JobID = "job-3"
	empty.ReleaseID = "release-job-3"
	empty.EpisodeIDs = nil
	if err := store.SaveAcquisition(ctx, empty); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("SaveAcquisition empty episodes error = %v, want ErrInvalid", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE downloads SET media_type = 'tv' WHERE id = 'job-3'`); err != nil {
		t.Fatalf("mark legacy TV download: %v", err)
	}
	legacyTV := acquisition
	legacyTV.JobID = "job-3"
	legacyTV.ReleaseID = "release-job-3"
	if err := store.SaveAcquisition(ctx, legacyTV); err != nil {
		t.Fatalf("SaveAcquisition legacy TV download: %v", err)
	}
	unknown := acquisition
	unknown.JobID = "job-missing"
	unknown.ReleaseID = "release-missing"
	if err := store.SaveAcquisition(ctx, unknown); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("SaveAcquisition unknown job error = %v, want ErrNotFound", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO movies (id, data) VALUES ('movie-1', '{"title":"Movie"}'::jsonb)`); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO movie_acquisitions (job_id, movie_id, release, status) VALUES ('job-2', 'movie-1', '{}'::jsonb, 'grabbed')`); err != nil {
		t.Fatalf("seed movie acquisition: %v", err)
	}
	movieOwned := acquisition
	movieOwned.JobID = "job-2"
	movieOwned.ReleaseID = "release-job-2"
	if err := store.SaveAcquisition(ctx, movieOwned); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("SaveAcquisition movie-owned job error = %v, want ErrConflict", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM movie_acquisitions WHERE job_id = 'job-2'`); err != nil {
		t.Fatalf("delete movie acquisition: %v", err)
	}
	if err := store.SaveAcquisition(ctx, movieOwned); !errors.Is(err, tv.ErrConflict) {
		t.Fatalf("SaveAcquisition movie-owned job after catalog delete error = %v, want ErrConflict", err)
	}
	if err := store.Delete(ctx, "series-a"); err != nil {
		t.Fatalf("Delete series with acquisition: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tv_acquisitions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("tv_acquisitions after delete = %d, %v; want 0", count, err)
	}
	if err := pool.QueryRow(ctx, `SELECT movie_adopted, media_type FROM downloads WHERE id = $1`, "job-1").Scan(&adopted, &mediaType); err != nil || !adopted || mediaType != "tv" {
		t.Fatalf("download after series delete = adopted %v, media_type %q, %v; want true/tv", adopted, mediaType, err)
	}
}

func TestStoreHistoryAndBlocklist(t *testing.T) {
	ctx := context.Background()
	pool, store := testStore(t, testConfig(t))
	seedProfile(t, ctx, pool, "profile-a")
	createSeries(t, ctx, store, newSeries("series-a"))
	upsertEpisode(t, ctx, store, tv.Episode{ID: "ep-1", SeriesID: "series-a", Title: "Pilot", Season: 1, Number: 1})
	if err := store.Event(ctx, "series-a", "ep-1", "grabbed", "Episode grabbed"); err != nil {
		t.Fatalf("Event: %v", err)
	}
	if err := store.Event(ctx, "series-a", "", "refresh", "Series refreshed"); err != nil {
		t.Fatalf("Event series: %v", err)
	}
	history, err := store.History(ctx, "series-a")
	if err != nil || len(history) != 2 {
		t.Fatalf("History = %d, %v; want 2", len(history), err)
	}
	if history[0].Type != "refresh" || history[0].EpisodeID != "" || history[1].EpisodeID != "ep-1" {
		t.Fatalf("History order = %+v; want newest first with episode IDs", history)
	}
	if all, err := store.History(ctx, ""); err != nil || len(all) != 2 {
		t.Fatalf("History(all) = %d, %v; want 2", len(all), err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tv_history (series_id, episode_id, type, message)
		 SELECT 'series-a', '', 'probe', g::text FROM generate_series(1, 205) g`); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	if capped, err := store.History(ctx, "series-a"); err != nil || len(capped) != 200 {
		t.Fatalf("History cap = %d, %v; want 200", len(capped), err)
	}
	if _, err := store.History(ctx, "missing"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("History missing series error = %v, want ErrNotFound", err)
	}
	if err := store.Event(ctx, "missing", "", "refresh", "x"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Event missing series error = %v, want ErrNotFound", err)
	}
	if err := store.Event(ctx, "series-a", "ep-missing", "grabbed", "x"); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Event foreign episode error = %v, want ErrInvalid", err)
	}
	if err := store.Block(ctx, "series-a", "release-1"); err != nil {
		t.Fatalf("Block: %v", err)
	}
	if err := store.Block(ctx, "series-a", "release-1"); err != nil {
		t.Fatalf("Block repeated: %v", err)
	}
	if blocked, err := store.Blocked(ctx, "series-a", "release-1"); err != nil || !blocked {
		t.Fatalf("Blocked = %v, %v; want true", blocked, err)
	}
	if blocked, err := store.Blocked(ctx, "series-a", "release-2"); err != nil || blocked {
		t.Fatalf("Blocked other = %v, %v; want false", blocked, err)
	}
	if err := store.Unblock(ctx, "series-a", "release-1"); err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	if err := store.Unblock(ctx, "series-a", "release-1"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Unblock missing error = %v, want ErrNotFound", err)
	}
	if err := store.Block(ctx, "missing", "release-1"); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Block missing series error = %v, want ErrNotFound", err)
	}
	if _, err := store.Blocked(ctx, "series-a", ""); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Blocked empty release error = %v, want ErrInvalid", err)
	}
}

func TestStoreTaskDueClaim(t *testing.T) {
	ctx := context.Background()
	_, store := testStore(t, testConfig(t))
	due, err := store.TaskDue(ctx, "rss", time.Hour)
	if err != nil || !due {
		t.Fatalf("TaskDue first = %v, %v; want true", due, err)
	}
	if due, err := store.TaskDue(ctx, "rss", time.Hour); err != nil || due {
		t.Fatalf("TaskDue second = %v, %v; want false", due, err)
	}
	if err := store.MarkTask(ctx, "rss"); err != nil {
		t.Fatalf("MarkTask: %v", err)
	}
	if due, err := store.TaskDue(ctx, "rss", time.Hour); err != nil || due {
		t.Fatalf("TaskDue after MarkTask = %v, %v; want false", due, err)
	}
	var claims int32
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if due, err := store.TaskDue(ctx, "sync", time.Hour); err == nil && due {
				atomic.AddInt32(&claims, 1)
			}
		}()
	}
	wait.Wait()
	if claims != 1 {
		t.Fatalf("concurrent TaskDue claims = %d, want exactly 1", claims)
	}
	if _, err := store.TaskDue(ctx, "bad name", time.Hour); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("TaskDue invalid name error = %v, want ErrInvalid", err)
	}
	if _, err := store.TaskDue(ctx, "rss", 0); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("TaskDue invalid interval error = %v, want ErrInvalid", err)
	}
}

func TestStoreAcquisitionMediaClaimRace(t *testing.T) {
	ctx := context.Background()
	pool, store := testStore(t, testConfig(t))
	seedProfile(t, ctx, pool, "profile-a")
	createSeries(t, ctx, store, newSeries("series-a"))
	upsertEpisode(t, ctx, store, tv.Episode{ID: "ep-a1", SeriesID: "series-a", Title: "One", Season: 1, Number: 1})
	insertDownload(t, ctx, pool, "job-race", "release-race")
	if _, err := pool.Exec(ctx, `INSERT INTO movies (id, data) VALUES ('movie-race', '{"title":"Movie"}'::jsonb)`); err != nil {
		t.Fatalf("seed movie: %v", err)
	}
	movieStore, err := movies.NewStore(ctx, pool, movies.Config{})
	if err != nil {
		t.Fatalf("movies.NewStore: %v", err)
	}
	raceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var wait sync.WaitGroup
	outcomes := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		outcomes <- store.SaveAcquisition(raceCtx, tv.Acquisition{
			SeriesID: "series-a", EpisodeIDs: []string{"ep-a1"}, JobID: "job-race",
			ReleaseID: "release-race", Status: "grabbed",
		})
	}()
	go func() {
		defer wait.Done()
		outcomes <- movieStore.SaveAcquisition(raceCtx, movies.Acquisition{
			MovieID: "movie-race", JobID: "job-race", ReleaseID: "release-race", Status: "grabbed",
		})
	}()
	wait.Wait()
	close(outcomes)
	var succeeded, conflicted int
	for err := range outcomes {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, tv.ErrConflict), errors.Is(err, movies.ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected media claim error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("media claim race = %d claimed, %d conflicted; want exactly one of each", succeeded, conflicted)
	}
	var mediaType string
	var tvRows, movieRows int
	if err := pool.QueryRow(ctx,
		`SELECT d.media_type,
		        (SELECT count(*) FROM tv_acquisitions WHERE job_id = d.id),
		        (SELECT count(*) FROM movie_acquisitions WHERE job_id = d.id)
		 FROM downloads d WHERE d.id = 'job-race'`).Scan(&mediaType, &tvRows, &movieRows); err != nil {
		t.Fatalf("inspect media claim: %v", err)
	}
	if tvRows+movieRows != 1 || (tvRows == 1) != (mediaType == "tv") || (movieRows == 1) != (mediaType == "movie") {
		t.Fatalf("media claim = type %q, tv %d, movie %d; want one matching claim", mediaType, tvRows, movieRows)
	}
}

func TestStoreEditAtomicSeriesAndMonitors(t *testing.T) {
	ctx := context.Background()
	pool, store := testStore(t, testConfig(t))
	seedProfile(t, ctx, pool, "profile-a")
	createSeries(t, ctx, store, newSeries("series-a"))
	createSeries(t, ctx, store, withIMDb(newSeries("series-b"), "tt2222222"))
	file := movies.File{RootID: "root-a", Path: "Show/S01E01.mkv", Size: 2048, Quality: "WEB-1080p", ImportedAt: time.Now().UTC()}
	upsertEpisode(t, ctx, store, tv.Episode{
		ID: "ep-a1", SeriesID: "series-a", IMDbID: "tt7654321", Title: "One", Season: 1, Number: 1,
		AirDate: "2020-03-01", Rating: float64Ptr(8), Monitored: true, Files: []movies.File{file},
	})
	upsertEpisode(t, ctx, store, tv.Episode{ID: "ep-a2", SeriesID: "series-a", Title: "Two", Season: 1, Number: 2})
	upsertEpisode(t, ctx, store, tv.Episode{ID: "ep-b1", SeriesID: "series-b", Title: "Other", Season: 1, Number: 1, Monitored: true})
	if _, err := store.Patch(ctx, "series-b", map[string]any{"monitorMode": "none", "monitored": false}); err != nil {
		t.Fatalf("Patch series-b: %v", err)
	}
	state := func() (tv.Series, tv.Series, tv.Episode, tv.Episode, tv.Episode) {
		t.Helper()
		seriesA, err := store.Get(ctx, "series-a")
		if err != nil {
			t.Fatalf("Get series-a: %v", err)
		}
		seriesB, err := store.Get(ctx, "series-b")
		if err != nil {
			t.Fatalf("Get series-b: %v", err)
		}
		episodeA1, err := store.Episode(ctx, "ep-a1")
		if err != nil {
			t.Fatalf("Episode ep-a1: %v", err)
		}
		episodeA2, err := store.Episode(ctx, "ep-a2")
		if err != nil {
			t.Fatalf("Episode ep-a2: %v", err)
		}
		episodeB1, err := store.Episode(ctx, "ep-b1")
		if err != nil {
			t.Fatalf("Episode ep-b1: %v", err)
		}
		return seriesA, seriesB, episodeA1, episodeA2, episodeB1
	}
	assertUntouched := func(t *testing.T) {
		t.Helper()
		seriesA, seriesB, episodeA1, episodeA2, episodeB1 := state()
		if seriesA.MonitorMode != "all" || !seriesA.Monitored || seriesB.MonitorMode != "none" || seriesB.Monitored {
			t.Fatalf("series state changed: %+v %+v", seriesA, seriesB)
		}
		if !episodeA1.Monitored || episodeA2.Monitored || !episodeB1.Monitored {
			t.Fatalf("monitor flags changed: %+v %+v %+v", episodeA1, episodeA2, episodeB1)
		}
		if len(episodeA1.Files) != 1 || episodeA1.Files[0].Path != file.Path || episodeA1.Title != "One" {
			t.Fatalf("episode files or metadata changed: %+v", episodeA1)
		}
	}
	if _, err := store.Edit(ctx, map[string]map[string]any{
		"series-a": {"monitorMode": "missing"},
		"series-b": {"profileId": "profile-missing"},
	}, map[string]bool{"ep-a1": false}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Edit invalid profile error = %v, want ErrInvalid", err)
	}
	assertUntouched(t)
	if _, err := store.Edit(ctx, map[string]map[string]any{
		"series-a":       {"monitorMode": "latest"},
		"series-missing": {},
	}, nil); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Edit missing series error = %v, want ErrNotFound", err)
	}
	assertUntouched(t)
	if _, err := store.Edit(ctx, map[string]map[string]any{"series-a": {}}, map[string]bool{"ep-b1": false}); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Edit foreign episode error = %v, want ErrInvalid", err)
	}
	assertUntouched(t)
	if _, err := store.Edit(ctx, map[string]map[string]any{"series-a": {"status": "wanted"}}, nil); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Edit derived field error = %v, want ErrInvalid", err)
	}
	if _, err := store.Edit(ctx, map[string]map[string]any{"series-a": {}}, map[string]bool{"ep-missing": true}); !errors.Is(err, tv.ErrNotFound) {
		t.Fatalf("Edit missing episode error = %v, want ErrNotFound", err)
	}
	assertUntouched(t)
	manySeries := make(map[string]map[string]any, 501)
	for i := range 501 {
		manySeries[fmt.Sprintf("series-%d", i)] = nil
	}
	if _, err := store.Edit(ctx, manySeries, nil); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Edit too many series error = %v, want ErrInvalid", err)
	}
	manyEpisodes := make(map[string]bool, 50001)
	for i := range 50001 {
		manyEpisodes[fmt.Sprintf("ep-%d", i)] = true
	}
	if _, err := store.Edit(ctx, nil, manyEpisodes); !errors.Is(err, tv.ErrInvalid) {
		t.Fatalf("Edit too many episodes error = %v, want ErrInvalid", err)
	}
	edited, err := store.Edit(ctx, map[string]map[string]any{
		"series-a": {"monitorMode": "future", "monitored": false},
		"series-b": {"monitorMode": "all", "monitored": true},
	}, map[string]bool{"ep-a1": false, "ep-a2": true, "ep-b1": false})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if len(edited) != 2 || edited[0].ID != "series-a" || edited[1].ID != "series-b" {
		t.Fatalf("Edit returned %+v; want persisted series-a and series-b", edited)
	}
	if edited[0].MonitorMode != "future" || edited[0].Monitored || edited[1].MonitorMode != "all" || !edited[1].Monitored {
		t.Fatalf("Edit returned modes %+v / %+v; want persisted modes", edited[0], edited[1])
	}
	if len(edited[0].Episodes) != 0 || edited[0].Status != "" || edited[0].Total != 0 {
		t.Fatalf("Edit returned derived fields: %+v", edited[0])
	}
	seriesA, seriesB, episodeA1, episodeA2, episodeB1 := state()
	if seriesA.MonitorMode != "future" || seriesA.Monitored || seriesB.MonitorMode != "all" || !seriesB.Monitored {
		t.Fatalf("persisted series modes = %+v / %+v", seriesA, seriesB)
	}
	if episodeA1.Monitored || !episodeA2.Monitored || episodeB1.Monitored {
		t.Fatalf("persisted monitor flags = %+v %+v %+v", episodeA1, episodeA2, episodeB1)
	}
	if len(episodeA1.Files) != 1 || episodeA1.Files[0].Path != file.Path || episodeA1.Files[0].Size != file.Size ||
		episodeA1.Title != "One" || episodeA1.IMDbID != "tt7654321" || episodeA1.AirDate != "2020-03-01" ||
		episodeA1.Rating == nil || *episodeA1.Rating != 8 {
		t.Fatalf("successful edit lost episode state: %+v", episodeA1)
	}
	refresh := episodeA1
	refresh.Title = "One (Revised)"
	refresh.Monitored = true
	refreshed, err := store.UpsertEpisode(ctx, refresh)
	if err != nil {
		t.Fatalf("UpsertEpisode after Edit: %v", err)
	}
	if refreshed.Monitored || refreshed.Title != "One (Revised)" || len(refreshed.Files) != 1 || refreshed.Files[0].Path != file.Path {
		t.Fatalf("metadata refresh after Edit = %+v; want new metadata, preserved monitor and files", refreshed)
	}
	if _, err := store.Edit(ctx, map[string]map[string]any{"series-b": nil}, map[string]bool{"ep-b1": true}); err != nil {
		t.Fatalf("Edit monitors-only: %v", err)
	}
	_, seriesB, _, _, episodeB1 = state()
	if seriesB.MonitorMode != "all" || !episodeB1.Monitored {
		t.Fatalf("monitors-only edit = mode %q, monitored %v; want all/true", seriesB.MonitorMode, episodeB1.Monitored)
	}
	if empty, err := store.Edit(ctx, nil, nil); err != nil || len(empty) != 0 {
		t.Fatalf("Edit no-op = %d, %v; want empty", len(empty), err)
	}
}
