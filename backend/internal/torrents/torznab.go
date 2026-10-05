package torrents

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const (
	maxSourceURLBytes   = 2048
	maxAPIKeyBytes      = 512
	maxCategories       = 20
	maxResultsPerSource = 200
	maxRedirects        = 3
	maxFeedBytes        = 4 << 20
	searchTimeout       = 15 * time.Second
	resultTTL           = 30 * time.Minute
	maxCachedResults    = 2000
)

type storedSource struct {
	source Source
	apiKey string
}

type cachedResult struct {
	sourceID string
	url      string
	// magnet keeps the feed's original link with its trackers; it is never serialized to clients.
	magnet  string
	private bool
	title   string
	expires time.Time
}

type resultCache struct {
	mu      sync.Mutex
	entries map[string]cachedResult
}

func newResultCache() *resultCache {
	return &resultCache{entries: make(map[string]cachedResult)}
}

// boundedHTTPClient caps source fetches: a finite timeout, limited redirects and no key forwarding.
func boundedHTTPClient() *http.Client {
	return &http.Client{
		Timeout: searchTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("torrents: too many redirects")
			}
			if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
				return errors.New("torrents: the redirect target is not HTTP(S)")
			}
			if len(via) > 0 && !sameOrigin(via[0].URL, request.URL) {
				if query := request.URL.Query(); query.Has("apikey") {
					query.Del("apikey")
					request.URL.RawQuery = query.Encode()
				}
			}
			return nil
		},
	}
}

// sameOrigin matches an indexer endpoint and its scheme upgrades, so credentials never leave the host.
func sameOrigin(source, target *url.URL) bool {
	if source == nil || target == nil || !strings.EqualFold(source.Hostname(), target.Hostname()) {
		return false
	}
	upgrade := strings.EqualFold(source.Scheme, "http") && strings.EqualFold(target.Scheme, "https")
	if !strings.EqualFold(source.Scheme, target.Scheme) && !upgrade {
		return false
	}
	return explicitPort(source) == explicitPort(target)
}

func explicitPort(target *url.URL) string {
	port := target.Port()
	if (strings.EqualFold(target.Scheme, "https") && port == "443") || (strings.EqualFold(target.Scheme, "http") && port == "80") {
		return ""
	}
	return port
}

func (c *resultCache) put(id string, entry cachedResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCachedResults {
		now := time.Now()
		for key, value := range c.entries {
			if now.After(value.expires) {
				delete(c.entries, key)
			}
		}
	}
	c.entries[id] = entry
}

func (c *resultCache) get(id string) (cachedResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[id]
	if !ok || time.Now().After(entry.expires) {
		return cachedResult{}, false
	}
	return entry, true
}

func validateSourceURL(raw string) (string, error) {
	raw = strings.TrimSpace(strings.TrimRight(raw, "/"))
	if raw == "" || len(raw) > maxSourceURLBytes {
		return "", invalidSource("the Torznab URL is not a valid absolute HTTP(S) URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", invalidSource("the Torznab URL is not a valid absolute HTTP(S) URL")
	}
	return parsed.String(), nil
}

func invalidSource(message string) error {
	return invalid(message)
}

func validateSourceInput(input SourceInput) error {
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return invalidSource("the source name must be between 1 and 100 characters")
	}
	if _, err := validateSourceURL(input.URL); err != nil {
		return err
	}
	if len(input.APIKey) > maxAPIKeyBytes {
		return invalidSource("the API key is too long")
	}
	if len(input.Categories) > maxCategories {
		return invalidSource("at most 20 categories are supported")
	}
	for _, category := range input.Categories {
		if category < 1 || category > 1_000_000 {
			return invalidSource("the categories are out of range")
		}
	}
	return nil
}

func (s *Service) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, url, api_key, categories, enabled FROM torrent_sources
		ORDER BY name, id`)
	if err != nil {
		return nil, storeError("list sources", err)
	}
	defer rows.Close()
	sources := make([]Source, 0)
	for rows.Next() {
		var (
			source     Source
			apiKey     string
			categories []byte
		)
		if err := rows.Scan(&source.ID, &source.Name, &source.URL, &apiKey, &categories, &source.Enabled); err != nil {
			return nil, storeError("list sources", err)
		}
		source.APIKeyConfigured = apiKey != ""
		source.Categories = decodeCategories(categories)
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("list sources", err)
	}
	return sources, nil
}

func decodeCategories(raw []byte) []int {
	categories := make([]int, 0)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &categories); err != nil {
			categories = []int{}
		}
	}
	return categories
}

func (s *Service) sourceByID(ctx context.Context, id string) (storedSource, error) {
	var (
		source     storedSource
		categories []byte
	)
	err := s.pool.QueryRow(ctx, `SELECT id, name, url, api_key, categories, enabled FROM torrent_sources WHERE id = $1`, id).
		Scan(&source.source.ID, &source.source.Name, &source.source.URL, &source.apiKey, &categories, &source.source.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedSource{}, ErrNotFound
	}
	if err != nil {
		return storedSource{}, storeError("load source", err)
	}
	source.source.Categories = decodeCategories(categories)
	source.source.APIKeyConfigured = source.apiKey != ""
	return source, nil
}

func (s *Service) CreateSource(ctx context.Context, input SourceInput) (Source, error) {
	if err := validateSourceInput(input); err != nil {
		return Source{}, err
	}
	sourceURL, _ := validateSourceURL(input.URL)
	encoded, err := json.Marshal(input.Categories)
	if err != nil {
		return Source{}, ErrInvalid
	}
	var (
		source     Source
		apiKey     string
		categories []byte
	)
	err = s.pool.QueryRow(ctx, `INSERT INTO torrent_sources (id, name, url, api_key, categories, enabled)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		RETURNING id, name, url, api_key, categories, enabled`,
		rand.Text(), strings.TrimSpace(input.Name), sourceURL, input.APIKey, string(encoded), input.Enabled).
		Scan(&source.ID, &source.Name, &source.URL, &apiKey, &categories, &source.Enabled)
	if err != nil {
		return Source{}, storeError("create source", err)
	}
	source.APIKeyConfigured = apiKey != ""
	source.Categories = decodeCategories(categories)
	return source, nil
}

func (s *Service) UpdateSource(ctx context.Context, id string, input SourceInput) (Source, error) {
	if err := validateSourceInput(input); err != nil {
		return Source{}, err
	}
	current, err := s.sourceByID(ctx, id)
	if err != nil {
		return Source{}, err
	}
	sourceURL, _ := validateSourceURL(input.URL)
	apiKey := input.APIKey
	if strings.TrimSpace(apiKey) == "" {
		apiKey = current.apiKey
	}
	encoded, err := json.Marshal(input.Categories)
	if err != nil {
		return Source{}, ErrInvalid
	}
	var (
		source     Source
		storedKey  string
		categories []byte
	)
	err = s.pool.QueryRow(ctx, `UPDATE torrent_sources SET name = $2, url = $3, api_key = $4, categories = $5::jsonb,
		enabled = $6, updated_at = now() WHERE id = $1
		RETURNING id, name, url, api_key, categories, enabled`,
		id, strings.TrimSpace(input.Name), sourceURL, apiKey, string(encoded), input.Enabled).
		Scan(&source.ID, &source.Name, &source.URL, &storedKey, &categories, &source.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	if err != nil {
		return Source{}, storeError("update source", err)
	}
	source.APIKeyConfigured = storedKey != ""
	source.Categories = decodeCategories(categories)
	return source, nil
}

func (s *Service) DeleteSource(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM torrent_sources WHERE id = $1`, id)
	if err != nil {
		return storeError("delete source", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) TestSource(ctx context.Context, id string) (SourceTest, error) {
	source, err := s.sourceByID(ctx, id)
	if err != nil {
		return SourceTest{}, err
	}
	body, err := s.torznabRequest(ctx, source, url.Values{"t": {"caps"}})
	if err != nil {
		return SourceTest{Error: err.Error()}, nil
	}
	var caps torznabCaps
	if err := xml.Unmarshal(body, &caps); err != nil {
		return SourceTest{Error: "the source did not return Torznab capabilities"}, nil
	}
	return SourceTest{OK: true, Caps: caps.available()}, nil
}

func (s *Service) Search(ctx context.Context, query, sourceID string) (SearchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" || utf8.RuneCountInString(query) > 200 {
		return SearchResponse{}, ErrInvalid
	}
	sources, err := s.ListSources(ctx)
	if err != nil {
		return SearchResponse{}, err
	}
	selected := make([]Source, 0, len(sources))
	for _, source := range sources {
		if !source.Enabled || (sourceID != "" && source.ID != sourceID) {
			continue
		}
		selected = append(selected, source)
	}
	if len(selected) == 0 {
		return SearchResponse{}, ErrNotConfigured
	}
	response := SearchResponse{Results: []SearchResult{}}
	var mu sync.Mutex
	var wait sync.WaitGroup
	for _, source := range selected {
		wait.Add(1)
		go func(source Source) {
			defer wait.Done()
			stored, err := s.sourceByID(ctx, source.ID)
			if err != nil {
				mu.Lock()
				response.Errors = append(response.Errors, SearchError{Source: source.Name, Error: "the source is no longer available"})
				mu.Unlock()
				return
			}
			results, err := s.searchSource(ctx, stored, query)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				response.Errors = append(response.Errors, SearchError{Source: source.Name, Error: err.Error()})
				return
			}
			response.Results = append(response.Results, results...)
		}(source)
	}
	wait.Wait()
	return response, nil
}

func (s *Service) searchSource(ctx context.Context, source storedSource, query string) ([]SearchResult, error) {
	params := url.Values{"t": {"search"}, "q": {query}, "limit": {"100"}}
	if len(source.source.Categories) > 0 {
		ids := make([]string, 0, len(source.source.Categories))
		for _, category := range source.source.Categories {
			ids = append(ids, strconv.Itoa(category))
		}
		params.Set("cat", strings.Join(ids, ","))
	}
	body, err := s.torznabRequest(ctx, source, params)
	if err != nil {
		return nil, err
	}
	var feed torznabFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, errors.New("the source did not return a Torznab feed")
	}
	results := make([]SearchResult, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		if len(results) >= maxResultsPerSource {
			break
		}
		result, err := s.resultFromItem(source, item)
		if err != nil {
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) resultFromItem(source storedSource, item torznabItem) (SearchResult, error) {
	title := sanitizeDisplayName(item.Title)
	if title == "" {
		return SearchResult{}, errors.New("torrents: the result has no title")
	}
	guid := firstNonEmpty(item.GUID, item.Enclosure.URL, item.Link)
	if guid == "" {
		guid = title
	}
	magnet := item.magnetURL()
	if magnet == "" {
		// A hash-only magnet lets DHT-capable clients use infohash-only results.
		magnet = magnetFromInfoHash(item.hash(), title)
	}
	link := strings.TrimSpace(firstNonEmpty(item.Enclosure.URL, item.Link))
	if strings.HasPrefix(strings.ToLower(link), "magnet:") {
		link = ""
	}
	if link == "" && magnet == "" {
		return SearchResult{}, errors.New("torrents: the result has no download link")
	}
	sum := sha256.Sum256([]byte(source.source.ID + "\x00" + guid))
	id := hex.EncodeToString(sum[:])[:20]
	s.results.put(id, cachedResult{
		// Trackers and passkeys stay in this server-side cache and are used when the result is added.
		sourceID: source.source.ID, url: link, magnet: magnet, private: item.privateHint(), title: title,
		expires: time.Now().Add(resultTTL),
	})
	size := item.size()
	published := item.published()
	seeders, leechers := item.counts()
	result := SearchResult{
		ID: id, SourceID: source.source.ID, Source: source.source.Name, Title: title,
		Size: size, Seeders: seeders, Leechers: leechers, Published: published,
		Category: sanitizeDisplayName(item.category()),
	}
	if !item.privateHint() {
		// Public results expose a tracker-free magnet; private trackers stay out of the browser JSON.
		result.Magnet = magnetFromInfoHash(item.hash(), title)
	}
	return result, nil
}

// AddFromResult downloads a searched result through the source, keeping indexer credentials server-side.
func (s *Service) AddFromResult(ctx context.Context, sourceID, resultID, title string) (Job, error) {
	source, err := s.sourceByID(ctx, sourceID)
	if err != nil {
		return Job{}, err
	}
	entry, ok := s.results.get(resultID)
	if !ok || entry.sourceID != sourceID {
		return Job{}, invalid("the search result expired; search again")
	}
	if entry.url == "" {
		if entry.magnet == "" {
			return Job{}, ErrNotConfigured
		}
		return s.Add(ctx, AddInput{Magnet: entry.magnet, Source: sourceTorznab, ReleaseID: resultID,
			Private: entry.private, Title: firstNonEmpty(title, entry.title)})
	}
	body, err := s.fetch(ctx, source, entry.url)
	if err != nil {
		return Job{}, err
	}
	if _, _, err := loadMetainfo(body); err != nil {
		return Job{}, err
	}
	return s.Add(ctx, AddInput{Torrent: body, Source: sourceTorznab, ReleaseID: resultID,
		Private: entry.private, Title: firstNonEmpty(title, entry.title)})
}

func (s *Service) torznabRequest(ctx context.Context, source storedSource, params url.Values) ([]byte, error) {
	body, err := s.fetch(ctx, source, source.source.URL+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("the source returned an empty response")
	}
	return body, nil
}

func (s *Service) fetch(ctx context.Context, source storedSource, rawURL string) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("the source URL is not a valid HTTP(S) URL")
	}
	// Download links may point at a CDN; the indexer key stays on the indexer host.
	if sourceURL, err := url.Parse(source.source.URL); source.apiKey != "" && err == nil && sameOrigin(sourceURL, parsed) {
		query := parsed.Query()
		if query.Get("apikey") == "" {
			query.Set("apikey", source.apiKey)
			parsed.RawQuery = query.Encode()
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, errors.New("the source request could not be created")
	}
	request.Header.Set("Accept", "application/xml, text/xml, */*")
	request.Header.Set("User-Agent", "Constellarr")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return nil, redactError(errors.New("the source could not be reached"), source.apiKey)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, redactError(errors.New("the source returned an unexpected status"), source.apiKey)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes+1))
	if err != nil {
		return nil, redactError(errors.New("the source response could not be read"), source.apiKey)
	}
	if len(body) > maxFeedBytes {
		return nil, errors.New("the source response is too large")
	}
	return body, nil
}

// redactError makes sure a configured API key never reaches clients or logs.
func redactError(err error, secrets ...string) error {
	message := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return errors.New(message)
}
