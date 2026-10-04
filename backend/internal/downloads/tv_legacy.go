package downloads

import (
	"context"
	"time"
)

// UnlinkedTV returns the next bounded page of completed jobs with no movie or TV owner.
func (m *Manager) UnlinkedTV(ctx context.Context, afterCreated time.Time, afterID string) ([]Job, error) {
	rows, err := m.pool.Query(ctx, `SELECT `+jobColumns+` FROM downloads
	 WHERE status = 'completed' AND movie_adopted = false AND NOT EXISTS
	 (SELECT 1 FROM tv_acquisitions WHERE job_id = downloads.id)
	 AND (created_at, id) > ($1, $2) ORDER BY created_at, id LIMIT 20`, afterCreated, afterID)
	if err != nil {
		return nil, dbError("load unimported TV downloads", err)
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, dbError("load unimported TV downloads", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
