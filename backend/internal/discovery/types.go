package discovery

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	ErrNotFound        = errors.New("request not found")
	ErrInvalid         = errors.New("invalid request")
	ErrConflict        = errors.New("request operation conflicts with current state")
	ErrForbidden       = errors.New("not permitted")
	ErrUnauthenticated = errors.New("authentication required")
	ErrNotConfigured   = errors.New("discovery: a required service is not configured")
	ErrUpstream        = errors.New("discovery: an upstream provider failed")
)

const (
	MediaMovie = "movie"
	MediaTV    = "tv"
	MediaMusic = "music"

	StatusPending   = "pending"
	StatusApproving = "approving"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusCancelled = "cancelled"
	StatusAvailable = "available"

	PhaseSearching   = "searching"
	PhaseDownloading = "downloading"
	PhaseImporting   = "importing"
	PhaseFailed      = "failed"
	PhaseAvailable   = "available"
	PhaseUnmonitored = "unmonitored"
	PhaseRemoved     = "removed"
	PhaseUnknown     = "unknown"
)

// Actor resolves the caller identity and whether they may approve requests; a missing actor denies all access.
type Actor func(*http.Request) (userID string, canApprove bool)

// Can resolves operation-specific permissions; a missing hook denies every extra permission.
type Can func(*http.Request, string) bool

const (
	PermissionLibraryRead     = "library.read"
	PermissionRequestsWrite   = "requests.write"
	PermissionLibraryWrite    = "library.write"
	PermissionRequestsApprove = "requests.approve"
)

// Permissions carries the caller's operation rights for one request.
type Permissions struct {
	Approve       bool `json:"approve"`
	RequestsWrite bool `json:"requestsWrite"`
	LibraryWrite  bool `json:"libraryWrite"`
}

// UserName resolves one account ID to a display name; a missing hook or unknown account stays unnamed.
type UserName func(context.Context, string) string

type Options struct {
	Actor Actor
	Can   Can
	// Notify is called on decisions, availability changes, reverted approvals, and once when a
	// delivery transitions into failed.
	Notify   func(context.Context, Request)
	UserName UserName
	Music    MusicLibrary
	Clock    func() time.Time
}

type Request struct {
	ID            string     `json:"id"`
	UserID        string     `json:"userId"`
	UserName      string     `json:"userName"`
	MediaType     string     `json:"mediaType"`
	Provider      string     `json:"provider"`
	ProviderID    string     `json:"providerId"`
	Title         string     `json:"title"`
	Year          int        `json:"year"`
	Poster        string     `json:"poster"`
	Status        string     `json:"status"`
	Message       string     `json:"message"`
	DecisionNote  string     `json:"decisionNote"`
	DecidedBy     string     `json:"decidedBy"`
	DecidedByName string     `json:"decidedByName"`
	DecidedAt     *time.Time `json:"decidedAt"`
	LibraryID     string     `json:"libraryId"`
	Delivery      Delivery   `json:"delivery"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type Delivery struct {
	Phase     string         `json:"phase"`
	Available bool           `json:"available"`
	Progress  *float64       `json:"progress,omitempty"`
	Done      int            `json:"done,omitempty"`
	Total     int            `json:"total,omitempty"`
	JobID     string         `json:"jobId,omitempty"`
	Message   string         `json:"message,omitempty"`
	Attempts  int            `json:"attempts,omitempty"`
	Approval  *ApprovalState `json:"approval,omitempty"`
}

// ApprovalState records the approver's edits so an interrupted approval can resume unchanged.
type ApprovalState struct {
	ProfileID   string `json:"profileId,omitempty"`
	RootID      string `json:"rootId,omitempty"`
	Monitored   bool   `json:"monitored"`
	MonitorMode string `json:"monitorMode,omitempty"`
}

type Event struct {
	ID         int64     `json:"id"`
	RequestID  string    `json:"requestId"`
	Actor      string    `json:"actor"`
	ActorName  string    `json:"actorName"`
	Action     string    `json:"action"`
	FromStatus string    `json:"fromStatus"`
	ToStatus   string    `json:"toStatus"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Comment struct {
	ID        int64     `json:"id"`
	RequestID string    `json:"requestId"`
	UserID    string    `json:"userId"`
	UserName  string    `json:"userName"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type CreateInput struct {
	MediaType  string `json:"mediaType"`
	Provider   string `json:"provider"`
	ProviderID string `json:"providerId"`
	Title      string `json:"title"`
	Year       int    `json:"year"`
	Poster     string `json:"poster"`
	Message    string `json:"message"`
}

type ApproveInput struct {
	ProfileID   string `json:"profileId"`
	RootID      string `json:"rootId"`
	Monitored   *bool  `json:"monitored"`
	MonitorMode string `json:"monitorMode"`
	Note        string `json:"note"`
}

type RejectInput struct {
	Reason string `json:"reason"`
}

type CommentInput struct {
	Body string `json:"body"`
}

type ListFilter struct {
	Status    string
	MediaType string
	UserID    string
	Query     string
	Limit     int
}

type Detail struct {
	Request     Request     `json:"request"`
	Comments    []Comment   `json:"comments"`
	Events      []Event     `json:"events"`
	CanApprove  bool        `json:"canApprove"`
	Permissions Permissions `json:"permissions"`
}

type List struct {
	Requests    []Request   `json:"requests"`
	Types       []string    `json:"types"`
	CanApprove  bool        `json:"canApprove"`
	Permissions Permissions `json:"permissions"`
}

type DiscoverResult struct {
	MediaType  string `json:"mediaType"`
	Provider   string `json:"provider"`
	ProviderID string `json:"providerId"`
	Title      string `json:"title"`
	Year       int    `json:"year"`
	Poster     string `json:"poster"`
}

type Discover struct {
	Results []DiscoverResult `json:"results"`
	Types   []string         `json:"types"`
}

type SyncResult struct {
	Approved  int `json:"approved"`
	Failed    int `json:"failed"`
	Reverted  int `json:"reverted"`
	Updated   int `json:"updated"`
	Available int `json:"available"`
}

type CalendarQuery struct {
	From    string   `json:"from"`
	To      string   `json:"to"`
	Types   []string `json:"types"`
	Sources []string `json:"sources"`
}

type CalendarEntry struct {
	ID        string `json:"id"`
	MediaType string `json:"mediaType"`
	Source    string `json:"source"`
	Title     string `json:"title"`
	Subtitle  string `json:"subtitle"`
	Date      string `json:"date"`
	Year      int    `json:"year"`
	Poster    string `json:"poster"`
	LibraryID string `json:"libraryId"`
	Status    string `json:"status"`
	IMDbID    string `json:"imdbId,omitempty"`
	Season    int    `json:"season,omitempty"`
	Episode   int    `json:"episode,omitempty"`
	Artist    string `json:"artist,omitempty"`
}

type CalendarSource struct {
	ID        string `json:"id"`
	MediaType string `json:"mediaType"`
	Available bool   `json:"available"`
}

type CalendarView struct {
	Entries []CalendarEntry  `json:"entries"`
	Sources []CalendarSource `json:"sources"`
	From    string           `json:"from"`
	To      string           `json:"to"`
}

type Candidate struct {
	Title        string `json:"title"`
	MediaType    string `json:"mediaType"`
	Year         int    `json:"year"`
	Reason       string `json:"reason"`
	Provider     string `json:"provider"`
	ProviderID   string `json:"providerId"`
	Poster       string `json:"poster"`
	Verified     bool   `json:"verified"`
	Verification string `json:"verification"`
	Accepted     bool   `json:"accepted"`
}

type RecommendationInput struct {
	MediaType  string   `json:"mediaType"`
	UseHistory bool     `json:"useHistory"`
	Titles     []string `json:"titles"`
	Genres     []string `json:"genres"`
	Count      int      `json:"count"`
}

type Recommendation struct {
	ID             string              `json:"id"`
	UserID         string              `json:"userId"`
	Model          string              `json:"model"`
	MediaType      string              `json:"mediaType"`
	Input          RecommendationInput `json:"input"`
	Candidates     []Candidate         `json:"candidates"`
	Warnings       []string            `json:"warnings"`
	AcceptedAt     *time.Time          `json:"acceptedAt"`
	AcceptedAction string              `json:"acceptedAction"`
	AcceptedID     string              `json:"acceptedId"`
	CreatedAt      time.Time           `json:"createdAt"`
}

type RecommendationList struct {
	Recommendations []Recommendation `json:"recommendations"`
	Types           []string         `json:"types"`
	CanApprove      bool             `json:"canApprove"`
	Permissions     Permissions      `json:"permissions"`
}

type AcceptInput struct {
	Candidate   *int   `json:"candidate"`
	Action      string `json:"action"`
	Message     string `json:"message"`
	ProfileID   string `json:"profileId"`
	RootID      string `json:"rootId"`
	Monitored   *bool  `json:"monitored"`
	MonitorMode string `json:"monitorMode"`
}

type AcceptResult struct {
	Action         string         `json:"action"`
	Request        *Request       `json:"request,omitempty"`
	LibraryID      string         `json:"libraryId,omitempty"`
	Recommendation Recommendation `json:"recommendation"`
}
