package music

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

const musicFeedBody = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <item>
      <title>Muse - Absolution (2003) [FLAC] [24bit]</title>
      <guid>https://api.example/get/abc123</guid>
      <pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate>
      <enclosure url="https://api.example/get/abc123" length="450000000" type="application/x-nzb"/>
      <newznab:attr name="size" value="450000000"/>
      <newznab:attr name="category" value="3040"/>
      <newznab:attr name="artist" value="Muse"/>
      <newznab:attr name="album" value="Absolution"/>
      <newznab:attr name="year" value="2003"/>
    </item>
    <item>
      <title>Some Movie 2020 1080p BluRay</title>
      <guid>movie-1</guid>
      <newznab:attr name="size" value="8000000000"/>
      <newznab:attr name="category" value="2040"/>
    </item>
    <item>
      <title>Missing GUID release</title>
      <newznab:attr name="size" value="1"/>
      <newznab:attr name="category" value="3010"/>
    </item>
  </channel>
</rss>`

func TestNewznabMusicSearchContract(t *testing.T) {
	var requests []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Query())
		w.Header().Set("Content-Type", "application/rss+xml")
		if r.URL.Query().Get("artist") != "" && r.URL.Query().Get("q") == "" {
			_, _ = w.Write([]byte(`<rss><channel><item><title>Unrelated recent album FLAC</title><guid>recent</guid></item></channel></rss>`))
			return
		}
		_, _ = w.Write([]byte(musicFeedBody))
	}))
	defer server.Close()
	client, err := newNewznab(downloads.Config{IndexerURL: server.URL + "/api", APIKey: "synthetic-key"})
	if err != nil {
		t.Fatalf("newNewznab: %v", err)
	}
	ctx := context.Background()

	releases, err := client.SearchAlbum(ctx, "Muse", "Absolution", 2003)
	if err != nil {
		t.Fatalf("SearchAlbum: %v", err)
	}
	if len(releases) != 1 {
		t.Fatalf("releases = %d, want the single audio item", len(releases))
	}
	release := releases[0]
	if release.ID != "abc123" || release.Size != 450000000 || release.Year != 2003 {
		t.Fatalf("release fields = %+v", release)
	}
	if release.Artist != "Muse" || release.Album != "Absolution" {
		t.Fatalf("audio attributes were not parsed: %+v", release)
	}
	if release.Published.IsZero() {
		t.Fatal("publication date was not parsed")
	}
	query := requests[0]
	for key, want := range map[string]string{"t": "music", "cat": "3000", "q": "Muse Absolution", "artist": "Muse", "album": "Absolution", "year": "2003", "apikey": "synthetic-key"} {
		if got := query.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q (%v)", key, got, want, query)
		}
	}

	if _, err := client.SearchQuery(ctx, "muse absolution"); err != nil {
		t.Fatalf("SearchQuery: %v", err)
	}
	if got := requests[1].Get("q"); got != "muse absolution" {
		t.Fatalf("free-text query = %q", got)
	}
	if _, err := client.Feed(ctx); err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if requests[2].Get("t") != "music" || requests[2].Get("cat") != "3000" {
		t.Fatalf("feed request = %v", requests[2])
	}
}

func TestNewznabFallsBackToMusicCategorySearch(t *testing.T) {
	var functions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		function := r.URL.Query().Get("t")
		functions = append(functions, function)
		w.Header().Set("Content-Type", "application/rss+xml")
		if function == "music" {
			_, _ = w.Write([]byte(`<?xml version="1.0"?><error code="203" description="Function not available"/>`))
			return
		}
		if function != "search" {
			t.Errorf("unexpected function %q", function)
		}
		if r.URL.Query().Get("q") != "Muse Absolution" {
			t.Error("the fallback lost the album search query")
		}
		_, _ = w.Write([]byte(musicFeedBody))
	}))
	defer server.Close()
	client, err := newNewznab(downloads.Config{IndexerURL: server.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("newNewznab: %v", err)
	}
	releases, err := client.SearchAlbum(context.Background(), "Muse", "Absolution", 2003)
	if err != nil {
		t.Fatalf("SearchQuery: %v (functions: %v)", err, functions)
	}
	if len(releases) != 1 {
		t.Fatalf("releases = %d", len(releases))
	}
	if len(functions) != 2 || functions[0] != "music" || functions[1] != "search" {
		t.Fatalf("fallback functions = %v", functions)
	}
}

func TestNewznabErrorsAndBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("q") {
		case "broken":
			_, _ = w.Write([]byte("<rss><channel><item>"))
			return
		case "missing":
			http.Error(w, "nope", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?><error code="100" description="Invalid API key"/>`))
	}))
	defer server.Close()
	client, err := newNewznab(downloads.Config{IndexerURL: server.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("newNewznab: %v", err)
	}
	ctx := context.Background()
	if _, err := client.SearchQuery(ctx, "broken"); err == nil || strings.Contains(err.Error(), "<rss>") {
		t.Fatalf("malformed feed error = %v", err)
	}
	if _, err := client.SearchQuery(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("HTTP error = %v", err)
	}
	if _, err := client.SearchQuery(ctx, "other"); err == nil || !strings.Contains(err.Error(), "100") {
		t.Fatalf("API error = %v", err)
	}
	if _, err := client.SearchQuery(ctx, "  "); err == nil {
		t.Fatal("empty query was accepted")
	}
	if _, err := client.SearchQuery(ctx, strings.Repeat("q", maxMusicQueryLen+1)); err == nil {
		t.Fatal("overlong query was accepted")
	}
}

func TestNewznabRequiresConfiguration(t *testing.T) {
	if _, err := newNewznab(downloads.Config{IndexerURL: ""}); err != ErrNotConfigured {
		t.Fatalf("missing indexer URL error = %v, want ErrNotConfigured", err)
	}
	if _, err := newNewznab(downloads.Config{IndexerURL: "ftp://indexer.example/api"}); err == nil {
		t.Fatal("non-HTTP indexer URL was accepted")
	}
}

func TestReleaseCategoryFiltering(t *testing.T) {
	cases := []struct {
		name    string
		item    musicItem
		isMusic bool
	}{
		{"audio subcategory", musicItem{Attributes: []musicAttr{{Name: "category", Value: "3040"}}}, true},
		{"audio range", musicItem{Categories: []string{"Audio > Lossless"}}, true},
		{"movie", musicItem{Attributes: []musicAttr{{Name: "category", Value: "2040"}}}, false},
		{"tv", musicItem{Attributes: []musicAttr{{Name: "category", Value: "5040"}}}, false},
		{"unknown", musicItem{}, true},
	}
	for _, test := range cases {
		if got := test.item.isMusic(); got != test.isMusic {
			t.Fatalf("%s: isMusic = %v, want %v", test.name, got, test.isMusic)
		}
	}
}
