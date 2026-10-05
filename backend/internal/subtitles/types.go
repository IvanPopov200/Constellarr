// Package subtitles inventories, downloads, synchronizes, and translates subtitle sidecars for catalog videos.
package subtitles

import (
	"errors"
	"time"
)

var (
	ErrNotFound         = errors.New("subtitle record not found")
	ErrInvalid          = errors.New("invalid subtitle request")
	ErrConflict         = errors.New("subtitle operation conflicts with current state")
	ErrUnsafe           = errors.New("unsafe subtitle path")
	ErrNotConfigured    = errors.New("no subtitle provider is configured")
	ErrNoTranslator     = errors.New("subtitle translation is not configured")
	ErrQuota            = errors.New("subtitle provider quota exceeded")
	ErrRateLimited      = errors.New("subtitle provider rate limited")
	ErrUnavailable      = errors.New("subtitle helper unavailable")
	ErrTimeout          = errors.New("subtitle operation timed out")
	ErrBudget           = errors.New("translation budget exhausted")
	ErrCueIntegrity     = errors.New("translated cues failed integrity checks")
	ErrLanguageMismatch = errors.New("translated cues report a different language")
	ErrLowQualitySync   = errors.New("subtitle alignment looks unreliable")
)

const (
	KindMovie   = "movie"
	KindEpisode = "episode"

	maxLanguageBytes = 16
	maxProviderBytes = 64
	maxPathBytes     = 4096
	maxTextRunes     = 500
	maxHistoryLimit  = 200
	maxJobLimit      = 50
	maxPayloadBytes  = 512 << 10
	maxSubtitleBytes = 8 << 20
	maxSearchResults = 100
)

// LanguagePreference describes one wanted language variant of a video.
type LanguagePreference struct {
	Code string `json:"code"`
	// Forced also wants a forced-only (foreign parts) sidecar.
	Forced bool `json:"forced"`
	// HI requires a hearing-impaired sidecar instead of the plain variant.
	HI bool `json:"hi"`
}

// Profile is a reusable set of desired subtitle languages with a cutoff.
type Profile struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Languages []LanguagePreference `json:"languages"`
	// Cutoff stops automatic searching once this many languages are satisfied; zero means all.
	Cutoff int `json:"cutoff"`
}

// Provider is one subtitle provider account. Password and APIKey are write-only.
type Provider struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Endpoint string `json:"endpoint"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
	APIKey   string `json:"apiKey,omitempty"`
	Enabled  bool   `json:"enabled"`
	// PasswordSet and APIKeySet are computed for responses and ignored on save.
	PasswordSet bool `json:"passwordSet,omitempty"`
	APIKeySet   bool `json:"apiKeySet,omitempty"`
}

// SyncConfig bounds helper execution and alignment quality.
type SyncConfig struct {
	HelperPath             string  `json:"helperPath"`
	FFmpegPath             string  `json:"ffmpegPath"`
	TimeoutSeconds         int     `json:"timeoutSeconds"`
	MaxOffsetSeconds       float64 `json:"maxOffsetSeconds"`
	MinScore               float64 `json:"minScore"`
	QualityMaxOffsetSecs   float64 `json:"qualityMaxOffsetSeconds"`
	MaxFramerateDeviation  float64 `json:"maxFramerateDeviation"`
	VAD                    string  `json:"vad"`
	AudioReferenceSeconds  int     `json:"audioReferenceSeconds"`
	MaxEmbeddedStreamIndex int     `json:"maxEmbeddedStreamIndex"`
}

// AIConfig configures the OpenAI-compatible translation service.
type AIConfig struct {
	Enabled          bool    `json:"enabled"`
	BaseURL          string  `json:"baseURL"`
	APIKey           string  `json:"apiKey,omitempty"`
	APIKeySet        bool    `json:"apiKeySet,omitempty"`
	Model            string  `json:"model"`
	TimeoutSeconds   int     `json:"timeoutSeconds"`
	MaxTokens        int     `json:"maxTokens"`
	MaxRequests      int     `json:"maxRequests"`
	MaxTotalTokens   int     `json:"maxTotalTokens"`
	MaxCharacters    int     `json:"maxCharacters"`
	Temperature      float64 `json:"temperature"`
	OverrideExisting bool    `json:"overrideExisting"`
}

// Config is the persisted subtitle service configuration.
type Config struct {
	Enabled                bool       `json:"enabled"`
	AutoSearch             bool       `json:"autoSearch"`
	AutoDownload           bool       `json:"autoDownload"`
	ScanMinutes            int        `json:"scanMinutes"`
	SearchIntervalHours    int        `json:"searchIntervalHours"`
	RetryMinutes           int        `json:"retryMinutes"`
	CutoffScore            int        `json:"cutoffScore"`
	ProviderTimeoutSeconds int        `json:"providerTimeoutSeconds"`
	DefaultProfileID       string     `json:"defaultProfileId"`
	Providers              []Provider `json:"providers"`
	Sync                   SyncConfig `json:"sync"`
	AI                     AIConfig   `json:"ai"`
}

// ProviderStatus reports provider health without exposing credentials.
type ProviderStatus struct {
	ProviderID     string `json:"providerId"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Configured     bool   `json:"configured"`
	Enabled        bool   `json:"enabled"`
	LastError      string `json:"lastError,omitempty"`
	QuotaRemaining *int   `json:"quotaRemaining,omitempty"`
	QuotaReset     string `json:"quotaReset,omitempty"`
}

// Video is a catalog-owned video file resolved through the movie or TV services.
type Video struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Title        string `json:"title"`
	SeriesTitle  string `json:"seriesTitle,omitempty"`
	Year         int    `json:"year"`
	IMDbID       string `json:"imdbId"`
	SeriesIMDbID string `json:"seriesImdbId,omitempty"`
	Season       int    `json:"season,omitempty"`
	Episode      int    `json:"episode,omitempty"`
	RootID       string `json:"rootId"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`

	rootPath string
}

// Sidecar is one subtitle file stored next to a catalog video.
type Sidecar struct {
	Kind      string    `json:"kind"`
	VideoID   string    `json:"videoId"`
	Path      string    `json:"path"`
	Language  string    `json:"language"`
	Format    string    `json:"format"`
	Forced    bool      `json:"forced"`
	HI        bool      `json:"hi"`
	Source    string    `json:"source"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Wanted is the search state of one wanted language variant.
type Wanted struct {
	Kind          string     `json:"kind"`
	VideoID       string     `json:"videoId"`
	Language      string     `json:"language"`
	Forced        bool       `json:"forced"`
	HI            bool       `json:"hi"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	LastAttemptAt *time.Time `json:"lastAttemptAt"`
	NextAttemptAt *time.Time `json:"nextAttemptAt"`
	Error         string     `json:"error"`
}

// Assignment binds a language profile and monitoring flag to one video.
type Assignment struct {
	Kind      string `json:"kind"`
	VideoID   string `json:"videoId"`
	ProfileID string `json:"profileId"`
	Monitored bool   `json:"monitored"`
}

// HistoryEntry is one durable subtitle action record.
type HistoryEntry struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	VideoID   string    `json:"videoId"`
	Action    string    `json:"action"`
	Language  string    `json:"language"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

// Job is a durable background operation.
type Job struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	VideoKind string    `json:"videoKind"`
	VideoID   string    `json:"videoId"`
	Language  string    `json:"language"`
	Status    string    `json:"status"`
	Progress  int       `json:"progress"`
	Detail    string    `json:"detail"`
	Error     string    `json:"error"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Output is a proposed subtitle awaiting review and application.
type Output struct {
	ID         string     `json:"id"`
	JobID      string     `json:"jobId"`
	VideoKind  string     `json:"videoKind"`
	VideoID    string     `json:"videoId"`
	Language   string     `json:"language"`
	Forced     bool       `json:"forced"`
	HI         bool       `json:"hi"`
	Format     string     `json:"format"`
	OriginPath string     `json:"originPath"`
	TargetPath string     `json:"targetPath"`
	Payload    string     `json:"payload,omitempty"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail"`
	CreatedAt  time.Time  `json:"createdAt"`
	AppliedAt  *time.Time `json:"appliedAt"`
}

// Result is one provider search result.
type Result struct {
	ProviderID   string  `json:"providerId"`
	ProviderName string  `json:"providerName"`
	FileID       string  `json:"fileId"`
	SubtitleID   string  `json:"subtitleId"`
	Language     string  `json:"language"`
	Format       string  `json:"format"`
	Forced       bool    `json:"forced"`
	HI           bool    `json:"hi"`
	FPS          float64 `json:"fps"`
	Downloads    int     `json:"downloads"`
	Rating       float64 `json:"rating"`
	Release      string  `json:"release"`
	FileName     string  `json:"fileName"`
	MatchedBy    string  `json:"matchedBy"`
	Score        int     `json:"score"`
}

// LibraryItem is one catalog video with its subtitle state.
type LibraryItem struct {
	Video     Video     `json:"video"`
	Sidecars  []Sidecar `json:"sidecars"`
	Wanted    []Wanted  `json:"wanted"`
	Outputs   []Output  `json:"outputs"`
	Profile   string    `json:"profileId"`
	Monitored bool      `json:"monitored"`
	Missing   int       `json:"missing"`
}

// LibraryFilter narrows the subtitle library listing.
type LibraryFilter struct {
	Query   string
	Kind    string
	Status  string
	Missing bool
	Limit   int
}

// ScanSummary reports one inventory reconciliation.
type ScanSummary struct {
	Videos   int `json:"videos"`
	Sidecars int `json:"sidecars"`
	Wanted   int `json:"wanted"`
}

// DownloadRequest asks to download one provider result.
type DownloadRequest struct {
	ProviderID string `json:"providerId"`
	FileID     string `json:"fileId"`
	Language   string `json:"language"`
	Forced     bool   `json:"forced"`
	HI         bool   `json:"hi"`
	FileName   string `json:"fileName"`
}

// SearchOutcome is a provider search with per-provider warnings.
type SearchOutcome struct {
	Results  []Result `json:"results"`
	Warnings []string `json:"warnings"`
}

// SyncRequest describes an offset, frame-rate, audio, or reference synchronization.
type SyncRequest struct {
	Path          string  `json:"path"`
	Mode          string  `json:"mode"`
	OffsetSeconds float64 `json:"offsetSeconds"`
	FPSFrom       float64 `json:"fpsFrom"`
	FPSTo         float64 `json:"fpsTo"`
	ReferencePath string  `json:"referencePath"`
	// AudioStream is an embedded stream index; nil and negative values mean automatic.
	AudioStream    *int    `json:"audioStream"`
	MaxOffset      float64 `json:"maxOffsetSeconds"`
	MinScore       float64 `json:"minScore"`
	NoFixFramerate bool    `json:"noFixFramerate"`
	GoldenSection  bool    `json:"goldenSectionSearch"`
	VAD            string  `json:"vad"`
	Preview        bool    `json:"preview"`
}

// SyncResult reports the applied alignment.
type SyncResult struct {
	Mode     string  `json:"mode"`
	Offset   float64 `json:"offsetSeconds"`
	Scale    float64 `json:"scale"`
	Score    float64 `json:"score"`
	OutputID string  `json:"outputId,omitempty"`
	Path     string  `json:"path,omitempty"`
	Changed  bool    `json:"changed"`
	Detail   string  `json:"detail"`
}

// TranslateRequest describes an AI translation of one sidecar.
type TranslateRequest struct {
	Path           string `json:"path"`
	Language       string `json:"language"`
	SourceLanguage string `json:"sourceLanguage"`
	Apply          bool   `json:"apply"`
}

// TranslateResult reports a finished translation.
type TranslateResult struct {
	OutputID    string `json:"outputId"`
	Cues        int    `json:"cues"`
	Tokens      int    `json:"tokens"`
	Requests    int    `json:"requests"`
	Language    string `json:"language"`
	Applied     bool   `json:"applied"`
	SidecarPath string `json:"sidecarPath,omitempty"`
}

// Stream describes one embedded media stream for the UI.
type Stream struct {
	Index    int    `json:"index"`
	Type     string `json:"type"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Title    string `json:"title"`
	Forced   bool   `json:"forced"`
	HI       bool   `json:"hi"`
	Default  bool   `json:"default"`
	Channels int    `json:"channels,omitempty"`
}

// ExtractRequest describes embedded stream extraction; Forced and HI override the stream tags.
type ExtractRequest struct {
	StreamIndex int    `json:"streamIndex"`
	Language    string `json:"language"`
	Preview     bool   `json:"preview"`
	Forced      *bool  `json:"forced"`
	HI          *bool  `json:"hi"`
}

// ProviderTest is the result of a provider connectivity check.
type ProviderTest struct {
	ProviderID string `json:"providerId"`
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	Message    string `json:"message,omitempty"`
}
