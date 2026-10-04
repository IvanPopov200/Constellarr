package downloads

import (
	"context"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
)

func (m *Manager) SearchTV(ctx context.Context, imdbID, title string, season, episode int) ([]indexer.Release, error) {
	client := m.indexerClient()
	if client == nil {
		return nil, ErrNotConfigured
	}
	return client.SearchTV(ctx, imdbID, title, season, episode)
}

func (m *Manager) RSSTV(ctx context.Context) ([]indexer.Release, error) {
	client := m.indexerClient()
	if client == nil {
		return nil, ErrNotConfigured
	}
	return client.RSSTV(ctx)
}
