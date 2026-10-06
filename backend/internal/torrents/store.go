package torrents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const jobColumns = `id, info_hash, name, title, release_id, source, status, magnet_uri, metainfo,
	private, bytes_done, uploaded, bytes_total, pieces_total, pieces_done, piece_bits, files,
	seed_ratio_limit, seed_time_limit_minutes, seeding_started_at, seeding_seconds, error,
	added_at, updated_at, completed_at`

// listColumns omits magnet URIs, metainfo and resume bitmaps so read paths cannot leak them.
const listColumns = `id, info_hash, name, title, release_id, source, status, private, bytes_done,
	uploaded, bytes_total, pieces_total, pieces_done, files, seed_ratio_limit,
	seed_time_limit_minutes, seeding_started_at, seeding_seconds, error, added_at, updated_at, completed_at`

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func storeError(op string, cause error) error {
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		return fmt.Errorf("torrents: %s: database error %s", op, pgErr.Code)
	}
	return fmt.Errorf("torrents: %s: database error", op)
}

func scanJob(row pgx.Row) (Job, error) {
	var (
		job            Job
		filesJSON      []byte
		seedingStarted *time.Time
		completedAt    *time.Time
	)
	err := row.Scan(&job.ID, &job.InfoHash, &job.Name, &job.Title, &job.ReleaseID, &job.Source,
		&job.Status, &job.Private, &job.BytesDone, &job.Uploaded, &job.BytesTotal, &job.PiecesTotal,
		&job.PiecesDone, &filesJSON, &job.SeedRatioLimit, &job.SeedTimeLimitMinutes, &seedingStarted,
		&job.SeedingElapsed, &job.Error, &job.AddedAt, &job.UpdatedAt, &completedAt)
	if err != nil {
		return Job{}, err
	}
	job.CompletedAt = completedAt
	job.Files = make([]File, 0)
	if len(filesJSON) > 0 {
		if err := json.Unmarshal(filesJSON, &job.Files); err != nil {
			return Job{}, errors.New("torrents: stored file list is invalid")
		}
	}
	if job.Files == nil {
		job.Files = []File{}
	}
	return job, nil
}

func (s *Service) jobByID(ctx context.Context, q querier, id string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx, `SELECT `+listColumns+` FROM torrent_jobs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, storeError("load job", err)
	}
	return job, nil
}

func (s *Service) jobByInfoHash(ctx context.Context, q querier, hash string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx, `SELECT `+listColumns+` FROM torrent_jobs WHERE info_hash = $1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, storeError("load job", err)
	}
	return job, nil
}

type storedJob struct {
	Job
	metainfo []byte
	magnet   string
	bits     []byte
}

func (s *Service) storedByID(ctx context.Context, q querier, id string) (storedJob, error) {
	var (
		job            storedJob
		files          []byte
		seedingStarted *time.Time
		completedAt    *time.Time
	)
	err := q.QueryRow(ctx, `SELECT `+jobColumns+` FROM torrent_jobs WHERE id = $1`, id).Scan(
		&job.ID, &job.InfoHash, &job.Name, &job.Title, &job.ReleaseID, &job.Source, &job.Status,
		&job.magnet, &job.metainfo, &job.Private, &job.BytesDone, &job.Uploaded, &job.BytesTotal,
		&job.PiecesTotal, &job.PiecesDone, &job.bits, &files, &job.SeedRatioLimit,
		&job.SeedTimeLimitMinutes, &seedingStarted, &job.SeedingElapsed, &job.Error, &job.AddedAt,
		&job.UpdatedAt, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedJob{}, ErrNotFound
	}
	if err != nil {
		return storedJob{}, storeError("load job", err)
	}
	job.Job.Files = []File{}
	if len(files) > 0 {
		if err := json.Unmarshal(files, &job.Job.Files); err != nil {
			return storedJob{}, errors.New("torrents: stored file list is invalid")
		}
	}
	job.Job.CompletedAt = completedAt
	return job, nil
}

func (s *Service) listJobs(ctx context.Context, q querier, statuses ...string) ([]Job, error) {
	rows, err := q.Query(ctx, `SELECT `+listColumns+` FROM torrent_jobs
		WHERE ($1::text[] IS NULL OR status = ANY($1)) ORDER BY added_at DESC, id DESC LIMIT $2`,
		statusesOrNil(statuses), listLimit)
	if err != nil {
		return nil, storeError("list jobs", err)
	}
	defer rows.Close()
	jobs := make([]Job, 0)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, storeError("list jobs", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("list jobs", err)
	}
	return jobs, nil
}

func statusesOrNil(statuses []string) []string {
	if len(statuses) == 0 {
		return nil
	}
	return statuses
}

func (s *Service) countJobs(ctx context.Context, q querier, statuses ...string) (int, error) {
	var count int
	err := q.QueryRow(ctx, `SELECT count(*) FROM torrent_jobs WHERE status = ANY($1)`, statuses).Scan(&count)
	if err != nil {
		return 0, storeError("count jobs", err)
	}
	return count, nil
}

func (s *Service) insertJob(ctx context.Context, q querier, job storedJob) (Job, error) {
	inserted, err := scanJob(q.QueryRow(ctx, `INSERT INTO torrent_jobs
		(id, info_hash, name, title, release_id, source, status, magnet_uri, metainfo, private,
		 seed_ratio_limit, seed_time_limit_minutes)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (info_hash) DO NOTHING RETURNING `+listColumns,
		job.ID, job.InfoHash, job.Name, job.Title, job.ReleaseID, job.Source, statusQueued,
		job.magnet, job.metainfo, job.Private, job.SeedRatioLimit, job.SeedTimeLimitMinutes))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, storeError("create job", err)
	}
	return inserted, nil
}

// setPrivate repairs jobs stored by versions that never copied the metainfo private flag.
func (s *Service) setPrivate(ctx context.Context, q querier, id string) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET private = true, updated_at = now() WHERE id = $1`, id)
	if err != nil {
		return storeError("update job", err)
	}
	return nil
}

func (s *Service) saveMetadata(ctx context.Context, q querier, id, name string, total int64, pieces int, files []File, metainfo []byte, private bool) error {
	encoded, err := json.Marshal(files)
	if err != nil {
		return errors.New("torrents: the file list could not be encoded")
	}
	_, err = q.Exec(ctx, `UPDATE torrent_jobs SET name = $2, bytes_total = $3, pieces_total = $4,
		files = $5::jsonb, metainfo = $6, private = $7, updated_at = now() WHERE id = $1`,
		id, name, total, pieces, string(encoded), metainfo, private)
	if err != nil {
		return storeError("save metadata", err)
	}
	return nil
}

func (s *Service) saveProgress(ctx context.Context, q querier, id string, done, uploaded int64, piecesDone int, bits []byte) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET bytes_done = $2, uploaded = $3, pieces_done = $4,
		piece_bits = $5, updated_at = now() WHERE id = $1`, id, done, uploaded, piecesDone, bits)
	if err != nil {
		return storeError("save progress", err)
	}
	return nil
}

func (s *Service) setStatus(ctx context.Context, q querier, id, status, message string) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET status = $2, error = $3, updated_at = now() WHERE id = $1`,
		id, status, message)
	if err != nil {
		return storeError("update job", err)
	}
	return nil
}

func (s *Service) setStatusWhen(ctx context.Context, q querier, id, status, message string, protected ...string) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET status = $2, error = $3, updated_at = now()
		WHERE id = $1 AND status <> ALL($4::text[])`, id, status, message, protected)
	if err != nil {
		return storeError("update job", err)
	}
	return nil
}

// setQueuedState requeues a held job unless a manual pause or cancel won the race.
func (s *Service) setQueuedState(ctx context.Context, q querier, id, reason string) error {
	return s.setStatusWhen(ctx, q, id, statusQueued, reason, statusPaused, statusCancelled)
}

// queueUnlessCancelled requeues a job and reports a conflict when a cancel won the race.
func (s *Service) queueUnlessCancelled(ctx context.Context, q querier, id string) error {
	tag, err := q.Exec(ctx, `UPDATE torrent_jobs SET status = $2, error = '', updated_at = now()
		WHERE id = $1 AND status <> $3`, id, statusQueued, statusCancelled)
	if err != nil {
		return storeError("queue job", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (s *Service) setQueuedReason(ctx context.Context, q querier, id, reason string) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET error = $2, updated_at = now()
		WHERE id = $1 AND status = $3`, id, reason, statusQueued)
	if err != nil {
		return storeError("update job", err)
	}
	return nil
}

func (s *Service) markSeeding(ctx context.Context, q querier, id string, started time.Time) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET status = $2, error = '', seeding_started_at = $3,
		updated_at = now() WHERE id = $1 AND status <> $4`, id, statusSeeding, started, statusCancelled)
	if err != nil {
		return storeError("start seeding", err)
	}
	return nil
}

func (s *Service) markCompleted(ctx context.Context, q querier, id string, seedingSeconds int64) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET status = $2, error = '', seeding_started_at = NULL,
		seeding_seconds = $3, completed_at = now(), updated_at = now() WHERE id = $1 AND status <> $4`,
		id, statusCompleted, seedingSeconds, statusCancelled)
	if err != nil {
		return storeError("complete job", err)
	}
	return nil
}

func (s *Service) markFailed(ctx context.Context, q querier, id, message string) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET status = $2, error = $3, seeding_started_at = NULL,
		updated_at = now() WHERE id = $1 AND status <> $4`, id, statusFailed, message, statusCancelled)
	if err != nil {
		return storeError("fail job", err)
	}
	return nil
}

func (s *Service) saveSeedLimits(ctx context.Context, q querier, id string, ratio float64, minutes int) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx, `UPDATE torrent_jobs SET seed_ratio_limit = $2,
		seed_time_limit_minutes = $3, updated_at = now() WHERE id = $1 RETURNING `+listColumns,
		id, ratio, minutes))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, storeError("save seed limits", err)
	}
	return job, nil
}

// requeueRunning resumes jobs that were transferring when the previous process exited.
func (s *Service) requeueRunning(ctx context.Context, q querier) error {
	_, err := q.Exec(ctx, `UPDATE torrent_jobs SET status = $1, seeding_started_at = NULL, updated_at = now()
		WHERE status IN ($2, $3, $4, $5)`, statusQueued, statusMetadata, statusChecking, statusDownloading, statusSeeding)
	if err != nil {
		return storeError("resume jobs", err)
	}
	return nil
}

func (s *Service) deleteJob(ctx context.Context, q querier, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM torrent_jobs WHERE id = $1`, id)
	if err != nil {
		return storeError("delete job", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) checkSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT to_regclass('torrent_jobs') IS NOT NULL
		AND to_regclass('torrent_settings') IS NOT NULL
		AND to_regclass('torrent_sources') IS NOT NULL`).Scan(&exists)
	if err != nil || !exists {
		return ErrSchema
	}
	return nil
}
