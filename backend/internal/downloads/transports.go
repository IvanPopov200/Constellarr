package downloads

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
)

const TorrentPrefix = "torrent_"

type TorrentSource interface {
	Search(context.Context, string) ([]indexer.Release, error)
	Add(context.Context, string, string) (Job, error)
	Get(context.Context, string) (Job, error)
	Retry(context.Context, string) (Job, error)
	Pause(context.Context, string) (Job, error)
	Resume(context.Context, string) (Job, error)
	Cancel(context.Context, string) (Job, error)
	OutputDirectory(string) (string, error)
}

func (m *Manager) SetTorrents(source TorrentSource) {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	m.torrents = source
}

func (m *Manager) torrentSource() TorrentSource {
	m.configMu.RLock()
	defer m.configMu.RUnlock()
	return m.torrents
}

func (m *Manager) SearchTorrents(ctx context.Context, query string) ([]indexer.Release, error) {
	if source := m.torrentSource(); source != nil {
		return source.Search(ctx, query)
	}
	return nil, ErrNotConfigured
}

func (m *Manager) searchMedia(ctx context.Context, query string, usenet func() ([]indexer.Release, error)) ([]indexer.Release, error) {
	source := m.torrentSource()
	if source == nil {
		return usenet()
	}
	type result struct {
		items []indexer.Release
		err   error
	}
	results := make(chan result, 2)
	go func() { items, err := usenet(); results <- result{items, err} }()
	go func() { items, err := source.Search(ctx, query); results <- result{items, err} }()
	items := []indexer.Release{}
	var failures []error
	for range 2 {
		select {
		case result := <-results:
			items = append(items, result.items...)
			if result.err != nil {
				failures = append(failures, result.err)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if len(failures) == 2 && len(items) == 0 {
		return nil, errors.Join(failures...)
	}
	return items, nil
}

func (m *Manager) saveTorrent(ctx context.Context, job Job, releaseID string) (Job, error) {
	job.Protocol = "torrent"
	job.ReleaseID = releaseID
	files, err := json.Marshal(job.Files)
	if err != nil {
		return Job{}, err
	}
	_, err = m.pool.Exec(ctx, `INSERT INTO downloads
		(id, release_id, title, nzb, protocol, status, bytes_done, bytes_total, segments_done, segments_total, files, error)
		VALUES ($1,$2,$3,''::bytea,'torrent',$4,$5,$6,$7,$8,$9::jsonb,$10)
		ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, bytes_done=EXCLUDED.bytes_done,
		bytes_total=EXCLUDED.bytes_total, segments_done=EXCLUDED.segments_done,
		segments_total=EXCLUDED.segments_total, files=EXCLUDED.files, error=EXCLUDED.error, updated_at=now()
		WHERE downloads.protocol='torrent'`, job.ID, releaseID, job.Title, job.Status, job.BytesDone,
		job.BytesTotal, job.SegmentsDone, job.SegmentsTotal, files, job.Error)
	if err != nil {
		return Job{}, dbError("save torrent acquisition", err)
	}
	return m.jobByID(ctx, m.pool, job.ID)
}

func (m *Manager) ImportMode(ctx context.Context, jobID, requested string) (string, error) {
	if jobID == "" {
		return requested, nil
	}
	job, err := m.jobByID(ctx, m.pool, jobID)
	if err != nil {
		return "", err
	}
	if job.Protocol == "torrent" && (requested == "move" || requested == "") {
		return "hardlink", nil
	}
	return requested, nil
}
