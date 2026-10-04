package movies

import (
	"context"
	"sync"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

type Service struct {
	Store     *Store
	Downloads *downloads.Manager
	syncMu    sync.Mutex
	startOnce sync.Once
	cancel    context.CancelFunc
	workers   sync.WaitGroup
}

type Release struct {
	indexer.Release
	Decision quality.Decision `json:"decision"`
}
type Candidate struct {
	library.Candidate
	MatchedMovieID string `json:"matchedMovieId,omitempty"`
	Error          string `json:"error,omitempty"`
}

func (s *Service) Get(ctx context.Context, id string) (Movie, error) {
	movie, err := s.Store.Get(ctx, id)
	if err != nil {
		return Movie{}, err
	}
	return s.decorated(ctx, movie)
}
