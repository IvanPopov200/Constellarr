package metadata_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

const testKey = "synthetic-omdb-key"

type requestInfo struct {
	values url.Values
	raw    string
}

func newTestClient(t *testing.T, server *httptest.Server) *metadata.Client {
	t.Helper()
	client, err := metadata.New(server.URL, testKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func serve(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

const searchDocument = `{
  "Search": [
    {"Title": "Big Buck Bunny", "Year": "2008", "imdbID": "tt1234567", "Type": "movie", "Poster": "https://example.com/bbb.jpg"},
    {"Title": "Big Buck Bunny Two", "Year": "2010", "imdbID": "tt7654321", "Type": "movie", "Poster": "N/A"},
    {"Title": "Big Buck Series", "Year": "2015", "imdbID": "tt9999999", "Type": "series", "Poster": "N/A"},
    {"Title": "Broken Identity", "Year": "2015", "imdbID": "not-an-id", "Type": "movie", "Poster": "N/A"}
  ],
  "totalResults": "4",
  "Response": "True"
}`

func TestSearchMapsProviderResults(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, searchDocument)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	titles, err := client.Search(context.Background(), `Big Buck & "Bunny"`, 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	got := <-requests
	wantParams := map[string]string{
		"s": `Big Buck & "Bunny"`, "type": "movie", "page": "3", "apikey": testKey,
	}
	for key, want := range wantParams {
		if got.values.Get(key) != want {
			t.Fatalf("query parameter %s = %q, want %q", key, got.values.Get(key), want)
		}
	}
	if strings.Contains(got.raw, " ") || !strings.Contains(got.raw, "s=Big+Buck") {
		t.Fatalf("query is not escaped: %s", got.raw)
	}

	if len(titles) != 2 {
		t.Fatalf("got %d titles, want 2 movies: %+v", len(titles), titles)
	}
	first := titles[0]
	if first.IMDbID != "tt1234567" || first.Title != "Big Buck Bunny" || first.Year != 2008 ||
		first.Type != "movie" || first.Poster != "https://example.com/bbb.jpg" {
		t.Fatalf("first title = %+v, want the mapped movie", first)
	}
	if titles[1].IMDbID != "tt7654321" || titles[1].Year != 2010 || titles[1].Poster != "" {
		t.Fatalf("second title = %+v, want a mapped result without a poster", titles[1])
	}
	if titles[0].Rating != nil || titles[0].Runtime != 0 || titles[0].Genres != nil {
		t.Fatalf("search title carries detail fields: %+v", titles[0])
	}
}

func TestSearchReturnsEmptyForMissingResults(t *testing.T) {
	server := serve(t, `{"Response": "False", "Error": "Movie not found!"}`, http.StatusOK)
	client := newTestClient(t, server)

	titles, err := client.Search(context.Background(), "nothing matches this", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if titles == nil || len(titles) != 0 {
		t.Fatalf("got %+v, want an empty non-nil result", titles)
	}
}

const detailDocument = `{
  "Title": "Inception",
  "Year": "2010",
  "Rated": "PG-13",
  "Released": "16 Jul 2010",
  "Runtime": "148 min",
  "Genre": "Action, Sci-Fi, Adventure",
  "Director": "Christopher Nolan",
  "Actors": "Leonardo DiCaprio, Joseph Gordon-Levitt, Elliot Page",
  "Plot": "A thief who steals corporate secrets.",
  "Language": "English, Japanese, French",
  "Country": "United States, United Kingdom",
  "Poster": "https://example.com/inception.jpg",
  "imdbRating": "8.8",
  "imdbVotes": "2,500,123",
  "imdbID": "tt1375666",
  "Type": "movie",
  "Response": "True"
}`

func TestLookupMapsFullDetails(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, detailDocument)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	title, err := client.Lookup(context.Background(), "TT1375666")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	got := <-requests
	if got.values.Get("i") != "tt1375666" || got.values.Get("plot") != "full" || got.values.Get("apikey") != testKey {
		t.Fatalf("Lookup sent %s, want an authenticated full-plot lookup", got.raw)
	}

	if title.IMDbID != "tt1375666" || title.Title != "Inception" || title.Year != 2010 || title.Type != "movie" {
		t.Fatalf("identity = %+v", title)
	}
	if title.Released != "2010-07-16" {
		t.Fatalf("Released = %q, want 2010-07-16", title.Released)
	}
	if title.Rating == nil || *title.Rating != 8.8 {
		t.Fatalf("Rating = %v, want 8.8", title.Rating)
	}
	if title.Votes != 2500123 || title.Runtime != 148 {
		t.Fatalf("Votes = %d, Runtime = %d", title.Votes, title.Runtime)
	}
	if title.Certification != "PG-13" || title.Poster != "https://example.com/inception.jpg" || title.Plot == "" {
		t.Fatalf("certification, poster, or plot missing: %+v", title)
	}
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"directors", title.Directors, []string{"Christopher Nolan"}},
		{"cast", title.Cast, []string{"Leonardo DiCaprio", "Joseph Gordon-Levitt", "Elliot Page"}},
		{"genres", title.Genres, []string{"Action", "Sci-Fi", "Adventure"}},
		{"languages", title.Languages, []string{"English", "Japanese", "French"}},
		{"countries", title.Countries, []string{"United States", "United Kingdom"}},
	}
	for _, tc := range cases {
		if len(tc.got) != len(tc.want) {
			t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
		for i := range tc.want {
			if tc.got[i] != tc.want[i] {
				t.Fatalf("%s = %v, want %v", tc.name, tc.got, tc.want)
			}
		}
	}
}

func TestLookupKeepsMissingValuesUnknown(t *testing.T) {
	server := serve(t, `{
      "Title": "Unknown Movie", "Year": "2019", "Rated": "N/A", "Released": "N/A",
      "Runtime": "N/A", "Genre": "N/A", "Director": "N/A", "Actors": "N/A", "Plot": "N/A",
      "Language": "N/A", "Country": "N/A", "Poster": "N/A", "imdbRating": "N/A",
      "imdbVotes": "N/A", "imdbID": "tt0000001", "Type": "movie", "Response": "True"
    }`, http.StatusOK)
	client := newTestClient(t, server)

	title, err := client.Lookup(context.Background(), "tt0000001")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if title.Rating != nil || title.Votes != 0 || title.Runtime != 0 || title.Released != "" ||
		title.Certification != "" || title.Poster != "" || title.Plot != "" {
		t.Fatalf("unknown values were not kept empty: %+v", title)
	}
	for name, list := range map[string][]string{
		"directors": title.Directors, "cast": title.Cast, "genres": title.Genres,
		"languages": title.Languages, "countries": title.Countries,
	} {
		if list != nil {
			t.Fatalf("%s = %v, want unknown", name, list)
		}
	}
}

func TestLookupRejectsNonMovies(t *testing.T) {
	server := serve(t, `{"Title": "A Show", "Year": "2019", "imdbID": "tt1234567", "Type": "series", "Response": "True"}`, http.StatusOK)
	client := newTestClient(t, server)

	_, err := client.Lookup(context.Background(), "tt1234567")
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "not a movie" || providerErr.Op != "lookup" {
		t.Fatalf("got %v, want a not a movie error", err)
	}
}

func TestLookupRejectsWrongIdentity(t *testing.T) {
	server := serve(t, `{"Response":"True","Type":"movie","Title":"Wrong movie","imdbID":"tt7654321"}`, http.StatusOK)
	_, err := newTestClient(t, server).Lookup(context.Background(), "tt1234567")
	var failure *metadata.Error
	if !errors.As(err, &failure) || failure.Kind != "invalid response" {
		t.Fatalf("lookup accepted a different movie identity: %v", err)
	}
}

func TestLookupRejectsMalformedIMDbIDsWithoutRequest(t *testing.T) {
	hits := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	for _, id := range []string{"", "tt123456", "tt1234567890123", "nm0000123", "tt12ab567", "https://www.imdb.com/title/tt1234567/"} {
		if _, err := client.Lookup(context.Background(), id); err == nil {
			t.Fatalf("Lookup(%q) succeeded, want an error", id)
		}
	}
	select {
	case <-hits:
		t.Fatal("malformed IMDb ID reached the provider")
	default:
	}
}

func TestValidIMDbID(t *testing.T) {
	valid := []string{"tt0000001", "tt1234567", "tt123456789012"}
	for _, id := range valid {
		if !metadata.ValidIMDbID(id) {
			t.Fatalf("ValidIMDbID(%q) = false, want true", id)
		}
	}
	invalid := []string{"", "t", "tt", "tt123456", "tt1234567890123", "TT1234567", " tt1234567", "tt1234567 ", "nm1234567", "tt123456a", "https://www.imdb.com/title/tt1234567/"}
	for _, id := range invalid {
		if metadata.ValidIMDbID(id) {
			t.Fatalf("ValidIMDbID(%q) = true, want false", id)
		}
	}
}

func TestProviderErrorsAreSanitizedAndActionable(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantKind   string
		wantStatus int
		wantWord   string
		retry      bool
	}{
		{"rejected key", http.StatusUnauthorized, `{"Response":"False","Error":"Invalid API Key!"}`, "api key", http.StatusUnauthorized, "API key", false},
		{"rate limited key status", http.StatusUnauthorized, `{"Response":"False","Error":"Request limit reached!"}`, "rate limited", http.StatusUnauthorized, "retry later", true},
		{"rate limited status", http.StatusTooManyRequests, `Request limit reached for synthetic-omdb-key`, "rate limited", http.StatusTooManyRequests, "retry later", true},
		{"rate limited envelope", http.StatusOK, `{"Response":"False","Error":"Request limit reached!"}`, "rate limited", 0, "retry later", true},
		{"unavailable", http.StatusBadGateway, `upstream synthetic-omdb-key failure`, "unavailable", http.StatusBadGateway, "retry later", true},
		{"unknown provider error", http.StatusOK, `{"Response":"False","Error":"Some unexpected provider detail"}`, "request failed", 0, "metadata search", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := serve(t, tc.body, tc.status)
			client := newTestClient(t, server)

			_, err := client.Search(context.Background(), "inception", 1)
			var providerErr *metadata.Error
			if !errors.As(err, &providerErr) || providerErr.Kind != tc.wantKind || providerErr.Op != "search" {
				t.Fatalf("got %v, want kind %q", err, tc.wantKind)
			}
			if providerErr.Status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", providerErr.Status, tc.wantStatus)
			}
			if providerErr.Retry != tc.retry {
				t.Fatalf("Retry = %v, want %v", providerErr.Retry, tc.retry)
			}
			if !strings.Contains(err.Error(), tc.wantWord) {
				t.Fatalf("error %q does not explain %q", err, tc.wantWord)
			}
			for _, leak := range []string{testKey, "Invalid API Key", "Request limit", "upstream", "unexpected provider"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("error leaks %q: %v", leak, err)
				}
			}
		})
	}
}

func TestLookupReportNotFound(t *testing.T) {
	server := serve(t, `{"Response":"False","Error":"Movie not found!"}`, http.StatusOK)
	client := newTestClient(t, server)

	_, err := client.Lookup(context.Background(), "tt1234567")
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "not found" {
		t.Fatalf("got %v, want a not found error", err)
	}
	if strings.Contains(err.Error(), "Movie not found") {
		t.Fatalf("error leaks provider text: %v", err)
	}
}

func TestTestUsesAuthenticatedSearch(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, `{"Search":[{"Title":"Test","Year":"2000","imdbID":"tt1234567","Type":"movie"}],"Response":"True"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	if err := client.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	got := <-requests
	if got.values.Get("s") == "" || got.values.Get("type") != "movie" || got.values.Get("apikey") != testKey {
		t.Fatalf("Test sent %s, want an authenticated movie search", got.raw)
	}
}

func TestTestFailsOnRejectedKey(t *testing.T) {
	server := serve(t, `{"Response":"False","Error":"Invalid API Key!"}`, http.StatusUnauthorized)
	client := newTestClient(t, server)

	err := client.Test(context.Background())
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "api key" || providerErr.Op != "test" {
		t.Fatalf("got %v, want a test api key error", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "Invalid API Key") {
		t.Fatalf("error leaks provider details: %v", err)
	}
}

func TestSearchValidatesInputWithoutRequest(t *testing.T) {
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
		{"", 1}, {"   ", 1}, {strings.Repeat("q", 257), 1}, {"valid", 0}, {"valid", -1}, {"valid", 101},
	} {
		if _, err := client.Search(context.Background(), tc.query, tc.page); err == nil {
			t.Fatalf("Search(%d chars, page %d) succeeded, want an error", len(tc.query), tc.page)
		}
	}
	select {
	case <-hits:
		t.Fatal("invalid input reached the provider")
	default:
	}
}

func TestSearchRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 4<<20+1))
	}))
	defer server.Close()
	client := newTestClient(t, server)

	_, err := client.Search(context.Background(), "anything", 1)
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "response too large" {
		t.Fatalf("got %v, want a response too large error", err)
	}
}

func TestSearchRejectsMalformedResponse(t *testing.T) {
	server := serve(t, `<html><body>maintenance synthetic-omdb-key</body></html>`, http.StatusOK)
	client := newTestClient(t, server)

	_, err := client.Search(context.Background(), "anything", 1)
	var providerErr *metadata.Error
	if !errors.As(err, &providerErr) || providerErr.Kind != "invalid response" {
		t.Fatalf("got %v, want an invalid response error", err)
	}
	if strings.Contains(err.Error(), "maintenance") || strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks provider details: %v", err)
	}
}

func TestRedirectsStayOnConfiguredOrigin(t *testing.T) {
	t.Run("same origin", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/api", http.StatusFound)
				return
			}
			fmt.Fprint(w, searchDocument)
		}))
		defer server.Close()
		client := newTestClient(t, server)

		if _, err := client.Search(context.Background(), "anything", 1); err != nil {
			t.Fatalf("same-origin redirect: %v", err)
		}
	})

	t.Run("cross origin", func(t *testing.T) {
		targetHits := make(chan struct{}, 1)
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetHits <- struct{}{}
		}))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/api", http.StatusFound)
		}))
		defer server.Close()
		client := newTestClient(t, server)

		_, err := client.Search(context.Background(), "anything", 1)
		var providerErr *metadata.Error
		if !errors.As(err, &providerErr) || providerErr.Kind != "cross-origin redirect" {
			t.Fatalf("got %v, want a cross-origin redirect error", err)
		}
		if strings.Contains(err.Error(), testKey) {
			t.Fatalf("redirect error leaks the API key: %v", err)
		}
		select {
		case <-targetHits:
			t.Fatal("client followed a cross-origin redirect with credentials")
		default:
		}
	})
}

func TestSearchHonoursContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Search(cancelled, "anything", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: got %v, want context.Canceled", err)
	}
}

func TestNewValidatesConfiguration(t *testing.T) {
	invalid := []string{"/api", "https:///api", "ftp://example.com", "https://user:hunter2@example.com", "https://example.com/?apikey=hunter2"}
	for _, base := range invalid {
		if _, err := metadata.New(base, testKey); err == nil {
			t.Fatalf("New(%q) succeeded, want an error", base)
		} else if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("error leaks URL credentials: %v", err)
		}
	}
	if _, err := metadata.New("https://www.omdbapi.com", ""); err == nil {
		t.Fatal("New with an empty key succeeded, want an error")
	}
	client, err := metadata.New("", testKey)
	if err != nil {
		t.Fatalf("New with the default endpoint: %v", err)
	}
	if client == nil {
		t.Fatal("New returned no client")
	}
}
