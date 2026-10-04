package tv

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("TV record not found")
	ErrInvalid  = errors.New("invalid TV request")
	ErrConflict = errors.New("TV operation conflicts with current state")
)

type Config struct {
	RootFolders    []movies.RootFolder `json:"rootFolders"`
	FolderTemplate string              `json:"folderTemplate"`
	FileTemplate   string              `json:"fileTemplate"`
	ImportMode     string              `json:"importMode"`
	WriteNFO       bool                `json:"writeNFO"`
	PollMinutes    int                 `json:"pollMinutes"`
	SearchHours    int                 `json:"searchHours"`
	RetryFailed    bool                `json:"retryFailed"`
}

type Series struct {
	ID            string         `json:"id"`
	Metadata      metadata.Title `json:"metadata"`
	Monitored     bool           `json:"monitored"`
	MonitorMode   string         `json:"monitorMode"`
	ProfileID     string         `json:"profileId"`
	RootID        string         `json:"rootId"`
	Tags          []string       `json:"tags"`
	AddedAt       time.Time      `json:"addedAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
	LastRefreshAt *time.Time     `json:"lastRefreshAt"`
	Error         string         `json:"error"`
	Episodes      []Episode      `json:"episodes,omitempty"`
	Status        string         `json:"status"`
	Downloaded    int            `json:"downloaded"`
	Total         int            `json:"total"`
	Wanted        int            `json:"wanted"`
}

type Episode struct {
	ID           string        `json:"id"`
	SeriesID     string        `json:"seriesId"`
	IMDbID       string        `json:"imdbId"`
	Title        string        `json:"title"`
	Season       int           `json:"season"`
	Number       int           `json:"number"`
	AirDate      string        `json:"airDate"`
	Rating       *float64      `json:"rating"`
	Monitored    bool          `json:"monitored"`
	Files        []movies.File `json:"files"`
	LastSearchAt *time.Time    `json:"lastSearchAt"`
	Error        string        `json:"error"`
	Status       string        `json:"status"`
}

type AddInput struct {
	IMDbID      string         `json:"imdbId"`
	Metadata    metadata.Title `json:"metadata"`
	Monitored   bool           `json:"monitored"`
	MonitorMode string         `json:"monitorMode"`
	ProfileID   string         `json:"profileId"`
	RootID      string         `json:"rootId"`
	Tags        []string       `json:"tags"`
}

type BulkInput struct {
	IDs         []string  `json:"ids"`
	Monitored   *bool     `json:"monitored"`
	MonitorMode *string   `json:"monitorMode"`
	ProfileID   *string   `json:"profileId"`
	RootID      *string   `json:"rootId"`
	Tags        *[]string `json:"tags"`
}

type MonitorInput struct {
	Season     *int     `json:"season"`
	EpisodeIDs []string `json:"episodeIds"`
	Monitored  bool     `json:"monitored"`
}

type Target struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

type GrabInput struct {
	Target
	ReleaseID string `json:"releaseId"`
	Override  bool   `json:"override"`
}

type Acquisition struct {
	SeriesID   string           `json:"seriesId"`
	EpisodeIDs []string         `json:"episodeIds"`
	JobID      string           `json:"jobId"`
	ReleaseID  string           `json:"releaseId"`
	Title      string           `json:"title"`
	Decision   quality.Decision `json:"decision"`
	Override   bool             `json:"override"`
	Status     string           `json:"status"`
	Error      string           `json:"error"`
}

type Release struct {
	indexer.Release
	Decision   quality.Decision `json:"decision"`
	EpisodeIDs []string         `json:"episodeIds"`
	Pack       bool             `json:"pack"`
}

type History struct {
	ID        int64     `json:"id"`
	SeriesID  string    `json:"seriesId"`
	EpisodeID string    `json:"episodeId"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type CalendarEntry struct {
	SeriesID     string `json:"seriesId"`
	SeriesIMDbID string `json:"seriesIMDbId"`
	SeriesTitle  string `json:"seriesTitle"`
	Poster       string `json:"poster"`
	Episode
}

type Candidate struct {
	Path            string `json:"path"`
	Size            int64  `json:"size"`
	Title           string `json:"title"`
	Year            int    `json:"year"`
	IMDbID          string `json:"imdbId"`
	Quality         string `json:"quality"`
	Season          int    `json:"season"`
	Episodes        []int  `json:"episodes"`
	AirDate         string `json:"airDate"`
	MatchedSeriesID string `json:"matchedSeriesId,omitempty"`
	Error           string `json:"error,omitempty"`
}

type ImportInput struct {
	RootID   string `json:"rootId"`
	Path     string `json:"path"`
	SeriesID string `json:"seriesId"`
	Season   int    `json:"season"`
	Episodes []int  `json:"episodes"`
}

type Service struct {
	Store     *Store
	Shared    *movies.Store
	Downloads *downloads.Manager
	pool      *pgxpool.Pool
	lockSlots chan struct{}
	syncMu    sync.Mutex
	startOnce sync.Once
	cancel    context.CancelFunc
	workers   sync.WaitGroup
}
