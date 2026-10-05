package music

import (
	"errors"
	"time"
)

var (
	ErrNotFound      = errors.New("music: not found")
	ErrInvalid       = errors.New("music: invalid request")
	ErrConflict      = errors.New("music: the operation conflicts with current state")
	ErrNotConfigured = errors.New("music: the provider is not configured")
	ErrUnsafe        = errors.New("music: unsafe path")
)

type RootFolder struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

// QualityProfile lists formats in preference order; the first format is the cutoff origin.
type QualityProfile struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Formats        []string `json:"formats"`
	LosslessOnly   bool     `json:"losslessOnly"`
	MinBitrateKbps int      `json:"minBitrateKbps"`
	MinMB          float64  `json:"minMB"`
	MaxMB          float64  `json:"maxMB"`
	Cutoff         string   `json:"cutoff"`
	Upgrade        bool     `json:"upgrade"`
}

type Config struct {
	RootFolders       []RootFolder     `json:"rootFolders"`
	FolderTemplate    string           `json:"folderTemplate"`
	FileTemplate      string           `json:"fileTemplate"`
	ImportMode        string           `json:"importMode"`
	FFprobePath       string           `json:"ffprobePath"`
	MusicBrainzURL    string           `json:"musicBrainzURL"`
	MusicBrainzRateMs int              `json:"musicBrainzRateMs"`
	CoverArtURL       string           `json:"coverArtURL"`
	PollMinutes       int              `json:"pollMinutes"`
	SearchHours       int              `json:"searchHours"`
	RetryFailed       bool             `json:"retryFailed"`
	QualityProfiles   []QualityProfile `json:"qualityProfiles"`
	DefaultProfileID  string           `json:"defaultProfileId"`
	// Computed fields describe the running environment; they are not stored.
	MusicBrainzReady bool `json:"musicBrainzReady"`
	FFprobeAvailable bool `json:"ffprobeAvailable"`
}

type Artist struct {
	ID             string     `json:"id"`
	MusicBrainzID  string     `json:"musicBrainzId"`
	Name           string     `json:"name"`
	SortName       string     `json:"sortName"`
	Disambiguation string     `json:"disambiguation"`
	Country        string     `json:"country"`
	Type           string     `json:"type"`
	Monitored      bool       `json:"monitored"`
	MonitorOption  string     `json:"monitorOption"`
	ProfileID      string     `json:"profileId"`
	RootID         string     `json:"rootId"`
	Albums         []Album    `json:"albums,omitempty"`
	Status         string     `json:"status"`
	Error          string     `json:"error"`
	AddedAt        time.Time  `json:"addedAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	LastSearchAt   *time.Time `json:"lastSearchAt"`
	LastRefreshAt  *time.Time `json:"lastRefreshAt"`
}

type TrackFile struct {
	RootID      string    `json:"rootId"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	Format      string    `json:"format"`
	BitrateKbps int       `json:"bitrateKbps"`
	Lossless    bool      `json:"lossless"`
	Score       int       `json:"score"`
	ImportedAt  time.Time `json:"importedAt"`
	Missing     bool      `json:"missing,omitempty"`
}

type Track struct {
	ID            string     `json:"id"`
	MusicBrainzID string     `json:"musicBrainzId"`
	Disc          int        `json:"disc"`
	Number        int        `json:"number"`
	Title         string     `json:"title"`
	Artist        string     `json:"artist"`
	DurationMS    int        `json:"durationMs"`
	File          *TrackFile `json:"file,omitempty"`
	Missing       bool       `json:"missing,omitempty"`
}

type File struct {
	RootID      string    `json:"rootId"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	Format      string    `json:"format"`
	BitrateKbps int       `json:"bitrateKbps"`
	Lossless    bool      `json:"lossless"`
	Score       int       `json:"score"`
	Disc        int       `json:"disc"`
	Number      int       `json:"number"`
	TrackTitle  string    `json:"trackTitle"`
	ImportedAt  time.Time `json:"importedAt"`
	Missing     bool      `json:"missing,omitempty"`
}

type Album struct {
	ID            string     `json:"id"`
	ArtistID      string     `json:"artistId"`
	ArtistName    string     `json:"artistName"`
	MusicBrainzID string     `json:"musicBrainzId"`
	Title         string     `json:"title"`
	Year          int        `json:"year"`
	ReleaseDate   string     `json:"releaseDate"`
	Type          string     `json:"type"`
	Monitored     bool       `json:"monitored"`
	ProfileID     string     `json:"profileId"`
	RootID        string     `json:"rootId"`
	Tracks        []Track    `json:"tracks"`
	Files         []File     `json:"files"`
	Format        string     `json:"format"`
	Score         int        `json:"score"`
	Size          int64      `json:"size"`
	CoverPath     string     `json:"coverPath"`
	Status        string     `json:"status"`
	Error         string     `json:"error"`
	AddedAt       time.Time  `json:"addedAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	LastSearchAt  *time.Time `json:"lastSearchAt"`
}

type Release struct {
	ID        string    `json:"id"`
	Protocol  string    `json:"protocol,omitempty"`
	Source    string    `json:"source,omitempty"`
	Seeders   int       `json:"seeders,omitempty"`
	Title     string    `json:"title"`
	Size      int64     `json:"size"`
	Published time.Time `json:"published"`
	Artist    string    `json:"artist"`
	Album     string    `json:"album"`
	Year      int       `json:"year"`
	Decision  Decision  `json:"decision"`
	Error     string    `json:"error,omitempty"`
}

type Decision struct {
	Format      string   `json:"format"`
	BitrateKbps int      `json:"bitrateKbps"`
	Lossless    bool     `json:"lossless"`
	Media       string   `json:"media"`
	Score       int      `json:"score"`
	Rank        int      `json:"rank"`
	Allowed     bool     `json:"allowed"`
	Upgrade     bool     `json:"upgrade"`
	Reasons     []string `json:"reasons"`
}

type Acquisition struct {
	AlbumID   string   `json:"albumId"`
	JobID     string   `json:"jobId"`
	ReleaseID string   `json:"releaseId"`
	Title     string   `json:"title"`
	Decision  Decision `json:"decision"`
	Override  bool     `json:"override,omitempty"`
	Attempts  int      `json:"attempts,omitempty"`
	Status    string   `json:"status"`
	Error     string   `json:"error"`
}

type History struct {
	ID        int64     `json:"id"`
	AlbumID   string    `json:"albumId"`
	ArtistID  string    `json:"artistId"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

type CalendarEntry struct {
	ID          string `json:"id"`
	AlbumID     string `json:"albumId"`
	ArtistID    string `json:"artistId"`
	Title       string `json:"title"`
	ArtistName  string `json:"artistName"`
	ReleaseDate string `json:"releaseDate"`
	Type        string `json:"type"`
}

type Candidate struct {
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	Artist         string `json:"artist"`
	Album          string `json:"album"`
	Year           int    `json:"year"`
	Discs          int    `json:"discs"`
	Tracks         int    `json:"tracks"`
	Format         string `json:"format"`
	BitrateKbps    int    `json:"bitrateKbps"`
	MatchedAlbumID string `json:"matchedAlbumId,omitempty"`
	Error          string `json:"error,omitempty"`
}

type ArtistResult struct {
	MusicBrainzID  string `json:"musicBrainzId"`
	Name           string `json:"name"`
	SortName       string `json:"sortName"`
	Disambiguation string `json:"disambiguation"`
	Country        string `json:"country"`
	Type           string `json:"type"`
	Score          int    `json:"score"`
	InLibrary      bool   `json:"inLibrary,omitempty"`
}

type AlbumResult struct {
	MusicBrainzID string `json:"musicBrainzId"`
	Title         string `json:"title"`
	ArtistName    string `json:"artistName"`
	ArtistID      string `json:"artistId"`
	Year          int    `json:"year"`
	Type          string `json:"type"`
	Score         int    `json:"score"`
	InLibrary     bool   `json:"inLibrary,omitempty"`
}

type DiscoverResult struct {
	Artists []ArtistResult `json:"artists"`
	Albums  []AlbumResult  `json:"albums"`
}

type AddArtistInput struct {
	MusicBrainzID string `json:"musicBrainzId"`
	Name          string `json:"name"`
	Monitored     bool   `json:"monitored"`
	MonitorOption string `json:"monitorOption"`
	ProfileID     string `json:"profileId"`
	RootID        string `json:"rootId"`
}

type AddAlbumInput struct {
	ArtistID      string `json:"artistId"`
	MusicBrainzID string `json:"musicBrainzId"`
	Title         string `json:"title"`
	Year          int    `json:"year"`
	Monitored     bool   `json:"monitored"`
	ProfileID     string `json:"profileId"`
	RootID        string `json:"rootId"`
}

type MonitorInput struct {
	Monitored     bool   `json:"monitored"`
	MonitorOption string `json:"monitorOption"`
}

type AlbumMonitorInput struct {
	Monitored bool   `json:"monitored"`
	ProfileID string `json:"profileId"`
}

type ImportInput struct {
	RootID  string `json:"rootId"`
	Path    string `json:"path"`
	AlbumID string `json:"albumId"`
}

type SyncResult struct {
	Searched  int `json:"searched"`
	Queued    int `json:"queued"`
	Imported  int `json:"imported"`
	Refreshed int `json:"refreshed"`
}

type RenameFile struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type RenameResult struct {
	Files   []RenameFile `json:"files"`
	Applied bool         `json:"applied"`
}

type TestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type ConnectionTests struct {
	MusicBrainz TestResult `json:"musicBrainz"`
	Indexer     TestResult `json:"indexer"`
	FFprobe     TestResult `json:"ffprobe"`
}
