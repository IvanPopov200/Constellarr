package music

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errDatabase = errors.New("music: database operation failed")

const (
	// Distinct cross-process advisory lock for music configuration writes.
	musicConfigLock int64 = 0x4D75736963436F6E

	maxIDBytes      = 64
	maxNameRunes    = 512
	maxTextRunes    = 1000
	maxReleaseBytes = 512
	maxRoots        = 32
	maxPathBytes    = 4096
	maxFormats      = 12
	historyLimit    = 200
)

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}

type Store struct {
	pool           *pgxpool.Pool
	operationSlots chan struct{}
}

func NewStore(ctx context.Context, pool *pgxpool.Pool, defaults Config) (*Store, error) {
	if pool == nil {
		return nil, errors.New("music: a PostgreSQL pool is required")
	}
	if err := seed(ctx, pool, defaults); err != nil {
		return nil, err
	}
	return &Store{pool: pool, operationSlots: make(chan struct{}, max(1, int(pool.Config().MaxConns-4)/2))}, nil
}

func (s *Store) OperationSlots() chan struct{} {
	return s.operationSlots
}

func seed(ctx context.Context, pool *pgxpool.Pool, defaults Config) error {
	encoded, err := encode(defaults)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx,
		`INSERT INTO music_config (id, data) VALUES (true, $1::jsonb) ON CONFLICT (id) DO NOTHING`, string(encoded))
	if err != nil {
		return dbError("initialize music", err)
	}
	return nil
}

func encode(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("music: value could not be encoded")
	}
	return encoded, nil
}

func dbError(op string, cause error) error {
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		return fmt.Errorf("music: %s: database error %s: %w", op, pgErr.Code, errDatabase)
	}
	return fmt.Errorf("music: %s: %w", op, errDatabase)
}

func missingReference(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func validID(id string) bool {
	if id == "" || len(id) > maxIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func newID() string {
	return rand.Text()
}

const artistColumns = `id, coalesce(musicbrainz_id, ''), name, data, added_at, updated_at`

func scanArtist(row rowScanner) (Artist, error) {
	var (
		artist Artist
		raw    []byte
	)
	if err := row.Scan(&artist.ID, &artist.MusicBrainzID, &artist.Name, &raw, &artist.AddedAt, &artist.UpdatedAt); err != nil {
		return Artist{}, err
	}
	if err := json.Unmarshal(raw, &artist); err != nil {
		return Artist{}, err
	}
	artist.Albums = nil
	return artist, nil
}

func (s *Store) Artists(ctx context.Context) ([]Artist, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+artistColumns+` FROM music_artists ORDER BY lower(name), id`)
	if err != nil {
		return nil, dbError("list artists", err)
	}
	defer rows.Close()
	artists := make([]Artist, 0, 32)
	for rows.Next() {
		artist, err := scanArtist(rows)
		if err != nil {
			return nil, dbError("list artists", err)
		}
		artists = append(artists, artist)
	}
	return artists, rows.Err()
}

func (s *Store) Artist(ctx context.Context, id string) (Artist, error) {
	artist, err := scanArtist(s.pool.QueryRow(ctx, `SELECT `+artistColumns+` FROM music_artists WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Artist{}, ErrNotFound
	}
	if err != nil {
		return Artist{}, dbError("load artist", err)
	}
	albums, err := s.AlbumsForArtist(ctx, id)
	if err != nil {
		return Artist{}, err
	}
	artist.Albums = albums
	return artist, nil
}

func (s *Store) ArtistByMusicBrainz(ctx context.Context, mbid string) (Artist, error) {
	artist, err := scanArtist(s.pool.QueryRow(ctx, `SELECT `+artistColumns+` FROM music_artists WHERE musicbrainz_id = $1`, mbid))
	if errors.Is(err, pgx.ErrNoRows) {
		return Artist{}, ErrNotFound
	}
	if err != nil {
		return Artist{}, dbError("load artist", err)
	}
	return artist, nil
}

func (s *Store) SaveArtist(ctx context.Context, artist Artist) (Artist, error) {
	return s.saveArtist(ctx, s.pool, artist, nil)
}

func (s *Store) SaveArtistWithAlbums(ctx context.Context, artist Artist, albums []Album) (Artist, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Artist{}, dbError("save artist", err)
	}
	defer tx.Rollback(ctx)
	saved, err := s.saveArtist(ctx, tx, artist, albums)
	if err != nil {
		return Artist{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Artist{}, dbError("save artist", err)
	}
	return saved, nil
}

func (s *Store) saveArtist(ctx context.Context, q querier, artist Artist, albums []Album) (Artist, error) {
	artist.ID = strings.TrimSpace(artist.ID)
	if !validID(artist.ID) || strings.TrimSpace(artist.Name) == "" {
		return Artist{}, fmt.Errorf("%w: an artist ID and name are required", ErrInvalid)
	}
	artist.Name = truncate(artist.Name, maxNameRunes)
	artist.Albums = nil
	encoded, err := encode(artist)
	if err != nil {
		return Artist{}, err
	}
	row := q.QueryRow(ctx,
		`INSERT INTO music_artists (id, musicbrainz_id, name, data, updated_at)
		 VALUES ($1, nullif($2, ''), $3, $4::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET musicbrainz_id = EXCLUDED.musicbrainz_id, name = EXCLUDED.name,
		 data = EXCLUDED.data, updated_at = now()
		 RETURNING `+artistColumns,
		artist.ID, artist.MusicBrainzID, artist.Name, string(encoded))
	saved, err := scanArtist(row)
	if uniqueViolation(err) {
		return Artist{}, fmt.Errorf("%w: this artist is already in the library", ErrConflict)
	}
	if err != nil {
		return Artist{}, dbError("save artist", err)
	}
	for _, album := range albums {
		album.ArtistID = saved.ID
		if _, err := s.saveAlbum(ctx, q, album, nil); err != nil {
			return Artist{}, err
		}
	}
	return saved, nil
}

func (s *Store) DeleteArtist(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM music_artists WHERE id = $1`, id)
	if err != nil {
		return dbError("delete artist", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const albumColumns = `id, artist_id, coalesce(musicbrainz_id, ''), title, release_date, album_type, data, added_at, updated_at`

func scanAlbum(row rowScanner) (Album, error) {
	var (
		album Album
		raw   []byte
	)
	if err := row.Scan(&album.ID, &album.ArtistID, &album.MusicBrainzID, &album.Title, &album.ReleaseDate,
		&album.Type, &raw, &album.AddedAt, &album.UpdatedAt); err != nil {
		return Album{}, err
	}
	if err := json.Unmarshal(raw, &album); err != nil {
		return Album{}, err
	}
	album.Tracks = nil
	return album, nil
}

func (s *Store) Albums(ctx context.Context) ([]Album, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+albumColumns+` FROM music_albums ORDER BY lower(title), id`)
	if err != nil {
		return nil, dbError("list albums", err)
	}
	defer rows.Close()
	albums := make([]Album, 0, 32)
	for rows.Next() {
		album, err := scanAlbum(rows)
		if err != nil {
			return nil, dbError("list albums", err)
		}
		albums = append(albums, album)
	}
	return albums, rows.Err()
}

func (s *Store) AlbumsForArtist(ctx context.Context, artistID string) ([]Album, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+albumColumns+` FROM music_albums WHERE artist_id = $1 ORDER BY release_date, lower(title), id`, artistID)
	if err != nil {
		return nil, dbError("list artist albums", err)
	}
	defer rows.Close()
	albums := make([]Album, 0, 16)
	for rows.Next() {
		album, err := scanAlbum(rows)
		if err != nil {
			return nil, dbError("list artist albums", err)
		}
		albums = append(albums, album)
	}
	return albums, rows.Err()
}

func (s *Store) AlbumByMusicBrainz(ctx context.Context, mbid string) (Album, error) {
	album, err := scanAlbum(s.pool.QueryRow(ctx, `SELECT `+albumColumns+` FROM music_albums WHERE musicbrainz_id = $1`, mbid))
	if errors.Is(err, pgx.ErrNoRows) {
		return Album{}, ErrNotFound
	}
	if err != nil {
		return Album{}, dbError("load album", err)
	}
	return album, nil
}

func (s *Store) Album(ctx context.Context, id string) (Album, error) {
	album, err := scanAlbum(s.pool.QueryRow(ctx, `SELECT `+albumColumns+` FROM music_albums WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Album{}, ErrNotFound
	}
	if err != nil {
		return Album{}, dbError("load album", err)
	}
	tracks, err := s.Tracks(ctx, id)
	if err != nil {
		return Album{}, err
	}
	album.Tracks = tracks
	return album, nil
}

func (s *Store) SaveAlbum(ctx context.Context, album Album) (Album, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Album{}, dbError("save album", err)
	}
	defer tx.Rollback(ctx)
	saved, err := s.saveAlbum(ctx, tx, album, album.Tracks)
	if err != nil {
		return Album{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Album{}, dbError("save album", err)
	}
	return saved, nil
}

// PatchAlbum merges operation-owned fields and the tracklist so concurrent edits survive.
func (s *Store) PatchAlbum(ctx context.Context, albumID string, fields map[string]any, tracks []Track) error {
	albumID = strings.TrimSpace(albumID)
	if !validID(albumID) || len(fields) == 0 {
		return fmt.Errorf("%w: an album ID and fields are required", ErrInvalid)
	}
	encoded, err := encode(fields)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError("update album", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx,
		`UPDATE music_albums SET data = data || $2::jsonb,
		 title = coalesce(nullif($2::jsonb->>'title', ''), title),
		 release_date = coalesce(nullif($2::jsonb->>'releaseDate', ''), release_date),
		 album_type = coalesce(nullif($2::jsonb->>'type', ''), album_type),
		 updated_at = now()
		 WHERE id = $1`, albumID, string(encoded))
	if err != nil {
		return dbError("update album", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if tracks != nil {
		if err := s.saveTracks(ctx, tx, albumID, tracks); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("update album", err)
	}
	return nil
}

func (s *Store) PatchAlbumData(ctx context.Context, albumID string, fields map[string]any) error {
	return s.PatchAlbum(ctx, albumID, fields, nil)
}

// PatchArtistData merges artist fields without replacing the whole artist record.
func (s *Store) PatchArtistData(ctx context.Context, artistID string, fields map[string]any) error {
	artistID = strings.TrimSpace(artistID)
	if !validID(artistID) || len(fields) == 0 {
		return fmt.Errorf("%w: an artist ID and fields are required", ErrInvalid)
	}
	encoded, err := encode(fields)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE music_artists SET data = data || $2::jsonb,
		 name = coalesce(nullif($2::jsonb->>'name', ''), name),
		 updated_at = now()
		 WHERE id = $1`, artistID, string(encoded))
	if err != nil {
		return dbError("update artist", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddArtistAlbums inserts new albums for an existing artist without rewriting the artist row.
func (s *Store) AddArtistAlbums(ctx context.Context, artistID string, albums []Album) error {
	if len(albums) == 0 {
		return nil
	}
	if !validID(artistID) {
		return ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError("save artist albums", err)
	}
	defer tx.Rollback(ctx)
	for _, album := range albums {
		album.ArtistID = artistID
		if _, err := s.saveAlbum(ctx, tx, album, nil); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("save artist albums", err)
	}
	return nil
}

func (s *Store) saveAlbum(ctx context.Context, q querier, album Album, tracks []Track) (Album, error) {
	album.ID = strings.TrimSpace(album.ID)
	album.ArtistID = strings.TrimSpace(album.ArtistID)
	if !validID(album.ID) || !validID(album.ArtistID) || strings.TrimSpace(album.Title) == "" {
		return Album{}, fmt.Errorf("%w: an album ID, artist, and title are required", ErrInvalid)
	}
	album.Title = truncate(album.Title, maxNameRunes)
	album.Tracks = nil
	encoded, err := encode(album)
	if err != nil {
		return Album{}, err
	}
	row := q.QueryRow(ctx,
		`INSERT INTO music_albums (id, artist_id, musicbrainz_id, title, release_date, album_type, data, updated_at)
		 VALUES ($1, $2, nullif($3, ''), $4, $5, $6, $7::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET artist_id = EXCLUDED.artist_id, musicbrainz_id = EXCLUDED.musicbrainz_id,
		 title = EXCLUDED.title, release_date = EXCLUDED.release_date, album_type = EXCLUDED.album_type,
		 data = EXCLUDED.data, updated_at = now()
		 RETURNING `+albumColumns,
		album.ID, album.ArtistID, album.MusicBrainzID, album.Title, album.ReleaseDate, album.Type, string(encoded))
	saved, err := scanAlbum(row)
	switch {
	case uniqueViolation(err):
		return Album{}, fmt.Errorf("%w: this album is already in the library", ErrConflict)
	case missingReference(err):
		return Album{}, fmt.Errorf("%w: the artist does not exist", ErrNotFound)
	case err != nil:
		return Album{}, dbError("save album", err)
	}
	if tracks != nil {
		if err := s.saveTracks(ctx, q, saved.ID, tracks); err != nil {
			return Album{}, err
		}
	}
	return saved, nil
}

func (s *Store) DeleteAlbum(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM music_albums WHERE id = $1`, id)
	if err != nil {
		return dbError("delete album", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Tracks(ctx context.Context, albumID string) ([]Track, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, disc, number, title, data FROM music_tracks WHERE album_id = $1 ORDER BY disc, number, id`, albumID)
	if err != nil {
		return nil, dbError("list tracks", err)
	}
	defer rows.Close()
	tracks := make([]Track, 0, 16)
	for rows.Next() {
		var (
			track Track
			raw   []byte
		)
		if err := rows.Scan(&track.ID, &track.Disc, &track.Number, &track.Title, &raw); err != nil {
			return nil, dbError("list tracks", err)
		}
		if err := json.Unmarshal(raw, &track); err != nil {
			return nil, dbError("list tracks", err)
		}
		tracks = append(tracks, track)
	}
	return tracks, rows.Err()
}

// saveTracks replaces the album tracklist while keeping canonical rows stable by ID.
func (s *Store) saveTracks(ctx context.Context, q querier, albumID string, tracks []Track) error {
	kept := make([]string, 0, len(tracks))
	for i := range tracks {
		track := tracks[i]
		if !validID(track.ID) {
			track.ID = newID()
		}
		if track.Disc < 1 {
			track.Disc = 1
		}
		if track.Number < 0 {
			track.Number = 0
		}
		track.Title = truncate(track.Title, maxNameRunes)
		track.Artist = truncate(track.Artist, maxNameRunes)
		encoded, err := encode(track)
		if err != nil {
			return err
		}
		// Number 0 marks an unmatched file so several rows can coexist.
		storedID := track.ID
		if track.Number == 0 {
			_, err = q.Exec(ctx,
				`INSERT INTO music_tracks (id, album_id, disc, number, title, data) VALUES ($1, $2, $3, $4, $5, $6::jsonb)
				 ON CONFLICT (id) DO UPDATE SET disc = EXCLUDED.disc, number = EXCLUDED.number,
				 title = EXCLUDED.title, data = EXCLUDED.data`,
				track.ID, albumID, track.Disc, track.Number, track.Title, string(encoded))
		} else {
			// A conflicting disc/number keeps its canonical row and ID.
			err = q.QueryRow(ctx,
				`INSERT INTO music_tracks (id, album_id, disc, number, title, data) VALUES ($1, $2, $3, $4, $5, $6::jsonb)
				 ON CONFLICT (album_id, disc, number) DO UPDATE SET title = EXCLUDED.title, data = EXCLUDED.data
				 RETURNING id`,
				track.ID, albumID, track.Disc, track.Number, track.Title, string(encoded)).Scan(&storedID)
		}
		if err != nil {
			return dbError("save tracks", err)
		}
		kept = append(kept, storedID)
	}
	if len(kept) == 0 {
		_, err := q.Exec(ctx, `DELETE FROM music_tracks WHERE album_id = $1`, albumID)
		return wrapDB("save tracks", err)
	}
	_, err := q.Exec(ctx, `DELETE FROM music_tracks WHERE album_id = $1 AND NOT (id = ANY($2::text[]))`, albumID, kept)
	return wrapDB("save tracks", err)
}

func wrapDB(op string, err error) error {
	if err != nil {
		return dbError(op, err)
	}
	return nil
}

func (s *Store) Config(ctx context.Context) (Config, error) {
	var (
		cfg Config
		raw []byte
	)
	err := s.pool.QueryRow(ctx, `SELECT data FROM music_config WHERE id = true`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, dbError("load music config", err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, dbError("load music config", err)
	}
	return cfg, nil
}

func (s *Store) SaveConfig(ctx context.Context, cfg Config) (Config, error) {
	cfg.MusicBrainzReady = false
	cfg.FFprobeAvailable = false
	encoded, err := encode(cfg)
	if err != nil {
		return Config{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Config{}, dbError("save music config", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, musicConfigLock); err != nil {
		return Config{}, dbError("save music config", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO music_config (id, data, updated_at) VALUES (true, $1::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, string(encoded)); err != nil {
		return Config{}, dbError("save music config", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Config{}, dbError("save music config", err)
	}
	return cfg, nil
}

const acquisitionColumns = `job_id, album_id, release, status, error, updated_at`

func scanAcquisition(row rowScanner) (Acquisition, error) {
	var (
		acquisition Acquisition
		raw         []byte
		updated     time.Time
	)
	if err := row.Scan(&acquisition.JobID, &acquisition.AlbumID, &raw, &acquisition.Status, &acquisition.Error, &updated); err != nil {
		return Acquisition{}, err
	}
	if err := json.Unmarshal(raw, &acquisition); err != nil {
		return Acquisition{}, err
	}
	return acquisition, nil
}

func (s *Store) Acquisitions(ctx context.Context) ([]Acquisition, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+acquisitionColumns+` FROM music_acquisitions ORDER BY updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, dbError("list acquisitions", err)
	}
	defer rows.Close()
	acquisitions := make([]Acquisition, 0, 16)
	for rows.Next() {
		acquisition, err := scanAcquisition(rows)
		if err != nil {
			return nil, dbError("list acquisitions", err)
		}
		acquisitions = append(acquisitions, acquisition)
	}
	return acquisitions, rows.Err()
}

func (s *Store) AcquisitionsFor(ctx context.Context, albumID string) ([]Acquisition, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+acquisitionColumns+` FROM music_acquisitions WHERE album_id = $1 ORDER BY updated_at DESC`, albumID)
	if err != nil {
		return nil, dbError("list album acquisitions", err)
	}
	defer rows.Close()
	acquisitions := make([]Acquisition, 0, 4)
	for rows.Next() {
		acquisition, err := scanAcquisition(rows)
		if err != nil {
			return nil, dbError("list album acquisitions", err)
		}
		acquisitions = append(acquisitions, acquisition)
	}
	return acquisitions, rows.Err()
}

// ArtistHistory returns one artist's events.
func (s *Store) ArtistHistory(ctx context.Context, artistID string) ([]History, error) {
	if !validID(artistID) {
		return nil, ErrNotFound
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM music_artists WHERE id = $1)`, artistID).Scan(&exists); err != nil {
		return nil, dbError("load music history", err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id, album_id, artist_id, type, message, created_at FROM music_history
		 WHERE artist_id = $1 ORDER BY id DESC LIMIT $2`, artistID, historyLimit)
	if err != nil {
		return nil, dbError("load music history", err)
	}
	defer rows.Close()
	history := make([]History, 0, 16)
	for rows.Next() {
		var event History
		if err := rows.Scan(&event.ID, &event.AlbumID, &event.ArtistID, &event.Type, &event.Message, &event.CreatedAt); err != nil {
			return nil, dbError("load music history", err)
		}
		history = append(history, event)
	}
	return history, rows.Err()
}

// SaveAcquisition records ownership of a download; a job another media type owns is never taken over.
func (s *Store) SaveAcquisition(ctx context.Context, acquisition Acquisition) error {
	acquisition.AlbumID = strings.TrimSpace(acquisition.AlbumID)
	acquisition.JobID = strings.TrimSpace(acquisition.JobID)
	acquisition.ReleaseID = strings.TrimSpace(acquisition.ReleaseID)
	acquisition.Status = strings.TrimSpace(acquisition.Status)
	if !validID(acquisition.AlbumID) || !validID(acquisition.JobID) {
		return fmt.Errorf("%w: acquisition album and job IDs are required", ErrInvalid)
	}
	if len(acquisition.ReleaseID) > maxReleaseBytes {
		return fmt.Errorf("%w: acquisition release ID is too long", ErrInvalid)
	}
	acquisition.Error = truncate(acquisition.Error, maxTextRunes)
	acquisition.Title = truncate(acquisition.Title, maxNameRunes)
	release, err := encode(acquisition)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError("claim music download", err)
	}
	defer tx.Rollback(ctx)
	var owned bool
	if err := tx.QueryRow(ctx,
		`SELECT movie_adopted OR media_type IN ('movie', 'tv') FROM downloads WHERE id = $1 FOR UPDATE`,
		acquisition.JobID).Scan(&owned); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return dbError("claim music download", err)
	}
	var musicOwned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM music_acquisitions WHERE job_id = $1)`, acquisition.JobID).Scan(&musicOwned); err != nil {
		return dbError("claim music download", err)
	}
	if owned && !musicOwned {
		return fmt.Errorf("%w: the download belongs to another library", ErrConflict)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO music_acquisitions (job_id, album_id, release, status, error, updated_at)
		 VALUES ($1, $2, $3::jsonb, $4, $5, now())
		 ON CONFLICT (job_id) DO UPDATE SET album_id = EXCLUDED.album_id, release = EXCLUDED.release,
		 status = EXCLUDED.status, error = EXCLUDED.error, updated_at = now()
		 WHERE music_acquisitions.album_id = EXCLUDED.album_id`,
		acquisition.JobID, acquisition.AlbumID, string(release), acquisition.Status, acquisition.Error)
	switch {
	case missingReference(err):
		return fmt.Errorf("%w: acquisition album or download does not exist", ErrNotFound)
	case uniqueViolation(err):
		return fmt.Errorf("%w: the download belongs to another library", ErrConflict)
	case err != nil:
		return dbError("save acquisition", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("claim music download", err)
	}
	return nil
}

// History returns one album's events, or the newest events across all albums for a blank ID.
func (s *Store) History(ctx context.Context, albumID string) ([]History, error) {
	query := `SELECT id, album_id, artist_id, type, message, created_at FROM music_history ORDER BY id DESC LIMIT $1`
	args := []any{historyLimit}
	if albumID != "" {
		if !validID(albumID) {
			return nil, ErrNotFound
		}
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM music_albums WHERE id = $1)`, albumID).Scan(&exists); err != nil {
			return nil, dbError("load music history", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
		query = `SELECT id, album_id, artist_id, type, message, created_at FROM music_history
		 WHERE album_id = $1 ORDER BY id DESC LIMIT $2`
		args = []any{albumID, historyLimit}
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbError("load music history", err)
	}
	defer rows.Close()
	history := make([]History, 0, 16)
	for rows.Next() {
		var event History
		if err := rows.Scan(&event.ID, &event.AlbumID, &event.ArtistID, &event.Type, &event.Message, &event.CreatedAt); err != nil {
			return nil, dbError("load music history", err)
		}
		history = append(history, event)
	}
	return history, rows.Err()
}

func (s *Store) Event(ctx context.Context, albumID, artistID, kind, message string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO music_history (album_id, artist_id, type, message) VALUES ($1, $2, $3, $4)`,
		truncate(albumID, maxIDBytes), truncate(artistID, maxIDBytes), truncate(kind, 64), truncate(message, maxTextRunes))
	return wrapDB("save music event", err)
}

func (s *Store) Block(ctx context.Context, albumID, releaseID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO music_blocklist (album_id, release_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		albumID, truncate(releaseID, maxReleaseBytes))
	return wrapDB("block release", err)
}

// TorrentJobIDs lists recent torrent downloads; the shared download list is usenet-only.
func (s *Store) TorrentJobIDs(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM downloads WHERE protocol = 'torrent' ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, dbError("list torrent downloads", err)
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbError("list torrent downloads", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) Blocked(ctx context.Context, albumID, releaseID string) (bool, error) {
	var blocked bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM music_blocklist WHERE album_id = $1 AND release_id = $2)`,
		albumID, releaseID).Scan(&blocked)
	if err != nil {
		return false, dbError("load blocklist", err)
	}
	return blocked, nil
}

func (s *Store) Blocklist(ctx context.Context, albumID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT release_id FROM music_blocklist WHERE album_id = $1 ORDER BY created_at DESC`, albumID)
	if err != nil {
		return nil, dbError("load blocklist", err)
	}
	defer rows.Close()
	ids := make([]string, 0, 8)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbError("load blocklist", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) Unblock(ctx context.Context, albumID, releaseID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM music_blocklist WHERE album_id = $1 AND release_id = $2`, albumID, releaseID)
	return wrapDB("unblock release", err)
}

func (s *Store) TaskDue(ctx context.Context, name string, interval time.Duration) bool {
	var last time.Time
	if err := s.pool.QueryRow(ctx, `SELECT last_run FROM music_automation WHERE name = $1`, name).Scan(&last); err != nil {
		return true
	}
	return time.Since(last) >= interval
}

func (s *Store) RecordTask(ctx context.Context, name string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO music_automation (name) VALUES ($1) ON CONFLICT (name) DO UPDATE SET last_run = now()`, name)
	return wrapDB("save music task", err)
}
