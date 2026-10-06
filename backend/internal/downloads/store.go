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

const jobColumns = `id, release_id, title, protocol, status, pause_reason, bytes_done, bytes_total, segments_done, ` +
	`segments_total, missing_segments, error, files, created_at, updated_at`

// activeStatuses are the states a running worker may write through; anything else owns the row.
const activeStatuses = `('downloading', 'verifying', 'repairing', 'extracting')`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner, extra ...any) (Job, error) {
	var (
		job     Job
		rawJSON []byte
	)
	dest := []any{
		&job.ID, &job.ReleaseID, &job.Title, &job.Protocol, &job.Status, &job.PauseReason, &job.BytesDone, &job.BytesTotal,
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
	rows, err := q.Query(ctx, `SELECT `+jobColumns+` FROM downloads WHERE protocol = 'usenet' ORDER BY created_at DESC, id DESC LIMIT $1`, listLimit)
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
	// A paused policy holds the whole queue; the caller polls again once it lifts.
	if !m.policy.Allowed() {
		return Job{}, nil, ErrNotFound
	}
	tx, err := q.Begin(ctx)
	if err != nil {
		return Job{}, nil, dbError("claim download", err)
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx,
		`SELECT id FROM downloads WHERE protocol = 'usenet' AND status = $1 ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1`,
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
		 WHERE protocol = 'usenet' AND status IN ('downloading', 'verifying', 'repairing', 'extracting')`, statusQueued)
	if err != nil {
		return dbError("requeue downloads", err)
	}
	return nil
}

// saveProgress only writes while a worker owns the job, so a paused or cancelled row keeps its state.
func (m *Manager) saveProgress(ctx context.Context, q querier, id string, progress usenet.Progress) error {
	tag, err := q.Exec(ctx,
		`UPDATE downloads SET bytes_done = $2, bytes_total = $3, segments_done = $4,
		 segments_total = $5, missing_segments = $6, updated_at = now()
		 WHERE id = $1 AND status IN `+activeStatuses,
		id, progress.DownloadedBytes, progress.TotalBytes, progress.CompletedSegments,
		progress.TotalSegments, progress.MissingSegments)
	if err != nil {
		return dbError("save download progress", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (m *Manager) setStatus(ctx context.Context, q querier, id, status string) error {
	tag, err := q.Exec(ctx,
		`UPDATE downloads SET status = $2, updated_at = now() WHERE id = $1 AND status IN `+activeStatuses,
		id, status)
	if err != nil {
		return dbError("update download", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (m *Manager) complete(ctx context.Context, q querier, id string, files []OutputFile) error {
	encoded, err := json.Marshal(files)
	if err != nil {
		return errors.New("downloads: output files could not be encoded")
	}
	tag, err := q.Exec(ctx,
		`UPDATE downloads SET status = $2, error = '', pause_reason = '', files = $3::jsonb, updated_at = now()
		 WHERE id = $1 AND status IN `+activeStatuses,
		id, statusCompleted, string(encoded))
	if err != nil {
		return dbError("complete download", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (m *Manager) failJob(ctx context.Context, q querier, id, message string) error {
	tag, err := q.Exec(ctx,
		`UPDATE downloads SET status = $2, error = $3, updated_at = now()
		 WHERE id = $1 AND status IN `+activeStatuses,
		id, statusFailed, message)
	if err != nil {
		return dbError("fail download", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func (m *Manager) requeueJob(ctx context.Context, q querier, id string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx,
		`UPDATE downloads SET status = $2, error = '', pause_reason = '', updated_at = now()
		 WHERE id = $1 AND status IN ('failed', 'cancelled') RETURNING `+jobColumns,
		id, statusQueued))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, dbError("retry download", err)
	}
	return job, nil
}

// pauseJob pauses queued work and running work; a worker's guarded writes cannot undo it.
func (m *Manager) pauseJob(ctx context.Context, q querier, id string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx,
		`UPDATE downloads SET status = $2, pause_reason = $3, error = '', updated_at = now()
		 WHERE id = $1 AND status IN ('queued', 'downloading', 'verifying', 'repairing', 'extracting')
		 RETURNING `+jobColumns,
		id, statusPaused, pauseReasonManual))
	if errors.Is(err, pgx.ErrNoRows) {
		current, loadErr := m.jobByID(ctx, q, id)
		if loadErr != nil {
			return Job{}, loadErr
		}
		if current.Status == statusPaused {
			return current, nil
		}
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, dbError("pause download", err)
	}
	return job, nil
}

// resumeJob requeues paused work; running work is already where the user wants it.
func (m *Manager) resumeJob(ctx context.Context, q querier, id string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx,
		`UPDATE downloads SET status = $2, pause_reason = '', error = '', updated_at = now()
		 WHERE id = $1 AND status = $3 RETURNING `+jobColumns,
		id, statusQueued, statusPaused))
	if errors.Is(err, pgx.ErrNoRows) {
		current, loadErr := m.jobByID(ctx, q, id)
		if loadErr != nil {
			return Job{}, loadErr
		}
		switch current.Status {
		case statusQueued, statusDownloading, "verifying", "repairing", "extracting":
			return current, nil
		}
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, dbError("resume download", err)
	}
	return job, nil
}

// cancelJob stops a job without touching its files, cache, or history.
func (m *Manager) cancelJob(ctx context.Context, q querier, id string) (Job, error) {
	job, err := scanJob(q.QueryRow(ctx,
		`UPDATE downloads SET status = $2, pause_reason = '', error = '', updated_at = now()
		 WHERE id = $1 AND status IN ('queued', 'paused', 'downloading', 'verifying', 'repairing', 'extracting')
		 RETURNING `+jobColumns,
		id, statusCancelled))
	if errors.Is(err, pgx.ErrNoRows) {
		current, loadErr := m.jobByID(ctx, q, id)
		if loadErr != nil {
			return Job{}, loadErr
		}
		if current.Status == statusCancelled {
			return current, nil
		}
		return Job{}, ErrConflict
	}
	if err != nil {
		return Job{}, dbError("cancel download", err)
	}
	return job, nil
}

// requeueHeld returns a job stopped by the global policy to the queue with its progress intact.
func (m *Manager) requeueHeld(ctx context.Context, q querier, id string) error {
	tag, err := q.Exec(ctx,
		`UPDATE downloads SET status = $2, updated_at = now() WHERE id = $1 AND status IN `+activeStatuses,
		id, statusQueued)
	if err != nil {
		return dbError("requeue download", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}
