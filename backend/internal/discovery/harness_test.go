package discovery_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

const (
	testUserA    = "user-a"
	testUserB    = "user-b"
	testApprover = "user-approver"
)

// testPool returns a pool that only sees a fresh, disposable schema.
func testPool(t *testing.T) *pgxpool.Pool {
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
	schema := "discovery_test_" + strings.ToLower(rand.Text())
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

type environment struct {
	pool      *pgxpool.Pool
	movies    *movies.Service
	tv        *tv.Service
	service   *discovery.Service
	handler   http.Handler
	music     *stubMusic
	notified  *notifyLog
	names     *nameLog
	closeOnce sync.Once
}

type envOptions struct {
	music     *stubMusic
	metadata  *omdbFixture
	indexer   *httptest.Server
	withActor bool
	withCan   bool
	withStart bool
	withNames bool
}

func newEnvironment(t *testing.T, opts envOptions) *environment {
	t.Helper()
	for _, name := range []string{"OMDB_URL", "OMDB_API_KEY", "JELLYFIN_URL", "JELLYFIN_API_KEY", "IMPORT_WEBHOOK_URL", "ALLOWED_HOSTS"} {
		t.Setenv(name, "")
	}
	if opts.metadata != nil {
		t.Setenv("OMDB_URL", opts.metadata.URL)
		t.Setenv("OMDB_API_KEY", "synthetic-metadata-key")
	}
	pool := testPool(t)
	ctx := context.Background()
	downloadConfig := downloads.Config{Directory: t.TempDir()}
	if opts.indexer != nil {
		downloadConfig.IndexerURL = opts.indexer.URL + "/api"
		downloadConfig.APIKey = "synthetic-indexer-key"
	}
	manager, err := downloads.New(ctx, pool, downloadConfig)
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	movieService, err := movies.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	tvService, err := tv.New(ctx, pool, manager, movieService.Store)
	if err != nil {
		t.Fatalf("tv.New: %v", err)
	}
	notified := &notifyLog{}
	names := &nameLog{known: map[string]string{testUserA: "Ada Lovelace", testApprover: "Grace Hopper"}}
	options := discovery.Options{Music: opts.music, Notify: notified.record}
	if opts.withNames {
		options.UserName = names.lookup
	}
	if opts.withActor {
		options.Actor = testActor
		options.Can = testCan
	}
	if opts.withCan {
		options.Can = testCan
	}
	service, err := discovery.New(ctx, pool, movieService, tvService, options)
	if err != nil {
		t.Fatalf("discovery.New: %v", err)
	}
	if opts.withStart {
		service.Start(ctx)
	}
	mux := http.NewServeMux()
	service.Register(mux)
	env := &environment{
		pool: pool, movies: movieService, tv: tvService, service: service,
		handler: mux, music: opts.music, notified: notified, names: names,
	}
	t.Cleanup(func() {
		env.closeOnce.Do(func() {
			service.Close()
			movieService.Close()
			tvService.Close()
		})
	})
	return env
}

// testActor reads identity from headers so one test can exercise several users.
func testActor(r *http.Request) (string, bool) {
	return r.Header.Get("X-Test-User"), r.Header.Get("X-Test-Approve") == "1"
}

// testCan resolves operation permissions from a comma-separated test header.
func testCan(r *http.Request, permission string) bool {
	for _, held := range strings.Split(r.Header.Get("X-Test-Perms"), ",") {
		if strings.TrimSpace(held) == permission {
			return true
		}
	}
	return false
}

type callOptions struct {
	user       string
	canApprove bool
	perms      []string
	headers    map[string]string
}

func (e *environment) call(t *testing.T, method, path, body string, opts callOptions) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	if opts.user != "" {
		req.Header.Set("X-Test-User", opts.user)
	}
	if opts.canApprove {
		req.Header.Set("X-Test-Approve", "1")
	}
	if len(opts.perms) > 0 {
		req.Header.Set("X-Test-Perms", strings.Join(opts.perms, ","))
	}
	for key, value := range opts.headers {
		req.Header.Set(key, value)
	}
	e.handler.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes()
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, raw)
	}
	return value
}

// nameLog stands in for the auth directory and counts lookups.
type nameLog struct {
	mu    sync.Mutex
	known map[string]string
	calls []string
}

func (n *nameLog) lookup(_ context.Context, id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, id)
	return n.known[id]
}

func (n *nameLog) has(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, call := range n.calls {
		if call == id {
			return true
		}
	}
	return false
}

type notifyLog struct {
	mu      sync.Mutex
	records []discovery.Request
}

func (n *notifyLog) record(_ context.Context, request discovery.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.records = append(n.records, request)
}

func (n *notifyLog) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.records)
}

func (n *notifyLog) failed() []discovery.Request {
	n.mu.Lock()
	defer n.mu.Unlock()
	failures := []discovery.Request{}
	for _, record := range n.records {
		if record.Delivery.Phase == discovery.PhaseFailed {
			failures = append(failures, record)
		}
	}
	return failures
}

// newIndexerFixture serves one synthetic release so a grab can create a real download job.
func newIndexerFixture(t *testing.T, releaseID, title string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("t") {
		case "movie", "search", "rss":
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><item>
<title>%s</title><guid isPermaLink="false">%s</guid>
<pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate>
<enclosure url="http://indexer.example/%s.nzb" length="4096" type="application/x-nzb"/>
</item></channel></rss>`, title, releaseID, releaseID)
		case "get":
			if r.URL.Query().Get("id") != releaseID {
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

// stubMusic stands in for the music module through the exported hook interface.
type stubMusic struct {
	mu       sync.Mutex
	releases []discovery.MusicRelease
	items    map[string]discovery.MusicItem
	added    []discovery.MusicAddInput
}

func newStubMusic() *stubMusic {
	return &stubMusic{items: map[string]discovery.MusicItem{}}
}

func (m *stubMusic) Search(_ context.Context, query string, limit int) ([]discovery.MusicItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []discovery.MusicItem{}
	for _, item := range m.items {
		if strings.Contains(strings.ToLower(item.Title), strings.ToLower(query)) {
			results = append(results, item)
			if len(results) >= limit {
				break
			}
		}
	}
	return results, nil
}

func (m *stubMusic) Add(_ context.Context, input discovery.MusicAddInput) (discovery.MusicItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.added = append(m.added, input)
	item := discovery.MusicItem{ID: input.ID, Title: input.Title, Artist: "Synthetic Artist", Year: input.Year, Status: "wanted"}
	m.items[input.ID] = item
	return item, nil
}

func (m *stubMusic) Calendar(context.Context) ([]discovery.MusicRelease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]discovery.MusicRelease{}, m.releases...), nil
}

func (m *stubMusic) Status(_ context.Context, id string) (discovery.MusicItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if item, ok := m.items[id]; ok {
		return item, nil
	}
	return discovery.MusicItem{ID: id, Status: "wanted"}, nil
}

// omdbFixture serves the synthetic metadata contract used by the movie and TV services.
type omdbFixture struct {
	*httptest.Server
	mu      sync.Mutex
	healthy bool
	hang    bool
	keys    []string
}

type omdbEntry struct {
	imdbID   string
	title    string
	released string
	kind     string
	year     int
}

var omdbEntries = []omdbEntry{
	{"tt1234567", "Synthetic Lookup", "15 May 2026", "movie", 2026},
	{"tt0133093", "The Matrix", "31 Mar 1999", "movie", 1999},
	{"tt0234215", "The Matrix Reloaded", "15 May 2003", "movie", 2003},
	{"tt2234567", "Synthetic Series", "01 Jan 2026", "series", 2026},
}

func newOMDbFixture(t *testing.T) *omdbFixture {
	t.Helper()
	fixture := &omdbFixture{healthy: true}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		healthy, hang := fixture.healthy, fixture.hang
		if key := r.URL.Query().Get("apikey"); key != "" {
			fixture.keys = append(fixture.keys, key)
		}
		fixture.mu.Unlock()
		if hang {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		if !healthy {
			http.Error(w, "metadata unavailable", http.StatusInternalServerError)
			return
		}
		query := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		if imdbID := query.Get("i"); imdbID != "" {
			if season := query.Get("Season"); season != "" {
				fmt.Fprintf(w, `{"Season":%q,"Episodes":[{"Title":"Synthetic Pilot","Released":"2026-01-01","Episode":"1","imdbID":"tt3234567"}],"Response":"True"}`, season)
				return
			}
			for _, entry := range omdbEntries {
				if entry.imdbID != imdbID {
					continue
				}
				fmt.Fprintf(w, `{"Title":%q,"Year":%q,"Released":%q,"imdbID":%q,"Type":%q,"totalSeasons":"1","Response":"True"}`,
					entry.title, fmt.Sprint(entry.year), entry.released, entry.imdbID, entry.kind)
				return
			}
			fmt.Fprint(w, `{"Response":"False","Error":"Movie not found!"}`)
			return
		}
		kind := query.Get("type")
		search := strings.ToLower(strings.TrimSpace(query.Get("s")))
		items := []string{}
		for _, entry := range omdbEntries {
			if kind != "" && entry.kind != kind {
				continue
			}
			if search != "" && !strings.Contains(strings.ToLower(entry.title), search) {
				continue
			}
			items = append(items, fmt.Sprintf(`{"Title":%q,"Year":%q,"imdbID":%q,"Type":%q}`,
				entry.title, fmt.Sprint(entry.year), entry.imdbID, entry.kind))
		}
		if len(items) == 0 {
			fmt.Fprint(w, `{"Search":[],"Response":"True"}`)
			return
		}
		fmt.Fprintf(w, `{"Search":[%s],"Response":"True"}`, strings.Join(items, ","))
	}))
	t.Cleanup(fixture.Server.Close)
	return fixture
}

func (f *omdbFixture) setHealthy(healthy bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthy = healthy
}

func (f *omdbFixture) setHang(hang bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hang = hang
}

// aiFixture serves the OpenAI-compatible surface used by recommendations.
type aiFixture struct {
	*httptest.Server
	mu       sync.Mutex
	reply    string
	status   int
	hang     bool
	prompts  []string
	auths    []string
	requests int
}

func newAIFixture(t *testing.T, reply string) *aiFixture {
	t.Helper()
	fixture := &aiFixture{reply: reply}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		reply, status, hang := fixture.reply, fixture.status, fixture.hang
		fixture.requests++
		fixture.auths = append(fixture.auths, r.Header.Get("Authorization"))
		fixture.mu.Unlock()
		if hang {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprint(w, `{"data":[{"id":"test-model"},{"id":"other-model"}]}`)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"error":{"message":"boom"}}`)
			return
		}
		var payload struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		fixture.mu.Lock()
		if len(payload.Messages) > 0 {
			fixture.prompts = append(fixture.prompts, payload.Messages[0].Content)
		}
		fixture.mu.Unlock()
		encoded, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
		})
		w.Write(encoded)
	}))
	t.Cleanup(fixture.Server.Close)
	return fixture
}

func (f *aiFixture) set(reply string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply, f.status = reply, status
}

func (f *aiFixture) setHang(hang bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hang = hang
}

func (f *aiFixture) snapshot() (prompts, auths []string, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.prompts...), append([]string{}, f.auths...), f.requests
}

func addMovieRequest(t *testing.T, env *environment, user, imdbID, title string, year int) discovery.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{"mediaType": "movie", "providerId": imdbID, "title": title, "year": year})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	status, raw := env.call(t, http.MethodPost, "/api/v1/requests", string(body), callOptions{user: user})
	if status != http.StatusCreated {
		t.Fatalf("POST requests: status %d, body %s", status, raw)
	}
	return decode[discovery.Request](t, raw)
}
