package downloads

import (
	"context"
	"errors"
	"fmt"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"os"
	"path/filepath"
	"time"
)

func (m *Manager) SearchMovie(ctx context.Context, imdbID, title string, year int) ([]indexer.Release, error) {
	query := title
	if year > 0 {
		query = fmt.Sprintf("%s %d", title, year)
	}
	return m.searchMedia(ctx, query, func() ([]indexer.Release, error) {
		client := m.indexerClient()
		if client == nil {
			return nil, ErrNotConfigured
		}
		return client.SearchMovie(ctx, imdbID, title, year)
	})
}

func (m *Manager) openLibraryFile(ctx context.Context, id, name string) (*os.File, error) {
	var directory, relative string
	err := m.pool.QueryRow(ctx, `SELECT root_path,path FROM download_library_files WHERE job_id=$1 AND name=$2 AND ready=true`, id, name).Scan(&directory, &relative)
	if err != nil || !filepath.IsAbs(directory) || !filepath.IsLocal(relative) {
		return nil, ErrNotFound
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrNotFound
	}
	defer root.Close()
	file, err := root.Open(relative)
	if err != nil {
		return nil, ErrNotFound
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		file.Close()
		return nil, ErrNotFound
	}
	return file, nil
}

func (m *Manager) RSS(ctx context.Context) ([]indexer.Release, error) {
	client := m.indexerClient()
	if client == nil {
		return nil, ErrNotConfigured
	}
	return client.RSS(ctx)
}

func (m *Manager) UnlinkedMovies(ctx context.Context) ([]Job, error) {
	rows, err := m.pool.Query(ctx, `SELECT `+jobColumns+` FROM downloads
	 WHERE status = 'completed' AND movie_adopted = false AND media_type <> 'tv' AND NOT EXISTS
	 (SELECT 1 FROM movie_acquisitions WHERE job_id = downloads.id) ORDER BY created_at LIMIT 20`)
	if err != nil {
		return nil, dbError("load unimported movies", err)
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, dbError("load unimported movies", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (m *Manager) OutputDirectory(id string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := m.jobByID(ctx, m.pool, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if job.Protocol == "torrent" {
		source := m.torrentSource()
		if source == nil {
			return "", ErrNotConfigured
		}
		return source.OutputDirectory(id)
	}
	directory, ok := m.jobDir(id, "output")
	if !ok {
		return "", ErrInvalid
	}
	return directory, nil
}
