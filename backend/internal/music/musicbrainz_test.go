package music

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testMBID    = "b10bbbfc-cf9e-42e0-be17-e2c3e1d2600d"
	testGroupID = "9cd1c1e1-1e1e-4b1e-8b1e-1e1e1e1e1e1e"
)

func writeJSONBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestBrainzSearchContracts(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		if !strings.Contains(r.Header.Get("User-Agent"), "Constellarr") {
			t.Errorf("request has no identifying User-Agent: %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/ws/2/artist":
			writeJSONBody(t, w, `{"artists":[{"id":"`+testMBID+`","name":"Muse","sort-name":"Muse","disambiguation":"UK rock band","country":"GB","type":"Group","score":100}]}`)
		case "/ws/2/release-group":
			if r.URL.Query().Get("artist") != "" {
				writeJSONBody(t, w, `{"release-groups":[{"id":"`+testGroupID+`","title":"Absolution","primary-type":"Album","first-release-date":"2003-09-29"}]}`)
				return
			}
			writeJSONBody(t, w, `{"release-groups":[{"id":"`+testGroupID+`","title":"Absolution","primary-type":"Album","secondary-types":["Live"],"first-release-date":"2003-09-29","score":97,"artist-credit":[{"name":"Muse","artist":{"id":"`+testMBID+`","name":"Muse"}}]}]}`)
		case "/ws/2/release-group/" + testGroupID:
			writeJSONBody(t, w, `{"id":"`+testGroupID+`","title":"Absolution","primary-type":"Album","first-release-date":"2003-09-29",
				"artist-credit":[{"name":"Muse","artist":{"id":"`+testMBID+`","name":"Muse"}}],
				"releases":[{"id":"rel-1","title":"Absolution","date":"2003-09-29","status":"Official"},{"id":"rel-2","title":"Absolution","date":"2004-01-01","status":"Bootleg"}]}`)
		case "/ws/2/release/rel-1":
			writeJSONBody(t, w, `{"id":"rel-1","title":"Absolution","date":"2003-09-29","media":[
				{"position":1,"format":"CD","track-count":2,"tracks":[
					{"id":"rec-1","number":"1","position":1,"title":"Intro","length":60000,"artist-credit":[{"name":"Muse","artist":{"id":"`+testMBID+`"}}]},
					{"id":"rec-2","number":"2","position":2,"title":"Apocalypse Please","length":240000}]},
				{"position":2,"format":"CD","track-count":1,"tracks":[
					{"id":"rec-3","number":"1","position":1,"title":"Fury","length":200000}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newBrainz(server.URL+"/ws/2", 0)
	if err != nil {
		t.Fatalf("newBrainz: %v", err)
	}
	ctx := context.Background()

	artists, err := client.searchArtists(ctx, "muse", 10, 0)
	if err != nil || len(artists) != 1 {
		t.Fatalf("searchArtists = %v, %v", artists, err)
	}
	if artists[0].MusicBrainzID != testMBID || artists[0].Country != "GB" || artists[0].Disambiguation == "" {
		t.Fatalf("artist fields were not parsed: %+v", artists[0])
	}
	if !strings.Contains(queries[0], "query=muse") || !strings.Contains(queries[0], "limit=10") || !strings.Contains(queries[0], "fmt=json") {
		t.Fatalf("search parameters are wrong: %s", queries[0])
	}

	albums, err := client.searchAlbums(ctx, "muse absolution", 5, 20)
	if err != nil || len(albums) != 1 {
		t.Fatalf("searchAlbums = %v, %v", albums, err)
	}
	if albums[0].Year != 2003 || albums[0].ArtistName != "Muse" || albums[0].ArtistID != testMBID {
		t.Fatalf("album fields were not parsed: %+v", albums[0])
	}
	if albums[0].Type != "live" {
		t.Fatalf("album type = %q, want live from the secondary type", albums[0].Type)
	}
	if !strings.Contains(queries[1], "offset=20") {
		t.Fatalf("search offset is missing: %s", queries[1])
	}

	groups, err := client.artistAlbums(ctx, testMBID, 100)
	if err != nil || len(groups) != 1 || groups[0].ArtistID != testMBID {
		t.Fatalf("artistAlbums = %v, %v", groups, err)
	}

	album, err := client.lookupAlbum(ctx, testGroupID)
	if err != nil {
		t.Fatalf("lookupAlbum: %v", err)
	}
	if album.Title != "Absolution" || album.Year != 2003 || album.ArtistName != "Muse" {
		t.Fatalf("album identity = %+v", album)
	}
	if len(album.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(album.Tracks))
	}
	if album.Tracks[2].Disc != 2 || album.Tracks[2].Number != 1 || album.Tracks[2].Title != "Fury" {
		t.Fatalf("multidisc track was not parsed: %+v", album.Tracks[2])
	}
	if album.Tracks[0].DurationMS != 60000 || album.Tracks[1].Artist != "" {
		t.Fatalf("track details were not parsed: %+v", album.Tracks)
	}
}

func TestBrainzErrorsAreSanitized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("query") {
		case "absent":
			http.NotFound(w, r)
			return
		case "broken":
			writeJSONBody(t, w, `{not json`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("secret upstream detail"))
	}))
	defer server.Close()
	client, err := newBrainz(server.URL, 0)
	if err != nil {
		t.Fatalf("newBrainz: %v", err)
	}
	if _, err := client.searchArtists(context.Background(), "q", 1, 0); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("server error leaked: %v", err)
	}
	if _, err := client.searchArtists(context.Background(), "absent", 1, 0); err != ErrNotFound {
		t.Fatalf("missing artist error = %v, want ErrNotFound", err)
	}
	if _, err := client.searchAlbums(context.Background(), "broken", 1, 0); err == nil {
		t.Fatal("broken JSON was accepted")
	}
}

func TestBrainzRateLimit(t *testing.T) {
	var calls []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, time.Now())
		writeJSONBody(t, w, `{"artists":[]}`)
	}))
	defer server.Close()
	client, err := newBrainz(server.URL, 40*time.Millisecond)
	if err != nil {
		t.Fatalf("newBrainz: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := client.searchArtists(context.Background(), "q", 1, 0); err != nil {
			t.Fatalf("search %d: %v", i, err)
		}
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	if spacing := calls[2].Sub(calls[1]); spacing < 40*time.Millisecond {
		t.Fatalf("requests were not rate limited: %s apart", spacing)
	}
}

func TestBrainzLookupArtistWithoutParams(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.Path + "?" + r.URL.RawQuery
		if r.URL.Path != "/ws/2/artist/"+testMBID {
			http.NotFound(w, r)
			return
		}
		writeJSONBody(t, w, `{"id":"`+testMBID+`","name":"Muse","sort-name":"Muse","disambiguation":"UK rock band","country":"GB","type":"Group"}`)
	}))
	defer server.Close()
	client, err := newBrainz(server.URL+"/ws/2/", 0)
	if err != nil {
		t.Fatalf("newBrainz: %v", err)
	}
	// A lookup passes no parameters; the request must still be built and answered.
	artist, err := client.lookupArtist(context.Background(), testMBID)
	if err != nil {
		t.Fatalf("lookupArtist: %v", err)
	}
	if artist.MusicBrainzID != testMBID || artist.Name != "Muse" || artist.Country != "GB" {
		t.Fatalf("artist = %+v", artist)
	}
	if !strings.Contains(seen, "fmt=json") {
		t.Fatalf("lookup request query = %q, want fmt=json", seen)
	}
	if _, err := client.lookupArtist(context.Background(), "11111111-1111-1111-1111-111111111111"); err != ErrNotFound {
		t.Fatalf("missing artist error = %v, want ErrNotFound", err)
	}
}

func TestBrainzCover(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/missing/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\ncover"))
	}))
	defer server.Close()
	client, err := newBrainz(server.URL, 0)
	if err != nil {
		t.Fatalf("newBrainz: %v", err)
	}
	imageBase, err := coverImageBase(server.URL + "/cover/")
	if err != nil || imageBase == nil {
		t.Fatalf("coverImageBase: %v, %v", imageBase, err)
	}
	data, contentType, err := client.cover(context.Background(), imageBase, testGroupID)
	if err != nil {
		t.Fatalf("cover: %v", err)
	}
	if contentType != "image/png" || len(data) == 0 {
		t.Fatalf("cover = %q, %d bytes", contentType, len(data))
	}
	missingBase, err := coverImageBase(server.URL + "/missing/")
	if err != nil {
		t.Fatalf("coverImageBase: %v", err)
	}
	if _, _, err := client.cover(context.Background(), missingBase, testGroupID); err != errCoverUnavailable {
		t.Fatalf("missing cover error = %v, want errCoverUnavailable", err)
	}
	if _, _, err := client.cover(context.Background(), nil, testGroupID); err != ErrNotConfigured {
		t.Fatalf("unconfigured cover error = %v, want ErrNotConfigured", err)
	}
}

func TestProviderURLValidation(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "https://user:pass@example.com/ws/2", "https://example.com/ws/2?q=1", "not a url"} {
		if _, err := newBrainz(raw, 0); err == nil {
			t.Fatalf("newBrainz(%q) accepted an unsafe URL", raw)
		}
	}
	if _, err := newBrainz("", 0); err != nil {
		t.Fatalf("default MusicBrainz URL was rejected: %v", err)
	}
	if _, err := newBrainz("https://example.com/ws/2", -1); err == nil {
		t.Fatal("negative rate limit was accepted")
	}
}

func TestValidMBID(t *testing.T) {
	if !validMBID(testMBID) {
		t.Fatalf("real MBID was rejected")
	}
	for _, raw := range []string{"", "abc", strings.Repeat("z", 36), "b10bbbfc-cf9e-42e0-be17-e2c3e1d2600"} {
		if validMBID(raw) {
			t.Fatalf("validMBID(%q) = true", raw)
		}
	}
}

func TestYearFromDate(t *testing.T) {
	if got := yearFromDate("2003-09-29"); got != 2003 {
		t.Fatalf("yearFromDate = %d", got)
	}
	if got := yearFromDate("2003"); got != 2003 {
		t.Fatalf("yearFromDate = %d", got)
	}
	if got := yearFromDate(""); got != 0 {
		t.Fatalf("yearFromDate = %d, want 0", got)
	}
}

func TestCoverContentType(t *testing.T) {
	if _, ok := coverContentType("text/html"); ok {
		t.Fatal("html was accepted as cover art")
	}
	if value, ok := coverContentType("image/jpeg; charset=binary"); !ok || value != "image/jpeg" {
		t.Fatalf("jpeg content type = %q, %v", value, ok)
	}
}
