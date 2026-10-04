package indexer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
)

const movieFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <item>
      <title>Inception 2010 1080p BluRay x264-GROUP</title>
      <guid>https://api.nzbgeek.info/details/inception-1</guid>
      <pubDate>Mon, 02 Jan 2012 15:04:05 +0000</pubDate>
      <newznab:attr name="size" value="8388608"/>
      <newznab:attr name="imdb" value="tt1375666"/>
    </item>
    <item>
      <title>Inception 2010 2160p REMUX-GROUP2</title>
      <guid>https://api.nzbgeek.info/details/inception-2</guid>
      <newznab:attr name="size" value="16777216"/>
      <newznab:attr name="imdbid" value="01375666"/>
      <newznab:attr name="IMDB" value="tt1375666"/>
    </item>
    <item>
      <title>Inception 2010 CAM-GROUP3</title>
      <guid>https://api.nzbgeek.info/details/inception-3</guid>
      <newznab:attr name="imdb" value="not-an-id"/>
    </item>
  </channel>
</rss>`

func TestSearchMovieUsesIMDbIdentifier(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, movieFeed)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	releases, err := client.SearchMovie(context.Background(), "tt1375666", "Inception", 2010)
	if err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	got := <-requests
	if got.values.Get("t") != "movie" || got.values.Get("cat") != "2000" || got.values.Get("apikey") != testKey {
		t.Fatalf("SearchMovie sent %s, want a movie search", got.raw)
	}
	if got.values.Get("imdbid") != "1375666" {
		t.Fatalf("imdbid = %q, want digits without the tt prefix", got.values.Get("imdbid"))
	}
	if got.values.Get("q") != "" {
		t.Fatalf("SearchMovie sent a query: %s", got.raw)
	}
	if len(releases) != 3 {
		t.Fatalf("got %d releases, want 3", len(releases))
	}
	if releases[0].IMDbID != "tt1375666" || releases[1].IMDbID != "tt01375666" || releases[2].IMDbID != "" {
		t.Fatalf("IMDb IDs = %q, %q, %q", releases[0].IMDbID, releases[1].IMDbID, releases[2].IMDbID)
	}
	if releases[0].Size != 8388608 || releases[0].Title != "Inception 2010 1080p BluRay x264-GROUP" {
		t.Fatalf("first release = %+v", releases[0])
	}
}

func TestReleaseJSONCarriesOptionalIMDbID(t *testing.T) {
	encoded, err := json.Marshal(indexer.Release{ID: "x", Title: "y", IMDbID: "tt1375666"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"imdbId":"tt1375666"`) {
		t.Fatalf("JSON lacks imdbId: %s", encoded)
	}
	encoded, err = json.Marshal(indexer.Release{ID: "x", Title: "y"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "imdbId") {
		t.Fatalf("unknown IMDb ID was serialized: %s", encoded)
	}
}

func TestSearchMovieFallsBackToTitleAndYear(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	if _, err := client.SearchMovie(context.Background(), "", "Dune: Part Two", 2024); err != nil {
		t.Fatalf("SearchMovie: %v", err)
	}
	got := <-requests
	if got.values.Get("t") != "search" || got.values.Get("q") != "Dune: Part Two 2024" ||
		got.values.Get("cat") != "2000" || got.values.Get("imdbid") != "" {
		t.Fatalf("SearchMovie sent %s, want a title search with the year", got.raw)
	}
}

func TestSearchMovieRejectsUnusableInput(t *testing.T) {
	hits := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	cases := []struct {
		imdbID string
		title  string
	}{
		{"not-an-id", "Inception"},
		{"tt123", "Inception"},
		{"tt1234567890123", "Inception"},
		{"", ""},
	}
	for _, tc := range cases {
		if _, err := client.SearchMovie(context.Background(), tc.imdbID, tc.title, 2010); err == nil {
			t.Fatalf("SearchMovie(%q, %q) succeeded, want an error", tc.imdbID, tc.title)
		}
	}
	select {
	case <-hits:
		t.Fatal("unusable input reached the indexer")
	default:
	}
}

func TestRSSUsesRecentMovieFeed(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, movieFeed)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	releases, err := client.RSS(context.Background())
	if err != nil {
		t.Fatalf("RSS: %v", err)
	}
	got := <-requests
	if got.values.Get("t") != "search" || got.values.Get("cat") != "2000" ||
		got.values.Get("extended") != "1" || got.values.Get("apikey") != testKey {
		t.Fatalf("RSS sent %s, want an extended movie feed", got.raw)
	}
	if got.values.Get("q") != "" {
		t.Fatalf("RSS sent a query: %s", got.raw)
	}
	if len(releases) != 3 || releases[1].IMDbID != "tt01375666" {
		t.Fatalf("RSS parsed %+v", releases)
	}
}

func TestSearchMovieReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<error code="100" description="Invalid API Key"/>`)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	_, err := client.SearchMovie(context.Background(), "tt1375666", "Inception", 2010)
	var apiErr *indexer.Error
	if !errors.As(err, &apiErr) || apiErr.Op != "movie" || apiErr.Code != 100 {
		t.Fatalf("got %v, want a movie API error 100", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "Invalid API Key") {
		t.Fatalf("error leaks provider details: %v", err)
	}
}
