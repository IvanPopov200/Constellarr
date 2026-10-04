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

const tvFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <item>
      <title>Big Buck Series S02E03 1080p WEB-DL-GROUP</title>
      <guid>https://api.nzbgeek.info/details/series-1</guid>
      <pubDate>Mon, 02 Jan 2012 15:04:05 +0000</pubDate>
      <newznab:attr name="size" value="1073741824"/>
      <newznab:attr name="imdbid" value="1111111"/>
      <newznab:attr name="season" value="2"/>
      <newznab:attr name="episode" value="3"/>
      <newznab:attr name="tvdbid" value="123456"/>
    </item>
    <item>
      <title>Big Buck Series S02 1080p WEB-DL-GROUP2</title>
      <guid>https://api.nzbgeek.info/details/series-2</guid>
      <newznab:attr name="SEASON" value="not-a-number"/>
      <newznab:attr name="Episode" value="0"/>
      <newznab:attr name="tvdb" value="tt-123456"/>
    </item>
    <item>
      <title>Big Buck Series S01E04 720p HDTV-GROUP3</title>
      <guid>https://api.nzbgeek.info/details/series-3</guid>
      <newznab:attr name="season" value="1"/>
      <newznab:attr name="episode" value="4"/>
    </item>
  </channel>
</rss>`

func TestSearchTVSendsEpisodeQuery(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, tvFeed)
	}))
	defer server.Close()

	releases, err := newTestClient(t, server).SearchTV(context.Background(), "tt1111111", "Big Buck Series", 2, 3)
	if err != nil {
		t.Fatalf("SearchTV: %v", err)
	}
	got := <-requests
	wantParams := map[string]string{
		"t": "tvsearch", "q": "Big Buck Series", "cat": "5000", "limit": "50",
		"extended": "1", "season": "2", "ep": "3", "apikey": testKey,
	}
	for key, want := range wantParams {
		if got.values.Get(key) != want {
			t.Fatalf("query parameter %s = %q, want %q in %s", key, got.values.Get(key), want, got.raw)
		}
	}
	if got.values.Get("imdbid") != "" {
		t.Fatalf("SearchTV sent an IMDb ID that providers may not support: %s", got.raw)
	}
	if len(releases) != 3 {
		t.Fatalf("got %d releases, want 3", len(releases))
	}
	first := releases[0]
	if first.IMDbID != "tt1111111" || first.Season != 2 || first.Episode != 3 || first.TVDBID != "123456" {
		t.Fatalf("first release = %+v, want IMDb, season, episode, and TVDB attributes", first)
	}
	if first.Size != 1073741824 || first.Title != "Big Buck Series S02E03 1080p WEB-DL-GROUP" {
		t.Fatalf("first release = %+v", first)
	}
	if releases[1].Season != 0 || releases[1].Episode != 0 || releases[1].TVDBID != "" {
		t.Fatalf("invalid attributes were not kept unknown: %+v", releases[1])
	}
	if releases[2].Season != 1 || releases[2].Episode != 4 || releases[2].TVDBID != "" {
		t.Fatalf("third release = %+v", releases[2])
	}
}

func TestSearchTVOmitsEpisodeForSeasonPack(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, tvFeed)
	}))
	defer server.Close()

	if _, err := newTestClient(t, server).SearchTV(context.Background(), "", "Big Buck Series", 2, 0); err != nil {
		t.Fatalf("SearchTV: %v", err)
	}
	got := <-requests
	if got.values.Get("season") != "2" || got.values.Get("ep") != "" || got.values.Get("q") != "Big Buck Series" {
		t.Fatalf("season pack sent %s, want a season without an episode", got.raw)
	}
	if got.values.Get("imdbid") != "" {
		t.Fatalf("season pack sent an empty IMDb ID: %s", got.raw)
	}
}

func TestSearchTVOmitsSeasonForAllSeries(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, tvFeed)
	}))
	defer server.Close()

	if _, err := newTestClient(t, server).SearchTV(context.Background(), "tt1111111", "Big Buck Series", -1, 0); err != nil {
		t.Fatalf("SearchTV: %v", err)
	}
	got := <-requests
	if got.values.Get("season") != "" || got.values.Get("ep") != "" || got.values.Get("q") != "Big Buck Series" {
		t.Fatalf("all-series search sent %s, want no season or episode", got.raw)
	}
}

func TestSearchTVRejectsUnusableInput(t *testing.T) {
	hits := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	cases := []struct {
		imdbID  string
		title   string
		season  int
		episode int
	}{
		{"tt1111111", "", 1, 1},
		{"", "", 1, 1},
		{"not-an-id", "Big Buck Series", 1, 1},
		{"tt123", "Big Buck Series", 1, 1},
		{"tt1111111", strings.Repeat("x", 257), 1, 1},
		{"tt1111111", "Big Buck Series", 0, 1001},
		{"tt1111111", "Big Buck Series", -2, 0},
		{"tt1111111", "Big Buck Series", 101, 0},
		{"tt1111111", "Big Buck Series", 2, -1},
		{"tt1111111", "Big Buck Series", 2, 1001},
		{"tt1111111", "Big Buck Series", -1, 5},
	}
	for _, tc := range cases {
		if _, err := client.SearchTV(context.Background(), tc.imdbID, tc.title, tc.season, tc.episode); err == nil {
			t.Fatalf("SearchTV(%q, %d chars, season %d, episode %d) succeeded, want an error",
				tc.imdbID, len(tc.title), tc.season, tc.episode)
		}
	}
	select {
	case <-hits:
		t.Fatal("unusable input reached the indexer")
	default:
	}
}

func TestSearchTVSupportsSpecialsSeason(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, tvFeed)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	if _, err := client.SearchTV(context.Background(), "", "Big Buck Series", 0, 0); err != nil {
		t.Fatalf("SearchTV: %v", err)
	}
	got := <-requests
	if got.values.Get("season") != "0" || got.values.Get("ep") != "" || got.values.Get("q") != "Big Buck Series" {
		t.Fatalf("specials pack sent %s, want season 0 without an episode", got.raw)
	}

	if _, err := client.SearchTV(context.Background(), "", "Big Buck Series", 0, 2); err != nil {
		t.Fatalf("SearchTV: %v", err)
	}
	got = <-requests
	if got.values.Get("season") != "0" || got.values.Get("ep") != "2" {
		t.Fatalf("specials episode sent %s, want season 0 with the episode", got.raw)
	}
}

// NZBGeek advertises tv-search supportedParams q,rid,tvdbid,tvmazeid,season,ep; IMDb IDs are not among them.
func TestSearchTVStaysWithinProviderCaps(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, tvFeed)
	}))
	defer server.Close()

	if _, err := newTestClient(t, server).SearchTV(context.Background(), "tt1111111", "Big Buck Series", 2, 3); err != nil {
		t.Fatalf("SearchTV: %v", err)
	}
	got := <-requests
	allowed := map[string]bool{"t": true, "q": true, "cat": true, "limit": true, "extended": true, "season": true, "ep": true, "apikey": true}
	for key := range got.values {
		if !allowed[key] {
			t.Fatalf("SearchTV sent parameter %s outside the verified provider contract: %s", key, got.raw)
		}
	}
	for _, unsupported := range []string{"imdbid", "rid", "tvdbid", "tvmazeid"} {
		if got.values.Get(unsupported) != "" {
			t.Fatalf("SearchTV sent unsupported parameter %s: %s", unsupported, got.raw)
		}
	}
}

func TestRSSTVUsesRecentTVFeed(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, tvFeed)
	}))
	defer server.Close()

	releases, err := newTestClient(t, server).RSSTV(context.Background())
	if err != nil {
		t.Fatalf("RSSTV: %v", err)
	}
	got := <-requests
	if got.values.Get("t") != "search" || got.values.Get("cat") != "5000" ||
		got.values.Get("extended") != "1" || got.values.Get("apikey") != testKey {
		t.Fatalf("RSSTV sent %s, want an extended TV feed", got.raw)
	}
	if got.values.Get("q") != "" {
		t.Fatalf("RSSTV sent a query: %s", got.raw)
	}
	if len(releases) != 3 || releases[0].Season != 2 || releases[0].Episode != 3 {
		t.Fatalf("RSSTV parsed %+v", releases)
	}
}

func TestSearchTVReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<error code="100" description="Invalid API Key"/>`)
	}))
	defer server.Close()

	_, err := newTestClient(t, server).SearchTV(context.Background(), "tt1111111", "Big Buck Series", 2, 3)
	var apiErr *indexer.Error
	if !errors.As(err, &apiErr) || apiErr.Op != "tv" || apiErr.Code != 100 {
		t.Fatalf("got %v, want a tv API error 100", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "Invalid API Key") {
		t.Fatalf("error leaks provider details: %v", err)
	}
}

func TestReleaseJSONCarriesOptionalTVFields(t *testing.T) {
	encoded, err := json.Marshal(indexer.Release{ID: "x", Title: "y", Season: 2, Episode: 3, TVDBID: "123456"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"season":2`, `"episode":3`, `"tvdbId":"123456"`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON %s lacks %s", encoded, want)
		}
	}
	encoded, err = json.Marshal(indexer.Release{ID: "x", Title: "y"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, absent := range []string{"season", "episode", "tvdbId"} {
		if strings.Contains(string(encoded), absent) {
			t.Fatalf("unknown TV field %s was serialized: %s", absent, encoded)
		}
	}
}
