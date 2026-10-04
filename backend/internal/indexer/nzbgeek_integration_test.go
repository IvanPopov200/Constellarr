package indexer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
)

const testKey = "synthetic-key-123"

type requestInfo struct {
	values url.Values
	raw    string
}

func newTestClient(t *testing.T, server *httptest.Server) *indexer.Client {
	t.Helper()
	client, err := indexer.New(server.URL+"/api", testKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

const searchFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <title>NZBgeek</title>
    <item>
      <title>Big Buck Bunny 2008 1080p BluRay x264</title>
      <guid isPermaLink="true">https://api.nzbgeek.info/details/abc123DEF456</guid>
      <pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate>
      <enclosure url="https://api.nzbgeek.info/api?t=get&amp;id=abc123DEF456&amp;apikey=synthetic-key-123" length="0" type="application/x-nzb"/>
      <newznab:attr name="category" value="2040"/>
      <newznab:attr name="size" value="734003200"/>
      <newznab:attr name="guid" value="abc123DEF456"/>
    </item>
    <item>
      <title>Ubuntu 24.04 Server</title>
      <guid>https://api.nzbgeek.info/details/XYZ-987_most_wanted</guid>
      <pubDate>not a date</pubDate>
      <enclosure url="https://api.nzbgeek.info/api?t=get&amp;id=XYZ-987_most_wanted&amp;apikey=synthetic-key-123" length="52428800" type="application/x-nzb"/>
      <attr name="size" value="not-a-number"/>
    </item>
  </channel>
</rss>`

func TestSearchSendsAuthenticatedQueryAndParsesFeed(t *testing.T) {
	const query = `Big Buck Bunny & "More" <2008>`
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		fmt.Fprint(w, searchFeed)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	releases, err := client.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	got := <-requests
	wantParams := map[string]string{
		"t": "search", "q": query, "cat": "2000", "limit": "50", "extended": "1", "apikey": testKey,
	}
	for key, want := range wantParams {
		if got.values.Get(key) != want {
			t.Fatalf("query parameter %s = %q, want %q", key, got.values.Get(key), want)
		}
	}
	if strings.Contains(got.raw, " ") || !strings.Contains(got.raw, "q=Big+Buck+Bunny") {
		t.Fatalf("query is not escaped: %s", got.raw)
	}

	want := []indexer.Release{
		{
			ID:        "abc123DEF456",
			Title:     "Big Buck Bunny 2008 1080p BluRay x264",
			Size:      734003200,
			Published: time.Date(2006, time.January, 2, 15, 4, 5, 0, time.UTC),
		},
		{ID: "XYZ-987_most_wanted", Title: "Ubuntu 24.04 Server", Size: 52428800},
	}
	if len(releases) != len(want) {
		t.Fatalf("got %d releases, want %d: %+v", len(releases), len(want), releases)
	}
	for i := range want {
		if releases[i].ID != want[i].ID || releases[i].Title != want[i].Title ||
			releases[i].Size != want[i].Size || !releases[i].Published.Equal(want[i].Published) {
			t.Fatalf("release %d = %+v, want %+v", i, releases[i], want[i])
		}
	}

	encoded, err := json.Marshal(releases[0])
	if err != nil {
		t.Fatalf("marshal release: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("release JSON is invalid: %s", encoded)
	}
	if len(fields) != 4 {
		t.Fatalf("release JSON has %d fields, want 4: %s", len(fields), encoded)
	}
	for _, key := range []string{"id", "title", "size", "published"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("release JSON is missing %q: %s", key, encoded)
		}
	}
	if strings.Contains(string(encoded), "http") || strings.Contains(string(encoded), testKey) {
		t.Fatalf("release JSON exposes an enclosure URL or key: %s", encoded)
	}
}

func TestSearchSkipsReleasesWithoutSafeIdentifiers(t *testing.T) {
	longID := strings.Repeat("a", 129)
	feed := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <item>
      <title>Unsafe identifier</title>
      <guid>https://api.nzbgeek.info/details/also bad!</guid>
      <newznab:attr name="guid" value="bad id!"/>
    </item>
    <item>
      <title></title>
      <newznab:attr name="guid" value="validbutuntitled"/>
    </item>
    <item>
      <title>Too long</title>
      <guid>https://api.nzbgeek.info/details/%s</guid>
    </item>
    <item>
      <title>Kept release</title>
      <guid>https://api.nzbgeek.info/getnzb/kept-id-1.nzb</guid>
      <enclosure length="1024"/>
    </item>
  </channel>
</rss>`, longID)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, feed)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	releases, err := client.Search(context.Background(), "kept")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(releases) != 1 || releases[0].ID != "kept-id-1" || releases[0].Title != "Kept release" || releases[0].Size != 1024 {
		t.Fatalf("got %+v, want only the kept release", releases)
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	hits := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	for _, query := range []string{"", "   ", strings.Repeat("q", 257)} {
		if _, err := client.Search(context.Background(), query); err == nil {
			t.Fatalf("Search(%d chars) succeeded, want an error", len(query))
		}
	}
	select {
	case <-hits:
		t.Fatal("invalid query reached the indexer")
	default:
	}
}

func TestSearchReportsAPIErrorWithoutLeakingDetails(t *testing.T) {
	const query = "dune part two 2160p"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<error code="100" description="Invalid API Key"/>`)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	_, err := client.Search(context.Background(), query)
	var apiErr *indexer.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %T (%v), want *indexer.Error", err, err)
	}
	if apiErr.Op != "search" || apiErr.Code != 100 {
		t.Fatalf("got %+v, want search API error 100", apiErr)
	}
	for _, leak := range []string{testKey, query, "Invalid API Key", "<error"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q: %v", leak, err)
		}
	}
}

func TestSearchReportsHTTPStatusWithoutLeakingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failure "+testKey, http.StatusInternalServerError)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	_, err := client.Search(context.Background(), "anything")
	var apiErr *indexer.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("got %v, want HTTP 500 error", err)
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "upstream failure") {
		t.Fatalf("error leaks response details: %v", err)
	}
}

func TestTestUsesAuthenticatedSearchWithLimitOne(t *testing.T) {
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		fmt.Fprint(w, `<rss version="2.0"><channel><title>NZBgeek</title></channel></rss>`)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	if err := client.Test(context.Background()); err != nil {
		t.Fatalf("Test: %v", err)
	}
	got := <-requests
	if got.values.Get("t") != "search" || got.values.Get("limit") != "1" || got.values.Get("apikey") != testKey {
		t.Fatalf("Test sent %s, want an authenticated search with limit=1", got.raw)
	}
	if got.values.Get("q") != "" {
		t.Fatalf("Test sent a query: %s", got.raw)
	}
}

func TestTestFailsOnAPIErrorEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `<error code="100" description="Invalid API Key"/>`)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	err := client.Test(context.Background())
	var apiErr *indexer.Error
	if !errors.As(err, &apiErr) || apiErr.Op != "test" || apiErr.Code != 100 {
		t.Fatalf("got %v, want a test API error 100", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}
}

const nzbDocument = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head>
    <meta type="title">Big Buck Bunny</meta>
  </head>
  <file poster="poster &lt;poster@example.com&gt;" date="1136214245" subject="[1/1] - &quot;bbb.par2&quot; yEnc (1/1)">
    <groups><group>alt.binaries.test</group></groups>
    <segments><segment bytes="123456" number="1">abcdef123@example.com</segment></segments>
  </file>
</nzb>`

func TestNZBRetrievesDocumentWithAuthenticatedGet(t *testing.T) {
	const id = "abc123DEF456"
	requests := make(chan requestInfo, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- requestInfo{values: r.URL.Query(), raw: r.URL.RawQuery}
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprint(w, nzbDocument)
	}))
	defer server.Close()
	client := newTestClient(t, server)

	document, err := client.NZB(context.Background(), id)
	if err != nil {
		t.Fatalf("NZB: %v", err)
	}
	if string(document) != nzbDocument {
		t.Fatalf("NZB returned %d bytes, want the document verbatim", len(document))
	}
	got := <-requests
	if got.values.Get("t") != "get" || got.values.Get("id") != id || got.values.Get("apikey") != testKey {
		t.Fatalf("NZB sent %s, want an authenticated get for %s", got.raw, id)
	}
}

func TestNZBReportsErrorsWithoutLeakingDetails(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode int
		wantKind string
	}{
		{"api error envelope", `<error code="300" description="No such release"/>`, 300, "api"},
		{"unexpected document", `<html><body>maintenance synthetic-key-123</body></html>`, 0, "invalid response"},
		{"malformed document", `not xml at all`, 0, "invalid response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client := newTestClient(t, server)

			_, err := client.NZB(context.Background(), "abc123DEF456")
			var apiErr *indexer.Error
			if !errors.As(err, &apiErr) {
				t.Fatalf("got %T (%v), want *indexer.Error", err, err)
			}
			if apiErr.Op != "nzb" || apiErr.Code != tc.wantCode || apiErr.Kind != tc.wantKind {
				t.Fatalf("got %+v, want op nzb, code %d, kind %q", apiErr, tc.wantCode, tc.wantKind)
			}
			for _, leak := range []string{testKey, "No such release", "maintenance", "<error"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("error leaks %q: %v", leak, err)
				}
			}
		})
	}
}

func TestNZBRejectsUnsafeIdentifier(t *testing.T) {
	hits := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
	}))
	defer server.Close()
	client := newTestClient(t, server)

	for _, id := range []string{"", "bad id!", "a/b", strings.Repeat("a", 129)} {
		if _, err := client.NZB(context.Background(), id); err == nil {
			t.Fatalf("NZB(%q) succeeded, want an error", id)
		}
	}
	select {
	case <-hits:
		t.Fatal("unsafe identifier reached the indexer")
	default:
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	cases := []struct {
		name string
		base string
	}{
		{"relative URL", "/api"},
		{"missing host", "https:///api"},
		{"unsupported scheme", "ftp://example.com/api"},
		{"embedded credentials", "https://user:hunter2@example.com/api"},
		{"query in base URL", "https://example.com/api?apikey=hunter2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := indexer.New(tc.base, testKey)
			if err == nil {
				t.Fatalf("New(%q) succeeded, want an error", tc.base)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("error leaks URL credentials: %v", err)
			}
		})
	}
}

func TestRedirectsStayOnConfiguredOrigin(t *testing.T) {
	t.Run("same origin", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api" {
				http.Redirect(w, r, "/api/feed", http.StatusFound)
				return
			}
			fmt.Fprint(w, `<rss version="2.0"><channel></channel></rss>`)
		}))
		defer server.Close()
		client := newTestClient(t, server)

		if _, err := client.Search(context.Background(), "anything"); err != nil {
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

		_, err := client.Search(context.Background(), "anything")
		var apiErr *indexer.Error
		if !errors.As(err, &apiErr) || apiErr.Kind != "cross-origin redirect" {
			t.Fatalf("got %v, want a redirect error", err)
		}
		if strings.Contains(err.Error(), testKey) {
			t.Fatalf("redirect error leaks the API key: %v", err)
		}
		select {
		case <-targetHits:
			t.Fatal("client followed a cross-origin redirect")
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
	if _, err := client.Search(cancelled, "anything"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: got %v, want context.Canceled", err)
	}

	expired, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Search(expired, "anything"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired context: got %v, want context.DeadlineExceeded", err)
	}
}

func TestSearchRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), 16<<20+1))
	}))
	defer server.Close()
	client := newTestClient(t, server)

	_, err := client.Search(context.Background(), "anything")
	var apiErr *indexer.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != "response too large" {
		t.Fatalf("got %v, want a response too large error", err)
	}
}
