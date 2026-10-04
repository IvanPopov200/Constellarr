package api_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

	"github.com/IvanPopov200/Constellarr/backend/internal/api"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

func movieEnvironment(t *testing.T, downloadConfig downloads.Config) (*pgxpool.Pool, *downloads.Manager, *movies.Service, http.Handler) {
	t.Helper()
	for _, name := range []string{"OMDB_URL", "OMDB_API_KEY", "JELLYFIN_URL", "JELLYFIN_API_KEY", "IMPORT_WEBHOOK_URL"} {
		t.Setenv(name, "")
	}
	pool := testSchema(t)
	ctx := context.Background()
	if downloadConfig.Directory == "" {
		downloadConfig.Directory = t.TempDir()
	}
	manager, err := downloads.New(ctx, pool, downloadConfig)
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	service, err := movies.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	t.Cleanup(service.Close)
	return pool, manager, service, api.New(pool, api.Services{Downloads: manager, Movies: service})
}

func TestMovieAPICRUDLifecycle(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	body := `{"imdbId":"tt0133093","metadata":{"imdbId":"tt0133093","title":"The Matrix","year":1999,"type":"movie","released":"1999-03-31","genres":["Action"]},"monitored":true,"tags":["favorite"],"collection":"The Matrix"}`
	status, raw := request(t, handler, http.MethodPost, "/api/v1/movies", body, nil)
	if status != http.StatusCreated {
		t.Fatalf("POST movies: status %d, body %s", status, raw)
	}
	var movie movies.Movie
	if err := json.Unmarshal(raw, &movie); err != nil {
		t.Fatalf("movie response is not JSON: %v (%s)", err, raw)
	}
	if movie.ID == "" || movie.Metadata.Title != "The Matrix" || movie.Metadata.Year != 1999 || !movie.Monitored ||
		movie.Collection != "The Matrix" || strings.Join(movie.Tags, ",") != "favorite" {
		t.Fatalf("created movie = %+v", movie)
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movies: status %d, body %s", status, raw)
	}
	var list []movies.Movie
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 || list[0].ID != movie.ID {
		t.Fatalf("movie list = %s, %v; want the created movie", raw, err)
	}

	movie.Monitored = false
	movie.Tags = []string{"classic", "rewatch"}
	encoded, err := json.Marshal(movie)
	if err != nil {
		t.Fatalf("marshal movie: %v", err)
	}
	status, raw = request(t, handler, http.MethodPut, "/api/v1/movies/"+url.PathEscape(movie.ID), string(encoded), nil)
	if status != http.StatusOK {
		t.Fatalf("PUT movie: status %d, body %s", status, raw)
	}
	var updated movies.Movie
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("updated movie is not JSON: %v (%s)", err, raw)
	}
	if updated.ID != movie.ID || updated.Monitored || strings.Join(updated.Tags, ",") != "classic,rewatch" {
		t.Fatalf("updated movie = %+v", updated)
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies/"+url.PathEscape(movie.ID)+"/history", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movie history: status %d, body %s", status, raw)
	}
	var history []movies.History
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatalf("movie history is not JSON: %v (%s)", err, raw)
	}
	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies/history", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET all history: status %d, body %s", status, raw)
	}
	if err := json.Unmarshal(raw, &history); err != nil {
		t.Fatalf("all history is not JSON: %v (%s)", err, raw)
	}

	status, raw = request(t, handler, http.MethodDelete, "/api/v1/movies/"+url.PathEscape(movie.ID), "", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("DELETE movie: status %d, body %s", status, raw)
	}
	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies", "", nil)
	if status != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("movie list after delete: status %d, body %s", status, raw)
	}
	status, raw = request(t, handler, http.MethodDelete, "/api/v1/movies/"+url.PathEscape(movie.ID), "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("DELETE of a missing movie: status %d, body %s; want 404", status, raw)
	}
	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies/"+url.PathEscape(movie.ID)+"/history", "", nil)
	if status != http.StatusNotFound {
		t.Fatalf("history of a missing movie: status %d, body %s; want 404", status, raw)
	}
}

func TestMovieAPIDuplicateIMDb(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	first := addSyntheticMovie(t, handler, "tt0133093", "The Matrix", 1999)
	status, raw := request(t, handler, http.MethodPost, "/api/v1/movies",
		`{"imdbId":"tt0133093","metadata":{"title":"The Matrix Reloaded","year":2003},"monitored":true}`, nil)
	switch status {
	case http.StatusCreated, http.StatusOK:
		var duplicate movies.Movie
		if err := json.Unmarshal(raw, &duplicate); err != nil {
			t.Fatalf("duplicate response is not JSON: %v (%s)", err, raw)
		}
		if duplicate.ID != first.ID {
			t.Fatalf("duplicate IMDb created movie %s alongside %s", duplicate.ID, first.ID)
		}
		if duplicate.Metadata.Title != "The Matrix" {
			t.Fatalf("duplicate IMDb replaced saved metadata with %q", duplicate.Metadata.Title)
		}
	case http.StatusConflict:
	default:
		t.Fatalf("duplicate IMDb: status %d, body %s; want the existing movie or 409", status, raw)
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies", "", nil)
	var list []movies.Movie
	if status != http.StatusOK {
		t.Fatalf("GET movies: status %d, body %s", status, raw)
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 {
		t.Fatalf("movie list = %s, %v; want exactly one movie for a duplicate IMDb add", raw, err)
	}
}

func TestMovieProfileAPI(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	status, raw := request(t, handler, http.MethodGet, "/api/v1/movie-profiles", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movie-profiles: status %d, body %s", status, raw)
	}
	var profiles []quality.Profile
	if err := json.Unmarshal(raw, &profiles); err != nil || len(profiles) == 0 {
		t.Fatalf("seeded movie profiles = %s, %v", raw, err)
	}

	profile := profiles[0]
	profile.ID = ""
	profile.Name = "Synthetic Profile " + rand.Text()
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	status, raw = request(t, handler, http.MethodPut, "/api/v1/movie-profiles", string(encoded), nil)
	if status != http.StatusOK {
		t.Fatalf("PUT movie-profile: status %d, body %s", status, raw)
	}
	var saved quality.Profile
	if err := json.Unmarshal(raw, &saved); err != nil || saved.ID == "" || saved.Name != profile.Name {
		t.Fatalf("saved profile = %s, %v", raw, err)
	}

	// The same name under a new ID is a conflict, not a silent rename.
	conflict := profile
	conflict.ID = ""
	encoded, err = json.Marshal(conflict)
	if err != nil {
		t.Fatalf("marshal conflicting profile: %v", err)
	}
	if status, raw = request(t, handler, http.MethodPut, "/api/v1/movie-profiles", string(encoded), nil); status != http.StatusConflict {
		t.Fatalf("PUT duplicate profile name: status %d, body %s; want 409", status, raw)
	}

	saved.Upgrade = !saved.Upgrade
	encoded, err = json.Marshal(saved)
	if err != nil {
		t.Fatalf("marshal updated profile: %v", err)
	}
	if status, raw = request(t, handler, http.MethodPut, "/api/v1/movie-profiles", string(encoded), nil); status != http.StatusOK {
		t.Fatalf("PUT updated profile: status %d, body %s", status, raw)
	}
	var updated quality.Profile
	if err := json.Unmarshal(raw, &updated); err != nil || updated.ID != saved.ID || updated.Upgrade != saved.Upgrade {
		t.Fatalf("updated profile = %s, %v", raw, err)
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movie-profiles", "", nil)
	var listed []quality.Profile
	if status != http.StatusOK {
		t.Fatalf("GET movie-profiles: status %d, body %s", status, raw)
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("profile list is not JSON: %v (%s)", err, raw)
	}
	found := false
	for _, item := range listed {
		if item.ID == saved.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved profile %s is missing from %s", saved.ID, raw)
	}

	status, raw = request(t, handler, http.MethodDelete, "/api/v1/movie-profiles/"+url.PathEscape(saved.ID), "", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("DELETE movie-profile: status %d, body %s", status, raw)
	}
	if status, raw = request(t, handler, http.MethodDelete, "/api/v1/movie-profiles/"+url.PathEscape(saved.ID), "", nil); status != http.StatusNotFound {
		t.Fatalf("DELETE of a missing profile: status %d, body %s; want 404", status, raw)
	}

	if status, raw = request(t, handler, http.MethodPut, "/api/v1/movie-profiles", `{"name":""}`, nil); status != http.StatusBadRequest {
		t.Fatalf("PUT profile without a name: status %d, body %s; want 400", status, raw)
	}
}

func TestMovieWatchlistAPI(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	body := `{"name":"Synthetic Watchlist ` + rand.Text() + `","imdbIds":["tt0133093"],"monitor":true,"intervalHours":24}`
	status, raw := request(t, handler, http.MethodPut, "/api/v1/movie-watchlists", body, nil)
	if status != http.StatusOK {
		t.Fatalf("PUT movie-watchlist: status %d, body %s", status, raw)
	}
	var saved movies.Watchlist
	if err := json.Unmarshal(raw, &saved); err != nil || saved.ID == "" || !saved.Monitor || len(saved.IMDbIDs) != 1 {
		t.Fatalf("saved watchlist = %s, %v", raw, err)
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movie-watchlists", "", nil)
	var listed []movies.Watchlist
	if status != http.StatusOK {
		t.Fatalf("GET movie-watchlists: status %d, body %s", status, raw)
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("watchlist response is not JSON: %v (%s)", err, raw)
	}
	found := false
	for _, item := range listed {
		if item.ID == saved.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved watchlist %s is missing from %s", saved.ID, raw)
	}

	saved.Monitor = false
	encoded, err := json.Marshal(saved)
	if err != nil {
		t.Fatalf("marshal watchlist: %v", err)
	}
	if status, raw = request(t, handler, http.MethodPut, "/api/v1/movie-watchlists", string(encoded), nil); status != http.StatusOK {
		t.Fatalf("PUT updated watchlist: status %d, body %s", status, raw)
	}
	if status, raw = request(t, handler, http.MethodDelete, "/api/v1/movie-watchlists/"+url.PathEscape(saved.ID), "", nil); status != http.StatusOK {
		t.Fatalf("DELETE movie-watchlist: status %d, body %s", status, raw)
	}
	if status, raw = request(t, handler, http.MethodDelete, "/api/v1/movie-watchlists/"+url.PathEscape(saved.ID), "", nil); status != http.StatusNotFound {
		t.Fatalf("DELETE of a missing watchlist: status %d, body %s; want 404", status, raw)
	}
	if status, raw = request(t, handler, http.MethodPut, "/api/v1/movie-watchlists", `{"name":"","intervalHours":24}`, nil); status != http.StatusBadRequest {
		t.Fatalf("PUT watchlist without a name: status %d, body %s; want 400", status, raw)
	}
}

func TestMovieAPIBodyAndParameterBounds(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	movie := addSyntheticMovie(t, handler, "tt0133093", "The Matrix", 1999)
	id := url.PathEscape(movie.ID)

	cases := []struct {
		name, method, path, body string
	}{
		{"malformed JSON", http.MethodPost, "/api/v1/movies", "{"},
		{"empty movie body", http.MethodPost, "/api/v1/movies", ""},
		{"unknown movie field", http.MethodPost, "/api/v1/movies", `{"monitored":true,"unexpected":true}`},
		{"trailing movie document", http.MethodPost, "/api/v1/movies", `{"monitored":true}{"monitored":true}`},
		{"oversized movie body", http.MethodPost, "/api/v1/movies", `{"monitored":true,"collection":"` + strings.Repeat("x", 257<<10) + `"}`},
		{"unknown bulk field", http.MethodPost, "/api/v1/movies/bulk", `{"ids":[],"unexpected":true}`},
		{"empty grab body", http.MethodPost, "/api/v1/movies/" + id + "/grab", ""},
		{"unknown grab field", http.MethodPost, "/api/v1/movies/" + id + "/grab", `{"releaseId":"release-a","unexpected":true}`},
		{"unknown rename field", http.MethodPost, "/api/v1/movies/" + id + "/rename", `{"preview":true,"unexpected":true}`},
		{"unknown refresh field", http.MethodPost, "/api/v1/movies/" + id + "/refresh", `{"unexpected":true}`},
		{"unknown config field", http.MethodPut, "/api/v1/movie-config", `{"unexpected":true}`},
		{"trailing profile document", http.MethodPut, "/api/v1/movie-profiles", `{"name":"A"}{"name":"B"}`},
		{"missing discover query", http.MethodGet, "/api/v1/movies/discover", ""},
		{"discover page zero", http.MethodGet, "/api/v1/movies/discover?q=dune&page=0", ""},
		{"discover page not a number", http.MethodGet, "/api/v1/movies/discover?q=dune&page=abc", ""},
		{"invalid deleteFiles", http.MethodDelete, "/api/v1/movies/" + id + "?deleteFiles=maybe", ""},
	}
	for _, tc := range cases {
		status, raw := request(t, handler, tc.method, tc.path, tc.body, nil)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s; want 400", tc.name, status, raw)
		}
	}
	if status, raw := request(t, handler, http.MethodGet, "/api/v1/movies/discover?q="+strings.Repeat("x", 300), "", nil); status != http.StatusBadRequest {
		t.Errorf("oversized discover query: status %d, body %s; want 400", status, raw)
	}

	// Action endpoints accept an omitted body.
	if status, raw := request(t, handler, http.MethodPost, "/api/v1/movies/"+id+"/rename", "", nil); status != http.StatusOK {
		t.Errorf("rename without a body: status %d, body %s; want 200", status, raw)
	}
}

func TestMovieConfigSecretsAndMetadata(t *testing.T) {
	pool, _, _, handler := movieEnvironment(t, downloads.Config{})
	ctx := context.Background()
	fixture := newMetadataFixture(t)
	root := t.TempDir()

	putConfig := func(metadataKey string) (int, []byte) {
		t.Helper()
		return request(t, handler, http.MethodPut, "/api/v1/movie-config",
			movieConfigBody(t, fixture.URL, metadataKey, root), nil)
	}
	status, raw := putConfig("synthetic-metadata-key")
	if status != http.StatusOK {
		t.Fatalf("PUT movie-config: status %d, body %s", status, raw)
	}
	var config movies.Config
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("movie config is not JSON: %v (%s)", err, raw)
	}
	if config.MetadataURL != fixture.URL || !config.MetadataConfigured || len(config.RootFolders) != 1 || config.RootFolders[0].ID != "root-1" {
		t.Fatalf("saved movie config = %+v", config)
	}
	assertHidden(t, raw, "synthetic-metadata-key")

	var stored []byte
	if err := pool.QueryRow(ctx, `SELECT data FROM movie_config WHERE id`).Scan(&stored); err != nil {
		t.Fatalf("read stored movie config: %v", err)
	}
	if !strings.Contains(string(stored), "synthetic-metadata-key") {
		t.Fatal("the metadata API key was not retained")
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movie-config", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movie-config: status %d, body %s", status, raw)
	}
	assertHidden(t, raw, "synthetic-metadata-key")
	if err := json.Unmarshal(raw, &config); err != nil || !config.MetadataConfigured || config.MetadataAPIKey != "" {
		t.Fatalf("movie config view = %s, %v", raw, err)
	}

	// A blank key keeps the saved secret while the remaining fields are replaced.
	status, raw = putConfig("")
	if status != http.StatusOK {
		t.Fatalf("PUT movie-config with a blank key: status %d, body %s", status, raw)
	}
	assertHidden(t, raw, "synthetic-metadata-key")
	if err := json.Unmarshal(raw, &config); err != nil || !config.MetadataConfigured {
		t.Fatalf("movie config after a blank key = %s, %v", raw, err)
	}
	if err := pool.QueryRow(ctx, `SELECT data FROM movie_config WHERE id`).Scan(&stored); err != nil {
		t.Fatalf("read stored movie config: %v", err)
	}
	if !strings.Contains(string(stored), "synthetic-metadata-key") {
		t.Fatal("a blank update dropped the retained metadata API key")
	}

	// Discovery and refresh use the retained key against the provider.
	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies/discover?q=synthetic&page=1", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movies/discover: status %d, body %s", status, raw)
	}
	var titles []metadata.Title
	if err := json.Unmarshal(raw, &titles); err != nil || len(titles) != 1 || titles[0].IMDbID != "tt1234567" {
		t.Fatalf("discover titles = %s, %v", raw, err)
	}
	if fixture.lastKey() != "synthetic-metadata-key" {
		t.Fatalf("metadata provider received key %q", fixture.lastKey())
	}

	movie := addSyntheticMovie(t, handler, "tt1234567", "Synthetic Lookup", 2026)
	if status, raw = request(t, handler, http.MethodPost, "/api/v1/movies/"+url.PathEscape(movie.ID)+"/refresh", "", nil); status != http.StatusOK {
		t.Fatalf("POST movie refresh: status %d, body %s", status, raw)
	}
	var refreshed movies.Movie
	if err := json.Unmarshal(raw, &refreshed); err != nil {
		t.Fatalf("refreshed movie is not JSON: %v (%s)", err, raw)
	}
	if refreshed.Metadata.Released != "2026-05-15" || refreshed.Metadata.Rating == nil || refreshed.Metadata.Runtime != 120 {
		t.Fatalf("refreshed metadata = %+v", refreshed.Metadata)
	}
	if fixture.lastKey() != "synthetic-metadata-key" {
		t.Fatalf("refresh used metadata key %q", fixture.lastKey())
	}
}

func TestMovieCalendarICS(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	released := time.Now().UTC().AddDate(0, 0, 5)
	title := "Synthetic Release, Part Two; Special Edition With A Very Long Title For Folding"
	movie := addSyntheticMovieWithReleased(t, handler, "tt0133093", title, 2026, released.Format("2006-01-02"))

	status, raw := request(t, handler, http.MethodGet, "/api/v1/movies/calendar", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movies/calendar: status %d, body %s", status, raw)
	}
	var calendar []movies.Movie
	if err := json.Unmarshal(raw, &calendar); err != nil {
		t.Fatalf("calendar is not JSON: %v (%s)", err, raw)
	}
	found := false
	for _, item := range calendar {
		if item.ID == movie.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("calendar = %s does not include movie %s", raw, movie.ID)
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies/calendar.ics", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movies/calendar.ics: status %d, body %s", status, raw)
	}
	body := string(raw)
	if !strings.HasPrefix(body, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(body, "END:VCALENDAR\r\n") {
		t.Fatalf("calendar is not a complete iCalendar document: %q", body)
	}
	if strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n") {
		t.Fatal("calendar contains line feeds without carriage returns")
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("calendar line exceeds 75 octets: %q", line)
		}
	}
	for _, want := range []string{
		"VERSION:2.0\r\n",
		"UID:tt0133093@constellarr\r\n",
		"DTSTART:" + released.Format("20060102") + "T000000Z\r\n",
		"URL:https://www.imdb.com/title/tt0133093/\r\n",
		"SUMMARY:Synthetic Release\\, Part Two\\; Special Edition",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("calendar is missing %q:\n%s", want, body)
		}
	}
	unfolded := strings.ReplaceAll(body, "\r\n ", "")
	if !strings.Contains(unfolded, "SUMMARY:Synthetic Release\\, Part Two\\; Special Edition With A Very Long Title For Folding (2026)\r\n") {
		t.Fatalf("folded summary did not unfold correctly:\n%s", body)
	}
	if !strings.Contains(unfolded, "DTSTAMP:") || !strings.Contains(unfolded, "Z\r\n") {
		t.Fatalf("calendar timestamps are not UTC:\n%s", body)
	}
}

func TestMovieAPIRequestEnforcement(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	movie := addSyntheticMovie(t, handler, "tt0133093", "The Matrix", 1999)

	if status, raw := request(t, handler, http.MethodDelete, "/api/v1/movies/"+url.PathEscape(movie.ID), "",
		map[string]string{"Origin": "http://evil.example"}); status != http.StatusForbidden {
		t.Fatalf("DELETE from a foreign origin: status %d, body %s; want 403", status, raw)
	}
	if status, raw := request(t, handler, http.MethodPost, "/api/v1/movies", `{"monitored":true}`,
		map[string]string{"Content-Type": "text/plain"}); status != http.StatusUnsupportedMediaType {
		t.Fatalf("POST with a text content type: status %d, body %s; want 415", status, raw)
	}
	// GETs stay outside the JSON and origin middleware.
	if status, raw := request(t, handler, http.MethodGet, "/api/v1/movies", "", nil); status != http.StatusOK {
		t.Fatalf("GET movies: status %d, body %s", status, raw)
	}
}

func TestMovieFileRangeStreamingAndDeleteFiles(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	incoming := t.TempDir()
	libraryRoot := t.TempDir()
	if status, raw := request(t, handler, http.MethodPut, "/api/v1/movie-config", movieRootsConfigBody(t, incoming, libraryRoot), nil); status != http.StatusOK {
		t.Fatalf("PUT movie-config: status %d, body %s", status, raw)
	}
	writeMedia := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(incoming, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write media %s: %v", name, err)
		}
	}
	importMedia := func(movie movies.Movie, source string) movies.Movie {
		t.Helper()
		status, raw := request(t, handler, http.MethodPost, "/api/v1/movies/import",
			fmt.Sprintf(`{"rootId":"incoming","path":%q,"movieId":%q}`, source, movie.ID), nil)
		if status != http.StatusOK {
			t.Fatalf("POST import: status %d, body %s", status, raw)
		}
		var imported movies.Movie
		if err := json.Unmarshal(raw, &imported); err != nil {
			t.Fatalf("import response is not JSON: %v (%s)", err, raw)
		}
		if len(imported.Files) != 1 || imported.Files[0].Size <= 0 {
			t.Fatalf("imported files = %+v", imported.Files)
		}
		return imported
	}

	movie := addMovieAtRoot(t, handler, "tt1160419", "Dune: Part Two", 2024, "library")
	const source = "Dune.Part.Two.2024.1080p.BluRay.x264.mkv"
	writeMedia(source, "0123456789")
	imported := importMedia(movie, source)
	filePath := imported.Files[0].Path

	target := "/api/v1/movies/" + url.PathEscape(movie.ID) + "/file?path=" + url.QueryEscape(filePath)
	recorder := movieRequest(t, handler, http.MethodGet, target, "", map[string]string{"Range": "bytes=0-3"})
	if recorder.Code != http.StatusPartialContent || recorder.Body.String() != "0123" || recorder.Header().Get("Content-Range") != "bytes 0-3/10" {
		t.Fatalf("range request: status %d, headers %v, body %q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if disposition := recorder.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment;") {
		t.Fatalf("Content-Disposition = %q", disposition)
	}
	recorder = movieRequest(t, handler, http.MethodGet, target, "", nil)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "0123456789" {
		t.Fatalf("full file request: status %d, body %q", recorder.Code, recorder.Body.String())
	}

	for name, bad := range map[string]string{
		"traversal":     "../outside.mkv",
		"unlisted file": source,
		"missing path":  "",
	} {
		recorder := movieRequest(t, handler, http.MethodGet,
			"/api/v1/movies/"+url.PathEscape(movie.ID)+"/file?path="+url.QueryEscape(bad), "", nil)
		if recorder.Code != http.StatusNotFound && recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, body %s", name, recorder.Code, recorder.Body.String())
		}
	}

	live := filepath.Join(libraryRoot, filepath.FromSlash(filePath))
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("imported media is missing: %v", err)
	}
	if status, raw := request(t, handler, http.MethodDelete, "/api/v1/movies/"+url.PathEscape(movie.ID), "", nil); status != http.StatusOK {
		t.Fatalf("DELETE movie: status %d, body %s", status, raw)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("default delete removed the library file: %v", err)
	}

	// An explicit deleteFiles archives the media below .recycle instead of deleting it.
	second := addMovieAtRoot(t, handler, "tt0111161", "The Shawshank Redemption", 1994, "library")
	const secondSource = "The.Shawshank.Redemption.1994.1080p.BluRay.x264.mkv"
	writeMedia(secondSource, "abcdefghij")
	secondImported := importMedia(second, secondSource)
	archived := filepath.Join(libraryRoot, filepath.FromSlash(secondImported.Files[0].Path))
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("second imported media is missing: %v", err)
	}
	if status, raw := request(t, handler, http.MethodDelete, "/api/v1/movies/"+url.PathEscape(second.ID)+"?deleteFiles=true", "", nil); status != http.StatusOK {
		t.Fatalf("DELETE movie with deleteFiles: status %d, body %s", status, raw)
	}
	if _, err := os.Stat(archived); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleteFiles left the media in place: %v", err)
	}
}

func TestMovieSearchAndGrabRoutes(t *testing.T) {
	indexer := newMovieIndexer(t)
	_, _, _, handler := movieEnvironment(t, downloads.Config{
		IndexerURL: indexer.URL + "/api", APIKey: indexerAPIKey,
	})
	movie := addSyntheticMovie(t, handler, "tt0133093", "The Matrix", 1999)

	status, raw := request(t, handler, http.MethodPost, "/api/v1/movies/"+url.PathEscape(movie.ID)+"/search", "{}", nil)
	if status != http.StatusOK {
		t.Fatalf("POST movie search: status %d, body %s", status, raw)
	}
	var releases []struct {
		ID       string           `json:"id"`
		Title    string           `json:"title"`
		Size     int64            `json:"size"`
		Decision quality.Decision `json:"decision"`
	}
	if err := json.Unmarshal(raw, &releases); err != nil {
		t.Fatalf("release search is not JSON: %v (%s)", err, raw)
	}
	if len(releases) != 1 || releases[0].ID != "matrix-release" || releases[0].Title != "The.Matrix.1999.1080p.BluRay.x264-GROUP" {
		t.Fatalf("release search = %s", raw)
	}
	if !releases[0].Decision.Allowed || releases[0].Decision.Details.Quality != "Bluray-1080p" {
		t.Fatalf("release decision = %+v", releases[0].Decision)
	}

	status, raw = request(t, handler, http.MethodPost, "/api/v1/movies/"+url.PathEscape(movie.ID)+"/grab",
		`{"releaseId":"matrix-release","override":false}`, nil)
	if status != http.StatusCreated {
		t.Fatalf("POST movie grab: status %d, body %s", status, raw)
	}
	var job downloads.Job
	if err := json.Unmarshal(raw, &job); err != nil || job.ReleaseID != "matrix-release" || job.Status != "queued" {
		t.Fatalf("grabbed job = %s, %v", raw, err)
	}
	if status, raw = request(t, handler, http.MethodPost, "/api/v1/movies/"+url.PathEscape(movie.ID)+"/grab",
		`{"releaseId":"missing-release"}`, nil); status != http.StatusBadRequest {
		t.Fatalf("grab of an unknown release: status %d, body %s; want 400", status, raw)
	}
}

func TestMovieConfigTestAndWatchlistSync(t *testing.T) {
	_, _, _, handler := movieEnvironment(t, downloads.Config{})
	fixture := newMetadataFixture(t)
	root := t.TempDir()
	if status, raw := request(t, handler, http.MethodPut, "/api/v1/movie-config",
		movieConfigBody(t, fixture.URL, "synthetic-metadata-key", root), nil); status != http.StatusOK {
		t.Fatalf("PUT movie-config: status %d, body %s", status, raw)
	}

	status, raw := request(t, handler, http.MethodPost, "/api/v1/movie-config/test", "", nil)
	if status != http.StatusOK {
		t.Fatalf("POST movie-config/test: status %d, body %s", status, raw)
	}
	var tests movies.ConnectionTests
	if err := json.Unmarshal(raw, &tests); err != nil {
		t.Fatalf("connection tests are not JSON: %v (%s)", err, raw)
	}
	if !tests.Metadata.OK || tests.Metadata.Error != "" {
		t.Fatalf("metadata test = %+v", tests.Metadata)
	}
	if tests.Jellyfin.OK || tests.Jellyfin.Error == "" {
		t.Fatalf("unconfigured Jellyfin test = %+v", tests.Jellyfin)
	}
	assertHidden(t, raw, "synthetic-metadata-key")

	status, raw = request(t, handler, http.MethodPut, "/api/v1/movie-watchlists",
		`{"name":"Synthetic Sync `+rand.Text()+`","imdbIds":["tt1234567"],"monitor":true,"intervalHours":24,"rootId":"root-1"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("PUT movie-watchlist: status %d, body %s", status, raw)
	}
	var watchlist movies.Watchlist
	if err := json.Unmarshal(raw, &watchlist); err != nil || watchlist.ID == "" {
		t.Fatalf("watchlist response = %s, %v", raw, err)
	}
	status, raw = request(t, handler, http.MethodPost, "/api/v1/movie-watchlists/"+url.PathEscape(watchlist.ID)+"/sync", "", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), `"added":1`) {
		t.Fatalf("watchlist sync: status %d, body %s", status, raw)
	}
	if status, raw = request(t, handler, http.MethodPost, "/api/v1/movie-watchlists/"+url.PathEscape(watchlist.ID)+"/sync", "", nil); status != http.StatusOK || !strings.Contains(string(raw), `"added":0`) {
		t.Fatalf("second watchlist sync: status %d, body %s", status, raw)
	}
	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies", "", nil)
	var list []movies.Movie
	if status != http.StatusOK {
		t.Fatalf("GET movies: status %d, body %s", status, raw)
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list) != 1 || list[0].Metadata.IMDbID != "tt1234567" {
		t.Fatalf("movies after watchlist sync = %s, %v", raw, err)
	}
	if status, raw = request(t, handler, http.MethodPost, "/api/v1/movie-watchlists/missing-watchlist/sync", "", nil); status != http.StatusNotFound {
		t.Fatalf("sync of a missing watchlist: status %d, body %s; want 404", status, raw)
	}
}

func addSyntheticMovie(t *testing.T, handler http.Handler, imdbID, title string, year int) movies.Movie {
	t.Helper()
	return addSyntheticMovieWithReleased(t, handler, imdbID, title, year, "")
}

func addSyntheticMovieWithReleased(t *testing.T, handler http.Handler, imdbID, title string, year int, released string) movies.Movie {
	t.Helper()
	body := map[string]any{
		"imdbId": imdbID,
		"metadata": map[string]any{
			"imdbId": imdbID, "title": title, "year": year, "type": "movie", "released": released,
		},
		"monitored": true,
	}
	encoded, err := json.Marshal(body)
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

func addMovieAtRoot(t *testing.T, handler http.Handler, imdbID, title string, year int, rootID string) movies.Movie {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"imdbId": imdbID,
		"metadata": map[string]any{
			"imdbId": imdbID, "title": title, "year": year, "type": "movie",
			"released": fmt.Sprintf("%d-01-01", year),
		},
		"monitored": true,
		"rootId":    rootID,
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

func movieRootsConfigBody(t *testing.T, incoming, libraryRoot string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"rootFolders": []map[string]string{
			{"id": "incoming", "path": incoming},
			{"id": "library", "path": libraryRoot},
		},
		"folderTemplate":      "{title} ({year})",
		"fileTemplate":        "{title} ({year})",
		"importMode":          "copy",
		"writeNFO":            false,
		"pollMinutes":         60,
		"searchHours":         24,
		"minimumAvailability": "released",
		"retryFailed":         false,
		"metadataURL":         "",
		"metadataAPIKey":      "",
		"jellyfinURL":         "",
		"jellyfinAPIKey":      "",
		"webhookURL":          "",
	})
	if err != nil {
		t.Fatalf("marshal movie config: %v", err)
	}
	return string(encoded)
}

func movieRequest(t *testing.T, handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func movieConfigBody(t *testing.T, metadataURL, metadataKey, root string) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"rootFolders":         []map[string]string{{"id": "root-1", "path": root}},
		"folderTemplate":      "{title} ({year})",
		"fileTemplate":        "{title} ({year})",
		"importMode":          "copy",
		"writeNFO":            true,
		"pollMinutes":         60,
		"searchHours":         24,
		"minimumAvailability": "released",
		"retryFailed":         false,
		"metadataURL":         metadataURL,
		"metadataAPIKey":      metadataKey,
		"jellyfinURL":         "",
		"jellyfinAPIKey":      "",
		"webhookURL":          "",
	})
	if err != nil {
		t.Fatalf("marshal movie config: %v", err)
	}
	return string(encoded)
}

const metadataSearchResponse = `{"Search":[{"Title":"Synthetic Search","Year":"2026","imdbID":"tt1234567","Type":"movie","Poster":"http://poster.example/search.jpg"}],"totalResults":"1","Response":"True"}`

func newMovieIndexer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("t") {
		case "movie", "search", "rss":
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><item>
<title>The.Matrix.1999.1080p.BluRay.x264-GROUP</title><guid isPermaLink="false">matrix-release</guid>
<pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate>
<enclosure url="http://indexer.example/matrix-release.nzb" length="4096" type="application/x-nzb"/>
</item></channel></rss>`)
		case "get":
			if r.URL.Query().Get("id") != "matrix-release" {
				http.Error(w, "unexpected release", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/x-nzb")
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

type metadataFixture struct {
	*httptest.Server
	mu   sync.Mutex
	keys []string
}

func newMetadataFixture(t *testing.T) *metadataFixture {
	t.Helper()
	fixture := &metadataFixture{}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.keys = append(fixture.keys, r.URL.Query().Get("apikey"))
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if imdbID := r.URL.Query().Get("i"); imdbID != "" {
			fmt.Fprintf(w, `{"Title":"Synthetic Lookup","Year":"2026","Released":"15 May 2026","Runtime":"120 min","Genre":"Action, Drama","Director":"Synthetic Director","Actors":"Synthetic Actor","Language":"English","Country":"Syntheticland","Rated":"PG-13","Poster":"http://poster.example/lookup.jpg","Plot":"Synthetic plot.","imdbRating":"8.1","imdbVotes":"12,345","imdbID":%q,"Type":"movie","Response":"True"}`, imdbID)
			return
		}
		fmt.Fprint(w, metadataSearchResponse)
	}))
	t.Cleanup(fixture.Server.Close)
	return fixture
}

func (f *metadataFixture) lastKey() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keys) == 0 {
		return ""
	}
	return f.keys[len(f.keys)-1]
}
