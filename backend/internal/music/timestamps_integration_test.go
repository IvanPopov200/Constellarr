package music_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/music"
)

func newTimestampEnv(t *testing.T) (*pgxpool.Pool, *music.Store) {
	t.Helper()
	env := newEnv(t, nil, "Muse", "Absolution")
	return env.pool, env.service.Store
}

func canonicalArtistTimes(t *testing.T, pool *pgxpool.Pool, id string) (time.Time, time.Time) {
	t.Helper()
	var added, updated time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT added_at, updated_at FROM music_artists WHERE id = $1`, id).Scan(&added, &updated); err != nil {
		t.Fatalf("read artist columns: %v", err)
	}
	return added, updated
}

func canonicalAlbumTimes(t *testing.T, pool *pgxpool.Pool, id string) (time.Time, time.Time) {
	t.Helper()
	var added, updated time.Time
	if err := pool.QueryRow(context.Background(),
		`SELECT added_at, updated_at FROM music_albums WHERE id = $1`, id).Scan(&added, &updated); err != nil {
		t.Fatalf("read album columns: %v", err)
	}
	return added, updated
}

// Stale JSON must not replace the database timestamps after an import.
func TestMusicStoreTimestampsMatchCanonicalColumns(t *testing.T) {
	pool, store := newTimestampEnv(t)
	ctx := context.Background()

	saved, err := store.SaveArtistWithAlbums(ctx, music.Artist{ID: "artist-1", Name: "Muse"},
		[]music.Album{{ID: "album-1", ArtistID: "artist-1", Title: "Absolution"}})
	if err != nil {
		t.Fatalf("SaveArtistWithAlbums: %v", err)
	}
	if saved.AddedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatalf("created artist timestamps = added %s, updated %s; want the database values", saved.AddedAt, saved.UpdatedAt)
	}
	artistAdded, artistUpdated := canonicalArtistTimes(t, pool, "artist-1")
	if !saved.AddedAt.Equal(artistAdded) || !saved.UpdatedAt.Equal(artistUpdated) {
		t.Fatalf("created artist timestamps = %s/%s, columns = %s/%s", saved.AddedAt, saved.UpdatedAt, artistAdded, artistUpdated)
	}
	albumAdded, albumUpdated := canonicalAlbumTimes(t, pool, "album-1")

	// The library views that back GET /api/v1/music/albums and /artists must report the columns.
	albums, err := store.Albums(ctx)
	if err != nil || len(albums) != 1 {
		t.Fatalf("Albums = %+v, %v", albums, err)
	}
	if albums[0].AddedAt.IsZero() || albums[0].UpdatedAt.IsZero() {
		t.Fatalf("listed album timestamps = added %s, updated %s; want the database values", albums[0].AddedAt, albums[0].UpdatedAt)
	}
	if !albums[0].AddedAt.Equal(albumAdded) || !albums[0].UpdatedAt.Equal(albumUpdated) {
		t.Fatalf("listed album timestamps = %s/%s, columns = %s/%s", albums[0].AddedAt, albums[0].UpdatedAt, albumAdded, albumUpdated)
	}
	artists, err := store.Artists(ctx)
	if err != nil || len(artists) != 1 {
		t.Fatalf("Artists = %+v, %v", artists, err)
	}
	if artists[0].AddedAt.IsZero() || artists[0].UpdatedAt.IsZero() {
		t.Fatalf("listed artist timestamps = added %s, updated %s; want the database values", artists[0].AddedAt, artists[0].UpdatedAt)
	}
	artist, err := store.Artist(ctx, "artist-1")
	if err != nil || len(artist.Albums) != 1 {
		t.Fatalf("Artist = %+v, %v", artist, err)
	}
	if artist.AddedAt.IsZero() || artist.Albums[0].AddedAt.IsZero() || artist.Albums[0].UpdatedAt.IsZero() {
		t.Fatalf("loaded artist timestamps = artist %s, album %s/%s", artist.AddedAt, artist.Albums[0].AddedAt, artist.Albums[0].UpdatedAt)
	}

	// Updates move updated_at, keep added_at, and still load from the columns.
	time.Sleep(10 * time.Millisecond)
	if err := store.PatchAlbumData(ctx, "album-1", map[string]any{"status": "wanted"}); err != nil {
		t.Fatalf("PatchAlbumData: %v", err)
	}
	if err := store.PatchArtistData(ctx, "artist-1", map[string]any{"name": "Muse (UK)"}); err != nil {
		t.Fatalf("PatchArtistData: %v", err)
	}
	updatedAlbumAdded, updatedAlbumUpdated := canonicalAlbumTimes(t, pool, "album-1")
	if !updatedAlbumUpdated.After(albumUpdated) {
		t.Fatalf("album updated_at did not advance: %s -> %s", albumUpdated, updatedAlbumUpdated)
	}
	reloaded, err := store.Album(ctx, "album-1")
	if err != nil {
		t.Fatalf("Album: %v", err)
	}
	if !reloaded.AddedAt.Equal(updatedAlbumAdded) || !reloaded.UpdatedAt.Equal(updatedAlbumUpdated) {
		t.Fatalf("reloaded album timestamps = %s/%s, columns = %s/%s", reloaded.AddedAt, reloaded.UpdatedAt, updatedAlbumAdded, updatedAlbumUpdated)
	}
	reloadedArtist, err := store.Artist(ctx, "artist-1")
	if err != nil {
		t.Fatalf("Artist: %v", err)
	}
	updatedArtistAdded, updatedArtistUpdated := canonicalArtistTimes(t, pool, "artist-1")
	if !updatedArtistUpdated.After(artistUpdated) {
		t.Fatalf("artist updated_at did not advance: %s -> %s", artistUpdated, updatedArtistUpdated)
	}
	if !reloadedArtist.AddedAt.Equal(updatedArtistAdded) || !reloadedArtist.UpdatedAt.Equal(updatedArtistUpdated) {
		t.Fatalf("reloaded artist timestamps = %s/%s, columns = %s/%s", reloadedArtist.AddedAt, reloadedArtist.UpdatedAt, updatedArtistAdded, updatedArtistUpdated)
	}

	// A stale snapshot, like the ones already stored before the fix, must not win over the row.
	if _, err := pool.Exec(ctx, `UPDATE music_artists SET data = data || '{"id":"ghost-artist","name":"Ghost","addedAt":"0001-01-01T00:00:00Z","updatedAt":"0001-01-01T00:00:00Z"}'::jsonb WHERE id = 'artist-1'`); err != nil {
		t.Fatalf("tamper artist snapshot: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE music_albums SET data = data || '{"id":"ghost-album","artistId":"ghost-artist","title":"Ghost","addedAt":"0001-01-01T00:00:00Z","updatedAt":"0001-01-01T00:00:00Z"}'::jsonb WHERE id = 'album-1'`); err != nil {
		t.Fatalf("tamper album snapshot: %v", err)
	}
	stale, err := store.Album(ctx, "album-1")
	if err != nil {
		t.Fatalf("Album after snapshot tamper: %v", err)
	}
	if stale.ID != "album-1" || stale.ArtistID != "artist-1" || stale.Title != "Absolution" {
		t.Fatalf("album identity = %s/%s/%q; want the canonical row", stale.ID, stale.ArtistID, stale.Title)
	}
	if !stale.AddedAt.Equal(updatedAlbumAdded) || !stale.UpdatedAt.Equal(updatedAlbumUpdated) {
		t.Fatalf("album timestamps after snapshot tamper = %s/%s, columns = %s/%s", stale.AddedAt, stale.UpdatedAt, updatedAlbumAdded, updatedAlbumUpdated)
	}
	staleArtist, err := store.Artist(ctx, "artist-1")
	if err != nil {
		t.Fatalf("Artist after snapshot tamper: %v", err)
	}
	if staleArtist.ID != "artist-1" || staleArtist.Name != "Muse (UK)" {
		t.Fatalf("artist identity = %s/%q; want the canonical row", staleArtist.ID, staleArtist.Name)
	}
	if !staleArtist.AddedAt.Equal(updatedArtistAdded) || !staleArtist.UpdatedAt.Equal(updatedArtistUpdated) {
		t.Fatalf("artist timestamps after snapshot tamper = %s/%s, columns = %s/%s", staleArtist.AddedAt, staleArtist.UpdatedAt, updatedArtistAdded, updatedArtistUpdated)
	}
}

// Re-saving the same track position keeps its database identity.
func TestMusicStoreTrackIdentityUsesCanonicalRow(t *testing.T) {
	pool, store := newTimestampEnv(t)
	ctx := context.Background()
	if _, err := store.SaveArtist(ctx, music.Artist{ID: "artist-1", Name: "Muse"}); err != nil {
		t.Fatalf("SaveArtist: %v", err)
	}
	original := music.Track{ID: "track-a", Disc: 1, Number: 1, Title: "Intro"}
	if _, err := store.SaveAlbum(ctx, music.Album{ID: "album-1", ArtistID: "artist-1", Title: "Absolution", Tracks: []music.Track{original}}); err != nil {
		t.Fatalf("SaveAlbum: %v", err)
	}
	// The same position is saved again with a different snapshot ID and title.
	incoming := music.Track{ID: "track-b", Disc: 1, Number: 1, Title: "Intro (Remaster)"}
	if _, err := store.SaveAlbum(ctx, music.Album{ID: "album-1", ArtistID: "artist-1", Title: "Absolution", Tracks: []music.Track{incoming}}); err != nil {
		t.Fatalf("second SaveAlbum: %v", err)
	}
	var canonicalID, canonicalTitle string
	if err := pool.QueryRow(ctx, `SELECT id, title FROM music_tracks WHERE album_id = 'album-1'`).Scan(&canonicalID, &canonicalTitle); err != nil {
		t.Fatalf("read track columns: %v", err)
	}
	tracks, err := store.Tracks(ctx, "album-1")
	if err != nil || len(tracks) != 1 {
		t.Fatalf("Tracks = %+v, %v", tracks, err)
	}
	if tracks[0].ID != canonicalID {
		t.Fatalf("track ID = %q, want the canonical row ID %q", tracks[0].ID, canonicalID)
	}
	if tracks[0].Title != canonicalTitle {
		t.Fatalf("track title = %q, want the canonical row title %q", tracks[0].Title, canonicalTitle)
	}
}

// Recovery status in the row must take precedence over the saved release snapshot.
func TestMusicStoreAcquisitionStatusUsesCanonicalRow(t *testing.T) {
	pool, store := newTimestampEnv(t)
	ctx := context.Background()
	if _, err := store.SaveArtist(ctx, music.Artist{ID: "artist-1", Name: "Muse"}); err != nil {
		t.Fatalf("SaveArtist: %v", err)
	}
	if _, err := store.SaveAlbum(ctx, music.Album{ID: "album-1", ArtistID: "artist-1", Title: "Absolution"}); err != nil {
		t.Fatalf("SaveAlbum: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb, status, files, media_type)
		 VALUES ('job-1', 'rel-1', 'Muse - Absolution', '\x00'::bytea, 'completed', '[]', 'music')`); err != nil {
		t.Fatalf("seed download: %v", err)
	}
	if err := store.SaveAcquisition(ctx, music.Acquisition{AlbumID: "album-1", JobID: "job-1", ReleaseID: "rel-1", Status: "queued"}); err != nil {
		t.Fatalf("SaveAcquisition: %v", err)
	}
	// A restart records the interrupted import on the canonical column only.
	if _, err := pool.Exec(ctx, `UPDATE music_acquisitions SET status = 'importing' WHERE job_id = 'job-1'`); err != nil {
		t.Fatalf("mark interrupted import: %v", err)
	}
	acquisitions, err := store.AcquisitionsFor(ctx, "album-1")
	if err != nil || len(acquisitions) != 1 {
		t.Fatalf("AcquisitionsFor = %+v, %v", acquisitions, err)
	}
	if acquisitions[0].Status != "importing" {
		t.Fatalf("acquisition status = %q, want the canonical column value %q", acquisitions[0].Status, "importing")
	}
}
