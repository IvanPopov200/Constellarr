package main

import (
	"context"
	"errors"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/torrents"
)

type torrentSource struct{ service *torrents.Service }

func (s torrentSource) Search(ctx context.Context, query string) ([]indexer.Release, error) {
	response, err := s.service.Search(ctx, query, "")
	if err != nil {
		return nil, torrentError(err)
	}
	items := make([]indexer.Release, 0, len(response.Results))
	for _, result := range response.Results {
		items = append(items, indexer.Release{
			ID:    downloads.TorrentPrefix + result.SourceID + "_" + result.ID,
			Title: result.Title, Size: result.Size, Published: result.Published,
			Protocol: "torrent", Source: result.Source, Seeders: result.Seeders,
		})
	}
	if len(items) == 0 && len(response.Errors) > 0 {
		return nil, errors.New("torrent indexers could not complete the search")
	}
	return items, nil
}

func (s torrentSource) Add(ctx context.Context, releaseID, title string) (downloads.Job, error) {
	sourceID, resultID, ok := strings.Cut(strings.TrimPrefix(releaseID, downloads.TorrentPrefix), "_")
	if !ok {
		return downloads.Job{}, downloads.ErrInvalid
	}
	job, err := s.service.AddFromResult(ctx, sourceID, resultID, title)
	if err != nil {
		return downloads.Job{}, torrentError(err)
	}
	return s.Get(ctx, job.ID)
}

func (s torrentSource) Get(ctx context.Context, id string) (downloads.Job, error) {
	job, err := s.service.DownloadJob(ctx, id)
	return job, torrentError(err)
}

func (s torrentSource) Retry(ctx context.Context, id string) (downloads.Job, error) {
	if _, err := s.service.Resume(ctx, id); err != nil {
		return downloads.Job{}, torrentError(err)
	}
	return s.Get(ctx, id)
}

func (s torrentSource) OutputDirectory(id string) (string, error) {
	path, err := s.service.OutputDirectory(id)
	return path, torrentError(err)
}

func torrentError(err error) error {
	switch {
	case errors.Is(err, torrents.ErrNotFound):
		return downloads.ErrNotFound
	case errors.Is(err, torrents.ErrConflict):
		return downloads.ErrConflict
	case errors.Is(err, torrents.ErrNotConfigured):
		return downloads.ErrNotConfigured
	case errors.Is(err, torrents.ErrInvalid):
		return downloads.ErrInvalid
	}
	return err
}
