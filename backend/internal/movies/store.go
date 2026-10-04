package movies

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

var errDatabase = errors.New("movies: database operation failed")

const (
	// Distinct cross-process advisory lock for movie configuration writes.
	movieConfigLock int64 = 0x4D6F76696573436F

	maxIDBytes      = 64
	maxTitleRunes   = 512
	maxTextRunes    = 1000
	maxReleaseBytes = 512
	maxTemplateLen  = 512
	maxTokenBytes   = 32
	maxURLBytes     = 2048
	maxSecretBytes  = 512
	maxRoots        = 32
	maxPathBytes    = 4096
	historyLimit    = 200
)

type rowScanner interface {
	Scan(dest ...any) error
}

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, pool *pgxpool.Pool, defaults Config) (*Store, error) {
	if pool == nil {
		return nil, errors.New("movies: a PostgreSQL pool is required")
	}
	if err := seed(ctx, pool, defaults); err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func seed(ctx context.Context, pool *pgxpool.Pool, defaults Config) error {
	defaults.RootFolders = normalizeRoots(defaults.RootFolders)
	encoded, err := encode(defaults)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return dbError("initialize movies", err)
	}
	defer tx.Rollback(ctx)
	initialized, err := tx.Exec(ctx,
		`INSERT INTO movie_config (id, data) VALUES (true, $1::jsonb) ON CONFLICT (id) DO NOTHING`,
		string(encoded))
	if err != nil {
		return dbError("initialize movies", err)
	}
	if initialized.RowsAffected() == 0 {
		if err := tx.Commit(ctx); err != nil {
			return dbError("initialize movies", err)
		}
		return nil
	}
	for _, profile := range quality.Defaults() {
		if strings.TrimSpace(profile.Name) == "" {
			continue
		}
		if profile.ID == "" {
			profile.ID = rand.Text()
		}
		encoded, err := encode(profile)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO movie_profiles (id, name, data) VALUES ($1, $2, $3::jsonb) ON CONFLICT DO NOTHING`,
			profile.ID, profile.Name, string(encoded)); err != nil {
			return dbError("initialize movies", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("initialize movies", err)
	}
	return nil
}

func encode(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("movies: value could not be encoded")
	}
	return encoded, nil
}

func dbError(op string, cause error) error {
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		return fmt.Errorf("movies: %s: database error %s: %w", op, pgErr.Code, errDatabase)
	}
	return fmt.Errorf("movies: %s: %w", op, errDatabase)
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func missingReference(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func validID(id string) bool {
	if id == "" || len(id) > maxIDBytes {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func normalizeIMDb(imdbID string) (string, bool) {
	imdbID = strings.ToLower(strings.TrimSpace(imdbID))
	if imdbID == "" {
		return "", true
	}
	if !metadata.ValidIMDbID(imdbID) {
		return "", false
	}
	return imdbID, true
}

func normalizeList(values []string) []string {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		normalized = append(normalized, value)
	}
	return normalized
}

func normalizeRoots(roots []RootFolder) []RootFolder {
	normalized := make([]RootFolder, 0, len(roots))
	for _, root := range roots {
		path := strings.TrimSpace(root.Path)
		if path != "" {
			path = filepath.Clean(path)
		}
		normalized = append(normalized, RootFolder{ID: strings.TrimSpace(root.ID), Path: path})
	}
	return normalized
}

var releasedLayouts = []string{"2006-01-02", "02 Jan 2006", "Jan 2, 2006", "2006/01/02", "2006-01", "2006", time.RFC3339}

func validReleased(value string) bool {
	for _, layout := range releasedLayouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func normalizeMovie(movie *Movie) {
	movie.ID = strings.TrimSpace(movie.ID)
	movie.Metadata.IMDbID = strings.TrimSpace(movie.Metadata.IMDbID)
	movie.Metadata.Title = strings.TrimSpace(movie.Metadata.Title)
	movie.Metadata.Type = strings.TrimSpace(movie.Metadata.Type)
	movie.Metadata.Released = strings.TrimSpace(movie.Metadata.Released)
	movie.Metadata.Certification = strings.TrimSpace(movie.Metadata.Certification)
	movie.Metadata.Poster = strings.TrimSpace(movie.Metadata.Poster)
	movie.Metadata.Plot = strings.TrimSpace(movie.Metadata.Plot)
	movie.Metadata.Directors = normalizeList(movie.Metadata.Directors)
	movie.Metadata.Cast = normalizeList(movie.Metadata.Cast)
	movie.Metadata.Genres = normalizeList(movie.Metadata.Genres)
	movie.Metadata.Languages = normalizeList(movie.Metadata.Languages)
	movie.Metadata.Countries = normalizeList(movie.Metadata.Countries)
	movie.ProfileID = strings.TrimSpace(movie.ProfileID)
	movie.RootID = strings.TrimSpace(movie.RootID)
	movie.Tags = normalizeList(movie.Tags)
	if movie.Files == nil {
		movie.Files = []File{}
	}
	for i := range movie.Files {
		movie.Files[i].RootID = strings.TrimSpace(movie.Files[i].RootID)
		movie.Files[i].Path = strings.TrimSpace(movie.Files[i].Path)
		movie.Files[i].Quality = strings.TrimSpace(movie.Files[i].Quality)
	}
	movie.Status = strings.TrimSpace(movie.Status)
	movie.Collection = strings.TrimSpace(movie.Collection)
	movie.Error = truncate(movie.Error, maxTextRunes)
}

func validateMovie(movie Movie) error {
	if movie.Metadata.Title == "" {
		return fmt.Errorf("%w: a movie title is required", ErrInvalid)
	}
	if utf8.RuneCountInString(movie.Metadata.Title) > maxTitleRunes {
		return fmt.Errorf("%w: movie title is too long", ErrInvalid)
	}
	if movie.ID != "" && !validID(movie.ID) {
		return fmt.Errorf("%w: movie ID is invalid", ErrInvalid)
	}
	if movie.ProfileID != "" && !validID(movie.ProfileID) {
		return fmt.Errorf("%w: quality profile ID is invalid", ErrInvalid)
	}
	if movie.RootID != "" && !validID(movie.RootID) {
		return fmt.Errorf("%w: root folder ID is invalid", ErrInvalid)
	}
	if movie.Metadata.Year != 0 && (movie.Metadata.Year < 1870 || movie.Metadata.Year > time.Now().Year()+10) {
		return fmt.Errorf("%w: movie year is out of range", ErrInvalid)
	}
	if movie.Metadata.Released != "" && !validReleased(movie.Metadata.Released) {
		return fmt.Errorf("%w: movie release date is not a recognized date", ErrInvalid)
	}
	if movie.Metadata.Rating != nil && (math.IsNaN(*movie.Metadata.Rating) || *movie.Metadata.Rating < 0 || *movie.Metadata.Rating > 10) {
		return fmt.Errorf("%w: movie rating must be between 0 and 10", ErrInvalid)
	}
	if movie.Metadata.Votes < 0 || movie.Metadata.Runtime < 0 {
		return fmt.Errorf("%w: movie votes and runtime cannot be negative", ErrInvalid)
	}
	for _, file := range movie.Files {
		if file.Path == "" || len(file.Path) > maxPathBytes || !filepath.IsLocal(file.Path) || strings.Contains(file.Path, "://") {
			return fmt.Errorf("%w: movie file paths must be local relative paths", ErrInvalid)
		}
		if file.Size <= 0 {
			return fmt.Errorf("%w: movie file sizes must be positive", ErrInvalid)
		}
		if file.RootID != "" && !validID(file.RootID) {
			return fmt.Errorf("%w: movie file root ID is invalid", ErrInvalid)
		}
	}
	return nil
}

func validateReferences(ctx context.Context, q querier, profileID string, rootIDsToCheck []string) error {
	if profileID != "" {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM movie_profiles WHERE id = $1)`, profileID).Scan(&exists); err != nil {
			return dbError("validate references", err)
		}
		if !exists {
			return fmt.Errorf("%w: quality profile does not exist", ErrInvalid)
		}
	}
	if len(rootIDsToCheck) == 0 {
		return nil
	}
	roots, err := savedRootIDs(ctx, q)
	if err != nil {
		return err
	}
	for _, id := range rootIDsToCheck {
		if id != "" && !roots[id] {
			return fmt.Errorf("%w: root folder does not exist", ErrInvalid)
		}
	}
	return nil
}

func savedRootIDs(ctx context.Context, q querier) (map[string]bool, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT data FROM movie_config WHERE id`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, dbError("load movie config", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, errors.New("movies: saved config is invalid")
	}
	roots := make(map[string]bool, len(cfg.RootFolders))
	for _, root := range cfg.RootFolders {
		roots[root.ID] = true
	}
	return roots, nil
}

const movieColumns = `id, coalesce(imdb_id, ''), data, added_at, updated_at`

func scanMovie(row rowScanner) (Movie, error) {
	var (
		movie          Movie
		id, imdbID     string
		raw            []byte
		added, updated time.Time
	)
	if err := row.Scan(&id, &imdbID, &raw, &added, &updated); err != nil {
		return Movie{}, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &movie); err != nil {
			return Movie{}, errors.New("movies: saved movie data is invalid")
		}
	}
	movie.ID, movie.Metadata.IMDbID, movie.AddedAt, movie.UpdatedAt = id, imdbID, added, updated
	normalizeMovie(&movie)
	return movie, nil
}

func movieByID(ctx context.Context, q querier, id string) (Movie, error) {
	movie, err := scanMovie(q.QueryRow(ctx, `SELECT `+movieColumns+` FROM movies WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Movie{}, ErrNotFound
	}
	if err != nil {
		return Movie{}, dbError("load movie", err)
	}
	return movie, nil
}

func movieByIMDb(ctx context.Context, q querier, imdbID string) (Movie, error) {
	movie, err := scanMovie(q.QueryRow(ctx, `SELECT `+movieColumns+` FROM movies WHERE imdb_id = $1`, imdbID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Movie{}, ErrNotFound
	}
	if err != nil {
		return Movie{}, dbError("load movie", err)
	}
	return movie, nil
}

func (s *Store) List(ctx context.Context) ([]Movie, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+movieColumns+` FROM movies ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, dbError("list movies", err)
	}
	defer rows.Close()
	movies := make([]Movie, 0)
	for rows.Next() {
		movie, err := scanMovie(rows)
		if err != nil {
			return nil, dbError("list movies", err)
		}
		movies = append(movies, movie)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("list movies", err)
	}
	return movies, nil
}

func (s *Store) Get(ctx context.Context, id string) (Movie, error) {
	return movieByID(ctx, s.pool, strings.TrimSpace(id))
}

func (s *Store) Patch(ctx context.Context, id string, fields map[string]any) (Movie, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Movie{}, dbError("update movie", err)
	}
	defer tx.Rollback(ctx)
	movie, err := patchMovie(ctx, tx, id, fields)
	if err != nil {
		return Movie{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Movie{}, dbError("update movie", err)
	}
	return movie, nil
}

// Merge only changed fields so independent catalog edits and imports cannot overwrite one another.
func patchMovie(ctx context.Context, q querier, id string, fields map[string]any) (Movie, error) {
	body, err := encode(fields)
	if err != nil {
		return Movie{}, err
	}
	movie, err := scanMovie(q.QueryRow(ctx,
		`UPDATE movies SET data = data || $2::jsonb, updated_at = now() WHERE id = $1 RETURNING `+movieColumns,
		id, string(body)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Movie{}, ErrNotFound
	}
	if err != nil {
		return Movie{}, dbError("update movie", err)
	}
	if err := validateMovie(movie); err != nil {
		return Movie{}, err
	}
	roots := []string{movie.RootID}
	for _, file := range movie.Files {
		roots = append(roots, file.RootID)
	}
	if err := validateReferences(ctx, q, movie.ProfileID, roots); err != nil {
		return Movie{}, err
	}
	return movie, nil
}

func (s *Store) FindIMDb(ctx context.Context, imdbID string) (Movie, error) {
	imdbID, ok := normalizeIMDb(imdbID)
	if !ok || imdbID == "" {
		return Movie{}, ErrNotFound
	}
	return movieByIMDb(ctx, s.pool, imdbID)
}

func (s *Store) Save(ctx context.Context, movie Movie) (Movie, error) {
	normalizeMovie(&movie)
	imdbID, ok := normalizeIMDb(movie.Metadata.IMDbID)
	if !ok {
		return Movie{}, fmt.Errorf("%w: IMDb ID must use the tt1234567 form", ErrInvalid)
	}
	movie.Metadata.IMDbID = imdbID
	if err := validateMovie(movie); err != nil {
		return Movie{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Movie{}, dbError("save movie", err)
	}
	defer tx.Rollback(ctx)
	if imdbID != "" {
		var existingID string
		switch err := tx.QueryRow(ctx, `SELECT id FROM movies WHERE imdb_id = $1`, imdbID).Scan(&existingID); {
		case err == nil && existingID != movie.ID:
			return movieByID(ctx, tx, existingID)
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return Movie{}, dbError("save movie", err)
		}
	}
	rootIDs := make([]string, 0, len(movie.Files)+1)
	rootIDs = append(rootIDs, movie.RootID)
	for _, file := range movie.Files {
		rootIDs = append(rootIDs, file.RootID)
	}
	if err := validateReferences(ctx, tx, movie.ProfileID, rootIDs); err != nil {
		return Movie{}, err
	}
	if movie.ID == "" {
		movie.ID = rand.Text()
	}
	now := time.Now().UTC()
	if movie.AddedAt.IsZero() {
		movie.AddedAt = now
	}
	var storedAdded time.Time
	var storedIMDb string
	switch err := tx.QueryRow(ctx, `SELECT added_at, coalesce(imdb_id, '') FROM movies WHERE id = $1`, movie.ID).Scan(&storedAdded, &storedIMDb); {
	case err == nil:
		movie.AddedAt = storedAdded
		// An update without an identity keeps the stored IMDb ID.
		if imdbID == "" && storedIMDb != "" {
			imdbID, movie.Metadata.IMDbID = storedIMDb, storedIMDb
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return Movie{}, dbError("save movie", err)
	}
	movie.UpdatedAt = now
	encoded, err := encode(movie)
	if err != nil {
		return Movie{}, err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO movies (id, imdb_id, data, added_at, updated_at)
		 VALUES ($1, nullif($2, ''), $3::jsonb, $4, $5)
		 ON CONFLICT (id) DO UPDATE SET imdb_id = EXCLUDED.imdb_id, data = EXCLUDED.data, updated_at = EXCLUDED.updated_at`,
		movie.ID, imdbID, string(encoded), movie.AddedAt, movie.UpdatedAt)
	if err != nil {
		if imdbID != "" && uniqueViolation(err) {
			_ = tx.Rollback(ctx)
			if existing, lookupErr := movieByIMDb(ctx, s.pool, imdbID); lookupErr == nil {
				return existing, nil
			}
		}
		return Movie{}, dbError("save movie", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Movie{}, dbError("save movie", err)
	}
	return movie, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM movies WHERE id = $1`, strings.TrimSpace(id))
	if err != nil {
		return dbError("delete movie", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const profileColumns = `id, name, data`

func scanProfile(row rowScanner) (quality.Profile, error) {
	var (
		profile  quality.Profile
		id, name string
		raw      []byte
	)
	if err := row.Scan(&id, &name, &raw); err != nil {
		return quality.Profile{}, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &profile); err != nil {
			return quality.Profile{}, errors.New("movies: saved profile data is invalid")
		}
	}
	profile.ID, profile.Name = id, name
	if profile.Qualities == nil {
		profile.Qualities = []string{}
	}
	if profile.Rules == nil {
		profile.Rules = []quality.Rule{}
	}
	return profile, nil
}

func (s *Store) Profiles(ctx context.Context) ([]quality.Profile, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+profileColumns+` FROM movie_profiles ORDER BY lower(name), id`)
	if err != nil {
		return nil, dbError("list profiles", err)
	}
	defer rows.Close()
	profiles := make([]quality.Profile, 0)
	for rows.Next() {
		profile, err := scanProfile(rows)
		if err != nil {
			return nil, dbError("list profiles", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("list profiles", err)
	}
	return profiles, nil
}

func (s *Store) Profile(ctx context.Context, id string) (quality.Profile, error) {
	profile, err := scanProfile(s.pool.QueryRow(ctx, `SELECT `+profileColumns+` FROM movie_profiles WHERE id = $1`, strings.TrimSpace(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return quality.Profile{}, ErrNotFound
	}
	if err != nil {
		return quality.Profile{}, dbError("load profile", err)
	}
	return profile, nil
}

func (s *Store) SaveProfile(ctx context.Context, profile quality.Profile) (quality.Profile, error) {
	profile.ID = strings.TrimSpace(profile.ID)
	profile.Name = strings.TrimSpace(profile.Name)
	if profile.Name == "" || utf8.RuneCountInString(profile.Name) > maxTitleRunes || (profile.ID != "" && !validID(profile.ID)) {
		return quality.Profile{}, fmt.Errorf("%w: a profile name and valid ID are required", ErrInvalid)
	}
	if err := quality.Validate(profile); err != nil {
		return quality.Profile{}, fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	if profile.ID == "" {
		profile.ID = rand.Text()
	}
	encoded, err := encode(profile)
	if err != nil {
		return quality.Profile{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return quality.Profile{}, dbError("save profile", err)
	}
	defer tx.Rollback(ctx)
	var existingID string
	switch err := tx.QueryRow(ctx, `SELECT id FROM movie_profiles WHERE name = $1`, profile.Name).Scan(&existingID); {
	case err == nil && existingID != profile.ID:
		return quality.Profile{}, ErrConflict
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return quality.Profile{}, dbError("save profile", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO movie_profiles (id, name, data, updated_at) VALUES ($1, $2, $3::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, data = EXCLUDED.data, updated_at = now()`,
		profile.ID, profile.Name, string(encoded))
	if err != nil {
		if uniqueViolation(err) {
			return quality.Profile{}, ErrConflict
		}
		return quality.Profile{}, dbError("save profile", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return quality.Profile{}, dbError("save profile", err)
	}
	if profile.Qualities == nil {
		profile.Qualities = []string{}
	}
	if profile.Rules == nil {
		profile.Rules = []quality.Rule{}
	}
	return profile, nil
}

func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM movie_profiles WHERE id = $1
		 AND NOT EXISTS (SELECT 1 FROM movies WHERE data->>'profileId' = $1)
		 AND NOT EXISTS (SELECT 1 FROM movie_watchlists WHERE data->>'profileId' = $1)`, id)
	if err != nil {
		return dbError("delete profile", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM movie_profiles WHERE id = $1)`, id).Scan(&exists); err != nil {
		return dbError("delete profile", err)
	}
	if exists {
		return ErrConflict
	}
	return ErrNotFound
}

func loadConfig(ctx context.Context, q querier) (Config, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT data FROM movie_config WHERE id`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, dbError("load movie config", err)
	}
	var cfg Config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, errors.New("movies: saved config is invalid")
		}
	}
	cfg.RootFolders = normalizeRoots(cfg.RootFolders)
	return cfg, nil
}

func (s *Store) Config(ctx context.Context) (Config, error) {
	return loadConfig(ctx, s.pool)
}

func (s *Store) SaveConfig(ctx context.Context, cfg Config) (Config, error) {
	cfg.RootFolders = normalizeRoots(cfg.RootFolders)
	cfg.FolderTemplate = strings.TrimSpace(cfg.FolderTemplate)
	cfg.FileTemplate = strings.TrimSpace(cfg.FileTemplate)
	cfg.ImportMode = strings.ToLower(strings.TrimSpace(cfg.ImportMode))
	cfg.MinimumAvailability = strings.TrimSpace(cfg.MinimumAvailability)
	cfg.MetadataURL = strings.TrimSpace(cfg.MetadataURL)
	cfg.JellyfinURL = strings.TrimSpace(cfg.JellyfinURL)
	cfg.WebhookURL = strings.TrimSpace(cfg.WebhookURL)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Config{}, dbError("save movie config", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, movieConfigLock); err != nil {
		return Config{}, dbError("save movie config", err)
	}
	current, err := loadConfig(ctx, tx)
	if errors.Is(err, ErrNotFound) {
		current = Config{}
	} else if err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.MetadataAPIKey) == "" && current.MetadataAPIKey != "" && cfg.MetadataURL != "" && providerOrigin(cfg.MetadataURL) != providerOrigin(current.MetadataURL) {
		return Config{}, fmt.Errorf("%w: provide the metadata API key when changing its server", ErrInvalid)
	}
	if strings.TrimSpace(cfg.JellyfinAPIKey) == "" && current.JellyfinAPIKey != "" && cfg.JellyfinURL != "" && providerOrigin(cfg.JellyfinURL) != providerOrigin(current.JellyfinURL) {
		return Config{}, fmt.Errorf("%w: provide the Jellyfin API key when changing its server", ErrInvalid)
	}
	// Blank keys stay bound to their configured origin.
	if key := strings.TrimSpace(cfg.MetadataAPIKey); key == "" || key == current.MetadataAPIKey {
		cfg.MetadataAPIKey = current.MetadataAPIKey
	} else if len(cfg.MetadataAPIKey) > maxSecretBytes {
		return Config{}, fmt.Errorf("%w: metadata API key is too long", ErrInvalid)
	}
	if key := strings.TrimSpace(cfg.JellyfinAPIKey); key == "" || key == current.JellyfinAPIKey {
		cfg.JellyfinAPIKey = current.JellyfinAPIKey
	} else if len(cfg.JellyfinAPIKey) > maxSecretBytes {
		return Config{}, fmt.Errorf("%w: Jellyfin API key is too long", ErrInvalid)
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	cfg.MetadataConfigured = cfg.MetadataURL != "" && cfg.MetadataAPIKey != ""
	cfg.JellyfinConfigured = cfg.JellyfinURL != "" && cfg.JellyfinAPIKey != ""
	encoded, err := encode(cfg)
	if err != nil {
		return Config{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO movie_config (id, data, updated_at) VALUES (true, $1::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, string(encoded)); err != nil {
		return Config{}, dbError("save movie config", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Config{}, dbError("save movie config", err)
	}
	return cfg, nil
}

func validateConfig(cfg Config) error {
	if len(cfg.RootFolders) > maxRoots {
		return fmt.Errorf("%w: too many root folders", ErrInvalid)
	}
	paths := make(map[string]bool, len(cfg.RootFolders))
	ids := make(map[string]bool, len(cfg.RootFolders))
	for _, root := range cfg.RootFolders {
		if !validID(root.ID) {
			return fmt.Errorf("%w: root folder IDs must be non-empty and safe", ErrInvalid)
		}
		if !filepath.IsAbs(root.Path) || len(root.Path) > maxPathBytes || root.Path == string(filepath.Separator) {
			return fmt.Errorf("%w: root folder paths must be absolute", ErrInvalid)
		}
		if paths[root.Path] || ids[root.ID] {
			return fmt.Errorf("%w: root folders must be unique", ErrInvalid)
		}
		paths[root.Path], ids[root.ID] = true, true
	}
	switch cfg.ImportMode {
	case "copy", "hardlink", "move":
	default:
		return fmt.Errorf("%w: import mode must be copy, hardlink, or move", ErrInvalid)
	}
	if cfg.PollMinutes < 1 || cfg.PollMinutes > 1440 {
		return fmt.Errorf("%w: poll interval must be between 1 and 1440 minutes", ErrInvalid)
	}
	if cfg.SearchHours < 1 || cfg.SearchHours > 720 {
		return fmt.Errorf("%w: search interval must be between 1 and 720 hours", ErrInvalid)
	}
	switch strings.ToLower(cfg.MinimumAvailability) {
	case "", "announced", "incinemas", "released", "predb":
	default:
		return fmt.Errorf("%w: minimum availability must be announced, inCinemas, or released", ErrInvalid)
	}
	if err := validateHTTPURL("metadata", cfg.MetadataURL, false); err != nil {
		return err
	}
	if err := validateHTTPURL("Jellyfin", cfg.JellyfinURL, true); err != nil {
		return err
	}
	if err := validateHTTPURL("webhook", cfg.WebhookURL, true); err != nil {
		return err
	}
	if err := validateTemplate("folder", cfg.FolderTemplate); err != nil {
		return err
	}
	return validateTemplate("file", cfg.FileTemplate)
}

func providerOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host)
}

func validateHTTPURL(label, raw string, allowQuery bool) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || len(raw) > maxURLBytes || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%w: %s URL must be an absolute HTTP(S) URL without credentials", ErrInvalid, label)
	}
	if !allowQuery && parsed.RawQuery != "" {
		return fmt.Errorf("%w: %s URL must not include a query", ErrInvalid, label)
	}
	return nil
}

// namingTokens mirrors the library importer's supported naming tokens.
var namingTokens = map[string]bool{
	"title": true, "year": true, "imdbId": true, "quality": true, "original": true, "part": true,
}

func validateTemplate(label, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s naming template is required", ErrInvalid, label)
	}
	if len(value) > maxTemplateLen || !filepath.IsLocal(value) || strings.Contains(value, "://") || strings.Contains(value, `\`) {
		return fmt.Errorf("%w: %s naming template must be a local relative path", ErrInvalid, label)
	}
	tokens := 0
	for i := 0; i < len(value); {
		switch value[i] {
		case '{':
			end := strings.IndexByte(value[i+1:], '}')
			if end < 1 || end > maxTokenBytes {
				return fmt.Errorf("%w: %s naming template has an invalid token", ErrInvalid, label)
			}
			name := value[i+1 : i+1+end]
			if !namingTokens[name] {
				return fmt.Errorf("%w: %s naming template has an unknown token {%s}", ErrInvalid, label, name)
			}
			tokens++
			i += end + 2
		case '}':
			return fmt.Errorf("%w: %s naming template has an invalid token", ErrInvalid, label)
		default:
			r, size := utf8.DecodeRuneInString(value[i:])
			if r < ' ' || r == 0x7f {
				return fmt.Errorf("%w: %s naming template has an invalid token", ErrInvalid, label)
			}
			i += size
		}
	}
	if tokens == 0 {
		return fmt.Errorf("%w: %s naming template needs at least one {token}", ErrInvalid, label)
	}
	return nil
}

type acquisitionRelease struct {
	ReleaseID string           `json:"releaseId"`
	Title     string           `json:"title"`
	Decision  quality.Decision `json:"decision"`
	Override  bool             `json:"override,omitempty"`
}

const acquisitionColumns = `movie_id, job_id, release, status, error`

func scanAcquisition(row rowScanner) (Acquisition, error) {
	var (
		acquisition Acquisition
		raw         []byte
	)
	if err := row.Scan(&acquisition.MovieID, &acquisition.JobID, &raw, &acquisition.Status, &acquisition.Error); err != nil {
		return Acquisition{}, err
	}
	if len(raw) > 0 {
		var release acquisitionRelease
		if err := json.Unmarshal(raw, &release); err != nil {
			return Acquisition{}, errors.New("movies: saved acquisition data is invalid")
		}
		acquisition.ReleaseID, acquisition.Title, acquisition.Decision = release.ReleaseID, release.Title, release.Decision
		acquisition.Override = release.Override
	}
	if acquisition.Decision.Reasons == nil {
		acquisition.Decision.Reasons = []string{}
	}
	return acquisition, nil
}

func (s *Store) Acquisitions(ctx context.Context) ([]Acquisition, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+acquisitionColumns+` FROM movie_acquisitions ORDER BY updated_at DESC, job_id`)
	if err != nil {
		return nil, dbError("list acquisitions", err)
	}
	defer rows.Close()
	acquisitions := make([]Acquisition, 0)
	for rows.Next() {
		acquisition, err := scanAcquisition(rows)
		if err != nil {
			return nil, dbError("list acquisitions", err)
		}
		acquisitions = append(acquisitions, acquisition)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("list acquisitions", err)
	}
	return acquisitions, nil
}

func (s *Store) SaveAcquisition(ctx context.Context, acquisition Acquisition) error {
	acquisition.MovieID = strings.TrimSpace(acquisition.MovieID)
	acquisition.JobID = strings.TrimSpace(acquisition.JobID)
	acquisition.Status = strings.TrimSpace(acquisition.Status)
	acquisition.ReleaseID = strings.TrimSpace(acquisition.ReleaseID)
	if !validID(acquisition.MovieID) || !validID(acquisition.JobID) {
		return fmt.Errorf("%w: acquisition movie and job IDs are required", ErrInvalid)
	}
	if len(acquisition.ReleaseID) > maxReleaseBytes {
		return fmt.Errorf("%w: acquisition release ID is too long", ErrInvalid)
	}
	acquisition.Error = truncate(acquisition.Error, maxTextRunes)
	acquisition.Title = truncate(strings.TrimSpace(acquisition.Title), maxTitleRunes)
	release, err := encode(acquisitionRelease{
		ReleaseID: acquisition.ReleaseID, Title: acquisition.Title, Decision: acquisition.Decision, Override: acquisition.Override,
	})
	if err != nil {
		return err
	}
	// A job keeps its original movie; later updates can only refresh the release state.
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO movie_acquisitions (movie_id, job_id, release, status, error, updated_at)
		 VALUES ($1, $2, $3::jsonb, $4, $5, now())
		 ON CONFLICT (job_id) DO UPDATE SET release = EXCLUDED.release, status = EXCLUDED.status,
		 error = EXCLUDED.error, updated_at = now()
		 WHERE movie_acquisitions.movie_id = EXCLUDED.movie_id`,
		acquisition.MovieID, acquisition.JobID, string(release), acquisition.Status, acquisition.Error)
	if err != nil {
		if missingReference(err) {
			return fmt.Errorf("%w: acquisition movie or download does not exist", ErrNotFound)
		}
		return dbError("save acquisition", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// History returns one movie's events, or the newest events across all movies for a blank ID.
func (s *Store) History(ctx context.Context, movieID string) ([]History, error) {
	movieID = strings.TrimSpace(movieID)
	query := `SELECT id, movie_id, type, message, created_at FROM movie_history ORDER BY id DESC LIMIT $1`
	args := []any{historyLimit}
	if movieID != "" {
		if !validID(movieID) {
			return nil, ErrNotFound
		}
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM movies WHERE id = $1)`, movieID).Scan(&exists); err != nil {
			return nil, dbError("load movie history", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
		query = `SELECT id, movie_id, type, message, created_at FROM movie_history
		 WHERE movie_id = $1 ORDER BY id DESC LIMIT $2`
		args = []any{movieID, historyLimit}
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbError("load movie history", err)
	}
	defer rows.Close()
	history := make([]History, 0)
	for rows.Next() {
		var event History
		if err := rows.Scan(&event.ID, &event.MovieID, &event.Type, &event.Message, &event.CreatedAt); err != nil {
			return nil, dbError("load movie history", err)
		}
		history = append(history, event)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("load movie history", err)
	}
	return history, nil
}

func (s *Store) Event(ctx context.Context, movieID, kind, message string) error {
	movieID = strings.TrimSpace(movieID)
	kind = strings.TrimSpace(kind)
	if !validID(movieID) || kind == "" || utf8.RuneCountInString(kind) > maxIDBytes {
		return fmt.Errorf("%w: movie ID and event type are required", ErrInvalid)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO movie_history (movie_id, type, message) VALUES ($1, $2, $3)`,
		movieID, kind, truncate(message, maxTextRunes))
	if err != nil {
		if missingReference(err) {
			return fmt.Errorf("%w: movie does not exist", ErrNotFound)
		}
		return dbError("record movie event", err)
	}
	return nil
}

func (s *Store) Block(ctx context.Context, movieID, releaseID string) error {
	movieID = strings.TrimSpace(movieID)
	releaseID = strings.TrimSpace(releaseID)
	if !validID(movieID) || releaseID == "" || len(releaseID) > maxReleaseBytes {
		return fmt.Errorf("%w: a movie and release are required", ErrInvalid)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO movie_blocklist (movie_id, release_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, movieID, releaseID)
	if err != nil {
		if missingReference(err) {
			return fmt.Errorf("%w: movie does not exist", ErrNotFound)
		}
		return dbError("block release", err)
	}
	return nil
}

func (s *Store) Blocked(ctx context.Context, movieID, releaseID string) (bool, error) {
	movieID = strings.TrimSpace(movieID)
	releaseID = strings.TrimSpace(releaseID)
	if !validID(movieID) || releaseID == "" || len(releaseID) > maxReleaseBytes {
		return false, fmt.Errorf("%w: a movie and release are required", ErrInvalid)
	}
	var blocked bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM movie_blocklist WHERE movie_id = $1 AND release_id = $2)`,
		movieID, releaseID).Scan(&blocked); err != nil {
		return false, dbError("load blocked release", err)
	}
	return blocked, nil
}

func (s *Store) Unblock(ctx context.Context, movieID, releaseID string) error {
	movieID = strings.TrimSpace(movieID)
	releaseID = strings.TrimSpace(releaseID)
	if !validID(movieID) || releaseID == "" || len(releaseID) > maxReleaseBytes {
		return fmt.Errorf("%w: a movie and release are required", ErrInvalid)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM movie_blocklist WHERE movie_id = $1 AND release_id = $2`, movieID, releaseID)
	if err != nil {
		return dbError("unblock release", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const watchlistColumns = `id, data`

func scanWatchlist(row rowScanner) (Watchlist, error) {
	var (
		list Watchlist
		id   string
		raw  []byte
	)
	if err := row.Scan(&id, &raw); err != nil {
		return Watchlist{}, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &list); err != nil {
			return Watchlist{}, errors.New("movies: saved watchlist data is invalid")
		}
	}
	list.ID = id
	list.IMDbIDs = normalizeList(list.IMDbIDs)
	return list, nil
}

func (s *Store) Watchlists(ctx context.Context) ([]Watchlist, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+watchlistColumns+` FROM movie_watchlists ORDER BY updated_at DESC, id`)
	if err != nil {
		return nil, dbError("list watchlists", err)
	}
	defer rows.Close()
	watchlists := make([]Watchlist, 0)
	for rows.Next() {
		list, err := scanWatchlist(rows)
		if err != nil {
			return nil, dbError("list watchlists", err)
		}
		watchlists = append(watchlists, list)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("list watchlists", err)
	}
	return watchlists, nil
}

func (s *Store) SaveWatchlist(ctx context.Context, list Watchlist) (Watchlist, error) {
	list.ID = strings.TrimSpace(list.ID)
	list.Name = strings.TrimSpace(list.Name)
	list.URL = strings.TrimSpace(list.URL)
	list.ProfileID = strings.TrimSpace(list.ProfileID)
	list.RootID = strings.TrimSpace(list.RootID)
	if list.Name == "" || utf8.RuneCountInString(list.Name) > maxTitleRunes || (list.ID != "" && !validID(list.ID)) {
		return Watchlist{}, fmt.Errorf("%w: a watchlist name and valid ID are required", ErrInvalid)
	}
	ids := make([]string, 0, len(list.IMDbIDs))
	for _, raw := range list.IMDbIDs {
		imdbID, ok := normalizeIMDb(raw)
		if !ok || imdbID == "" {
			return Watchlist{}, fmt.Errorf("%w: watchlist IMDb IDs must use the tt1234567 form", ErrInvalid)
		}
		ids = append(ids, imdbID)
	}
	list.IMDbIDs = normalizeList(ids)
	if len(list.IMDbIDs) == 0 && list.URL == "" {
		return Watchlist{}, fmt.Errorf("%w: a watchlist needs IMDb IDs or a URL", ErrInvalid)
	}
	if err := validateHTTPURL("watchlist", list.URL, true); err != nil {
		return Watchlist{}, err
	}
	if list.IntervalHours < 1 || list.IntervalHours > 720 {
		return Watchlist{}, fmt.Errorf("%w: watchlist interval must be between 1 and 720 hours", ErrInvalid)
	}
	if list.ProfileID != "" && !validID(list.ProfileID) {
		return Watchlist{}, fmt.Errorf("%w: quality profile ID is invalid", ErrInvalid)
	}
	if list.RootID != "" && !validID(list.RootID) {
		return Watchlist{}, fmt.Errorf("%w: root folder ID is invalid", ErrInvalid)
	}
	if list.ID == "" {
		list.ID = rand.Text()
	}
	list.Error = truncate(list.Error, maxTextRunes)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Watchlist{}, dbError("save watchlist", err)
	}
	defer tx.Rollback(ctx)
	if err := validateReferences(ctx, tx, list.ProfileID, []string{list.RootID}); err != nil {
		return Watchlist{}, err
	}
	encoded, err := encode(list)
	if err != nil {
		return Watchlist{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO movie_watchlists (id, data, updated_at) VALUES ($1, $2::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, list.ID, string(encoded)); err != nil {
		return Watchlist{}, dbError("save watchlist", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Watchlist{}, dbError("save watchlist", err)
	}
	if list.IMDbIDs == nil {
		list.IMDbIDs = []string{}
	}
	return list, nil
}

func (s *Store) DeleteWatchlist(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM movie_watchlists WHERE id = $1`, strings.TrimSpace(id))
	if err != nil {
		return dbError("delete watchlist", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
