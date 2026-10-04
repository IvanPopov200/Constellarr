package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

var errDatabase = errors.New("downloads: database operation failed")

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

func dbError(op string, cause error) error {
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		return fmt.Errorf("downloads: %s: database error %s: %w", op, pgErr.Code, errDatabase)
	}
	return fmt.Errorf("downloads: %s: %w", op, errDatabase)
}

const jobColumns = `id, release_id, title, status, bytes_done, bytes_total, segments_done, ` +
	`segments_total, missing_segments, error, files, created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner, extra ...any) (Job, error) {
	var (
		job     Job
		rawJSON []byte
	)
	dest := []any{
		&job.ID, &job.ReleaseID, &job.Title, &job.Status, &job.BytesDone, &job.BytesTotal,
		&job.SegmentsDone, &job.SegmentsTotal, &job.MissingSegments, &job.Error, &rawJSON,
		&job.CreatedAt, &job.UpdatedAt,
	}
	dest = append(dest, extra...)
	if err := row.Scan(dest...); err != nil {
		return Job{}, err
	}
	job.Files = make([]OutputFile, 0)
	if len(rawJSON) > 0 {
		if err := json.Unmarshal(rawJSON, &job.Files); err != nil {
			return Job{}, err
		}
	}
	if job.Files == nil {
		job.Files = []OutputFile{}
	}
	return job, nil
}

func (m *Manager) jobByID(ctx context.Context, q querier, id string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx, `SELECT `+jobColumns+` FROM downloads WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, dbError("load download", err)
	}
	return job, nil
}

func (m *Manager) jobByRelease(ctx context.Context, q querier, releaseID string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx, `SELECT `+jobColumns+` FROM downloads WHERE release_id = $1`, releaseID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, dbError("load download", err)
	}
	return job, nil
}

func (m *Manager) listJobs(ctx context.Context, q querier) ([]Job, error) {
	rows, err := q.Query(ctx, `SELECT `+jobColumns+` FROM downloads ORDER BY created_at DESC, id DESC LIMIT $1`, listLimit)
	if err != nil {
		return nil, dbError("list downloads", err)
	}
	defer rows.Close()
	jobs := make([]Job, 0, listLimit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, dbError("list downloads", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, dbError("list downloads", err)
	}
	return jobs, nil
}

func (m *Manager) insertJob(ctx context.Context, q querier, id, releaseID, title string, nzb []byte) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx,
		`INSERT INTO downloads (id, release_id, title, nzb) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (release_id) DO NOTHING RETURNING `+jobColumns,
		id, releaseID, title, nzb))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, dbError("create download", err)
	}
	return job, nil
}

func (m *Manager) claimNext(ctx context.Context, q querier) (Job, []byte, error) {
	tx, err := q.Begin(ctx)
	if err != nil {
		return Job{}, nil, dbError("claim download", err)
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx,
		`SELECT id FROM downloads WHERE status = $1 ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1`,
		statusQueued).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, nil, ErrNotFound
	}
	if err != nil {
		return Job{}, nil, dbError("claim download", err)
	}
	var nzb []byte
	job, err := scanJob(tx.QueryRow(ctx,
		`UPDATE downloads SET status = $2, updated_at = now() WHERE id = $1 RETURNING `+jobColumns+`, nzb`,
		id, statusDownloading), &nzb)
	if err != nil {
		return Job{}, nil, dbError("claim download", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, nil, dbError("claim download", err)
	}
	return job, nzb, nil
}

func (m *Manager) requeueInterrupted(ctx context.Context, q querier) error {
	_, err := q.Exec(ctx,
		`UPDATE downloads SET status = $1, updated_at = now()
		 WHERE status IN ('downloading', 'verifying', 'repairing', 'extracting')`, statusQueued)
	if err != nil {
		return dbError("requeue downloads", err)
	}
	return nil
}

func (m *Manager) saveProgress(ctx context.Context, q querier, id string, progress usenet.Progress) error {
	_, err := q.Exec(ctx,
		`UPDATE downloads SET bytes_done = $2, bytes_total = $3, segments_done = $4,
		 segments_total = $5, missing_segments = $6, updated_at = now() WHERE id = $1`,
		id, progress.DownloadedBytes, progress.TotalBytes, progress.CompletedSegments,
		progress.TotalSegments, progress.MissingSegments)
	if err != nil {
		return dbError("save download progress", err)
	}
	return nil
}

func (m *Manager) setStatus(ctx context.Context, q querier, id, status string) error {
	_, err := q.Exec(ctx, `UPDATE downloads SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	if err != nil {
		return dbError("update download", err)
	}
	return nil
}

func (m *Manager) complete(ctx context.Context, q querier, id string, files []OutputFile) error {
	encoded, err := json.Marshal(files)
	if err != nil {
		return errors.New("downloads: output files could not be encoded")
	}
	_, err = q.Exec(ctx,
		`UPDATE downloads SET status = $2, error = '', files = $3::jsonb, updated_at = now() WHERE id = $1`,
		id, statusCompleted, string(encoded))
	if err != nil {
		return dbError("complete download", err)
	}
	return nil
}

func (m *Manager) failJob(ctx context.Context, q querier, id, message string) error {
	_, err := q.Exec(ctx,
		`UPDATE downloads SET status = $2, error = $3, updated_at = now() WHERE id = $1`,
		id, statusFailed, message)
	if err != nil {
		return dbError("fail download", err)
	}
	return nil
}

func (m *Manager) requeueJob(ctx context.Context, q querier, id string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx,
		`UPDATE downloads SET status = $2, error = '', updated_at = now()
		 WHERE id = $1 AND status = $3 RETURNING `+jobColumns,
		id, statusQueued, statusFailed))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, dbError("retry download", err)
	}
	return job, nil
}
