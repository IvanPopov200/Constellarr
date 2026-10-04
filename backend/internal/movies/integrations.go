package movies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

const (
	providerTimeout   = 10 * time.Second
	webhookTimeout    = 5 * time.Second
	maxWatchlistBytes = 1 << 20
)

var watchlistIMDbPattern = regexp.MustCompile(`\btt\d{7,12}\b`)

func (s *Service) notifyImport(ctx context.Context, cfg Config, movie Movie, paths []string) {
	if cfg.JellyfinURL != "" && strings.TrimSpace(cfg.JellyfinAPIKey) != "" {
		if err := jellyfinRefresh(ctx, cfg); err != nil {
			_ = s.Store.Event(ctx, movie.ID, "jellyfin", sanitizeMessage(cfg, "Jellyfin refresh failed: "+err.Error()))
		}
	}
	if cfg.WebhookURL != "" {
		if err := postWebhook(ctx, cfg, movie, paths); err != nil {
			_ = s.Store.Event(ctx, movie.ID, "webhook", sanitizeMessage(cfg, "Webhook failed: "+err.Error()))
		}
	}
}

func jellyfinRefresh(ctx context.Context, cfg Config) error {
	return jellyfinRequest(ctx, cfg, http.MethodPost, "/Library/Refresh")
}

func jellyfinCheck(ctx context.Context, cfg Config) error {
	return jellyfinRequest(ctx, cfg, http.MethodGet, "/System/Info")
}

func jellyfinRequest(ctx context.Context, cfg Config, method, endpoint string) error {
	requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, joinURLPath(cfg.JellyfinURL, endpoint), nil)
	if err != nil {
		return errors.New("Jellyfin URL is not usable")
	}
	req.Header.Set("X-Emby-Token", cfg.JellyfinAPIKey)
	client := &http.Client{Timeout: providerTimeout, CheckRedirect: sameOriginRedirect(cfg.JellyfinURL)}
	resp, err := client.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			return errors.New("Jellyfin request timed out")
		}
		return errors.New("Jellyfin could not be reached")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	switch {
	case resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("Jellyfin rejected the API key")
	default:
		return fmt.Errorf("Jellyfin returned HTTP status %d", resp.StatusCode)
	}
}

type webhookPayload struct {
	Event   string   `json:"event"`
	MovieID string   `json:"movieId"`
	Title   string   `json:"title"`
	Year    int      `json:"year"`
	IMDbID  string   `json:"imdbId,omitempty"`
	Quality string   `json:"quality,omitempty"`
	Files   []string `json:"files"`
}

func postWebhook(ctx context.Context, cfg Config, movie Movie, paths []string) error {
	payload := webhookPayload{
		Event:   "movie.imported",
		MovieID: movie.ID,
		Title:   movie.Metadata.Title,
		Year:    movie.Metadata.Year,
		IMDbID:  movie.Metadata.IMDbID,
		Quality: bestFileQuality(movie.Files),
		Files:   paths,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("webhook payload could not be encoded")
	}
	requestCtx, cancel := context.WithTimeout(ctx, webhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("webhook URL is not usable")
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: webhookTimeout, CheckRedirect: sameOriginRedirect(cfg.WebhookURL)}
	resp, err := client.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			return errors.New("webhook request timed out")
		}
		return errors.New("webhook could not be reached")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("webhook returned HTTP status %d", resp.StatusCode)
	}
	return nil
}

func bestFileQuality(files []File) string {
	best, found := File{}, false
	for _, file := range files {
		if !found || file.Score >= best.Score {
			best, found = file, true
		}
	}
	if !found {
		return ""
	}
	return best.Quality
}

func joinURLPath(baseURL, suffix string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return strings.TrimSuffix(baseURL, "/") + suffix
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + suffix
	return parsed.String()
}

func sameOriginRedirect(baseURL string) func(*http.Request, []*http.Request) error {
	origin, err := url.Parse(strings.TrimSpace(baseURL))
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if err != nil || !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
			return errors.New("redirect left the configured origin")
		}
		return nil
	}
}

func (s *Service) TestConnections(ctx context.Context) (ConnectionTests, error) {
	cfg, err := s.Store.Config(ctx)
	if err != nil {
		return ConnectionTests{}, err
	}
	tests := ConnectionTests{}
	if !s.metadataConfigured(cfg) {
		tests.Metadata.Error = "OMDb API key is not configured"
	} else if client, err := metadata.New(cfg.MetadataURL, cfg.MetadataAPIKey); err != nil {
		tests.Metadata.Error = sanitizeMessage(cfg, err.Error())
	} else {
		checkCtx, cancel := context.WithTimeout(ctx, providerTimeout)
		defer cancel()
		if err := client.Test(checkCtx); err != nil {
			tests.Metadata.Error = sanitizeMessage(cfg, err.Error())
		} else {
			tests.Metadata.OK = true
		}
	}
	if cfg.JellyfinURL == "" || strings.TrimSpace(cfg.JellyfinAPIKey) == "" {
		tests.Jellyfin.Error = "Jellyfin URL and API key are not configured"
	} else if err := jellyfinCheck(ctx, cfg); err != nil {
		tests.Jellyfin.Error = sanitizeMessage(cfg, err.Error())
	} else {
		tests.Jellyfin.OK = true
	}
	return tests, nil
}

func (s *Service) SyncWatchlist(ctx context.Context, id string) (int, error) {
	lists, err := s.Store.Watchlists(ctx)
	if err != nil {
		return 0, err
	}
	id = strings.TrimSpace(id)
	var list *Watchlist
	for i := range lists {
		if lists[i].ID == id {
			list = &lists[i]
			break
		}
	}
	if list == nil {
		return 0, ErrNotFound
	}
	ids, err := s.watchlistIDs(ctx, *list)
	if err != nil {
		s.saveWatchlistState(ctx, *list, sanitizeMessage(Config{}, err.Error()))
		return 0, err
	}
	added := 0
	problems := make([]string, 0, 2)
	for _, imdbID := range ids {
		if _, err := s.Store.FindIMDb(ctx, imdbID); err == nil {
			continue
		} else if !errors.Is(err, ErrNotFound) {
			problems = append(problems, imdbID+": "+err.Error())
			continue
		}
		if _, err := s.Add(ctx, AddInput{
			IMDbID: imdbID, Monitored: list.Monitor, ProfileID: list.ProfileID, RootID: list.RootID,
		}); err != nil {
			problems = append(problems, imdbID+": "+sanitizeMessage(Config{}, err.Error()))
			continue
		}
		added++
	}
	if len(problems) > 0 {
		message := sanitizeMessage(Config{}, strings.Join(problems, "; "))
		s.saveWatchlistState(ctx, *list, message)
		if added == 0 {
			return 0, errors.New("movies: " + message)
		}
		return added, nil
	}
	s.saveWatchlistState(ctx, *list, "")
	return added, nil
}

func (s *Service) watchlistIDs(ctx context.Context, list Watchlist) ([]string, error) {
	if len(list.IMDbIDs) > 0 {
		return normalizeList(list.IMDbIDs), nil
	}
	if strings.TrimSpace(list.URL) == "" {
		return nil, errors.New("movies: the watchlist has no IMDb IDs or URL")
	}
	return fetchWatchlistIDs(ctx, list.URL)
}

func (s *Service) saveWatchlistState(ctx context.Context, list Watchlist, message string) {
	now := time.Now().UTC()
	list.LastSyncAt = &now
	list.Error = message
	_, _ = s.Store.SaveWatchlist(ctx, list)
}

func fetchWatchlistIDs(ctx context.Context, rawURL string) ([]string, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("movies: the watchlist URL must be an HTTP(S) URL without credentials")
	}
	requestCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("movies: the watchlist URL is not usable")
	}
	req.Header.Set("Accept", "application/json, text/csv, text/plain")
	client := &http.Client{Timeout: providerTimeout, CheckRedirect: sameOriginRedirect(rawURL)}
	resp, err := client.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			return nil, errors.New("movies: the watchlist request timed out")
		}
		return nil, errors.New("movies: the watchlist could not be fetched")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("movies: the watchlist returned HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxWatchlistBytes+1))
	if err != nil {
		return nil, errors.New("movies: the watchlist could not be read")
	}
	if len(body) > maxWatchlistBytes {
		return nil, errors.New("movies: the watchlist is too large")
	}
	return parseWatchlistIDs(body)
}

func parseWatchlistIDs(body []byte) ([]string, error) {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, errors.New("movies: the watchlist is empty")
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype") || strings.Contains(lower, "</body>") {
		return nil, errors.New("movies: the watchlist URL returned HTML; provide CSV, JSON, or IMDb ID lines")
	}
	seen := map[string]bool{}
	ids := make([]string, 0, 8)
	for _, match := range watchlistIMDbPattern.FindAllString(lower, -1) {
		if !metadata.ValidIMDbID(match) || seen[match] {
			continue
		}
		seen[match] = true
		ids = append(ids, match)
	}
	if len(ids) == 0 {
		return nil, errors.New("movies: the watchlist contains no IMDb IDs")
	}
	return ids, nil
}

func sanitizeMessage(cfg Config, message string) string {
	message = strings.TrimSpace(message)
	for _, secret := range []string{cfg.MetadataAPIKey, cfg.JellyfinAPIKey} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	if message == "" {
		message = "operation failed"
	}
	return truncate(message, maxTextRunes)
}
