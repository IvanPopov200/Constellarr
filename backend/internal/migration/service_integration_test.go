package migration

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
	"github.com/IvanPopov200/Constellarr/backend/internal/subtitles"
	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

// migrationSchema creates an isolated schema so migration tests never touch real tables.
func migrationSchema(t *testing.T) *pgxpool.Pool {
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
	schema := "migration_test_" + strings.ToLower(rand.Text())
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

const (
	radarrKey       = "radarr-fixture-key"
	sonarrKey       = "sonarr-fixture-key"
	lidarrKey       = "lidarr-fixture-key"
	prowlarrKey     = "prowlarr-fixture-key"
	bazarrKey       = "bazarr-fixture-key"
	sabKey          = "sab-fixture-key"
	nzbgetKey       = "nzbget-fixture-password"
	jellyfinKey     = "jellyfin-fixture-key"
	sabUser         = "news-fixture-user"
	sabPassword     = "news-fixture-password"
	subtitleUser    = "opensubtitles-fixture-user"
	subtitlePass    = "opensubtitles-fixture-password"
	transmissionKey = "transmission-fixture-password"
	artistMBID      = "4f1b3f7a-1111-4a2b-8c3d-0123456789ab"
	albumMBID       = "9c8d7e6f-2222-4b3c-9d4e-abcdefabcdef"
	completeHash    = "0123456789abcdef0123456789abcdef01234567"
	partialHash     = "89abcdef0123456789abcdef0123456789abcdef"
)

type migrationEnv struct {
	pool        *pgxpool.Pool
	service     *Service
	movies      *movies.Service
	tv          *tv.Service
	music       *music.Service
	subtitles   *subtitles.Service
	torrents    *torrents.Service
	manager     *downloads.Manager
	handler     http.Handler
	connections []Connection
	directory   string
	moviesRoot  string
	tvRoot      string
	musicRoot   string
	torrentRoot string
	upstream    *atomic.Int64
}

func newTestService(t *testing.T, env *migrationEnv, options Options) (*Service, http.Handler) {
	t.Helper()
	options.Movies, options.TV, options.Downloads = env.movies, env.tv, env.manager
	options.Music, options.Subtitles, options.Torrents = env.music, env.subtitles, env.torrents
	if options.RetryDelay == 0 {
		options.RetryDelay = time.Millisecond
	}
	if options.CallTimeout == 0 {
		options.CallTimeout = 3 * time.Second
	}
	if options.ApplyBudget == 0 {
		options.ApplyBudget = 30 * time.Second
	}
	service, err := New(context.Background(), env.pool, options)
	if err != nil {
		t.Fatalf("migration.New: %v", err)
	}
	mux := http.NewServeMux()
	service.Register(mux)
	return service, mux
}

func newMigrationEnv(t *testing.T, rejectRadarrKey bool) *migrationEnv {
	t.Helper()
	for _, name := range []string{"OMDB_URL", "OMDB_API_KEY", "JELLYFIN_URL", "JELLYFIN_API_KEY", "IMPORT_WEBHOOK_URL"} {
		t.Setenv(name, "")
	}
	ctx := context.Background()
	pool := migrationSchema(t)
	directory := t.TempDir()
	manager, err := downloads.New(ctx, pool, downloads.Config{
		Directory: directory, IndexerURL: "http://127.0.0.1:9/api", APIKey: "initial-key",
		Usenet: usenet.Config{Host: "initial.example.com", Port: 563, Username: "initial-user", Password: "initial-pass", Connections: 4},
	})
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
	musicService, err := music.New(ctx, pool, manager)
	if err != nil {
		t.Fatalf("music.New: %v", err)
	}
	subtitleService, err := subtitles.New(ctx, pool, subtitles.Options{Movies: movieService, TV: tvService})
	if err != nil {
		t.Fatalf("subtitles.New: %v", err)
	}
	torrentService, err := torrents.New(ctx, pool, torrents.Options{Directory: directory, Testing: true})
	if err != nil {
		t.Fatalf("torrents.New: %v", err)
	}
	movieCfg, err := movieService.Store.Config(ctx)
	if err != nil {
		t.Fatalf("movie config: %v", err)
	}
	movieCfg.ImportMode = "copy"
	if _, err := movieService.SetConfig(ctx, movieCfg); err != nil {
		t.Fatalf("save movie config: %v", err)
	}
	tvCfg, err := tvService.Config(ctx)
	if err != nil {
		t.Fatalf("tv config: %v", err)
	}
	tvCfg.ImportMode = "copy"
	if _, err := tvService.SetConfig(ctx, tvCfg); err != nil {
		t.Fatalf("save tv config: %v", err)
	}
	brainz := brainzFixture(t)
	musicCfg, err := musicService.ConfigView(ctx)
	if err != nil {
		t.Fatalf("music config: %v", err)
	}
	musicCfg.ImportMode = "copy"
	musicCfg.MusicBrainzURL = brainz + "/ws/2/"
	musicCfg.MusicBrainzRateMs = 0
	if _, err := musicService.SetConfig(ctx, musicCfg); err != nil {
		t.Fatalf("save music config: %v", err)
	}
	upstream := &atomic.Int64{}
	env := &migrationEnv{
		pool: pool, movies: movieService, tv: tvService, music: musicService,
		subtitles: subtitleService, torrents: torrentService, manager: manager,
		directory: directory, upstream: upstream,
	}
	env.moviesRoot = filepath.Join(directory, "movies")
	env.tvRoot = filepath.Join(directory, "tv")
	env.musicRoot = filepath.Join(directory, "music")
	env.torrentRoot = filepath.Join(directory, "downloads")
	for _, root := range []string{env.moviesRoot, env.tvRoot, env.musicRoot, env.torrentRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create mapped root: %v", err)
		}
	}
	writeTestFile(t, filepath.Join(env.moviesRoot, "The Dark Knight (2008)", "The.Dark.Knight.2008.1080p.mkv"), "movie payload")
	writeTestFile(t, filepath.Join(env.tvRoot, "The Office", "Season 1", "The.Office.S01E01.480p.DVDRip.mkv"), "episode payload")
	completeFile := filepath.Join(env.torrentRoot, "Example.Release", "file.mkv")
	writeTestFile(t, completeFile, strings.Repeat("x", 64))

	radarr := arrFixture(t, upstream, map[string]string{
		"/api/v3/system/status": `{"appName":"Radarr","version":"6.1.1.10360"}`,
		"/api/v3/rootfolder":    `[{"id":1,"path":"/media/movies","accessible":true}]`,
		"/api/v3/qualityprofile": `[{"id":3,"name":"HD-1080p","upgradeAllowed":true,"cutoff":7,"items":[
			{"quality":{"id":7,"name":"Bluray-1080p"},"allowed":true},
			{"quality":{"id":8,"name":"WEBDL-1080p"},"allowed":true}]}]`,
		"/api/v3/tag": `[{"id":1,"label":"favorites"}]`,
		"/api/v3/movie": `[
			{"id":1,"title":"The Dark Knight","year":2008,"imdbId":"tt0468569","monitored":true,"tags":[1],
			 "qualityProfileId":3,"rootFolderPath":"/media/movies","path":"/media/movies/The Dark Knight (2008)","hasFile":true,
			 "movieFile":{"relativePath":"The.Dark.Knight.2008.1080p.mkv","path":"/media/movies/The Dark Knight (2008)/The.Dark.Knight.2008.1080p.mkv","size":14}},
			{"id":2,"title":"Catalog Only","year":1999,"imdbId":"tt0000002","monitored":false,"tags":[],
			 "qualityProfileId":3,"rootFolderPath":"/media/movies","path":"/media/movies/Catalog Only (1999)","hasFile":true,
			 "movieFile":{"relativePath":"Catalog.Only.1999.mkv","path":"/media/movies/Catalog Only (1999)/Catalog.Only.1999.mkv","size":14}}]`,
		"/api/v3/config/naming": `{"movieFolderFormat":"{Movie Title} ({Release Year})","standardMovieFormat":"{Movie Title} ({Release Year}) {Quality Full}"}`,
	}, radarrKey, rejectRadarrKey)
	sonarr := arrFixture(t, upstream, map[string]string{
		"/api/v3/system/status": `{"appName":"Sonarr","version":"4.0.17.2952"}`,
		"/api/v3/rootfolder":    `[{"id":1,"path":"/media/tv","accessible":true}]`,
		"/api/v3/qualityprofile": `[{"id":2,"name":"HD-720p","upgradeAllowed":false,"cutoff":6,"items":[
			{"quality":{"id":6,"name":"Bluray-720p"},"allowed":true},
			{"quality":{"id":5,"name":"WEBDL-720p"},"allowed":true}]}]`,
		"/api/v3/tag": `[]`,
		"/api/v3/series": `[{"id":1,"title":"The Office","year":2005,"imdbId":"tt0386676","tvdbId":73244,"monitored":true,
			"monitorNewItems":"all","tags":[],"qualityProfileId":2,"rootFolderPath":"/media/tv","path":"/media/tv/The Office",
			"statistics":{"episodeFileCount":1},
			"seasons":[{"seasonNumber":1,"monitored":true},{"seasonNumber":2,"monitored":false}]}]`,
		"/api/v3/episodefile": `[{"id":1,"seriesId":1,"seasonNumber":1,"relativePath":"Season 1/The.Office.S01E01.480p.DVDRip.mkv",
			"path":"/media/tv/The Office/Season 1/The.Office.S01E01.480p.DVDRip.mkv","size":15}]`,
		"/api/v3/config/naming": `{"seriesFolderFormat":"{Series Title}","seasonFolderFormat":"Season {season}","standardEpisodeFormat":"{Series Title} - S{season:00}E{episode:00} - {Episode Title} {Quality Full}"}`,
	}, sonarrKey, false)
	lidarr := arrFixture(t, upstream, map[string]string{
		"/api/v1/system/status": `{"appName":"Lidarr","version":"2.11.0.4520"}`,
		"/api/v1/rootfolder":    `[{"id":1,"path":"/media/music","accessible":true}]`,
		"/api/v1/qualityprofile": `[{"id":2,"name":"Lossless","upgradeAllowed":true,"cutoff":6,"items":[
			{"quality":{"id":6,"name":"FLAC"},"allowed":true},
			{"quality":{"id":3,"name":"MP3-256"},"allowed":true}]}]`,
		"/api/v1/tag": `[{"id":1,"label":"favorites"}]`,
		"/api/v1/artist": `[{"id":10,"artistName":"Example Artist","sortName":"Example Artist","foreignArtistId":"` + artistMBID + `",
			"monitored":true,"monitorNewItems":"new","qualityProfileId":2,"rootFolderPath":"/media/music","path":"/media/music/Example Artist","tags":[1]}]`,
		"/api/v1/album": `[{"id":100,"artistId":10,"title":"First Album","foreignAlbumId":"` + albumMBID + `",
			"albumType":"Album","releaseDate":"2019-05-01","monitored":true,"mediumCount":1},
			{"id":101,"artistId":10,"title":"Unmatched Single","foreignAlbumId":"","albumType":"Single","releaseDate":"2020-01-01","monitored":false}]`,
		"/api/v1/config/naming": `{"artistFolderFormat":"{Artist Name}","standardTrackFormat":"{Artist Name} - {Album Title} - {track:00} - {Track Title}","multiDiscTrackFormat":"{Medium Number}-{track:00}"}`,
	}, lidarrKey, false)
	prowlarr := arrFixture(t, upstream, map[string]string{
		"/api/v1/system/status": `{"appName":"Prowlarr","version":"2.3.5.5327"}`,
		"/api/v1/indexer": `[
			{"id":1,"name":"NZBGeek","enable":true,"definitionName":"Newznab","protocol":"usenet","fields":[]},
			{"id":2,"name":"Public tracker","enable":true,"definitionName":"Cardigann","protocol":"torrent","fields":[]}]`,
		"/api/v1/indexer/1": `{"id":1,"name":"NZBGeek","fields":[{"name":"baseUrl","value":"https://api.nzbgeek.info"},{"name":"apiPath","value":"/api"},{"name":"apiKey","value":"` + radarrKey + `"}]}`,
		"/api/v1/indexer/2": `{"id":2,"name":"Public tracker","fields":[{"name":"baseUrl","value":"https://tracker.example"}]}`,
	}, prowlarrKey, false)
	bazarr := arrFixture(t, upstream, map[string]string{
		"/api/system/status":             `{"data":{"bazarr_version":"1.5.6"}}`,
		"/api/system/languages":          `[{"name":"English","code2":"en","enabled":true},{"name":"Polish","code2":"pl","enabled":true},{"name":"Afar","code2":"aa","enabled":false}]`,
		"/api/system/languages/profiles": `[{"profileId":"1","name":"Anime","cutoff":"en","items":[{"language":"en","forced":true,"hi":false},{"language":"pl","forced":false,"hi":false}]}]`,
		"/api/system/settings": `{"general":{"single_language":false,"language_equals":[],"adaptive_searching":true,"minimum_score":90,"minimum_score_movie":70,
			"upgrade_subs":true,"enabled_providers":["opensubtitlescom","subssabbz"],"path_mappings":[],"serie_default_profile":"1","movie_default_profile":"","wanted_search_frequency":6},
			"subsync":{"use_subsync":true,"max_offset_seconds":60,"subsync_threshold":90,"subsync_movie_threshold":70},
			"opensubtitlescom":{"username":"` + subtitleUser + `","password":"` + subtitlePass + `"}}`,
	}, bazarrKey, false)
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		if r.URL.Query().Get("apikey") != sabKey {
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("mode") == "version" {
			_, _ = w.Write([]byte(`{"version":"4.1.0"}`))
			return
		}
		_, _ = w.Write([]byte(`{"config":{"servers":[{"name":"news.example.com","host":"news.example.com","port":563,"username":"` + sabUser + `","password":"` + sabPassword + `","connections":8,"ssl":1,"enable":1}]}}`))
	}))
	t.Cleanup(sab.Close)
	nzbget := nzbgetFixture(t, upstream)
	transmission := transmissionFixture(t, upstream)
	jellyfin := arrFixture(t, upstream, map[string]string{
		"/System/Info":            `{"ServerName":"media","Version":"10.10.7","ProductName":"Jellyfin Server"}`,
		"/Library/VirtualFolders": `[{"Name":"Movies","CollectionType":"movies","Locations":["/media/movies"]},{"Name":"Shows","CollectionType":"tvshows","Locations":["/media/tv"]}]`,
	}, jellyfinKey, false)

	env.connections = []Connection{
		{App: AppRadarr, URL: radarr, APIKey: radarrKey},
		{App: AppSonarr, URL: sonarr, APIKey: sonarrKey},
		{App: AppLidarr, URL: lidarr, APIKey: lidarrKey},
		{App: AppProwlarr, URL: prowlarr, APIKey: prowlarrKey},
		{App: AppBazarr, URL: bazarr, APIKey: bazarrKey},
		{App: AppSABnzbd, URL: sab.URL, APIKey: sabKey},
		{App: AppNZBGet, URL: nzbget, Username: "nzbget", Password: nzbgetKey},
		{App: AppTransmission, URL: transmission, Username: "transmission", Password: transmissionKey},
		{App: AppJellyfin, URL: jellyfin, APIKey: jellyfinKey},
	}
	env.service, env.handler = newTestService(t, env, Options{})
	return env
}

// brainzFixture serves the MusicBrainz endpoints the music module reads.
func brainzFixture(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/ws/2/artist/"):
			fmt.Fprintf(w, `{"id":"%s","name":"Example Artist","sort-name":"Example Artist","country":"GB","type":"Group"}`, artistMBID)
		case strings.HasPrefix(r.URL.Path, "/ws/2/release-group/"):
			fmt.Fprintf(w, `{"id":"%s","title":"First Album","primary-type":"Album","first-release-date":"2019-05-01","artist-credit":[{"name":"Example Artist","artist":{"id":"%s","name":"Example Artist"}}],"releases":[]}`, albumMBID, artistMBID)
		case r.URL.Path == "/ws/2/release-group":
			fmt.Fprintf(w, `{"release-groups":[{"id":"%s","title":"First Album","primary-type":"Album","first-release-date":"2019-05-01"}]}`, albumMBID)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func nzbgetFixture(t *testing.T, calls *atomic.Int64) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "nzbget" || password != nzbgetKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
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
				{"Name":"Server1.Active","Value":"yes"},{"Name":"Server1.Host","Value":"fallback.example.com"},
				{"Name":"Server1.Port","Value":"563"},{"Name":"Server1.Username","Value":"` + sabUser + `"},
				{"Name":"Server1.Password","Value":"` + sabPassword + `"},{"Name":"Server1.Connections","Value":"8"},
				{"Name":"Server1.Encryption","Value":"yes"}]}`))
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func arrFixture(t *testing.T, calls *atomic.Int64, routes map[string]string, expectedKey string, rejectKey bool) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		key := strings.TrimSpace(r.Header.Get("X-Api-Key"))
		if key == "" {
			key = strings.TrimSpace(r.Header.Get("X-API-KEY"))
		}
		if key == "" {
			key = strings.TrimSpace(r.Header.Get("X-Emby-Token"))
		}
		if rejectKey || key != expectedKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func transmissionFixture(t *testing.T, calls *atomic.Int64) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "transmission" || password != transmissionKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "fixture-session" {
			w.Header().Set("X-Transmission-Session-Id", "fixture-session")
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
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
			fmt.Fprintf(w, `{"result":"success","arguments":{"torrents":[
				{"id":1,"name":"Example.Release","hashString":"%s","magnetLink":"magnet:?xt=urn:btih:%s&dn=Example.Release","status":6,"percentDone":1,
				 "downloadDir":"/downloads","labels":["tv"],"totalSize":64,"leftUntilDone":0,"addedDate":1700000000,"isFinished":true,
				 "trackers":[{"announce":"https://tracker.example/announce?passkey=secret-passkey"}],
				 "files":[{"name":"Example.Release/file.mkv","length":64,"bytesCompleted":64}]},
				{"id":2,"name":"Partial.Release","hashString":"%s","magnetLink":"magnet:?xt=urn:btih:%s&dn=Partial.Release","status":4,"percentDone":0.25,
				 "downloadDir":"/downloads","labels":["tv"],"totalSize":100,"leftUntilDone":75,"addedDate":1700000100,"isFinished":false,
				 "trackers":[],"files":[{"name":"Partial.Release/missing.mkv","length":100,"bytesCompleted":25}]}]}}`, completeHash, completeHash, partialHash, partialHash)
		default:
			http.Error(w, "unknown method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create folder: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func migrationCall(t *testing.T, handler http.Handler, method, target, body string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes()
}

func connectionsJSON(t *testing.T, connections []Connection) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Connections []Connection `json:"connections"`
	}{connections})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func previewPlan(t *testing.T, handler http.Handler, env *migrationEnv) PlanView {
	t.Helper()
	status, raw := migrationCall(t, handler, http.MethodPost, "/api/v1/migration/preview", connectionsJSON(t, env.connections))
	if status != http.StatusCreated {
		t.Fatalf("preview: %d %s", status, raw)
	}
	var view PlanView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("preview response is not JSON: %s", raw)
	}
	return view
}

func applyInput(env *migrationEnv, overrides func(*ApplyInput)) ApplyInput {
	input := ApplyInput{
		Movies: true, TV: true, Music: true, Files: true, Naming: true, Providers: true,
		Subtitles: true, Torrents: true, TorrentJobs: true, CreateProfiles: true,
		MoviesRoots:     map[string]string{"/media/movies": env.moviesRoot},
		TVRoots:         map[string]string{"/media/tv": env.tvRoot},
		MusicRoots:      map[string]string{"/media/music": env.musicRoot},
		TorrentRoots:    map[string]string{"/downloads": env.torrentRoot},
		Profiles:        map[string]string{},
		MusicProfiles:   map[string]string{},
		Usenet:          "sabnzbd",
		UsenetFallbacks: []string{"nzbget"},
		Torznab:         []string{"prowlarr:2"},
		Redownload:      []string{partialHash},
		Connections:     env.connections,
	}
	if overrides != nil {
		overrides(&input)
	}
	return input
}

// applyPlan drives the bounded batches until no pending items remain.
func applyPlan(t *testing.T, handler http.Handler, planID string, input ApplyInput) ApplyResult {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result := ApplyResult{}
	for round := 0; round < 12; round++ {
		status, raw := migrationCall(t, handler, http.MethodPost, "/api/v1/migration/plans/"+planID+"/apply", string(encoded))
		if status != http.StatusOK {
			t.Fatalf("apply round %d: %d %s", round, status, raw)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("apply response is not JSON: %s", raw)
		}
		if result.Remaining == 0 {
			return result
		}
	}
	t.Fatalf("apply did not finish after 12 rounds: %+v", result)
	return result
}

func planItemsFor(t *testing.T, env *migrationEnv, planID string) []PlanItem {
	t.Helper()
	items, err := env.service.items(context.Background(), planID, "", 500)
	if err != nil {
		t.Fatalf("plan items: %v", err)
	}
	return items
}

func itemFor(t *testing.T, env *migrationEnv, planID, kind, target string) PlanItem {
	t.Helper()
	for _, item := range planItemsFor(t, env, planID) {
		if item.Kind == kind && item.Target == target {
			return item
		}
	}
	t.Fatalf("no %s item for %q in plan %s", kind, target, planID)
	return PlanItem{}
}

func TestMigrationWizardImportsCatalogAndSettings(t *testing.T) {
	env := newMigrationEnv(t, false)
	ctx := context.Background()

	status, raw := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/connections/test", connectionsJSON(t, env.connections))
	if status != http.StatusOK {
		t.Fatalf("connections test: %d %s", status, raw)
	}
	var tests struct {
		Results []connectionTest `json:"results"`
	}
	if err := json.Unmarshal(raw, &tests); err != nil {
		t.Fatalf("test response is not JSON: %s", raw)
	}
	if len(tests.Results) != len(env.connections) {
		t.Fatalf("connection results: %+v", tests.Results)
	}
	for _, result := range tests.Results {
		if !result.OK || result.Version == "" {
			t.Errorf("%s was not verified: %+v", result.App, result)
		}
	}

	view := previewPlan(t, env.handler, env)
	if view.Counts.Movies != 2 || view.Counts.Series != 1 || view.Counts.Files != 3 || view.Counts.Seasons != 1 {
		t.Fatalf("catalog counts: %+v", view.Counts)
	}
	if view.Counts.Artists != 1 || view.Counts.Albums != 1 {
		t.Fatalf("music counts: %+v", view.Counts)
	}
	if view.Counts.Indexers != 2 || view.Counts.NewsServer != 2 || view.Counts.Torrents != 2 || view.Counts.Languages != 2 {
		t.Fatalf("source counts: %+v", view.Counts)
	}
	if len(view.Indexers) != 1 || view.Indexers[0].Key != "prowlarr:1" || len(view.Torznab) != 1 || view.Torznab[0].Key != "prowlarr:2" {
		t.Fatalf("indexer candidates: %+v %+v", view.Indexers, view.Torznab)
	}
	if len(view.UsenetSources) != 2 {
		t.Fatalf("news server candidates: %+v", view.UsenetSources)
	}
	// The Lidarr profile shares a name with a music profile but not its formats, so it stays unmapped.
	if view.Music == nil || len(view.Music.Profiles) != 1 || view.Music.Profiles[0].Suggested != "" || view.Music.Roots[0].Path != "/media/music" {
		t.Fatalf("music plan: %+v", view.Music)
	}
	// A source profile with no exact equivalent must stay unmapped instead of widening to "any" or "hd".
	if len(view.Profiles) != 2 {
		t.Fatalf("profiles: %+v", view.Profiles)
	}
	for _, profile := range view.Profiles {
		if profile.Suggested != "" {
			t.Fatalf("profile %q must not be silently mapped: %+v", profile.Name, profile)
		}
	}
	if view.Subtitles == nil || view.Subtitles.MinimumScore != 90 || view.Subtitles.SearchHours != 6 || len(view.Subtitles.ProviderPlans) != 2 {
		t.Fatalf("subtitle plan: %+v", view.Subtitles)
	}
	if view.Torrents == nil || !view.Torrents.SpeedDownEnabled || view.Torrents.DownloadQueueSize != 5 {
		t.Fatalf("torrent plan: %+v", view.Torrents)
	}
	if len(view.Failures) != 0 || len(view.Warnings) == 0 {
		t.Fatalf("multiple candidates must be flagged: %+v", view.Warnings)
	}
	assertNoSecrets(t, raw)

	view2 := previewPlan(t, env.handler, env)
	_ = view2 // A second preview must stay deterministic.

	// Applying uses the stored plan and must not call the source applications again.
	callsBeforeApply := env.upstream.Load()
	result := applyPlan(t, env.handler, view.ID, applyInput(env, nil))
	if result.Status != statusApplied || result.Totals.Failed != 0 || result.Totals.Pending != 0 {
		t.Fatalf("apply result: %+v %+v", result.Totals, result.Failures)
	}
	if calls := env.upstream.Load() - callsBeforeApply; calls != 0 {
		t.Fatalf("apply made %d upstream calls, expected none", calls)
	}

	catalog, err := env.movies.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("movie catalog: %+v", catalog)
	}
	if item := itemFor(t, env, view.ID, itemMovieFile, "radarr:2"); item.Status != itemSkipped {
		t.Fatalf("a missing local file must be skipped: %+v", item)
	}
	series, err := env.tv.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 1 {
		t.Fatalf("TV catalog: %+v", series)
	}
	episodes, err := env.tv.Store.Episodes(ctx, series[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, episode := range episodes {
		files += len(episode.Files)
	}
	if files != 1 {
		t.Fatalf("episode file count: %+v", episodes)
	}

	artists, err := env.music.Artists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != 1 || artists[0].MusicBrainzID != artistMBID || artists[0].Name != "Example Artist" {
		t.Fatalf("music catalog: %+v", artists)
	}
	if artists[0].MonitorOption != "future" || !artists[0].Monitored {
		t.Fatalf("artist monitoring: %+v", artists[0])
	}
	albums, err := env.music.Albums(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 1 || albums[0].MusicBrainzID != albumMBID || !albums[0].Monitored {
		t.Fatalf("album catalog: %+v", albums)
	}
	movieProfiles, err := env.movies.Store.Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var created *quality.Profile
	for i := range movieProfiles {
		if movieProfiles[i].Name == "HD-1080p" {
			created = &movieProfiles[i]
		}
	}
	if created == nil || !sameQualityOrder(created.Qualities, []string{"Bluray-1080p", "WEB-1080p"}) {
		t.Fatalf("the created profile must keep the source qualities exactly: %+v", movieProfiles)
	}
	musicCfg, err := env.music.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(musicCfg.QualityProfiles) != 4 || musicCfg.FolderTemplate != "{artist}/{album} ({year})" || musicCfg.FileTemplate != "{artist} - {album} - {track:02} - {title}" {
		t.Fatalf("music configuration: %+v", musicCfg)
	}
	migratedMusic := ""
	for _, profile := range musicCfg.QualityProfiles {
		if profile.Name == "Lossless (lidarr:2)" {
			migratedMusic = profile.ID
			if !sameQualityOrder(profile.Formats, []string{"flac", "mp3"}) || profile.MinBitrateKbps != 256 {
				t.Fatalf("the migrated music profile must keep the source settings: %+v", profile)
			}
		}
		if profile.ID == "lossless" && !sameQualityOrder(profile.Formats, []string{"flac", "alac", "wav"}) {
			t.Fatalf("the existing music profile was overwritten: %+v", profile)
		}
	}
	if migratedMusic == "" || artists[0].ProfileID != migratedMusic {
		t.Fatalf("artist profile mapping: %+v", artists[0])
	}
	if item := itemFor(t, env, view.ID, itemNaming, "music"); item.Status != itemDone {
		t.Fatalf("music naming must apply: %+v", item)
	}

	settings := env.manager.Config()
	if settings.IndexerURL != "https://api.nzbgeek.info/api" || settings.APIKey != radarrKey {
		t.Fatalf("indexer settings: %+v", settings)
	}
	if settings.Usenet.Host != "news.example.com" || settings.Usenet.Username != sabUser || settings.Usenet.Password != sabPassword {
		t.Fatalf("usenet settings: %+v", settings.Usenet)
	}
	if len(settings.Usenet.FallbackHosts) != 1 || settings.Usenet.FallbackHosts[0] != "fallback.example.com" {
		t.Fatalf("compatible fallback hosts: %+v", settings.Usenet.FallbackHosts)
	}
	if item := itemFor(t, env, view.ID, itemUsenet, "nzbget"); item.Status != itemDone || !strings.Contains(item.Message, "fallback") {
		t.Fatalf("the fallback source must be reported: %+v", item)
	}

	movieCfg, err := env.movies.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if movieCfg.JellyfinURL == "" || !movieCfg.JellyfinConfigured || movieCfg.FolderTemplate != "{title} ({year})" {
		t.Fatalf("movie configuration: %+v", movieCfg)
	}

	sources, err := env.torrents.ListSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].Name != "Prowlarr: Public tracker" || !strings.HasSuffix(sources[0].URL, "/2/api") {
		t.Fatalf("torznab sources: %+v", sources)
	}
	torrentSettings := env.torrents.Settings()
	if torrentSettings.DownloadLimitKBps != 100 || torrentSettings.UploadLimitKBps != 0 || torrentSettings.MaxActiveJobs != 5 ||
		torrentSettings.SeedRatioLimit != 2 || !torrentSettings.DHTEnabled || torrentSettings.PEXEnabled {
		t.Fatalf("torrent settings: %+v", torrentSettings)
	}

	jobs, err := env.torrents.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("torrent jobs: %+v", jobs)
	}
	paused := 0
	for _, job := range jobs {
		if job.Status == "paused" {
			paused++
		}
	}
	if paused != 2 {
		t.Fatalf("imported torrents must start paused: %+v", jobs)
	}
	placed := filepath.Join(env.directory, "torrents", completeHash, "Example.Release", "file.mkv")
	if info, err := os.Stat(placed); err != nil || info.Size() != 64 {
		t.Fatalf("existing data must be placed for verification: %v %+v", err, info)
	}
	if item := itemFor(t, env, view.ID, itemTorrentJob, completeHash); item.Status != itemDone || !strings.Contains(item.Message, "existing file") {
		t.Fatalf("complete torrent reuse: %+v", item)
	}
	if item := itemFor(t, env, view.ID, itemTorrentJob, partialHash); item.Status != itemDone || !strings.Contains(item.Message, "download again") {
		t.Fatalf("explicit redownload: %+v", item)
	}

	subtitleCfg, err := env.subtitles.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if subtitleCfg.CutoffScore != 90 || subtitleCfg.SearchIntervalHours != 6 || subtitleCfg.Sync.MaxOffsetSeconds != 60 {
		t.Fatalf("subtitle configuration: %+v", subtitleCfg)
	}
	profiles, err := env.subtitles.Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var migrated *subtitles.Profile
	for i := range profiles {
		if strings.HasPrefix(profiles[i].ID, "bazarr-") {
			migrated = &profiles[i]
		}
	}
	if migrated == nil || len(migrated.Languages) != 2 || migrated.Cutoff != 1 {
		t.Fatalf("subtitle profiles: %+v", profiles)
	}
	if subtitleCfg.DefaultProfileID != migrated.ID {
		t.Fatalf("the Bazarr default profile must be applied: %+v", subtitleCfg.DefaultProfileID)
	}
	if len(subtitleCfg.Providers) == 0 || subtitleCfg.Providers[0].Username != subtitleUser || !subtitleCfg.Providers[0].PasswordSet {
		t.Fatalf("subtitle provider account: %+v", subtitleCfg.Providers)
	}
	if item := itemFor(t, env, view.ID, itemSubtitleProvider, "subssabbz"); item.Status != itemSkipped {
		t.Fatalf("unsupported providers must be skipped with a reason: %+v", item)
	}
	assertNoSecrets(t, raw)

	// Repeating the migration must not duplicate catalog items, profiles, or jobs.
	second := previewPlan(t, env.handler, env)
	result = applyPlan(t, env.handler, second.ID, applyInput(env, nil))
	if result.Status != statusApplied || result.Totals.Done+result.Totals.Skipped != result.Totals.Total {
		t.Fatalf("second apply: %+v", result)
	}
	repeatMovies, _ := env.movies.List(ctx)
	repeatAlbums, _ := env.music.Albums(ctx)
	repeatJobs, _ := env.torrents.List(ctx)
	if len(repeatMovies) != 2 || len(repeatAlbums) != 1 || len(repeatJobs) != 2 {
		t.Fatalf("repeat duplicated data: movies=%d albums=%d jobs=%d", len(repeatMovies), len(repeatAlbums), len(repeatJobs))
	}
	repeatProfiles, _ := env.subtitles.Profiles(ctx)
	if len(repeatProfiles) != len(profiles) {
		t.Fatalf("repeat duplicated subtitle profiles: %d", len(repeatProfiles))
	}
}

func TestMigrationSettingsOnlyImportSkipsTheCatalogs(t *testing.T) {
	env := newMigrationEnv(t, false)
	ctx := context.Background()
	view := previewPlan(t, env.handler, env)

	settingsOnly := applyInput(env, func(input *ApplyInput) {
		input.Movies, input.TV, input.Music = false, false, false
		input.Files, input.Naming, input.CreateProfiles = false, false, false
		input.Providers, input.Subtitles, input.Torrents, input.TorrentJobs = true, true, true, false
	})
	result := applyPlan(t, env.handler, view.ID, settingsOnly)
	if result.Status != statusApplied || result.Totals.Failed != 0 {
		t.Fatalf("settings-only import: %+v %+v", result.Totals, result.Failures)
	}
	if item := itemFor(t, env, view.ID, itemMovie, "radarr:1"); item.Status != itemSkipped || !strings.Contains(item.Message, "not requested") {
		t.Fatalf("catalog items must stay untouched: %+v", item)
	}
	if item := itemFor(t, env, view.ID, itemNaming, "movies"); item.Status != itemSkipped {
		t.Fatalf("naming must stay untouched: %+v", item)
	}
	if item := itemFor(t, env, view.ID, itemProfile, "radarr:3"); item.Status != itemSkipped {
		t.Fatalf("profiles must stay untouched: %+v", item)
	}
	if item := itemFor(t, env, view.ID, itemIndexer, "prowlarr:1"); item.Status != itemDone {
		t.Fatalf("provider settings must still apply: %+v", item)
	}
	if item := itemFor(t, env, view.ID, itemSubtitleConfig, "config"); item.Status != itemDone {
		t.Fatalf("subtitle settings must still apply: %+v", item)
	}
	if movies, err := env.movies.List(ctx); err != nil || len(movies) != 0 {
		t.Fatalf("settings-only import created movies: %v %+v", err, movies)
	}
	if series, err := env.tv.List(ctx); err != nil || len(series) != 0 {
		t.Fatalf("settings-only import created series: %v %+v", err, series)
	}
	if artists, err := env.music.Artists(ctx); err != nil || len(artists) != 0 {
		t.Fatalf("settings-only import created artists: %v %+v", err, artists)
	}
	movieCfg, err := env.movies.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if movieCfg.FolderTemplate != "{title} ({year}) [imdb-{imdbId}]" {
		t.Fatalf("naming changed without being requested: %+v", movieCfg.FolderTemplate)
	}
	if settings := env.manager.Config(); settings.APIKey != radarrKey {
		t.Fatalf("provider settings were not applied: %+v", settings)
	}
	if subtitleCfg, err := env.subtitles.Config(ctx); err != nil || subtitleCfg.CutoffScore != 90 {
		t.Fatalf("subtitle settings were not applied: %v %+v", err, subtitleCfg.CutoffScore)
	}
}

func TestMigrationProfileCollisionsKeepExistingProfiles(t *testing.T) {
	env := newMigrationEnv(t, false)
	ctx := context.Background()
	// A user profile that shares the source name but not its settings must survive untouched.
	if _, err := env.movies.Store.SaveProfile(ctx, quality.Profile{
		ID: "user-sd", Name: "HD-1080p", Qualities: []string{"SD"}, Cutoff: "SD", Upgrade: false,
	}); err != nil {
		t.Fatalf("seed user profile: %v", err)
	}
	// Same formats as the Lidarr profile but in reverse preference order: rank comes from the order,
	// so this profile must not be reused for the artist.
	seededMusic, err := env.music.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seededMusic.QualityProfiles = append(seededMusic.QualityProfiles, music.QualityProfile{
		ID: "reversed-order", Name: "Reversed order", Formats: []string{"mp3", "flac"},
		Cutoff: "flac", Upgrade: true, MinBitrateKbps: 256,
	})
	if _, err := env.music.SetConfig(ctx, seededMusic); err != nil {
		t.Fatalf("seed reversed music profile: %v", err)
	}
	view := previewPlan(t, env.handler, env)
	for _, profile := range view.Profiles {
		if profile.Suggested != "" {
			t.Fatalf("a differing name lookalike must not be suggested: %+v", profile)
		}
	}
	if view.Music == nil || len(view.Music.Profiles) != 1 || view.Music.Profiles[0].Suggested != "" {
		t.Fatalf("a differing music name lookalike must not be suggested: %+v", view.Music)
	}

	result := applyPlan(t, env.handler, view.ID, applyInput(env, nil))
	if result.Status != statusApplied || result.Totals.Failed != 0 {
		t.Fatalf("apply: %+v %+v", result.Totals, result.Failures)
	}
	profiles, err := env.movies.Store.Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var userProfile, created *quality.Profile
	for i := range profiles {
		switch profiles[i].ID {
		case "user-sd":
			userProfile = &profiles[i]
		case migratedProfileID("radarr:3", AppRadarr):
			created = &profiles[i]
		}
	}
	if userProfile == nil || !sameQualityOrder(userProfile.Qualities, []string{"SD"}) || userProfile.Name != "HD-1080p" {
		t.Fatalf("the existing profile was overwritten: %+v", userProfile)
	}
	if created == nil || created.Name != "HD-1080p (radarr:3)" || !sameQualityOrder(created.Qualities, []string{"Bluray-1080p", "WEB-1080p"}) || !created.Upgrade || created.Cutoff != "Bluray-1080p" {
		t.Fatalf("the migrated profile must keep the source settings under a free name: %+v", created)
	}
	catalog, err := env.movies.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, movie := range catalog {
		if movie.Metadata.IMDbID == "tt0468569" && movie.ProfileID != created.ID {
			t.Fatalf("the movie must use the migrated profile: %+v", movie.ProfileID)
		}
	}
	musicCfg, err := env.music.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var defaultLossless, migratedMusic *music.QualityProfile
	for i := range musicCfg.QualityProfiles {
		switch musicCfg.QualityProfiles[i].ID {
		case "lossless":
			defaultLossless = &musicCfg.QualityProfiles[i]
		case migratedProfileID("lidarr:2", AppLidarr):
			migratedMusic = &musicCfg.QualityProfiles[i]
		}
	}
	if defaultLossless == nil || !sameQualityOrder(defaultLossless.Formats, []string{"flac", "alac", "wav"}) {
		t.Fatalf("the existing music profile was overwritten: %+v", defaultLossless)
	}
	if migratedMusic == nil || migratedMusic.Name != "Lossless (lidarr:2)" || !sameQualityOrder(migratedMusic.Formats, []string{"flac", "mp3"}) ||
		migratedMusic.MinBitrateKbps != 256 || !migratedMusic.Upgrade || migratedMusic.Cutoff != "flac" {
		t.Fatalf("the migrated music profile must keep the source settings: %+v", migratedMusic)
	}
	artists, err := env.music.Artists(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(artists) != 1 || artists[0].ProfileID != migratedMusic.ID {
		t.Fatalf("the artist must use the migrated music profile: %+v", artists)
	}
	if artists[0].ProfileID == "reversed-order" {
		t.Fatal("a reversed format order must never be reused")
	}
	for _, profile := range musicCfg.QualityProfiles {
		if profile.ID == "reversed-order" && !sameQualityOrder(profile.Formats, []string{"mp3", "flac"}) {
			t.Fatalf("the reversed profile was rewritten: %+v", profile)
		}
	}

	// A user edit of the migrated profile survives a rerun of the same plan.
	edited := *created
	edited.Name = "HD-1080p tuned"
	edited.Cutoff = "WEB-1080p"
	if _, err := env.movies.Store.SaveProfile(ctx, edited); err != nil {
		t.Fatalf("edit migrated profile: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE migration_plan_items SET status = $2, message = '' WHERE plan_id = $1`, view.ID, itemPending); err != nil {
		t.Fatal(err)
	}
	result = applyPlan(t, env.handler, view.ID, applyInput(env, nil))
	if result.Status != statusApplied || result.Totals.Failed != 0 {
		t.Fatalf("rerun: %+v %+v", result.Totals, result.Failures)
	}
	rerunProfiles, err := env.movies.Store.Profiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	var rerunCreated *quality.Profile
	for i := range rerunProfiles {
		if rerunProfiles[i].ID == migratedProfileID("radarr:3", AppRadarr) {
			rerunCreated = &rerunProfiles[i]
		}
		if strings.HasPrefix(rerunProfiles[i].Name, "HD-1080p") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("the rerun duplicated profiles: %d", count)
	}
	if rerunCreated == nil || rerunCreated.Name != "HD-1080p tuned" || rerunCreated.Cutoff != "WEB-1080p" {
		t.Fatalf("the rerun overwrote user edits: %+v", rerunCreated)
	}
	rerunMusic, err := env.music.ConfigView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	musicCount := 0
	for _, profile := range rerunMusic.QualityProfiles {
		if strings.HasPrefix(profile.Name, "Lossless") {
			musicCount++
		}
	}
	if musicCount != 2 {
		t.Fatalf("the rerun duplicated music profiles: %d", musicCount)
	}
}

func TestMigrationRejectsBadMappingsAndExpiredPlans(t *testing.T) {
	env := newMigrationEnv(t, false)
	view := previewPlan(t, env.handler, env)

	input := applyInput(env, func(input *ApplyInput) {
		input.MoviesRoots = map[string]string{"/media/movies": "/does/not/exist"}
	})
	encoded, _ := json.Marshal(input)
	status, raw := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/plans/"+view.ID+"/apply", string(encoded))
	if status != http.StatusBadRequest || !strings.Contains(string(raw), "local folder") {
		t.Fatalf("bad mapping must be rejected: %d %s", status, raw)
	}
	if status, _ := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/plans/unknown/apply", `{}`); status != http.StatusNotFound {
		t.Fatalf("unknown plan: %d", status)
	}

	if _, err := env.pool.Exec(context.Background(), `UPDATE migration_plans SET expires_at = now() - interval '1 minute' WHERE id = $1`, view.ID); err != nil {
		t.Fatal(err)
	}
	if status, raw := migrationCall(t, env.handler, http.MethodGet, "/api/v1/migration/plans/"+view.ID, ""); status != http.StatusGone {
		t.Fatalf("expired plan read: %d %s", status, raw)
	}
	if status, raw := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/plans/"+view.ID+"/apply", `{}`); status != http.StatusGone {
		t.Fatalf("expired plan apply: %d %s", status, raw)
	}
}

func TestMigrationSerializesConcurrentApply(t *testing.T) {
	env := newMigrationEnv(t, false)
	view := previewPlan(t, env.handler, env)
	ctx := context.Background()

	if _, err := env.pool.Exec(ctx, `UPDATE migration_plans SET status = $2, updated_at = now() WHERE id = $1`, view.ID, statusApplying); err != nil {
		t.Fatal(err)
	}
	status, raw := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/plans/"+view.ID+"/apply", `{"movies":true}`)
	if status != http.StatusConflict {
		t.Fatalf("a fresh apply claim must conflict: %d %s", status, raw)
	}

	// A crashed apply is reclaimable after the stale window and keeps its per-item markers.
	if _, err := env.pool.Exec(ctx, `UPDATE migration_plans SET updated_at = now() - interval '5 minutes' WHERE id = $1`, view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE migration_plan_items SET status = $2 WHERE plan_id = $1`, view.ID, itemDone); err != nil {
		t.Fatal(err)
	}
	result := applyPlan(t, env.handler, view.ID, applyInput(env, nil))
	if result.Remaining != 0 || result.Status != statusApplied {
		t.Fatalf("reclaimed apply: %+v", result)
	}
	read, err := env.service.load(ctx, view.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Movies) != 2 || len(read.Series) != 1 || read.Music == nil || len(read.Music.Artists) != 1 {
		t.Fatalf("the stored plan changed: %+v", read.Counts)
	}
}

func TestMigrationResumesAfterRestartAndProviderSecrets(t *testing.T) {
	env := newMigrationEnv(t, false)
	view := previewPlan(t, env.handler, env)

	// A new service instance holds no credentials; catalog items still apply from the stored plan.
	_, restarted := newTestService(t, env, Options{})
	encoded, _ := json.Marshal(applyInput(env, func(input *ApplyInput) { input.Connections = nil }))
	var result ApplyResult
	failed := map[string]string{}
	for round := 0; round < 8; round++ {
		status, raw := migrationCall(t, restarted, http.MethodPost, "/api/v1/migration/plans/"+view.ID+"/apply", string(encoded))
		if status != http.StatusOK {
			t.Fatalf("apply without credentials (round %d): %d %s", round, status, raw)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		for _, failure := range result.Failures {
			failed[failure.Kind] = failure.Message
		}
		if result.Remaining == 0 {
			break
		}
	}
	if result.Status != statusPartial {
		t.Fatalf("credential-backed items must fail without credentials: %+v", result)
	}
	for kind, message := range failed {
		if !strings.Contains(message, "credentials are no longer held") {
			t.Fatalf("unclear provider failure for %s: %s", kind, message)
		}
	}
	if len(failed) == 0 || failed[itemUsenet] == "" || failed[itemIndexer] == "" || failed[itemTorznab] == "" || failed[itemSubtitleProvider] == "" {
		t.Fatalf("expected provider items to fail: %+v", failed)
	}
	if item := itemFor(t, env, view.ID, itemNaming, "movies"); item.Status != itemDone || !strings.Contains(item.Message, "naming updated") {
		t.Fatalf("naming must apply without provider credentials: %+v", item)
	}
	if result.Totals.Done == 0 {
		t.Fatalf("catalog items must still apply after a restart: %+v", result.Totals)
	}
	if catalog, err := env.movies.List(context.Background()); err != nil || len(catalog) != 2 {
		t.Fatalf("catalog after the credential-free apply: %v %+v", err, catalog)
	}
	if settings := env.manager.Config(); settings.APIKey == radarrKey {
		t.Fatal("provider settings were applied without credentials")
	}

	// Resending connections lets the restarted service re-read and apply the provider settings.
	retry := applyInput(env, func(input *ApplyInput) { input.RetryFailed = true })
	encoded, _ = json.Marshal(retry)
	for round := 0; round < 8; round++ {
		status, raw := migrationCall(t, restarted, http.MethodPost, "/api/v1/migration/plans/"+view.ID+"/apply", string(encoded))
		if status != http.StatusOK {
			t.Fatalf("retry apply (round %d): %d %s", round, status, raw)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if result.Remaining == 0 {
			break
		}
	}
	if result.Status != statusApplied || result.Totals.Failed != 0 || result.Remaining != 0 {
		t.Fatalf("retry result: %+v", result)
	}
	settings := env.manager.Config()
	if settings.APIKey != radarrKey || settings.Usenet.Password != sabPassword {
		t.Fatalf("rediscovered settings were not applied: %+v", settings)
	}
	if cfg, err := env.subtitles.Config(context.Background()); err != nil || len(cfg.Providers) == 0 || cfg.Providers[0].Username != subtitleUser {
		t.Fatalf("rediscovered subtitle provider: %v %+v", err, cfg.Providers)
	}
}

func TestMigrationTorrentReuseNeedsVerifiedDataOrExplicitRedownload(t *testing.T) {
	env := newMigrationEnv(t, false)
	view := previewPlan(t, env.handler, env)

	// Without a download mapping the complete torrent cannot reuse its data and must not be
	// dropped silently: the item asks for an explicit redownload instead.
	input := applyInput(env, func(input *ApplyInput) {
		input.TorrentRoots = map[string]string{}
		input.Redownload = nil
	})
	result := applyPlan(t, env.handler, view.ID, input)
	if result.Status != statusApplied {
		t.Fatalf("apply with skipped torrents: %+v", result)
	}
	if item := itemFor(t, env, view.ID, itemTorrentJob, completeHash); item.Status != itemSkipped || !strings.Contains(item.Message, "redownload") {
		t.Fatalf("reuse without a mapping must require a choice: %+v", item)
	}
	jobs, err := env.torrents.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if strings.EqualFold(job.InfoHash, completeHash) {
			t.Fatalf("a torrent without a reuse decision must not be started: %+v", job)
		}
	}
}

func TestMigrationConnectionTestReportsSanitizedFailures(t *testing.T) {
	env := newMigrationEnv(t, true)
	status, raw := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/connections/test", connectionsJSON(t, env.connections))
	if status != http.StatusOK {
		t.Fatalf("connections test: %d %s", status, raw)
	}
	var tests struct {
		Results []connectionTest `json:"results"`
	}
	if err := json.Unmarshal(raw, &tests); err != nil {
		t.Fatal(err)
	}
	var radarr connectionTest
	for _, result := range tests.Results {
		if result.App == AppRadarr {
			radarr = result
		}
	}
	if radarr.OK || !strings.Contains(radarr.Error, "authorization failed") {
		t.Fatalf("a rejected key must be reported: %+v", radarr)
	}
	assertNoSecrets(t, raw)

	status, raw = migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/preview", connectionsJSON(t, env.connections))
	if status != http.StatusBadGateway || !strings.Contains(string(raw), "authorization failed") {
		t.Fatalf("preview with a rejected key: %d %s", status, raw)
	}
	assertNoSecrets(t, raw)
}

func TestMigrationRequestDecodingIsStrict(t *testing.T) {
	env := newMigrationEnv(t, false)
	if status, _ := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/preview", `{"connections":[{"app":"radarr","url":"http://host:7878","apiKey":"k","unknown":1}]}`); status != http.StatusBadRequest {
		t.Fatalf("unknown fields must be rejected: %d", status)
	}
	if status, _ := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/connections/test", `{"connections":[]}`); status != http.StatusBadRequest {
		t.Fatalf("an empty connection list must be rejected: %d", status)
	}
	trailing := fmt.Sprintf(`{"connections":[{"app":"radarr","url":"http://host:7878","apiKey":"%s"}]}{"second":true}`, radarrKey)
	if status, _ := migrationCall(t, env.handler, http.MethodPost, "/api/v1/migration/preview", trailing); status != http.StatusBadRequest {
		t.Fatalf("trailing documents must be rejected: %d", status)
	}
}

func TestMigrationAppliesInBoundedBatches(t *testing.T) {
	env := newMigrationEnv(t, false)
	_, handler := newTestService(t, env, Options{ApplyBatch: 3})
	view := previewPlan(t, handler, env)

	encoded, _ := json.Marshal(applyInput(env, nil))
	status, raw := migrationCall(t, handler, http.MethodPost, "/api/v1/migration/plans/"+view.ID+"/apply", string(encoded))
	if status != http.StatusOK {
		t.Fatalf("first batch: %d %s", status, raw)
	}
	var result ApplyResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Remaining == 0 || len(result.Applied) > 3 {
		t.Fatalf("a batch must stay bounded: %+v", result)
	}
	final := applyPlan(t, handler, view.ID, applyInput(env, nil))
	if final.Status != statusApplied || final.Remaining != 0 {
		t.Fatalf("batched apply: %+v", final)
	}
}

func assertNoSecrets(t *testing.T, raw []byte) {
	t.Helper()
	body := string(raw)
	for _, secret := range []string{
		radarrKey, sonarrKey, lidarrKey, prowlarrKey, bazarrKey, sabKey, nzbgetKey, jellyfinKey,
		sabUser, sabPassword, subtitleUser, subtitlePass, transmissionKey,
		`"apiKey"`, `"password"`, `"api_key"`,
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaks %q: %s", secret, body)
		}
	}
}
