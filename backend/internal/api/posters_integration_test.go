package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
)

const syntheticPosterKey = "synthetic-poster-key"

const posterProxyPrefix = "/api/v1/movie-poster?imdbId="

// clearProviderEnvironment removes live provider values so poster tests only use synthetic credentials and local servers.
func clearProviderEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"OMDB_API_KEY", "OMDB_URL", "OMDB_POSTER_URL", "JELLYFIN_URL", "JELLYFIN_API_KEY", "IMPORT_WEBHOOK_URL"} {
		t.Setenv(name, "")
	}
}

// saveProviderConfig stores a synthetic metadata credential through the service store the API reads.
func saveProviderConfig(t *testing.T, service *movies.Service, metadataURL string) {
	t.Helper()
	ctx := context.Background()
	config, err := service.Store.Config(ctx)
	if err != nil {
		t.Fatalf("load movie config: %v", err)
	}
	config.MetadataURL = metadataURL
	config.MetadataAPIKey = syntheticPosterKey
	if _, err := service.Store.SaveConfig(ctx, config); err != nil {
		t.Fatalf("save movie config: %v", err)
	}
}

type providerServer struct {
	*httptest.Server
	requests atomic.Int64
	mu       sync.Mutex
	queries  []url.Values
}

// newProviderServer records every query a local provider stand-in receives.
func newProviderServer(t *testing.T, respond http.HandlerFunc) *providerServer {
	t.Helper()
	provider := &providerServer{}
	provider.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.requests.Add(1)
		provider.mu.Lock()
		provider.queries = append(provider.queries, r.URL.Query())
		provider.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(provider.Server.Close)
	return provider
}

func (p *providerServer) count() int64 {
	return p.requests.Load()
}

func (p *providerServer) queryValues() []url.Values {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]url.Values(nil), p.queries...)
}

// encodedImage returns a tiny valid image so MIME detection and disk cache behavior can be asserted.
func encodedImage(t *testing.T, format string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 0x33, G: 0x66, B: 0x99, A: 0xff})
	var err error
	switch format {
	case "png":
		err = png.Encode(&buffer, picture)
	case "jpeg":
		err = jpeg.Encode(&buffer, picture, &jpeg.Options{Quality: 80})
	default:
		t.Fatalf("unsupported image format %q", format)
	}
	if err != nil {
		t.Fatalf("encode %s image: %v", format, err)
	}
	return buffer.Bytes()
}

// assertPosterHidesKey fails when a poster response echoes the synthetic credential in its body or headers.
func assertPosterHidesKey(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	assertHidden(t, recorder.Body.Bytes(), syntheticPosterKey, "apikey")
	for name, values := range recorder.Header() {
		for _, value := range values {
			if strings.Contains(value, syntheticPosterKey) || strings.Contains(value, "apikey") {
				t.Fatalf("poster response header %s leaks the credential: %s", name, value)
			}
		}
	}
}

// assertPosterProxyURL checks a JSON payload links the poster through the API proxy without the credential.
func assertPosterProxyURL(t *testing.T, raw []byte, imdbID string) {
	t.Helper()
	assertHidden(t, raw, syntheticPosterKey, "apikey")
	if !strings.Contains(string(raw), posterProxyPrefix+imdbID) {
		t.Fatalf("response %s does not use the poster proxy for %s", raw, imdbID)
	}
}

func TestMoviePosterProxyServesCachedImages(t *testing.T) {
	clearProviderEnvironment(t)
	_, manager, service, handler := movieEnvironment(t, downloads.Config{Directory: t.TempDir()})
	cases := []struct {
		id     string
		format string
		image  []byte
	}{
		{"tt0133093", "png", encodedImage(t, "png")},
		{"tt0133094", "jpeg", encodedImage(t, "jpeg")},
	}
	payloads := make(map[string][]byte, len(cases))
	for _, tc := range cases {
		payloads[tc.id] = tc.image
	}
	upstream := newProviderServer(t, func(w http.ResponseWriter, r *http.Request) {
		payload, ok := payloads[r.URL.Query().Get("i")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Deliberately wrong upstream type and cache policy prove the proxy sets its own image headers.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = w.Write(payload)
	})
	t.Setenv("OMDB_POSTER_URL", upstream.URL)
	saveProviderConfig(t, service, upstream.URL)

	for _, invalid := range []string{"", "nm0000209", "tt123456", "tt0133093x"} {
		status, raw := request(t, handler, http.MethodGet, "/api/v1/movie-poster?imdbId="+url.QueryEscape(invalid), "", nil)
		if status != http.StatusBadRequest || !strings.Contains(string(raw), "invalid IMDb ID") {
			t.Fatalf("poster with IMDb ID %q: status %d, body %s; want 400", invalid, status, raw)
		}
	}
	if upstream.count() != 0 {
		t.Fatalf("invalid IMDb IDs reached the provider %d times", upstream.count())
	}

	for _, tc := range cases {
		target := posterProxyPrefix + tc.id
		before := upstream.count()
		first := movieRequest(t, handler, http.MethodGet, target, "", nil)
		if first.Code != http.StatusOK {
			t.Fatalf("poster %s: status %d, body %s; want 200", tc.id, first.Code, first.Body)
		}
		if got := first.Header().Get("Content-Type"); got != "image/"+tc.format {
			t.Fatalf("poster %s content type = %q; want image/%s", tc.id, got, tc.format)
		}
		if got := first.Header().Get("Cache-Control"); got != "private, max-age=86400" {
			t.Fatalf("poster %s cache control = %q; want private, max-age=86400", tc.id, got)
		}
		if got := first.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("poster %s nosniff = %q; want nosniff", tc.id, got)
		}
		if !bytes.Equal(first.Body.Bytes(), tc.image) {
			t.Fatalf("poster %s body = %d bytes; want the provider image of %d bytes", tc.id, first.Body.Len(), len(tc.image))
		}
		assertPosterHidesKey(t, first)
		if upstream.count() != before+1 {
			t.Fatalf("poster %s provider requests = %d; want %d", tc.id, upstream.count(), before+1)
		}
		queries := upstream.queryValues()
		query := queries[len(queries)-1]
		if query.Get("i") != tc.id || query.Get("h") != "900" || query.Get("apikey") != syntheticPosterKey {
			t.Fatalf("poster %s provider query = %v; want the synthetic key and poster parameters", tc.id, query)
		}

		cached := filepath.Join(manager.Config().Directory, "posters", tc.id+".image")
		if _, err := os.Stat(cached); err != nil {
			t.Fatalf("poster %s cache file: %v", tc.id, err)
		}
		second := movieRequest(t, handler, http.MethodGet, target, "", nil)
		if second.Code != http.StatusOK || !bytes.Equal(second.Body.Bytes(), tc.image) {
			t.Fatalf("cached poster %s: status %d, %d bytes; want the first image", tc.id, second.Code, second.Body.Len())
		}
		assertPosterHidesKey(t, second)
		if upstream.count() != before+1 {
			t.Fatalf("poster %s was fetched from the provider again: %d requests", tc.id, upstream.count())
		}
	}

	upstream.Close()
	for _, tc := range cases {
		recorder := movieRequest(t, handler, http.MethodGet, posterProxyPrefix+tc.id, "", nil)
		if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), tc.image) {
			t.Fatalf("poster %s without the provider: status %d, %d bytes; want the disk-cached image", tc.id, recorder.Code, recorder.Body.Len())
		}
		assertPosterHidesKey(t, recorder)
	}
}

func TestMoviePosterProviderFailuresHideCredential(t *testing.T) {
	clearProviderEnvironment(t)
	_, manager, service, handler := movieEnvironment(t, downloads.Config{Directory: t.TempDir()})
	redirectTarget := newProviderServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("redirect target should not be reached"))
	})
	upstream := newProviderServer(t, func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("apikey")
		switch r.URL.Query().Get("i") {
		case "tt0133093":
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, `{"apikey":%q}`, key)
		case "tt0133094":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, "<html><body>%s</body></html>", key)
		case "tt0133095":
			http.Redirect(w, r, redirectTarget.URL+"/poster?apikey="+url.QueryEscape(key), http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	t.Setenv("OMDB_POSTER_URL", upstream.URL)
	saveProviderConfig(t, service, upstream.URL)

	cases := []struct{ id, failure string }{
		{"tt0133093", "provider returned HTTP 502"},
		{"tt0133094", "provider returned a non-image body"},
		{"tt0133095", "provider redirected to another origin"},
	}
	for _, tc := range cases {
		recorder := movieRequest(t, handler, http.MethodGet, posterProxyPrefix+tc.id, "", nil)
		if recorder.Code != http.StatusBadGateway {
			t.Fatalf("%s: status %d, body %s; want 502", tc.failure, recorder.Code, recorder.Body)
		}
		if !strings.Contains(recorder.Body.String(), "movie poster is currently unavailable") {
			t.Fatalf("%s: body %s; want the sanitized poster error", tc.failure, recorder.Body)
		}
		if strings.Contains(recorder.Body.String(), tc.id) {
			t.Fatalf("%s: body %s leaks the provider query", tc.failure, recorder.Body)
		}
		assertPosterHidesKey(t, recorder)
		if _, err := os.Stat(filepath.Join(manager.Config().Directory, "posters", tc.id+".image")); !os.IsNotExist(err) {
			t.Fatalf("%s: failure left a cache file behind: %v", tc.failure, err)
		}
	}
	if redirectTarget.count() != 0 {
		t.Fatalf("cross-origin redirect target received %d requests; want 0", redirectTarget.count())
	}
	queries := upstream.queryValues()
	if len(queries) != len(cases) {
		t.Fatalf("provider requests = %d; want %d", len(queries), len(cases))
	}
	for _, query := range queries {
		if query.Get("apikey") != syntheticPosterKey {
			t.Fatalf("provider query = %v; want the synthetic key upstream", query)
		}
	}
}

func TestMoviePosterURLsHideMetadataKey(t *testing.T) {
	clearProviderEnvironment(t)
	_, _, service, handler := movieEnvironment(t, downloads.Config{Directory: t.TempDir()})

	// The provider embeds the credential in its poster URLs, like OMDb image links do.
	provider := newProviderServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		key := r.URL.Query().Get("apikey")
		if imdbID := r.URL.Query().Get("i"); imdbID != "" {
			fmt.Fprintf(w, `{"Title":"Synthetic Lookup","Year":"2026","Released":"15 May 2026","Runtime":"120 min","Genre":"Action","Director":"Synthetic Director","Actors":"Synthetic Actor","Language":"English","Country":"Syntheticland","Rated":"PG-13","Poster":"http://poster.example/lookup.jpg?apikey=%s","Plot":"Synthetic plot.","imdbRating":"8.1","imdbVotes":"12,345","imdbID":%q,"Type":"movie","Response":"True"}`, key, imdbID)
			return
		}
		fmt.Fprintf(w, `{"Search":[{"Title":"Synthetic Search","Year":"2026","imdbID":"tt1234567","Type":"movie","Poster":"http://poster.example/search.jpg?apikey=%s"}],"totalResults":"1","Response":"True"}`, key)
	})

	// A stored provider poster URL from before configuration must also be replaced.
	body, err := json.Marshal(map[string]any{
		"imdbId": "tt0133093",
		"metadata": map[string]any{
			"imdbId": "tt0133093", "title": "The Matrix", "year": 1999, "type": "movie",
			"poster": "https://img.example/poster.jpg?apikey=" + syntheticPosterKey,
		},
		"monitored": true,
	})
	if err != nil {
		t.Fatalf("marshal movie: %v", err)
	}
	status, raw := request(t, handler, http.MethodPost, "/api/v1/movies", string(body), nil)
	if status != http.StatusCreated {
		t.Fatalf("POST movie: status %d, body %s", status, raw)
	}
	var created movies.Movie
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("created movie is not JSON: %v (%s)", err, raw)
	}
	if created.ID == "" || created.Metadata.Poster != posterProxyPrefix+"tt0133093" {
		t.Fatalf("created movie poster = %q, %v", created.Metadata.Poster, err)
	}
	assertPosterProxyURL(t, raw, "tt0133093")

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movies: status %d, body %s", status, raw)
	}
	var listed []movies.Movie
	if err := json.Unmarshal(raw, &listed); err != nil || len(listed) != 1 || listed[0].Metadata.Poster != posterProxyPrefix+"tt0133093" {
		t.Fatalf("movie list = %s, %v", raw, err)
	}
	assertPosterProxyURL(t, raw, "tt0133093")

	saveProviderConfig(t, service, provider.URL)

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies/discover?q=synthetic&page=1", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movies/discover: status %d, body %s", status, raw)
	}
	var titles []metadata.Title
	if err := json.Unmarshal(raw, &titles); err != nil || len(titles) != 1 || titles[0].IMDbID != "tt1234567" {
		t.Fatalf("discover titles = %s, %v", raw, err)
	}
	if titles[0].Poster != posterProxyPrefix+"tt1234567" {
		t.Fatalf("discover poster = %q", titles[0].Poster)
	}
	assertPosterProxyURL(t, raw, "tt1234567")

	body, err = json.Marshal(map[string]any{
		"imdbId":    "tt1234567",
		"metadata":  map[string]any{"imdbId": "tt1234567", "title": "Synthetic Placeholder", "year": 2026, "type": "movie"},
		"monitored": true,
	})
	if err != nil {
		t.Fatalf("marshal movie: %v", err)
	}
	status, raw = request(t, handler, http.MethodPost, "/api/v1/movies", string(body), nil)
	if status != http.StatusCreated {
		t.Fatalf("POST looked-up movie: status %d, body %s", status, raw)
	}
	var added movies.Movie
	if err := json.Unmarshal(raw, &added); err != nil {
		t.Fatalf("added movie is not JSON: %v (%s)", err, raw)
	}
	if added.Metadata.Title != "Synthetic Lookup" || added.Metadata.Poster != posterProxyPrefix+"tt1234567" {
		t.Fatalf("added movie = %+v", added.Metadata)
	}
	assertPosterProxyURL(t, raw, "tt1234567")

	status, raw = request(t, handler, http.MethodPost, "/api/v1/movies/"+url.PathEscape(added.ID)+"/refresh", "", nil)
	if status != http.StatusOK {
		t.Fatalf("POST movie refresh: status %d, body %s", status, raw)
	}
	var refreshed movies.Movie
	if err := json.Unmarshal(raw, &refreshed); err != nil || refreshed.Metadata.Poster != posterProxyPrefix+"tt1234567" {
		t.Fatalf("refreshed movie = %s, %v", raw, err)
	}
	assertPosterProxyURL(t, raw, "tt1234567")

	status, raw = request(t, handler, http.MethodGet, "/api/v1/movies", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET movies: status %d, body %s", status, raw)
	}
	assertPosterProxyURL(t, raw, "tt0133093")
	assertPosterProxyURL(t, raw, "tt1234567")

	queries := provider.queryValues()
	if len(queries) < 3 {
		t.Fatalf("provider requests = %d; want discover, add, and refresh traffic", len(queries))
	}
	for _, query := range queries {
		if query.Get("apikey") != syntheticPosterKey {
			t.Fatalf("provider query = %v; want the synthetic key upstream", query)
		}
	}
}
