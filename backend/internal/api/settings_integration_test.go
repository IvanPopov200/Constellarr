package api_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/api"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

const (
	indexerAPIKey  = "synthetic-indexer-key"
	usenetPassword = "synthetic-usenet-password"
)

const indexerRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><item>
<title>%s</title><guid isPermaLink="false">%s</guid><pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate>
<enclosure url="http://indexer.example/%s.nzb" length="1024" type="application/x-nzb"/>
</item></channel></rss>`

const indexerNZB = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`

func TestSettingsRoundtripAndSecretRetention(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	directory := t.TempDir()
	indexer := newIndexerServer(t, "release-a")
	manager := settingsManager(t, pool, downloads.Config{
		IndexerURL: indexer.URL + "/api", APIKey: indexerAPIKey, Directory: directory,
		Usenet: usenet.Config{
			Host: "bootstrap.example", Port: 563, Username: "bootstrap-user",
			Password: usenetPassword, Connections: 6, FallbackHosts: []string{"backup.example"},
		},
	})
	handler := api.New(pool, api.Services{Downloads: manager})

	status, raw := request(t, handler, http.MethodGet, "/api/v1/settings", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings: status %d, body %s", status, raw)
	}
	view := decodeSettingsView(t, raw)
	if view.Indexer.URL != indexer.URL+"/api" || !view.Indexer.APIKeyConfigured {
		t.Fatalf("bootstrap indexer = %+v", view.Indexer)
	}
	if view.Usenet.Host != "bootstrap.example" || view.Usenet.Port != 563 || view.Usenet.Username != "bootstrap-user" ||
		!view.Usenet.PasswordConfigured || view.Usenet.Connections != 6 {
		t.Fatalf("bootstrap usenet = %+v", view.Usenet)
	}
	if strings.Join(view.Usenet.FallbackHosts, ",") != "backup.example" {
		t.Fatalf("bootstrap fallbacks = %v", view.Usenet.FallbackHosts)
	}
	if view.Storage.Directory != directory {
		t.Fatalf("storage directory = %q, want %q", view.Storage.Directory, directory)
	}
	assertHidden(t, raw, indexerAPIKey, usenetPassword)

	// Blank secrets keep stored values while the remaining fields are replaced.
	status, raw = request(t, handler, http.MethodPut, "/api/v1/settings",
		encodeSettings(t, indexer.URL+"/api2", "", "news.example", 8119, "", "", 12, []string{"news2.example"}), nil)
	if status != http.StatusOK {
		t.Fatalf("PUT settings: status %d, body %s", status, raw)
	}
	view = decodeSettingsView(t, raw)
	if view.Indexer.URL != indexer.URL+"/api2" || !view.Indexer.APIKeyConfigured {
		t.Fatalf("updated indexer = %+v", view.Indexer)
	}
	if view.Usenet.Host != "news.example" || view.Usenet.Port != 8119 || view.Usenet.Username != "" ||
		!view.Usenet.PasswordConfigured || view.Usenet.Connections != 12 {
		t.Fatalf("updated usenet = %+v", view.Usenet)
	}
	if strings.Join(view.Usenet.FallbackHosts, ",") != "news2.example" {
		t.Fatalf("updated fallbacks = %v", view.Usenet.FallbackHosts)
	}
	assertHidden(t, raw, indexerAPIKey, usenetPassword)

	var rows int
	var storedURL, storedKey, storedPassword string
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM settings`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("settings rows = %d, %v; want exactly one", rows, err)
	}
	if err := pool.QueryRow(ctx, `SELECT indexer_url, indexer_api_key, usenet_password FROM settings`).
		Scan(&storedURL, &storedKey, &storedPassword); err != nil {
		t.Fatalf("read settings row: %v", err)
	}
	if storedURL != indexer.URL+"/api2" || storedKey != indexerAPIKey || storedPassword != usenetPassword {
		t.Fatal("persisted settings do not match the update")
	}

	// A restart applies the persisted row over environment bootstrap values.
	restartDirectory := t.TempDir()
	restarted := settingsManager(t, pool, downloads.Config{
		IndexerURL: "https://bootstrap.invalid/api", APIKey: "bootstrap-key", Directory: restartDirectory,
		Usenet: usenet.Config{
			Host: "bootstrap.example", Port: 563, Username: "bootstrap-user",
			Password: "bootstrap-password", Connections: 1,
		},
	})
	status, raw = request(t, api.New(pool, api.Services{Downloads: restarted}), http.MethodGet, "/api/v1/settings", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings after restart: status %d, body %s", status, raw)
	}
	view = decodeSettingsView(t, raw)
	if view.Indexer.URL != indexer.URL+"/api2" || !view.Indexer.APIKeyConfigured {
		t.Fatalf("restart indexer = %+v, want the persisted source", view.Indexer)
	}
	if view.Usenet.Host != "news.example" || view.Usenet.Port != 8119 || view.Usenet.Connections != 12 {
		t.Fatalf("restart usenet = %+v, want the persisted source", view.Usenet)
	}
	if view.Storage.Directory != restartDirectory {
		t.Fatalf("storage directory = %q, want the runtime value %q", view.Storage.Directory, restartDirectory)
	}

	// A failed save must leave the running configuration untouched.
	pool.Close()
	status, raw = request(t, handler, http.MethodPut, "/api/v1/settings",
		encodeSettings(t, "https://unreachable.invalid/api", "", "news.example", 563, "", "", 8, nil), nil)
	if status != http.StatusBadGateway {
		t.Fatalf("PUT with an unavailable database: status %d, body %s", status, raw)
	}
	status, raw = request(t, handler, http.MethodGet, "/api/v1/settings", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET settings after a failed save: status %d, body %s", status, raw)
	}
	if view = decodeSettingsView(t, raw); view.Indexer.URL != indexer.URL+"/api2" {
		t.Fatalf("runtime settings changed after a failed save: %+v", view.Indexer)
	}
}

func TestSettingsUpdateKeepsCrossInstanceRotation(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	indexer := newIndexerServer(t, "release-a")
	rotating := settingsManager(t, pool, downloads.Config{
		IndexerURL: indexer.URL + "/api", APIKey: "bootstrap-key-a", Directory: t.TempDir(),
		Usenet: usenet.Config{Host: "a.example", Port: 563, Username: "user-a", Password: "password-a", Connections: 4},
	})
	directory := t.TempDir()
	stale := settingsManager(t, pool, downloads.Config{
		IndexerURL: indexer.URL + "/api", APIKey: "bootstrap-key-b", Directory: directory,
		Usenet: usenet.Config{Host: "b.example", Port: 563, Username: "user-b", Password: "password-b", Connections: 4},
	})

	if err := rotating.UpdateSettings(ctx, downloads.SettingsUpdate{
		IndexerURL: indexer.URL + "/api", APIKey: "rotated-key",
		UsenetHost: "news.example", UsenetPort: 563, UsenetUsername: "rotated-user", UsenetPassword: "rotated-password",
		Connections: 8,
	}); err != nil {
		t.Fatalf("rotate settings: %v", err)
	}

	// The stale instance still holds bootstrap values and saves without secrets.
	if err := stale.UpdateSettings(ctx, downloads.SettingsUpdate{
		IndexerURL: indexer.URL + "/api2", UsenetHost: "news2.example", UsenetPort: 8119, Connections: 12,
	}); err != nil {
		t.Fatalf("blank update: %v", err)
	}

	var storedKey, storedPassword string
	if err := pool.QueryRow(ctx, `SELECT indexer_api_key, usenet_password FROM settings`).Scan(&storedKey, &storedPassword); err != nil {
		t.Fatalf("read settings row: %v", err)
	}
	if storedKey != "rotated-key" || storedPassword != "rotated-password" {
		t.Fatalf("blank update rolled back rotated credentials: key %q, password %q", storedKey, storedPassword)
	}
	cfg := stale.Config()
	if cfg.APIKey != "rotated-key" || cfg.Usenet.Password != "rotated-password" {
		t.Fatalf("stale instance activated stale credentials: key %q, password %q", cfg.APIKey, cfg.Usenet.Password)
	}
	if cfg.IndexerURL != indexer.URL+"/api2" || cfg.Usenet.Host != "news2.example" || cfg.Usenet.Port != 8119 || cfg.Usenet.Connections != 12 {
		t.Fatalf("stale instance did not apply the update: %+v", cfg)
	}
	if cfg.Directory != directory {
		t.Fatalf("storage directory = %q, want the local runtime value %q", cfg.Directory, directory)
	}
}

func TestSettingsUpdateKeepsOversizedBootstrapSecrets(t *testing.T) {
	pool := testSchema(t)
	ctx := context.Background()
	indexer := newIndexerServer(t, "release-a")
	long := strings.Repeat("k", 600)
	manager := settingsManager(t, pool, downloads.Config{
		IndexerURL: indexer.URL + "/api", APIKey: long, Directory: t.TempDir(),
		Usenet: usenet.Config{Host: "long.example", Port: 563, Username: "long-user", Password: long, Connections: 2},
	})
	handler := api.New(pool, api.Services{Downloads: manager})

	// Blank secrets retain trusted bootstrap values even when they exceed the request limit.
	status, raw := request(t, handler, http.MethodPut, "/api/v1/settings",
		encodeSettings(t, indexer.URL+"/api", "", "long2.example", 563, "long-user", "", 4, nil), nil)
	if status != http.StatusOK {
		t.Fatalf("PUT with retained oversized secrets: status %d, body %s", status, raw)
	}
	if view := decodeSettingsView(t, raw); !view.Indexer.APIKeyConfigured || !view.Usenet.PasswordConfigured {
		t.Fatalf("retained secrets reported missing: %+v", view)
	}
	assertHidden(t, raw, long)
	var storedKey, storedPassword string
	if err := pool.QueryRow(ctx, `SELECT indexer_api_key, usenet_password FROM settings`).Scan(&storedKey, &storedPassword); err != nil {
		t.Fatalf("read settings row: %v", err)
	}
	if storedKey != long || storedPassword != long {
		t.Fatal("saved secrets changed after a blank update")
	}
}

func TestSettingsValidationAndRequestEnforcement(t *testing.T) {
	pool := testSchema(t)
	indexer := newIndexerServer(t, "release-a")
	manager := settingsManager(t, pool, downloads.Config{
		IndexerURL: indexer.URL + "/api", APIKey: indexerAPIKey, Directory: t.TempDir(),
	})
	handler := api.New(pool, api.Services{Downloads: manager})

	valid := func() map[string]any {
		return map[string]any{
			"indexer": map[string]any{"url": indexer.URL + "/api", "apiKey": "put-key"},
			"usenet": map[string]any{
				"host": "news.example", "port": 563, "username": "put-user",
				"password": "put-password", "connections": 8, "fallbackHosts": []string{},
			},
		}
	}
	encode := func(edit func(map[string]any)) string {
		body := valid()
		if edit != nil {
			edit(body)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal settings body: %v", err)
		}
		return string(raw)
	}
	indexerField := func(body map[string]any) map[string]any { return body["indexer"].(map[string]any) }
	usenetField := func(body map[string]any) map[string]any { return body["usenet"].(map[string]any) }
	validBody := encode(nil)

	cases := []struct{ name, body string }{
		{"malformed JSON", "{"},
		{"empty body", ""},
		{"unknown top-level field", encode(func(b map[string]any) { b["storage"] = map[string]any{"directory": "/srv/media"} })},
		{"unknown nested field", encode(func(b map[string]any) { usenetField(b)["tls"] = true })},
		{"wrong field type", encode(func(b map[string]any) { usenetField(b)["fallbackHosts"] = "backup.example" })},
		{"trailing document", validBody + validBody},
		{"oversized body", encode(func(b map[string]any) {
			indexerField(b)["url"] = "https://indexer.example/" + strings.Repeat("a", 20<<10)
		})},
		{"blank indexer URL", encode(func(b map[string]any) { indexerField(b)["url"] = "" })},
		{"non-HTTP indexer URL", encode(func(b map[string]any) { indexerField(b)["url"] = "ftp://indexer.example/api" })},
		{"indexer URL with credentials", encode(func(b map[string]any) {
			indexerField(b)["url"] = "https://user:pass@indexer.example/api"
		})},
		{"indexer URL with query", encode(func(b map[string]any) { indexerField(b)["url"] = "https://indexer.example/api?x=1" })},
		{"indexer URL with fragment", encode(func(b map[string]any) { indexerField(b)["url"] = "https://indexer.example/api#top" })},
		{"blank usenet host", encode(func(b map[string]any) { usenetField(b)["host"] = " " })},
		{"usenet host with port", encode(func(b map[string]any) { usenetField(b)["host"] = "news.example:563" })},
		{"port below range", encode(func(b map[string]any) { usenetField(b)["port"] = 0 })},
		{"port above range", encode(func(b map[string]any) { usenetField(b)["port"] = 70000 })},
		{"connections below range", encode(func(b map[string]any) { usenetField(b)["connections"] = 0 })},
		{"connections above range", encode(func(b map[string]any) { usenetField(b)["connections"] = 33 })},
		{"too many fallbacks", encode(func(b map[string]any) {
			usenetField(b)["fallbackHosts"] = []string{
				"a1.example", "a2.example", "a3.example", "a4.example", "a5.example",
				"a6.example", "a7.example", "a8.example", "a9.example",
			}
		})},
		{"invalid fallback host", encode(func(b map[string]any) { usenetField(b)["fallbackHosts"] = []string{"bad/host"} })},
		{"username without password", encode(func(b map[string]any) { usenetField(b)["password"] = "" })},
		{"oversized supplied API key", encode(func(b map[string]any) { indexerField(b)["apiKey"] = strings.Repeat("k", 600) })},
		{"oversized supplied password", encode(func(b map[string]any) { usenetField(b)["password"] = strings.Repeat("p", 600) })},
	}
	for _, tc := range cases {
		status, raw := request(t, handler, http.MethodPut, "/api/v1/settings", tc.body, nil)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s; want 400", tc.name, status, raw)
		}
		assertHidden(t, raw, "put-key", "put-password")
	}

	// PUT shares POST's Origin and JSON enforcement.
	for _, contentType := range []string{"", "text/plain"} {
		if status, raw := request(t, handler, http.MethodPut, "/api/v1/settings", validBody, map[string]string{"Content-Type": contentType}); status != http.StatusUnsupportedMediaType {
			t.Errorf("PUT with content type %q: status %d, body %s; want 415", contentType, status, raw)
		}
	}
	evil := map[string]string{"Origin": "http://evil.example"}
	if status, raw := request(t, handler, http.MethodPut, "/api/v1/settings", validBody, evil); status != http.StatusForbidden {
		t.Errorf("PUT from a foreign origin: status %d, body %s; want 403", status, raw)
	}
	if status, raw := request(t, handler, http.MethodPost, "/api/v1/sources/test", validBody, evil); status != http.StatusForbidden {
		t.Errorf("POST from a foreign origin: status %d, body %s; want 403", status, raw)
	}
	status, raw := request(t, handler, http.MethodPut, "/api/v1/settings", validBody, map[string]string{"Origin": "http://localhost"})
	if status != http.StatusOK {
		t.Fatalf("PUT from the request origin: status %d, body %s", status, raw)
	}
	assertHidden(t, raw, "put-key", "put-password")

	for name, origin := range map[string]string{
		"foreign host":   "http://other.example",
		"invalid scheme": "ftp://localhost",
		"opaque origin":  "null",
	} {
		if status, raw := request(t, handler, http.MethodPut, "/api/v1/settings", validBody, map[string]string{"Origin": origin}); status != http.StatusForbidden {
			t.Errorf("%s: status %d, body %s; want 403", name, status, raw)
		}
	}
	// Scheme is not compared with the request so TLS-terminating proxies keep working.
	for name, origin := range map[string]string{
		"case-insensitive host": "HTTP://LOCALHOST",
		"proxy-terminated TLS":  "https://localhost",
	} {
		if status, raw := request(t, handler, http.MethodPut, "/api/v1/settings", validBody, map[string]string{"Origin": origin}); status != http.StatusOK {
			t.Errorf("%s: status %d, body %s; want 200", name, status, raw)
		}
	}

	// Duplicates are removed before the unique fallback cap is enforced.
	status, raw = request(t, handler, http.MethodPut, "/api/v1/settings", encode(func(b map[string]any) {
		usenetField(b)["fallbackHosts"] = []string{
			"a1.example", "a1.example", "a2.example", "a3.example", "a4.example",
			"a5.example", "a6.example", "a7.example", "a8.example",
		}
	}), nil)
	if status != http.StatusOK {
		t.Fatalf("PUT with duplicate fallbacks: status %d, body %s", status, raw)
	}
	if view := decodeSettingsView(t, raw); strings.Join(view.Usenet.FallbackHosts, ",") != "a1.example,a2.example,a3.example,a4.example,a5.example,a6.example,a7.example,a8.example" {
		t.Fatalf("deduplicated fallbacks = %v", view.Usenet.FallbackHosts)
	}
}

func TestSettingsAffectServicesWithoutRestart(t *testing.T) {
	pool := testSchema(t)
	first := newIndexerServer(t, "release-first")
	second := newIndexerServer(t, "release-second")
	manager := settingsManager(t, pool, downloads.Config{
		IndexerURL: first.URL + "/api", APIKey: "first-key", Directory: t.TempDir(),
	})
	handler := api.New(pool, api.Services{Downloads: manager})

	status, raw := request(t, handler, http.MethodGet, "/api/v1/releases?q=dune", "", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), "release-first") {
		t.Fatalf("search before the update: status %d, body %s", status, raw)
	}

	status, raw = request(t, handler, http.MethodPut, "/api/v1/settings",
		encodeSettings(t, second.URL+"/api", "second-key", "news.example", 563, "", "", 8, nil), nil)
	if status != http.StatusOK {
		t.Fatalf("PUT settings: status %d, body %s", status, raw)
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/releases?q=dune", "", nil)
	if status != http.StatusOK || !strings.Contains(string(raw), "release-second") {
		t.Fatalf("search after the update: status %d, body %s", status, raw)
	}

	// Source tests run against the saved configuration.
	status, raw = request(t, handler, http.MethodPost, "/api/v1/sources/test", "", nil)
	if status != http.StatusOK {
		t.Fatalf("POST sources/test: status %d, body %s", status, raw)
	}
	var tests struct {
		Indexer struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"indexer"`
		Usenet struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		} `json:"usenet"`
	}
	if err := json.Unmarshal(raw, &tests); err != nil {
		t.Fatalf("sources/test response is not JSON: %v (%s)", err, raw)
	}
	if !tests.Indexer.OK || tests.Indexer.Error != "" {
		t.Fatalf("saved indexer test = %+v", tests.Indexer)
	}
	if tests.Usenet.OK {
		t.Fatalf("unconfigured usenet reported success: %+v", tests.Usenet)
	}

	// New downloads fetch their NZB through the new indexer.
	status, raw = request(t, handler, http.MethodPost, "/api/v1/downloads",
		`{"releaseId":"release-second","title":"Second release"}`, nil)
	if status != http.StatusCreated || !strings.Contains(string(raw), "release-second") {
		t.Fatalf("create download: status %d, body %s", status, raw)
	}

	status, raw = request(t, handler, http.MethodGet, "/api/v1/sources", "", nil)
	if status != http.StatusOK {
		t.Fatalf("GET sources: status %d, body %s", status, raw)
	}
	var sources struct {
		Indexer struct {
			Configured bool `json:"configured"`
		} `json:"indexer"`
		Usenet struct {
			Host        string `json:"host"`
			Port        int    `json:"port"`
			Connections int    `json:"connections"`
			Configured  bool   `json:"configured"`
		} `json:"usenet"`
	}
	if err := json.Unmarshal(raw, &sources); err != nil {
		t.Fatalf("sources response is not JSON: %v (%s)", err, raw)
	}
	if !sources.Indexer.Configured {
		t.Fatalf("indexer not reported as configured: %s", raw)
	}
	if sources.Usenet.Host != "news.example" || sources.Usenet.Port != 563 || sources.Usenet.Connections != 8 {
		t.Fatalf("sources usenet = %+v, want the updated settings", sources.Usenet)
	}
}

type settingsView struct {
	Indexer struct {
		URL              string `json:"url"`
		APIKeyConfigured bool   `json:"apiKeyConfigured"`
	} `json:"indexer"`
	Usenet struct {
		Host               string   `json:"host"`
		Port               int      `json:"port"`
		Username           string   `json:"username"`
		PasswordConfigured bool     `json:"passwordConfigured"`
		Connections        int      `json:"connections"`
		FallbackHosts      []string `json:"fallbackHosts"`
	} `json:"usenet"`
	Storage struct {
		Directory string `json:"directory"`
	} `json:"storage"`
}

func decodeSettingsView(t *testing.T, raw []byte) settingsView {
	t.Helper()
	var view settingsView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("settings response is not JSON: %v (%s)", err, raw)
	}
	return view
}

func encodeSettings(t *testing.T, url, apiKey, host string, port int, username, password string, connections int, fallbacks []string) string {
	t.Helper()
	if fallbacks == nil {
		fallbacks = []string{}
	}
	raw, err := json.Marshal(map[string]any{
		"indexer": map[string]any{"url": url, "apiKey": apiKey},
		"usenet": map[string]any{
			"host": host, "port": port, "username": username, "password": password,
			"connections": connections, "fallbackHosts": fallbacks,
		},
	})
	if err != nil {
		t.Fatalf("marshal settings body: %v", err)
	}
	return string(raw)
}

func assertHidden(t *testing.T, body []byte, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(string(body), secret) {
			t.Fatalf("response leaks a secret: %s", body)
		}
	}
}

func request(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes()
}

type syntheticIndexer struct {
	*httptest.Server
}

func newIndexerServer(t *testing.T, id string) *syntheticIndexer {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("t") {
		case "search":
			fmt.Fprintf(w, indexerRSS, "Synthetic "+id, id, id)
		case "get":
			if r.URL.Query().Get("id") != id {
				http.Error(w, "unexpected release", http.StatusBadRequest)
				return
			}
			fmt.Fprint(w, indexerNZB)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return &syntheticIndexer{Server: server}
}

func settingsManager(t *testing.T, pool *pgxpool.Pool, cfg downloads.Config) *downloads.Manager {
	t.Helper()
	manager, err := downloads.New(context.Background(), pool, cfg)
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	return manager
}

// testSchema creates an isolated schema and a pool scoped to it, leaving real tables untouched.
func testSchema(t *testing.T) *pgxpool.Pool {
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
	schema := "api_settings_test_" + strings.ToLower(rand.Text())
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
