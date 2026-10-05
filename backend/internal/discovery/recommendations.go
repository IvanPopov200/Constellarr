package discovery

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

const (
	maxTasteTitles  = 10
	maxTasteGenres  = 10
	maxPromptRunes  = 4000
	maxCandidates   = 10
	defaultCount    = 5
	historyLimit    = 20
	tasteTitleRunes = 200
	tasteGenreRunes = 40
	reasonRunes     = 300
)

// Generate asks the configured provider for candidates and resolves them against the real metadata provider.
func (s *Service) Generate(ctx context.Context, actor string, input RecommendationInput) (Recommendation, error) {
	input, err := s.normalizeRecommendation(ctx, actor, input)
	if err != nil {
		return Recommendation{}, err
	}
	client, err := s.AI.Client(ctx)
	if err != nil {
		return Recommendation{}, err
	}
	content, err := client.Complete(ctx, s.recommendationPrompt(ctx, actor, input))
	if err != nil {
		return Recommendation{}, err
	}
	parsed, err := parseCandidates(content, input.Count)
	if err != nil {
		return Recommendation{}, err
	}
	candidates, warnings := s.resolveCandidates(ctx, parsed)
	if len(candidates) == 0 {
		warnings = append(warnings, "The model did not return usable candidates.")
	}
	rec := Recommendation{
		ID: rand.Text(), UserID: actor, Model: client.Model(), MediaType: input.MediaType,
		Input: input, Candidates: candidates, Warnings: warnings,
	}
	if err := s.insertRecommendation(ctx, rec); err != nil {
		return Recommendation{}, err
	}
	return s.recommendation(ctx, rec.ID)
}

func (s *Service) normalizeRecommendation(ctx context.Context, actor string, input RecommendationInput) (RecommendationInput, error) {
	mediaType := strings.ToLower(strings.TrimSpace(input.MediaType))
	allowed := s.Types()
	switch mediaType {
	case "":
		mediaType = "any"
	case "any":
	case MediaMusic:
		if !s.typeAvailable(MediaMusic) {
			return RecommendationInput{}, fmt.Errorf("%w: music recommendations are not configured on this server", ErrNotConfigured)
		}
	default:
		normalized, err := normalizeMediaType(mediaType)
		if err != nil {
			return RecommendationInput{}, err
		}
		if !s.typeAvailable(normalized) {
			return RecommendationInput{}, fmt.Errorf("%w: %s recommendations are not configured on this server", ErrNotConfigured, normalized)
		}
		mediaType = normalized
	}
	if len(allowed) == 0 {
		return RecommendationInput{}, fmt.Errorf("%w: no media type is configured for recommendations", ErrNotConfigured)
	}
	input.MediaType = mediaType
	titles, err := boundedList(input.Titles, maxTasteTitles, tasteTitleRunes, "titles")
	if err != nil {
		return RecommendationInput{}, err
	}
	genres, err := boundedList(input.Genres, maxTasteGenres, tasteGenreRunes, "genres")
	if err != nil {
		return RecommendationInput{}, err
	}
	input.Titles, input.Genres = titles, genres
	if input.Count <= 0 {
		input.Count = defaultCount
	}
	if input.Count > maxCandidates {
		input.Count = maxCandidates
	}
	if len(titles) == 0 && len(genres) == 0 && !input.UseHistory {
		return RecommendationInput{}, fmt.Errorf("%w: select a title, a genre, or your request history first", ErrInvalid)
	}
	return input, nil
}

func boundedList(values []string, limit, runes int, name string) ([]string, error) {
	if len(values) > limit {
		return nil, fmt.Errorf("%w: select at most %d %s", ErrInvalid, limit, name)
	}
	kept := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		value, err := plainText(raw, runes, false)
		if err != nil {
			return nil, err
		}
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		kept = append(kept, value)
	}
	return kept, nil
}

// recommendationPrompt builds a bounded prompt; the taste section is trimmed before the instructions.
func (s *Service) recommendationPrompt(ctx context.Context, actor string, input RecommendationInput) string {
	types := s.Types()
	if input.MediaType != "any" {
		types = []string{input.MediaType}
	}
	sort.Strings(types)
	head := "Recommend media for a personal library. Reply with JSON only, in this exact shape:\n" +
		`{"candidates":[{"title":"Name","mediaType":"movie","year":1999,"reason":"one short sentence"}]}` + "\n" +
		"Only suggest real, released titles. mediaType must be one of: " + strings.Join(types, ", ") + ".\n"
	tail := "\nReturn at most " + strconv.Itoa(input.Count) + " candidates. Do not explain outside the JSON."
	taste := s.tasteSummary(ctx, actor, input)
	budget := maxPromptRunes - utf8.RuneCountInString(head) - utf8.RuneCountInString(tail)
	if budget < 0 {
		budget = 0
	}
	taste = truncateRunes(taste, budget)
	return head + taste + tail
}

func (s *Service) tasteSummary(ctx context.Context, actor string, input RecommendationInput) string {
	lines := make([]string, 0, 4)
	if len(input.Genres) > 0 {
		lines = append(lines, "Preferred genres: "+strings.Join(input.Genres, ", ")+".")
	}
	if len(input.Titles) > 0 {
		lines = append(lines, "Titles the user picked: "+strings.Join(input.Titles, ", ")+".")
	}
	if input.UseHistory {
		history, err := s.requestHistory(ctx, actor, historyLimit)
		if err == nil && len(history) > 0 {
			lines = append(lines, "Titles the user already requested: "+strings.Join(history, ", ")+".")
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// requestHistory returns the caller's own request titles; no other user's data reaches the provider.
func (s *Service) requestHistory(ctx context.Context, actor string, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT title FROM discovery_requests
		WHERE user_id = $1 AND status IN ('pending', 'approving', 'approved', 'available')
		ORDER BY created_at DESC LIMIT $2`, actor, limit)
	if err != nil {
		return nil, errors.New("discovery: request history could not be loaded")
	}
	defer rows.Close()
	titles := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var title string
		if err := rows.Scan(&title); err != nil {
			return nil, errors.New("discovery: request history could not be loaded")
		}
		key := strings.ToLower(strings.TrimSpace(title))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		titles = append(titles, title)
	}
	return titles, rows.Err()
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

type flexibleInt int

// UnmarshalJSON accepts a year written as a number or a string.
func (f *flexibleInt) UnmarshalJSON(raw []byte) error {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if value == "" || value == "null" {
		*f = 0
		return nil
	}
	if index := strings.IndexAny(value, "-/"); index > 0 {
		value = value[:index]
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexibleInt(parsed)
	return nil
}

type rawCandidate struct {
	Title     string      `json:"title"`
	MediaType string      `json:"mediaType"`
	Year      flexibleInt `json:"year"`
	Reason    string      `json:"reason"`
}

// parseCandidates accepts strict JSON, a fenced block, or JSON embedded in prose, and rejects garbage.
func parseCandidates(content string, limit int) ([]Candidate, error) {
	payload := extractJSON(content)
	if payload == "" {
		return nil, fmt.Errorf("%w: the AI provider returned an unusable recommendation list", ErrUpstream)
	}
	var doc struct {
		Candidates []rawCandidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return nil, fmt.Errorf("%w: the AI provider returned an unusable recommendation list", ErrUpstream)
	}
	candidates := make([]Candidate, 0, len(doc.Candidates))
	seen := map[string]bool{}
	for _, raw := range doc.Candidates {
		if len(candidates) >= limit {
			break
		}
		mediaType, err := normalizeMediaType(raw.MediaType)
		if err != nil {
			continue
		}
		title, err := plainText(raw.Title, tasteTitleRunes, true)
		if err != nil {
			continue
		}
		reason, err := plainText(raw.Reason, reasonRunes, false)
		if err != nil {
			continue
		}
		key := mediaType + "\x00" + normalizeTitle(title)
		if seen[key] {
			continue
		}
		seen[key] = true
		year := int(raw.Year)
		if year < 0 || year > 2200 {
			year = 0
		}
		candidates = append(candidates, Candidate{
			Title: title, MediaType: mediaType, Year: year, Reason: reason,
			Verification: "Not confirmed by a metadata provider",
		})
	}
	if len(doc.Candidates) > 0 && len(candidates) == 0 {
		return nil, fmt.Errorf("%w: the AI provider returned an unusable recommendation list", ErrUpstream)
	}
	return candidates, nil
}

// extractJSON tolerates code fences and surrounding prose without trusting either.
func extractJSON(content string) string {
	content = strings.TrimSpace(content)
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return ""
	}
	return content[start : end+1]
}

// resolveCandidates confirms each title with the real provider; unconfirmed entries keep no identifier.
func (s *Service) resolveCandidates(ctx context.Context, candidates []Candidate) ([]Candidate, []string) {
	resolved := make([]Candidate, 0, len(candidates))
	warnings := []string{}
	blocked := map[string]string{}
	for _, candidate := range candidates {
		if !s.typeAvailable(candidate.MediaType) {
			continue
		}
		if message, ok := blocked[candidate.MediaType]; ok {
			candidate.Verification = message
			resolved = append(resolved, candidate)
			continue
		}
		switch candidate.MediaType {
		case MediaMovie:
			titles, err := s.movies.Discover(ctx, candidate.Title, 1)
			if err != nil {
				blocked[MediaMovie] = providerWarning(err)
				candidate.Verification = blocked[MediaMovie]
				resolved = append(resolved, candidate)
				continue
			}
			applyMatch(&candidate, firstMatch(titles, candidate.Title, candidate.Year), "omdb")
		case MediaTV:
			titles, err := s.tv.Discover(ctx, candidate.Title, 1)
			if err != nil {
				blocked[MediaTV] = providerWarning(err)
				candidate.Verification = blocked[MediaTV]
				resolved = append(resolved, candidate)
				continue
			}
			applyMatch(&candidate, firstMatch(titles, candidate.Title, candidate.Year), "omdb")
		case MediaMusic:
			items, err := s.music.Search(ctx, candidate.Title, 5)
			if err != nil {
				blocked[MediaMusic] = "The music library could not be searched"
				candidate.Verification = blocked[MediaMusic]
				resolved = append(resolved, candidate)
				continue
			}
			for _, item := range items {
				if normalizeTitle(item.Title) == normalizeTitle(candidate.Title) && validMusicID(strings.TrimSpace(item.ID)) {
					candidate.Verified = true
					candidate.Provider = MediaMusic
					candidate.ProviderID = item.ID
					candidate.Poster = item.Poster
					candidate.Verification = "Confirmed by the music library"
					break
				}
			}
		}
		resolved = append(resolved, candidate)
	}
	for _, mediaType := range []string{MediaMovie, MediaTV, MediaMusic} {
		if message, ok := blocked[mediaType]; ok {
			warnings = append(warnings, message)
		}
	}
	return resolved, warnings
}

func providerWarning(err error) string {
	if errors.Is(err, downloads.ErrNotConfigured) {
		return "The metadata provider is not configured; candidates stay unverified"
	}
	return "The metadata provider could not be reached; candidates stay unverified"
}

func applyMatch(candidate *Candidate, imdbID string, provider string) {
	if !metadata.ValidIMDbID(strings.TrimSpace(imdbID)) {
		return
	}
	candidate.Verified = true
	candidate.Provider = provider
	candidate.ProviderID = strings.ToLower(strings.TrimSpace(imdbID))
	candidate.Verification = "Confirmed by the metadata provider"
}

// firstMatch picks an exact title match, preferring the candidate's year.
func firstMatch(titles []metadata.Title, title string, year int) string {
	want := normalizeTitle(title)
	best := ""
	bestYearDiff := 0
	for _, item := range titles {
		if normalizeTitle(item.Title) != want || !metadata.ValidIMDbID(strings.TrimSpace(item.IMDbID)) {
			continue
		}
		diff := yearDiff(item.Year, year)
		if best == "" || diff < bestYearDiff {
			best, bestYearDiff = item.IMDbID, diff
		}
	}
	return best
}

func yearDiff(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > b {
		return a - b
	}
	return b - a
}

// normalizeTitle reduces a title to lowercase letters and digits for comparison.
func normalizeTitle(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, value)
}

func (s *Service) Recommendations(ctx context.Context, actor string, perms Permissions) (RecommendationList, error) {
	list, err := s.recommendations(ctx, actor, 20)
	if err != nil {
		return RecommendationList{}, err
	}
	return RecommendationList{Recommendations: list, Types: s.Types(), CanApprove: perms.Approve, Permissions: perms}, nil
}

func (s *Service) Recommendation(ctx context.Context, actor, id string) (Recommendation, error) {
	rec, err := s.recommendation(ctx, id)
	if err != nil {
		return Recommendation{}, err
	}
	if rec.UserID != actor {
		return Recommendation{}, ErrNotFound
	}
	return rec, nil
}

// Accept turns one recommendation candidate into a request, or adds it directly for approvers.
func (s *Service) Accept(ctx context.Context, actor string, perms Permissions, id string, input AcceptInput) (AcceptResult, error) {
	rec, err := s.Recommendation(ctx, actor, id)
	if err != nil {
		return AcceptResult{}, err
	}
	index := 0
	if input.Candidate != nil {
		index = *input.Candidate
	}
	if index < 0 || index >= len(rec.Candidates) {
		return AcceptResult{}, fmt.Errorf("%w: candidate does not exist", ErrInvalid)
	}
	candidate := rec.Candidates[index]
	if candidate.Accepted {
		return AcceptResult{}, fmt.Errorf("%w: this candidate was already accepted", ErrConflict)
	}
	if !candidate.Verified || candidate.ProviderID == "" {
		return AcceptResult{}, fmt.Errorf("%w: this candidate is not confirmed by a metadata provider", ErrInvalid)
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action == "" {
		action = "request"
	}
	result := AcceptResult{Action: action}
	switch action {
	case "request":
		if !perms.RequestsWrite {
			return AcceptResult{}, fmt.Errorf("%w: requesting media needs the requests.write permission", ErrForbidden)
		}
		request, _, err := s.Create(ctx, actor, CreateInput{
			MediaType: candidate.MediaType, Provider: candidate.Provider, ProviderID: candidate.ProviderID,
			Title: candidate.Title, Year: candidate.Year, Poster: candidate.Poster, Message: input.Message,
		})
		if err != nil {
			return AcceptResult{}, err
		}
		result.Request = &request
		result.LibraryID = request.LibraryID
	case "add":
		if !perms.LibraryWrite {
			return AcceptResult{}, fmt.Errorf("%w: adding to the library needs the library.write permission", ErrForbidden)
		}
		if !s.typeAvailable(candidate.MediaType) {
			return AcceptResult{}, fmt.Errorf("%w: %s is not configured on this server", ErrNotConfigured, candidate.MediaType)
		}
		state, err := s.approvalState(ApproveInput{
			ProfileID: input.ProfileID, RootID: input.RootID, Monitored: input.Monitored, MonitorMode: input.MonitorMode,
		}, nil)
		if err != nil {
			return AcceptResult{}, err
		}
		if err := s.validateEdits(ctx, candidate.MediaType, state); err != nil {
			return AcceptResult{}, err
		}
		libraryID, err := s.addToLibrary(ctx, Request{
			MediaType: candidate.MediaType, ProviderID: candidate.ProviderID, Title: candidate.Title,
			Year: candidate.Year, Poster: candidate.Poster,
		}, state)
		if err != nil {
			return AcceptResult{}, fmt.Errorf("%w: %s", ErrUpstream, sanitize(err.Error()))
		}
		result.LibraryID = libraryID
		s.queueDomainSync(candidate.MediaType)
	default:
		return AcceptResult{}, fmt.Errorf("%w: action must be request or add", ErrInvalid)
	}
	rec.Candidates[index].Accepted = true
	if err := s.markRecommendationAccepted(ctx, rec, action, acceptedID(result)); err != nil {
		return AcceptResult{}, err
	}
	updated, err := s.Recommendation(ctx, actor, id)
	if err != nil {
		return AcceptResult{}, err
	}
	result.Recommendation = updated
	return result, nil
}

func acceptedID(result AcceptResult) string {
	if result.Request != nil {
		return result.Request.ID
	}
	return result.LibraryID
}
