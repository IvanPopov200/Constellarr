package torrents

import (
	"context"
	"os"
	"path/filepath"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
)

// ImportMode keeps torrent data in place so imports never move files that are still seeding.
func (s *Service) ImportMode() string {
	return library.ModeLink
}

// CompletedJobs exposes finished torrents in the downloads job shape used by the shared import pipeline.
func (s *Service) CompletedJobs(ctx context.Context) ([]downloads.Job, error) {
	jobs, err := s.listJobs(ctx, s.pool, statusSeeding, statusCompleted)
	if err != nil {
		return nil, err
	}
	states, err := s.processingByJob(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	result := make([]downloads.Job, 0, len(jobs))
	for _, job := range jobs {
		row, ok := states[job.ID]
		if !ok {
			result = append(result, downloadJob(job, nil))
			continue
		}
		result = append(result, downloadJob(job, &row))
	}
	return result, nil
}

// DownloadJob exposes one job for the shared import pipeline; only importable data is reported complete.
func (s *Service) DownloadJob(ctx context.Context, id string) (downloads.Job, error) {
	job, err := s.jobByID(ctx, s.pool, id)
	if err != nil {
		return downloads.Job{}, err
	}
	var row *processingRow
	if stored, err := s.loadProcessing(ctx, s.pool, id); err == nil {
		row = &stored
	}
	return downloadJob(s.liveJob(job), row), nil
}

// OutputDirectory is the directory scanned by library imports: extracted media when available.
func (s *Service) OutputDirectory(jobID string) (string, error) {
	job, err := s.jobByID(context.Background(), s.pool, jobID)
	if err != nil {
		return "", err
	}
	if row, err := s.loadProcessing(context.Background(), s.pool, jobID); err == nil && row.state == processCompleted {
		dir, err := s.processedDir(job.InfoHash)
		if err != nil {
			return "", ErrNotFound
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
		return "", ErrNotFound
	}
	dir, err := s.dataDir(job.InfoHash)
	if err != nil {
		return "", ErrNotFound
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", ErrNotFound
	}
	return dir, nil
}

// OpenFile serves either an extracted file or a payload file, whichever the job reports.
func (s *Service) OpenFile(ctx context.Context, id, name string) (*os.File, error) {
	job, err := s.jobByID(ctx, s.pool, id)
	if err != nil {
		return nil, err
	}
	if job.Status != statusCompleted && job.Status != statusSeeding {
		return nil, ErrConflict
	}
	rel, err := splitRel(name)
	if err != nil {
		return nil, ErrNotFound
	}
	declared := job.Files
	dir, err := s.dataDir(job.InfoHash)
	if err != nil {
		return nil, ErrNotFound
	}
	if row, err := s.loadProcessing(ctx, s.pool, id); err == nil && row.state == processCompleted && declaredFile(row.files, rel) {
		if processed, err := s.processedDir(job.InfoHash); err == nil {
			declared, dir = row.files, processed
		}
	}
	if !declaredFile(declared, rel) {
		return nil, ErrNotFound
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrNotFound
	}
	defer root.Close()
	handle, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return nil, ErrNotFound
	}
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		handle.Close()
		return nil, ErrNotFound
	}
	return handle, nil
}

func declaredFile(files []File, name string) bool {
	for _, file := range files {
		if file.Name == name {
			return true
		}
	}
	return false
}

// effectiveProcessing keeps the import gate independent of when the durable row is written.
func effectiveProcessing(job Job, row *processingRow) *processingRow {
	if row != nil {
		return row
	}
	if job.Status != statusCompleted && job.Status != statusSeeding {
		return nil
	}
	if len(job.Files) == 0 {
		return nil
	}
	decided := processingDecision(job.Files)
	return &decided
}

func downloadJob(job Job, row *processingRow) downloads.Job {
	row = effectiveProcessing(job, row)
	files := make([]downloads.OutputFile, 0, len(job.Files))
	for _, file := range job.Files {
		if job.Status != statusCompleted && job.Status != statusSeeding {
			continue
		}
		files = append(files, downloads.OutputFile{Name: file.Name, Size: file.Size, URL: torrentFileURL(job.ID, file.Name)})
	}
	if row != nil && row.state == processCompleted {
		files = files[:0]
		for _, file := range row.files {
			files = append(files, downloads.OutputFile{Name: file.Name, Size: file.Size, URL: torrentFileURL(job.ID, file.Name)})
		}
	}
	return downloads.Job{
		ID: job.ID, ReleaseID: job.ReleaseID, Title: firstNonEmpty(job.Title, job.Name),
		Status: downloadStatus(job.Status, row), BytesDone: job.BytesDone, BytesTotal: job.BytesTotal,
		SegmentsDone: job.PiecesDone, SegmentsTotal: job.PiecesTotal, CreatedAt: job.AddedAt,
		UpdatedAt: job.UpdatedAt, Error: downloadError(job, row), Files: files,
	}
}

// downloadStatus maps torrent states onto the vocabulary used by movie and TV imports.
func downloadStatus(status string, row *processingRow) string {
	switch status {
	case statusCancelled:
		return "cancelled"
	case statusPaused:
		return "paused"
	case statusCompleted, statusSeeding:
		if row != nil {
			switch row.state {
			case processPending, processRunning:
				return "extracting"
			case processFailed:
				return "failed"
			}
		}
		return "completed"
	case statusFailed:
		return "failed"
	default:
		return "downloading"
	}
}

func downloadError(job Job, row *processingRow) string {
	if row != nil && row.state == processFailed {
		return row.error
	}
	return job.Error
}
