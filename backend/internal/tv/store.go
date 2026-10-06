package tv

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

var (
	errDatabase = errors.New("tv: database operation failed")
	errCorrupt  = errors.New("tv: saved record data is invalid")
)

const (
	// Distinct cross-process advisory lock for TV configuration writes.
	tvConfigLock int64 = 0x5456436F6E666967

	maxIDBytes       = 64
	maxTitleRunes    = 512
	maxTextRunes     = 1000
	maxReleaseBytes  = 512
	maxTemplateLen   = 512
	maxTokenBytes    = 32
	maxRoots         = 32
	maxPathBytes     = 4096
	historyLimit     = 200
	maxSeason        = 100
	maxEpisodeNumber = 1000
	maxEditSeries    = 500
	maxEditEpisodes  = 50000
	dateLayout       = "2006-01-02"
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
		return nil, errors.New("tv: a PostgreSQL pool is required")
	}
	if err := seedConfig(ctx, pool, defaults); err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func seedConfig(ctx context.Context, pool *pgxpool.Pool, defaults Config) error {
	defaults = normalizeConfig(defaults)
	encoded, err := encode(defaults)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return dbError("initialize TV settings", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`INSERT INTO tv_config (id, data) VALUES (true, $1::jsonb) ON CONFLICT (id) DO NOTHING`,
		string(encoded)); err != nil {
		return dbError("initialize TV settings", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("initialize TV settings", err)
	}
	return nil
}

func encode(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("tv: value could not be encoded")
	}
	return encoded, nil
}

func dbError(op string, cause error) error {
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		return fmt.Errorf("tv: %s: database error %s: %w", op, pgErr.Code, errDatabase)
	}
	return fmt.Errorf("tv: %s: %w", op, errDatabase)
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

func normalizeRoots(roots []movies.RootFolder) []movies.RootFolder {
	normalized := make([]movies.RootFolder, 0, len(roots))
	for _, root := range roots {
		path := strings.TrimSpace(root.Path)
		if path != "" {
			path = filepath.Clean(path)
		}
		normalized = append(normalized, movies.RootFolder{ID: strings.TrimSpace(root.ID), Path: path})
	}
	return normalized
}

func validDate(value string) bool {
	if len(value) != 10 {
		return false
	}
	parsed, err := time.Parse(dateLayout, value)
	return err == nil && parsed.Format(dateLayout) == value
}

func validRating(rating *float64) bool {
	return rating == nil || (!math.IsNaN(*rating) && !math.IsInf(*rating, 0) && *rating >= 0 && *rating <= 10)
}

func normalizeTime(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func normalizeFiles(files []movies.File) []movies.File {
	if files == nil {
		return []movies.File{}
	}
	for i := range files {
		files[i].RootID = strings.TrimSpace(files[i].RootID)
		files[i].Path = strings.TrimSpace(files[i].Path)
		files[i].Quality = strings.TrimSpace(files[i].Quality)
		if !files[i].ImportedAt.IsZero() {
			files[i].ImportedAt = files[i].ImportedAt.UTC()
		}
	}
	return files
}

func validateFiles(files []movies.File) error {
	for _, file := range files {
		if file.Path == "" || len(file.Path) > maxPathBytes || !filepath.IsLocal(file.Path) || strings.Contains(file.Path, "://") {
			return fmt.Errorf("%w: episode file paths must be local relative paths", ErrInvalid)
		}
		if file.Size <= 0 {
			return fmt.Errorf("%w: episode file sizes must be positive", ErrInvalid)
		}
		if file.RootID != "" && !validID(file.RootID) {
			return fmt.Errorf("%w: episode file root ID is invalid", ErrInvalid)
		}
	}
	return nil
}

func fileRootIDs(files []movies.File) []string {
	ids := make([]string, 0, len(files))
	for _, file := range files {
		if file.RootID != "" {
			ids = append(ids, file.RootID)
		}
	}
	return ids
}

func normalizeConfig(cfg Config) Config {
	cfg.RootFolders = normalizeRoots(cfg.RootFolders)
	cfg.FolderTemplate = strings.TrimSpace(cfg.FolderTemplate)
	cfg.FileTemplate = strings.TrimSpace(cfg.FileTemplate)
	cfg.ImportMode = strings.ToLower(strings.TrimSpace(cfg.ImportMode))
	return cfg
}

func loadConfig(ctx context.Context, q querier) (Config, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT data FROM tv_config WHERE id`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return Config{}, ErrNotFound
	}
	if err != nil {
		return Config{}, dbError("load TV settings", err)
	}
	var cfg Config
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, errCorrupt
		}
	}
	return normalizeConfig(cfg), nil
}

func (s *Store) Config(ctx context.Context) (Config, error) {
	return loadConfig(ctx, s.pool)
}

func (s *Store) SaveConfig(ctx context.Context, cfg Config) (Config, error) {
	cfg = normalizeConfig(cfg)
	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Config{}, dbError("save TV settings", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, tvConfigLock); err != nil {
		return Config{}, dbError("save TV settings", err)
	}
	current, err := loadConfig(ctx, tx)
	if errors.Is(err, ErrNotFound) {
		current = Config{}
	} else if err != nil {
		return Config{}, err
	}
	if err := validateRootChanges(ctx, tx, current, cfg); err != nil {
		return Config{}, err
	}
	encoded, err := encode(cfg)
	if err != nil {
		return Config{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO tv_config (id, data, updated_at) VALUES (true, $1::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, string(encoded)); err != nil {
		return Config{}, dbError("save TV settings", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Config{}, dbError("save TV settings", err)
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
		return fmt.Errorf("%w: RSS interval must be between 1 and 1440 minutes", ErrInvalid)
	}
	if cfg.SearchHours < 1 || cfg.SearchHours > 168 {
		return fmt.Errorf("%w: search interval must be between 1 and 168 hours", ErrInvalid)
	}
	if _, err := templateTokens("folder", cfg.FolderTemplate); err != nil {
		return err
	}
	fileTokens, err := templateTokens("file", cfg.FileTemplate)
	if err != nil {
		return err
	}
	// Without an episode-unique token every TV file name can collide on the series alone.
	if !fileTokens["episodeCode"] && !fileTokens["episode"] && !fileTokens["original"] {
		return fmt.Errorf("%w: file naming template must include {episodeCode}, {episode}, or {original}", ErrInvalid)
	}
	return nil
}

// tvNamingTokens mirrors the library importer's supported movie and episode tokens.
var tvNamingTokens = map[string]bool{
	"title": true, "year": true, "imdbId": true, "quality": true, "original": true, "part": true,
	"season": true, "episode": true, "episodeCode": true, "episodeTitle": true,
}

func templateTokens(label, value string) (map[string]bool, error) {
	if value == "" {
		return nil, fmt.Errorf("%w: %s naming template is required", ErrInvalid, label)
	}
	if len(value) > maxTemplateLen || !filepath.IsLocal(value) || strings.Contains(value, "://") || strings.Contains(value, `\`) {
		return nil, fmt.Errorf("%w: %s naming template must be a local relative path", ErrInvalid, label)
	}
	tokens := make(map[string]bool)
	for i := 0; i < len(value); {
		switch value[i] {
		case '{':
			end := strings.IndexByte(value[i+1:], '}')
			if end < 1 || end > maxTokenBytes {
				return nil, fmt.Errorf("%w: %s naming template has an invalid token", ErrInvalid, label)
			}
			name := value[i+1 : i+1+end]
			if !tvNamingTokens[name] {
				return nil, fmt.Errorf("%w: %s naming template has an unknown token {%s}", ErrInvalid, label, name)
			}
			tokens[name] = true
			i += end + 2
		case '}':
			return nil, fmt.Errorf("%w: %s naming template has an invalid token", ErrInvalid, label)
		default:
			r, size := utf8.DecodeRuneInString(value[i:])
			if r < ' ' || r == 0x7f {
				return nil, fmt.Errorf("%w: %s naming template has an invalid token", ErrInvalid, label)
			}
			i += size
		}
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("%w: %s naming template needs at least one {token}", ErrInvalid, label)
	}
	return tokens, nil
}

func validateRootChanges(ctx context.Context, q querier, current, next Config) error {
	nextPaths := make(map[string]string, len(next.RootFolders))
	for _, root := range next.RootFolders {
		nextPaths[root.ID] = root.Path
	}
	for _, root := range current.RootFolders {
		if path, ok := nextPaths[root.ID]; ok && path == root.Path {
			continue
		}
		used, err := rootReferenced(ctx, q, root)
		if err != nil {
			return err
		}
		if used {
			return fmt.Errorf("%w: root folder %q is used by a series, episode file, or import journal", ErrConflict, root.ID)
		}
	}
	return nil
}

func rootReferenced(ctx context.Context, q querier, root movies.RootFolder) (bool, error) {
	var used bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tv_series WHERE data->>'rootId' = $1)
		     OR EXISTS (SELECT 1 FROM tv_episodes
		                WHERE data->'files' @> jsonb_build_array(jsonb_build_object('rootId', $1::text)))
		     OR EXISTS (SELECT 1 FROM download_library_files WHERE root_path = $2)`,
		root.ID, root.Path).Scan(&used)
	if err != nil {
		return false, dbError("validate root folder", err)
	}
	return used, nil
}

func validateReferences(ctx context.Context, q querier, profileID string, rootIDs []string) error {
	// The shared lock keeps a concurrent config save from dropping a root this write references.
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, tvConfigLock); err != nil {
		return dbError("validate references", err)
	}
	if profileID != "" {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM movie_profiles WHERE id = $1)`, profileID).Scan(&exists); err != nil {
			return dbError("validate references", err)
		}
		if !exists {
			return fmt.Errorf("%w: quality profile does not exist", ErrInvalid)
		}
	}
	if len(rootIDs) == 0 {
		return nil
	}
	roots, err := savedRootIDs(ctx, q)
	if err != nil {
		return err
	}
	for _, id := range rootIDs {
		if id != "" && !roots[id] {
			return fmt.Errorf("%w: root folder does not exist", ErrInvalid)
		}
	}
	return nil
}

func savedRootIDs(ctx context.Context, q querier) (map[string]bool, error) {
	cfg, err := loadConfig(ctx, q)
	if errors.Is(err, ErrNotFound) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	roots := make(map[string]bool, len(cfg.RootFolders))
	for _, root := range cfg.RootFolders {
		roots[root.ID] = true
	}
	return roots, nil
}

type storedSeries struct {
	Metadata      metadata.Title `json:"metadata"`
	Monitored     bool           `json:"monitored"`
	MonitorMode   string         `json:"monitorMode"`
	ProfileID     string         `json:"profileId"`
	RootID        string         `json:"rootId"`
	Tags          []string       `json:"tags"`
	LastRefreshAt *time.Time     `json:"lastRefreshAt"`
	Error         string         `json:"error"`
}

func storedSeriesOf(series Series) storedSeries {
	return storedSeries{
		Metadata:      series.Metadata,
		Monitored:     series.Monitored,
		MonitorMode:   series.MonitorMode,
		ProfileID:     series.ProfileID,
		RootID:        series.RootID,
		Tags:          series.Tags,
		LastRefreshAt: series.LastRefreshAt,
		Error:         series.Error,
	}
}

func normalizeSeries(series *Series) {
	series.ID = strings.TrimSpace(series.ID)
	series.Metadata.IMDbID = strings.TrimSpace(series.Metadata.IMDbID)
	series.Metadata.Title = strings.TrimSpace(series.Metadata.Title)
	series.Metadata.Type = strings.TrimSpace(series.Metadata.Type)
	series.Metadata.Released = strings.TrimSpace(series.Metadata.Released)
	series.Metadata.Certification = strings.TrimSpace(series.Metadata.Certification)
	series.Metadata.Poster = strings.TrimSpace(series.Metadata.Poster)
	series.Metadata.Plot = strings.TrimSpace(series.Metadata.Plot)
	series.Metadata.Directors = normalizeList(series.Metadata.Directors)
	series.Metadata.Cast = normalizeList(series.Metadata.Cast)
	series.Metadata.Genres = normalizeList(series.Metadata.Genres)
	series.Metadata.Languages = normalizeList(series.Metadata.Languages)
	series.Metadata.Countries = normalizeList(series.Metadata.Countries)
	if series.Metadata.Type == "" {
		series.Metadata.Type = "series"
	}
	series.MonitorMode = strings.ToLower(strings.TrimSpace(series.MonitorMode))
	series.ProfileID = strings.TrimSpace(series.ProfileID)
	series.RootID = strings.TrimSpace(series.RootID)
	series.Tags = normalizeList(series.Tags)
	series.LastRefreshAt = normalizeTime(series.LastRefreshAt)
	series.Error = truncate(series.Error, maxTextRunes)
}

var monitorModes = map[string]bool{"all": true, "future": true, "missing": true, "existing": true, "first": true, "latest": true, "none": true}

func validateSeries(series Series) error {
	if series.Metadata.Title == "" {
		return fmt.Errorf("%w: a series title is required", ErrInvalid)
	}
	if utf8.RuneCountInString(series.Metadata.Title) > maxTitleRunes {
		return fmt.Errorf("%w: series title is too long", ErrInvalid)
	}
	if !validID(series.ID) {
		return fmt.Errorf("%w: series ID is invalid", ErrInvalid)
	}
	if series.Metadata.Type != "series" {
		return fmt.Errorf("%w: series metadata type must be series", ErrInvalid)
	}
	if _, ok := normalizeIMDb(series.Metadata.IMDbID); !ok {
		return fmt.Errorf("%w: IMDb ID must use the tt1234567 form", ErrInvalid)
	}
	if series.Metadata.Released != "" && !validDate(series.Metadata.Released) {
		return fmt.Errorf("%w: series release date must use YYYY-MM-DD", ErrInvalid)
	}
	if !validRating(series.Metadata.Rating) {
		return fmt.Errorf("%w: series rating must be between 0 and 10", ErrInvalid)
	}
	if series.MonitorMode != "" && !monitorModes[series.MonitorMode] {
		return fmt.Errorf("%w: monitor mode must be all, future, missing, existing, first, latest, or none", ErrInvalid)
	}
	if series.ProfileID != "" && !validID(series.ProfileID) {
		return fmt.Errorf("%w: quality profile ID is invalid", ErrInvalid)
	}
	if series.RootID != "" && !validID(series.RootID) {
		return fmt.Errorf("%w: root folder ID is invalid", ErrInvalid)
	}
	return nil
}

const seriesColumns = `id, coalesce(imdb_id, ''), coalesce(profile_id, ''), data, added_at, updated_at`

func scanSeries(row rowScanner) (Series, error) {
	var (
		series         Series
		id, imdbID     string
		profileID      string
		raw            []byte
		added, updated time.Time
	)
	if err := row.Scan(&id, &imdbID, &profileID, &raw, &added, &updated); err != nil {
		return Series{}, err
	}
	var stored storedSeries
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &stored); err != nil {
			return Series{}, errCorrupt
		}
	}
	series.ID = id
	series.Metadata = stored.Metadata
	series.Metadata.IMDbID = imdbID
	series.Monitored = stored.Monitored
	series.MonitorMode = stored.MonitorMode
	series.ProfileID = stored.ProfileID
	if series.ProfileID == "" {
		series.ProfileID = profileID
	}
	series.RootID = stored.RootID
	series.Tags = stored.Tags
	series.LastRefreshAt = stored.LastRefreshAt
	series.Error = stored.Error
	series.AddedAt, series.UpdatedAt = added, updated
	normalizeSeries(&series)
	return series, nil
}

func seriesByID(ctx context.Context, q querier, id string) (Series, error) {
	series, err := scanSeries(q.QueryRow(ctx, `SELECT `+seriesColumns+` FROM tv_series WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Series{}, ErrNotFound
	}
	if err != nil {
		return Series{}, dbError("load series", err)
	}
	return series, nil
}

func (s *Store) List(ctx context.Context) ([]Series, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+seriesColumns+` FROM tv_series ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, dbError("list series", err)
	}
	defer rows.Close()
	series := make([]Series, 0)
	for rows.Next() {
		item, err := scanSeries(rows)
		if err != nil {
			return nil, dbError("list series", err)
		}
		series = append(series, item)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("list series", err)
	}
	return series, nil
}

func (s *Store) Get(ctx context.Context, id string) (Series, error) {
	return seriesByID(ctx, s.pool, strings.TrimSpace(id))
}

func (s *Store) FindIMDb(ctx context.Context, imdbID string) (Series, error) {
	imdbID, ok := normalizeIMDb(imdbID)
	if !ok || imdbID == "" {
		return Series{}, ErrNotFound
	}
	series, err := scanSeries(s.pool.QueryRow(ctx, `SELECT `+seriesColumns+` FROM tv_series WHERE imdb_id = $1`, imdbID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Series{}, ErrNotFound
	}
	if err != nil {
		return Series{}, dbError("load series", err)
	}
	return series, nil
}

func (s *Store) Create(ctx context.Context, series Series) (Series, error) {
	normalizeSeries(&series)
	imdbID, ok := normalizeIMDb(series.Metadata.IMDbID)
	if !ok {
		return Series{}, fmt.Errorf("%w: IMDb ID must use the tt1234567 form", ErrInvalid)
	}
	series.Metadata.IMDbID = imdbID
	if series.ID == "" {
		series.ID = rand.Text()
	}
	if err := validateSeries(series); err != nil {
		return Series{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Series{}, dbError("create series", err)
	}
	defer tx.Rollback(ctx)
	rootIDs := []string{}
	if series.RootID != "" {
		rootIDs = append(rootIDs, series.RootID)
	}
	if err := validateReferences(ctx, tx, series.ProfileID, rootIDs); err != nil {
		return Series{}, err
	}
	encoded, err := encode(storedSeriesOf(series))
	if err != nil {
		return Series{}, err
	}
	created, err := scanSeries(tx.QueryRow(ctx,
		`INSERT INTO tv_series (id, imdb_id, profile_id, data, added_at, updated_at)
		 VALUES ($1, nullif($2, ''), nullif($3, ''), $4::jsonb, now(), now()) RETURNING `+seriesColumns,
		series.ID, imdbID, series.ProfileID, string(encoded)))
	if err != nil {
		if uniqueViolation(err) {
			return Series{}, ErrConflict
		}
		if missingReference(err) {
			return Series{}, fmt.Errorf("%w: quality profile does not exist", ErrInvalid)
		}
		return Series{}, dbError("create series", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Series{}, dbError("create series", err)
	}
	return created, nil
}

var seriesPatchFields = map[string]bool{
	"metadata": true, "monitored": true, "monitorMode": true, "profileId": true,
	"rootId": true, "tags": true, "lastRefreshAt": true, "error": true,
}

func (s *Store) Patch(ctx context.Context, id string, fields map[string]any) (Series, error) {
	id = strings.TrimSpace(id)
	if !validID(id) {
		return Series{}, ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Series{}, dbError("update series", err)
	}
	defer tx.Rollback(ctx)
	series, err := patchSeries(ctx, tx, id, fields)
	if err != nil {
		return Series{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Series{}, dbError("update series", err)
	}
	return series, nil
}

func patchSeries(ctx context.Context, q querier, id string, fields map[string]any) (Series, error) {
	for name := range fields {
		if !seriesPatchFields[name] {
			return Series{}, fmt.Errorf("%w: series field %q cannot be patched", ErrInvalid, name)
		}
	}
	body, err := encode(fields)
	if err != nil {
		return Series{}, err
	}
	series, err := scanSeries(q.QueryRow(ctx,
		`UPDATE tv_series SET data = data || $2::jsonb,
		     imdb_id = CASE WHEN ($2::jsonb -> 'metadata') ? 'imdbId'
		                    THEN nullif(lower((data || $2::jsonb) #>> '{metadata,imdbId}'), '') ELSE imdb_id END,
		     profile_id = CASE WHEN $2::jsonb ? 'profileId'
		                       THEN nullif((data || $2::jsonb) ->> 'profileId', '') ELSE profile_id END,
		     updated_at = now()
		 WHERE id = $1 RETURNING `+seriesColumns, id, string(body)))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Series{}, ErrNotFound
	case errors.Is(err, errCorrupt):
		return Series{}, fmt.Errorf("%w: series patch has an invalid field type", ErrInvalid)
	case err != nil:
		if uniqueViolation(err) {
			return Series{}, ErrConflict
		}
		if missingReference(err) {
			return Series{}, fmt.Errorf("%w: quality profile does not exist", ErrInvalid)
		}
		return Series{}, dbError("update series", err)
	}
	if err := validateSeries(series); err != nil {
		return Series{}, err
	}
	rootIDs := []string{}
	if series.RootID != "" {
		rootIDs = append(rootIDs, series.RootID)
	}
	if err := validateReferences(ctx, q, series.ProfileID, rootIDs); err != nil {
		return Series{}, err
	}
	encoded, err := encode(storedSeriesOf(series))
	if err != nil {
		return Series{}, err
	}
	if _, err := q.Exec(ctx, `UPDATE tv_series SET data = $2::jsonb WHERE id = $1`, series.ID, string(encoded)); err != nil {
		return Series{}, dbError("update series", err)
	}
	return series, nil
}

// Edit applies series patches and explicit episode monitor flags in one transaction.
func (s *Store) Edit(ctx context.Context, changes map[string]map[string]any, monitors map[string]bool) ([]Series, error) {
	if len(changes) > maxEditSeries {
		return nil, fmt.Errorf("%w: too many series in one edit", ErrInvalid)
	}
	if len(monitors) > maxEditEpisodes {
		return nil, fmt.Errorf("%w: too many episodes in one edit", ErrInvalid)
	}
	normalized := make(map[string]map[string]any, len(changes))
	seriesIDs := make([]string, 0, len(changes))
	for rawID, fields := range changes {
		id := strings.TrimSpace(rawID)
		if !validID(id) {
			return nil, fmt.Errorf("%w: series ID is invalid", ErrInvalid)
		}
		if _, exists := normalized[id]; exists {
			return nil, fmt.Errorf("%w: series IDs must be unique", ErrInvalid)
		}
		if fields == nil {
			fields = map[string]any{}
		}
		normalized[id] = fields
		seriesIDs = append(seriesIDs, id)
	}
	sort.Strings(seriesIDs)
	flags := make(map[string]bool, len(monitors))
	episodeIDs := make([]string, 0, len(monitors))
	for rawID, monitored := range monitors {
		id := strings.TrimSpace(rawID)
		if !validID(id) {
			return nil, fmt.Errorf("%w: episode ID is invalid", ErrInvalid)
		}
		if _, exists := flags[id]; exists {
			return nil, fmt.Errorf("%w: episode IDs must be unique", ErrInvalid)
		}
		flags[id] = monitored
		episodeIDs = append(episodeIDs, id)
	}
	sort.Strings(episodeIDs)
	if len(seriesIDs) == 0 && len(episodeIDs) == 0 {
		return []Series{}, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, dbError("edit series", err)
	}
	defer tx.Rollback(ctx)
	for _, id := range seriesIDs {
		var locked string
		switch err := tx.QueryRow(ctx, `SELECT id FROM tv_series WHERE id = $1 FOR UPDATE`, id).Scan(&locked); {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, fmt.Errorf("%w: series does not exist", ErrNotFound)
		case err != nil:
			return nil, dbError("edit series", err)
		}
	}
	if len(episodeIDs) > 0 {
		owned := make(map[string]bool, len(seriesIDs))
		for _, id := range seriesIDs {
			owned[id] = true
		}
		found := 0
		rows, err := tx.Query(ctx, `SELECT id, series_id FROM tv_episodes WHERE id = ANY($1) ORDER BY id FOR UPDATE`, episodeIDs)
		if err != nil {
			return nil, dbError("edit episodes", err)
		}
		for rows.Next() {
			var id, seriesID string
			if err := rows.Scan(&id, &seriesID); err != nil {
				rows.Close()
				return nil, dbError("edit episodes", err)
			}
			found++
			if !owned[seriesID] {
				rows.Close()
				return nil, fmt.Errorf("%w: episode %q does not belong to an edited series", ErrInvalid, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, dbError("edit episodes", err)
		}
		rows.Close()
		if found != len(episodeIDs) {
			return nil, fmt.Errorf("%w: episode does not exist", ErrNotFound)
		}
		values := make([]bool, len(episodeIDs))
		for i, id := range episodeIDs {
			values[i] = flags[id]
		}
		if _, err := tx.Exec(ctx,
			`UPDATE tv_episodes SET data = data || jsonb_build_object('monitored', flags.monitored)
			 FROM unnest($1::text[], $2::boolean[]) AS flags(id, monitored)
			 WHERE tv_episodes.id = flags.id`, episodeIDs, values); err != nil {
			return nil, dbError("edit episodes", err)
		}
	}
	edited := make([]Series, 0, len(seriesIDs))
	for _, id := range seriesIDs {
		series, err := patchSeries(ctx, tx, id, normalized[id])
		if err != nil {
			return nil, err
		}
		edited = append(edited, series)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, dbError("edit series", err)
	}
	return edited, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM tv_series WHERE id = $1`, strings.TrimSpace(id))
	if err != nil {
		return dbError("delete series", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type storedEpisode struct {
	IMDbID       string        `json:"imdbId"`
	Title        string        `json:"title"`
	Season       int           `json:"season"`
	Number       int           `json:"number"`
	AirDate      string        `json:"airDate"`
	Rating       *float64      `json:"rating"`
	Monitored    bool          `json:"monitored"`
	Files        []movies.File `json:"files"`
	LastSearchAt *time.Time    `json:"lastSearchAt"`
	Error        string        `json:"error"`
}

func storedEpisodeOf(episode Episode) storedEpisode {
	return storedEpisode{
		IMDbID:       episode.IMDbID,
		Title:        episode.Title,
		Season:       episode.Season,
		Number:       episode.Number,
		AirDate:      episode.AirDate,
		Rating:       episode.Rating,
		Monitored:    episode.Monitored,
		Files:        episode.Files,
		LastSearchAt: episode.LastSearchAt,
		Error:        episode.Error,
	}
}

func normalizeEpisode(episode *Episode) {
	episode.ID = strings.TrimSpace(episode.ID)
	episode.SeriesID = strings.TrimSpace(episode.SeriesID)
	episode.IMDbID = strings.TrimSpace(episode.IMDbID)
	episode.Title = strings.TrimSpace(episode.Title)
	episode.AirDate = strings.TrimSpace(episode.AirDate)
	episode.Files = normalizeFiles(episode.Files)
	episode.LastSearchAt = normalizeTime(episode.LastSearchAt)
	episode.Error = truncate(episode.Error, maxTextRunes)
}

func validateEpisode(episode Episode) error {
	if !validID(episode.SeriesID) {
		return fmt.Errorf("%w: episode series ID is required", ErrInvalid)
	}
	if episode.ID != "" && !validID(episode.ID) {
		return fmt.Errorf("%w: episode ID is invalid", ErrInvalid)
	}
	if utf8.RuneCountInString(episode.Title) > maxTitleRunes {
		return fmt.Errorf("%w: episode title is too long", ErrInvalid)
	}
	if _, ok := normalizeIMDb(episode.IMDbID); !ok {
		return fmt.Errorf("%w: episode IMDb ID must use the tt1234567 form", ErrInvalid)
	}
	if episode.Season < 0 || episode.Season > maxSeason {
		return fmt.Errorf("%w: episode season must be between 0 and %d", ErrInvalid, maxSeason)
	}
	if episode.Number < 1 || episode.Number > maxEpisodeNumber {
		return fmt.Errorf("%w: episode number must be between 1 and %d", ErrInvalid, maxEpisodeNumber)
	}
	if episode.AirDate != "" && !validDate(episode.AirDate) {
		return fmt.Errorf("%w: episode air date must use YYYY-MM-DD", ErrInvalid)
	}
	if !validRating(episode.Rating) {
		return fmt.Errorf("%w: episode rating must be between 0 and 10", ErrInvalid)
	}
	return validateFiles(episode.Files)
}

const episodeColumns = `id, series_id, season, number, data`

func scanEpisode(row rowScanner) (Episode, error) {
	var (
		episode Episode
		raw     []byte
	)
	if err := row.Scan(&episode.ID, &episode.SeriesID, &episode.Season, &episode.Number, &raw); err != nil {
		return Episode{}, err
	}
	var stored storedEpisode
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &stored); err != nil {
			return Episode{}, errCorrupt
		}
	}
	episode.IMDbID = stored.IMDbID
	episode.Title = stored.Title
	episode.AirDate = stored.AirDate
	episode.Rating = stored.Rating
	episode.Monitored = stored.Monitored
	episode.Files = stored.Files
	episode.LastSearchAt = stored.LastSearchAt
	episode.Error = stored.Error
	if episode.Files == nil {
		episode.Files = []movies.File{}
	}
	return episode, nil
}

func (s *Store) Episodes(ctx context.Context, seriesID string) ([]Episode, error) {
	seriesID = strings.TrimSpace(seriesID)
	query := `SELECT ` + episodeColumns + ` FROM tv_episodes`
	args := []any{}
	if seriesID != "" {
		if !validID(seriesID) {
			return nil, ErrNotFound
		}
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tv_series WHERE id = $1)`, seriesID).Scan(&exists); err != nil {
			return nil, dbError("list episodes", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
		query += ` WHERE series_id = $1 ORDER BY season, number, id`
		args = append(args, seriesID)
	} else {
		query += ` ORDER BY series_id, season, number, id`
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbError("list episodes", err)
	}
	defer rows.Close()
	episodes := make([]Episode, 0)
	for rows.Next() {
		episode, err := scanEpisode(rows)
		if err != nil {
			return nil, dbError("list episodes", err)
		}
		episodes = append(episodes, episode)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("list episodes", err)
	}
	return episodes, nil
}

func (s *Store) Episode(ctx context.Context, id string) (Episode, error) {
	id = strings.TrimSpace(id)
	if !validID(id) {
		return Episode{}, ErrNotFound
	}
	episode, err := scanEpisode(s.pool.QueryRow(ctx, `SELECT `+episodeColumns+` FROM tv_episodes WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Episode{}, ErrNotFound
	}
	if err != nil {
		return Episode{}, dbError("load episode", err)
	}
	return episode, nil
}

func (s *Store) UpsertEpisode(ctx context.Context, episode Episode) (Episode, error) {
	normalizeEpisode(&episode)
	imdbID, ok := normalizeIMDb(episode.IMDbID)
	if !ok {
		return Episode{}, fmt.Errorf("%w: episode IMDb ID must use the tt1234567 form", ErrInvalid)
	}
	episode.IMDbID = imdbID
	if episode.ID == "" {
		episode.ID = rand.Text()
	}
	if err := validateEpisode(episode); err != nil {
		return Episode{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Episode{}, dbError("save episode", err)
	}
	defer tx.Rollback(ctx)
	var seriesExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tv_series WHERE id = $1)`, episode.SeriesID).Scan(&seriesExists); err != nil {
		return Episode{}, dbError("save episode", err)
	}
	if !seriesExists {
		return Episode{}, fmt.Errorf("%w: series does not exist", ErrNotFound)
	}
	if err := validateReferences(ctx, tx, "", fileRootIDs(episode.Files)); err != nil {
		return Episode{}, err
	}
	encoded, err := encode(storedEpisodeOf(episode))
	if err != nil {
		return Episode{}, err
	}
	// A season/number conflict refreshes only metadata; user state stays untouched.
	stored, err := scanEpisode(tx.QueryRow(ctx,
		`INSERT INTO tv_episodes (id, series_id, season, number, data)
		 VALUES ($1, $2, $3, $4, $5::jsonb)
		 ON CONFLICT (series_id, season, number) DO UPDATE SET data = tv_episodes.data || jsonb_build_object(
		     'imdbId', EXCLUDED.data->'imdbId',
		     'title', EXCLUDED.data->'title',
		     'airDate', EXCLUDED.data->'airDate',
		     'rating', EXCLUDED.data->'rating')
		 RETURNING `+episodeColumns,
		episode.ID, episode.SeriesID, episode.Season, episode.Number, string(encoded)))
	switch {
	case err == nil:
	case uniqueViolation(err):
		return Episode{}, ErrConflict
	case missingReference(err):
		return Episode{}, fmt.Errorf("%w: series does not exist", ErrNotFound)
	default:
		return Episode{}, dbError("save episode", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Episode{}, dbError("save episode", err)
	}
	return stored, nil
}

var episodePatchFields = map[string]bool{"monitored": true, "files": true, "lastSearchAt": true, "error": true}

func (s *Store) PatchEpisode(ctx context.Context, id string, fields map[string]any) (Episode, error) {
	id = strings.TrimSpace(id)
	if !validID(id) {
		return Episode{}, ErrNotFound
	}
	for name := range fields {
		if !episodePatchFields[name] {
			return Episode{}, fmt.Errorf("%w: episode field %q cannot be patched", ErrInvalid, name)
		}
	}
	body, err := encode(fields)
	if err != nil {
		return Episode{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Episode{}, dbError("update episode", err)
	}
	defer tx.Rollback(ctx)
	episode, err := scanEpisode(tx.QueryRow(ctx,
		`UPDATE tv_episodes SET data = data || $2::jsonb WHERE id = $1 RETURNING `+episodeColumns, id, string(body)))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Episode{}, ErrNotFound
	case errors.Is(err, errCorrupt):
		return Episode{}, fmt.Errorf("%w: episode patch has an invalid field type", ErrInvalid)
	case err != nil:
		return Episode{}, dbError("update episode", err)
	}
	if err := validateEpisode(episode); err != nil {
		return Episode{}, err
	}
	if err := validateReferences(ctx, tx, "", fileRootIDs(episode.Files)); err != nil {
		return Episode{}, err
	}
	encoded, err := encode(storedEpisodeOf(episode))
	if err != nil {
		return Episode{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE tv_episodes SET data = $2::jsonb WHERE id = $1`, episode.ID, string(encoded)); err != nil {
		return Episode{}, dbError("update episode", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Episode{}, dbError("update episode", err)
	}
	return episode, nil
}

func (s *Store) Monitor(ctx context.Context, seriesID string, input MonitorInput) ([]Episode, error) {
	seriesID = strings.TrimSpace(seriesID)
	if !validID(seriesID) {
		return nil, ErrNotFound
	}
	if input.Season != nil && len(input.EpisodeIDs) > 0 {
		return nil, fmt.Errorf("%w: select a season or explicit episodes, not both", ErrInvalid)
	}
	if input.Season != nil && (*input.Season < 0 || *input.Season > maxSeason) {
		return nil, fmt.Errorf("%w: episode season must be between 0 and %d", ErrInvalid, maxSeason)
	}
	ids := normalizeList(input.EpisodeIDs)
	for _, id := range ids {
		if !validID(id) {
			return nil, fmt.Errorf("%w: episode IDs are invalid", ErrInvalid)
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, dbError("monitor episodes", err)
	}
	defer tx.Rollback(ctx)
	var seriesExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tv_series WHERE id = $1)`, seriesID).Scan(&seriesExists); err != nil {
		return nil, dbError("monitor episodes", err)
	}
	if !seriesExists {
		return nil, ErrNotFound
	}
	targetQuery := `SELECT id FROM tv_episodes WHERE series_id = $1`
	args := []any{seriesID}
	switch {
	case len(ids) > 0:
		targetQuery += ` AND id = ANY($2)`
		args = append(args, ids)
	case input.Season != nil:
		targetQuery += ` AND season = $2`
		args = append(args, *input.Season)
	}
	targetQuery += ` ORDER BY season, number, id FOR UPDATE`
	rows, err := tx.Query(ctx, targetQuery, args...)
	if err != nil {
		return nil, dbError("monitor episodes", err)
	}
	targets := make([]string, 0, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, dbError("monitor episodes", err)
		}
		targets = append(targets, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, dbError("monitor episodes", err)
	}
	rows.Close()
	if len(ids) > 0 && len(targets) != len(ids) {
		return nil, fmt.Errorf("%w: every episode must belong to the series", ErrInvalid)
	}
	if len(targets) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, dbError("monitor episodes", err)
		}
		return []Episode{}, nil
	}
	updated, err := tx.Query(ctx,
		`UPDATE tv_episodes SET data = jsonb_set(data, '{monitored}', to_jsonb($3::boolean))
		 WHERE series_id = $1 AND id = ANY($2) RETURNING `+episodeColumns, seriesID, targets, input.Monitored)
	if err != nil {
		return nil, dbError("monitor episodes", err)
	}
	episodes := make([]Episode, 0, len(targets))
	for updated.Next() {
		episode, err := scanEpisode(updated)
		if err != nil {
			updated.Close()
			return nil, dbError("monitor episodes", err)
		}
		episodes = append(episodes, episode)
	}
	if err := updated.Err(); err != nil {
		updated.Close()
		return nil, dbError("monitor episodes", err)
	}
	updated.Close()
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].Season != episodes[j].Season {
			return episodes[i].Season < episodes[j].Season
		}
		if episodes[i].Number != episodes[j].Number {
			return episodes[i].Number < episodes[j].Number
		}
		return episodes[i].ID < episodes[j].ID
	})
	if err := tx.Commit(ctx); err != nil {
		return nil, dbError("monitor episodes", err)
	}
	return episodes, nil
}

type acquisitionRelease struct {
	ReleaseID  string           `json:"releaseId"`
	Title      string           `json:"title"`
	Decision   quality.Decision `json:"decision"`
	Override   bool             `json:"override,omitempty"`
	EpisodeIDs []string         `json:"episodeIds"`
}

const acquisitionColumns = `a.series_id, a.job_id, a.release,
 CASE WHEN a.status IN ('imported','superseded','import-failed') THEN a.status
      WHEN d.status IN ('paused','cancelled') THEN d.status
      WHEN a.status IN ('paused','cancelled') THEN 'queued'
      ELSE a.status END, a.error`

func scanAcquisition(row rowScanner) (Acquisition, error) {
	var (
		acquisition Acquisition
		raw         []byte
	)
	if err := row.Scan(&acquisition.SeriesID, &acquisition.JobID, &raw, &acquisition.Status, &acquisition.Error); err != nil {
		return Acquisition{}, err
	}
	if len(raw) > 0 {
		var release acquisitionRelease
		if err := json.Unmarshal(raw, &release); err != nil {
			return Acquisition{}, errCorrupt
		}
		acquisition.ReleaseID, acquisition.Title = release.ReleaseID, release.Title
		acquisition.Decision, acquisition.Override = release.Decision, release.Override
		acquisition.EpisodeIDs = release.EpisodeIDs
	}
	if acquisition.Decision.Reasons == nil {
		acquisition.Decision.Reasons = []string{}
	}
	if acquisition.EpisodeIDs == nil {
		acquisition.EpisodeIDs = []string{}
	}
	return acquisition, nil
}

func (s *Store) Acquisitions(ctx context.Context) ([]Acquisition, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+acquisitionColumns+` FROM tv_acquisitions a JOIN downloads d ON d.id=a.job_id ORDER BY a.updated_at DESC, job_id`)
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
	acquisition.SeriesID = strings.TrimSpace(acquisition.SeriesID)
	acquisition.JobID = strings.TrimSpace(acquisition.JobID)
	acquisition.ReleaseID = strings.TrimSpace(acquisition.ReleaseID)
	acquisition.Status = strings.TrimSpace(acquisition.Status)
	if !validID(acquisition.SeriesID) || !validID(acquisition.JobID) {
		return fmt.Errorf("%w: acquisition series and job IDs are required", ErrInvalid)
	}
	if len(acquisition.ReleaseID) > maxReleaseBytes {
		return fmt.Errorf("%w: acquisition release ID is too long", ErrInvalid)
	}
	episodeIDs := normalizeList(acquisition.EpisodeIDs)
	if len(episodeIDs) == 0 {
		return fmt.Errorf("%w: an acquisition needs at least one episode", ErrInvalid)
	}
	for _, id := range episodeIDs {
		if !validID(id) {
			return fmt.Errorf("%w: acquisition episode IDs are invalid", ErrInvalid)
		}
	}
	acquisition.Error = truncate(acquisition.Error, maxTextRunes)
	acquisition.Title = truncate(strings.TrimSpace(acquisition.Title), maxTitleRunes)
	release, err := encode(acquisitionRelease{
		ReleaseID: acquisition.ReleaseID, Title: acquisition.Title, Decision: acquisition.Decision,
		Override: acquisition.Override, EpisodeIDs: episodeIDs,
	})
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError("save acquisition", err)
	}
	defer tx.Rollback(ctx)
	var mediaType string
	if err := tx.QueryRow(ctx, `SELECT media_type FROM downloads WHERE id = $1 FOR UPDATE`, acquisition.JobID).Scan(&mediaType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: download does not exist", ErrNotFound)
		}
		return dbError("claim TV download", err)
	}
	var seriesExists, movieOwned bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tv_series WHERE id = $1),
		        EXISTS (SELECT 1 FROM movie_acquisitions WHERE job_id = $2)`,
		acquisition.SeriesID, acquisition.JobID).Scan(&seriesExists, &movieOwned); err != nil {
		return dbError("save acquisition", err)
	}
	if !seriesExists {
		return fmt.Errorf("%w: series does not exist", ErrNotFound)
	}
	// The claimed media type outlives deleted catalogs, so a job cannot switch media.
	if mediaType == "movie" || movieOwned {
		return fmt.Errorf("%w: download already belongs to a movie", ErrConflict)
	}
	var owner string
	switch err := tx.QueryRow(ctx, `SELECT series_id FROM tv_acquisitions WHERE job_id = $1`, acquisition.JobID).Scan(&owner); {
	case err == nil && owner != acquisition.SeriesID:
		return fmt.Errorf("%w: download already belongs to another series", ErrConflict)
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return dbError("save acquisition", err)
	}
	var matched int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM tv_episodes WHERE series_id = $1 AND id = ANY($2)`,
		acquisition.SeriesID, episodeIDs).Scan(&matched); err != nil {
		return dbError("save acquisition", err)
	}
	if matched != len(episodeIDs) {
		return fmt.Errorf("%w: every acquisition episode must belong to the series", ErrInvalid)
	}
	tag, err := tx.Exec(ctx,
		`INSERT INTO tv_acquisitions (job_id, series_id, release, status, error, updated_at)
		 VALUES ($1, $2, $3::jsonb, $4, $5, now())
		 ON CONFLICT (job_id) DO UPDATE SET release = EXCLUDED.release, status = EXCLUDED.status,
		 error = EXCLUDED.error, updated_at = now()
		 WHERE tv_acquisitions.series_id = EXCLUDED.series_id`,
		acquisition.JobID, acquisition.SeriesID, string(release), acquisition.Status, acquisition.Error)
	if err != nil {
		if missingReference(err) {
			return fmt.Errorf("%w: series, download, or episode does not exist", ErrNotFound)
		}
		return dbError("save acquisition", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("save acquisition", err)
	}
	return nil
}

func (s *Store) Event(ctx context.Context, seriesID, episodeID, kind, message string) error {
	seriesID = strings.TrimSpace(seriesID)
	episodeID = strings.TrimSpace(episodeID)
	kind = strings.TrimSpace(kind)
	if !validID(seriesID) || kind == "" || utf8.RuneCountInString(kind) > maxIDBytes {
		return fmt.Errorf("%w: series ID and event type are required", ErrInvalid)
	}
	if episodeID != "" && !validID(episodeID) {
		return fmt.Errorf("%w: episode ID is invalid", ErrInvalid)
	}
	var seriesExists, episodeExists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tv_series WHERE id = $1),
		        $2 = '' OR EXISTS (SELECT 1 FROM tv_episodes WHERE id = $2 AND series_id = $1)`,
		seriesID, episodeID).Scan(&seriesExists, &episodeExists); err != nil {
		return dbError("record series event", err)
	}
	if !seriesExists {
		return fmt.Errorf("%w: series does not exist", ErrNotFound)
	}
	if !episodeExists {
		return fmt.Errorf("%w: episode does not belong to the series", ErrInvalid)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO tv_history (series_id, episode_id, type, message) VALUES ($1, $2, $3, $4)`,
		seriesID, episodeID, kind, truncate(message, maxTextRunes)); err != nil {
		if missingReference(err) {
			return fmt.Errorf("%w: series does not exist", ErrNotFound)
		}
		return dbError("record series event", err)
	}
	return nil
}

func (s *Store) History(ctx context.Context, seriesID string) ([]History, error) {
	seriesID = strings.TrimSpace(seriesID)
	query := `SELECT id, series_id, episode_id, type, message, created_at FROM tv_history ORDER BY id DESC LIMIT $1`
	args := []any{historyLimit}
	if seriesID != "" {
		if !validID(seriesID) {
			return nil, ErrNotFound
		}
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tv_series WHERE id = $1)`, seriesID).Scan(&exists); err != nil {
			return nil, dbError("load series history", err)
		}
		if !exists {
			return nil, ErrNotFound
		}
		query = `SELECT id, series_id, episode_id, type, message, created_at FROM tv_history
		 WHERE series_id = $1 ORDER BY id DESC LIMIT $2`
		args = []any{seriesID, historyLimit}
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbError("load series history", err)
	}
	defer rows.Close()
	history := make([]History, 0)
	for rows.Next() {
		var event History
		if err := rows.Scan(&event.ID, &event.SeriesID, &event.EpisodeID, &event.Type, &event.Message, &event.CreatedAt); err != nil {
			return nil, dbError("load series history", err)
		}
		history = append(history, event)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("load series history", err)
	}
	return history, nil
}

func (s *Store) Block(ctx context.Context, seriesID, releaseID string) error {
	seriesID = strings.TrimSpace(seriesID)
	releaseID = strings.TrimSpace(releaseID)
	if !validID(seriesID) || releaseID == "" || len(releaseID) > maxReleaseBytes {
		return fmt.Errorf("%w: a series and release are required", ErrInvalid)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tv_blocklist (series_id, release_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, seriesID, releaseID)
	if err != nil {
		if missingReference(err) {
			return fmt.Errorf("%w: series does not exist", ErrNotFound)
		}
		return dbError("block release", err)
	}
	return nil
}

func (s *Store) Blocked(ctx context.Context, seriesID, releaseID string) (bool, error) {
	seriesID = strings.TrimSpace(seriesID)
	releaseID = strings.TrimSpace(releaseID)
	if !validID(seriesID) || releaseID == "" || len(releaseID) > maxReleaseBytes {
		return false, fmt.Errorf("%w: a series and release are required", ErrInvalid)
	}
	var blocked bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM tv_blocklist WHERE series_id = $1 AND release_id = $2)`,
		seriesID, releaseID).Scan(&blocked); err != nil {
		return false, dbError("load blocked release", err)
	}
	return blocked, nil
}

func (s *Store) Unblock(ctx context.Context, seriesID, releaseID string) error {
	seriesID = strings.TrimSpace(seriesID)
	releaseID = strings.TrimSpace(releaseID)
	if !validID(seriesID) || releaseID == "" || len(releaseID) > maxReleaseBytes {
		return fmt.Errorf("%w: a series and release are required", ErrInvalid)
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM tv_blocklist WHERE series_id = $1 AND release_id = $2`, seriesID, releaseID)
	if err != nil {
		return dbError("unblock release", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TaskDue atomically claims a due automation run so concurrent workers cannot both start it.
func (s *Store) TaskDue(ctx context.Context, name string, interval time.Duration) (bool, error) {
	name = strings.TrimSpace(name)
	if !validID(name) || interval <= 0 {
		return false, fmt.Errorf("%w: a task name and positive interval are required", ErrInvalid)
	}
	var claimed bool
	err := s.pool.QueryRow(ctx,
		`INSERT INTO tv_automation (name, last_run) VALUES ($1, now())
		 ON CONFLICT (name) DO UPDATE SET last_run = now()
		 WHERE tv_automation.last_run <= now() - make_interval(secs => $2)
		 RETURNING true`, name, interval.Seconds()).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, dbError("claim TV task", err)
	}
	return claimed, nil
}

func (s *Store) MarkTask(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if !validID(name) {
		return fmt.Errorf("%w: a task name is required", ErrInvalid)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO tv_automation (name, last_run) VALUES ($1, now())
		 ON CONFLICT (name) DO UPDATE SET last_run = now()`, name); err != nil {
		return dbError("record TV task", err)
	}
	return nil
}
