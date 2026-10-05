package subtitles

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

var errDatabase = errors.New("subtitles: database operation failed")

type rowScanner interface {
	Scan(dest ...any) error
}

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Store persists subtitle configuration, inventory, wanted state, jobs, and history.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(ctx context.Context, pool *pgxpool.Pool, defaults Config) (*Store, error) {
	if pool == nil {
		return nil, errors.New("subtitles: a PostgreSQL pool is required")
	}
	store := &Store{pool: pool}
	if err := store.seed(ctx, defaults); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) seed(ctx context.Context, defaults Config) error {
	encoded, err := encode(defaults)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO subtitle_config (id, data) VALUES (true, $1::jsonb) ON CONFLICT (id) DO NOTHING`, string(encoded)); err != nil {
		return dbError("initialize subtitles", err)
	}
	profile := Profile{
		ID:        "default",
		Name:      "Default",
		Languages: []LanguagePreference{{Code: "en"}},
		Cutoff:    1,
	}
	encodedProfile, err := encode(profile)
	if err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO subtitle_profiles (id, data) VALUES ($1, $2::jsonb) ON CONFLICT (id) DO NOTHING`,
		profile.ID, string(encodedProfile)); err != nil {
		return dbError("initialize subtitle profiles", err)
	}
	return nil
}

func (s *Store) Config(ctx context.Context) (Config, error) {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT data FROM subtitle_config WHERE id`).Scan(&raw); err != nil {
		return Config{}, dbError("load subtitle config", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, dbError("decode subtitle config", err)
	}
	cfg.Providers = append([]Provider(nil), cfg.Providers...)
	return applyConfigDefaults(cfg), nil
}

// applyConfigDefaults fills zero fields from older rows so a fresh seed and a loaded row behave identically.
func applyConfigDefaults(cfg Config) Config {
	defaults := defaultConfig()
	fillInt := func(value *int, fallback int) {
		if *value == 0 {
			*value = fallback
		}
	}
	fillFloat := func(value *float64, fallback float64) {
		if *value == 0 {
			*value = fallback
		}
	}
	fillInt(&cfg.ScanMinutes, defaults.ScanMinutes)
	fillInt(&cfg.SearchIntervalHours, defaults.SearchIntervalHours)
	fillInt(&cfg.RetryMinutes, defaults.RetryMinutes)
	fillInt(&cfg.CutoffScore, defaults.CutoffScore)
	fillInt(&cfg.ProviderTimeoutSeconds, defaults.ProviderTimeoutSeconds)
	if cfg.DefaultProfileID == "" {
		cfg.DefaultProfileID = defaults.DefaultProfileID
	}
	if len(cfg.Providers) == 0 {
		cfg.Providers = defaults.Providers
	}
	fillInt(&cfg.Sync.TimeoutSeconds, defaults.Sync.TimeoutSeconds)
	fillFloat(&cfg.Sync.MaxOffsetSeconds, defaults.Sync.MaxOffsetSeconds)
	fillFloat(&cfg.Sync.QualityMaxOffsetSecs, defaults.Sync.QualityMaxOffsetSecs)
	fillFloat(&cfg.Sync.MaxFramerateDeviation, defaults.Sync.MaxFramerateDeviation)
	fillInt(&cfg.Sync.AudioReferenceSeconds, defaults.Sync.AudioReferenceSeconds)
	fillInt(&cfg.Sync.MaxEmbeddedStreamIndex, defaults.Sync.MaxEmbeddedStreamIndex)
	fillInt(&cfg.AI.TimeoutSeconds, defaults.AI.TimeoutSeconds)
	fillInt(&cfg.AI.MaxTokens, defaults.AI.MaxTokens)
	fillInt(&cfg.AI.MaxRequests, defaults.AI.MaxRequests)
	fillInt(&cfg.AI.MaxTotalTokens, defaults.AI.MaxTotalTokens)
	fillInt(&cfg.AI.MaxCharacters, defaults.AI.MaxCharacters)
	fillFloat(&cfg.AI.Temperature, defaults.AI.Temperature)
	return cfg
}

// SaveConfig stores configuration while keeping previously saved secrets when the input leaves them blank.
func (s *Store) SaveConfig(ctx context.Context, cfg Config) (Config, error) {
	current, err := s.Config(ctx)
	if err != nil {
		return Config{}, err
	}
	for i := range cfg.Providers {
		cfg.Providers[i].PasswordSet, cfg.Providers[i].APIKeySet = false, false
		if previous, ok := findProvider(current, cfg.Providers[i].ID); ok {
			if cfg.Providers[i].Password == "" {
				cfg.Providers[i].Password = previous.Password
			}
			if cfg.Providers[i].APIKey == "" {
				cfg.Providers[i].APIKey = previous.APIKey
			}
		}
	}
	if cfg.AI.APIKey == "" {
		cfg.AI.APIKey = current.AI.APIKey
	}
	cfg.AI.APIKeySet = false
	encoded, err := encode(cfg)
	if err != nil {
		return Config{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO subtitle_config (id, data, updated_at) VALUES (true, $1::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`, string(encoded)); err != nil {
		return Config{}, dbError("save subtitle config", err)
	}
	return cfg, nil
}

func findProvider(cfg Config, id string) (Provider, bool) {
	for _, provider := range cfg.Providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return Provider{}, false
}

func (s *Store) Profiles(ctx context.Context) ([]Profile, error) {
	rows, err := s.pool.Query(ctx, `SELECT data FROM subtitle_profiles ORDER BY id`)
	if err != nil {
		return nil, dbError("list subtitle profiles", err)
	}
	defer rows.Close()
	profiles := []Profile{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, dbError("list subtitle profiles", err)
		}
		var profile Profile
		if err := json.Unmarshal(raw, &profile); err != nil {
			return nil, dbError("decode subtitle profile", err)
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) Profile(ctx context.Context, id string) (Profile, error) {
	rows, err := s.Profiles(ctx)
	if err != nil {
		return Profile{}, err
	}
	for _, profile := range rows {
		if profile.ID == id {
			return profile, nil
		}
	}
	return Profile{}, ErrNotFound
}

func (s *Store) SaveProfile(ctx context.Context, profile Profile) (Profile, error) {
	encoded, err := encode(profile)
	if err != nil {
		return Profile{}, err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO subtitle_profiles (id, data, updated_at) VALUES ($1, $2::jsonb, now())
		 ON CONFLICT (id) DO UPDATE SET data = EXCLUDED.data, updated_at = now()`,
		profile.ID, string(encoded)); err != nil {
		return Profile{}, dbError("save subtitle profile", err)
	}
	return profile, nil
}

func (s *Store) DeleteProfile(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM subtitle_profiles WHERE id = $1`, id)
	if err != nil {
		return dbError("delete subtitle profile", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Assignments returns per-video profile bindings keyed by kind and id.
func (s *Store) Assignments(ctx context.Context) (map[string]Assignment, error) {
	rows, err := s.pool.Query(ctx, `SELECT video_kind, video_id, profile_id, monitored FROM subtitle_assignments`)
	if err != nil {
		return nil, dbError("list subtitle assignments", err)
	}
	defer rows.Close()
	out := map[string]Assignment{}
	for rows.Next() {
		var item Assignment
		if err := rows.Scan(&item.Kind, &item.VideoID, &item.ProfileID, &item.Monitored); err != nil {
			return nil, dbError("list subtitle assignments", err)
		}
		out[videoKey(item.Kind, item.VideoID)] = item
	}
	return out, rows.Err()
}

// SetAssignment stores one video's language profile binding.
func (s *Store) SetAssignment(ctx context.Context, item Assignment) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO subtitle_assignments (video_kind, video_id, profile_id, monitored, updated_at)
		 VALUES ($1, $2, $3, $4, now())
		 ON CONFLICT (video_kind, video_id) DO UPDATE
		 SET profile_id = EXCLUDED.profile_id, monitored = EXCLUDED.monitored, updated_at = now()`,
		item.Kind, item.VideoID, item.ProfileID, item.Monitored)
	if err != nil {
		return dbError("save subtitle assignment", err)
	}
	return nil
}

// DeleteAssignment reverts a video to the default profile and monitoring.
func (s *Store) DeleteAssignment(ctx context.Context, kind, id string) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM subtitle_assignments WHERE video_kind = $1 AND video_id = $2`, kind, id); err != nil {
		return dbError("delete subtitle assignment", err)
	}
	return nil
}

// Inventory returns persisted sidecars grouped by kind and id.
func (s *Store) Inventory(ctx context.Context) (map[string][]Sidecar, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT video_kind, video_id, rel_path, language, format, forced, hi, source, size, updated_at
		 FROM subtitle_sidecars ORDER BY video_kind, video_id, rel_path`)
	if err != nil {
		return nil, dbError("list subtitle sidecars", err)
	}
	defer rows.Close()
	out := map[string][]Sidecar{}
	for rows.Next() {
		var item Sidecar
		if err := rows.Scan(&item.Kind, &item.VideoID, &item.Path, &item.Language, &item.Format,
			&item.Forced, &item.HI, &item.Source, &item.Size, &item.UpdatedAt); err != nil {
			return nil, dbError("list subtitle sidecars", err)
		}
		key := videoKey(item.Kind, item.VideoID)
		out[key] = append(out[key], item)
	}
	return out, rows.Err()
}

// ReplaceSidecars reconciles the stored sidecar list for one video with the files found on disk.
func (s *Store) ReplaceSidecars(ctx context.Context, kind, id string, sidecars []Sidecar) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError("save subtitle sidecars", err)
	}
	defer tx.Rollback(ctx)
	paths := make([]string, 0, len(sidecars))
	for _, sidecar := range sidecars {
		paths = append(paths, sidecar.Path)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM subtitle_sidecars WHERE video_kind = $1 AND video_id = $2 AND NOT (rel_path = ANY($3::text[]))`,
		kind, id, paths); err != nil {
		return dbError("save subtitle sidecars", err)
	}
	for _, sidecar := range sidecars {
		if _, err := tx.Exec(ctx,
			`INSERT INTO subtitle_sidecars (video_kind, video_id, rel_path, root_id, root_path, language, format, forced, hi, source, size, updated_at)
			 VALUES ($1, $2, $3, '', '', $4, $5, $6, $7, $8, $9, now())
			 ON CONFLICT (video_kind, video_id, rel_path) DO UPDATE
			 SET language = EXCLUDED.language, format = EXCLUDED.format, forced = EXCLUDED.forced,
			     hi = EXCLUDED.hi, source = EXCLUDED.source, size = EXCLUDED.size, updated_at = now()`,
			kind, id, sidecar.Path, sidecar.Language, sidecar.Format, sidecar.Forced, sidecar.HI, sidecar.Source, sidecar.Size); err != nil {
			return dbError("save subtitle sidecars", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("save subtitle sidecars", err)
	}
	return nil
}

func (s *Store) UpsertSidecar(ctx context.Context, sidecar Sidecar) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError("save subtitle sidecar", err)
	}
	defer tx.Rollback(ctx)
	if err := upsertSidecar(ctx, tx, sidecar); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("save subtitle sidecar", err)
	}
	return nil
}

func upsertSidecar(ctx context.Context, tx pgx.Tx, sidecar Sidecar) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO subtitle_sidecars (video_kind, video_id, rel_path, language, format, forced, hi, source, size, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		 ON CONFLICT (video_kind, video_id, rel_path) DO UPDATE
		 SET language = EXCLUDED.language, format = EXCLUDED.format, forced = EXCLUDED.forced,
		     hi = EXCLUDED.hi, source = EXCLUDED.source, size = EXCLUDED.size, updated_at = now()`,
		sidecar.Kind, sidecar.VideoID, sidecar.Path, sidecar.Language, sidecar.Format,
		sidecar.Forced, sidecar.HI, sidecar.Source, sidecar.Size); err != nil {
		return dbError("save subtitle sidecar", err)
	}
	return nil
}

func (s *Store) DeleteSidecar(ctx context.Context, kind, id, rel string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM subtitle_sidecars WHERE video_kind = $1 AND video_id = $2 AND rel_path = $3`, kind, id, rel)
	if err != nil {
		return dbError("delete subtitle sidecar", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Wanted returns persisted wanted rows grouped by kind and id.
func (s *Store) Wanted(ctx context.Context) (map[string][]Wanted, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT video_kind, video_id, language, forced, hi, status, attempts, last_attempt_at, next_attempt_at, error
		 FROM subtitle_wanted ORDER BY video_kind, video_id, language`)
	if err != nil {
		return nil, dbError("list wanted subtitles", err)
	}
	defer rows.Close()
	out := map[string][]Wanted{}
	for rows.Next() {
		var item Wanted
		if err := rows.Scan(&item.Kind, &item.VideoID, &item.Language, &item.Forced, &item.HI,
			&item.Status, &item.Attempts, &item.LastAttemptAt, &item.NextAttemptAt, &item.Error); err != nil {
			return nil, dbError("list wanted subtitles", err)
		}
		key := videoKey(item.Kind, item.VideoID)
		out[key] = append(out[key], item)
	}
	return out, rows.Err()
}

// ReplaceWanted reconciles wanted rows while preserving attempt counters and backoff.
func (s *Store) ReplaceWanted(ctx context.Context, kind, id string, rows []Wanted) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError("save wanted subtitles", err)
	}
	defer tx.Rollback(ctx)
	type key struct {
		language string
		forced   bool
		hi       bool
	}
	keys := make([]key, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, key{row.Language, row.Forced, row.HI})
	}
	if len(keys) == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM subtitle_wanted WHERE video_kind = $1 AND video_id = $2`, kind, id); err != nil {
			return dbError("save wanted subtitles", err)
		}
	} else {
		languages := make([]string, 0, len(keys))
		forced := make([]bool, 0, len(keys))
		hi := make([]bool, 0, len(keys))
		for _, k := range keys {
			languages = append(languages, k.language)
			forced = append(forced, k.forced)
			hi = append(hi, k.hi)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM subtitle_wanted WHERE video_kind = $1 AND video_id = $2
			 AND NOT ((language, forced, hi) IN (SELECT * FROM unnest($3::text[], $4::bool[], $5::bool[])))`,
			kind, id, languages, forced, hi); err != nil {
			return dbError("save wanted subtitles", err)
		}
	}
	for _, row := range rows {
		if _, err := tx.Exec(ctx,
			`INSERT INTO subtitle_wanted (video_kind, video_id, language, forced, hi, status, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, now())
			 ON CONFLICT (video_kind, video_id, language, forced, hi) DO UPDATE
			 SET status = EXCLUDED.status, updated_at = now()`,
			kind, id, row.Language, row.Forced, row.HI, row.Status); err != nil {
			return dbError("save wanted subtitles", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("save wanted subtitles", err)
	}
	return nil
}

// DueWanted lists wanted variants whose retry backoff has elapsed.
func (s *Store) DueWanted(ctx context.Context, limit int) ([]Wanted, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT video_kind, video_id, language, forced, hi, status, attempts, last_attempt_at, next_attempt_at, error
		 FROM subtitle_wanted
		 WHERE status = 'wanted' AND (next_attempt_at IS NULL OR next_attempt_at <= now())
		 ORDER BY next_attempt_at NULLS FIRST, updated_at
		 LIMIT $1`, limit)
	if err != nil {
		return nil, dbError("list due subtitles", err)
	}
	defer rows.Close()
	out := []Wanted{}
	for rows.Next() {
		var item Wanted
		if err := rows.Scan(&item.Kind, &item.VideoID, &item.Language, &item.Forced, &item.HI,
			&item.Status, &item.Attempts, &item.LastAttemptAt, &item.NextAttemptAt, &item.Error); err != nil {
			return nil, dbError("list due subtitles", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkWantedAttempt records one search attempt with its next retry time.
func (s *Store) MarkWantedAttempt(ctx context.Context, item Wanted, status, message string, next time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE subtitle_wanted SET attempts = attempts + 1, last_attempt_at = now(), next_attempt_at = $6,
		 status = $5, error = $7, updated_at = now()
		 WHERE video_kind = $1 AND video_id = $2 AND language = $3 AND forced = $4 AND hi = $8`,
		item.Kind, item.VideoID, item.Language, item.Forced, status, next, truncate(message, maxTextRunes), item.HI)
	if err != nil {
		return dbError("update wanted subtitles", err)
	}
	return nil
}

// SetWantedStatus updates one variant's status, for example after a successful download.
func (s *Store) SetWantedStatus(ctx context.Context, kind, id, language string, forced, hi bool, status string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE subtitle_wanted SET status = $6, error = '', updated_at = now()
		 WHERE video_kind = $1 AND video_id = $2 AND language = $3 AND forced = $4 AND hi = $5`,
		kind, id, language, forced, hi, status)
	if err != nil {
		return dbError("update wanted subtitles", err)
	}
	return nil
}

func (s *Store) Event(ctx context.Context, entry HistoryEntry) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO subtitle_history (video_kind, video_id, action, language, message) VALUES ($1, $2, $3, $4, $5)`,
		entry.Kind, entry.VideoID, entry.Action, entry.Language, truncate(entry.Message, maxTextRunes))
	if err != nil {
		return dbError("save subtitle history", err)
	}
	return nil
}

func (s *Store) History(ctx context.Context, kind, id string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	query := `SELECT id, video_kind, video_id, action, language, message, created_at FROM subtitle_history`
	args := []any{}
	if kind != "" && id != "" {
		query += ` WHERE video_kind = $1 AND video_id = $2`
		args = append(args, kind, id)
	} else if kind != "" {
		query += ` WHERE video_kind = $1`
		args = append(args, kind)
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbError("list subtitle history", err)
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var entry HistoryEntry
		if err := rows.Scan(&entry.ID, &entry.Kind, &entry.VideoID, &entry.Action, &entry.Language, &entry.Message, &entry.CreatedAt); err != nil {
			return nil, dbError("list subtitle history", err)
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

func (s *Store) RecordFailure(ctx context.Context, kind, id, language, provider, message string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO subtitle_failures (video_kind, video_id, language, provider, message) VALUES ($1, $2, $3, $4, $5)`,
		kind, id, language, provider, truncate(message, maxTextRunes))
	if err != nil {
		return dbError("save subtitle failure", err)
	}
	return nil
}

// CreateJob stores a queued durable job with its bounded payload.
func (s *Store) CreateJob(ctx context.Context, job Job, payload []byte) (Job, error) {
	if job.ID == "" {
		job.ID = "sub-" + rand.Text()
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	if len(payload) > maxPayloadBytes {
		return Job{}, fmt.Errorf("%w: job payload is too large", ErrInvalid)
	}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO subtitle_jobs (id, video_kind, video_id, kind, language, status, detail, payload)
		 VALUES ($1, $2, $3, $4, $5, 'queued', $6, $7::jsonb)
		 RETURNING created_at, updated_at`,
		job.ID, job.VideoKind, job.VideoID, job.Kind, job.Language, job.Detail, string(payload)).
		Scan(&job.CreatedAt, &job.UpdatedAt); err != nil {
		return Job{}, dbError("create subtitle job", err)
	}
	job.Status = "queued"
	return job, nil
}

func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	var job Job
	err := s.pool.QueryRow(ctx,
		`SELECT id, kind, video_kind, video_id, language, status, progress, detail, error, created_at, updated_at
		 FROM subtitle_jobs WHERE id = $1`, id).
		Scan(&job.ID, &job.Kind, &job.VideoKind, &job.VideoID, &job.Language, &job.Status,
			&job.Progress, &job.Detail, &job.Error, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, dbError("load subtitle job", err)
	}
	return job, nil
}

func (s *Store) JobPayload(ctx context.Context, id string, dst any) error {
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT payload FROM subtitle_jobs WHERE id = $1`, id).Scan(&raw); err != nil {
		return dbError("load subtitle job payload", err)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%w: stored job payload is invalid", ErrInvalid)
	}
	return nil
}

func (s *Store) Jobs(ctx context.Context, limit int, activeOnly bool) ([]Job, error) {
	if limit <= 0 || limit > maxJobLimit {
		limit = maxJobLimit
	}
	query := `SELECT id, kind, video_kind, video_id, language, status, progress, detail, error, created_at, updated_at FROM subtitle_jobs`
	if activeOnly {
		query += ` WHERE status IN ('queued', 'running')`
	}
	query += fmt.Sprintf(` ORDER BY created_at DESC LIMIT %d`, limit)
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, dbError("list subtitle jobs", err)
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var job Job
		if err := rows.Scan(&job.ID, &job.Kind, &job.VideoKind, &job.VideoID, &job.Language, &job.Status,
			&job.Progress, &job.Detail, &job.Error, &job.CreatedAt, &job.UpdatedAt); err != nil {
			return nil, dbError("list subtitle jobs", err)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// StartJob marks a queued job running; false means it was cancelled or already claimed.
func (s *Store) StartJob(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE subtitle_jobs SET status = 'running', progress = 5, detail = '', error = '', updated_at = now()
		 WHERE id = $1 AND status = 'queued'`, id)
	if err != nil {
		return false, dbError("start subtitle job", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) UpdateJob(ctx context.Context, id, status string, progress int, detail, jobError string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE subtitle_jobs SET status = $2, progress = $3, detail = $4, error = $5, updated_at = now() WHERE id = $1`,
		id, status, progress, truncate(detail, maxTextRunes), truncate(jobError, maxTextRunes))
	if err != nil {
		return dbError("update subtitle job", err)
	}
	return nil
}

// CancelJob marks a pending job cancelled so a queued job is never executed later.
func (s *Store) CancelJob(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE subtitle_jobs SET status = 'cancelled', detail = 'cancelled', updated_at = now()
		 WHERE id = $1 AND status IN ('queued', 'running')`, id)
	if err != nil {
		return dbError("cancel subtitle job", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.Job(ctx, id); err != nil {
			return err
		}
		return fmt.Errorf("%w: job is already finished", ErrConflict)
	}
	return nil
}

// RecoverJobs requeues jobs left running by a previous process.
func (s *Store) RecoverJobs(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE subtitle_jobs SET status = 'queued', progress = 0, detail = 'requeued after restart', updated_at = now()
		 WHERE status = 'running'`)
	if err != nil {
		return 0, dbError("recover subtitle jobs", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) SaveOutput(ctx context.Context, output Output) (Output, error) {
	if output.ID == "" {
		output.ID = "subout-" + rand.Text()
	}
	if len(output.Payload) > maxSubtitleBytes*2 {
		return Output{}, fmt.Errorf("%w: subtitle output is too large", ErrInvalid)
	}
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO subtitle_outputs (id, job_id, video_kind, video_id, language, forced, hi, format, origin_path, target_path, payload, status, detail)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12)
		 RETURNING created_at`,
		output.ID, output.JobID, output.VideoKind, output.VideoID, output.Language, output.Forced, output.HI,
		output.Format, output.OriginPath, output.TargetPath, output.Payload, truncate(output.Detail, maxTextRunes)).Scan(&output.CreatedAt); err != nil {
		return Output{}, dbError("save subtitle output", err)
	}
	output.Status = "pending"
	return output, nil
}

func (s *Store) Output(ctx context.Context, id string) (Output, error) {
	var output Output
	err := s.pool.QueryRow(ctx,
		`SELECT id, job_id, video_kind, video_id, language, forced, hi, format, origin_path, target_path, payload, status, detail, created_at, applied_at
		 FROM subtitle_outputs WHERE id = $1`, id).
		Scan(&output.ID, &output.JobID, &output.VideoKind, &output.VideoID, &output.Language, &output.Forced,
			&output.HI, &output.Format, &output.OriginPath, &output.TargetPath, &output.Payload, &output.Status,
			&output.Detail, &output.CreatedAt, &output.AppliedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Output{}, ErrNotFound
	}
	if err != nil {
		return Output{}, dbError("load subtitle output", err)
	}
	return output, nil
}

func (s *Store) Outputs(ctx context.Context, kind, id string) ([]Output, error) {
	query := `SELECT id, job_id, video_kind, video_id, language, forced, hi, format, origin_path, target_path, payload, status, detail, created_at, applied_at
		 FROM subtitle_outputs WHERE status = 'pending'`
	args := []any{}
	if kind != "" && id != "" {
		query += ` AND video_kind = $1 AND video_id = $2`
		args = append(args, kind, id)
	}
	query += ` ORDER BY created_at DESC LIMIT 20`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, dbError("list subtitle outputs", err)
	}
	defer rows.Close()
	out := []Output{}
	for rows.Next() {
		var output Output
		if err := rows.Scan(&output.ID, &output.JobID, &output.VideoKind, &output.VideoID, &output.Language,
			&output.Forced, &output.HI, &output.Format, &output.OriginPath, &output.TargetPath, &output.Payload,
			&output.Status, &output.Detail, &output.CreatedAt, &output.AppliedAt); err != nil {
			return nil, dbError("list subtitle outputs", err)
		}
		out = append(out, output)
	}
	return out, rows.Err()
}

func (s *Store) MarkOutputApplied(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE subtitle_outputs SET status = 'applied', applied_at = now() WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return dbError("apply subtitle output", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: output was already reviewed", ErrConflict)
	}
	return nil
}

func (s *Store) DeleteOutput(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM subtitle_outputs WHERE id = $1`, id)
	if err != nil {
		return dbError("discard subtitle output", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func encode(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("subtitles: encode configuration: %w", err)
	}
	return raw, nil
}

func videoKey(kind, id string) string {
	return kind + "\x00" + id
}

func dbError(operation string, err error) error {
	return fmt.Errorf("%w: %s: %v", errDatabase, operation, err)
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	for len(text) > limit {
		_, size := utf8.DecodeLastRuneInString(text)
		text = text[:len(text)-size]
	}
	return text
}
