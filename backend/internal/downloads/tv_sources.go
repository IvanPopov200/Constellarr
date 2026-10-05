package downloads

import (
	"context"
	"fmt"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
)

func (m *Manager) SearchTV(ctx context.Context, imdbID, title string, season, episode int) ([]indexer.Release, error) {
	query := title
	if season > 0 {
		query += fmt.Sprintf(" S%02d", season)
	}
	if episode > 0 {
		query += fmt.Sprintf("E%02d", episode)
	}
	return m.searchMedia(ctx, query, func() ([]indexer.Release, error) {
		client := m.indexerClient()
		if client == nil {
			return nil, ErrNotConfigured
		}
		return client.SearchTV(ctx, imdbID, title, season, episode)
	})
}

func (m *Manager) RSSTV(ctx context.Context) ([]indexer.Release, error) {
	client := m.indexerClient()
	if client == nil {
		return nil, ErrNotConfigured
	}
	return client.RSSTV(ctx)
}
