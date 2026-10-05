package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errDatabase = errors.New("operations: database operation failed")

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func dbError(op string, cause error) error {
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		return fmt.Errorf("operations: %s: database error %s: %w", op, pgErr.Code, errDatabase)
	}
	return fmt.Errorf("operations: %s: %w", op, errDatabase)
}

type EventRecord struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Source   string    `json:"source,omitempty"`
	Message  string    `json:"message,omitempty"`
	Ref      string    `json:"ref,omitempty"`
}

func (s *Service) insertEvent(ctx context.Context, event Event) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO operations_events (kind, severity, source, message, ref) VALUES ($1, $2, $3, $4, $5)`,
		event.Kind, event.Severity, event.Source, event.Message, event.Ref)
	if err != nil {
		return dbError("record event", err)
	}
	return nil
}

type eventFilter struct {
	kinds      []string
	severities []string
	limit      int
}

func (s *Service) listEvents(ctx context.Context, filter eventFilter) ([]EventRecord, error) {
	limit := filter.limit
	if limit <= 0 || limit > maxEventLimit {
		limit = defaultEventLimit
	}
	rows, err := s.pool.Query(ctx, `SELECT id, at, kind, severity, source, message, ref FROM operations_events
		WHERE (coalesce(cardinality($1::text[]), 0) = 0 OR kind = ANY($1))
		AND (coalesce(cardinality($2::text[]), 0) = 0 OR severity = ANY($2))
		ORDER BY id DESC LIMIT $3`, filter.kinds, filter.severities, limit)
	if err != nil {
		return nil, dbError("list events", err)
	}
	defer rows.Close()
	events := make([]EventRecord, 0, limit)
	for rows.Next() {
		var event EventRecord
		if err := rows.Scan(&event.ID, &event.At, &event.Kind, &event.Severity, &event.Source, &event.Message, &event.Ref); err != nil {
			return nil, dbError("list events", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("list events", err)
	}
	return events, nil
}

func (s *Service) countEventsByKind(ctx context.Context) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT kind, count(*) FROM operations_events
		WHERE kind IN ($1, $2, $3) GROUP BY kind`,
		KindDownloadFailed, KindImportError, KindProviderFailure)
	if err != nil {
		return nil, dbError("count events", err)
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var kind string
		var count int64
		if err := rows.Scan(&kind, &count); err != nil {
			return nil, dbError("count events", err)
		}
		counts[kind] = count
	}
	return counts, rows.Err()
}

func (s *Service) countEventsSince(ctx context.Context, kinds []string, since time.Time) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM operations_events WHERE kind = ANY($1) AND at >= $2`, kinds, since).Scan(&count)
	if err != nil {
		return 0, dbError("count events", err)
	}
	return count, nil
}

// failedDownloads returns failed jobs that have no failure event yet; torrent shadow rows count once.
func (s *Service) failedDownloads(ctx context.Context, limit int) ([]struct{ ID, Title, Error, Protocol string }, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.id, d.title, d.error`+s.protocolExpr()+` FROM downloads d
		WHERE d.status = 'failed'
		AND NOT EXISTS (SELECT 1 FROM operations_events e WHERE e.kind = $1 AND e.ref = d.id)
		ORDER BY d.updated_at DESC LIMIT $2`, KindDownloadFailed, limit)
	if err != nil {
		return nil, dbError("list failed downloads", err)
	}
	defer rows.Close()
	var failed []struct{ ID, Title, Error, Protocol string }
	for rows.Next() {
		var job struct{ ID, Title, Error, Protocol string }
		if err := rows.Scan(&job.ID, &job.Title, &job.Error, &job.Protocol); err != nil {
			return nil, dbError("list failed downloads", err)
		}
		failed = append(failed, job)
	}
	return failed, rows.Err()
}

func (s *Service) protocolExpr() string {
	if s.downloadsProtocol {
		return ", d.protocol"
	}
	return ", 'usenet'"
}

type downloadStat struct {
	State      string
	Count      int64
	BytesDone  int64
	BytesTotal int64
}

// usenetFilter keeps torrent shadow rows out of the download gauges.
func (s *Service) usenetFilter() string {
	if s.downloadsProtocol {
		return " WHERE protocol = 'usenet'"
	}
	return ""
}

func (s *Service) usenetCondition() string {
	if s.downloadsProtocol {
		return " AND protocol = 'usenet'"
	}
	return ""
}

func (s *Service) downloadStats(ctx context.Context) ([]downloadStat, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, count(*), coalesce(sum(bytes_done), 0), coalesce(sum(bytes_total), 0)
		FROM downloads`+s.usenetFilter()+` GROUP BY status`)
	if err != nil {
		return nil, dbError("download statistics", err)
	}
	defer rows.Close()
	var stats []downloadStat
	for rows.Next() {
		var stat downloadStat
		if err := rows.Scan(&stat.State, &stat.Count, &stat.BytesDone, &stat.BytesTotal); err != nil {
			return nil, dbError("download statistics", err)
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (s *Service) torrentStats(ctx context.Context) ([]downloadStat, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, count(*), coalesce(sum(bytes_done), 0), coalesce(sum(bytes_total), 0)
		FROM torrent_jobs GROUP BY status`)
	if err != nil {
		return nil, dbError("torrent statistics", err)
	}
	defer rows.Close()
	var stats []downloadStat
	for rows.Next() {
		var stat downloadStat
		if err := rows.Scan(&stat.State, &stat.Count, &stat.BytesDone, &stat.BytesTotal); err != nil {
			return nil, dbError("torrent statistics", err)
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (s *Service) countStuckJobs(ctx context.Context, stuckMinutes int) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM downloads
		WHERE status IN ('queued', 'downloading', 'verifying', 'repairing', 'extracting')
		AND updated_at < now() - make_interval(mins => $1)`+s.usenetCondition(), stuckMinutes).Scan(&count)
	if err != nil {
		return 0, dbError("stuck job count", err)
	}
	var torrents int
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM torrent_jobs
		WHERE status IN ('metadata', 'checking', 'downloading')
		AND updated_at < now() - make_interval(mins => $1)`, stuckMinutes).Scan(&torrents)
	var pgErr *pgconn.PgError
	if err != nil && !(errors.As(err, &pgErr) && pgErr.Code == "42P01") {
		return 0, dbError("stuck torrent count", err)
	}
	return count + torrents, nil
}

// countTable tolerates a missing domain table so partial deployments keep reporting metrics.
func (s *Service) countTable(ctx context.Context, table string) (int64, error) {
	if !validTableName(table) {
		return 0, InvalidError("unknown table")
	}
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
		return 0, nil
	}
	if err != nil {
		return 0, dbError("count "+table, err)
	}
	return count, nil
}

func validTableName(name string) bool {
	switch name {
	case "movies", "tv_series", "tv_episodes", "music_artists", "music_albums", "music_tracks":
		return true
	}
	return false
}

// importFailureSources read media import failures from the per-library history tables.
var importFailureSources = []struct{ name, table, condition string }{
	{"movies", "movie_history", "type = 'import-failed'"},
	{"tv", "tv_history", "type = 'import-failed'"},
	{"music", "music_history", "type = 'error' AND message LIKE 'Import failed%'"},
}

func (s *Service) pendingImportFailures(ctx context.Context, limit int) ([]struct{ Source, Message, Ref string }, error) {
	var pending []struct{ Source, Message, Ref string }
	for _, source := range importFailureSources {
		rows, err := s.pool.Query(ctx, `SELECT h.id, h.message FROM `+source.table+` h
			WHERE `+source.condition+`
			AND NOT EXISTS (SELECT 1 FROM operations_events e WHERE e.kind = $1 AND e.ref = $2 || h.id)
			ORDER BY h.id DESC LIMIT $3`, KindImportError, source.name+":", limit)
		var pgErr *pgconn.PgError
		if err != nil {
			if errors.As(err, &pgErr) && pgErr.Code == "42P01" {
				continue
			}
			return nil, dbError("list import failures", err)
		}
		for rows.Next() {
			var id int64
			var message string
			if err := rows.Scan(&id, &message); err != nil {
				rows.Close()
				return nil, dbError("list import failures", err)
			}
			pending = append(pending, struct{ Source, Message, Ref string }{
				Source: source.name, Message: message, Ref: source.name + ":" + strconv.FormatInt(id, 10),
			})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, dbError("list import failures", err)
		}
	}
	return pending, nil
}

func (s *Service) schemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.pool.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, dbError("schema version", err)
	}
	return version, nil
}

func (s *Service) serverVersion(ctx context.Context) (string, error) {
	var version string
	if err := s.pool.QueryRow(ctx, `SHOW server_version`).Scan(&version); err != nil {
		return "", dbError("server version", err)
	}
	return version, nil
}

func (s *Service) loadConfig(ctx context.Context) error {
	if err := s.ensureRules(ctx); err != nil {
		return err
	}
	rules, err := s.loadRules(ctx)
	if err != nil {
		return err
	}
	webhook, err := s.loadWebhook(ctx)
	if err != nil {
		return err
	}
	backups, err := s.loadBackupConfig(ctx)
	if err != nil {
		return err
	}
	states, err := s.loadAlertStates(ctx)
	if err != nil {
		return err
	}
	s.configMu.Lock()
	s.rules, s.webhook, s.backups, s.alertView = rules, webhook, backups, states
	s.configMu.Unlock()
	return nil
}

// ensureRules adds defaults for rules introduced after the schema migration.
func (s *Service) ensureRules(ctx context.Context) error {
	for _, name := range ruleNames {
		thresholds := defaultThresholds(name)
		body, err := json.Marshal(thresholds)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `INSERT INTO operations_alert_rules (name, severity, config)
			VALUES ($1, $2, $3) ON CONFLICT (name) DO NOTHING`, name, defaultSeverity(name), body); err != nil {
			return dbError("seed alert rules", err)
		}
	}
	return nil
}

func (s *Service) loadRules(ctx context.Context) ([]alertRule, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, enabled, severity, config FROM operations_alert_rules`)
	if err != nil {
		return nil, dbError("load alert rules", err)
	}
	defer rows.Close()
	rules := make([]alertRule, 0, len(ruleNames))
	for rows.Next() {
		var (
			rule      alertRule
			rawConfig []byte
			severity  string
		)
		if err := rows.Scan(&rule.name, &rule.enabled, &severity, &rawConfig); err != nil {
			return nil, dbError("load alert rules", err)
		}
		rule.severity = normalizeSeverity(severity)
		rule.thresholds = mergeThresholds(defaultThresholds(rule.name), rawConfig)
		if !validRuleName(rule.name) {
			continue
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("load alert rules", err)
	}
	return rules, nil
}

func (s *Service) saveRules(ctx context.Context, updates []alertRule) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbError("save alert rules", err)
	}
	defer tx.Rollback(ctx)
	for _, rule := range updates {
		body, err := json.Marshal(rule.thresholds)
		if err != nil {
			return InvalidError("alert thresholds could not be encoded")
		}
		tag, err := tx.Exec(ctx, `UPDATE operations_alert_rules SET enabled = $2, severity = $3, config = $4, updated_at = now()
			WHERE name = $1`, rule.name, rule.enabled, rule.severity, body)
		if err != nil {
			return dbError("save alert rules", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return dbError("save alert rules", err)
	}
	return nil
}

func (s *Service) loadWebhook(ctx context.Context) (webhookConfig, error) {
	config := defaultWebhook()
	raw, err := s.configData(ctx, "webhook")
	if err != nil {
		return config, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			s.logger.Warn("operations: ignoring an unreadable webhook configuration")
			return defaultWebhook(), nil
		}
	}
	return config.normalized(), nil
}

func (s *Service) loadBackupConfig(ctx context.Context) (backupConfig, error) {
	config := defaultBackupConfig()
	raw, err := s.configData(ctx, "backups")
	if err != nil {
		return config, err
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &config); err != nil {
			s.logger.Warn("operations: ignoring an unreadable backup configuration")
			return defaultBackupConfig(), nil
		}
	}
	return config.normalized(), nil
}

func (s *Service) configData(ctx context.Context, name string) ([]byte, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM operations_config WHERE name = $1`, name).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, dbError("load configuration", err)
	}
	return raw, nil
}

func (s *Service) saveConfigData(ctx context.Context, name string, body []byte) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO operations_config (name, data) VALUES ($1, $2)
		ON CONFLICT (name) DO UPDATE SET data = excluded.data, updated_at = now()`, name, body)
	if err != nil {
		return dbError("save configuration", err)
	}
	return nil
}

type alertState struct {
	Name      string    `json:"name"`
	Firing    bool      `json:"firing"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message,omitempty"`
	Value     float64   `json:"value"`
	Since     time.Time `json:"since"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Service) loadAlertStates(ctx context.Context) ([]alertState, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, firing, severity, message, value, since, updated_at
		FROM operations_alert_state ORDER BY name`)
	if err != nil {
		return nil, dbError("load alert state", err)
	}
	defer rows.Close()
	states := make([]alertState, 0, len(ruleNames))
	for rows.Next() {
		var state alertState
		if err := rows.Scan(&state.Name, &state.Firing, &state.Severity, &state.Message, &state.Value, &state.Since, &state.UpdatedAt); err != nil {
			return nil, dbError("load alert state", err)
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

func (s *Service) saveAlertState(ctx context.Context, rule alertRule, evaluation evaluation) error {
	var since time.Time
	err := s.pool.QueryRow(ctx, `INSERT INTO operations_alert_state (name, firing, severity, message, value, since, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())
		ON CONFLICT (name) DO UPDATE SET firing = excluded.firing, severity = excluded.severity, message = excluded.message,
			value = excluded.value, since = CASE WHEN operations_alert_state.firing IS DISTINCT FROM excluded.firing THEN now() ELSE operations_alert_state.since END,
			updated_at = now()
		RETURNING since`, rule.name, evaluation.firing, rule.severity, evaluation.message, evaluation.value).Scan(&since)
	if err != nil {
		return dbError("save alert state", err)
	}
	return nil
}

func (s *Service) recordAlertEvent(ctx context.Context, rule alertRule, evaluation evaluation) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `INSERT INTO operations_alert_events (name, firing, severity, message, value)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		rule.name, evaluation.firing, rule.severity, evaluation.message, evaluation.value).Scan(&id)
	if err != nil {
		return 0, dbError("record alert event", err)
	}
	return id, nil
}

func (s *Service) listAlertEvents(ctx context.Context, limit int, name string) ([]AlertEventRecord, error) {
	if limit <= 0 || limit > maxEventLimit {
		limit = defaultEventLimit
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name, firing, severity, message, value, at FROM operations_alert_events
		WHERE ($1 = '' OR name = $1) ORDER BY id DESC LIMIT $2`, name, limit)
	if err != nil {
		return nil, dbError("list alert history", err)
	}
	defer rows.Close()
	events := make([]AlertEventRecord, 0, limit)
	for rows.Next() {
		var event AlertEventRecord
		if err := rows.Scan(&event.ID, &event.Name, &event.Firing, &event.Severity, &event.Message, &event.Value, &event.At); err != nil {
			return nil, dbError("list alert history", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

type AlertEventRecord struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Firing   bool      `json:"firing"`
	Severity string    `json:"severity"`
	Message  string    `json:"message,omitempty"`
	Value    float64   `json:"value"`
	At       time.Time `json:"at"`
}

func (s *Service) lastEventAt(ctx context.Context, kind string) (time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT coalesce(max(at), to_timestamp(0)) FROM operations_events WHERE kind = $1`, kind).Scan(&at)
	if err != nil {
		return time.Time{}, dbError("last event time", err)
	}
	return at, nil
}
