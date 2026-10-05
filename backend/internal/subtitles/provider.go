package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	providerTypeOpenSubtitles = "opensubtitles"
	userAgent                 = "Constellarr/1.0"
	providerClientTimeout     = 60 * time.Second
	maxProviderRedirects      = 5
	searchBodyLimit           = 4 << 20
	downloadBodyLimit         = maxSubtitleBytes
	tokenLifetime             = 23 * time.Hour
)

// SearchQuery carries the identifiers used to find candidate subtitles.
type SearchQuery struct {
	Type         string
	IMDbID       string
	ParentIMDbID string
	Query        string
	Year         int
	Season       int
	Episode      int
	Languages    []string
	Limit        int
}

// Downloaded is a provider subtitle payload with quota context.
type Downloaded struct {
	Data      []byte
	FileName  string
	Format    Format
	Remaining int
	Requests  int
	ResetAt   string
}

type provider interface {
	ID() string
	Name() string
	Configured() bool
	Search(ctx context.Context, query SearchQuery) ([]Result, error)
	Download(ctx context.Context, fileID string) (Downloaded, error)
	Test(ctx context.Context) (string, error)
}

func newProvider(cfg Provider, client *http.Client) (provider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case providerTypeOpenSubtitles, "":
		return newOpenSubtitles(cfg, client)
	default:
		return nil, fmt.Errorf("%w: unsupported provider type %q", ErrInvalid, cfg.Type)
	}
}

type openSubtitles struct {
	id       string
	name     string
	endpoint string
	username string
	password string
	apiKey   string
	client   *http.Client

	mu    sync.Mutex
	token string
	until time.Time
}

func newOpenSubtitles(cfg Provider, client *http.Client) (*openSubtitles, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = "https://api.opensubtitles.com/api/v1"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("%w: provider endpoint must be an http(s) URL", ErrInvalid)
	}
	if len(endpoint) > maxPathBytes {
		return nil, fmt.Errorf("%w: provider endpoint is too long", ErrInvalid)
	}
	if client == nil {
		client = http.DefaultClient
	}
	guarded := *client
	if guarded.Transport == nil {
		guarded.Transport = http.DefaultTransport
	}
	guarded.Timeout = providerClientTimeout
	guarded.CheckRedirect = guardedRedirects(client.CheckRedirect)
	return &openSubtitles{
		id:       cfg.ID,
		name:     cfg.Name,
		endpoint: strings.TrimRight(endpoint, "/"),
		username: strings.TrimSpace(cfg.Username),
		password: cfg.Password,
		apiKey:   strings.TrimSpace(cfg.APIKey),
		client:   &guarded,
	}, nil
}

// guardedRedirects blocks cross-origin redirects for credentialled requests; Go only strips Authorization automatically.
func guardedRedirects(next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if next != nil {
			if err := next(request, via); err != nil {
				return err
			}
		}
		if len(via) >= maxProviderRedirects {
			return errors.New("subtitles: too many provider redirects")
		}
		if len(via) > 0 && !sameOrigin(via[0].URL, request.URL) && carriesCredentials(via[0].Header) {
			return fmt.Errorf("%w: refusing a cross-origin provider redirect", ErrUnsafe)
		}
		return nil
	}
}

func carriesCredentials(header http.Header) bool {
	return header.Get("Api-Key") != "" || header.Get("Authorization") != ""
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func (p *openSubtitles) ID() string       { return p.id }
func (p *openSubtitles) Name() string     { return p.name }
func (p *openSubtitles) Configured() bool { return p.apiKey != "" }

func (p *openSubtitles) Search(ctx context.Context, query SearchQuery) ([]Result, error) {
	if !p.Configured() {
		return nil, fmt.Errorf("%w: add an OpenSubtitles API key", ErrNotConfigured)
	}
	params := url.Values{}
	params.Set("languages", strings.Join(query.Languages, ","))
	params.Set("per_page", strconv.Itoa(boundedResults(query.Limit)))
	switch strings.ToLower(query.Type) {
	case KindEpisode:
		params.Set("type", "episode")
		if query.ParentIMDbID != "" {
			params.Set("parent_imdb_id", numericIMDb(query.ParentIMDbID))
		}
		if query.Season > 0 {
			params.Set("season_number", strconv.Itoa(query.Season))
		}
		if query.Episode > 0 {
			params.Set("episode_number", strconv.Itoa(query.Episode))
		}
	default:
		params.Set("type", "movie")
		if query.IMDbID != "" {
			params.Set("imdb_id", numericIMDb(query.IMDbID))
		}
		if query.Year > 0 {
			params.Set("year", strconv.Itoa(query.Year))
		}
	}
	matchedBy := "imdb"
	if !params.Has("imdb_id") && !params.Has("parent_imdb_id") {
		matchedBy = "title"
		if query.Query == "" {
			return nil, fmt.Errorf("%w: a video title or IMDb ID is needed to search", ErrInvalid)
		}
		params.Set("query", query.Query)
	}
	var body osSearchResponse
	if err := p.do(ctx, http.MethodGet, "/subtitles?"+params.Encode(), nil, &body, true); err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(body.Data))
	for _, item := range body.Data {
		attributes := item.Attributes
		fileID, fileName := "", ""
		if len(attributes.Files) > 0 {
			fileID = attributes.Files[0].FileID.String()
			fileName = attributes.Files[0].FileName
		}
		if fileID == "" || fileID == "0" {
			continue
		}
		results = append(results, Result{
			ProviderID:   p.id,
			ProviderName: p.name,
			FileID:       fileID,
			SubtitleID:   attributes.SubtitleID.String(),
			Language:     attributes.Language,
			Format:       strings.ToLower(attributes.Format),
			Forced:       attributes.ForeignPartsOnly,
			HI:           attributes.HearingImpaired,
			FPS:          attributes.FPS,
			Downloads:    attributes.DownloadCount,
			Rating:       attributes.Ratings,
			Release:      truncate(attributes.Release, maxTextRunes),
			FileName:     truncate(fileName, maxTextRunes),
			MatchedBy:    matchedBy,
		})
	}
	return results, nil
}

func (p *openSubtitles) Download(ctx context.Context, fileID string) (Downloaded, error) {
	if !p.Configured() {
		return Downloaded{}, fmt.Errorf("%w: add an OpenSubtitles API key", ErrNotConfigured)
	}
	if _, err := strconv.Atoi(fileID); err != nil {
		return Downloaded{}, fmt.Errorf("%w: an OpenSubtitles file ID is required", ErrInvalid)
	}
	var ticket osDownloadResponse
	if err := p.do(ctx, http.MethodPost, "/download", map[string]string{"file_id": fileID}, &ticket, true); err != nil {
		return Downloaded{}, err
	}
	if ticket.Link == "" {
		return Downloaded{}, errors.New("subtitles: the provider did not return a download link")
	}
	link, err := url.Parse(ticket.Link)
	if err != nil || (link.Scheme != "http" && link.Scheme != "https") || link.Host == "" {
		return Downloaded{}, errors.New("subtitles: the provider returned an unusable download link")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, ticket.Link, nil)
	if err != nil {
		return Downloaded{}, errors.New("subtitles: the download request could not be built")
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := p.client.Do(request)
	if err != nil {
		return Downloaded{}, sanitizeProviderError(ctx, "subtitles: download failed", err, p.secrets()...)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Downloaded{}, providerStatusError(response.StatusCode, response, p.secrets()...)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, downloadBodyLimit+1))
	if err != nil {
		return Downloaded{}, sanitizeProviderError(ctx, "subtitles: download failed", err, p.secrets()...)
	}
	if len(data) > downloadBodyLimit {
		return Downloaded{}, fmt.Errorf("%w: provider subtitle exceeds %d bytes", ErrInvalid, downloadBodyLimit)
	}
	parsed, err := ParseDocument(data)
	if err != nil {
		return Downloaded{}, err
	}
	return Downloaded{
		Data:      data,
		FileName:  truncate(ticket.FileName, maxTextRunes),
		Format:    parsed.Format,
		Remaining: ticket.Remaining,
		Requests:  ticket.Requests,
		ResetAt:   ticket.ResetTimeUTC,
	}, nil
}

func (p *openSubtitles) Test(ctx context.Context) (string, error) {
	if !p.Configured() {
		return "", fmt.Errorf("%w: add an OpenSubtitles API key", ErrNotConfigured)
	}
	if p.username != "" {
		if err := p.login(ctx, false); err != nil {
			return "", err
		}
		return "signed in as " + truncate(p.username, 64), nil
	}
	params := url.Values{}
	params.Set("query", "constellarr")
	params.Set("languages", "en")
	params.Set("per_page", "1")
	var body osSearchResponse
	if err := p.do(ctx, http.MethodGet, "/subtitles?"+params.Encode(), nil, &body, false); err != nil {
		return "", err
	}
	return "API key accepted without user credentials", nil
}

// login fetches a bearer token once and reuses it until it approaches expiry.
func (p *openSubtitles) login(ctx context.Context, force bool) error {
	p.mu.Lock()
	if !force && p.token != "" && time.Now().Before(p.until) {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	payload, err := json.Marshal(map[string]string{"username": p.username, "password": p.password})
	if err != nil {
		return errors.New("subtitles: sign-in request could not be built")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/login", bytes.NewReader(payload))
	if err != nil {
		return errors.New("subtitles: sign-in request could not be built")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Api-Key", p.apiKey)
	response, err := p.client.Do(request)
	if err != nil {
		return sanitizeProviderError(ctx, "OpenSubtitles sign-in failed", err, p.secrets()...)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return providerStatusError(response.StatusCode, response, p.secrets()...)
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body); err != nil || body.Token == "" {
		return errors.New("OpenSubtitles sign-in returned no token")
	}
	p.mu.Lock()
	p.token, p.until = body.Token, time.Now().Add(tokenLifetime)
	p.mu.Unlock()
	return nil
}

func (p *openSubtitles) bearer(ctx context.Context) (string, error) {
	if p.username == "" {
		return "", nil
	}
	if err := p.login(ctx, false); err != nil {
		return "", err
	}
	p.mu.Lock()
	token := p.token
	p.mu.Unlock()
	if token == "" {
		return "", errors.New("OpenSubtitles sign-in returned no token")
	}
	return token, nil
}

// do performs one bounded provider request, refreshing an expired token once.
func (p *openSubtitles) do(ctx context.Context, method, path string, payload any, out any, auth bool) error {
	return p.doAttempt(ctx, method, path, payload, out, auth, false)
}

func (p *openSubtitles) doAttempt(ctx context.Context, method, path string, payload any, out any, auth, retried bool) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return errors.New("subtitles: provider request could not be built")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, p.endpoint+path, body)
	if err != nil {
		return errors.New("subtitles: provider request could not be built")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Api-Key", p.apiKey)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if auth {
		token, err := p.bearer(ctx)
		if err != nil {
			return err
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	response, err := p.client.Do(request)
	if err != nil {
		return sanitizeProviderError(ctx, "OpenSubtitles request failed", err, p.secrets()...)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized && auth && p.username != "" && !retried {
		// One forced re-login recovers a token revoked between requests.
		if err := p.login(ctx, true); err != nil {
			return err
		}
		return p.doAttempt(ctx, method, path, payload, out, auth, true)
	}
	if response.StatusCode != http.StatusOK {
		return providerStatusError(response.StatusCode, response, p.secrets()...)
	}
	if out == nil {
		io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, searchBodyLimit)).Decode(out); err != nil {
		return errors.New("OpenSubtitles returned an unreadable response")
	}
	return nil
}

// providerStatusError maps provider failures to typed, credential-free errors.
func providerStatusError(status int, response *http.Response, secrets ...string) error {
	message := readProviderMessage(response)
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	switch {
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrRateLimited, message)
	case status == http.StatusPaymentRequired || status == http.StatusNotAcceptable:
		return fmt.Errorf("%w: %s", ErrQuota, message)
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: OpenSubtitles rejected the API key or credentials: %s", ErrNotConfigured, message)
	default:
		return fmt.Errorf("OpenSubtitles request failed with HTTP %d: %s", status, message)
	}
}

func readProviderMessage(response *http.Response) string {
	var body struct {
		Message string `json:"message"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<10))
	if err == nil {
		_ = json.Unmarshal(raw, &body)
	}
	message := strings.Join(strings.Fields(body.Message), " ")
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

// secrets returns configured secrets so upstream error text can never echo them.
func (p *openSubtitles) secrets() []string {
	out := []string{}
	if p.apiKey != "" {
		out = append(out, p.apiKey)
	}
	if p.password != "" {
		out = append(out, p.password)
	}
	return out
}

func sanitizeProviderError(ctx context.Context, prefix string, err error, secrets ...string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", prefix, ctx.Err())
	}
	message := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 300 {
		message = message[:300]
	}
	return fmt.Errorf("%s: %s", prefix, message)
}

func boundedResults(limit int) int {
	if limit <= 0 {
		return 40
	}
	if limit > maxSearchResults {
		return maxSearchResults
	}
	return limit
}

// numericIMDb converts tt0133093 to the numeric identifier OpenSubtitles expects.
func numericIMDb(id string) string {
	digits := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "tt")
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0"
	}
	return digits
}

// scoreResult ranks one provider result against a wanted language variant.
func scoreResult(want LanguagePreference, result Result) int {
	language, ok := NormalizeLanguage(result.Language)
	if !ok {
		return -1
	}
	haveBase, haveRegion, _ := strings.Cut(strings.ToLower(language), "-")
	wantBase, wantRegion, _ := strings.Cut(strings.ToLower(want.Code), "-")
	if haveBase != wantBase {
		return -1
	}
	if haveRegion != "" && wantRegion != "" && haveRegion != wantRegion {
		return -1
	}
	if result.Forced != want.Forced {
		return -1
	}
	score := 20
	if haveRegion == wantRegion {
		score += 20
	}
	if result.HI == want.HI {
		score += 20
	} else if want.HI {
		return -1
	} else {
		score -= 10
	}
	switch strings.ToLower(result.Format) {
	case "srt":
		score += 15
	case "vtt", "webvtt":
		score += 10
	case "ass", "ssa":
		score += 5
	}
	score += min(10, result.Downloads/500)
	if result.Rating > 0 {
		score += int(math.Round(min(10, result.Rating)))
	}
	return score
}

type osSearchResponse struct {
	TotalPages int `json:"total_pages"`
	TotalCount int `json:"total_count"`
	Data       []struct {
		Attributes struct {
			SubtitleID       flexString `json:"subtitle_id"`
			Language         string     `json:"language"`
			DownloadCount    int        `json:"download_count"`
			HearingImpaired  bool       `json:"hearing_impaired"`
			ForeignPartsOnly bool       `json:"foreign_parts_only"`
			Format           string     `json:"format"`
			FPS              float64    `json:"fps"`
			Ratings          float64    `json:"ratings"`
			Release          string     `json:"release"`
			Files            []struct {
				FileID   flexInt `json:"file_id"`
				FileName string  `json:"file_name"`
			} `json:"files"`
		} `json:"attributes"`
	} `json:"data"`
}

type osDownloadResponse struct {
	Link         string `json:"link"`
	FileName     string `json:"file_name"`
	Requests     int    `json:"requests"`
	Remaining    int    `json:"remaining"`
	ResetTimeUTC string `json:"reset_time_utc"`
	Message      string `json:"message"`
}

// flexString accepts provider fields that may be a string or a number.
type flexString string

func (f *flexString) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(string(raw), `"`)
	if string(raw) == "null" {
		*f = ""
		return nil
	}
	*f = flexString(text)
	return nil
}

func (f flexString) String() string { return string(f) }

// flexInt accepts provider fields that may be a number or a numeric string.
type flexInt int

func (f *flexInt) UnmarshalJSON(raw []byte) error {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		text := strings.Trim(string(raw), `"`)
		parsed, convErr := strconv.Atoi(text)
		if convErr != nil {
			*f = 0
			return nil
		}
		*f = flexInt(parsed)
		return nil
	}
	parsed, err := number.Int64()
	if err != nil {
		*f = 0
		return nil
	}
	*f = flexInt(parsed)
	return nil
}

func (f flexInt) String() string { return strconv.Itoa(int(f)) }
