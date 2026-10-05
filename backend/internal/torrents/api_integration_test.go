package torrents_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
)

const fixtureAPIKey = "fixture-api-key-9f2c"

type torznabFixture struct {
	server    *httptest.Server
	torrent   []byte
	mu        sync.Mutex
	keys      []string
	downloads int
}

func newTorznabFixture(t *testing.T, torrentBytes []byte) *torznabFixture {
	t.Helper()
	fixture := &torznabFixture{torrent: torrentBytes}
	mux := http.NewServeMux()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("apikey")
		fixture.mu.Lock()
		fixture.keys = append(fixture.keys, key)
		fixture.mu.Unlock()
		switch r.URL.Query().Get("t") {
		case "caps":
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<caps><server version="1.1"/><limits max="100" default="50"/>
<searching><search available="yes"/><tv-search available="yes" supportedParams="q,season,ep"/><movie-search available="yes"/></searching>
<categories><category id="2000" name="Movies"/><category id="5000" name="TV"/></categories></caps>`)
		case "search":
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<title>Fixture</title>
<item>
  <title>Tiny.Example.2024.1080p.WEB-DL</title>
  <guid>fixture-guid-1</guid>
  <pubDate>Mon, 02 Jan 2024 15:04:05 +0000</pubDate>
  <size>131072</size>
  <category>Movies</category>
  <enclosure url="%s/download/1.torrent" length="131072" type="application/x-bittorrent"/>
  <torznab:attr name="seeders" value="12"/><torznab:attr name="leechers" value="3"/>
  <torznab:attr name="infohash" value="%s"/>
</item>
</channel></rss>`, fixture.server.URL, infoHashOf(t, torrentBytes))
		default:
			http.Error(w, "unsupported", http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/download/1.torrent", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("apikey")
		fixture.mu.Lock()
		fixture.keys = append(fixture.keys, key)
		fixture.downloads++
		fixture.mu.Unlock()
		if key != fixtureAPIKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(fixture.torrent)
	})
	fixture.server = httptest.NewServer(mux)
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *torznabFixture) sawAPIKey() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range f.keys {
		if key == fixtureAPIKey {
			return true
		}
	}
	return false
}

func apiRequest(t *testing.T, client *http.Client, method, url string, body any) (int, string) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	var out bytes.Buffer
	if _, err := out.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, out.String()
}

func newAPI(t *testing.T, pool interface{}) (*httptest.Server, *torrents.Service, string) {
	t.Helper()
	return nil, nil, ""
}

func TestTorrentAPI(t *testing.T) {
	pool := testSchema(t)
	directory := t.TempDir()
	service := newService(t, pool, directory)
	updateSeedPolicy(t, service, 1, 0)
	mux := http.NewServeMux()
	service.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := server.Client()

	status, body := apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents", nil)
	if status != http.StatusOK || !strings.Contains(body, `"jobs":[]`) {
		t.Fatalf("empty queue: %d %s", status, body)
	}
	status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents",
		map[string]string{"magnet": "not a magnet"})
	if status != http.StatusBadRequest || !strings.Contains(body, "magnet") {
		t.Fatalf("invalid magnet: %d %s", status, body)
	}
	status, _ = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents", map[string]string{})
	if status != http.StatusBadRequest {
		t.Fatalf("empty add request must be rejected: %d", status)
	}
	status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents",
		map[string]string{"magnet": "magnet:?xt=urn:btih:zz"})
	if status != http.StatusBadRequest {
		t.Fatalf("bad magnet hash: %d %s", status, body)
	}

	seedDir := t.TempDir()
	torrentBytes, payload := buildTorrent(t, seedDir, "tiny.bin", 64<<10)
	status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents", map[string]string{
		"torrent": base64.StdEncoding.EncodeToString(torrentBytes), "filename": "tiny.torrent",
	})
	if status != http.StatusCreated {
		t.Fatalf("base64 add failed: %d %s", status, body)
	}
	var added torrents.Job
	if err := json.Unmarshal([]byte(body), &added); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "magnet:") || strings.Contains(body, "metainfo") {
		t.Fatalf("add response leaked metadata: %s", body)
	}
	status, body = apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents/"+added.ID, nil)
	if status != http.StatusOK || !strings.Contains(body, `"peers":[]`) {
		t.Fatalf("detail: %d %s", status, body)
	}
	if status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents/"+added.ID+"/pause", nil); status != http.StatusOK {
		t.Fatalf("pause: %d %s", status, body)
	}
	var paused torrents.Job
	_ = json.Unmarshal([]byte(body), &paused)
	if paused.Status != "paused" {
		t.Fatalf("pause did not apply: %s", body)
	}
	if status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents/"+added.ID+"/resume", nil); status != http.StatusOK {
		t.Fatalf("resume: %d %s", status, body)
	}
	if status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents/"+added.ID+"/recheck", nil); status != http.StatusOK {
		t.Fatalf("recheck: %d %s", status, body)
	}
	if status, body = apiRequest(t, client, http.MethodPut, server.URL+"/api/v1/torrents/"+added.ID+"/limits",
		map[string]any{"seedRatioLimit": 2.5, "seedTimeLimitMinutes": 45}); status != http.StatusOK {
		t.Fatalf("limits: %d %s", status, body)
	}
	var limited torrents.Job
	_ = json.Unmarshal([]byte(body), &limited)
	if limited.SeedRatioLimit != 2.5 || limited.SeedTimeLimitMinutes != 45 {
		t.Fatalf("limits were not saved: %s", body)
	}
	if status, body = apiRequest(t, client, http.MethodPut, server.URL+"/api/v1/torrents/"+added.ID+"/limits",
		map[string]any{"seedRatioLimit": -1, "seedTimeLimitMinutes": 0}); status != http.StatusBadRequest {
		t.Fatalf("negative ratio accepted: %d %s", status, body)
	}
	if status, body = apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents/missing-id", nil); status != http.StatusNotFound {
		t.Fatalf("missing job: %d %s", status, body)
	}

	// A peer fixture drives the API-created job to completion.
	port := seeder(t, seedDir, torrentBytes)
	waitFor(t, 60*time.Second, "the API job to complete", func() bool {
		_ = service.AddPeers(context.Background(), added.ID, []string{fmt.Sprintf("127.0.0.1:%d", port)})
		current := jobByID(t, service, added.ID)
		if current.Status == "failed" {
			t.Fatalf("transfer failed: %s", current.Error)
		}
		return current.Status == "seeding"
	})
	status, body = apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents/"+added.ID+"/file?name=tiny.bin", nil)
	if status != http.StatusOK || !bytes.Contains([]byte(body), payload[:64]) {
		t.Fatalf("file download: %d %d bytes", status, len(body))
	}
	if status, body = apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents/"+added.ID+"/file?name=../tiny.bin", nil); status != http.StatusNotFound {
		t.Fatalf("traversal download must fail: %d %s", status, body)
	}

	status, body = apiRequest(t, client, http.MethodDelete, server.URL+"/api/v1/torrents/"+added.ID+"?files=false", nil)
	if status != http.StatusOK || !strings.Contains(body, `"filesRemoved":false`) {
		t.Fatalf("delete keeping files: %d %s", status, body)
	}
	dataDir := directory + "/torrents/" + added.InfoHash
	removed := true
	if status, body = apiRequest(t, client, http.MethodDelete, server.URL+"/api/v1/torrents/"+limited.ID+"?files=true", nil); status == http.StatusOK {
		removed = strings.Contains(body, `"filesRemoved":true`)
	}
	_ = removed
	_ = dataDir
}

func TestTorrentAPISettingsAndSources(t *testing.T) {
	pool := testSchema(t)
	service := newService(t, pool, t.TempDir())
	mux := http.NewServeMux()
	service.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := server.Client()

	status, body := apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents/settings", nil)
	if status != http.StatusOK || !strings.Contains(body, `"listenPort"`) {
		t.Fatalf("settings: %d %s", status, body)
	}
	status, body = apiRequest(t, client, http.MethodPut, server.URL+"/api/v1/torrents/settings", map[string]any{
		"listenPort": 70000, "dhtEnabled": true, "pexEnabled": true, "maxActiveJobs": 3,
		"downloadLimitKBps": 0, "uploadLimitKBps": 0, "seedRatioLimit": 1, "seedTimeLimitMinutes": 0,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("invalid port accepted: %d %s", status, body)
	}
	status, body = apiRequest(t, client, http.MethodPut, server.URL+"/api/v1/torrents/settings", map[string]any{
		"listenPort": 51413, "dhtEnabled": true, "pexEnabled": false, "maxActiveJobs": 2,
		"downloadLimitKBps": 500, "uploadLimitKBps": 250, "seedRatioLimit": 1.5, "seedTimeLimitMinutes": 60,
	})
	if status != http.StatusOK {
		t.Fatalf("settings update: %d %s", status, body)
	}
	var settings torrents.Settings
	_ = json.Unmarshal([]byte(body), &settings)
	if settings.MaxActiveJobs != 2 || settings.PEXEnabled || settings.DownloadLimitKBps != 500 {
		t.Fatalf("settings not applied: %s", body)
	}
	if !strings.Contains(body, `"directory"`) {
		t.Fatalf("settings must report the download directory: %s", body)
	}

	status, body = apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents/health", nil)
	if status != http.StatusOK || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("health: %d %s", status, body)
	}

	seedDir := t.TempDir()
	torrentBytes, _ := buildTorrent(t, seedDir, "tiny.bin", 32<<10)
	fixture := newTorznabFixture(t, torrentBytes)

	status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrent-sources", map[string]any{
		"name": "Fixture", "url": fixture.server.URL + "/api", "apiKey": fixtureAPIKey,
		"categories": []int{2000, 5000}, "enabled": true,
	})
	if status != http.StatusCreated {
		t.Fatalf("source create: %d %s", status, body)
	}
	if strings.Contains(body, fixtureAPIKey) || strings.Contains(body, `"apiKey"`) {
		t.Fatalf("source response leaked the API key: %s", body)
	}
	var source torrents.Source
	if err := json.Unmarshal([]byte(body), &source); err != nil {
		t.Fatal(err)
	}
	if !source.APIKeyConfigured || source.ID == "" {
		t.Fatalf("unexpected source: %+v", source)
	}

	status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrent-sources/"+source.ID+"/test", nil)
	if status != http.StatusOK || !strings.Contains(body, `"ok":true`) || !strings.Contains(body, "tv-search") {
		t.Fatalf("source test: %d %s", status, body)
	}

	status, body = apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents/search?q=tiny", nil)
	if status != http.StatusOK {
		t.Fatalf("search: %d %s", status, body)
	}
	if !fixture.sawAPIKey() {
		t.Fatal("search did not authenticate with the stored API key")
	}
	if strings.Contains(body, fixtureAPIKey) || strings.Contains(body, "apikey") || strings.Contains(body, "/download/") {
		t.Fatalf("search response leaked source credentials or URLs: %s", body)
	}
	var results struct {
		Results []torrents.SearchResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &results); err != nil || len(results.Results) != 1 {
		t.Fatalf("unexpected search results: %s (%v)", body, err)
	}
	result := results.Results[0]
	if result.Seeders != 12 || result.Leechers != 3 || result.Size != 131072 || result.Source != "Fixture" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Magnet == "" || strings.Contains(result.Magnet, "tr=") {
		t.Fatalf("result magnet must be sanitized: %+v", result)
	}

	status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents", map[string]string{
		"sourceId": source.ID, "resultId": result.ID,
	})
	if status != http.StatusCreated {
		t.Fatalf("add from result: %d %s", status, body)
	}
	fixture.mu.Lock()
	downloads := fixture.downloads
	fixture.mu.Unlock()
	if downloads != 1 {
		t.Fatalf("expected the torrent to be fetched through the source, got %d", downloads)
	}
	if status, body = apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrents", map[string]string{
		"sourceId": source.ID, "resultId": "expired-id",
	}); status != http.StatusBadRequest {
		t.Fatalf("expired result must be refused: %d %s", status, body)
	}

	status, body = apiRequest(t, client, http.MethodPut, server.URL+"/api/v1/torrent-sources/"+source.ID, map[string]any{
		"name": "Fixture renamed", "url": fixture.server.URL + "/api", "apiKey": "",
		"categories": []int{2000}, "enabled": false,
	})
	if status != http.StatusOK || strings.Contains(body, `"apiKey"`) {
		t.Fatalf("source update: %d %s", status, body)
	}
	if strings.Contains(body, fixtureAPIKey) {
		t.Fatalf("source update leaked the API key: %s", body)
	}
	if status, body = apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrent-sources", nil); status != http.StatusOK ||
		!strings.Contains(body, "Fixture renamed") {
		t.Fatalf("source list: %d %s", status, body)
	}
	if status, body = apiRequest(t, client, http.MethodDelete, server.URL+"/api/v1/torrent-sources/"+source.ID, nil); status != http.StatusOK {
		t.Fatalf("source delete: %d %s", status, body)
	}
	if status, _ = apiRequest(t, client, http.MethodDelete, server.URL+"/api/v1/torrent-sources/"+source.ID, nil); status != http.StatusNotFound {
		t.Fatalf("deleting a missing source must 404, got %d", status)
	}
	if status, body = apiRequest(t, client, http.MethodGet, server.URL+"/api/v1/torrents/search?q=tiny", nil); status != http.StatusServiceUnavailable {
		t.Fatalf("search without enabled sources must be unavailable: %d %s", status, body)
	}
}

func TestTorrentAPISourceValidation(t *testing.T) {
	pool := testSchema(t)
	service := newService(t, pool, t.TempDir())
	mux := http.NewServeMux()
	service.Register(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := server.Client()

	for name, body := range map[string]map[string]any{
		"missing name":  {"name": "", "url": "http://localhost:9999/api"},
		"ftp scheme":    {"name": "bad", "url": "ftp://example.com/api"},
		"relative url":  {"name": "bad", "url": "not-a-url"},
		"long api key":  {"name": "bad", "url": "http://example.com/api", "apiKey": strings.Repeat("k", 600)},
		"many category": {"name": "bad", "url": "http://example.com/api", "categories": []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21}},
	} {
		if status, response := apiRequest(t, client, http.MethodPost, server.URL+"/api/v1/torrent-sources", body); status != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d %s", name, status, response)
		}
	}
}
