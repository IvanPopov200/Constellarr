package torrents

import (
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("torrents: not found")
	ErrConflict      = errors.New("torrents: conflict")
	ErrInvalid       = errors.New("torrents: invalid input")
	ErrNotConfigured = errors.New("torrents: source is not configured")
	ErrSchema        = errors.New("torrents: database schema is missing; start the downloads service first")
)

// invalidError reports a user-fixable problem and maps to HTTP 400.
type invalidError string

func (e invalidError) Error() string { return string(e) }

func (e invalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(message string) error { return invalidError("torrents: " + message) }

const (
	statusQueued      = "queued"
	statusMetadata    = "metadata"
	statusChecking    = "checking"
	statusDownloading = "downloading"
	statusSeeding     = "seeding"
	statusPaused      = "paused"
	statusCompleted   = "completed"
	statusFailed      = "failed"
	statusCancelled   = "cancelled"

	sourceMagnet  = "magnet"
	sourceFile    = "file"
	sourceTorznab = "torznab"

	listLimit = 100
)

type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Done int64  `json:"done"`
	URL  string `json:"url,omitempty"`
}

type Job struct {
	ID                   string      `json:"id"`
	InfoHash             string      `json:"infoHash"`
	Name                 string      `json:"name"`
	Title                string      `json:"title,omitempty"`
	ReleaseID            string      `json:"releaseId,omitempty"`
	Source               string      `json:"source"`
	Status               string      `json:"status"`
	Private              bool        `json:"private"`
	BytesDone            int64       `json:"bytesDone"`
	BytesTotal           int64       `json:"bytesTotal"`
	Uploaded             int64       `json:"uploaded"`
	DownloadRate         int64       `json:"downloadRate"`
	UploadRate           int64       `json:"uploadRate"`
	Ratio                float64     `json:"ratio"`
	Peers                int         `json:"peers"`
	Seeds                int         `json:"seeds"`
	PiecesDone           int         `json:"piecesDone"`
	PiecesTotal          int         `json:"piecesTotal"`
	ETASeconds           int64       `json:"etaSeconds"`
	SeedRatioLimit       float64     `json:"seedRatioLimit"`
	SeedTimeLimitMinutes int         `json:"seedTimeLimitMinutes"`
	SeedingElapsed       int64       `json:"seedingElapsedSeconds"`
	AddedAt              time.Time   `json:"addedAt"`
	UpdatedAt            time.Time   `json:"updatedAt"`
	CompletedAt          *time.Time  `json:"completedAt,omitempty"`
	Error                string      `json:"error,omitempty"`
	Files                []File      `json:"files"`
	Processing           *Processing `json:"processing,omitempty"`
}

type Peer struct {
	Address   string  `json:"address"`
	Client    string  `json:"client,omitempty"`
	Direction string  `json:"direction"`
	Progress  float64 `json:"progress"`
}

type Detail struct {
	Job   Job    `json:"job"`
	Peers []Peer `json:"peers"`
}

type SearchResult struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"sourceId"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	Size      int64     `json:"size"`
	Seeders   int       `json:"seeders"`
	Leechers  int       `json:"leechers"`
	Published time.Time `json:"published,omitempty"`
	Category  string    `json:"category,omitempty"`
	Magnet    string    `json:"magnet,omitempty"`
}

type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Errors  []SearchError  `json:"errors,omitempty"`
}

type SearchError struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

type Source struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	URL              string   `json:"url"`
	Categories       []int    `json:"categories"`
	Enabled          bool     `json:"enabled"`
	APIKeyConfigured bool     `json:"apiKeyConfigured"`
	Caps             []string `json:"caps,omitempty"`
}

// SourceInput carries a Torznab source; an empty APIKey keeps the stored key.
type SourceInput struct {
	Name       string
	URL        string
	APIKey     string
	Categories []int
	Enabled    bool
}

type SourceTest struct {
	OK    bool     `json:"ok"`
	Caps  []string `json:"caps"`
	Error string   `json:"error,omitempty"`
}

type Settings struct {
	Directory            string  `json:"directory"`
	ListenPort           int     `json:"listenPort"`
	DHTEnabled           bool    `json:"dhtEnabled"`
	PEXEnabled           bool    `json:"pexEnabled"`
	MaxActiveJobs        int     `json:"maxActiveJobs"`
	DownloadLimitKBps    int     `json:"downloadLimitKBps"`
	UploadLimitKBps      int     `json:"uploadLimitKBps"`
	SeedRatioLimit       float64 `json:"seedRatioLimit"`
	SeedTimeLimitMinutes int     `json:"seedTimeLimitMinutes"`
	RestartRequired      bool    `json:"restartRequired,omitempty"`
}

type SettingsUpdate struct {
	ListenPort           int
	DHTEnabled           bool
	PEXEnabled           bool
	MaxActiveJobs        int
	DownloadLimitKBps    int
	UploadLimitKBps      int
	SeedRatioLimit       float64
	SeedTimeLimitMinutes int
}

type Health struct {
	OK             bool   `json:"ok"`
	Started        bool   `json:"started"`
	ListenPort     int    `json:"listenPort"`
	DHTEnabled     bool   `json:"dhtEnabled"`
	PEXEnabled     bool   `json:"pexEnabled"`
	ActiveJobs     int    `json:"activeJobs"`
	QueuedJobs     int    `json:"queuedJobs"`
	FailedJobs     int    `json:"failedJobs"`
	ProcessingJobs int    `json:"processingJobs"`
	Error          string `json:"error,omitempty"`
}
