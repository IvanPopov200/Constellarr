package metadata_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

const seriesSearchDocument = `{
  "Search": [
    {"Title": "Big Buck Series", "Year": "2011–2019", "imdbID": "tt1111111", "Type": "series", "Poster": "https://example.com/series.jpg"},
    {"Title": "Big Buck Movie", "Year": "2011", "imdbID": "tt2222222", "Type": "movie", "Poster": "N/A"},
    {"Title": "Broken Identity", "Year": "2015", "imdbID": "not-an-id", "Type": "series", "Poster": "N/A"}
  ],
  "totalResults": "3",
  "Response": "True"
}`

func TestSearchSeriesMapsSeriesOnly(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, seriesSearchDocument)
	}))
	defer server.Close()

	titles, err := newTestClient(t, server).SearchSeries(context.Background(), `Big Buck & "Series"`, 2)
	if err != nil {
		t.Fatalf("SearchSeries: %v", err)
	}
	got := <-requests
	wantParams := map[string]string{
		"s": `Big Buck & "Series"`, "type": "series", "page": "2", "apikey": testKey,
	}
	for key, want := range wantParams {
		if got.values.Get(key) != want {
			t.Fatalf("query parameter %s = %q, want %q", key, got.values.Get(key), want)
		}
	}
	if len(titles) != 1 {
		t.Fatalf("got %d titles, want 1 series: %+v", len(titles), titles)
	}
	if titles[0].IMDbID != "tt1111111" || titles[0].Type != "series" || titles[0].Year != 2011 ||
		titles[0].Poster != "https://example.com/series.jpg" || titles[0].TotalSeasons != 0 {
		t.Fatalf("series title = %+v, want the mapped multi-year series", titles[0])
	}
}

func TestSearchSeriesValidatesInputWithoutRequest(t *testing.T) {
	hits := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	for _, tc := range []struct {
		query string
		page  int
	}{
		{"", 1}, {"   ", 1}, {strings.Repeat("q", 257), 1}, {"valid", 0}, {"valid", 101},
	} {
		if _, err := client.SearchSeries(context.Background(), tc.query, tc.page); err == nil {
			t.Fatalf("SearchSeries(%d chars, page %d) succeeded, want an error", len(tc.query), tc.page)
		}
	}
	select {
	case <-hits:
		t.Fatal("invalid input reached the provider")
	default:
	}
}

func TestSearchSeriesReturnsEmptyWhenProviderFindsNothing(t *testing.T) {
	server := serve(t, `{"Response": "False", "Error": "Series not found!"}`, http.StatusOK)

	titles, err := newTestClient(t, server).SearchSeries(context.Background(), "nothing matches this", 1)
	if err != nil {
		t.Fatalf("SearchSeries: %v", err)
	}
	if titles == nil || len(titles) != 0 {
		t.Fatalf("got %+v, want an empty non-nil result", titles)
	}
}

func TestSearchSeriesReportsProviderErrors(t *testing.T) {
	server := serve(t, `{"Response":"False","Error":"Invalid API Key!"}`, http.StatusUnauthorized)

	_, err := newTestClient(t, server).SearchSeries(context.Background(), "big buck series", 1)
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "api key" || providerErr.Op != "series search" {
		t.Fatalf("got %v, want a series search api key error", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "Invalid API Key") {
		t.Fatalf("error leaks provider details: %v", err)
	}
}

const seriesDocument = `{
  "Title": "Big Buck Series",
  "Year": "2011–2019",
  "Rated": "TV-MA",
  "Released": "17 Apr 2011",
  "Runtime": "57 min",
  "Genre": "Action, Adventure, Drama",
  "Director": "N/A",
  "Actors": "Actor One, Actor Two",
  "Plot": "A synthetic series.",
  "Language": "English",
  "Country": "United States",
  "Poster": "https://example.com/series.jpg",
  "imdbRating": "9.2",
  "imdbVotes": "1,234,567",
  "imdbID": "tt1111111",
  "Type": "series",
  "totalSeasons": "8",
  "Response": "True"
}`

func TestLookupSeriesMapsSeriesDetails(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, seriesDocument)
	}))
	defer server.Close()

	title, err := newTestClient(t, server).LookupSeries(context.Background(), "TT1111111")
	if err != nil {
		t.Fatalf("LookupSeries: %v", err)
	}
	got := <-requests
	if got.values.Get("i") != "tt1111111" || got.values.Get("plot") != "full" || got.values.Get("apikey") != testKey {
		t.Fatalf("LookupSeries sent %s, want an authenticated full-plot lookup", got.raw)
	}
	if title.IMDbID != "tt1111111" || title.Title != "Big Buck Series" || title.Type != "series" {
		t.Fatalf("identity = %+v", title)
	}
	if title.Year != 2011 || title.TotalSeasons != 8 {
		t.Fatalf("Year = %d, TotalSeasons = %d, want 2011 and 8", title.Year, title.TotalSeasons)
	}
	if title.Rating == nil || *title.Rating != 9.2 || title.Runtime != 57 || title.Released != "2011-04-17" {
		t.Fatalf("series details = %+v", title)
	}
}

func TestLookupSeriesRejectsMovies(t *testing.T) {
	server := serve(t, `{"Title": "A Movie", "Year": "2019", "imdbID": "tt1111111", "Type": "movie", "Response": "True"}`, http.StatusOK)

	_, err := newTestClient(t, server).LookupSeries(context.Background(), "tt1111111")
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "not a series" || providerErr.Op != "series lookup" {
		t.Fatalf("got %v, want a not a series error", err)
	}
	if !strings.Contains(err.Error(), "not a series") {
		t.Fatalf("error does not explain the type mismatch: %v", err)
	}
}

func TestLookupSeriesRejectsWrongIdentity(t *testing.T) {
	server := serve(t, `{"Response":"True","Type":"series","Title":"Wrong series","imdbID":"tt7654321"}`, http.StatusOK)

	_, err := newTestClient(t, server).LookupSeries(context.Background(), "tt1111111")
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "invalid response" {
		t.Fatalf("lookup accepted a different series identity: %v", err)
	}
}

func TestLookupSeriesNormalizesSeasonCounts(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"8", 8}, {"150", 100}, {"0", 0}, {"N/A", 0}, {"", 0}, {"many", 0},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			body := fmt.Sprintf(`{"Title":"A Show","Year":"2011–2019","imdbID":"tt1111111","Type":"series","totalSeasons":%q,"Response":"True"}`, tc.raw)
			server := serve(t, body, http.StatusOK)

			title, err := newTestClient(t, server).LookupSeries(context.Background(), "tt1111111")
			if err != nil {
				t.Fatalf("LookupSeries: %v", err)
			}
			if title.TotalSeasons != tc.want {
				t.Fatalf("TotalSeasons = %d, want %d", title.TotalSeasons, tc.want)
			}
		})
	}
}

const seasonDocument = `{
  "Title": "Big Buck Series",
  "Season": "2",
  "seriesID": "tt1111111",
  "totalSeasons": "8",
  "Episodes": [
    {"Title": "First Episode", "Released": "2012-04-01", "Episode": "1", "imdbRating": "8.5", "imdbID": "tt3000001"},
    {"Title": "Second Episode", "Released": "N/A", "Episode": "2", "imdbRating": "N/A", "imdbID": "tt3000002"},
    {"Title": "Third Episode", "Released": "not a date", "Episode": "3", "imdbRating": "11.0", "imdbID": "tt3000003"},
    {"Title": "No Identity", "Released": "2012-04-15", "Episode": "4", "imdbRating": "7.1", "imdbID": "N/A"}
  ],
  "Response": "True"
}`

func TestSeasonMapsEpisodes(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, seasonDocument)
	}))
	defer server.Close()

	episodes, err := newTestClient(t, server).Season(context.Background(), "TT1111111", 2)
	if err != nil {
		t.Fatalf("Season: %v", err)
	}
	got := <-requests
	if got.values.Get("i") != "tt1111111" || got.values.Get("Season") != "2" || got.values.Get("apikey") != testKey {
		t.Fatalf("Season sent %s, want an authenticated season lookup", got.raw)
	}
	if len(episodes) != 3 {
		t.Fatalf("got %d episodes, want 3 with the identity-less entry dropped: %+v", len(episodes), episodes)
	}
	first := episodes[0]
	if first.IMDbID != "tt3000001" || first.Title != "First Episode" || first.Season != 2 || first.Number != 1 {
		t.Fatalf("first episode = %+v", first)
	}
	if first.AirDate != "2012-04-01" || first.Rating == nil || *first.Rating != 8.5 {
		t.Fatalf("first episode date or rating = %+v", first)
	}
	second := episodes[1]
	if second.Number != 2 || second.AirDate != "" || second.Rating != nil || second.Season != 2 {
		t.Fatalf("second episode = %+v, want empty date and unknown rating, got unknown=%v", second, second.Rating != nil)
	}
	third := episodes[2]
	if third.Number != 3 || third.AirDate != "" || third.Rating != nil {
		t.Fatalf("third episode = %+v, want an empty date and no out-of-range rating", third)
	}
}

func TestSeasonSupportsSpecials(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, `{
      "Title": "Big Buck Series", "Season": "0",
      "Episodes": [{"Title": "Special One", "Released": "2010-12-24", "Episode": "1", "imdbRating": "7.5", "imdbID": "tt4000001"}],
      "Response": "True"
    }`)
	}))
	defer server.Close()

	episodes, err := newTestClient(t, server).Season(context.Background(), "tt1111111", 0)
	if err != nil {
		t.Fatalf("Season: %v", err)
	}
	got := <-requests
	if got.values.Get("Season") != "0" || got.values.Get("i") != "tt1111111" || got.values.Get("apikey") != testKey {
		t.Fatalf("Season sent %s, want an authenticated specials lookup", got.raw)
	}
	if len(episodes) != 1 || episodes[0].Season != 0 || episodes[0].Number != 1 || episodes[0].AirDate != "2010-12-24" {
		t.Fatalf("specials = %+v", episodes)
	}
}

func TestSeasonAcceptsEpisodeNumberAtBound(t *testing.T) {
	server := serve(t, `{"Season":"2","Episodes":[{"Title":"Last Episode","Episode":"1000","imdbID":"tt3001000"}],"Response":"True"}`, http.StatusOK)

	episodes, err := newTestClient(t, server).Season(context.Background(), "tt1111111", 2)
	if err != nil {
		t.Fatalf("Season: %v", err)
	}
	if len(episodes) != 1 || episodes[0].Number != 1000 || episodes[0].Season != 2 {
		t.Fatalf("episodes = %+v, want episode number 1000 accepted", episodes)
	}
}

func TestSeasonReportsMissingSpecials(t *testing.T) {
	server := serve(t, `{"Response":"False","Error":"Series not found!"}`, http.StatusOK)

	_, err := newTestClient(t, server).Season(context.Background(), "tt1111111", 0)
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "not found" || providerErr.Op != "season" {
		t.Fatalf("got %v, want a not found specials error", err)
	}
}

func TestSeasonReturnsEmptyEpisodeList(t *testing.T) {
	server := serve(t, `{"Title":"An Empty Show","Season":"2","Episodes":[],"Response":"True"}`, http.StatusOK)

	episodes, err := newTestClient(t, server).Season(context.Background(), "tt1111111", 2)
	if err != nil {
		t.Fatalf("Season: %v", err)
	}
	if episodes == nil || len(episodes) != 0 {
		t.Fatalf("got %+v, want an empty non-nil result", episodes)
	}
}

func TestSeasonRejectsMismatchedResponses(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"season", `{"Title":"A Show","Season":"3","Episodes":[],"Response":"True"}`},
		{"series", `{"Title":"A Show","Season":"2","seriesID":"tt9999999","Episodes":[],"Response":"True"}`},
		{"missing season", `{"Title":"A Show","Episodes":[],"Response":"True"}`},
		{"missing response marker", `{"Episodes":[]}`},
		{"duplicate episode numbers", `{"Season":"2","Episodes":[{"Episode":"1","imdbID":"tt3000001"},{"Episode":"1","imdbID":"tt3000002"}],"Response":"True"}`},
		{"non-numeric episode number", `{"Season":"2","Episodes":[{"Episode":"two","imdbID":"tt3000001"}],"Response":"True"}`},
		{"zero episode number", `{"Season":"2","Episodes":[{"Episode":"0","imdbID":"tt3000001"}],"Response":"True"}`},
		{"episode number over bound", `{"Season":"2","Episodes":[{"Episode":"1001","imdbID":"tt3000001"}],"Response":"True"}`},
		{"invalid series identity", `{"Season":"2","seriesID":"not-an-id","Episodes":[],"Response":"True"}`},
		{"invalid imdb identity", `{"Season":"2","imdbID":"N/A","Episodes":[],"Response":"True"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := serve(t, tc.body, http.StatusOK)

			_, err := newTestClient(t, server).Season(context.Background(), "tt1111111", 2)
			var providerErr *metadata.Error
			if !errors.As(err, &providerErr) || providerErr.Kind != "invalid response" || providerErr.Op != "season" {
				t.Fatalf("got %v, want an invalid season response", err)
			}
		})
	}
}

func TestSeasonRejectsOversizedEpisodeLists(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"Season":"2","Episodes":[`)
	for i := 1; i <= 1001; i++ {
		if i > 1 {
			builder.WriteString(",")
		}
		fmt.Fprintf(&builder, `{"Episode":"%d","imdbID":"tt300%04d"}`, i, i)
	}
	builder.WriteString(`],"Response":"True"}`)
	server := serve(t, builder.String(), http.StatusOK)

	_, err := newTestClient(t, server).Season(context.Background(), "tt1111111", 2)
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "invalid response" {
		t.Fatalf("got %v, want an invalid response error", err)
	}
}

func TestSeasonValidatesInputWithoutRequest(t *testing.T) {
	hits := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	for _, tc := range []struct {
		imdbID string
		season int
	}{
		{"", 1}, {"tt123456", 1}, {"nm0000123", 1}, {"tt1111111", -1}, {"tt1111111", 101},
	} {
		if _, err := client.Season(context.Background(), tc.imdbID, tc.season); err == nil {
			t.Fatalf("Season(%q, %d) succeeded, want an error", tc.imdbID, tc.season)
		}
	}
	select {
	case <-hits:
		t.Fatal("invalid input reached the provider")
	default:
	}
}

func TestSeasonReportsProviderErrors(t *testing.T) {
	server := serve(t, `{"Response":"False","Error":"Series not found!"}`, http.StatusOK)

	_, err := newTestClient(t, server).Season(context.Background(), "tt1111111", 9)
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "not found" || providerErr.Op != "season" {
		t.Fatalf("got %v, want a not found season error", err)
	}
	if strings.Contains(err.Error(), "Series not found") {
		t.Fatalf("error leaks provider text: %v", err)
	}
}

func TestTitleJSONCarriesOptionalTotalSeasons(t *testing.T) {
	encoded, err := json.Marshal(metadata.Title{IMDbID: "tt1111111", Type: "series", TotalSeasons: 8})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"totalSeasons":8`) {
		t.Fatalf("JSON lacks totalSeasons: %s", encoded)
	}
	encoded, err = json.Marshal(metadata.Title{IMDbID: "tt1111111", Type: "movie"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "totalSeasons") {
		t.Fatalf("unknown season count was serialized: %s", encoded)
	}
}

func TestEpisodeJSONCarriesSeasonIdentity(t *testing.T) {
	encoded, err := json.Marshal(metadata.Episode{IMDbID: "tt3000001", Title: "First", Season: 2, Number: 1, AirDate: "2012-04-01"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"imdbId":"tt3000001"`, `"season":2`, `"number":1`, `"airDate":"2012-04-01"`, `"rating":null`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("JSON %s lacks %s", encoded, want)
		}
	}
}
