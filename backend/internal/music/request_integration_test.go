package music_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IvanPopov200/Constellarr/backend/internal/music"
)

const (
	fixtureArtistMBID = "b10bbbfc-cf9e-42e0-be17-e2c3e1d2600d"
	fixtureGroupMBID  = "9cd1c1e1-1e1e-4b1e-8b1e-1e1e1e1e1e1e"
	fixtureReleaseID  = "9cd1c1e1-1e1e-4b1e-8b1e-2e2e2e2e2e2e"
)

// newBrainzFixture serves MusicBrainz JSON; a gate can hold the release-group lookup.
func newBrainzFixture(t *testing.T, gates ...*searchGate) *httptest.Server {
	t.Helper()
	artist := `{"id":"` + fixtureArtistMBID + `","name":"Muse","sort-name":"Muse","disambiguation":"UK rock band","country":"GB","type":"Group"}`
	group := `{"id":"` + fixtureGroupMBID + `","title":"Absolution","primary-type":"Album","first-release-date":"2003-09-29",
		"artist-credit":[{"name":"Muse","artist":{"id":"` + fixtureArtistMBID + `","name":"Muse"}}],
		"releases":[{"id":"` + fixtureReleaseID + `","title":"Absolution","date":"2003-09-29","status":"Official"}]}`
	release := `{"id":"` + fixtureReleaseID + `","title":"Absolution","date":"2003-09-29","media":[{"position":1,"format":"CD","track-count":2,"tracks":[
		{"id":"rec-1","number":"1","position":1,"title":"Intro","length":60000},
		{"id":"rec-2","number":"2","position":2,"title":"Apocalypse Please","length":240000}]}]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/ws/2/artist/"+fixtureArtistMBID:
			_, _ = w.Write([]byte(artist))
		case r.URL.Path == "/ws/2/artist":
			_, _ = w.Write([]byte(`{"artists":[` + artist + `]}`))
		case r.URL.Path == "/ws/2/release-group/"+fixtureGroupMBID:
			if len(gates) > 0 && gates[0] != nil && gates[0].holding() {
				gates[0].enter()
			}
			_, _ = w.Write([]byte(group))
		case r.URL.Path == "/ws/2/release-group":
			_, _ = w.Write([]byte(`{"release-groups":[` + group + `]}`))
		case r.URL.Path == "/ws/2/release/"+fixtureReleaseID:
			_, _ = w.Write([]byte(release))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestMusicAddArtistUsesMusicBrainzContract covers the nil-parameter artist lookup on the real call path.
func TestMusicAddArtistUsesMusicBrainzContract(t *testing.T) {
	env := newEnv(t, nil, "", "")
	env.musicBrain = newBrainzFixture(t).URL + "/ws/2/"
	env.libraryConfig(t)
	ctx := context.Background()

	artist, err := env.service.AddArtist(ctx, music.AddArtistInput{
		MusicBrainzID: fixtureArtistMBID, Monitored: true, MonitorOption: "all",
	})
	if err != nil {
		t.Fatalf("AddArtist: %v", err)
	}
	if artist.Name != "Muse" || artist.SortName != "Muse" || artist.Country != "GB" || artist.Type != "Group" {
		t.Fatalf("artist metadata = %+v", artist)
	}
	if artist.MusicBrainzID != fixtureArtistMBID || !artist.Monitored {
		t.Fatalf("artist identity = %+v", artist)
	}
	if len(artist.Albums) != 1 {
		t.Fatalf("artist albums = %+v", artist.Albums)
	}
	album := artist.Albums[0]
	if album.MusicBrainzID != fixtureGroupMBID || album.Title != "Absolution" || album.Year != 2003 || !album.Monitored {
		t.Fatalf("release group album = %+v", album)
	}
	stored, err := env.service.Artist(ctx, artist.ID)
	if err != nil || stored.Name != "Muse" || len(stored.Albums) != 1 {
		t.Fatalf("stored artist = %+v, %v", stored, err)
	}
}

// TestMusicRequestedAlbumSearchesWithUnmonitoredArtist proves a requested album is still searched.
func TestMusicRequestedAlbumSearchesWithUnmonitoredArtist(t *testing.T) {
	env := newEnv(t, []fakeRelease{{ID: "rel-flac-request", Title: "Muse - Absolution (2003) [FLAC]", Size: 400 << 20}}, "Muse", "Absolution")
	env.musicBrain = newBrainzFixture(t).URL + "/ws/2/"
	env.libraryConfig(t)
	ctx := context.Background()

	album, err := env.service.AddReleaseGroup(ctx, music.AddReleaseGroupInput{MusicBrainzID: fixtureGroupMBID, Title: "Absolution"})
	if err != nil {
		t.Fatalf("AddReleaseGroup: %v", err)
	}
	if !album.Monitored || album.MusicBrainzID != fixtureGroupMBID || len(album.Tracks) != 2 {
		t.Fatalf("requested album = %+v", album)
	}
	artist, err := env.service.Artist(ctx, album.ArtistID)
	if err != nil {
		t.Fatalf("Artist: %v", err)
	}
	if artist.Monitored || artist.MonitorOption != "none" {
		t.Fatalf("request artist = %+v, want unmonitored with monitor option none", artist)
	}

	result, err := env.service.Sync(ctx, true)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Queued != 1 {
		t.Fatalf("sync result = %+v, want the requested album queued", result)
	}
	acquisitions, err := env.service.Store.AcquisitionsFor(ctx, album.ID)
	if err != nil || len(acquisitions) != 1 || acquisitions[0].Status != "queued" {
		t.Fatalf("acquisitions = %+v, %v", acquisitions, err)
	}
	refreshed, err := env.service.Artist(ctx, album.ArtistID)
	if err != nil {
		t.Fatalf("Artist after sync: %v", err)
	}
	if refreshed.LastRefreshAt != nil {
		t.Fatalf("an unmonitored request artist was crawled: %+v", refreshed.LastRefreshAt)
	}
	wanted, err := env.service.Wanted(ctx)
	if err != nil {
		t.Fatalf("Wanted: %v", err)
	}
	found := false
	for _, entry := range wanted {
		if entry.ID == album.ID {
			found = true
			if entry.Status != "downloading" {
				t.Fatalf("requested album status = %q", entry.Status)
			}
		}
	}
	if !found {
		t.Fatalf("requested album missing from wanted: %+v", wanted)
	}
}
