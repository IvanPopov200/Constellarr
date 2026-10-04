package movies

import (
	"errors"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
	"time"
)

var (
	ErrNotFound = errors.New("movie not found")
	ErrInvalid  = errors.New("invalid movie request")
	ErrConflict = errors.New("movie operation conflicts with current state")
)

type RootFolder struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type Config struct {
	RootFolders         []RootFolder `json:"rootFolders"`
	FolderTemplate      string       `json:"folderTemplate"`
	FileTemplate        string       `json:"fileTemplate"`
	ImportMode          string       `json:"importMode"`
	WriteNFO            bool         `json:"writeNFO"`
	PollMinutes         int          `json:"pollMinutes"`
	SearchHours         int          `json:"searchHours"`
	MinimumAvailability string       `json:"minimumAvailability"`
	RetryFailed         bool         `json:"retryFailed"`
	MetadataURL         string       `json:"metadataURL"`
	MetadataAPIKey      string       `json:"metadataAPIKey,omitempty"`
	MetadataConfigured  bool         `json:"metadataConfigured"`
	JellyfinURL         string       `json:"jellyfinURL"`
	JellyfinAPIKey      string       `json:"jellyfinAPIKey,omitempty"`
	JellyfinConfigured  bool         `json:"jellyfinConfigured"`
	WebhookURL          string       `json:"webhookURL"`
}

type File struct {
	RootID     string    `json:"rootId"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	Quality    string    `json:"quality"`
	Score      int       `json:"score"`
	ImportedAt time.Time `json:"importedAt"`
}

type Movie struct {
	ID           string         `json:"id"`
	Metadata     metadata.Title `json:"metadata"`
	Monitored    bool           `json:"monitored"`
	ProfileID    string         `json:"profileId"`
	RootID       string         `json:"rootId"`
	Tags         []string       `json:"tags"`
	Collection   string         `json:"collection"`
	Files        []File         `json:"files"`
	Status       string         `json:"status"`
	AddedAt      time.Time      `json:"addedAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
	LastSearchAt *time.Time     `json:"lastSearchAt"`
	Error        string         `json:"error"`
}

type History struct {
	ID        int64     `json:"id"`
	MovieID   string    `json:"movieId"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type Acquisition struct {
	MovieID   string           `json:"movieId"`
	JobID     string           `json:"jobId"`
	ReleaseID string           `json:"releaseId"`
	Title     string           `json:"title"`
	Decision  quality.Decision `json:"decision"`
	Status    string           `json:"status"`
	Error     string           `json:"error"`
}

type Watchlist struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	IMDbIDs       []string   `json:"imdbIds"`
	Monitor       bool       `json:"monitor"`
	ProfileID     string     `json:"profileId"`
	RootID        string     `json:"rootId"`
	IntervalHours int        `json:"intervalHours"`
	LastSyncAt    *time.Time `json:"lastSyncAt"`
	Error         string     `json:"error"`
}
