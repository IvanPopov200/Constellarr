package discovery

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

const (
	maxTitleRunes   = 256
	maxMessageRunes = 1000
	maxNoteRunes    = 500
	maxQueryRunes   = 256
	maxPosterRunes  = 500
	identifierBytes = 128
	defaultListSize = 100
	maxListSize     = 200
	maxDiscoverPage = 100
	musicSearchSize = 25
)

func normalizeMediaType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case MediaMovie:
		return MediaMovie, nil
	case MediaTV:
		return MediaTV, nil
	case MediaMusic:
		return MediaMusic, nil
	default:
		return "", fmt.Errorf("%w: media type must be movie, tv, or music", ErrInvalid)
	}
}

// plainText validates bounded user text: no control characters and no HTML tags.
func plainText(value string, limit int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			return "", fmt.Errorf("%w: enter between 1 and %d characters", ErrInvalid, limit)
		}
		return "", nil
	}
	if utf8.RuneCountInString(value) > limit {
		return "", fmt.Errorf("%w: enter at most %d characters", ErrInvalid, limit)
	}
	for _, r := range value {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: remove control characters", ErrInvalid)
		}
	}
	if containsMarkup(value) {
		return "", fmt.Errorf("%w: write plain text without HTML", ErrInvalid)
	}
	return value, nil
}

// containsMarkup reports a tag-like sequence, allowing ordinary text such as "2 < 3".
func containsMarkup(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] != '<' || i+1 >= len(value) {
			continue
		}
		next := value[i+1]
		if next == '/' || next == '!' || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') {
			return true
		}
	}
	return false
}

func (s *Service) validProviderID(mediaType, providerID string) (string, error) {
	id := strings.TrimSpace(providerID)
	switch mediaType {
	case MediaMovie, MediaTV:
		id = strings.ToLower(id)
		if !metadata.ValidIMDbID(id) {
			return "", fmt.Errorf("%w: an IMDb ID like tt1234567 is required", ErrInvalid)
		}
	case MediaMusic:
		if !validMusicID(id) {
			return "", fmt.Errorf("%w: a music provider ID is required", ErrInvalid)
		}
	}
	return id, nil
}

// validMusicID keeps music provider identifiers to a conservative charset.
func validMusicID(id string) bool {
	if id == "" || len(id) > identifierBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.', c == ':':
		default:
			return false
		}
	}
	return true
}

func validPoster(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if utf8.RuneCountInString(raw) > maxPosterRunes {
		return "", fmt.Errorf("%w: the poster URL is too long", ErrInvalid)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("%w: the poster URL is not usable", ErrInvalid)
	}
	return raw, nil
}

func (s *Service) providerFor(mediaType string) string {
	if mediaType == MediaMusic {
		return MediaMusic
	}
	return "omdb"
}

// Types reports the media types this server can actually act on.
func (s *Service) Types() []string {
	types := make([]string, 0, 3)
	if s.movies != nil {
		types = append(types, MediaMovie)
	}
	if s.tv != nil {
		types = append(types, MediaTV)
	}
	if s.music != nil {
		types = append(types, MediaMusic)
	}
	return types
}

func (s *Service) typeAvailable(mediaType string) bool {
	switch mediaType {
	case MediaMovie:
		return s.movies != nil
	case MediaTV:
		return s.tv != nil
	case MediaMusic:
		return s.music != nil
	default:
		return false
	}
}

// libraryIdentity reports whether the media already exists in the library.
func (s *Service) libraryIdentity(ctx context.Context, mediaType, providerID string) (string, bool) {
	switch mediaType {
	case MediaMovie:
		movie, err := s.movies.Store.FindIMDb(ctx, providerID)
		if err == nil {
			return movie.ID, true
		}
		if errors.Is(err, movies.ErrNotFound) {
			return "", false
		}
	case MediaTV:
		series, err := s.tv.Store.FindIMDb(ctx, providerID)
		if err == nil {
			return series.ID, true
		}
		if errors.Is(err, tv.ErrNotFound) {
			return "", false
		}
	case MediaMusic:
		if s.music == nil {
			return "", false
		}
		if item, err := s.music.Status(ctx, providerID); err == nil && item.Available {
			return item.ID, true
		}
	}
	return "", false
}

// Create records one request; a repeated active request for the same media returns the existing one.
func (s *Service) Create(ctx context.Context, actor string, input CreateInput) (Request, bool, error) {
	mediaType, err := normalizeMediaType(input.MediaType)
	if err != nil {
		return Request{}, false, err
	}
	if !s.typeAvailable(mediaType) {
		return Request{}, false, fmt.Errorf("%w: %s requests are not configured on this server", ErrNotConfigured, mediaType)
	}
	providerID, err := s.validProviderID(mediaType, input.ProviderID)
	if err != nil {
		return Request{}, false, err
	}
	title, err := plainText(input.Title, maxTitleRunes, true)
	if err != nil {
		return Request{}, false, fmt.Errorf("%w: enter the media title", ErrInvalid)
	}
	if input.Year < 0 || input.Year > 2200 {
		return Request{}, false, fmt.Errorf("%w: enter a release year between 0 and 2200", ErrInvalid)
	}
	message, err := plainText(input.Message, maxMessageRunes, false)
	if err != nil {
		return Request{}, false, err
	}
	poster, err := validPoster(input.Poster)
	if err != nil {
		return Request{}, false, err
	}
	request := Request{
		ID:         rand.Text(),
		UserID:     actor,
		MediaType:  mediaType,
		Provider:   s.providerFor(mediaType),
		ProviderID: providerID,
		Title:      title,
		Year:       input.Year,
		Poster:     poster,
		Status:     StatusPending,
		Message:    message,
		Delivery:   Delivery{Phase: PhaseSearching},
	}
	if libraryID, ok := s.libraryIdentity(ctx, mediaType, providerID); ok {
		request.Status = StatusAvailable
		request.LibraryID = libraryID
		request.Delivery = Delivery{Phase: PhaseAvailable, Available: true, Message: "Already in the library"}
	}
	if err := s.insertRequest(ctx, s.pool, request); err != nil {
		if isUniqueViolation(err) {
			// A concurrent create already owns the active request; return that record.
			existing, reloadErr := s.activeRequest(ctx, s.pool, actor, mediaType, providerID)
			if reloadErr == nil {
				return existing, false, nil
			}
			return Request{}, false, fmt.Errorf("%w: this media is already requested", ErrConflict)
		}
		return Request{}, false, errors.New("discovery: the request could not be saved")
	}
	action := "created"
	if prior, err := s.lastDecidedRequest(ctx, request.ID, actor, mediaType, providerID); err == nil && prior != "" && prior != request.Status {
		action = "re-requested"
	}
	_ = s.addEvent(ctx, s.pool, request.ID, actor, action, "", request.Status, "")
	if request.Status == StatusAvailable {
		_ = s.addEvent(ctx, s.pool, request.ID, actor, "available", StatusPending, StatusAvailable, "Already in the library")
	}
	saved, err := s.refresh(ctx, request.ID)
	if err != nil {
		return Request{}, true, err
	}
	return saved, true, nil
}

func (s *Service) List(ctx context.Context, actor string, perms Permissions, filter ListFilter) (List, error) {
	if filter.Status != "" && !validStatus(filter.Status) {
		return List{}, fmt.Errorf("%w: unknown request status", ErrInvalid)
	}
	if filter.MediaType != "" {
		mediaType, err := normalizeMediaType(filter.MediaType)
		if err != nil {
			return List{}, err
		}
		filter.MediaType = mediaType
	}
	if filter.Query != "" {
		query, err := plainText(filter.Query, maxQueryRunes, false)
		if err != nil {
			return List{}, err
		}
		filter.Query = query
	}
	if filter.UserID != "" {
		if _, err := plainText(filter.UserID, identifierBytes, true); err != nil {
			return List{}, err
		}
	}
	filter.Limit = clampLimit(filter.Limit)
	requests, err := s.requestList(ctx, filter, actor, perms.Approve)
	if err != nil {
		return List{}, err
	}
	return List{Requests: requests, Types: s.Types(), CanApprove: perms.Approve, Permissions: perms}, nil
}

func validStatus(status string) bool {
	switch status {
	case StatusPending, StatusApproving, StatusApproved, StatusRejected, StatusCancelled, StatusAvailable:
		return true
	default:
		return false
	}
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultListSize
	}
	if limit > maxListSize {
		return maxListSize
	}
	return limit
}

func (s *Service) Detail(ctx context.Context, actor string, perms Permissions, id string) (Detail, error) {
	request, err := s.requestByID(ctx, s.pool, id)
	if err != nil {
		return Detail{}, err
	}
	if !visible(actor, perms.Approve, request) {
		return Detail{}, ErrNotFound
	}
	comments, err := s.comments(ctx, request.ID)
	if err != nil {
		return Detail{}, err
	}
	events, err := s.events(ctx, request.ID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Request: request, Comments: comments, Events: events, CanApprove: perms.Approve, Permissions: perms}, nil
}

func visible(actor string, canApprove bool, request Request) bool {
	return canApprove || request.UserID == actor
}

// Cancel withdraws one pending request; only its requester can cancel it.
func (s *Service) Cancel(ctx context.Context, actor, id string) (Request, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, errors.New("discovery: the request could not be updated")
	}
	defer tx.Rollback(ctx)
	request, err := s.lockRequest(ctx, tx, id)
	if err != nil {
		return Request{}, err
	}
	if request.UserID != actor {
		return Request{}, ErrNotFound
	}
	if request.Status != StatusPending {
		return Request{}, fmt.Errorf("%w: only pending requests can be cancelled", ErrConflict)
	}
	if err := s.setCancelled(ctx, tx, request.ID); err != nil {
		return Request{}, err
	}
	if err := s.addEvent(ctx, tx, request.ID, actor, "cancelled", request.Status, StatusCancelled, ""); err != nil {
		return Request{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Request{}, errors.New("discovery: the request could not be updated")
	}
	return s.refresh(ctx, request.ID)
}

// Reject refuses a pending request with a reason and records the decision.
func (s *Service) Reject(ctx context.Context, actor, id string, input RejectInput) (Request, error) {
	reason, err := plainText(input.Reason, maxNoteRunes, true)
	if err != nil {
		return Request{}, fmt.Errorf("%w: a rejection reason is required", ErrInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Request{}, errors.New("discovery: the decision could not be recorded")
	}
	defer tx.Rollback(ctx)
	request, err := s.lockRequest(ctx, tx, id)
	if err != nil {
		return Request{}, err
	}
	switch request.Status {
	case StatusRejected:
		return request, nil
	case StatusPending:
	default:
		return Request{}, fmt.Errorf("%w: only pending requests can be rejected", ErrConflict)
	}
	if err := s.setRejected(ctx, tx, request.ID, actor, reason); err != nil {
		return Request{}, err
	}
	if err := s.addEvent(ctx, tx, request.ID, actor, "rejected", request.Status, StatusRejected, reason); err != nil {
		return Request{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Request{}, errors.New("discovery: the decision could not be recorded")
	}
	updated, err := s.refresh(ctx, request.ID)
	if err != nil {
		return Request{}, err
	}
	s.notify(ctx, updated)
	return updated, nil
}

func (s *Service) Comment(ctx context.Context, actor string, perms Permissions, id string, input CommentInput) (Comment, error) {
	body, err := plainText(input.Body, maxMessageRunes, true)
	if err != nil {
		return Comment{}, fmt.Errorf("%w: write a comment between 1 and %d characters", ErrInvalid, maxMessageRunes)
	}
	request, err := s.requestByID(ctx, s.pool, id)
	if err != nil {
		return Comment{}, err
	}
	if !visible(actor, perms.Approve, request) {
		return Comment{}, ErrNotFound
	}
	return s.addComment(ctx, request.ID, actor, body)
}

// Discover searches the metadata providers that back requests; music stays hidden without its module.
func (s *Service) Discover(ctx context.Context, mediaType, query string, page int) (Discover, error) {
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return Discover{}, err
	}
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > maxQueryRunes {
		return Discover{}, fmt.Errorf("%w: enter a title up to %d characters", ErrInvalid, maxQueryRunes)
	}
	if page < 1 || page > maxDiscoverPage {
		return Discover{}, fmt.Errorf("%w: search page must be between 1 and %d", ErrInvalid, maxDiscoverPage)
	}
	results := []DiscoverResult{}
	switch mediaType {
	case MediaMovie:
		titles, err := s.movies.Discover(ctx, query, page)
		if err != nil {
			return Discover{}, err
		}
		for _, title := range titles {
			results = append(results, DiscoverResult{
				MediaType: MediaMovie, Provider: "omdb", ProviderID: title.IMDbID,
				Title: title.Title, Year: title.Year, Poster: title.Poster,
			})
		}
	case MediaTV:
		titles, err := s.tv.Discover(ctx, query, page)
		if err != nil {
			return Discover{}, err
		}
		for _, title := range titles {
			results = append(results, DiscoverResult{
				MediaType: MediaTV, Provider: "omdb", ProviderID: title.IMDbID,
				Title: title.Title, Year: title.Year, Poster: title.Poster,
			})
		}
	case MediaMusic:
		if s.music == nil {
			return Discover{}, fmt.Errorf("%w: music requests are not configured on this server", ErrNotConfigured)
		}
		items, err := s.music.Search(ctx, query, musicSearchSize)
		if err != nil {
			if errors.Is(err, ErrNotConfigured) {
				return Discover{}, err
			}
			return Discover{}, fmt.Errorf("%w: the music library could not be searched: %s", ErrUpstream, sanitize(err.Error()))
		}
		for _, item := range items {
			if !validMusicID(strings.TrimSpace(item.ID)) {
				continue
			}
			title := strings.TrimSpace(item.Title)
			if len(item.Artist) > 0 {
				title = title + " — " + strings.TrimSpace(item.Artist)
			}
			results = append(results, DiscoverResult{
				MediaType: MediaMusic, Provider: MediaMusic, ProviderID: item.ID,
				Title: title, Year: item.Year, Poster: item.Poster,
			})
		}
	}
	return Discover{Results: results, Types: s.Types()}, nil
}
