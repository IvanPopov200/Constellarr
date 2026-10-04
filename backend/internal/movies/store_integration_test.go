package movies_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

func testStore(t *testing.T, defaults movies.Config) (*pgxpool.Pool, *movies.Store) {
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
	schema := "movies_test_" + strings.ToLower(rand.Text())
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
	store, err := movies.NewStore(ctx, pool, defaults)
	if err != nil {
		t.Fatalf("movies.NewStore: %v", err)
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

func testConfig(t *testing.T) movies.Config {
	t.Helper()
	return movies.Config{
		RootFolders: []movies.RootFolder{
			{ID: "root-a", Path: filepath.Join(t.TempDir(), "movies")},
			{ID: "root-b", Path: filepath.Join(t.TempDir(), "shows")},
		},
		FolderTemplate:      "{title} ({year})",
		FileTemplate:        "{title} ({year}) {quality}",
		ImportMode:          "copy",
		WriteNFO:            true,
		PollMinutes:         60,
		SearchHours:         6,
		MinimumAvailability: "released",
		RetryFailed:         true,
		MetadataURL:         "https://metadata.example/api",
		MetadataAPIKey:      "synthetic-metadata-key",
		MetadataConfigured:  true,
		JellyfinURL:         "https://jellyfin.example",
		JellyfinAPIKey:      "synthetic-jellyfin-key",
		JellyfinConfigured:  true,
		WebhookURL:          "https://hooks.example/constellarr",
	}
}

func cloneConfig(cfg movies.Config) movies.Config {
	cfg.RootFolders = append([]movies.RootFolder{}, cfg.RootFolders...)
	return cfg
}

func float64Ptr(value float64) *float64 { return &value }

func testProfile(t *testing.T, name string) quality.Profile {
	t.Helper()
	candidates := []quality.Profile{
		{Name: name, Qualities: []string{"Bluray-1080p", "WEB-1080p"}, Cutoff: "Bluray-1080p", Upgrade: true, Language: "en"},
		{Name: name, Qualities: append([]string(nil), quality.Qualities...)},
	}
	if len(quality.Qualities) > 0 {
		candidates = append(candidates, quality.Profile{Name: name, Qualities: []string{quality.Qualities[0]}})
	}
	for _, candidate := range candidates {
		if quality.Validate(candidate) == nil {
			return candidate
		}
	}
	t.Skip("quality.Validate rejected every probe profile")
	return quality.Profile{}
}

func saveTestProfile(t *testing.T, ctx context.Context, store *movies.Store, name string) quality.Profile {
	t.Helper()
	profile, err := store.SaveProfile(ctx, testProfile(t, name))
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	return profile
}

func saveTestMovie(t *testing.T, ctx context.Context, store *movies.Store, profileID, rootID, imdbID, title string) movies.Movie {
	t.Helper()
	movie, err := store.Save(ctx, movies.Movie{
		Metadata: metadata.Title{
			IMDbID: imdbID, Title: title, Year: 1999, Released: "1999-03-31",
			Rating: float64Ptr(8.7), Votes: 100, Runtime: 136,
			Genres: []string{"Drama"}, Languages: []string{"English"},
			Directors: []string{"Director"}, Cast: []string{"Actor"}, Countries: []string{"United States"},
		},
		Monitored: true, ProfileID: profileID, RootID: rootID, Status: "wanted",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return movie
}

func insertDownload(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb) VALUES ($1, $2, $3, $4)`,
		id, "release-"+id, "Synthetic "+id, []byte("<nzb/>")); err != nil {
		t.Fatalf("insert download %s: %v", id, err)
	}
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestStoreBootstrapAndDefaults(t *testing.T) {
	defaults := testConfig(t)
	pool, store := testStore(t, defaults)
	ctx := context.Background()

	cfg, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if len(cfg.RootFolders) != 2 || cfg.RootFolders[0].ID != "root-a" ||
		cfg.RootFolders[0].Path != defaults.RootFolders[0].Path || cfg.RootFolders[1].Path != defaults.RootFolders[1].Path {
		t.Fatalf("root folders = %+v, want the bootstrap roots", cfg.RootFolders)
	}
	if cfg.FolderTemplate != defaults.FolderTemplate || cfg.FileTemplate != defaults.FileTemplate ||
		cfg.ImportMode != "copy" || cfg.PollMinutes != 60 || cfg.SearchHours != 6 ||
		cfg.MinimumAvailability != "released" || !cfg.WriteNFO || !cfg.RetryFailed {
		t.Fatalf("config = %+v, want the bootstrap values", cfg)
	}
	if cfg.MetadataURL != defaults.MetadataURL || cfg.MetadataAPIKey != defaults.MetadataAPIKey || !cfg.MetadataConfigured {
		t.Fatalf("metadata config = %+v, want the bootstrap values with secrets", cfg)
	}
	if cfg.JellyfinURL != defaults.JellyfinURL || cfg.JellyfinAPIKey != defaults.JellyfinAPIKey || !cfg.JellyfinConfigured {
		t.Fatalf("Jellyfin config = %+v, want the bootstrap values with secrets", cfg)
	}
	if cfg.WebhookURL != defaults.WebhookURL {
		t.Fatalf("webhook = %q, want %q", cfg.WebhookURL, defaults.WebhookURL)
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movie_config`); count != 1 {
		t.Fatalf("movie_config rows = %d, want exactly one", count)
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movie_profiles`); count != len(quality.Defaults()) {
		t.Fatalf("movie_profiles rows = %d, want %d defaults", count, len(quality.Defaults()))
	}
	if defaults := quality.Defaults(); len(defaults) > 0 {
		profiles, err := store.Profiles(ctx)
		if err != nil {
			t.Fatalf("Profiles: %v", err)
		}
		found := false
		for _, profile := range profiles {
			if profile.Name == defaults[0].Name {
				found = true
			}
		}
		if !found {
			t.Fatalf("seeded profiles %+v do not include the default %q", profiles, defaults[0].Name)
		}
	}

	// A second bootstrap with different values must not replace the saved configuration.
	other := testConfig(t)
	other.WebhookURL = "https://other.example/hook"
	other.MetadataAPIKey = "other-metadata-key"
	other.PollMinutes = 5
	second, err := movies.NewStore(ctx, pool, other)
	if err != nil {
		t.Fatalf("second NewStore: %v", err)
	}
	reloaded, err := second.Config(ctx)
	if err != nil {
		t.Fatalf("second Config: %v", err)
	}
	if reloaded.WebhookURL != defaults.WebhookURL || reloaded.MetadataAPIKey != defaults.MetadataAPIKey ||
		reloaded.PollMinutes != defaults.PollMinutes || reloaded.RootFolders[0].Path != defaults.RootFolders[0].Path {
		t.Fatalf("second bootstrap overwrote saved config: %+v", reloaded)
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movie_profiles`); count != len(quality.Defaults()) {
		t.Fatalf("second bootstrap duplicated profiles: %d rows", count)
	}
	removed := quality.Defaults()[0].ID
	if err := second.DeleteProfile(ctx, removed); err != nil {
		t.Fatalf("delete starter profile: %v", err)
	}
	third, err := movies.NewStore(ctx, pool, other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.Profile(ctx, removed); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("restart recreated a deleted starter profile: %v", err)
	}

	catalog, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if catalog == nil || len(catalog) != 0 {
		t.Fatalf("empty catalog = %+v, want an empty slice", catalog)
	}
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("Get of a missing movie: got %v, want ErrNotFound", err)
	}
	if _, err := store.FindIMDb(ctx, "tt0133093"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("FindIMDb of a missing movie: got %v, want ErrNotFound", err)
	}
	if _, err := store.FindIMDb(ctx, "not-an-id"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("FindIMDb of an invalid ID: got %v, want ErrNotFound", err)
	}

	// Storage failures never expose the connection string.
	broken, err := pgxpool.New(ctx, "postgres://tester:supersecret@127.0.0.1:1/movies")
	if err != nil {
		t.Fatalf("cannot build an unreachable pool: %v", err)
	}
	defer broken.Close()
	if _, err := movies.NewStore(ctx, broken, movies.Config{}); err == nil {
		t.Fatal("NewStore with an unreachable database succeeded")
	} else {
		for _, leak := range []string{"supersecret", "127.0.0.1", "postgres://"} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("database error leaks %q: %v", leak, err)
			}
		}
	}
}

func TestStoreMovieLifecycle(t *testing.T) {
	pool, store := testStore(t, testConfig(t))
	ctx := context.Background()
	cfg, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	profile := saveTestProfile(t, ctx, store, "Movies HD")
	rootID := cfg.RootFolders[0].ID

	movie := movies.Movie{
		Metadata: metadata.Title{
			IMDbID: "tt0133093", Title: "The Matrix", Year: 1999, Type: "movie", Released: "1999-03-31",
			Rating: float64Ptr(8.7), Votes: 2000000, Runtime: 136,
			Directors: []string{"Lana Wachowski", "Lilly Wachowski"}, Cast: []string{"Keanu Reeves"},
			Genres: []string{"Action", "Sci-Fi"}, Languages: []string{"English"}, Countries: []string{"United States"},
			Certification: "R", Poster: "https://poster.example/matrix.jpg", Plot: "A hacker learns the truth.",
		},
		Monitored: true, ProfileID: profile.ID, RootID: rootID, Collection: "The Matrix", Status: "wanted",
	}
	saved, err := store.Save(ctx, movie)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(saved.ID) != 26 {
		t.Fatalf("generated ID = %q, want a crypto/rand token", saved.ID)
	}
	if saved.AddedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatalf("timestamps = %v/%v, want both set", saved.AddedAt, saved.UpdatedAt)
	}
	if saved.Tags == nil || len(saved.Tags) != 0 || saved.Files == nil || len(saved.Files) != 0 {
		t.Fatalf("nil arrays were not normalized: tags=%v files=%v", saved.Tags, saved.Files)
	}

	loaded, err := store.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.ID != saved.ID || loaded.Metadata.IMDbID != "tt0133093" || loaded.Metadata.Title != "The Matrix" ||
		loaded.Metadata.Year != 1999 || loaded.Metadata.Released != "1999-03-31" || loaded.Metadata.Runtime != 136 ||
		len(loaded.Metadata.Directors) != 2 || loaded.Metadata.Rating == nil || *loaded.Metadata.Rating != 8.7 ||
		!loaded.Monitored || loaded.ProfileID != profile.ID || loaded.RootID != rootID || loaded.Collection != "The Matrix" {
		t.Fatalf("reloaded movie = %+v, want the saved values", loaded)
	}
	if !loaded.AddedAt.Equal(saved.AddedAt) || !loaded.UpdatedAt.Equal(saved.UpdatedAt) {
		t.Fatalf("reloaded timestamps = %v/%v, want %v/%v", loaded.AddedAt, loaded.UpdatedAt, saved.AddedAt, saved.UpdatedAt)
	}

	found, err := store.FindIMDb(ctx, "tt0133093")
	if err != nil || found.ID != saved.ID {
		t.Fatalf("FindIMDb = %+v, %v; want the saved movie", found, err)
	}

	// A duplicate IMDb identity returns the stored movie instead of inserting another row.
	duplicate, err := store.Save(ctx, movies.Movie{Metadata: metadata.Title{IMDbID: "tt0133093", Title: "The Matrix"}})
	if err != nil {
		t.Fatalf("Save duplicate: %v", err)
	}
	if duplicate.ID != saved.ID {
		t.Fatalf("duplicate IMDb created %s, want the existing %s", duplicate.ID, saved.ID)
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movies`); count != 1 {
		t.Fatalf("movies rows = %d, want 1 after a duplicate save", count)
	}
	imposter, err := store.Save(ctx, movies.Movie{ID: "other-movie-id", Metadata: metadata.Title{IMDbID: "tt0133093", Title: "Imposter"}})
	if err != nil || imposter.ID != saved.ID || imposter.Metadata.Title != "The Matrix" {
		t.Fatalf("duplicate IMDb with another ID = %+v, %v; want the existing movie", imposter, err)
	}

	// An update without the identity keeps the stored IMDb ID.
	blanked, err := store.Save(ctx, movies.Movie{ID: saved.ID, Metadata: metadata.Title{Title: "The Matrix"}, Monitored: true})
	if err != nil {
		t.Fatalf("Save without an IMDb ID: %v", err)
	}
	if blanked.Metadata.IMDbID != "tt0133093" {
		t.Fatalf("stored IMDb ID = %q, want it preserved across a blank update", blanked.Metadata.IMDbID)
	}
	if found, err = store.FindIMDb(ctx, "tt0133093"); err != nil || found.ID != saved.ID {
		t.Fatalf("FindIMDb after a blank update = %+v, %v", found, err)
	}

	// Updates keep the original added_at and replace the rest.
	previousUpdated := saved.UpdatedAt
	saved.Monitored = false
	saved.Tags = []string{"sci-fi"}
	saved.Metadata.Genres = nil
	saved.Files = []movies.File{{
		RootID: rootID, Path: "The Matrix (1999)/The Matrix (1999) 1080p.mkv",
		Size: 2048, Quality: "1080p", Score: 1200, ImportedAt: time.Now().UTC(),
	}}
	updated, err := store.Save(ctx, saved)
	if err != nil {
		t.Fatalf("Save update: %v", err)
	}
	if !updated.AddedAt.Equal(saved.AddedAt) || updated.UpdatedAt.Before(previousUpdated) {
		t.Fatalf("update timestamps = %v/%v, want added %v preserved", updated.AddedAt, updated.UpdatedAt, saved.AddedAt)
	}
	if updated.Monitored || len(updated.Tags) != 1 || updated.Tags[0] != "sci-fi" || len(updated.Metadata.Genres) != 0 ||
		len(updated.Files) != 1 || updated.Files[0].Size != 2048 || updated.Files[0].Quality != "1080p" {
		t.Fatalf("updated movie = %+v, want the replacement values", updated)
	}
	var storedAdded time.Time
	if err := pool.QueryRow(ctx, `SELECT added_at FROM movies WHERE id = $1`, saved.ID).Scan(&storedAdded); err != nil {
		t.Fatalf("read added_at: %v", err)
	}
	if !storedAdded.Equal(saved.AddedAt) {
		t.Fatalf("stored added_at = %v, want %v", storedAdded, saved.AddedAt)
	}

	// A restart loads the catalog from PostgreSQL.
	restarted, err := movies.NewStore(ctx, pool, movies.Config{})
	if err != nil {
		t.Fatalf("restarted NewStore: %v", err)
	}
	catalog, err := restarted.List(ctx)
	if err != nil || len(catalog) != 1 || catalog[0].ID != saved.ID || len(catalog[0].Files) != 1 {
		t.Fatalf("catalog after restart = %+v, %v", catalog, err)
	}

	if err := store.Delete(ctx, saved.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, saved.ID); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("second Delete: got %v, want ErrNotFound", err)
	}
}

func TestStoreMovieValidation(t *testing.T) {
	pool, store := testStore(t, testConfig(t))
	ctx := context.Background()
	cfg, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	profile := saveTestProfile(t, ctx, store, "Movies HD")
	valid, err := store.Save(ctx, movies.Movie{
		Metadata: metadata.Title{
			IMDbID: "tt0133093", Title: "The Matrix", Year: 1999, Released: "1999-03-31",
			Rating: float64Ptr(8.7), Votes: 100, Runtime: 136,
		},
		Monitored: true, ProfileID: profile.ID, RootID: cfg.RootFolders[0].ID,
	})
	if err != nil {
		t.Fatalf("Save valid movie: %v", err)
	}

	cases := []struct {
		name   string
		change func(*movies.Movie)
	}{
		{"blank title", func(m *movies.Movie) { m.Metadata.Title = "   " }},
		{"oversized title", func(m *movies.Movie) { m.Metadata.Title = strings.Repeat("x", 600) }},
		{"invalid ID", func(m *movies.Movie) { m.ID = "bad id!" }},
		{"invalid IMDb", func(m *movies.Movie) { m.Metadata.IMDbID = "1234567" }},
		{"year below range", func(m *movies.Movie) { m.Metadata.Year = 1500 }},
		{"year above range", func(m *movies.Movie) { m.Metadata.Year = time.Now().Year() + 50 }},
		{"unknown release date", func(m *movies.Movie) { m.Metadata.Released = "not a date" }},
		{"rating above range", func(m *movies.Movie) { m.Metadata.Rating = float64Ptr(10.5) }},
		{"rating below range", func(m *movies.Movie) { m.Metadata.Rating = float64Ptr(-0.1) }},
		{"negative votes", func(m *movies.Movie) { m.Metadata.Votes = -1 }},
		{"negative runtime", func(m *movies.Movie) { m.Metadata.Runtime = -1 }},
		{"unknown profile", func(m *movies.Movie) { m.ProfileID = "no-such-profile" }},
		{"unknown root", func(m *movies.Movie) { m.RootID = "no-such-root" }},
		{"absolute file path", func(m *movies.Movie) {
			m.Files = []movies.File{{RootID: m.RootID, Path: "/tmp/movie.mkv", Size: 1024}}
		}},
		{"traversal file path", func(m *movies.Movie) {
			m.Files = []movies.File{{RootID: m.RootID, Path: "../../movie.mkv", Size: 1024}}
		}},
		{"file without size", func(m *movies.Movie) {
			m.Files = []movies.File{{RootID: m.RootID, Path: "movie.mkv"}}
		}},
		{"file with unknown root", func(m *movies.Movie) {
			m.Files = []movies.File{{RootID: "no-such-root", Path: "movie.mkv", Size: 1024}}
		}},
	}
	for _, tc := range cases {
		candidate := valid
		tc.change(&candidate)
		if _, err := store.Save(ctx, candidate); !errors.Is(err, movies.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", tc.name, err)
		}
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movies`); count != 1 {
		t.Fatalf("movies rows = %d, want 1 after rejected saves", count)
	}
}

func TestStoreProfiles(t *testing.T) {
	pool, store := testStore(t, testConfig(t))
	ctx := context.Background()
	cfg, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}

	profiles, err := store.Profiles(ctx)
	if err != nil {
		t.Fatalf("Profiles: %v", err)
	}
	if len(profiles) != len(quality.Defaults()) {
		t.Fatalf("Profiles returned %d rows, want %d seeded defaults", len(profiles), len(quality.Defaults()))
	}
	if _, err := store.Profile(ctx, "missing-profile"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("Profile of a missing row: got %v, want ErrNotFound", err)
	}

	created := saveTestProfile(t, ctx, store, "Worker HD")
	if len(created.ID) != 26 || created.ID == created.Name {
		t.Fatalf("saved profile = %+v, want a generated ID", created)
	}
	loaded, err := store.Profile(ctx, created.ID)
	if err != nil || loaded.Name != "Worker HD" || loaded.ID != created.ID {
		t.Fatalf("Profile = %+v, %v; want the saved profile", loaded, err)
	}

	created.Name = "Worker HD Upgraded"
	updated, err := store.SaveProfile(ctx, created)
	if err != nil {
		t.Fatalf("SaveProfile update: %v", err)
	}
	if updated.ID != created.ID || updated.Name != "Worker HD Upgraded" {
		t.Fatalf("updated profile = %+v, want the same ID", updated)
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movie_profiles WHERE id = $1`, created.ID); count != 1 {
		t.Fatalf("profile updated into %d rows, want 1", count)
	}

	duplicate := testProfile(t, "Worker HD Upgraded")
	if _, err := store.SaveProfile(ctx, duplicate); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("duplicate profile name: got %v, want ErrConflict", err)
	}
	if _, err := store.SaveProfile(ctx, quality.Profile{ID: "bad id!", Name: "Bad"}); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("invalid profile ID: got %v, want ErrInvalid", err)
	}
	if _, err := store.SaveProfile(ctx, quality.Profile{Name: "   "}); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("blank profile name: got %v, want ErrInvalid", err)
	}
	if invalid, ok := invalidProfile(); ok {
		if _, err := store.SaveProfile(ctx, invalid); !errors.Is(err, movies.ErrInvalid) {
			t.Fatalf("quality-invalid profile: got %v, want ErrInvalid", err)
		}
	}

	temporary := saveTestProfile(t, ctx, store, "Temporary")
	if err := store.DeleteProfile(ctx, temporary.ID); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if err := store.DeleteProfile(ctx, temporary.ID); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("second DeleteProfile: got %v, want ErrNotFound", err)
	}

	// Referenced profiles cannot be deleted.
	movie := saveTestMovie(t, ctx, store, created.ID, cfg.RootFolders[0].ID, "tt0133093", "The Matrix")
	if err := store.DeleteProfile(ctx, created.ID); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("DeleteProfile in use by a movie: got %v, want ErrConflict", err)
	}
	watchlist, err := store.SaveWatchlist(ctx, movies.Watchlist{
		Name: "IMDb list", URL: "https://example.com/list.rss",
		ProfileID: created.ID, RootID: cfg.RootFolders[0].ID, IntervalHours: 24,
	})
	if err != nil {
		t.Fatalf("SaveWatchlist: %v", err)
	}
	if err := store.DeleteProfile(ctx, created.ID); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("DeleteProfile in use by a watchlist: got %v, want ErrConflict", err)
	}
	if err := store.DeleteWatchlist(ctx, watchlist.ID); err != nil {
		t.Fatalf("DeleteWatchlist: %v", err)
	}
	if err := store.Delete(ctx, movie.ID); err != nil {
		t.Fatalf("Delete movie: %v", err)
	}
	if err := store.DeleteProfile(ctx, created.ID); err != nil {
		t.Fatalf("DeleteProfile after removing references: %v", err)
	}
}

func invalidProfile() (quality.Profile, bool) {
	for _, candidate := range []quality.Profile{
		{Name: "Broken", Qualities: []string{"not-a-real-quality"}, Cutoff: "not-a-real-quality"},
		{Name: "Broken", MinMB: -1, Qualities: []string{"not-a-real-quality"}},
	} {
		if quality.Validate(candidate) != nil {
			return candidate, true
		}
	}
	return quality.Profile{}, false
}

func TestStoreConfigSaveAndSecrets(t *testing.T) {
	pool, store := testStore(t, testConfig(t))
	ctx := context.Background()
	base, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}

	updated := cloneConfig(base)
	updated.WebhookURL = "https://hooks.example/updated"
	updated.PollMinutes = 30
	updated.ImportMode = "hardlink"
	saved, err := store.SaveConfig(ctx, updated)
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if saved.WebhookURL != "https://hooks.example/updated" || saved.PollMinutes != 30 || saved.ImportMode != "hardlink" {
		t.Fatalf("saved config = %+v, want the updated values", saved)
	}
	if !saved.MetadataConfigured || !saved.JellyfinConfigured {
		t.Fatalf("saved config reports unconfigured integrations: %+v", saved)
	}
	reloaded, err := store.Config(ctx)
	if err != nil || reloaded.WebhookURL != saved.WebhookURL || reloaded.PollMinutes != saved.PollMinutes {
		t.Fatalf("Config after save = %+v, %v", reloaded, err)
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movie_config`); count != 1 {
		t.Fatalf("movie_config rows = %d, want exactly one", count)
	}

	rotated := cloneConfig(reloaded)
	rotated.MetadataAPIKey = "rotated-metadata-key"
	rotated.JellyfinAPIKey = "rotated-jellyfin-key"
	if _, err := store.SaveConfig(ctx, rotated); err != nil {
		t.Fatalf("SaveConfig rotation: %v", err)
	}
	// A separate store instance saving blank secrets keeps the rotated values from the row.
	second, err := movies.NewStore(ctx, pool, movies.Config{})
	if err != nil {
		t.Fatalf("second NewStore: %v", err)
	}
	blank := cloneConfig(reloaded)
	blank.MetadataAPIKey = ""
	blank.JellyfinAPIKey = ""
	blank.WebhookURL = "https://hooks.example/final"
	stored, err := second.SaveConfig(ctx, blank)
	if err != nil {
		t.Fatalf("SaveConfig with blank secrets: %v", err)
	}
	if stored.MetadataAPIKey != "rotated-metadata-key" || stored.JellyfinAPIKey != "rotated-jellyfin-key" {
		t.Fatalf("blank secrets were not retained: %+v", stored)
	}
	for _, provider := range []string{"metadata", "jellyfin"} {
		repointed := cloneConfig(stored)
		repointed.MetadataAPIKey, repointed.JellyfinAPIKey = "", ""
		if provider == "metadata" {
			repointed.MetadataURL = "https://other.example/"
		} else {
			repointed.JellyfinURL = "https://other.example/"
		}
		if _, err := store.SaveConfig(ctx, repointed); !errors.Is(err, movies.ErrInvalid) {
			t.Fatalf("%s secret moved to another origin: %v", provider, err)
		}
	}
	var raw []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM movie_config WHERE id`).Scan(&raw); err != nil {
		t.Fatalf("read config row: %v", err)
	}
	if !strings.Contains(string(raw), "rotated-metadata-key") || strings.Contains(string(raw), "synthetic-metadata-key") {
		t.Fatal("persisted config does not hold the rotated secrets")
	}

	// Configured flags follow the stored URL and secret.
	cleared := cloneConfig(stored)
	cleared.MetadataURL = ""
	cleared.MetadataAPIKey = ""
	result, err := store.SaveConfig(ctx, cleared)
	if err != nil {
		t.Fatalf("SaveConfig without a metadata URL: %v", err)
	}
	if result.MetadataConfigured {
		t.Fatal("metadata reported configured without a URL")
	}
	if result.MetadataAPIKey != "rotated-metadata-key" {
		t.Fatal("clearing the URL dropped the retained secret")
	}

	oversized := cloneConfig(result)
	oversized.JellyfinAPIKey = strings.Repeat("k", 600)
	if _, err := store.SaveConfig(ctx, oversized); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("oversized supplied secret: got %v, want ErrInvalid", err)
	} else if strings.Contains(err.Error(), "kkkk") {
		t.Fatalf("validation error leaks the secret: %v", err)
	}
}

func TestStoreConfigKeepsOversizedBootstrapSecrets(t *testing.T) {
	long := strings.Repeat("b", 600)
	defaults := testConfig(t)
	defaults.MetadataAPIKey = long
	defaults.JellyfinAPIKey = long
	_, store := testStore(t, defaults)
	ctx := context.Background()

	// Blank updates retain trusted bootstrap secrets even beyond the request limit.
	cfg, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	cfg.WebhookURL = "https://hooks.example/oversized"
	saved, err := store.SaveConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if saved.MetadataAPIKey != long || saved.JellyfinAPIKey != long {
		t.Fatal("oversized bootstrap secrets were dropped")
	}
}

func TestStoreConfigValidation(t *testing.T) {
	_, store := testStore(t, testConfig(t))
	ctx := context.Background()
	base, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}

	cases := []struct {
		name   string
		change func(*movies.Config)
	}{
		{"relative root path", func(c *movies.Config) { c.RootFolders[0].Path = "relative/movies" }},
		{"filesystem root", func(c *movies.Config) { c.RootFolders[0].Path = string(filepath.Separator) }},
		{"duplicate root path", func(c *movies.Config) { c.RootFolders[1].Path = c.RootFolders[0].Path }},
		{"duplicate root ID", func(c *movies.Config) { c.RootFolders[1].ID = c.RootFolders[0].ID }},
		{"invalid root ID", func(c *movies.Config) { c.RootFolders[0].ID = "bad id!" }},
		{"unsupported import mode", func(c *movies.Config) { c.ImportMode = "symlink" }},
		{"blank import mode", func(c *movies.Config) { c.ImportMode = "" }},
		{"poll interval too small", func(c *movies.Config) { c.PollMinutes = 0 }},
		{"poll interval too large", func(c *movies.Config) { c.PollMinutes = 1441 }},
		{"search interval too small", func(c *movies.Config) { c.SearchHours = 0 }},
		{"search interval too large", func(c *movies.Config) { c.SearchHours = 721 }},
		{"unknown availability", func(c *movies.Config) { c.MinimumAvailability = "sometimes" }},
		{"non-HTTP metadata URL", func(c *movies.Config) { c.MetadataURL = "ftp://metadata.example/api" }},
		{"metadata URL with credentials", func(c *movies.Config) { c.MetadataURL = "https://user:pass@metadata.example/api" }},
		{"metadata URL with query", func(c *movies.Config) { c.MetadataURL = "https://metadata.example/api?apikey=x" }},
		{"metadata URL with fragment", func(c *movies.Config) { c.MetadataURL = "https://metadata.example/api#frag" }},
		{"Jellyfin URL with credentials", func(c *movies.Config) { c.JellyfinURL = "https://user:pass@jellyfin.example" }},
		{"webhook URL without scheme", func(c *movies.Config) { c.WebhookURL = "hooks.example/constellarr" }},
		{"blank folder template", func(c *movies.Config) { c.FolderTemplate = " " }},
		{"folder template traversal", func(c *movies.Config) { c.FolderTemplate = "../{title}" }},
		{"folder template without tokens", func(c *movies.Config) { c.FolderTemplate = "Movies" }},
		{"folder template unknown token", func(c *movies.Config) { c.FolderTemplate = "{unknown} ({year})" }},
		{"folder template broken token", func(c *movies.Config) { c.FolderTemplate = "{title" }},
		{"file template control character", func(c *movies.Config) { c.FileTemplate = "bad\ttitle {title}" }},
	}
	for _, tc := range cases {
		candidate := cloneConfig(base)
		tc.change(&candidate)
		if _, err := store.SaveConfig(ctx, candidate); !errors.Is(err, movies.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", tc.name, err)
		}
	}
	reloaded, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config after rejected saves: %v", err)
	}
	if reloaded.WebhookURL != base.WebhookURL || reloaded.PollMinutes != base.PollMinutes ||
		reloaded.ImportMode != base.ImportMode || reloaded.RootFolders[0].Path != base.RootFolders[0].Path {
		t.Fatalf("rejected saves changed the config: %+v", reloaded)
	}
}

func TestStoreAcquisitions(t *testing.T) {
	pool, store := testStore(t, testConfig(t))
	ctx := context.Background()
	cfg, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	profile := saveTestProfile(t, ctx, store, "Movies HD")
	rootID := cfg.RootFolders[0].ID
	first := saveTestMovie(t, ctx, store, profile.ID, rootID, "tt0133093", "The Matrix")
	second := saveTestMovie(t, ctx, store, profile.ID, rootID, "tt0109830", "Forrest Gump")
	insertDownload(t, ctx, pool, "job-a")
	insertDownload(t, ctx, pool, "job-b")

	acquisition := movies.Acquisition{
		MovieID: first.ID, JobID: "job-a", ReleaseID: "release-a", Title: "The Matrix 1999 1080p",
		Decision: quality.Decision{Score: 1200, Rank: 1, Allowed: true, Reasons: []string{"preferred group"}},
		Status:   "queued", Override: true,
	}
	if err := store.SaveAcquisition(ctx, acquisition); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}
	list, err := store.Acquisitions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("Acquisitions = %+v, %v; want one row", list, err)
	}
	if list[0].MovieID != first.ID || list[0].JobID != "job-a" || list[0].ReleaseID != "release-a" ||
		list[0].Title != "The Matrix 1999 1080p" || list[0].Status != "queued" || list[0].Decision.Score != 1200 ||
		len(list[0].Decision.Reasons) != 1 || !list[0].Override {
		t.Fatalf("stored acquisition = %+v", list[0])
	}

	// Re-saving a job refreshes its state without adding a row.
	acquisition.Status, acquisition.Error = "completed", "import pending"
	if err := store.SaveAcquisition(ctx, acquisition); err != nil {
		t.Fatalf("SaveAcquisition update: %v", err)
	}
	list, err = store.Acquisitions(ctx)
	if err != nil || len(list) != 1 || list[0].Status != "completed" || list[0].Error != "import pending" {
		t.Fatalf("refreshed acquisitions = %+v, %v", list, err)
	}

	// A job cannot change its movie.
	theft := acquisition
	theft.MovieID = second.ID
	if err := store.SaveAcquisition(ctx, theft); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("reassigning a job: got %v, want ErrConflict", err)
	}
	list, err = store.Acquisitions(ctx)
	if err != nil || len(list) != 1 || list[0].MovieID != first.ID {
		t.Fatalf("job ownership changed: %+v, %v", list, err)
	}

	if err := store.SaveAcquisition(ctx, movies.Acquisition{MovieID: "missing-movie", JobID: "job-b"}); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("unknown movie: got %v, want ErrNotFound", err)
	}
	if err := store.SaveAcquisition(ctx, movies.Acquisition{MovieID: first.ID, JobID: "missing-job"}); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("unknown download: got %v, want ErrNotFound", err)
	}
	if err := store.SaveAcquisition(ctx, movies.Acquisition{MovieID: "bad id!", JobID: "job-b"}); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("invalid movie ID: got %v, want ErrInvalid", err)
	}

	// Removing a download removes its acquisition; removing a movie cascades.
	insertDownload(t, ctx, pool, "job-c")
	if err := store.SaveAcquisition(ctx, movies.Acquisition{MovieID: second.ID, JobID: "job-c"}); err != nil {
		t.Fatalf("SaveAcquisition for job-c: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM downloads WHERE id = 'job-c'`); err != nil {
		t.Fatalf("delete download: %v", err)
	}
	if list, err = store.Acquisitions(ctx); err != nil || len(list) != 1 {
		t.Fatalf("acquisitions after download deletion = %+v, %v", list, err)
	}
	if err := store.Delete(ctx, first.ID); err != nil {
		t.Fatalf("Delete movie: %v", err)
	}
	if list, err = store.Acquisitions(ctx); err != nil || len(list) != 0 {
		t.Fatalf("acquisitions after movie deletion = %+v, %v", list, err)
	}
}

func TestStoreHistoryAndBlocklist(t *testing.T) {
	pool, store := testStore(t, testConfig(t))
	ctx := context.Background()
	cfg, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	profile := saveTestProfile(t, ctx, store, "Movies HD")
	movie := saveTestMovie(t, ctx, store, profile.ID, cfg.RootFolders[0].ID, "tt0133093", "The Matrix")

	for i, kind := range []string{"added", "search", "download"} {
		if err := store.Event(ctx, movie.ID, kind, fmt.Sprintf("event %d", i+1)); err != nil {
			t.Fatalf("Event: %v", err)
		}
	}
	history, err := store.History(ctx, movie.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 3 || history[0].Type != "download" || history[2].Type != "added" || history[0].CreatedAt.IsZero() {
		t.Fatalf("history = %+v, want the latest events first", history)
	}

	// History keeps the newest 200 events.
	if _, err := pool.Exec(ctx,
		`INSERT INTO movie_history (movie_id, type, message)
		 SELECT $1, 'search', 'bulk ' || g FROM generate_series(1, 205) g`, movie.ID); err != nil {
		t.Fatalf("insert bulk history: %v", err)
	}
	history, err = store.History(ctx, movie.ID)
	if err != nil || len(history) != 200 || history[0].Message != "bulk 205" {
		t.Fatalf("capped history has %d rows starting at %q, %v", len(history), history[0].Message, err)
	}

	// Event messages are capped to avoid bulk rows.
	if err := store.Event(ctx, movie.ID, "error", strings.Repeat("m", 1500)); err != nil {
		t.Fatalf("Event oversized message: %v", err)
	}
	history, err = store.History(ctx, movie.ID)
	if err != nil || len([]rune(history[0].Message)) != 1000 {
		t.Fatalf("stored message length = %d, %v; want 1000", len([]rune(history[0].Message)), err)
	}

	// A blank movie ID returns the newest events across the catalog for the activity feed.
	global, err := store.History(ctx, "")
	if err != nil || len(global) != 200 || global[0].MovieID != movie.ID || global[0].Message != history[0].Message {
		t.Fatalf("global history has %d rows, %v; want the newest 200 events", len(global), err)
	}

	if err := store.Event(ctx, "missing-movie", "added", "x"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("Event for a missing movie: got %v, want ErrNotFound", err)
	}
	if err := store.Event(ctx, movie.ID, "  ", "x"); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("Event without a type: got %v, want ErrInvalid", err)
	}
	if err := store.Event(ctx, "bad id!", "added", "x"); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("Event with an invalid movie ID: got %v, want ErrInvalid", err)
	}
	if _, err := store.History(ctx, "missing-movie"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("History of a missing movie: got %v, want ErrNotFound", err)
	}

	blocked, err := store.Blocked(ctx, movie.ID, "release-a")
	if err != nil || blocked {
		t.Fatalf("Blocked before blocking = %v, %v", blocked, err)
	}
	if err := store.Block(ctx, movie.ID, "release-a"); err != nil {
		t.Fatalf("Block: %v", err)
	}
	if err := store.Block(ctx, movie.ID, "release-a"); err != nil {
		t.Fatalf("Block duplicate: %v", err)
	}
	if blocked, err = store.Blocked(ctx, movie.ID, "release-a"); err != nil || !blocked {
		t.Fatalf("Blocked after blocking = %v, %v", blocked, err)
	}
	if blocked, err = store.Blocked(ctx, movie.ID, "release-b"); err != nil || blocked {
		t.Fatalf("Blocked for another release = %v, %v", blocked, err)
	}
	if err := store.Block(ctx, "missing-movie", "release-a"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("Block for a missing movie: got %v, want ErrNotFound", err)
	}
	if _, err := store.Blocked(ctx, movie.ID, ""); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("Blocked without a release: got %v, want ErrInvalid", err)
	}
	if err := store.Unblock(ctx, movie.ID, "release-a"); err != nil {
		t.Fatalf("Unblock: %v", err)
	}
	if blocked, err = store.Blocked(ctx, movie.ID, "release-a"); err != nil || blocked {
		t.Fatalf("Blocked after unblocking = %v, %v", blocked, err)
	}
	if err := store.Unblock(ctx, movie.ID, "release-a"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("second Unblock: got %v, want ErrNotFound", err)
	}

	// Deleting a movie removes its history and blocklist.
	if err := store.Block(ctx, movie.ID, "release-c"); err != nil {
		t.Fatalf("Block before delete: %v", err)
	}
	if err := store.Delete(ctx, movie.ID); err != nil {
		t.Fatalf("Delete movie: %v", err)
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movie_history WHERE movie_id = $1`, movie.ID); count != 0 {
		t.Fatalf("history rows after delete = %d, want 0", count)
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movie_blocklist WHERE movie_id = $1`, movie.ID); count != 0 {
		t.Fatalf("blocklist rows after delete = %d, want 0", count)
	}
}

func TestStoreWatchlists(t *testing.T) {
	pool, store := testStore(t, testConfig(t))
	ctx := context.Background()
	cfg, err := store.Config(ctx)
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	profile := saveTestProfile(t, ctx, store, "Movies HD")
	rootID := cfg.RootFolders[0].ID
	synced := time.Now().UTC().Truncate(time.Millisecond)

	list := movies.Watchlist{
		Name: "IMDb Watchlist", IMDbIDs: []string{"tt0133093", "TT0109830", "tt0133093"},
		Monitor: true, ProfileID: profile.ID, RootID: rootID, IntervalHours: 24, Error: "last sync found 2 titles",
	}
	saved, err := store.SaveWatchlist(ctx, list)
	if err != nil {
		t.Fatalf("SaveWatchlist: %v", err)
	}
	if len(saved.ID) != 26 {
		t.Fatalf("generated watchlist ID = %q, want a crypto/rand token", saved.ID)
	}
	if strings.Join(saved.IMDbIDs, ",") != "tt0133093,tt0109830" {
		t.Fatalf("normalized IMDb IDs = %v, want lowercase unique IDs", saved.IMDbIDs)
	}
	if saved.Error != "last sync found 2 titles" {
		t.Fatalf("watchlist error = %q, want the supplied message", saved.Error)
	}

	watchlists, err := store.Watchlists(ctx)
	if err != nil || len(watchlists) != 1 {
		t.Fatalf("Watchlists = %+v, %v; want one row", watchlists, err)
	}
	if watchlists[0].ID != saved.ID || watchlists[0].Name != "IMDb Watchlist" || !watchlists[0].Monitor ||
		watchlists[0].ProfileID != profile.ID || watchlists[0].RootID != rootID || watchlists[0].IntervalHours != 24 {
		t.Fatalf("stored watchlist = %+v", watchlists[0])
	}

	// Updates keep the ID and all fields.
	saved.IntervalHours, saved.LastSyncAt, saved.Error = 12, &synced, ""
	updated, err := store.SaveWatchlist(ctx, saved)
	if err != nil {
		t.Fatalf("SaveWatchlist update: %v", err)
	}
	if updated.ID != saved.ID || updated.IntervalHours != 12 || updated.LastSyncAt == nil || !updated.LastSyncAt.Equal(synced) {
		t.Fatalf("updated watchlist = %+v, want the same ID with the new interval", updated)
	}
	if watchlists, err = store.Watchlists(ctx); err != nil || len(watchlists) != 1 {
		t.Fatalf("watchlists after update = %+v, %v", watchlists, err)
	}

	if _, err := store.SaveWatchlist(ctx, movies.Watchlist{Name: "RSS", URL: "https://example.com/list.rss", IntervalHours: 6}); err != nil {
		t.Fatalf("SaveWatchlist with a URL: %v", err)
	}

	cases := []struct {
		name string
		list movies.Watchlist
	}{
		{"without a source", movies.Watchlist{Name: "Empty", IntervalHours: 24}},
		{"invalid IMDb", movies.Watchlist{Name: "Bad", IMDbIDs: []string{"tt12x"}, IntervalHours: 24}},
		{"URL with credentials", movies.Watchlist{Name: "Bad", URL: "https://user:pass@example.com/list", IntervalHours: 24}},
		{"non-HTTP URL", movies.Watchlist{Name: "Bad", URL: "ftp://example.com/list", IntervalHours: 24}},
		{"zero interval", movies.Watchlist{Name: "Bad", URL: "https://example.com/list"}},
		{"interval above range", movies.Watchlist{Name: "Bad", URL: "https://example.com/list", IntervalHours: 721}},
		{"blank name", movies.Watchlist{Name: " ", URL: "https://example.com/list", IntervalHours: 24}},
		{"unknown profile", movies.Watchlist{Name: "Bad", URL: "https://example.com/list", ProfileID: "no-such-profile", IntervalHours: 24}},
		{"unknown root", movies.Watchlist{Name: "Bad", URL: "https://example.com/list", RootID: "no-such-root", IntervalHours: 24}},
	}
	for _, tc := range cases {
		if _, err := store.SaveWatchlist(ctx, tc.list); !errors.Is(err, movies.ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", tc.name, err)
		}
	}
	if count := countRows(t, ctx, pool, `SELECT count(*) FROM movie_watchlists`); count != 2 {
		t.Fatalf("watchlist rows = %d, want the two valid rows", count)
	}

	if err := store.DeleteWatchlist(ctx, saved.ID); err != nil {
		t.Fatalf("DeleteWatchlist: %v", err)
	}
	if err := store.DeleteWatchlist(ctx, saved.ID); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("second DeleteWatchlist: got %v, want ErrNotFound", err)
	}
}
