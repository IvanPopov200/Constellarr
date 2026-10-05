package discovery

import "context"

// musicSyncer is the optional hook a music library uses to run its monitor pass after an approval.
type musicSyncer interface {
	Sync(ctx context.Context, force bool) error
}

// MusicLibrary is the hook the music module implements; a nil hook reports music as unavailable.
type MusicLibrary interface {
	Search(ctx context.Context, query string, limit int) ([]MusicItem, error)
	Add(ctx context.Context, input MusicAddInput) (MusicItem, error)
	Calendar(ctx context.Context) ([]MusicRelease, error)
	Status(ctx context.Context, id string) (MusicItem, error)
}

type MusicItem struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	Year      int    `json:"year"`
	Poster    string `json:"poster"`
	Available bool   `json:"available"`
	Status    string `json:"status"`
	Error     string `json:"error"`
}

type MusicRelease struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	Type        string `json:"type"`
	ReleaseDate string `json:"releaseDate"`
	Year        int    `json:"year"`
	Poster      string `json:"poster"`
}

type MusicAddInput struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Year   int    `json:"year"`
}
