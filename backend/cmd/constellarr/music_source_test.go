package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

const (
	fixtureArtistMBID   = "11111111-1111-4111-8111-111111111111"
	fixtureAlbumMBID    = "22222222-2222-4222-8222-222222222222"
	fixtureOtherMBID    = "33333333-3333-4333-8333-333333333333"
	fixtureReleaseID    = "44444444-4444-4444-8444-444444444444"
	fixtureAnonMBID     = "55555555-5555-4555-8555-555555555555"
	fixtureUnknownMBID  = "66666666-6666-4666-8666-666666666666"
	fixtureOtherRelease = "77777777-7777-4777-8777-777777777777"
)

func TestMusicRequestReflectsDownloadControls(t *testing.T) {
	for _, status := range []string{"paused", "cancelled"} {
		album := music.Album{ID: "album", Status: status}
		item := itemFromAlbum(album)
		if item.Status != status || item.Available {
			t.Fatalf("missing album with %s download: %+v", status, item)
		}
		album.Files = []music.File{{Path: "track.flac"}}
		if item = itemFromAlbum(album); !item.Available {
			t.Fatalf("%s upgrade hid an available album", status)
		}
		album.Files[0].Missing = true
		if item = itemFromAlbum(album); item.Available {
			t.Fatalf("%s upgrade reported a missing file as available", status)
		}
	}
}

// brainzFixture serves the MusicBrainz JSON contract the music module expects.
type brainzFixture struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
}

func newBrainzFixture(t *testing.T) *brainzFixture {
	t.Helper()
	fixture := &brainzFixture{}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.requests = append(fixture.requests, r.URL.Path+"?"+r.URL.RawQuery)
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/release-group/"):
			mbid := strings.TrimPrefix(r.URL.Path, "/release-group/")
			switch mbid {
			case fixtureAlbumMBID:
				fmt.Fprintf(w, `{"id":%q,"title":"Synthetic Album","primary-type":"Album","first-release-date":"2024-05-01",
					"artist-credit":[{"name":"Synthetic Artist","artist":{"id":%q,"name":"Synthetic Artist"}}],
					"releases":[{"id":%q,"status":"Official"}]}`, mbid, fixtureArtistMBID, fixtureReleaseID)
			case fixtureOtherMBID:
				fmt.Fprintf(w, `{"id":%q,"title":"Synthetic Other Album","primary-type":"Album","first-release-date":"2021-01-01",
					"artist-credit":[{"name":"Synthetic Artist","artist":{"id":%q}}],
					"releases":[{"id":%q,"status":"Official"}]}`, mbid, fixtureArtistMBID, fixtureOtherRelease)
			case fixtureAnonMBID:
				fmt.Fprintf(w, `{"id":%q,"title":"Synthetic Anonymous","primary-type":"Album","first-release-date":"2020-01-01","artist-credit":[]}`, mbid)
			default:
				http.NotFound(w, r)
			}
		case r.URL.Path == "/release-group":
			fmt.Fprintf(w, `{"release-groups":[
				{"id":%q,"title":"Synthetic Album","primary-type":"Album","first-release-date":"2024-05-01","score":100,"artist-credit":[{"name":"Synthetic Artist","artist":{"id":%q}}]},
				{"id":%q,"title":"Synthetic Other Album","primary-type":"Album","first-release-date":"2021-01-01","score":95,"artist-credit":[{"name":"Synthetic Artist","artist":{"id":%q}}]}]}`,
				fixtureAlbumMBID, fixtureArtistMBID, fixtureOtherMBID, fixtureArtistMBID)
		case r.URL.Path == "/artist":
			fmt.Fprintf(w, `{"artists":[{"id":%q,"name":"Synthetic Artist","sort-name":"Artist, Synthetic","country":"US","type":"Group","score":100}]}`, fixtureArtistMBID)
		case strings.HasPrefix(r.URL.Path, "/artist/"):
			fmt.Fprintf(w, `{"id":%q,"name":"Synthetic Artist","sort-name":"Artist, Synthetic","country":"US","type":"Group"}`, fixtureArtistMBID)
		case strings.HasPrefix(r.URL.Path, "/release/"):
			fmt.Fprintf(w, `{"id":%q,"title":"Synthetic Album","date":"2024-05-01","media":[{"position":1,"track-count":2,"tracks":[
				{"id":"88888888-8888-4888-8888-888888888881","number":"1","position":1,"title":"First Song","length":180000},
				{"id":"88888888-8888-4888-8888-888888888882","number":"2","position":2,"title":"Second Song","length":200000}]}]}`,
				fixtureReleaseID)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fixture.Server.Close)
	return fixture
}

func (f *brainzFixture) matched(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	found := []string{}
	for _, request := range f.requests {
		if strings.HasPrefix(request, prefix) {
			found = append(found, request)
		}
	}
	return found
}

func newMusicService(t *testing.T, brainzURL string) *music.Service {
	t.Helper()
	core := newTestCore(t)
	ctx := context.Background()
	service, err := music.New(ctx, core.pool, core.manager)
	if err != nil {
		t.Fatalf("music.New: %v", err)
	}
	cfg, err := service.ConfigView(ctx)
	if err != nil {
		t.Fatalf("music config: %v", err)
	}
	cfg.MusicBrainzURL = brainzURL + "/"
	cfg.MusicBrainzRateMs = 0
	cfg.CoverArtURL = brainzURL + "/"
	if _, err := service.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("music SetConfig: %v", err)
	}
	return service
}

func TestMusicSourceAddsOnlyTheSelectedAlbum(t *testing.T) {
	fixture := newBrainzFixture(t)
	service := newMusicService(t, fixture.URL)
	ctx := context.Background()
	source := musicSource{service: service}

	found, err := source.Search(ctx, "Synthetic", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(found) != 2 || found[0].ID != fixtureAlbumMBID || found[0].Artist != "Synthetic Artist" ||
		found[0].Year != 2024 || found[0].Available {
		t.Fatalf("search items = %+v", found)
	}

	item, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureAlbumMBID, Title: "Synthetic Album", Year: 2024})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if item.ID == "" || item.ID == fixtureAlbumMBID || !strings.EqualFold(item.Title, "Synthetic Album") || item.Available {
		t.Fatalf("added item = %+v", item)
	}

	albums, err := service.Albums(ctx)
	if err != nil {
		t.Fatalf("Albums: %v", err)
	}
	if len(albums) != 1 || albums[0].ID != item.ID || albums[0].MusicBrainzID != fixtureAlbumMBID || !albums[0].Monitored {
		t.Fatalf("library albums = %+v", albums)
	}
	stored, err := service.Album(ctx, item.ID)
	if err != nil || len(stored.Tracks) != 2 || stored.Tracks[1].Title != "Second Song" {
		t.Fatalf("stored album tracks = %+v (%v)", stored.Tracks, err)
	}
	artists, err := service.Artists(ctx)
	if err != nil {
		t.Fatalf("Artists: %v", err)
	}
	if len(artists) != 1 || artists[0].MusicBrainzID != fixtureArtistMBID || artists[0].Monitored || artists[0].MonitorOption != "none" {
		t.Fatalf("library artists = %+v", artists)
	}
	if len(artists[0].Albums) != 1 || artists[0].Albums[0].ID != item.ID {
		t.Fatalf("artist catalog = %+v", artists[0].Albums)
	}
	if extra := fixture.matched("/release-group?artist="); len(extra) != 0 {
		t.Fatalf("the artist catalog was requested: %v", extra)
	}

	// Retrying the same request after a restart returns the stored album without provider work.
	lookups := len(fixture.matched("/release-group/" + fixtureAlbumMBID))
	again, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureAlbumMBID, Title: "Synthetic Album", Year: 2024})
	if err != nil || again.ID != item.ID {
		t.Fatalf("repeated Add = %+v (%v)", again, err)
	}
	if albums, err = service.Albums(ctx); err != nil || len(albums) != 1 {
		t.Fatalf("albums after repeat = %+v (%v)", albums, err)
	}
	if now := len(fixture.matched("/release-group/" + fixtureAlbumMBID)); now != lookups {
		t.Fatalf("repeated Add used the provider again: %d lookups", now)
	}
}

func TestMusicSourceStatusCalendarAndAvailability(t *testing.T) {
	fixture := newBrainzFixture(t)
	service := newMusicService(t, fixture.URL)
	ctx := context.Background()
	source := musicSource{service: service}
	item, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureAlbumMBID, Title: "Synthetic Album", Year: 2024})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Both the release-group ID and the library ID resolve to the same album.
	byBrainz, err := source.Status(ctx, fixtureAlbumMBID)
	if err != nil || byBrainz.ID != item.ID || byBrainz.Status != "wanted" || byBrainz.Available {
		t.Fatalf("status by release group = %+v (%v)", byBrainz, err)
	}
	if byLibrary, err := source.Status(ctx, item.ID); err != nil || byLibrary.ID != item.ID {
		t.Fatalf("status by library ID = %+v (%v)", byLibrary, err)
	}
	if missing, err := source.Status(ctx, fixtureUnknownMBID); err != nil || missing.Status != "wanted" || missing.Available {
		t.Fatalf("status of a removed album = %+v (%v)", missing, err)
	}

	releaseDay := time.Now().UTC().AddDate(0, 0, 10).Format("2006-01-02")
	album, err := service.Store.Album(ctx, item.ID)
	if err != nil {
		t.Fatalf("load album: %v", err)
	}
	album.ReleaseDate = releaseDay
	if _, err := service.Store.SaveAlbum(ctx, album); err != nil {
		t.Fatalf("save album: %v", err)
	}
	calendar, err := source.Calendar(ctx)
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	if len(calendar) != 1 || calendar[0].ID != item.ID || calendar[0].Title != "Synthetic Album" ||
		calendar[0].Artist != "Synthetic Artist" || calendar[0].ReleaseDate != releaseDay ||
		calendar[0].Year != time.Now().UTC().Year() {
		t.Fatalf("calendar = %+v", calendar)
	}

	// A real file under the music root flips the album and the request view to available.
	writeAlbumFile(t, ctx, service, item.ID)
	available, err := source.Status(ctx, fixtureAlbumMBID)
	if err != nil || !available.Available || available.Status != "available" {
		t.Fatalf("available status = %+v (%v)", available, err)
	}
}

func TestMusicSourceRefusesUnresolvedRecords(t *testing.T) {
	fixture := newBrainzFixture(t)
	service := newMusicService(t, fixture.URL)
	ctx := context.Background()
	source := musicSource{service: service}

	if _, err := source.Add(ctx, discovery.MusicAddInput{ID: "spotify:album:1", Title: "Synthetic"}); !errors.Is(err, discovery.ErrInvalid) {
		t.Fatalf("non-MusicBrainz ID error = %v; want invalid", err)
	}
	if _, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureUnknownMBID, Title: "Synthetic"}); !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("unknown release group error = %v; want not found", err)
	}
	if _, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureAnonMBID, Title: "Synthetic Anonymous"}); !errors.Is(err, discovery.ErrNotFound) {
		t.Fatalf("artist-less release group error = %v; want not found", err)
	}
	if albums, err := service.Albums(ctx); err != nil || len(albums) != 0 {
		t.Fatalf("albums after refused adds = %+v (%v)", albums, err)
	}

	// A provided title has to match the provider record, and a stored album wins on repeats.
	if _, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureOtherMBID, Title: "A Different Album", Year: 2021}); !errors.Is(err, discovery.ErrConflict) {
		t.Fatalf("title mismatch error = %v; want conflict", err)
	}
	if albums, err := service.Albums(ctx); err != nil || len(albums) != 0 {
		t.Fatalf("albums after a title mismatch = %+v (%v)", albums, err)
	}
	added, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureOtherMBID, Title: "Synthetic Other Album", Year: 2021})
	if err != nil {
		t.Fatalf("Add second album: %v", err)
	}
	if again, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureOtherMBID, Title: "A Different Album", Year: 2021}); err != nil || again.ID != added.ID {
		t.Fatalf("repeat with a different title = %+v (%v)", again, err)
	}

	unconfigured := musicSource{}
	for name, call := range map[string]func() error{
		"Search": func() error { _, err := unconfigured.Search(ctx, "Synthetic", 5); return err },
		"Add": func() error {
			_, err := unconfigured.Add(ctx, discovery.MusicAddInput{ID: fixtureAlbumMBID})
			return err
		},
		"Calendar": func() error { _, err := unconfigured.Calendar(ctx); return err },
		"Status":   func() error { _, err := unconfigured.Status(ctx, fixtureAlbumMBID); return err },
	} {
		if err := call(); !errors.Is(err, discovery.ErrNotConfigured) {
			t.Errorf("%s without a music module = %v; want not configured", name, err)
		}
	}
}

// syntheticMusicRelease is the single audio release the indexer fixture serves.
const syntheticMusicRelease = "Synthetic Artist - Synthetic Album (2024) [FLAC]"

// newMusicIndexer serves the Newznab audio search and one NZB so a monitor pass can grab a release.
func newMusicIndexer(t *testing.T, releaseID string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("t") {
		case "music", "search":
			w.Header().Set("Content-Type", "application/rss+xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"><channel><item>
<title>%s</title>
<guid isPermaLink="false">%s</guid>
<pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate>
<enclosure url="http://indexer.example/%s.nzb" length="1073741824" type="application/x-nzb"/>
<newznab:attr name="category" value="3000"/>
<newznab:attr name="size" value="1073741824"/>
<newznab:attr name="artist" value="Synthetic Artist"/>
<newznab:attr name="album" value="Synthetic Album"/>
<newznab:attr name="year" value="2024"/>
<category>Audio</category>
</item></channel></rss>`, syntheticMusicRelease, releaseID, releaseID)
		case "get":
			w.Header().Set("Content-Type", "application/x-nzb")
			fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// useMusicIndexer points the download manager at the fixture so music searches can run.
// The update also carries a placeholder NNTP host, which settings validation requires.
func useMusicIndexer(t *testing.T, manager *downloads.Manager, indexerURL string) {
	t.Helper()
	if err := manager.UpdateSettings(context.Background(), downloads.SettingsUpdate{
		IndexerURL: indexerURL + "/api", APIKey: "synthetic-indexer-key",
		UsenetHost: "news.example", UsenetPort: 563, Connections: 1,
		UsenetUsername: "synthetic-user", UsenetPassword: "synthetic-password",
	}); err != nil {
		t.Fatalf("configure the indexer: %v", err)
	}
}

type requestEnvironment struct {
	music    *music.Service
	source   musicSource
	requests *discovery.Service
	mux      *http.ServeMux
	notified []discovery.Request
}

// newRequestEnvironment wires discovery onto the real music, movie, and TV services.
func newRequestEnvironment(t *testing.T, fixture *brainzFixture, indexerURL string) *requestEnvironment {
	t.Helper()
	core := newTestCore(t)
	ctx := context.Background()
	if indexerURL != "" {
		useMusicIndexer(t, core.manager, indexerURL)
	}
	movieService, err := movies.New(ctx, core.pool, core.manager)
	if err != nil {
		t.Fatalf("movies.New: %v", err)
	}
	t.Cleanup(movieService.Close)
	tvService, err := tv.New(ctx, core.pool, core.manager, movieService.Store)
	if err != nil {
		t.Fatalf("tv.New: %v", err)
	}
	t.Cleanup(tvService.Close)
	musicService := newMusicServiceOn(t, core, fixture.URL)
	env := &requestEnvironment{music: musicService, source: musicSource{service: musicService}}
	requests, err := discovery.New(ctx, core.pool, movieService, tvService, discovery.Options{
		Music: env.source, Actor: bridgeActor, Can: bridgeCan,
		Notify: func(_ context.Context, request discovery.Request) { env.notified = append(env.notified, request) },
	})
	if err != nil {
		t.Fatalf("discovery.New: %v", err)
	}
	t.Cleanup(requests.Close)
	env.requests = requests
	env.mux = http.NewServeMux()
	requests.Register(env.mux)
	return env
}

// TestMusicRequestApprovalAddsOnlyTheSelectedAlbum covers the whole request-to-library path.
func TestMusicRequestApprovalAddsOnlyTheSelectedAlbum(t *testing.T) {
	fixture := newBrainzFixture(t)
	indexer := newMusicIndexer(t, "synthetic-album-flac")
	env := newRequestEnvironment(t, fixture, indexer.URL)
	ctx := context.Background()
	musicService, mux := env.music, env.mux

	body := fmt.Sprintf(`{"mediaType":"music","providerId":%q,"title":"Synthetic Album","year":2024}`, fixtureAlbumMBID)
	status, raw := bridgeCall(t, mux, http.MethodPost, "/api/v1/requests", body, "requester", false, nil)
	if status != http.StatusCreated {
		t.Fatalf("POST music request: status %d, body %s", status, raw)
	}
	created := decodeBridge[discovery.Request](t, raw)
	status, raw = bridgeCall(t, mux, http.MethodPost, "/api/v1/requests/"+created.ID+"/approve", `{}`, "approver", true, nil)
	if status != http.StatusOK {
		t.Fatalf("approve: status %d, body %s", status, raw)
	}
	approved := decodeBridge[discovery.Request](t, raw)
	if approved.Status != discovery.StatusApproved || approved.LibraryID == "" {
		t.Fatalf("approved request = %+v", approved)
	}
	albums, err := musicService.Albums(ctx)
	if err != nil || len(albums) != 1 || albums[0].ID != approved.LibraryID || !albums[0].Monitored {
		t.Fatalf("albums after approval = %+v (%v)", albums, err)
	}
	artists, err := musicService.Artists(ctx)
	if err != nil || len(artists) != 1 || artists[0].Monitored || artists[0].MonitorOption != "none" {
		t.Fatalf("artists after approval = %+v (%v)", artists, err)
	}
	if len(env.notified) == 0 || env.notified[0].UserID != "requester" {
		t.Fatalf("notifications = %+v", env.notified)
	}

	// The calendar and the request list now report music as configured.
	status, raw = bridgeCall(t, mux, http.MethodGet, "/api/v1/requests", "", "requester", false, nil)
	if status != http.StatusOK {
		t.Fatalf("GET requests: status %d, body %s", status, raw)
	}
	list := decodeBridge[discovery.List](t, raw)
	if strings.Join(list.Types, ",") != "movie,tv,music" {
		t.Fatalf("request types = %v", list.Types)
	}
	releaseDay := time.Now().UTC().AddDate(0, 0, 10).Format("2006-01-02")
	album, err := musicService.Store.Album(ctx, approved.LibraryID)
	if err != nil {
		t.Fatalf("load album: %v", err)
	}
	album.ReleaseDate = releaseDay
	if _, err := musicService.Store.SaveAlbum(ctx, album); err != nil {
		t.Fatalf("save album: %v", err)
	}
	status, raw = bridgeCall(t, mux, http.MethodGet, "/api/v1/calendar", "", "requester", false, nil)
	if status != http.StatusOK {
		t.Fatalf("GET calendar: status %d, body %s", status, raw)
	}
	view := decodeBridge[discovery.CalendarView](t, raw)
	musicEntries := 0
	for _, entry := range view.Entries {
		if entry.MediaType == discovery.MediaMusic && entry.LibraryID == approved.LibraryID && entry.Date == releaseDay {
			musicEntries++
		}
	}
	if musicEntries != 1 {
		t.Fatalf("calendar entries = %+v", view.Entries)
	}

	// The approval queues a monitor pass; the grab it performs is the observable proof it ran.
	acquisition := waitForAcquisition(t, ctx, musicService, approved.LibraryID)
	if acquisition.ReleaseID != "synthetic-album-flac" || !strings.Contains(acquisition.Title, "Synthetic Album") {
		t.Fatalf("acquisition = %+v", acquisition)
	}
	acquisitions, err := musicService.Store.Acquisitions(ctx)
	if err != nil || len(acquisitions) != 1 {
		t.Fatalf("acquisitions after approval = %+v (%v); want only the selected album", acquisitions, err)
	}
	if extra := fixture.matched("/release-group?artist="); len(extra) != 0 {
		t.Fatalf("the artist catalog was requested: %v", extra)
	}

	// Re-approving the same request stays idempotent and does not grab a second release.
	if status, raw := bridgeCall(t, mux, http.MethodPost, "/api/v1/requests/"+created.ID+"/approve", `{}`, "approver", true, nil); status != http.StatusOK {
		t.Fatalf("repeated approve: status %d, body %s", status, raw)
	} else if again := decodeBridge[discovery.Request](t, raw); again.LibraryID != approved.LibraryID {
		t.Fatalf("repeated approve changed the album: %+v", again)
	}
	if albums, err := musicService.Albums(ctx); err != nil || len(albums) != 1 {
		t.Fatalf("albums after repeated approve = %+v (%v)", albums, err)
	}
	time.Sleep(200 * time.Millisecond)
	if acquisitions, err = musicService.Store.Acquisitions(ctx); err != nil || len(acquisitions) != 1 {
		t.Fatalf("acquisitions after repeated approve = %+v (%v)", acquisitions, err)
	}

	// A finished download plus files on disk still move the request to available.
	finishAcquisition(t, ctx, musicService, acquisition)
	writeAlbumFile(t, ctx, musicService, approved.LibraryID)
	if album, err := musicService.Store.Album(ctx, approved.LibraryID); err != nil || len(album.Files) != 1 {
		t.Fatalf("album files after import = %+v (%v)", album.Files, err)
	}
	if _, err := env.requests.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	status, raw = bridgeCall(t, mux, http.MethodGet, "/api/v1/requests/"+created.ID, "", "requester", false, nil)
	if status != http.StatusOK {
		t.Fatalf("request detail: status %d, body %s", status, raw)
	}
	detail := decodeBridge[discovery.Detail](t, raw)
	if detail.Request.Status != discovery.StatusAvailable || !detail.Request.Delivery.Available {
		t.Fatalf("request after availability = %+v", detail.Request)
	}
}

// writeAlbumFile puts one real track under the music root and records it on the album.
func writeAlbumFile(t *testing.T, ctx context.Context, service *music.Service, albumID string) {
	t.Helper()
	cfg, err := service.ConfigView(ctx)
	if err != nil || len(cfg.RootFolders) == 0 {
		t.Fatalf("music config = %+v (%v)", cfg, err)
	}
	root := cfg.RootFolders[0]
	rel := filepath.Join("Synthetic Artist", "Synthetic Album", "01 - First Song.flac")
	if err := os.MkdirAll(filepath.Join(root.Path, filepath.Dir(rel)), 0o755); err != nil {
		t.Fatalf("create album folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root.Path, rel), []byte("flac"), 0o600); err != nil {
		t.Fatalf("write track: %v", err)
	}
	album, err := service.Store.Album(ctx, albumID)
	if err != nil {
		t.Fatalf("reload album: %v", err)
	}
	album.Files = []music.File{{RootID: root.ID, Path: rel, Size: 4, Format: "flac", Score: 100, Disc: 1, Number: 1, TrackTitle: "First Song", ImportedAt: time.Now().UTC()}}
	if _, err := service.Store.SaveAlbum(ctx, album); err != nil {
		t.Fatalf("save album files: %v", err)
	}
}

func itemByID(items []discovery.MusicItem, id string) discovery.MusicItem {
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	return discovery.MusicItem{}
}

// waitForAcquisition waits for the queued monitor pass to grab a release for one album.
func waitForAcquisition(t *testing.T, ctx context.Context, service *music.Service, albumID string) music.Acquisition {
	t.Helper()
	var found music.Acquisition
	waitFor(t, 20*time.Second, func() bool {
		acquisitions, err := service.Store.Acquisitions(ctx)
		if err != nil {
			return false
		}
		for _, acquisition := range acquisitions {
			if acquisition.AlbumID == albumID {
				found = acquisition
				return true
			}
		}
		return false
	}, "the queued music monitor pass never grabbed a release")
	return found
}

// finishAcquisition marks a grab as imported so later availability checks see a settled album.
func finishAcquisition(t *testing.T, ctx context.Context, service *music.Service, acquisition music.Acquisition) {
	t.Helper()
	acquisition.Status = "imported"
	if err := service.Store.SaveAcquisition(ctx, acquisition); err != nil {
		t.Fatalf("mark acquisition imported: %v", err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, ready func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal(message)
}

// TestMusicSourceSearchDistinguishesCataloguedFromAvailable keeps catalogued albums requestable.
func TestMusicSourceSearchDistinguishesCataloguedFromAvailable(t *testing.T) {
	fixture := newBrainzFixture(t)
	service := newMusicService(t, fixture.URL)
	ctx := context.Background()
	source := musicSource{service: service}
	added, err := source.Add(ctx, discovery.MusicAddInput{ID: fixtureAlbumMBID, Title: "Synthetic Album", Year: 2024})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	found, err := source.Search(ctx, "Synthetic", 10)
	if err != nil || len(found) != 2 {
		t.Fatalf("Search = %+v (%v)", found, err)
	}
	if catalogued := itemByID(found, fixtureAlbumMBID); catalogued.Available || catalogued.Status != "wanted" {
		t.Fatalf("catalogued album without files = %+v", catalogued)
	}
	if uncatalogued := itemByID(found, fixtureOtherMBID); uncatalogued.Available || uncatalogued.Status != "wanted" {
		t.Fatalf("uncatalogued album = %+v", uncatalogued)
	}

	writeAlbumFile(t, ctx, service, added.ID)
	found, err = source.Search(ctx, "Synthetic", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if available := itemByID(found, fixtureAlbumMBID); !available.Available || available.Status != "available" {
		t.Fatalf("available album = %+v", available)
	}
	if albums, err := service.Albums(ctx); err != nil || len(albums) != 1 {
		t.Fatalf("searching changed the catalog = %+v (%v)", albums, err)
	}
}

// TestMusicRequestForCatalogedWantedAlbumStaysPending covers a requested album that is already catalogued.
func TestMusicRequestForCatalogedWantedAlbumStaysPending(t *testing.T) {
	fixture := newBrainzFixture(t)
	indexer := newMusicIndexer(t, "synthetic-album-flac")
	env := newRequestEnvironment(t, fixture, indexer.URL)
	ctx := context.Background()
	added, err := env.source.Add(ctx, discovery.MusicAddInput{ID: fixtureAlbumMBID, Title: "Synthetic Album", Year: 2024})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	body := fmt.Sprintf(`{"mediaType":"music","providerId":%q,"title":"Synthetic Album","year":2024}`, fixtureAlbumMBID)
	status, raw := bridgeCall(t, env.mux, http.MethodPost, "/api/v1/requests", body, "requester", false, nil)
	if status != http.StatusCreated {
		t.Fatalf("POST music request: status %d, body %s", status, raw)
	}
	created := decodeBridge[discovery.Request](t, raw)
	if created.Status != discovery.StatusPending || created.LibraryID != "" {
		t.Fatalf("a catalogued album without files was treated as available: %+v", created)
	}

	status, raw = bridgeCall(t, env.mux, http.MethodPost, "/api/v1/requests/"+created.ID+"/approve", `{}`, "approver", true, nil)
	if status != http.StatusOK {
		t.Fatalf("approve: status %d, body %s", status, raw)
	}
	approved := decodeBridge[discovery.Request](t, raw)
	if approved.Status != discovery.StatusApproved || approved.LibraryID != added.ID {
		t.Fatalf("approval did not reuse the catalogued album: %+v", approved)
	}
	if albums, err := env.music.Albums(ctx); err != nil || len(albums) != 1 || !albums[0].Monitored {
		t.Fatalf("albums after approval = %+v (%v)", albums, err)
	}

	// The queued monitor pass proves itself by grabbing the catalogued album's release.
	acquisition := waitForAcquisition(t, ctx, env.music, added.ID)
	if acquisition.ReleaseID != "synthetic-album-flac" {
		t.Fatalf("acquisition = %+v", acquisition)
	}
	if acquisitions, err := env.music.Store.Acquisitions(ctx); err != nil || len(acquisitions) != 1 {
		t.Fatalf("acquisitions = %+v (%v); want only the selected album", acquisitions, err)
	}

	// Files on disk still move the request to available once the grab settles.
	finishAcquisition(t, ctx, env.music, acquisition)
	writeAlbumFile(t, ctx, env.music, added.ID)
	if _, err := env.requests.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	status, raw = bridgeCall(t, env.mux, http.MethodGet, "/api/v1/requests/"+created.ID, "", "requester", false, nil)
	if status != http.StatusOK {
		t.Fatalf("request detail: status %d, body %s", status, raw)
	}
	if detail := decodeBridge[discovery.Detail](t, raw); detail.Request.Status != discovery.StatusAvailable {
		t.Fatalf("request after availability = %+v", detail.Request)
	}
}

func newMusicServiceOn(t *testing.T, core *testCore, brainzURL string) *music.Service {
	t.Helper()
	ctx := context.Background()
	service, err := music.New(ctx, core.pool, core.manager)
	if err != nil {
		t.Fatalf("music.New: %v", err)
	}
	cfg, err := service.ConfigView(ctx)
	if err != nil {
		t.Fatalf("music config: %v", err)
	}
	cfg.MusicBrainzURL = brainzURL + "/"
	cfg.MusicBrainzRateMs = 0
	cfg.CoverArtURL = brainzURL + "/"
	if _, err := service.SetConfig(ctx, cfg); err != nil {
		t.Fatalf("music SetConfig: %v", err)
	}
	return service
}

func bridgeActor(r *http.Request) (string, bool) {
	return r.Header.Get("X-Test-User"), r.Header.Get("X-Test-Approve") == "1"
}

func bridgeCan(r *http.Request, permission string) bool {
	for _, held := range strings.Split(r.Header.Get("X-Test-Perms"), ",") {
		if strings.TrimSpace(held) == permission {
			return true
		}
	}
	return false
}

func bridgeCall(t *testing.T, handler http.Handler, method, path, body, user string, canApprove bool, perms []string) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = "localhost"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-User", user)
	if canApprove {
		request.Header.Set("X-Test-Approve", "1")
	}
	if len(perms) > 0 {
		request.Header.Set("X-Test-Perms", strings.Join(perms, ","))
	}
	handler.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func decodeBridge[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, raw)
	}
	return value
}
