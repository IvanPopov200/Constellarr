package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/music"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

// fixtureServer answers the paths a real application exposes; bodies mirror verified upstream shapes.
func fixtureServer(t *testing.T, routes map[string]string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func testAdapterService(t *testing.T, server *httptest.Server) *Service {
	t.Helper()
	s := &Service{client: server.Client(), retries: 2, retryDelay: time.Millisecond, callTimeout: 2 * time.Second, maxResponse: 1 << 20}
	s.withDefaults()
	return s
}

const radarrFixture = `{"appName":"Radarr","version":"6.1.1.10360"}`

func radarrRoutes() map[string]string {
	return map[string]string{
		"/api/v3/system/status": radarrFixture,
		"/api/v3/rootfolder":    `[{"id":1,"path":"/media/movies","accessible":true,"freeSpace":1000000}]`,
		"/api/v3/qualityprofile": `[
			{"id":2,"name":"SD","upgradeAllowed":false,"cutoff":2,"items":[
				{"quality":{"id":0,"name":"Unknown"},"allowed":false},
				{"quality":{"id":24,"name":"WORKPRINT"},"allowed":true},
				{"quality":{"id":2,"name":"DVD"},"allowed":true},
				{"id":1000,"name":"WEB 480p","allowed":true,"items":[{"quality":{"id":3,"name":"WEBDL-480p"}}]}]},
			{"id":3,"name":"HD-1080p","upgradeAllowed":true,"cutoff":7,"items":[
				{"quality":{"id":7,"name":"Bluray-1080p"},"allowed":true},
				{"quality":{"id":8,"name":"WEBDL-1080p"},"allowed":true},
				{"quality":{"id":9,"name":"HDTV-1080p"},"allowed":false}]}]`,
		"/api/v3/tag": `[{"id":1,"label":"kids"}]`,
		"/api/v3/movie": `[
			{"id":1,"title":"The Dark Knight","year":2008,"imdbId":"tt0468569","monitored":true,"tags":[1],
			 "qualityProfileId":3,"rootFolderPath":"/media/movies","path":"/media/movies/The Dark Knight (2008)","hasFile":true,
			 "overview":"plot","images":[{"coverType":"poster","remoteUrl":"http://poster.example/one.jpg"}],
			 "movieFile":{"relativePath":"The.Dark.Knight.2008.1080p.mkv","path":"/media/movies/The Dark Knight (2008)/The.Dark.Knight.2008.1080p.mkv","size":2048}},
			{"id":2,"title":"No Identity","year":1999,"imdbId":"","monitored":false,"tags":[],
			 "qualityProfileId":2,"rootFolderPath":"/media/movies","path":"/media/movies/No Identity (1999)","hasFile":false,"movieFile":null}]`,
		"/api/v3/config/naming": `{"movieFolderFormat":"{Movie Title} ({Release Year})","standardMovieFormat":"{Movie Title} ({Release Year}) {Quality Full}"}`,
	}
}

func TestDiscoverRadarrContract(t *testing.T) {
	server, _ := fixtureServer(t, radarrRoutes())
	service := testAdapterService(t, server)
	connection := Connection{App: AppRadarr, URL: server.URL, APIKey: "radarr-key"}

	out, err := service.discoverRadarr(context.Background(), connection)
	if err != nil {
		t.Fatalf("discoverRadarr: %v", err)
	}
	if len(out.Roots) != 1 || out.Roots[0].Path != "/media/movies" || !out.Roots[0].Accessible || out.Roots[0].Media != MediaMovies {
		t.Fatalf("roots: %+v", out.Roots)
	}
	if len(out.Profiles) != 2 {
		t.Fatalf("profiles: %+v", out.Profiles)
	}
	hd := out.Profiles[1]
	if hd.Key != "radarr:3" || hd.Name != "HD-1080p" || !hd.Upgrade || hd.Cutoff != "Bluray-1080p" {
		t.Fatalf("profile plan: %+v", hd)
	}
	if got := strings.Join(hd.Qualities, ","); got != "Bluray-1080p,WEB-1080p" {
		t.Fatalf("mapped qualities: %s", got)
	}
	if len(out.Profiles[0].Unsupported) != 1 || out.Profiles[0].Unsupported[0] != "WORKPRINT" {
		t.Fatalf("unsupported qualities: %+v", out.Profiles[0].Unsupported)
	}
	if len(out.Movies) != 2 {
		t.Fatalf("movies: %+v", out.Movies)
	}
	dark := out.Movies[0]
	if dark.IMDbID != "tt0468569" || dark.FilePath != "The Dark Knight (2008)/The.Dark.Knight.2008.1080p.mkv" || dark.FileSize != 2048 {
		t.Fatalf("movie file mapping: %+v", dark)
	}
	if len(dark.Tags) != 1 || dark.Tags[0] != "kids" {
		t.Fatalf("tag labels: %+v", dark.Tags)
	}
	if dark.Poster != "http://poster.example/one.jpg" || dark.Plot != "plot" {
		t.Fatalf("metadata hints: %+v", dark)
	}
	if len(out.Naming) != 1 || out.Naming[0].Folder != "{title} ({year})" || out.Naming[0].File != "{title} ({year}) {quality}" || !out.Naming[0].Applicable {
		t.Fatalf("naming: %+v", out.Naming)
	}
}

func sonarrRoutes() map[string]string {
	return map[string]string{
		"/api/v3/system/status": `{"appName":"Sonarr","version":"4.0.17.2952"}`,
		"/api/v3/rootfolder":    `[{"id":1,"path":"/media/tv","accessible":true}]`,
		"/api/v3/qualityprofile": `[{"id":2,"name":"HD-720p","upgradeAllowed":false,"cutoff":6,"items":[
			{"quality":{"id":6,"name":"Bluray-720p"},"allowed":true},
			{"quality":{"id":5,"name":"WEBDL-1080p"},"allowed":true}]}]`,
		"/api/v3/tag": `[]`,
		"/api/v3/series": `[
			{"id":1,"title":"The Office","year":2005,"imdbId":"tt0386676","tvdbId":73244,"monitored":true,
			 "monitorNewItems":"none","tags":[],"qualityProfileId":2,"rootFolderPath":"/media/tv","path":"/media/tv/The Office",
			 "monitorMode":"all","seasons":[{"seasonNumber":0,"monitored":false},{"seasonNumber":1,"monitored":true},{"seasonNumber":2,"monitored":false}],
			 "statistics":{"episodeFileCount":2},
			 "overview":"office plot","images":[{"coverType":"poster","remoteUrl":"http://poster.example/tv.jpg"}]}]`,
		"/api/v3/episodefile": `[
			{"id":1,"seriesId":1,"seasonNumber":1,"relativePath":"Season 1/The.Office.S01E01.480p.DVDRip.mkv",
			 "path":"/media/tv/The Office/Season 1/The.Office.S01E01.480p.DVDRip.mkv","size":1024},
			{"id":2,"seriesId":1,"seasonNumber":1,"relativePath":"Season 1/some.release.name.mkv","path":"/media/tv/The Office/Season 1/some.release.name.mkv","size":1024}]`,
		"/api/v3/config/naming": `{"seriesFolderFormat":"{Series Title}","seasonFolderFormat":"Season {season}","standardEpisodeFormat":"{Series Title} - S{season:00}E{episode:00} - {Episode Title} {Quality Full}"}`,
	}
}

func TestDiscoverSonarrContract(t *testing.T) {
	server, _ := fixtureServer(t, sonarrRoutes())
	service := testAdapterService(t, server)
	connection := Connection{App: AppSonarr, URL: server.URL, APIKey: "sonarr-key"}

	out, err := service.discoverSonarr(context.Background(), connection)
	if err != nil {
		t.Fatalf("discoverSonarr: %v", err)
	}
	if len(out.Series) != 1 {
		t.Fatalf("series: %+v", out.Series)
	}
	series := out.Series[0]
	if series.MonitorMode != "none" || series.IMDbID != "tt0386676" {
		t.Fatalf("series flags: %+v", series)
	}
	if len(series.UnmonitoredSeasons) != 1 || series.UnmonitoredSeasons[0] != 2 {
		t.Fatalf("season 0 and 2 are unmonitored, plan keeps season 2: %+v", series.UnmonitoredSeasons)
	}
	if len(series.Files) != 2 {
		t.Fatalf("files: %+v", series.Files)
	}
	if series.Files[0].Season != 1 || len(series.Files[0].Numbers) != 1 || series.Files[0].Numbers[0] != 1 {
		t.Fatalf("episode identity: %+v", series.Files[0])
	}
	if series.Files[0].RelativePath != "The Office/Season 1/The.Office.S01E01.480p.DVDRip.mkv" {
		t.Fatalf("relative path: %+v", series.Files[0])
	}
	if series.Files[1].Unsupported == "" {
		t.Fatalf("unparseable file names must be reported: %+v", series.Files[1])
	}
	naming := out.Naming[0]
	if naming.Folder != "{title}/Season {season}" || naming.File != "{title} - S{season}E{episode} - {episodeTitle} {quality}" || !naming.Applicable {
		t.Fatalf("naming: %+v", naming)
	}
}

func TestNamingRejectsUnsupportedTokens(t *testing.T) {
	naming := seriesNaming(AppSonarr, "{Series Title}", "Season {season}", "{Series Title} - {Air-Date} {Quality Full}")
	if naming.Applicable || len(naming.Unsupported) != 1 || naming.Unsupported[0] != "{Air-Date}" {
		t.Fatalf("unsupported tokens must block the mapping: %+v", naming)
	}
}

func TestDiscoverProwlarrContract(t *testing.T) {
	routes := map[string]string{
		"/api/v1/system/status": `{"appName":"Prowlarr","version":"2.3.5.5327"}`,
		"/api/v1/indexer": `[
			{"id":1,"name":"NZBGeek","enable":true,"implementation":"Newznab","definitionName":"Newznab","protocol":"usenet","fields":[]},
			{"id":2,"name":"Public tracker","enable":true,"definitionName":"Cardigann","protocol":"torrent","fields":[]}]`,
		"/api/v1/indexer/1": `{"id":1,"name":"NZBGeek","fields":[
			{"name":"baseUrl","value":"https://api.nzbgeek.info"},{"name":"apiPath","value":"/api"},{"name":"apiKey","value":"indexer-key"}]}`,
		"/api/v1/indexer/2": `{"id":2,"name":"Public tracker","fields":[{"name":"baseUrl","value":"https://tracker.example"}]}`,
	}
	server, _ := fixtureServer(t, routes)
	service := testAdapterService(t, server)
	connection := Connection{App: AppProwlarr, URL: server.URL, APIKey: "prowlarr-key"}

	out, secrets, err := service.discoverProwlarr(context.Background(), connection)
	if err != nil {
		t.Fatalf("discoverProwlarr: %v", err)
	}
	if len(out.Indexers) != 1 || out.Indexers[0].Key != "prowlarr:1" || out.Indexers[0].URL != "https://api.nzbgeek.info/api" || !out.Indexers[0].APIKeySet {
		t.Fatalf("indexer candidates: %+v", out.Indexers)
	}
	if len(out.Torznab) != 1 || out.Torznab[0].Key != "prowlarr:2" || out.Torznab[0].URL != server.URL+"/2/api" || out.Torznab[0].Protocol != "torrent" {
		t.Fatalf("torznab candidates: %+v", out.Torznab)
	}
	secret, ok := secrets.indexer("prowlarr:1")
	if !ok || secret.APIKey != "indexer-key" {
		t.Fatalf("the indexer API key must be discovered but stay out of the plan: %+v", secrets.indexers)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "indexer-key") {
		t.Fatalf("the indexer candidate leaks the API key: %s", encoded)
	}
}

func TestDiscoverProwlarrReportsNonNZBGeekIndexer(t *testing.T) {
	routes := map[string]string{
		"/api/v1/system/status": `{"appName":"Prowlarr","version":"2.3.5.5327"}`,
		"/api/v1/indexer":       `[{"id":1,"name":"Other","enable":true,"definitionName":"Newznab","protocol":"usenet","fields":[]}]`,
		"/api/v1/indexer/1":     `{"id":1,"fields":[{"name":"baseUrl","value":"https://api.other.example"},{"name":"apiKey","value":"other-key"}]}`,
	}
	server, _ := fixtureServer(t, routes)
	service := testAdapterService(t, server)
	out, _, err := service.discoverProwlarr(context.Background(), Connection{App: AppProwlarr, URL: server.URL, APIKey: "prowlarr-key"})
	if err != nil {
		t.Fatalf("a non-NZBGeek indexer must not fail the preview: %v", err)
	}
	if len(out.Indexers) != 0 || len(out.Unsupported) != 1 || !strings.Contains(out.Unsupported[0].Detail, "NZBGeek") {
		t.Fatalf("unsupported indexers must be listed: %+v %+v", out.Indexers, out.Unsupported)
	}
}

func TestDiscoverSABnzbdContract(t *testing.T) {
	payload := `{"config":{"servers":[
		{"name":"news.example.com","host":"news.example.com","port":563,"username":"sab-user","password":"sab-pass","connections":8,"ssl":1,"enable":1},
		{"name":"disabled.example.com","host":"disabled.example.com","port":119,"username":"","password":"","connections":2,"ssl":0,"enable":0}]}}`
	var sawKey atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "sab-key" {
			http.Error(w, "missing key", http.StatusForbidden)
			return
		}
		sawKey.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(server.Close)
	service := testAdapterService(t, server)

	out, secrets, err := service.discoverSABnzbd(context.Background(), Connection{App: AppSABnzbd, URL: server.URL, APIKey: "sab-key"})
	if err != nil {
		t.Fatalf("discoverSABnzbd: %v", err)
	}
	if !sawKey.Load() {
		t.Fatal("the API key was not sent to SABnzbd")
	}
	if len(out.UsenetSources) != 1 {
		t.Fatalf("news server candidates: %+v", out.UsenetSources)
	}
	plan := out.UsenetSources[0]
	if plan.Key != "sabnzbd" || plan.Host != "news.example.com" || plan.Port != 563 || plan.Connections != 8 || !plan.UsernameSet || !plan.TLS {
		t.Fatalf("usenet plan: %+v", plan)
	}
	secret, ok := secrets.newsServer("sabnzbd")
	if !ok || secret.Password != "sab-pass" {
		t.Fatalf("usenet secrets: %+v", secrets.usenet)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "sab-pass") || strings.Contains(string(encoded), "sab-user") {
		t.Fatalf("the usenet plan leaks credentials: %s", encoded)
	}
}

func TestDiscoverNZBGetContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/json")
		switch payload.Method {
		case "version":
			_, _ = w.Write([]byte(`{"result":"25.4"}`))
		case "config":
			_, _ = w.Write([]byte(`{"result":[
				{"Name":"Server1.Active","Value":"yes"},{"Name":"Server1.Host","Value":"news.example.com"},
				{"Name":"Server1.Port","Value":"563"},{"Name":"Server1.Username","Value":"nzb-user"},
				{"Name":"Server1.Password","Value":"nzb-pass"},{"Name":"Server1.Connections","Value":"8"},
				{"Name":"Server1.Encryption","Value":"yes"},{"Name":"Server2.Active","Value":"yes"}]}`))
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	service := testAdapterService(t, server)

	out, secrets, err := service.discoverNZBGet(context.Background(), Connection{App: AppNZBGet, URL: server.URL, Username: "nzb-user", Password: "nzb-pass"})
	if err != nil {
		t.Fatalf("discoverNZBGet: %v", err)
	}
	if len(out.UsenetSources) != 1 || out.UsenetSources[0].Key != "nzbget" || out.UsenetSources[0].Host != "news.example.com" || !out.UsenetSources[0].TLS {
		t.Fatalf("usenet plan: %+v", out.UsenetSources)
	}
	if len(out.UsenetSources[0].Notes) == 0 || !strings.Contains(out.UsenetSources[0].Notes[0], "additional NZBGet news servers") {
		t.Fatalf("extra servers must be reported: %+v", out.UsenetSources[0].Notes)
	}
	if secret, ok := secrets.newsServer("nzbget"); !ok || secret.Password != "nzb-pass" {
		t.Fatalf("usenet secrets: %+v", secrets.usenet)
	}
}

func TestSelectNewsServersIsDeterministic(t *testing.T) {
	plan := &Plan{UsenetSources: []UsenetPlan{
		{Key: "sabnzbd", Source: AppSABnzbd, Host: "a.example.com"},
		{Key: "nzbget", Source: AppNZBGet, Host: "b.example.com"},
	}}
	secrets := providerSecrets{usenet: map[string]usenetSecret{
		"sabnzbd": {Host: "a.example.com", Username: "user", Password: "pass"},
		"nzbget":  {Host: "b.example.com", Username: "user", Password: "pass"},
	}}
	selection := selectNewsServers(plan, "", nil, secrets)
	if selection.primaryKey != "" || !strings.Contains(selection.reason, "select one") {
		t.Fatalf("an ambiguous choice must be explicit: %+v", selection)
	}
	selection = selectNewsServers(plan, "nzbget", []string{"sabnzbd"}, secrets)
	if selection.primaryKey != "nzbget" || len(selection.fallbackHosts) != 1 || selection.fallbackHosts[0] != "a.example.com" {
		t.Fatalf("compatible fallbacks share the account: %+v", selection)
	}
	secrets.usenet["sabnzbd"] = usenetSecret{Host: "a.example.com", Username: "other", Password: "pass"}
	selection = selectNewsServers(plan, "nzbget", []string{"sabnzbd"}, secrets)
	if len(selection.fallbackHosts) != 0 || !strings.Contains(selection.skipped["sabnzbd"], "different credentials") {
		t.Fatalf("incompatible sources must be reported, not discarded silently: %+v", selection)
	}
	selection = selectNewsServers(plan, "sabnzbd", nil, secrets)
	if selection.primaryKey != "sabnzbd" || !strings.Contains(selection.skipped["nzbget"], "another news server") {
		t.Fatalf("unselected sources must be reported: %+v", selection)
	}
}

func TestDiscoverTransmissionHandshake(t *testing.T) {
	var sessions atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") != "session-token" {
			w.Header().Set("X-Transmission-Session-Id", "session-token")
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		sessions.Add(1)
		var payload struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/json")
		switch payload.Method {
		case "session-get":
			_, _ = w.Write([]byte(`{"result":"success","arguments":{"version":"4.0.4","download-dir":"/downloads","incomplete-dir":"/incomplete",
				"speed-limit-down":100,"speed-limit-up":50,"speed-limit-down-enabled":true,"speed-limit-up-enabled":false,
				"seedRatioLimit":2,"seedRatioLimited":true,"idle-seeding-limit":30,"idle-seeding-limit-enabled":false,
				"dht-enabled":true,"pex-enabled":false,"download-queue-size":5}}`))
		case "torrent-get":
			_, _ = w.Write([]byte(`{"result":"success","arguments":{"torrents":[{"id":1,"name":"Example.Release","hashString":"0123456789abcdef0123456789abcdef01234567",
				"magnetLink":"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Example.Release","status":4,"percentDone":0.5,
				"downloadDir":"/downloads","labels":["tv"],"rateDownload":1024,"uploadRatio":0.25,"totalSize":1000,"leftUntilDone":500,
				"addedDate":1700000000,"isFinished":false,
				"trackers":[{"announce":"https://tracker.example/announce?passkey=secret-passkey"}],
				"files":[{"name":"Example.Release/file.mkv","length":1000,"bytesCompleted":500}]}]}}`))
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	service := testAdapterService(t, server)

	out, _, err := service.discoverTransmission(context.Background(), Connection{App: AppTransmission, URL: server.URL, Username: "t-user", Password: "t-pass"})
	if err != nil {
		t.Fatalf("discoverTransmission: %v", err)
	}
	if out.Torrents == nil || out.Torrents.Torrents != 1 || len(out.Torrents.Transfers) != 1 {
		t.Fatalf("torrent plan: %+v", out.Torrents)
	}
	plan := out.Torrents
	if !plan.SpeedDownEnabled || plan.SpeedUpEnabled || !plan.SeedRatioLimited || plan.IdleSeedingLimited || !plan.DHTEnabled || plan.PEXEnabled || plan.DownloadQueueSize != 5 {
		t.Fatalf("session settings: %+v", plan)
	}
	transfer := plan.Transfers[0]
	if transfer.BytesDone != 500 || transfer.PercentDone != 0.5 || !transfer.AddedAt.Equal(time.Unix(1700000000, 0).UTC()) {
		t.Fatalf("resumable transfer: %+v", transfer)
	}
	if transfer.MagnetLink == "" || len(transfer.Files) != 1 || transfer.Files[0].Name != "Example.Release/file.mkv" {
		t.Fatalf("magnet link and file list drive data reuse: %+v", transfer)
	}
	if len(transfer.TrackerHosts) != 1 || transfer.TrackerHosts[0] != "tracker.example" {
		t.Fatalf("only tracker hosts may be stored: %+v", transfer.TrackerHosts)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "secret-passkey") {
		t.Fatalf("tracker passkeys must never reach the plan: %s", encoded)
	}
	if sessions.Load() != 2 {
		t.Fatalf("both RPC calls must run after the handshake, got %d", sessions.Load())
	}
}

func TestDiscoverBazarrContract(t *testing.T) {
	routes := map[string]string{
		"/api/system/status":             `{"data":{"bazarr_version":"1.5.6"}}`,
		"/api/system/languages":          `[{"name":"English","code2":"en","enabled":true},{"name":"French","code2":"fr","enabled":true},{"name":"Afar","code2":"aa","enabled":false}]`,
		"/api/system/languages/profiles": `[{"profileId":"1","name":"Anime","cutoff":"en","items":[{"language":"en","forced":true,"hi":false},{"language":"fr","forced":false,"hi":false}]},{"profileId":"2","name":"Odd","cutoff":null,"items":[{"language":"!!","forced":false,"hi":false}]}]`,
		"/api/system/settings": `{"general":{"single_language":false,"language_equals":[],"adaptive_searching":true,"minimum_score":90,"minimum_score_movie":70,
			"upgrade_subs":true,"enabled_providers":["opensubtitlescom","subssabbz"],"path_mappings":[],"serie_default_profile":"1","movie_default_profile":"","wanted_search_frequency":6},
			"subsync":{"use_subsync":true,"use_subsync_movie":false,"max_offset_seconds":60,"subsync_threshold":90,"subsync_movie_threshold":70,"no_fix_framerate":false,"gss":true},
			"opensubtitlescom":{"username":"os-user","password":"os-pass","use_hash":true,"include_ai_translated":false,"include_machine_translated":false}}`,
	}
	server, _ := fixtureServer(t, routes)
	service := testAdapterService(t, server)

	out, secrets, err := service.discoverBazarr(context.Background(), Connection{App: AppBazarr, URL: server.URL, APIKey: "bazarr-key"})
	if err != nil {
		t.Fatalf("discoverBazarr: %v", err)
	}
	if out.Subtitles == nil {
		t.Fatal("the subtitle plan was not produced")
	}
	plan := out.Subtitles
	if got := strings.Join(plan.Languages, ","); got != "en,fr" {
		t.Fatalf("enabled languages: %s", got)
	}
	if plan.MinimumScore != 90 || plan.SearchHours != 6 || plan.Sync.MaxOffsetSeconds != 60 || !plan.Sync.GoldenSection {
		t.Fatalf("subtitle settings: %+v", plan)
	}
	if len(plan.Profiles) != 2 {
		t.Fatalf("language profiles: %+v", plan.Profiles)
	}
	anime := plan.Profiles[0]
	if anime.Key != "1" || anime.Cutoff != 1 || len(anime.Languages) != 2 || !anime.Languages[0].Forced {
		t.Fatalf("profile mapping: %+v", anime)
	}
	if len(plan.Profiles[1].Languages) != 0 || len(plan.Profiles[1].Unsupported) == 0 {
		t.Fatalf("unsupported languages must be reported: %+v", plan.Profiles[1])
	}
	if len(plan.ProviderPlans) != 2 {
		t.Fatalf("provider plans: %+v", plan.ProviderPlans)
	}
	if plan.ProviderPlans[0].Type != "opensubtitles" || !plan.ProviderPlans[0].UsernameSet || !plan.ProviderPlans[0].PasswordSet {
		t.Fatalf("OpenSubtitles mapping: %+v", plan.ProviderPlans[0])
	}
	if len(plan.ProviderPlans[1].Unsupported) == 0 {
		t.Fatalf("unsupported providers must be reported: %+v", plan.ProviderPlans[1])
	}
	if secrets.subtitle == nil || secrets.subtitle.Password != "os-pass" {
		t.Fatalf("provider credentials stay in ephemeral state: %+v", secrets.subtitle)
	}
	encoded, _ := json.Marshal(plan)
	if strings.Contains(string(encoded), "os-pass") || strings.Contains(string(encoded), "os-user") {
		t.Fatalf("the subtitle plan leaks credentials: %s", encoded)
	}
}

func TestDiscoverLidarrContract(t *testing.T) {
	routes := map[string]string{
		"/api/v1/system/status": `{"appName":"Lidarr","version":"2.11.0.4520"}`,
		"/api/v1/rootfolder":    `[{"id":1,"path":"/media/music","accessible":true}]`,
		"/api/v1/qualityprofile": `[{"id":2,"name":"Lossless","upgradeAllowed":true,"cutoff":6,"items":[
			{"quality":{"id":6,"name":"FLAC"},"allowed":true},
			{"quality":{"id":3,"name":"MP3-256"},"allowed":true},
			{"quality":{"id":32,"name":"MP3-8"},"allowed":true}]}]`,
		"/api/v1/tag": `[{"id":1,"label":"favorites"}]`,
		"/api/v1/artist": `[{"id":10,"artistName":"Example Artist","sortName":"Example Artist","foreignArtistId":"4f1b3f7a-1111-4a2b-8c3d-0123456789ab",
			"monitored":true,"monitorNewItems":"new","qualityProfileId":2,"rootFolderPath":"/media/music","path":"/media/music/Example Artist","tags":[1]}]`,
		"/api/v1/album": `[{"id":100,"artistId":10,"title":"First Album","foreignAlbumId":"9c8d7e6f-2222-4b3c-9d4e-abcdefabcdef",
			"albumType":"Album","releaseDate":"2019-05-01","monitored":true,"mediumCount":1},
			{"id":101,"artistId":10,"title":"Bad Album","foreignAlbumId":"","albumType":"Single","releaseDate":"2020-01-01","monitored":false}]`,
		"/api/v1/config/naming": `{"artistFolderFormat":"{Artist Name}","standardTrackFormat":"{Artist Name} - {Album Title} - {track:00} - {Track Title}","multiDiscTrackFormat":"{Medium Number}-{track:00}"}`,
	}
	server, _ := fixtureServer(t, routes)
	service := testAdapterService(t, server)

	out, err := service.discoverLidarr(context.Background(), Connection{App: AppLidarr, URL: server.URL, APIKey: "lidarr-key"})
	if err != nil {
		t.Fatalf("discoverLidarr: %v", err)
	}
	if out.Music == nil {
		t.Fatal("the music plan was not produced")
	}
	if len(out.Music.Roots) != 1 || out.Music.Roots[0].Media != MediaMusic || out.Music.Roots[0].Path != "/media/music" {
		t.Fatalf("music roots: %+v", out.Music.Roots)
	}
	if len(out.Music.Profiles) != 1 {
		t.Fatalf("music profiles: %+v", out.Music.Profiles)
	}
	profile := out.Music.Profiles[0]
	if got := strings.Join(profile.Formats, ","); got != "flac,mp3" {
		t.Fatalf("mapped formats: %s", got)
	}
	if profile.MinBitrateKbps != 256 || profile.Cutoff != "flac" || !profile.Upgrade || profile.LosslessOnly {
		t.Fatalf("profile mapping: %+v", profile)
	}
	if len(profile.Unsupported) != 1 || profile.Unsupported[0] != "MP3-8" {
		t.Fatalf("unsupported qualities: %+v", profile.Unsupported)
	}
	if len(out.Music.Artists) != 1 {
		t.Fatalf("artists: %+v", out.Music.Artists)
	}
	artist := out.Music.Artists[0]
	if artist.MusicBrainzID != "4f1b3f7a-1111-4a2b-8c3d-0123456789ab" || artist.MonitorOption != "future" || artist.ProfileKey != "lidarr:2" || len(artist.Tags) != 1 {
		t.Fatalf("artist mapping: %+v", artist)
	}
	if len(artist.Albums) != 2 {
		t.Fatalf("albums: %+v", artist.Albums)
	}
	if artist.Albums[0].Year != 2019 || artist.Albums[0].Type != "Album" || !artist.Albums[0].Monitored {
		t.Fatalf("album mapping: %+v", artist.Albums[0])
	}
	if artist.Albums[1].Unsupported == "" {
		t.Fatalf("albums without a MusicBrainz id must be reported: %+v", artist.Albums[1])
	}
	naming := out.Music.Naming[0]
	if naming.Media != MediaMusic || naming.Folder != "{artist}/{album} ({year})" || naming.File != "{artist} - {album} - {track:02} - {title}" {
		t.Fatalf("music naming: %+v", naming)
	}
	if !naming.Applicable || len(naming.Unsupported) != 0 {
		t.Fatalf("music naming must map cleanly: %+v", naming)
	}
	multiDisc := false
	for _, entry := range out.Unsupported {
		if strings.Contains(entry.Detail, "multi-disc") {
			multiDisc = true
		}
	}
	if !multiDisc {
		t.Fatalf("the multi-disc format must be reported: %+v", out.Unsupported)
	}
	if out.Versions[AppLidarr] != "2.11.0.4520" {
		t.Fatalf("version: %+v", out.Versions)
	}
}

func TestDiscoverJellyfinContract(t *testing.T) {
	routes := map[string]string{
		"/System/Info":            `{"ServerName":"media","Version":"10.10.7","ProductName":"Jellyfin Server"}`,
		"/Library/VirtualFolders": `[{"Name":"Movies","CollectionType":"movies","Locations":["/media/movies"]},{"Name":"Shows","CollectionType":"tvshows","Locations":["/media/tv"]}]`,
	}
	server, _ := fixtureServer(t, routes)
	service := testAdapterService(t, server)

	plan, err := service.discoverJellyfin(context.Background(), Connection{App: AppJellyfin, URL: server.URL, APIKey: "jellyfin-key"})
	if err != nil {
		t.Fatalf("discoverJellyfin: %v", err)
	}
	if plan.Version != "10.10.7" || plan.Libraries["movies"] != 1 || plan.Libraries["tvshows"] != 1 || plan.Locations != 2 {
		t.Fatalf("jellyfin plan: %+v", plan)
	}
}

// redirectFixture counts requests and optionally records the credential it received.
type redirectFixture struct {
	server *httptest.Server
	calls  *atomic.Int64
	keys   *atomic.Value
}

func newRedirectFixture(t *testing.T, location func(r *http.Request) string) *redirectFixture {
	t.Helper()
	fixture := &redirectFixture{calls: &atomic.Int64{}, keys: &atomic.Value{}}
	fixture.keys.Store("")
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.calls.Add(1)
		seen := firstNonEmpty(r.Header.Get("X-Api-Key"), r.Header.Get("X-API-KEY"), r.Header.Get("X-Emby-Token"))
		if user, password, ok := r.BasicAuth(); ok {
			seen = firstNonEmpty(seen, user+":"+password)
		}
		if token := r.URL.Query().Get("apikey"); token != "" {
			seen = firstNonEmpty(seen, "query:"+token)
		}
		if seen != "" {
			fixture.keys.Store(seen)
		}
		if target := location(r); target != "" {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"appName":"Radarr","version":"6.1.1.10360","version":"4.0.4"}`))
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func TestAuthenticatedRedirectsNeverLeaveTheOrigin(t *testing.T) {
	cases := []struct {
		name       string
		connection func(url string) Connection
	}{
		{"X-Api-Key", func(url string) Connection { return Connection{App: AppRadarr, URL: url, APIKey: "radarr-secret"} }},
		{"X-API-KEY", func(url string) Connection { return Connection{App: AppBazarr, URL: url, APIKey: "bazarr-secret"} }},
		{"X-Emby-Token", func(url string) Connection { return Connection{App: AppJellyfin, URL: url, APIKey: "jellyfin-secret"} }},
		{"apikey query", func(url string) Connection { return Connection{App: AppSABnzbd, URL: url, APIKey: "sab-secret"} }},
		{"basic auth", func(url string) Connection {
			return Connection{App: AppNZBGet, URL: url, Username: "nzb-user", Password: "nzb-secret"}
		}},
		{"transmission basic auth", func(url string) Connection {
			return Connection{App: AppTransmission, URL: url, Username: "transmission-user", Password: "transmission-secret"}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			second := newRedirectFixture(t, func(*http.Request) string { return "" })
			first := newRedirectFixture(t, func(r *http.Request) string {
				return second.server.URL + r.URL.Path
			})
			service := testAdapterService(t, first.server)
			connection := test.connection(first.server.URL)

			_, err := service.connect(context.Background(), connection)
			if err == nil {
				t.Fatal("a cross-origin authenticated redirect must fail")
			}
			if second.calls.Load() != 0 {
				t.Fatalf("the second origin received %d request(s)", second.calls.Load())
			}
			if calls := first.calls.Load(); calls == 0 {
				t.Fatal("the first origin was never called")
			}
			if seen, _ := first.keys.Load().(string); seen == "" {
				t.Fatal("the fixture never received a credential, so the test proves nothing")
			}
			if !strings.Contains(err.Error(), "another host") {
				t.Fatalf("unclear redirect error: %v", err)
			}
			if strings.Contains(err.Error(), first.server.URL) || strings.Contains(err.Error(), second.server.URL) {
				t.Fatalf("the redirect error leaks an upstream URL: %v", err)
			}
			for _, secret := range connection.secrets() {
				if secret != "" && strings.Contains(err.Error(), secret) {
					t.Fatalf("the redirect error leaks a credential: %v", err)
				}
			}
		})
	}
}

func TestSameOriginRedirectsKeepCredentials(t *testing.T) {
	redirector := newRedirectFixture(t, func(r *http.Request) string {
		if strings.HasPrefix(r.URL.Path, "/mirror") {
			return ""
		}
		return "/mirror" + r.URL.Path
	})
	service := testAdapterService(t, redirector.server)

	version, err := service.connect(context.Background(), Connection{App: AppRadarr, URL: redirector.server.URL, APIKey: "radarr-secret"})
	if err != nil || version == "" {
		t.Fatalf("a same-origin redirect must be followed with its credentials: %v %q", err, version)
	}
	if redirector.calls.Load() < 2 {
		t.Fatalf("the same-origin redirect target was not reached: %d call(s)", redirector.calls.Load())
	}
	if seen, _ := redirector.keys.Load().(string); seen != "radarr-secret" {
		t.Fatalf("the credential did not survive the same-origin redirect: %q", seen)
	}
}

func TestRedirectsAreBoundedAndNeverDowngradeScheme(t *testing.T) {
	// A redirect chain on one origin stops after the bounded number of hops.
	var calls atomic.Int64
	chain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/hop-"+strconv.FormatInt(calls.Load(), 10), http.StatusFound)
	}))
	t.Cleanup(chain.Close)
	service := testAdapterService(t, chain)
	err := service.requestJSON(context.Background(), Connection{App: AppRadarr, URL: chain.URL, APIKey: "key"}, http.MethodGet, "/start", nil, nil, &arrStatus{})
	if err == nil || !strings.Contains(err.Error(), "too many times") {
		t.Fatalf("a redirect chain must be bounded: %v", err)
	}
	if calls.Load() > maxRedirects+1 {
		t.Fatalf("the chain kept going for %d requests", calls.Load())
	}

	// An HTTPS endpoint must not redirect to plain HTTP.
	plain := newRedirectFixture(t, func(*http.Request) string { return "" })
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.server.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(secure.Close)
	tlsService := testAdapterService(t, secure)
	err = tlsService.requestJSON(context.Background(), Connection{App: AppRadarr, URL: secure.URL, APIKey: "key"}, http.MethodGet, "/api/v3/system/status", nil, nil, &arrStatus{})
	if err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Fatalf("a scheme downgrade must be refused: %v", err)
	}
	if plain.calls.Load() != 0 {
		t.Fatalf("the plain HTTP origin received %d request(s)", plain.calls.Load())
	}
}

func TestGuardedClientDoesNotMutateTheCaller(t *testing.T) {
	caller := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 2}}
	service := &Service{client: caller}
	service.withDefaults()
	if caller.CheckRedirect != nil {
		t.Fatal("the caller client was mutated")
	}
	if service.http == caller {
		t.Fatal("requests must use a guarded copy")
	}
	if service.http.Transport != caller.Transport {
		t.Fatal("the caller transport must be preserved")
	}
}

func TestProfileSuggestionRequiresEquivalentSettings(t *testing.T) {
	base := ProfilePlan{Key: "sonarr:2", Source: AppSonarr, Name: "Standard", Qualities: []string{"Bluray-1080p", "WEB-1080p"}, Cutoff: "Bluray-1080p", Upgrade: true}
	profiles := []quality.Profile{
		{ID: "standard", Name: "Standard", Qualities: []string{"Bluray-1080p", "WEB-1080p"}, Cutoff: "Bluray-1080p", Upgrade: true},
		{ID: "any", Name: "Any", Qualities: []string{"Bluray-1080p", "WEB-1080p", "SD"}},
		{ID: "rule-bound", Name: "Rule bound", Qualities: []string{"Bluray-1080p", "WEB-1080p"}, Cutoff: "Bluray-1080p", Upgrade: true, Rules: []quality.Rule{{Name: "prefer", Pattern: "some-group", Score: 5}}},
	}
	if got := suggestProfile(profiles, base); got != "standard" {
		t.Fatalf("an equivalent profile must be suggested: %q", got)
	}
	// The same name with more qualities must never be mapped automatically.
	broadened := profiles[1]
	broadened.Name = "Standard"
	if got := suggestProfile([]quality.Profile{broadened}, base); got != "" {
		t.Fatalf("a name lookalike must not be mapped: %q", got)
	}
	if got := suggestProfile([]quality.Profile{profiles[2]}, base); got != "" {
		t.Fatalf("a profile with extra rules must not be mapped: %q", got)
	}
	differentCutoff := profiles[0]
	differentCutoff.Cutoff = "WEB-1080p"
	if got := suggestProfile([]quality.Profile{differentCutoff}, base); got != "" {
		t.Fatalf("a different cutoff must not be mapped: %q", got)
	}
	// Profile rank follows the listed order, so a reversed preference is a different profile.
	reversedOrder := profiles[0]
	reversedOrder.Qualities = []string{"WEB-1080p", "Bluray-1080p"}
	if got := suggestProfile([]quality.Profile{reversedOrder}, base); got != "" {
		t.Fatalf("a reversed quality order must not be mapped: %q", got)
	}
	differentSize := profiles[0]
	differentSize.MinMB = 2048
	if got := suggestProfile([]quality.Profile{differentSize}, base); got != "" {
		t.Fatalf("a size limit must not be mapped: %q", got)
	}
	languageBound := profiles[0]
	languageBound.Language = "en"
	if got := suggestProfile([]quality.Profile{languageBound}, base); got != "" {
		t.Fatalf("a language restriction must not be mapped: %q", got)
	}
	scored := profiles[0]
	scored.CutoffScore = 50
	if got := suggestProfile([]quality.Profile{scored}, base); got != "" {
		t.Fatalf("a score threshold must not be mapped: %q", got)
	}
	if got := suggestProfile(profiles, ProfilePlan{Name: "Nothing"}); got != "" {
		t.Fatalf("an unmappable source must stay unmapped: %q", got)
	}
}

func TestMusicProfileSuggestionRequiresEquivalentSettings(t *testing.T) {
	base := MusicProfilePlan{Key: "lidarr:2", Source: AppLidarr, Name: "Lossless", Formats: []string{"flac", "mp3"}, Cutoff: "flac", Upgrade: true, MinBitrateKbps: 256}
	profiles := []music.QualityProfile{
		{ID: "lossless", Name: "Lossless", Formats: []string{"flac", "alac", "wav"}, Cutoff: "flac"},
		{ID: "candidate", Name: "Candidate", Formats: []string{"mp3", "flac"}, Cutoff: "flac", Upgrade: true, MinBitrateKbps: 256},
		{ID: "ordered", Name: "Ordered", Formats: []string{"flac", "mp3"}, Cutoff: "flac", Upgrade: true, MinBitrateKbps: 256},
	}
	// formatRank reads profile.Formats in order, so the same set in another order is a different profile.
	if got := suggestMusicProfile(profiles, base); got != "ordered" {
		t.Fatalf("the profile with the same order must be suggested: %q", got)
	}
	reversed := append([]music.QualityProfile{}, profiles[1])
	if got := suggestMusicProfile(reversed, base); got != "" {
		t.Fatalf("a reversed format order must not be mapped: %q", got)
	}
	if musicProfileEquivalent(profiles[1], base) {
		t.Fatal("a reversed format order must not be equivalent")
	}
	if !musicProfileEquivalent(profiles[2], base) {
		t.Fatal("an identically ordered profile must be equivalent")
	}
	// The name lookalike keeps formats the source never allowed, so it must not be mapped.
	if got := suggestMusicProfile(profiles, MusicProfilePlan{Key: "lidarr:3", Source: AppLidarr, Name: "Lossless", Formats: []string{"flac"}}); got != "" {
		t.Fatalf("a name lookalike must not be mapped: %q", got)
	}
	bitrate := MusicProfilePlan{Key: "lidarr:4", Source: AppLidarr, Name: "Candidate", Formats: []string{"mp3", "flac"}, Cutoff: "flac", Upgrade: true, MinBitrateKbps: 320}
	if got := suggestMusicProfile(profiles, bitrate); got != "" {
		t.Fatalf("a different bitrate floor must not be mapped: %q", got)
	}
	sized := profiles[1]
	sized.MinMB = 500
	if got := suggestMusicProfile([]music.QualityProfile{sized}, base); got != "" {
		t.Fatalf("a size limit must not be mapped: %q", got)
	}
}

func TestProfileFromPlanKeepsEveryMappedSetting(t *testing.T) {
	source := ProfilePlan{
		Key: "radarr:3", Source: AppRadarr, Name: "HD", Qualities: []string{"Bluray-1080p", "WEB-1080p"},
		Cutoff: "Bluray-1080p", Upgrade: true, MinMB: 4096, MaxMB: 30720, Language: "en",
	}
	created := profileFromPlan(source)
	if !sameQualityOrder(created.Qualities, source.Qualities) || created.Cutoff != source.Cutoff || !created.Upgrade ||
		created.MinMB != 4096 || created.MaxMB != 30720 || created.Language != "en" {
		t.Fatalf("mapped settings were dropped: %+v", created)
	}
	created.Qualities[0] = "SD"
	if source.Qualities[0] == "SD" {
		t.Fatal("the plan qualities must not be shared with the created profile")
	}
	music := musicProfileFromPlan(MusicProfilePlan{Key: "lidarr:2", Source: AppLidarr, Name: "Lossless", Formats: []string{"flac"}, Cutoff: "flac", Upgrade: true, MinBitrateKbps: 256, LosslessOnly: true})
	if len(music.Formats) != 1 || music.Formats[0] != "flac" || !music.LosslessOnly || music.MinBitrateKbps != 256 || !music.Upgrade || music.Cutoff != "flac" {
		t.Fatalf("mapped music settings were dropped: %+v", music)
	}
}

func TestUniqueProfileNameKeepsExistingProfiles(t *testing.T) {
	taken := map[string]bool{"hd": true}
	if got := uniqueProfileName("HD", "sonarr:2", taken); got != "HD (sonarr:2)" {
		t.Fatalf("a taken name must be suffixed with the source key: %q", got)
	}
	if got := uniqueProfileName("HD", "sonarr:2", map[string]bool{"hd": true, "hd (sonarr:2)": true}); got != "HD (sonarr:2 2)" {
		t.Fatalf("a repeated collision must keep counting: %q", got)
	}
	if got := uniqueProfileName("", "sonarr:2", nil); got == "" {
		t.Fatal("an unnamed source profile still needs a name")
	}
	if got := uniqueProfileName("Fresh", "sonarr:2", taken); got != "Fresh" {
		t.Fatalf("a free name must be kept: %q", got)
	}
}

func TestRequestRetriesTransientFailures(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(radarrFixture))
	}))
	t.Cleanup(server.Close)
	service := testAdapterService(t, server)

	var status arrStatus
	err := service.requestJSON(context.Background(), Connection{App: AppRadarr, URL: server.URL, APIKey: "key"}, http.MethodGet, "/api/v3/system/status", nil, nil, &status)
	if err != nil || status.Version != "6.1.1.10360" {
		t.Fatalf("transient failures must be retried: err=%v status=%+v", err, status)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected three attempts, got %d", calls.Load())
	}
}

func TestRequestDoesNotRetryAuthorizationFailures(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	service := testAdapterService(t, server)

	err := service.requestJSON(context.Background(), Connection{App: AppRadarr, URL: server.URL, APIKey: "key"}, http.MethodGet, "/api/v3/system/status", nil, nil, &arrStatus{})
	if err == nil || !strings.Contains(err.Error(), "authorization failed") {
		t.Fatalf("authorization failures must be reported: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("authorization failures must not be retried, got %d attempts", calls.Load())
	}
	if strings.Contains(err.Error(), `"key"`) || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("the error leaks credentials or the upstream URL: %v", err)
	}
}

func TestRequestHonorsTimeoutsAndResponseLimits(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(slow.Close)
	service := testAdapterService(t, slow)
	service.callTimeout = 50 * time.Millisecond
	service.retries = 0

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := service.requestJSON(ctx, Connection{App: AppRadarr, URL: slow.URL, APIKey: "key"}, http.MethodGet, "/slow", nil, nil, &arrStatus{}); err == nil {
		t.Fatal("a slow upstream must time out")
	} else if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout errors must stay diagnosable: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("the call timeout was not applied: %s", elapsed)
	}

	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	t.Cleanup(large.Close)
	limited := testAdapterService(t, large)
	limited.maxResponse = 512
	limited.retries = 0
	err := limited.requestJSON(context.Background(), Connection{App: AppRadarr, URL: large.URL, APIKey: "key"}, http.MethodGet, "/large", nil, nil, &arrStatus{})
	if err == nil || !strings.Contains(err.Error(), "exceeded the supported size") {
		t.Fatalf("oversized responses must be rejected: %v", err)
	}
}

func TestSanitizeRemovesNestedSecrets(t *testing.T) {
	message := fmt.Sprintf("radarr: failed for apiKey=%s password=%s at %s", "secret-key", "secret-pass", "http://192.0.2.10:7878")
	cleaned := sanitize(message, []string{"secret-key", "secret-pass"})
	if strings.Contains(cleaned, "secret-key") || strings.Contains(cleaned, "secret-pass") {
		t.Fatalf("secrets survived sanitizing: %s", cleaned)
	}
	if !strings.Contains(cleaned, "http://192.0.2.10:7878") {
		// Upstream URLs are only included when the caller put them in the message; adapter errors never do.
		t.Fatalf("sanitize truncated unexpectedly: %s", cleaned)
	}
}

func TestSelectIndexerIsExplicit(t *testing.T) {
	plan := &Plan{Indexers: []IndexerPlan{
		{Key: "prowlarr:1", Name: "NZBGeek"},
		{Key: "prowlarr:2", Name: "NZBGeek backup"},
	}}
	if _, reason := selectIndexer(plan, ""); !strings.Contains(reason, "select one") {
		t.Fatalf("an ambiguous indexer choice must be explicit: %s", reason)
	}
	selected, reason := selectIndexer(plan, "prowlarr:2")
	if reason != "" || selected.Key != "prowlarr:2" {
		t.Fatalf("the selected indexer must win deterministically: %+v %s", selected, reason)
	}
	if _, reason := selectIndexer(plan, "prowlarr:9"); !strings.Contains(reason, "not part of this plan") {
		t.Fatalf("unknown selections must be reported: %s", reason)
	}
	single := &Plan{Indexers: []IndexerPlan{{Key: "prowlarr:1"}}}
	if candidate, reason := selectIndexer(single, ""); reason != "" || candidate.Key != "prowlarr:1" {
		t.Fatalf("a single candidate is selected automatically: %+v %s", candidate, reason)
	}
}

func TestMergeSnapshotCollectsEveryProviderCandidate(t *testing.T) {
	found := snapshot{}
	mergeSnapshot(&found, snapshot{
		Indexers:      []IndexerPlan{{Key: "prowlarr:1", Source: AppProwlarr, Name: "NZBGeek"}},
		UsenetSources: []UsenetPlan{{Key: "sabnzbd", Source: AppSABnzbd, Host: "a.example.com"}},
	})
	mergeSnapshot(&found, snapshot{
		UsenetSources: []UsenetPlan{{Key: "nzbget", Source: AppNZBGet, Host: "b.example.com"}},
	})
	if len(found.Indexers) != 1 || len(found.UsenetSources) != 2 {
		t.Fatalf("every candidate must survive the merge: %+v %+v", found.Indexers, found.UsenetSources)
	}
	if found.UsenetSources[0].Key != "sabnzbd" || found.UsenetSources[1].Key != "nzbget" {
		t.Fatalf("candidate order must follow the connection order: %+v", found.UsenetSources)
	}
	if len(found.Warnings) != 0 {
		t.Fatalf("collecting candidates must not warn about silent overwrites: %+v", found.Warnings)
	}
}

func TestConnectionValidation(t *testing.T) {
	cases := []struct {
		name       string
		connection Connection
		want       string
	}{
		{"unknown app", Connection{App: "plex", URL: "http://host:32400"}, "not a supported application"},
		{"relative url", Connection{App: AppRadarr, URL: "host:7878", APIKey: "k"}, "absolute http or https URL"},
		{"credentials in url", Connection{App: AppRadarr, URL: "http://user:pass@host:7878", APIKey: "k"}, "credential fields"},
		{"missing key", Connection{App: AppRadarr, URL: "http://host:7878"}, "requires an API key"},
		{"nzbget password", Connection{App: AppNZBGet, URL: "http://host:6789", Username: "u"}, "username and password"},
		{"transmission half auth", Connection{App: AppTransmission, URL: "http://host:9091", Username: "u"}, "both a username and a password"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := test.connection.validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate: got %v, want %q", err, test.want)
			}
		})
	}
	if err := (Connection{App: AppTransmission, URL: "http://host:9091/"}).normalize().validate(); err != nil {
		t.Fatalf("transmission without auth is valid: %v", err)
	}
}
