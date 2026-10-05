package torrents

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	fixtureKey   = "fixture-source-api-key"
	privatePass  = "SECRETPASSKEY"
	privateHash  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	publicHash   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	staleHash    = "cccccccccccccccccccccccccccccccccccccccc"
	privateMag   = "magnet:?xt=urn:btih:" + privateHash + "&dn=Private.Film&tr=udp%3A%2F%2Ftracker.private.example%3A1337%2Fannounce&tr=http%3A%2F%2Ftracker.private.example%2Fannounce%3Fpasskey%3D" + privatePass
	publicMag    = "magnet:?xt=urn:btih:" + publicHash + "&dn=Public.Film&tr=udp%3A%2F%2Ftracker.public.example%3A80%2Fannounce"
	staleMag     = "magnet:?xt=urn:btih:" + staleHash + "&dn=Stale.Film"
	trackerHosts = "tracker.private.example"
)

func xmlAttr(t *testing.T, value string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := xml.EscapeText(&buf, []byte(value)); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// magnetFeed serves Torznab search results whose magnets carry trackers and a private marker.
func magnetFeed(t *testing.T, fileBytes []byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != fixtureKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		magnetItem := func(title, guid, magnet, private string) string {
			return fmt.Sprintf(`<item><title>%s</title><guid>%s</guid><size>4096</size>
<torznab:attr name="seeders" value="7"/><torznab:attr name="magneturl" value="%s"/>%s</item>`,
				title, guid, xmlAttr(t, magnet), private)
		}
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
%s
%s
<item><title>Private File</title><guid>private-file</guid><size>%d</size>
<torznab:attr name="seeders" value="3"/><torznab:attr name="private" value="true"/>
<enclosure url="http://%s/download/private-file.torrent" length="%d" type="application/x-bittorrent"/></item>
<item><title>Stale Magnet</title><guid>stale-magnet</guid><size>2048</size>
<torznab:attr name="magneturl" value="%s"/><torznab:attr name="private" value="true"/></item>
<item><title>Public Link</title><guid>public-link</guid><size>1024</size>
<link>%s</link><torznab:attr name="seeders" value="9"/></item>
</channel></rss>`,
			magnetItem("Private Film", "private-magnet", privateMag, `<torznab:attr name="private" value="true"/>`),
			magnetItem("Public Magnet", "public-magnet", publicMag, ""),
			len(fileBytes), r.Host, len(fileBytes), xmlAttr(t, staleMag), xmlAttr(t, publicMag))
	})
	mux.HandleFunc("/download/private-file.torrent", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != fixtureKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(fileBytes)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestTorznabKeepsTrackersServerSideAndHonoursPrivacy(t *testing.T) {
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

	fileBytes := torrentFileWithPrivacy(t, false)
	server := magnetFeed(t, fileBytes)
	source, err := service.CreateSource(ctx, SourceInput{
		Name: "Fixture", URL: server.URL + "/api", APIKey: fixtureKey, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	response, err := service.Search(ctx, "film", "")
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(response.Results) != 5 {
		t.Fatalf("expected every fixture result, got %+v", response.Results)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{privatePass, fixtureKey, trackerHosts, "tracker.public.example"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("search JSON leaked %q: %s", secret, encoded)
		}
	}
	byID := map[string]SearchResult{}
	for _, result := range response.Results {
		byID[result.Title] = result
	}
	privateResult := byID["Private Film"]
	if privateResult.Magnet != "" {
		t.Fatalf("private results must not expose a magnet: %+v", privateResult)
	}
	publicResult := byID["Public Magnet"]
	if !strings.Contains(publicResult.Magnet, "xt=urn:btih:"+publicHash) || strings.Contains(publicResult.Magnet, "tr=") {
		t.Fatalf("public results expose only a tracker-free magnet: %+v", publicResult)
	}

	cached, ok := service.results.get(privateResult.ID)
	if !ok || !cached.private || !strings.Contains(cached.magnet, privatePass) {
		t.Fatalf("the server-side cache must keep the private magnet: %+v", cached)
	}
	publicCached, ok := service.results.get(publicResult.ID)
	if !ok || publicCached.private || !strings.Contains(publicCached.magnet, "tracker.public.example") {
		t.Fatalf("the server-side cache must keep public trackers: %+v", publicCached)
	}

	// Adding a private result keeps its tracker, and the first attach never touches DHT or PEX.
	privateJob, err := service.AddFromResult(ctx, source.ID, privateResult.ID, "Private Film")
	if err != nil {
		t.Fatalf("cannot add the private result: %v", err)
	}
	if !privateJob.Private || privateJob.ReleaseID != privateResult.ID {
		t.Fatalf("the private feed hint and release identity must reach the job: %+v", privateJob)
	}
	privateStored, err := service.storedByID(ctx, pool, privateJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !privateStored.Private || privateStored.ReleaseID != privateResult.ID || !strings.Contains(privateStored.magnet, privatePass) {
		t.Fatalf("the stored job must keep its private tracker and release id: %+v", privateStored)
	}
	service.startRun(ctx, privateStored)
	privateRun := service.run(privateJob.ID)
	if privateRun == nil || !privateRun.private || privateRun.client != service.privateClient {
		t.Fatal("a private feed magnet must attach to the client without DHT or PEX")
	}
	if service.publicClient != nil {
		t.Fatal("a private feed magnet must never create the public DHT client")
	}
	service.dropRun(privateRun)

	// A public magnet keeps its trackers and is quarantined until its metadata proves it public.
	publicJob, err := service.AddFromResult(ctx, source.ID, publicResult.ID, "Public Magnet")
	if err != nil {
		t.Fatalf("cannot add the public result: %v", err)
	}
	publicStored, err := service.storedByID(ctx, pool, publicJob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if publicStored.Private || publicStored.ReleaseID != publicResult.ID ||
		!strings.Contains(publicStored.magnet, "tracker.public.example") {
		t.Fatalf("the stored public magnet must keep its tracker and release id: %+v", publicStored)
	}
	service.startRun(ctx, publicStored)
	publicRun := service.run(publicJob.ID)
	if publicRun == nil || !publicRun.private || publicRun.client != service.privateClient {
		t.Fatal("a tracker-bearing magnet must resolve metadata without DHT or PEX")
	}
	if service.publicClient != nil {
		t.Fatal("metadata must be quarantined before DHT is used")
	}
	service.dropRun(publicRun)

	// A private-only file result applies the hint before the first attach.
	fileResult := byID["Private File"]
	fileJob, err := service.AddFromResult(ctx, source.ID, fileResult.ID, "Private File")
	if err != nil {
		t.Fatalf("cannot add the private file result: %v", err)
	}
	if !fileJob.Private || fileJob.ReleaseID != fileResult.ID {
		t.Fatalf("the private hint and release identity must reach torrent file jobs: %+v", fileJob)
	}

	// Pasted trackerless magnets stay public and keep using DHT.
	pasted, err := service.Add(ctx, AddInput{Magnet: staleMag})
	if err != nil || pasted.Private {
		t.Fatalf("a pasted trackerless magnet stays public: %+v (%v)", pasted, err)
	}
	pastedStored, err := service.storedByID(ctx, pool, pasted.ID)
	if err != nil {
		t.Fatal(err)
	}
	service.startRun(ctx, pastedStored)
	pastedRun := service.run(pasted.ID)
	if pastedRun == nil || pastedRun.private || pastedRun.client != service.publicClient {
		t.Fatal("a pasted trackerless magnet must use the public DHT client")
	}

	// A private result without a tracker cannot reach its swarm and is refused with a clear error.
	if _, err := service.AddFromResult(ctx, source.ID, byID["Stale Magnet"].ID, "Stale Magnet"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a private trackerless magnet must be refused, got %v", err)
	} else if strings.Contains(err.Error(), fixtureKey) || strings.Contains(err.Error(), privatePass) {
		t.Fatalf("the refusal leaked a credential: %v", err)
	}
	if _, err := service.AddFromResult(ctx, source.ID, "expired-result", "Gone"); err == nil ||
		strings.Contains(err.Error(), fixtureKey) {
		t.Fatalf("expired results must fail without leaking the API key: %v", err)
	}
}
