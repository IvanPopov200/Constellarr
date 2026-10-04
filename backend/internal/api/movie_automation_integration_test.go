package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

func clearMovieAutomationEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"OMDB_URL", "OMDB_API_KEY", "JELLYFIN_URL", "JELLYFIN_API_KEY", "IMPORT_WEBHOOK_URL"} {
		t.Setenv(name, "")
	}
}

const automationNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head><meta type="title">Synthetic automation release</meta></head>
  <file poster="poster &lt;poster@example.com&gt;" date="1136214245" subject="[1/1] - &quot;movie.mkv&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="1024" number="1">synthetic-part-1@example.com</segment></segments>
  </file>
</nzb>`

type automationIndexer struct {
	*httptest.Server
	mu      sync.Mutex
	calls   map[string]int
	lastGet string
	rss     string
	search  map[string]string
}

func newAutomationIndexer(t *testing.T, rss string, search map[string]string) *automationIndexer {
	t.Helper()
	fixture := &automationIndexer{calls: map[string]int{}, rss: rss, search: search}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("apikey") != indexerAPIKey {
			http.Error(w, "unexpected api key", http.StatusUnauthorized)
			return
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.calls[query.Get("t")]++
		switch query.Get("t") {
		case "search":
			fmt.Fprint(w, fixture.rss)
		case "movie":
			feed, ok := fixture.search[query.Get("imdbid")]
			if !ok {
				// The client strips the tt prefix from IMDb IDs.
				feed = fixture.search["tt"+query.Get("imdbid")]
			}
			if feed == "" {
				feed = automationRSS()
			}
			fmt.Fprint(w, feed)
		case "get":
			fixture.lastGet = query.Get("id")
			w.Header().Set("Content-Type", "application/x-nzb")
			fmt.Fprint(w, automationNZB)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.Server.Close)
	return fixture
}

func (f *automationIndexer) count(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[kind]
}

func automationRSS(items ...string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel>` +
		strings.Join(items, "") + `</channel></rss>`
}

func automationItem(id, title, imdb string, size int64) string {
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	return fmt.Sprintf(`<item><title>%s</title><guid isPermaLink="false">%s</guid>`+
		`<pubDate>Fri, 02 Jan 2026 10:00:00 +0000</pubDate>`+
		`<enclosure url="https://indexer.example/%s.nzb" length="%d"/>`+
		`<newznab:attr name="size" value="%d"/><newznab:attr name="imdb" value="%s"/></item>`,
		escape(title), escape(id), escape(id), size, size, escape(imdb))
}

func automationProfile(t *testing.T, handler http.Handler, name string, qualities []string, cutoff string) quality.Profile {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"name": name, "qualities": qualities, "cutoff": cutoff, "upgrade": true,
	})
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	status, raw := request(t, handler, http.MethodPut, "/api/v1/movie-profiles", string(encoded), nil)
	if status != http.StatusOK {
		t.Fatalf("PUT movie-profile: status %d, body %s", status, raw)
	}
	var profile quality.Profile
	if err := json.Unmarshal(raw, &profile); err != nil || profile.ID == "" {
		t.Fatalf("profile response = %s, %v", raw, err)
	}
	return profile
}

func addAutomationMovie(t *testing.T, handler http.Handler, imdbID, title string, year int, released string, monitored bool, profileID string) movies.Movie {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"imdbId": imdbID,
		"metadata": map[string]any{
			"imdbId": imdbID, "title": title, "year": year, "type": "movie", "released": released,
		},
		"monitored": monitored,
		"profileId": profileID,
	})
	if err != nil {
		t.Fatalf("marshal movie body: %v", err)
	}
	status, raw := request(t, handler, http.MethodPost, "/api/v1/movies", string(encoded), nil)
	if status != http.StatusCreated {
		t.Fatalf("POST movie: status %d, body %s", status, raw)
	}
	var movie movies.Movie
	if err := json.Unmarshal(raw, &movie); err != nil {
		t.Fatalf("movie response is not JSON: %v (%s)", err, raw)
	}
	return movie
}

func getAutomationMovie(t *testing.T, handler http.Handler, id string) movies.Movie {
	t.Helper()
	for _, movie := range listAutomationMovies(t, handler) {
		if movie.ID == id {
			return movie
		}
	}
	t.Fatalf("movie %s is missing from the catalog", id)
	return movies.Movie{}
}

func listAutomationMovies(t *testing.T, handler http.Handler) []movies.Movie {
	t.Helper()
	status, raw := request(t, handler, http.MethodGet, "/api/v1/movies", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movies: status %d, body %s", status, raw)
	}
	var list []movies.Movie
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("movie list is not JSON: %v (%s)", err, raw)
	}
	return list
}

func syncAutomation(t *testing.T, service *movies.Service, force bool) movies.SyncResult {
	t.Helper()
	ctx := context.Background()
	for attempt := 0; ; attempt++ {
		result, err := service.Sync(ctx, force)
		if err == nil {
			return result
		}
		// The automation advisory lock is database-wide, so a parallel suite can hold it briefly.
		if !errors.Is(err, movies.ErrConflict) || attempt >= 20 {
			t.Fatalf("movies.Sync: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func automationCount(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestMovieAutomationRSSGrabUsesProfileOrder(t *testing.T) {
	clearMovieAutomationEnv(t)
	const (
		webReleaseID = "matrix-web-720"
		blurayID     = "matrix-bluray-1080"
		webTitle     = "The Matrix 1999 720p WEB-DL x264"
		blurayTitle  = "The Matrix 1999 1080p BluRay x264"
	)
	now := time.Now().UTC()
	futureYear := now.Year()
	futureReleased := now.AddDate(0, 1, 0).Format("2006-01-02")
	matrix := automationRSS(
		automationItem(blurayID, blurayTitle, "tt0133093", 2<<30),
		automationItem(webReleaseID, webTitle, "tt0133093", 800<<20),
	)
	indexer := newAutomationIndexer(t, automationRSS(
		automationItem(blurayID, blurayTitle, "tt0133093", 2<<30),
		automationItem(webReleaseID, webTitle, "tt0133093", 800<<20),
		automationItem("gump-bluray-1080", "Forrest Gump 1994 1080p BluRay x264", "tt0109830", 2<<30),
		automationItem("dune-bluray-2160", fmt.Sprintf("Dune Part Two %d 2160p BluRay x265", futureYear), "tt1160419", 12<<30),
	), map[string]string{"tt0133093": matrix})

	pool, _, _, handler := movieEnvironment(t, downloads.Config{
		IndexerURL: indexer.URL, APIKey: indexerAPIKey, Directory: t.TempDir(),
	})
	profile := automationProfile(t, handler, "Automation Web First", []string{"WEB-720p", "Bluray-1080p"}, "Bluray-1080p")
	released := addAutomationMovie(t, handler, "tt0133093", "The Matrix", 1999, "1999-03-31", true, profile.ID)
	unmonitored := addAutomationMovie(t, handler, "tt0109830", "Forrest Gump", 1994, "1994-07-06", false, profile.ID)
	future := addAutomationMovie(t, handler, "tt1160419", "Dune: Part Two", futureYear, futureReleased, true, profile.ID)

	status, raw := request(t, handler, http.MethodPost, "/api/v1/movies/sync", "", nil)
	if status != http.StatusOK {
		t.Fatalf("POST movies/sync: status %d, body %s", status, raw)
	}
	var result movies.SyncResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("sync result is not JSON: %v (%s)", err, raw)
	}
	if result.Queued != 1 || result.Searched != 0 || result.Imported != 0 {
		t.Fatalf("sync result = %+v, want exactly one RSS grab without a scheduled search", result)
	}
	if indexer.count("search") != 1 || indexer.count("movie") != 1 || indexer.count("get") != 1 {
		t.Fatalf("indexer calls = search %d, movie %d, get %d; want the RSS feed, one confirmation search, and one NZB",
			indexer.count("search"), indexer.count("movie"), indexer.count("get"))
	}
	if indexer.lastGet != webReleaseID {
		t.Fatalf("NZB fetched for %q, want the profile-preferred %q", indexer.lastGet, webReleaseID)
	}
	if stored := getAutomationMovie(t, handler, released.ID); stored.LastSearchAt != nil {
		t.Fatalf("the RSS grab also ran a scheduled search: lastSearchAt = %v", stored.LastSearchAt)
	}
	if stored := getAutomationMovie(t, handler, unmonitored.ID); stored.Status != "unmonitored" || stored.LastSearchAt != nil {
		t.Fatalf("unmonitored movie = %+v", stored)
	}
	if stored := getAutomationMovie(t, handler, future.ID); stored.Status != "missing" || stored.LastSearchAt != nil {
		t.Fatalf("future movie = %+v", stored)
	}

	ctx := context.Background()
	var releaseID, jobStatus string
	var adopted bool
	if err := pool.QueryRow(ctx, `SELECT release_id, status, movie_adopted FROM downloads`).
		Scan(&releaseID, &jobStatus, &adopted); err != nil {
		t.Fatalf("read queued download: %v", err)
	}
	if releaseID != webReleaseID || jobStatus != "queued" || !adopted {
		t.Fatalf("download = release %q, status %q, adopted %v", releaseID, jobStatus, adopted)
	}
	var movieID, acquisitionStatus, acquisitionReleaseID, qualityID string
	var rank int
	if err := pool.QueryRow(ctx,
		`SELECT movie_id, status, release->>'releaseId', release->'decision'->'details'->>'quality', (release->'decision'->>'rank')::int
		 FROM movie_acquisitions`).
		Scan(&movieID, &acquisitionStatus, &acquisitionReleaseID, &qualityID, &rank); err != nil {
		t.Fatalf("read acquisition: %v", err)
	}
	if movieID != released.ID || acquisitionStatus != "queued" || acquisitionReleaseID != webReleaseID || qualityID != "WEB-720p" || rank != 0 {
		t.Fatalf("acquisition = movie %s, status %s, release %s, quality %s, rank %d",
			movieID, acquisitionStatus, acquisitionReleaseID, qualityID, rank)
	}
	if count := automationCount(t, pool, `SELECT count(*) FROM downloads`); count != 1 {
		t.Fatalf("downloads = %d, want only the monitored released movie queued", count)
	}
	if count := automationCount(t, pool, `SELECT count(*) FROM movie_acquisitions`); count != 1 {
		t.Fatalf("acquisitions = %d, want one", count)
	}
}

func TestMovieAutomationDueStatePersistsAcrossRestart(t *testing.T) {
	clearMovieAutomationEnv(t)
	const matrixIMDb = "tt0133093"
	indexer := newAutomationIndexer(t, automationRSS(),
		map[string]string{matrixIMDb: automationRSS(
			automationItem("matrix-web-2160", "The Matrix 1999 2160p WEB-DL x264", matrixIMDb, 6<<30),
		)})
	directory := t.TempDir()
	pool, _, service, handler := movieEnvironment(t, downloads.Config{
		IndexerURL: indexer.URL, APIKey: indexerAPIKey, Directory: directory,
	})
	profile := automationProfile(t, handler, "Automation 1080p", []string{"Bluray-1080p"}, "Bluray-1080p")
	movie := addAutomationMovie(t, handler, matrixIMDb, "The Matrix", 1999, "1999-03-31", true, profile.ID)

	result := syncAutomation(t, service, false)
	if result.Searched != 1 || result.Queued != 0 || result.Imported != 0 {
		t.Fatalf("first sync = %+v, want one due scheduled search and no grab", result)
	}
	if indexer.count("search") != 1 || indexer.count("movie") != 1 || indexer.count("get") != 0 {
		t.Fatalf("first sync indexer calls = search %d, movie %d, get %d",
			indexer.count("search"), indexer.count("movie"), indexer.count("get"))
	}
	stored := getAutomationMovie(t, handler, movie.ID)
	if stored.LastSearchAt == nil || stored.Status != "wanted" {
		t.Fatalf("movie after the scheduled search = %+v", stored)
	}
	if count := automationCount(t, pool, `SELECT count(*) FROM movie_automation`); count != 1 {
		t.Fatalf("automation tasks = %d, want only rss", count)
	}

	restartedManager, err := downloads.New(context.Background(), pool, downloads.Config{
		IndexerURL: indexer.URL, APIKey: indexerAPIKey, Directory: directory,
	})
	if err != nil {
		t.Fatalf("downloads.New after restart: %v", err)
	}
	t.Cleanup(restartedManager.Close)
	restarted, err := movies.New(context.Background(), pool, restartedManager)
	if err != nil {
		t.Fatalf("movies.New after restart: %v", err)
	}
	t.Cleanup(restarted.Close)

	result = syncAutomation(t, restarted, false)
	if result.Searched != 0 || result.Queued != 0 || result.Imported != 0 {
		t.Fatalf("sync after restart = %+v, want no repeated RSS or scheduled search", result)
	}
	if indexer.count("search") != 1 || indexer.count("movie") != 1 || indexer.count("get") != 0 {
		t.Fatalf("sync after restart re-ran automation: search %d, movie %d, get %d",
			indexer.count("search"), indexer.count("movie"), indexer.count("get"))
	}
	var rssLastRun time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT last_run FROM movie_automation WHERE name = 'rss'`).Scan(&rssLastRun); err != nil {
		t.Fatalf("read rss task state: %v", err)
	}
	if rssLastRun.IsZero() || time.Since(rssLastRun) > time.Minute {
		t.Fatalf("rss task state = %v, want the run persisted by the first sync", rssLastRun)
	}
	status, raw := request(t, handler, http.MethodGet, "/api/v1/movie-config", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movie-config: status %d, body %s", status, raw)
	}
	var cfg movies.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("movie config is not JSON: %v (%s)", err, raw)
	}
	if cfg.MetadataConfigured || cfg.MetadataAPIKey != "" {
		t.Fatalf("metadata provider is enabled: %+v", cfg)
	}
	if count := automationCount(t, pool, `SELECT count(*) FROM movie_automation`); count != 1 {
		t.Fatalf("automation tasks after restart = %d, want only the persisted rss task", count)
	}
}

func TestMovieAutomationLegacyImportAdoptedAcrossCatalogDeletion(t *testing.T) {
	clearMovieAutomationEnv(t)
	const (
		jobID     = "legacy-completed-job"
		releaseID = "legacy-completed-release"
		fileName  = "The.Matrix.1999.1080p.BluRay.x264.mkv"
	)
	content := "synthetic legacy movie bytes"
	directory := t.TempDir()
	pool, manager, service, handler := movieEnvironment(t, downloads.Config{Directory: directory})
	ctx := context.Background()

	outputDir, err := manager.OutputDirectory(jobID)
	if err != nil {
		t.Fatalf("OutputDirectory: %v", err)
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatalf("create download output: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, fileName), []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture video: %v", err)
	}
	files, err := json.Marshal([]downloads.OutputFile{{
		Name: fileName, Size: int64(len(content)),
		URL: "/api/v1/downloads/" + jobID + "/file?name=" + url.QueryEscape(fileName),
	}})
	if err != nil {
		t.Fatalf("marshal output files: %v", err)
	}
	// A completed legacy download has no acquisition row yet, so the catalog must adopt it once.
	if _, err := pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb, status, files) VALUES ($1, $2, $3, $4, 'completed', $5::jsonb)`,
		jobID, releaseID, "The Matrix 1999 1080p BluRay x264", []byte(automationNZB), string(files)); err != nil {
		t.Fatalf("insert completed download: %v", err)
	}

	imported, err := service.SyncDownloads(ctx)
	if err != nil || imported != 1 {
		t.Fatalf("first SyncDownloads = %d, %v; want the legacy import", imported, err)
	}
	list := listAutomationMovies(t, handler)
	if len(list) != 1 || list[0].Monitored || len(list[0].Files) != 1 ||
		list[0].Metadata.Title != "The Matrix" || list[0].Metadata.Year != 1999 {
		t.Fatalf("catalog after the legacy import = %+v", list)
	}
	movie := list[0]
	if _, err := os.Stat(filepath.Join(directory, "library", "movies", filepath.FromSlash(movie.Files[0].Path))); err != nil {
		t.Fatalf("imported video is missing: %v", err)
	}
	var adopted bool
	if err := pool.QueryRow(ctx, `SELECT movie_adopted FROM downloads WHERE id = $1`, jobID).Scan(&adopted); err != nil || !adopted {
		t.Fatalf("movie_adopted = %v, %v; want the adopted legacy download", adopted, err)
	}
	if count := automationCount(t, pool,
		`SELECT count(*) FROM movie_acquisitions WHERE movie_id = $1 AND status = 'imported'`, movie.ID); count != 1 {
		t.Fatalf("imported acquisitions = %d, want one", count)
	}
	if again, err := service.SyncDownloads(ctx); err != nil || again != 0 {
		t.Fatalf("second SyncDownloads = %d, %v; want the import to be idempotent", again, err)
	}

	if status, raw := request(t, handler, http.MethodDelete, "/api/v1/movies/"+url.PathEscape(movie.ID), "", nil); status != http.StatusOK {
		t.Fatalf("DELETE movie: status %d, body %s", status, raw)
	}
	if count := automationCount(t, pool, `SELECT count(*) FROM movie_acquisitions`); count != 0 {
		t.Fatalf("acquisitions after catalog deletion = %d, want the cascade", count)
	}
	unlinked, err := manager.UnlinkedMovies(ctx)
	if err != nil || len(unlinked) != 0 {
		t.Fatalf("unlinked movies after catalog deletion = %+v, %v", unlinked, err)
	}
	if imported, err = service.SyncDownloads(ctx); err != nil || imported != 0 {
		t.Fatalf("SyncDownloads after catalog deletion = %d, %v; want the adopted download skipped", imported, err)
	}
	if count := automationCount(t, pool, `SELECT count(*) FROM movies`); count != 0 {
		t.Fatalf("movies after catalog deletion = %d, want no recreation", count)
	}
	if count := automationCount(t, pool, `SELECT count(*) FROM downloads`); count != 1 {
		t.Fatalf("downloads after catalog deletion = %d, want the original job", count)
	}
	if err := pool.QueryRow(ctx, `SELECT movie_adopted FROM downloads WHERE id = $1`, jobID).Scan(&adopted); err != nil || !adopted {
		t.Fatalf("movie_adopted after catalog deletion = %v, %v; want true", adopted, err)
	}
}
