package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery"
	"github.com/IvanPopov200/Constellarr/backend/internal/music"
)

// musicSource adapts the music library to the discovery request interface.
type musicSource struct{ service *music.Service }

func (m musicSource) Search(ctx context.Context, query string, limit int) ([]discovery.MusicItem, error) {
	if m.service == nil {
		return nil, discovery.ErrNotConfigured
	}
	found, err := m.service.Discover(ctx, query, 1)
	if err != nil {
		return nil, requestError(err)
	}
	items := make([]discovery.MusicItem, 0, len(found.Albums))
	for _, album := range found.Albums {
		if limit > 0 && len(items) >= limit {
			break
		}
		item := discovery.MusicItem{
			ID: album.MusicBrainzID, Title: album.Title, Album: album.Title, Artist: album.ArtistName,
			Year: album.Year, Status: "wanted",
		}
		// Catalogued albums can still be missing files, so the library state decides availability.
		if album.InLibrary {
			resolved, err := m.Status(ctx, album.MusicBrainzID)
			if err != nil {
				return nil, err
			}
			item.Available, item.Status, item.Error = resolved.Available, resolved.Status, resolved.Error
		}
		items = append(items, item)
	}
	return items, nil
}

// Add stores exactly the selected release group; artists are created unmonitored.
func (m musicSource) Add(ctx context.Context, input discovery.MusicAddInput) (discovery.MusicItem, error) {
	if m.service == nil {
		return discovery.MusicItem{}, discovery.ErrNotConfigured
	}
	album, err := m.service.AddReleaseGroup(ctx, music.AddReleaseGroupInput{
		MusicBrainzID: input.ID, Title: input.Title, Year: input.Year,
	})
	if err != nil {
		return discovery.MusicItem{}, requestError(err)
	}
	return itemFromAlbum(album), nil
}

func (m musicSource) Calendar(ctx context.Context) ([]discovery.MusicRelease, error) {
	if m.service == nil {
		return nil, discovery.ErrNotConfigured
	}
	entries, err := m.service.Calendar(ctx)
	if err != nil {
		return nil, requestError(err)
	}
	releases := make([]discovery.MusicRelease, 0, len(entries))
	for _, entry := range entries {
		releases = append(releases, discovery.MusicRelease{
			ID: entry.AlbumID, Album: entry.Title, Title: entry.Title, Artist: entry.ArtistName,
			Type: entry.Type, ReleaseDate: entry.ReleaseDate, Year: yearOf(entry.ReleaseDate),
		})
	}
	return releases, nil
}

// Status resolves an album by library ID or release-group ID; a removed album stays requestable.
func (m musicSource) Status(ctx context.Context, id string) (discovery.MusicItem, error) {
	if m.service == nil {
		return discovery.MusicItem{}, discovery.ErrNotConfigured
	}
	album, err := m.service.RequestAlbum(ctx, id)
	if err != nil {
		if errors.Is(err, music.ErrNotFound) {
			return discovery.MusicItem{ID: id, Status: "wanted"}, nil
		}
		return discovery.MusicItem{}, requestError(err)
	}
	return itemFromAlbum(album), nil
}

// Sync lets discovery trigger the music monitor pass right after an approval.
func (m musicSource) Sync(ctx context.Context, force bool) error {
	if m.service == nil {
		return discovery.ErrNotConfigured
	}
	if _, err := m.service.Sync(ctx, force); err != nil {
		return requestError(err)
	}
	return nil
}

func itemFromAlbum(album music.Album) discovery.MusicItem {
	item := discovery.MusicItem{
		ID: album.ID, Title: album.Title, Album: album.Title, Artist: album.ArtistName, Year: album.Year,
	}
	switch album.Status {
	case "downloading":
		item.Status = "downloading"
	case "failed":
		item.Status = "failed"
		item.Error = album.Error
	case "unmonitored":
		item.Status = "unmonitored"
	case "available", "cutoff-unmet":
		item.Status = "available"
		item.Available = true
	default:
		item.Status = "wanted"
	}
	return item
}

// requestError keeps discovery's error signalling intact for request flows.
func requestError(err error) error {
	switch {
	case errors.Is(err, music.ErrNotConfigured):
		return discovery.ErrNotConfigured
	case errors.Is(err, music.ErrNotFound):
		return discovery.ErrNotFound
	case errors.Is(err, music.ErrInvalid):
		return discovery.ErrInvalid
	case errors.Is(err, music.ErrConflict):
		return fmt.Errorf("%w: %s", discovery.ErrConflict, sanitized(err))
	default:
		return err
	}
}

// sanitized trims a module message to a single bounded line.
func sanitized(err error) string {
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}

func yearOf(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	year := 0
	for _, digit := range date[:4] {
		if digit < '0' || digit > '9' {
			return 0
		}
		year = year*10 + int(digit-'0')
	}
	return year
}
