package torrents

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func TestSameOrigin(t *testing.T) {
	parse := func(raw string) *url.URL {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	cases := []struct {
		source, target string
		want           bool
	}{
		{"http://host:9696/api", "http://host:9696/download?id=1", true},
		{"https://host/api", "https://host:443/file", true},
		{"http://host/api", "https://host/file", true},
		{"https://host/api", "http://host/file", false},
		{"http://host:9696/api", "http://host:8080/file", false},
		{"http://host/api", "http://other/file", false},
		{"http://host/api", "http://127.0.0.1:9696/file", false},
	}
	for _, test := range cases {
		if got := sameOrigin(parse(test.source), parse(test.target)); got != test.want {
			t.Errorf("sameOrigin(%s, %s) = %v, want %v", test.source, test.target, got, test.want)
		}
	}
}

func TestMagnetHasTrackers(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	if !magnetHasTrackers("magnet:?xt=urn:btih:" + hash + "&tr=udp%3A%2F%2Ftracker.example%3A80") {
		t.Fatal("tracker magnets must be detected")
	}
	for _, magnet := range []string{
		"magnet:?xt=urn:btih:" + hash,
		"magnet:?xt=urn:btih:" + hash + "&dn=Name",
		"magnet:?xt=urn:btih:" + hash + "&tr=",
	} {
		if magnetHasTrackers(magnet) {
			t.Errorf("trackerless magnet detected as tracked: %s", magnet)
		}
	}
}

// fetchWithKey exercises the real fetch path against a source and a foreign host.
func TestFetchNeverForwardsAPIKeyToAnotherHost(t *testing.T) {
	const key = "fixture-secret-key"
	var mu sync.Mutex
	foreignHits, sourceHits, redirectHits := 0, 0, 0
	foreignHandler := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Query().Get("apikey") == key {
			foreignHits++
		}
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}
	foreign := httptest.NewServer(http.HandlerFunc(foreignHandler))
	t.Cleanup(foreign.Close)

	mux := http.NewServeMux()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Query().Get("apikey") == key {
			sourceHits++
		}
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+"/steal?apikey="+key, http.StatusFound)
	})
	mux.HandleFunc("/redirect-origin", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if r.URL.Query().Get("apikey") == key {
			redirectHits++
		}
		mu.Unlock()
		http.Redirect(w, r, "/api", http.StatusFound)
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "failure mentioning "+key, http.StatusInternalServerError)
	})
	source := httptest.NewServer(mux)
	t.Cleanup(source.Close)

	service := &Service{httpClient: boundedHTTPClient()}
	stored := storedSource{source: Source{ID: "source", Name: "Fixture", URL: source.URL + "/api"}, apiKey: key}
	ctx := context.Background()

	if _, err := service.fetch(ctx, stored, source.URL+"/api?t=caps"); err != nil {
		t.Fatalf("source fetch failed: %v", err)
	}
	if _, err := service.fetch(ctx, stored, foreign.URL+"/download/1.torrent"); err != nil {
		t.Fatalf("cross-host download failed: %v", err)
	}
	if _, err := service.fetch(ctx, stored, source.URL+"/redirect"); err != nil {
		t.Fatalf("redirect fetch failed: %v", err)
	}
	if _, err := service.fetch(ctx, stored, source.URL+"/redirect-origin"); err != nil {
		t.Fatalf("same-host redirect failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if sourceHits != 1 {
		t.Fatalf("the indexer host must receive its API key, got %d", sourceHits)
	}
	if redirectHits != 1 {
		t.Fatalf("a same-host redirect keeps the key, got %d", redirectHits)
	}
	if foreignHits != 0 {
		t.Fatalf("the API key reached another host %d times", foreignHits)
	}

	if _, err := service.fetch(ctx, stored, source.URL+"/broken"); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("source errors must be sanitized: %v", err)
	}
	if message := redactError(errors.New("dial failed for "+key), key).Error(); strings.Contains(message, key) {
		t.Fatalf("redaction failed: %s", message)
	}
}

// torrentFileWithPrivacy builds a single-file torrent file with the BEP 27 flag set or cleared.
func torrentFileWithPrivacy(t *testing.T, private bool) []byte {
	t.Helper()
	target := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(target, []byte("payload-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(target); err != nil {
		t.Fatal(err)
	}
	if private {
		flag := true
		info.Private = &flag
	}
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	meta := metainfo.MetaInfo{InfoBytes: raw}
	if err := meta.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestPrivateTorrentFileAttachesToNoDHTClient covers the production configuration with DHT enabled.
func TestPrivateTorrentFileAttachesToNoDHTClient(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	service, err := New(ctx, pool, Options{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateSettings(ctx, SettingsUpdate{
		ListenPort: 0, DHTEnabled: true, PEXEnabled: true, MaxActiveJobs: 3,
		SeedRatioLimit: 0, SeedTimeLimitMinutes: 0,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)

	private, err := service.Add(ctx, AddInput{Torrent: torrentFileWithPrivacy(t, true), Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	if !private.Private {
		t.Fatalf("the private flag must be recorded on add: %+v", private)
	}
	stored, err := service.storedByID(ctx, pool, private.ID)
	if err != nil {
		t.Fatal(err)
	}
	service.startRun(ctx, stored)
	run := service.run(private.ID)
	if run == nil {
		t.Fatal("the private torrent was not attached")
	}
	if !run.private || run.client != service.privateClient {
		t.Fatal("a private torrent file must attach to the client without DHT or PEX")
	}
	if service.publicClient != nil {
		t.Fatal("a private torrent must never create the public DHT client")
	}
	if servers := service.privateClient.DhtServers(); len(servers) != 0 {
		t.Fatalf("the private client must not run DHT, got %d servers", len(servers))
	}

	// Jobs written by an earlier version are repaired from their stored metainfo before attaching.
	if _, err := pool.Exec(ctx, `UPDATE torrent_jobs SET private = false WHERE id = $1`, private.ID); err != nil {
		t.Fatal(err)
	}
	service.dropRun(run)
	legacy, err := service.storedByID(ctx, pool, private.ID)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Private {
		t.Fatal("the test did not clear the stored flag")
	}
	service.startRun(ctx, legacy)
	repaired := service.run(private.ID)
	if repaired == nil || !repaired.private || repaired.client != service.privateClient {
		t.Fatal("a stored private flag must be derived from metainfo before the first attach")
	}
	var storedFlag bool
	if err := pool.QueryRow(ctx, `SELECT private FROM torrent_jobs WHERE id = $1`, private.ID).Scan(&storedFlag); err != nil || !storedFlag {
		t.Fatalf("the repaired private flag was not saved: %v %v", storedFlag, err)
	}

	// Control: a public torrent file in the same configuration gets a DHT client.
	public, err := service.Add(ctx, AddInput{Torrent: torrentFileWithPrivacy(t, false), Source: "file"})
	if err != nil {
		t.Fatal(err)
	}
	publicStored, err := service.storedByID(ctx, pool, public.ID)
	if err != nil {
		t.Fatal(err)
	}
	service.startRun(ctx, publicStored)
	publicRun := service.run(public.ID)
	if publicRun == nil || publicRun.private || publicRun.client != service.publicClient {
		t.Fatal("a public torrent file must attach to the public client")
	}
	if servers := service.publicClient.DhtServers(); len(servers) == 0 {
		t.Fatal("the production configuration under test must run DHT on the public client")
	}
}

// clientForTest reports the client a job starts on, which drives DHT and PEX exposure.
func TestMagnetMetadataUsesNoDHTClientWhenTracked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	service, err := New(ctx, pool, Options{Directory: t.TempDir(), Testing: true})
	if err != nil {
		t.Fatal(err)
	}
	service.Start(ctx)
	t.Cleanup(service.Close)

	hash := strings.Repeat("cd", 20)
	tracked, err := service.Add(ctx, AddInput{
		Magnet: "magnet:?xt=urn:btih:" + hash + "&tr=udp%3A%2F%2Ftracker.example%3A80",
	})
	if err != nil {
		t.Fatal(err)
	}
	untracked, err := service.Add(ctx, AddInput{Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("ef", 20)})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 15*time.Second, "both magnets to attach", func() bool {
		return service.run(tracked.ID) != nil && service.run(untracked.ID) != nil
	})
	if run := service.run(tracked.ID); !run.private {
		t.Fatal("a tracker-bearing magnet must resolve metadata without DHT or PEX")
	}
	if run := service.run(untracked.ID); run.private {
		t.Fatal("a trackerless magnet keeps the public client for DHT discovery")
	}
}
