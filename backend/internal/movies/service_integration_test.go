package movies_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

// testSchema creates an isolated schema so tests never touch real tables.
func testSchema(t *testing.T) *pgxpool.Pool {
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
	return pool
}

type testEnv struct {
	service   *movies.Service
	manager   *downloads.Manager
	pool      *pgxpool.Pool
	directory string
	rootID    string
}

func newTestEnv(t *testing.T, indexerURL string) *testEnv {
	t.Helper()
	for _, name := range []string{"OMDB_URL", "OMDB_API_KEY", "JELLYFIN_URL", "JELLYFIN_API_KEY", "IMPORT_WEBHOOK_URL"} {
		t.Setenv(name, "")
	}
	pool := testSchema(t)
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
	service, err := movies.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	env := &testEnv{service: service, manager: manager, pool: pool, directory: directory, rootID: "root"}
	env.saveConfig(t, movies.Config{
		RootFolders:         []movies.RootFolder{{ID: env.rootID, Path: filepath.Join(directory, "library")}},
		FolderTemplate:      "{title} ({year}) [imdb-{imdbId}]",
		FileTemplate:        "{title} ({year}) [{quality}]",
		ImportMode:          library.ModeLink,
		WriteNFO:            true,
		PollMinutes:         15,
		SearchHours:         6,
		MinimumAvailability: "released",
		RetryFailed:         true,
	})
	return env
}

func (e *testEnv) saveConfig(t *testing.T, cfg movies.Config) {
	t.Helper()
	if _, err := e.service.SetConfig(context.Background(), cfg); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
}

func (e *testEnv) config(t *testing.T) movies.Config {
	t.Helper()
	cfg, err := e.service.Store.Config(context.Background())
	if err != nil {
		t.Fatalf("Store.Config: %v", err)
	}
	return cfg
}

func (e *testEnv) rootPath() string {
	return filepath.Join(e.directory, "library")
}

func (e *testEnv) writeLibraryFile(t *testing.T, rel, content string) movies.File {
	t.Helper()
	target := filepath.Join(e.rootPath(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("create library directory: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write library file: %v", err)
	}
	return movies.File{RootID: e.rootID, Path: rel, Size: int64(len(content)), Quality: "Bluray-1080p", Score: 0, ImportedAt: time.Now().UTC()}
}

func (e *testEnv) outputFile(t *testing.T, jobID, name, content string) downloads.OutputFile {
	t.Helper()
	outputDir := filepath.Join(e.directory, "downloads", jobID, "output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("create output directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}
	return downloads.OutputFile{Name: name, Size: int64(len(content)), URL: "/api/v1/downloads/" + jobID + "/file?name=" + name}
}

func (e *testEnv) completedJob(t *testing.T, jobID, releaseID, title string, files []downloads.OutputFile) {
	t.Helper()
	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatalf("encode output files: %v", err)
	}
	_, err = e.pool.Exec(context.Background(),
		`INSERT INTO downloads (id, release_id, title, nzb, status, files) VALUES ($1, $2, $3, $4, 'completed', $5::jsonb)`,
		jobID, releaseID, title, []byte("<nzb/>"), string(encoded))
	if err != nil {
		t.Fatalf("insert completed download: %v", err)
	}
}

func (e *testEnv) manualMovie(t *testing.T, title string, year int, monitored bool) movies.Movie {
	t.Helper()
	movie, err := e.service.Add(context.Background(), movies.AddInput{
		Metadata: archiveTestTitle(title, year), Monitored: monitored, ProfileID: "hd", RootID: e.rootID,
	})
	if err != nil {
		t.Fatalf("Add(%s): %v", title, err)
	}
	return movie
}

func archiveTestTitle(title string, year int) metadata.Title {
	return metadata.Title{Title: title, Year: year, Type: "movie", Released: fmt.Sprintf("%d-01-01", year)}
}

type omdbFixture struct {
	title    string
	year     int
	released string
	rating   string
}

func omdbServer(t *testing.T, fixtures map[string]omdbFixture) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		if id := query.Get("i"); id != "" {
			fixture, ok := fixtures[id]
			if !ok {
				_, _ = io.WriteString(w, `{"Response":"False","Error":"Movie not found!"}`)
				return
			}
			payload := map[string]string{
				"Title": fixture.title, "Year": strconv.Itoa(fixture.year), "Released": fixture.released,
				"Type": "movie", "imdbID": id, "Response": "True",
			}
			if fixture.rating != "" {
				payload["imdbRating"] = fixture.rating
				payload["imdbVotes"] = "1000"
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		text := strings.ToLower(query.Get("s"))
		ids := make([]string, 0, len(fixtures))
		for id := range fixtures {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		results := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			fixture := fixtures[id]
			if text != "" && !strings.Contains(strings.ToLower(fixture.title), text) {
				continue
			}
			results = append(results, map[string]string{
				"Title": fixture.title, "Year": strconv.Itoa(fixture.year), "Type": "movie", "imdbID": id,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Search": results, "Response": "True"})
	}))
	t.Cleanup(server.Close)
	return server
}

type releaseFixture struct {
	title string
	guid  string
	size  int64
	imdb  string
}

func indexerServer(t *testing.T, releases map[string][]releaseFixture) *httptest.Server {
	t.Helper()
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		switch r.URL.Query().Get("t") {
		case "movie", "search":
			var body strings.Builder
			body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>`)
			for _, item := range releases[r.URL.Query().Get("imdbid")] {
				fmt.Fprintf(&body, `<item><title>%s</title><guid isPermaLink="false">%s</guid><pubDate>Fri, 02 Jan 2026 10:00:00 +0000</pubDate><enclosure url="https://example.invalid/%s.nzb" length="%d"/>`,
					escape(item.title), escape(item.guid), escape(item.guid), item.size)
				if item.size > 0 {
					fmt.Fprintf(&body, `<newznab:attr name="size" value="%d"/>`, item.size)
				}
				if item.imdb != "" {
					fmt.Fprintf(&body, `<newznab:attr name="imdb" value="%s"/>`, escape(item.imdb))
				}
				body.WriteString(`</item>`)
			}
			body.WriteString(`</channel></rss>`)
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = io.WriteString(w, body.String())
		case "get":
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = io.WriteString(w, `<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><file subject="[1/1] - &quot;movie.mkv&quot; yEnc (1/1)" date="1136214245" poster="poster &lt;poster@example.com&gt;"><groups><group>alt.binaries.test</group></groups><segments><segment bytes="1024" number="1">synthetic-part@example.com</segment></segments></file></nzb>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestAddDiscoverAndListState(t *testing.T) {
	ctx := context.Background()
	omdb := omdbServer(t, map[string]omdbFixture{
		"tt0111161": {title: "The Shawshank Redemption", year: 1994, released: "14 Oct 1994", rating: "9.3"},
	})
	env := newTestEnv(t, "")
	env.saveConfig(t, movies.Config{
		RootFolders:         []movies.RootFolder{{ID: env.rootID, Path: env.rootPath()}},
		FolderTemplate:      "{title} ({year}) [imdb-{imdbId}]",
		FileTemplate:        "{title} ({year}) [{quality}]",
		ImportMode:          library.ModeLink,
		WriteNFO:            true,
		PollMinutes:         15,
		SearchHours:         6,
		MinimumAvailability: "released",
		RetryFailed:         true,
		MetadataURL:         omdb.URL,
		MetadataAPIKey:      "omdb-secret",
	})

	movie, err := env.service.Add(ctx, movies.AddInput{IMDbID: "tt0111161", Monitored: true, ProfileID: "hd", RootID: env.rootID})
	if err != nil {
		t.Fatalf("Add by IMDb: %v", err)
	}
	if movie.Metadata.Title != "The Shawshank Redemption" || movie.Metadata.Year != 1994 || movie.Metadata.Rating == nil {
		t.Fatalf("unexpected metadata: %+v", movie.Metadata)
	}
	duplicate, err := env.service.Add(ctx, movies.AddInput{IMDbID: "tt0111161", Monitored: false, ProfileID: "hd", RootID: env.rootID})
	if err != nil {
		t.Fatalf("Add duplicate: %v", err)
	}
	if duplicate.ID != movie.ID || len(mustList(t, env)) != 1 {
		t.Fatalf("duplicate IMDb add created a second movie")
	}
	if _, err := env.service.Add(ctx, movies.AddInput{IMDbID: "not-an-id", Monitored: true}); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("invalid IMDb ID error = %v, want ErrInvalid", err)
	}

	future := env.manualMovie(t, "Future Film", time.Now().UTC().Year()+1, true)
	past := env.manualMovie(t, "Past Film", 1999, true)
	unmonitored := env.manualMovie(t, "Archived Film", 1990, false)
	statuses := map[string]string{}
	for _, item := range mustList(t, env) {
		statuses[item.ID] = item.Status
	}
	if statuses[future.ID] != "missing" || statuses[past.ID] != "wanted" || statuses[unmonitored.ID] != "unmonitored" {
		t.Fatalf("statuses = %+v", statuses)
	}

	env.writeLibraryFile(t, "The Shawshank Redemption (1994)/file.mkv", "bytes")
	movie.Files = []movies.File{{RootID: env.rootID, Path: "The Shawshank Redemption (1994)/file.mkv", Size: 5, Quality: "Bluray-1080p", ImportedAt: time.Now().UTC()}}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save movie with file: %v", err)
	}
	for _, item := range mustList(t, env) {
		if item.ID == movie.ID && item.Status != "available" {
			t.Fatalf("movie status = %q, want available", item.Status)
		}
	}
	if err := os.Remove(filepath.Join(env.rootPath(), "The Shawshank Redemption (1994)", "file.mkv")); err != nil {
		t.Fatalf("remove library file: %v", err)
	}
	for _, item := range mustList(t, env) {
		if item.ID == movie.ID && (len(item.Files) != 1 || !item.Files[0].Missing || item.Status != "wanted") {
			t.Fatalf("missing file state = %+v", item)
		}
	}

	titles, err := env.service.Discover(ctx, "shawshank", 1)
	if err != nil || len(titles) != 1 || titles[0].IMDbID != "tt0111161" {
		t.Fatalf("Discover = %+v, %v", titles, err)
	}
	view, err := env.service.ConfigView(ctx)
	if err != nil {
		t.Fatalf("ConfigView: %v", err)
	}
	if view.MetadataAPIKey != "" || !view.MetadataConfigured {
		t.Fatalf("ConfigView leaked or hid the key incorrectly: %+v", view)
	}
	raw := env.config(t)
	if raw.MetadataAPIKey != "omdb-secret" {
		t.Fatalf("saved metadata key = %q", raw.MetadataAPIKey)
	}
}

func mustList(t *testing.T, env *testEnv) []movies.Movie {
	t.Helper()
	list, err := env.service.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return list
}

func TestBulkPrevalidationIsAtomic(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	first := env.manualMovie(t, "First Film", 2001, true)
	second := env.manualMovie(t, "Second Film", 2002, true)

	falseValue := false
	if _, err := env.service.Bulk(ctx, movies.BulkInput{IDs: []string{first.ID, "missing-movie"}, Monitored: &falseValue}); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("bulk with a missing ID error = %v, want ErrNotFound", err)
	}
	for _, movie := range mustList(t, env) {
		if !movie.Monitored {
			t.Fatalf("bulk with a missing ID changed monitored state")
		}
	}
	badProfile := "does-not-exist"
	if _, err := env.service.Bulk(ctx, movies.BulkInput{IDs: []string{first.ID, second.ID}, ProfileID: &badProfile}); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("bulk with a bad profile error = %v, want ErrInvalid", err)
	}
	collection := "Favorites"
	updated, err := env.service.Bulk(ctx, movies.BulkInput{IDs: []string{first.ID, second.ID}, Monitored: &falseValue, Collection: &collection})
	if err != nil || len(updated) != 2 {
		t.Fatalf("bulk update = %+v, %v", updated, err)
	}
	for _, movie := range updated {
		if movie.Monitored || movie.Collection != "Favorites" {
			t.Fatalf("bulk update did not persist: %+v", movie)
		}
	}
}

func TestConfigRetainsSecretsAndProtectsRoots(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	base := env.config(t)
	base.MetadataURL = "https://www.omdbapi.com/"
	base.MetadataAPIKey = "first-secret"

	first, err := env.service.SetConfig(ctx, base)
	if err != nil {
		t.Fatalf("SetConfig with key: %v", err)
	}
	if !first.MetadataConfigured || first.MetadataAPIKey != "" {
		t.Fatalf("SetConfig response must hide keys and report configuration: %+v", first)
	}
	base.MetadataAPIKey = ""
	base.PollMinutes = 30
	if _, err := env.service.SetConfig(ctx, base); err != nil {
		t.Fatalf("SetConfig with blank key: %v", err)
	}
	if env.config(t).MetadataAPIKey != "first-secret" || env.config(t).PollMinutes != 30 {
		t.Fatalf("blank key did not retain the saved secret")
	}

	movie := env.manualMovie(t, "Root Bound", 2000, true)
	movie.Files = []movies.File{env.writeLibraryFile(t, "Root Bound (2000)/file.mkv", "movie")}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save movie file: %v", err)
	}
	removed := env.config(t)
	removed.RootFolders = []movies.RootFolder{{ID: "other", Path: filepath.Join(env.directory, "other")}}
	if _, err := env.service.SetConfig(ctx, removed); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("removing a used root error = %v, want ErrConflict", err)
	}
	added := env.config(t)
	added.RootFolders = append(added.RootFolders, movies.RootFolder{ID: "extra", Path: filepath.Join(env.directory, "extra")})
	if _, err := env.service.SetConfig(ctx, added); err != nil {
		t.Fatalf("adding a root: %v", err)
	}
	if info, err := os.Stat(filepath.Join(env.directory, "extra")); err != nil || !info.IsDir() {
		t.Fatalf("added root folder was not created: %v", err)
	}
}

func TestGrabRejectsWrongFilmAndDuplicates(t *testing.T) {
	ctx := context.Background()
	indexer := indexerServer(t, map[string][]releaseFixture{
		"20202020": {
			{title: "The Right Film 2020 1080p BluRay x264-GRP", guid: "rel-right", size: 8 << 30, imdb: "tt20202020"},
			{title: "The Right Film 2020 1080p WEB-DL x264-GRP", guid: "rel-web", size: 4 << 30},
			{title: "The Wrong Film 2020 1080p BluRay x264-GRP", guid: "rel-wrong", size: 8 << 30, imdb: "tt99999999"},
			{title: "The Right Film 1990 1080p BluRay x264-GRP", guid: "rel-remake", size: 8 << 30},
		},
	})
	env := newTestEnv(t, indexer.URL)
	omdb := omdbServer(t, map[string]omdbFixture{
		"tt20202020": {title: "The Right Film", year: 2020, released: "01 Jun 2020"},
	})
	cfg := env.config(t)
	cfg.MetadataURL = omdb.URL
	cfg.MetadataAPIKey = "omdb-secret"
	env.saveConfig(t, cfg)

	movie, err := env.service.Add(ctx, movies.AddInput{IMDbID: "tt20202020", Monitored: true, ProfileID: "hd", RootID: env.rootID})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	releases, err := env.service.Search(ctx, movie.ID)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	byID := map[string]movies.Release{}
	for _, release := range releases {
		byID[release.ID] = release
	}
	if byID["rel-wrong"].Decision.Allowed || !containsReason(byID["rel-wrong"].Decision.Reasons, "does not match") {
		t.Fatalf("wrong film was not rejected: %+v", byID["rel-wrong"].Decision)
	}
	if byID["rel-remake"].Decision.Allowed || !containsReason(byID["rel-remake"].Decision.Reasons, "does not match") {
		t.Fatalf("remake year was not rejected: %+v", byID["rel-remake"].Decision)
	}
	if !byID["rel-right"].Decision.Allowed {
		t.Fatalf("matching release was rejected: %+v", byID["rel-right"].Decision)
	}
	if len(releases) == 0 || releases[0].ID != "rel-right" {
		t.Fatalf("release sorting did not prefer the allowed release first: %+v", releases)
	}

	if _, err := env.service.Grab(ctx, movie.ID, "spoofed-release", false); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("spoofed release error = %v, want ErrInvalid", err)
	}
	if _, err := env.service.Grab(ctx, movie.ID, "rel-wrong", true); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("wrong film with override error = %v, want ErrInvalid", err)
	}
	job, err := env.service.Grab(ctx, movie.ID, "rel-right", false)
	if err != nil || job.ID == "" {
		t.Fatalf("Grab: %+v, %v", job, err)
	}
	if _, err := env.service.Grab(ctx, movie.ID, "rel-web", false); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("duplicate grab error = %v, want ErrConflict", err)
	}
	acquisitions, err := env.service.Store.Acquisitions(ctx)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].JobID != job.ID {
		t.Fatalf("acquisitions = %+v, %v", acquisitions, err)
	}
	list := mustList(t, env)
	for _, item := range list {
		if item.ID == movie.ID && item.Status != "downloading" {
			t.Fatalf("movie status after grab = %q, want downloading", item.Status)
		}
	}
}

func TestConcurrentGrabsQueueOneDownload(t *testing.T) {
	ctx := context.Background()
	indexer := indexerServer(t, map[string][]releaseFixture{
		"": {{title: "Concurrent Film 2019 1080p BluRay x264-GRP", guid: "rel-one", size: 8 << 30, imdb: "tt30303030"}},
	})
	env := newTestEnv(t, indexer.URL)
	movie := env.manualMovie(t, "Concurrent Film", 2019, true)
	// Manual movies have no IMDb attribute, so identity is confirmed by title and year.
	jobIDs := make([]string, 0, 2)
	errs := make([]error, 0, 2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := env.service.Grab(ctx, movie.ID, "rel-one", false)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				jobIDs = append(jobIDs, job.ID)
			} else {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	if len(jobIDs) != 1 || len(errs) != 1 || !errors.Is(errs[0], movies.ErrConflict) {
		t.Fatalf("concurrent grabs = jobs %v, errors %v", jobIDs, errs)
	}
	acquisitions, err := env.service.Store.Acquisitions(ctx)
	if err != nil || len(acquisitions) != 1 {
		t.Fatalf("concurrent grabs created %d acquisitions, %v", len(acquisitions), err)
	}
}

func TestSyncDownloadsImportsCompletedDownload(t *testing.T) {
	ctx := context.Background()
	var jellyfinCalls int
	var jellyfinToken string
	jellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Library/Refresh" {
			jellyfinCalls++
			jellyfinToken = r.Header.Get("X-Emby-Token")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(jellyfin.Close)
	var webhookBody []byte
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webhookBody, _ = io.ReadAll(io.LimitReader(r.Body, 1<<16))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(webhook.Close)

	env := newTestEnv(t, "")
	cfg := env.config(t)
	cfg.JellyfinURL = jellyfin.URL
	cfg.JellyfinAPIKey = "jellyfin-secret"
	cfg.WebhookURL = webhook.URL
	env.saveConfig(t, cfg)

	movie := env.manualMovie(t, "Imported Film", 2018, true)
	source := env.outputFile(t, "job-import", "Imported.Film.2018.1080p.BluRay.x264-GRP.mkv", "film-bytes")
	env.completedJob(t, "job-import", "rel-import", "Imported Film 2018 1080p BluRay", []downloads.OutputFile{source})
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: "job-import", ReleaseID: "rel-import", Title: "Imported Film 2018 1080p BluRay", Status: "queued",
	}); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}

	imported, err := env.service.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("SyncDownloads = %d, %v", imported, err)
	}
	if imported, err := env.service.SyncDownloads(ctx); err != nil || imported != 0 {
		t.Fatalf("second SyncDownloads = %d, %v", imported, err)
	}
	acquisitions, err := env.service.Store.Acquisitions(ctx)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].Status != "imported" {
		t.Fatalf("acquisitions after import = %+v, %v", acquisitions, err)
	}
	stored, err := env.service.Store.Get(ctx, movie.ID)
	if err != nil || len(stored.Files) != 1 {
		t.Fatalf("movie files after import = %+v, %v", stored.Files, err)
	}
	file := stored.Files[0]
	if !strings.Contains(file.Path, "Imported Film (2018) [imdb-]") || file.Quality != "Bluray-1080p" {
		t.Fatalf("imported file = %+v", file)
	}
	if handle, err := env.service.OpenFile(ctx, movie.ID, file.Path); err != nil {
		t.Fatalf("OpenFile: %v", err)
	} else {
		handle.Close()
	}
	if _, err := env.service.OpenFile(ctx, movie.ID, "../escape.mkv"); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("OpenFile traversal error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(file.Path))); err != nil {
		t.Fatalf("imported file is missing: %v", err)
	}
	nfo := filepath.Join(env.rootPath(), filepath.Dir(filepath.FromSlash(file.Path)), "movie.nfo")
	if _, err := os.Stat(nfo); err != nil {
		t.Fatalf("NFO sidecar is missing: %v", err)
	}
	// The source stays available for default hardlink imports.
	if _, err := os.Stat(filepath.Join(env.directory, "downloads", "job-import", "output", source.Name)); err != nil {
		t.Fatalf("hardlink import removed its source: %v", err)
	}
	if jellyfinCalls != 1 || jellyfinToken != "jellyfin-secret" {
		t.Fatalf("Jellyfin refresh calls = %d, token = %q", jellyfinCalls, jellyfinToken)
	}
	var payload map[string]any
	if err := json.Unmarshal(webhookBody, &payload); err != nil {
		t.Fatalf("webhook payload: %v (%s)", err, webhookBody)
	}
	if payload["event"] != "movie.imported" || payload["title"] != "Imported Film" || payload["movieId"] != movie.ID {
		t.Fatalf("webhook payload = %s", webhookBody)
	}
	if files, ok := payload["files"].([]any); !ok || len(files) != 1 {
		t.Fatalf("webhook files = %v", payload["files"])
	}
	if strings.Contains(string(webhookBody), "jellyfin-secret") || strings.Contains(string(webhookBody), "synthetic-key") {
		t.Fatalf("webhook payload leaked a credential: %s", webhookBody)
	}
	var journalReady bool
	if err := env.pool.QueryRow(ctx, `SELECT ready FROM download_library_files WHERE job_id = 'job-import'`).Scan(&journalReady); err != nil || !journalReady {
		t.Fatalf("journal row ready = %v, %v", journalReady, err)
	}
	for _, item := range mustList(t, env) {
		if item.ID == movie.ID && item.Status != "available" {
			t.Fatalf("movie status after import = %q, want available", item.Status)
		}
	}
}

func TestMoveRestartRecoveryUsesJournalHash(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	cfg := env.config(t)
	cfg.ImportMode = library.ModeMove
	env.saveConfig(t, cfg)
	movie := env.manualMovie(t, "Recovered Film", 2017, true)

	destination := "Recovered Film (2017) [imdb-]/Recovered.Film.2017.1080p.mkv"
	published := env.writeLibraryFile(t, destination, "recovered-bytes")
	digest := sha256.Sum256([]byte("recovered-bytes"))
	env.completedJob(t, "job-move", "rel-move", "Recovered Film 2017 1080p BluRay", []downloads.OutputFile{
		{Name: "Recovered.Film.2017.1080p.mkv", Size: published.Size, URL: "/api/v1/downloads/job-move/file"},
	})
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO download_library_files (job_id, name, root_path, path, size, sha256, ready)
		 VALUES ('job-move', 'Recovered.Film.2017.1080p.mkv', $1, $2, $3, $4, false)`,
		env.rootPath(), destination, published.Size, hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("insert journal row: %v", err)
	}
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: movie.ID, JobID: "job-move", ReleaseID: "rel-move", Title: "Recovered Film 2017 1080p BluRay", Status: "importing",
	}); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}
	imported, err := env.service.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("recovered SyncDownloads = %d, %v", imported, err)
	}
	stored, err := env.service.Store.Get(ctx, movie.ID)
	if err != nil || len(stored.Files) != 1 || stored.Files[0].Path != destination {
		t.Fatalf("recovered movie files = %+v, %v", stored.Files, err)
	}
	var ready bool
	if err := env.pool.QueryRow(ctx, `SELECT ready FROM download_library_files WHERE job_id = 'job-move'`).Scan(&ready); err != nil || !ready {
		t.Fatalf("recovered journal ready = %v, %v", ready, err)
	}

	// A destination whose content does not match the journal hash must not be trusted.
	mismatched := env.manualMovie(t, "Mismatched Film", 2016, true)
	badDestination := "Mismatched Film (2016) [imdb-]/Mismatched.Film.2016.1080p.mkv"
	env.writeLibraryFile(t, badDestination, "different-bytes")
	env.completedJob(t, "job-mismatch", "rel-mismatch", "Mismatched Film 2016 1080p BluRay", []downloads.OutputFile{
		{Name: "Mismatched.Film.2016.1080p.mkv", Size: 15, URL: "/api/v1/downloads/job-mismatch/file"},
	})
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO download_library_files (job_id, name, root_path, path, size, sha256, ready)
		 VALUES ('job-mismatch', 'Mismatched.Film.2016.1080p.mkv', $1, $2, 15, $3, false)`,
		env.rootPath(), badDestination, hex.EncodeToString(digest[:])); err != nil {
		t.Fatalf("insert mismatched journal row: %v", err)
	}
	if err := env.service.Store.SaveAcquisition(ctx, movies.Acquisition{
		MovieID: mismatched.ID, JobID: "job-mismatch", ReleaseID: "rel-mismatch", Title: "Mismatched Film 2016 1080p BluRay", Status: "importing",
	}); err != nil {
		t.Fatalf("SaveAcquisition mismatch: %v", err)
	}
	if _, err := env.service.SyncDownloads(ctx); err != nil {
		t.Fatalf("SyncDownloads with a mismatched hash: %v", err)
	}
	acquisitions, err := env.service.Store.Acquisitions(ctx)
	if err != nil {
		t.Fatalf("Acquisitions: %v", err)
	}
	for _, acquisition := range acquisitions {
		if acquisition.JobID == "job-mismatch" {
			if acquisition.Status != "import-failed" || acquisition.Error == "" {
				t.Fatalf("mismatched acquisition = %+v", acquisition)
			}
		}
	}
	for _, item := range mustList(t, env) {
		if item.ID == mismatched.ID && (item.Status != "import-failed" || len(item.Files) != 0) {
			t.Fatalf("mismatched movie = %+v", item)
		}
	}
}

func TestScanMatchesConservatively(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, "")
	matched := env.manualMovie(t, "Matched Film", 1999, true)
	env.manualMovie(t, "Ambiguous Film", 2001, true)
	env.manualMovie(t, "Ambiguous Film", 2001, false)
	unique := env.manualMovie(t, "Unique Film", 2002, true)
	known := env.manualMovie(t, "Known Film", 2003, true)

	env.writeLibraryFile(t, "Matched Film (1999) [imdb-tt1111111]/movie.mkv", "a")
	env.writeLibraryFile(t, "Ambiguous Film (2001)/movie.mkv", "b")
	env.writeLibraryFile(t, "Unique Film (2002)/movie.mkv", "c")
	knownFile := env.writeLibraryFile(t, "Known Film (2003)/movie.mkv", "d")
	known.Files = []movies.File{knownFile}
	if _, err := env.service.Store.Save(ctx, known); err != nil {
		t.Fatalf("Save known movie: %v", err)
	}

	candidates, err := env.service.Scan(ctx, env.rootID)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	byIMDbID := map[string]movies.Candidate{}
	byPath := map[string]movies.Candidate{}
	for _, candidate := range candidates {
		if candidate.IMDbID != "" {
			byIMDbID[candidate.IMDbID] = candidate
		}
		byPath[candidate.Path] = candidate
	}
	if match := byIMDbID["tt1111111"]; match.MatchedMovieID != matched.ID {
		t.Fatalf("IMDb-tagged candidate = %+v, want %s", match, matched.ID)
	}
	if match := byPath["Ambiguous Film (2001)/movie.mkv"]; match.MatchedMovieID != "" {
		t.Fatalf("ambiguous candidate was matched: %+v", match)
	}
	if match := byPath["Unique Film (2002)/movie.mkv"]; match.MatchedMovieID != unique.ID {
		t.Fatalf("unique candidate = %+v, want %s", match, unique.ID)
	}
	if match := byPath["Known Film (2003)/movie.mkv"]; match.MatchedMovieID != known.ID || match.Error == "" {
		t.Fatalf("known file candidate = %+v, want known movie with an error", match)
	}
	if _, err := env.service.Scan(ctx, "unknown-root"); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("unknown root scan error = %v, want ErrInvalid", err)
	}
}

func TestWatchlistSyncUsesRemoteLists(t *testing.T) {
	ctx := context.Background()
	omdb := omdbServer(t, map[string]omdbFixture{
		"tt0111161": {title: "The Shawshank Redemption", year: 1994, released: "14 Oct 1994"},
		"tt0068646": {title: "The Godfather", year: 1972, released: "24 Mar 1972"},
	})
	env := newTestEnv(t, "")
	cfg := env.config(t)
	cfg.MetadataURL = omdb.URL
	cfg.MetadataAPIKey = "omdb-secret"
	env.saveConfig(t, cfg)

	list := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"imdbId":"tt0111161"},{"imdbId":"tt0068646"},{"title":"tt0111161"}]`)
	}))
	t.Cleanup(list.Close)
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><body><a href="/title/tt0111161/">x</a></body></html>`)
	}))
	t.Cleanup(html.Close)

	saved, err := env.service.Store.SaveWatchlist(ctx, movies.Watchlist{
		Name: "Remote", URL: list.URL, Monitor: true, ProfileID: "hd", RootID: env.rootID, IntervalHours: 6,
	})
	if err != nil {
		t.Fatalf("SaveWatchlist: %v", err)
	}
	added, err := env.service.SyncWatchlist(ctx, saved.ID)
	if err != nil || added != 2 {
		t.Fatalf("SyncWatchlist = %d, %v", added, err)
	}
	if added, err := env.service.SyncWatchlist(ctx, saved.ID); err != nil || added != 0 {
		t.Fatalf("duplicate SyncWatchlist = %d, %v", added, err)
	}
	if len(mustList(t, env)) != 2 {
		t.Fatalf("watchlist sync created the wrong number of movies")
	}
	lists, err := env.service.Store.Watchlists(ctx)
	if err != nil || lists[0].LastSyncAt == nil || lists[0].Error != "" {
		t.Fatalf("watchlist state = %+v, %v", lists, err)
	}

	if _, err := env.service.Store.SaveWatchlist(ctx, movies.Watchlist{
		Name: "Scraped", URL: html.URL, Monitor: false, IntervalHours: 6,
	}); err != nil {
		t.Fatalf("SaveWatchlist html: %v", err)
	}
	var scrapedID string
	for _, item := range mustGetWatchlists(t, env) {
		if item.Name == "Scraped" {
			scrapedID = item.ID
		}
	}
	if _, err := env.service.SyncWatchlist(ctx, scrapedID); err == nil {
		t.Fatalf("HTML watchlist sync did not fail")
	}
	for _, item := range mustGetWatchlists(t, env) {
		if item.ID == scrapedID && item.Error == "" {
			t.Fatalf("HTML watchlist failure was not persisted: %+v", item)
		}
	}
}

func mustGetWatchlists(t *testing.T, env *testEnv) []movies.Watchlist {
	t.Helper()
	lists, err := env.service.Store.Watchlists(context.Background())
	if err != nil {
		t.Fatalf("Watchlists: %v", err)
	}
	return lists
}

func containsReason(reasons []string, fragment string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, fragment) {
			return true
		}
	}
	return false
}

func TestManualImportConnectionsCalendarAndRemove(t *testing.T) {
	ctx := context.Background()
	omdb := omdbServer(t, map[string]omdbFixture{})
	jellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/System/Info" || r.Header.Get("X-Emby-Token") != "jellyfin-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(jellyfin.Close)

	env := newTestEnv(t, "")
	cfg := env.config(t)
	cfg.MetadataURL = omdb.URL
	cfg.MetadataAPIKey = "omdb-secret"
	cfg.JellyfinURL = jellyfin.URL
	cfg.JellyfinAPIKey = "jellyfin-secret"
	env.saveConfig(t, cfg)

	tests, err := env.service.TestConnections(ctx)
	if err != nil || !tests.Metadata.OK || !tests.Jellyfin.OK {
		t.Fatalf("TestConnections = %+v, %v", tests, err)
	}
	broken := env.config(t)
	broken.JellyfinURL = "http://127.0.0.1:1"
	if _, err := env.service.SetConfig(ctx, broken); err != nil {
		t.Fatalf("SetConfig with an unreachable Jellyfin: %v", err)
	}
	if tests, err := env.service.TestConnections(ctx); err != nil || tests.Jellyfin.OK || tests.Jellyfin.Error == "" {
		t.Fatalf("unreachable Jellyfin test = %+v, %v", tests, err)
	}
	restored := env.config(t)
	restored.JellyfinURL = jellyfin.URL
	env.saveConfig(t, restored)

	movie := env.manualMovie(t, "Manual Import", 2010, true)
	sourcePath := "Manual Import (2010)/movie.mkv"
	env.writeLibraryFile(t, sourcePath, "manual-bytes")
	imported, err := env.service.Import(ctx, movies.ImportInput{RootID: env.rootID, Path: sourcePath, MovieID: movie.ID})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(imported.Files) != 1 || imported.Files[0].Path == sourcePath || imported.Files[0].Size != int64(len("manual-bytes")) {
		t.Fatalf("imported movie files = %+v", imported.Files)
	}
	if handle, err := env.service.OpenFile(ctx, movie.ID, imported.Files[0].Path); err != nil {
		t.Fatalf("OpenFile imported: %v", err)
	} else {
		handle.Close()
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(sourcePath))); err != nil {
		t.Fatalf("manual import removed an unrelated original: %v", err)
	}
	if _, err := env.service.Import(ctx, movies.ImportInput{RootID: env.rootID, Path: "../escape.mkv", MovieID: movie.ID}); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("traversal import error = %v, want ErrInvalid", err)
	}

	// Calendar keeps only real release dates in order.
	later := env.manualMovie(t, "Later Film", time.Now().UTC().Year()+1, true)
	undated, err := env.service.Add(ctx, movies.AddInput{
		Metadata: metadata.Title{Title: "Undated Film", Year: 2005}, Monitored: true, ProfileID: "hd", RootID: env.rootID,
	})
	if err != nil {
		t.Fatalf("Add undated: %v", err)
	}
	releases, err := env.service.Calendar(ctx)
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	foundLater, foundUndated := false, false
	for i, item := range releases {
		if item.ID == later.ID {
			foundLater = true
		}
		if item.ID == undated.ID {
			foundUndated = true
		}
		if i > 0 {
			previous, _ := parseCalendarDate(releases[i-1].Metadata.Released)
			current, _ := parseCalendarDate(item.Metadata.Released)
			if previous.After(current) {
				t.Fatalf("calendar is not sorted: %v", releases)
			}
		}
	}
	if !foundLater || foundUndated {
		t.Fatalf("calendar contents = later:%v undated:%v", foundLater, foundUndated)
	}

	// Remove archives owned files instead of deleting them.
	removed := env.manualMovie(t, "Removable Film", 2012, true)
	removedFile := env.writeLibraryFile(t, "Removable Film (2012)/movie.mkv", "remove-me")
	removed.Files = []movies.File{removedFile}
	if _, err := env.service.Store.Save(ctx, removed); err != nil {
		t.Fatalf("Save removable movie: %v", err)
	}
	if err := env.service.Remove(ctx, removed.ID, true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := env.service.Store.Get(ctx, removed.ID); !errors.Is(err, movies.ErrNotFound) {
		t.Fatalf("removed movie error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(env.rootPath(), filepath.FromSlash(removedFile.Path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed movie file still exists")
	}
	if matches, err := filepath.Glob(filepath.Join(env.rootPath(), ".recycle", "Removable Film (2012)", "*")); err != nil || len(matches) != 1 {
		t.Fatalf("archived files = %v, %v", matches, err)
	}
}

func parseCalendarDate(value string) (time.Time, error) {
	return time.Parse("2006-01-02", value)
}

func TestGrabFallsBackToFreshRSSFeed(t *testing.T) {
	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("apikey") == "" {
			http.Error(w, "missing api key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		if query.Get("t") == "get" {
			_, _ = io.WriteString(w, `<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`)
			return
		}
		item := ""
		if query.Get("q") == "" {
			item = `<item><title>RSS Film 2021 1080p BluRay x264-GRP</title><guid isPermaLink="false">rel-rss</guid><pubDate>Fri, 02 Jan 2026 10:00:00 +0000</pubDate><enclosure url="https://example.invalid/rel-rss.nzb" length="8589934592"/><newznab:attr name="size" value="8589934592"/><newznab:attr name="imdb" value="tt50505050"/></item>`
		}
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>`+item+`</channel></rss>`)
	}))
	t.Cleanup(server.Close)

	env := newTestEnv(t, server.URL)
	movie := env.manualMovie(t, "RSS Film", 2021, true)
	job, err := env.service.Grab(ctx, movie.ID, "rel-rss", false)
	if err != nil || job.ID == "" {
		t.Fatalf("RSS grab = %+v, %v", job, err)
	}
	if _, err := env.service.Grab(ctx, movie.ID, "rel-unknown", false); !errors.Is(err, movies.ErrInvalid) {
		t.Fatalf("unknown release error = %v, want ErrInvalid", err)
	}
}

func TestUpgradeImportUsesProfileCutoff(t *testing.T) {
	ctx := context.Background()
	indexer := indexerServer(t, map[string][]releaseFixture{
		"40404040": {
			{title: "Upgrade Film 2020 1080p BluRay x264-GRP", guid: "rel-upgrade", size: 8 << 30, imdb: "tt40404040"},
			{title: "Upgrade Film 2020 720p WEB-DL x264-GRP", guid: "rel-downgrade", size: 2 << 30, imdb: "tt40404040"},
			{title: "Upgrade Film 2020 1080p WEB-DL x264-GRP", guid: "rel-same", size: 4 << 30, imdb: "tt40404040"},
		},
	})
	env := newTestEnv(t, indexer.URL)
	omdb := omdbServer(t, map[string]omdbFixture{
		"tt40404040": {title: "Upgrade Film", year: 2020, released: "01 Jun 2020"},
	})
	cfg := env.config(t)
	cfg.MetadataURL = omdb.URL
	cfg.MetadataAPIKey = "omdb-secret"
	env.saveConfig(t, cfg)
	// A custom profile keeps the upgrade decision explicit and independent of defaults.
	if _, err := env.service.Store.SaveProfile(ctx, quality.Profile{
		ID: "custom", Name: "Custom HD", Qualities: []string{"Bluray-1080p", "WEB-1080p", "WEB-720p"},
		Cutoff: "Bluray-1080p", Upgrade: true,
	}); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	movie, err := env.service.Add(ctx, movies.AddInput{IMDbID: "tt40404040", Monitored: true, ProfileID: "custom", RootID: env.rootID})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	file := env.writeLibraryFile(t, "Upgrade Film (2020)/Upgrade Film (2020) [WEB-720p].mkv", "existing")
	file.Quality, file.Score = "WEB-720p", 0
	movie.Files = []movies.File{file}
	if _, err := env.service.Store.Save(ctx, movie); err != nil {
		t.Fatalf("Save with an existing file: %v", err)
	}
	for _, item := range mustList(t, env) {
		if item.ID == movie.ID && item.Status != "cutoff-unmet" {
			t.Fatalf("movie status = %q, want cutoff-unmet", item.Status)
		}
	}
	releases, err := env.service.Search(ctx, movie.ID)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	byID := map[string]movies.Release{}
	for _, release := range releases {
		byID[release.ID] = release
	}
	if !byID["rel-upgrade"].Decision.Allowed || !byID["rel-upgrade"].Decision.Upgrade {
		t.Fatalf("upgrade release was not allowed: %+v", byID["rel-upgrade"].Decision)
	}
	if !byID["rel-same"].Decision.Allowed || !byID["rel-same"].Decision.Upgrade {
		t.Fatalf("same-quality upgrade release was not allowed: %+v", byID["rel-same"].Decision)
	}
	if byID["rel-downgrade"].Decision.Allowed {
		t.Fatalf("downgrade release was allowed: %+v", byID["rel-downgrade"].Decision)
	}
	if _, err := env.service.Grab(ctx, movie.ID, "rel-downgrade", false); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("downgrade grab error = %v, want ErrConflict", err)
	}
	if _, err := env.service.Grab(ctx, movie.ID, "rel-upgrade", false); err != nil {
		t.Fatalf("upgrade grab: %v", err)
	}

	// Once the cutoff is met, further grabs are refused without an explicit override.
	cutoff := env.writeLibraryFile(t, "Upgrade Film (2020)/Upgrade Film (2020) [Bluray-1080p].mkv", "cutoff")
	cutoff.Quality, cutoff.Score = "Bluray-1080p", 0
	stored, err := env.service.Store.Get(ctx, movie.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stored.Files = []movies.File{cutoff}
	if _, err := env.service.Store.Save(ctx, stored); err != nil {
		t.Fatalf("Save cutoff file: %v", err)
	}
	if _, err := env.service.Grab(ctx, movie.ID, "rel-same", false); !errors.Is(err, movies.ErrConflict) {
		t.Fatalf("grab above cutoff error = %v, want ErrConflict", err)
	}
}
