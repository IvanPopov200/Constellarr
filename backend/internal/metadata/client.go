package metadata

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultBaseURL   = "https://www.omdbapi.com"
	requestTimeout   = 15 * time.Second
	maxResponseBytes = 4 << 20
	maxQueryRunes    = 256
	maxSearchPage    = 100
)

// Error is a sanitized OMDb failure; Kind is stable for callers and Retry marks failures worth repeating.
type Error struct {
	Op     string
	Kind   string
	Status int
	Retry  bool
}

func (e *Error) Error() string {
	detail := "metadata " + e.Op + ": "
	switch e.Kind {
	case "api key":
		return detail + "OMDb rejected the API key"
	case "rate limited":
		return detail + "OMDb rate limit reached; retry later"
	case "not found":
		return detail + "title was not found"
	case "not a movie":
		return detail + "title is not a movie"
	case "unavailable":
		return detail + "OMDb is unavailable; retry later"
	case "timed out":
		return detail + "request timed out; retry later"
	case "cross-origin redirect":
		return detail + "provider redirect left the configured origin"
	case "response too large":
		return detail + "provider response is too large"
	case "invalid response":
		return detail + "provider response is not valid OMDb data"
	default:
		if e.Status != 0 {
			return detail + "HTTP status " + strconv.Itoa(e.Status)
		}
		return detail + "request failed"
	}
}

type Client struct {
	base   *url.URL
	apiKey string
	http   *http.Client
}

func New(baseURL, apiKey string) (*Client, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, errors.New("metadata: an OMDb API key is required")
	}
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("metadata: URL is not a valid absolute URL")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("metadata: URL must use http or https")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("metadata: URL must not include credentials, a query, or a fragment")
	}
	if base.Path == "" {
		base.Path = "/"
	}
	client := &Client{base: base, apiKey: apiKey}
	client.http = &http.Client{Timeout: requestTimeout, CheckRedirect: client.checkRedirect}
	return client, nil
}

func (c *Client) Search(ctx context.Context, query string, page int) ([]Title, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("metadata: a search query is required")
	}
	if utf8.RuneCountInString(query) > maxQueryRunes {
		return nil, errors.New("metadata: the search query is too long")
	}
	if page < 1 || page > maxSearchPage {
		return nil, errors.New("metadata: the search page must be between 1 and 100")
	}
	params := url.Values{"s": {query}, "type": {"movie"}, "page": {strconv.Itoa(page)}}
	body, err := c.get(ctx, "search", params, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	return decodeSearch("search", body)
}

func (c *Client) Lookup(ctx context.Context, imdbID string) (Title, error) {
	id := strings.ToLower(strings.TrimSpace(imdbID))
	if !ValidIMDbID(id) {
		return Title{}, errors.New("metadata: an IMDb ID must be tt followed by 7 to 12 digits")
	}
	body, err := c.get(ctx, "lookup", url.Values{"i": {id}, "plot": {"full"}}, maxResponseBytes)
	if err != nil {
		return Title{}, err
	}
	return decodeTitle("lookup", id, body)
}

func (c *Client) Test(ctx context.Context) error {
	params := url.Values{"s": {"test"}, "type": {"movie"}, "page": {"1"}}
	body, err := c.get(ctx, "test", params, maxResponseBytes)
	if err != nil {
		return err
	}
	_, err = decodeSearch("test", body)
	return err
}

var errCrossOriginRedirect = errors.New("metadata redirect left the configured origin")

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("metadata provider returned too many redirects")
	}
	if req.URL.Scheme != c.base.Scheme || req.URL.Host != c.base.Host {
		return errCrossOriginRedirect
	}
	return nil
}

func (c *Client) get(ctx context.Context, op string, params url.Values, maxBytes int64) ([]byte, error) {
	params.Set("apikey", c.apiKey)
	target := *c.base
	target.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, &Error{Op: op, Kind: "request failed"}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, errCrossOriginRedirect) {
			return nil, &Error{Op: op, Kind: "cross-origin redirect"}
		}
		var urlErr *url.Error
		if errors.As(err, &urlErr) && urlErr.Timeout() {
			return nil, &Error{Op: op, Kind: "timed out", Retry: true}
		}
		return nil, &Error{Op: op, Kind: "request failed", Retry: true}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &Error{Op: op, Kind: "request failed", Retry: true}
	}
	if int64(len(body)) > maxBytes {
		return nil, &Error{Op: op, Kind: "response too large"}
	}
	if err := statusError(op, resp.StatusCode); err != nil {
		if message := envelopeError(body); message != "" {
			return nil, classifyProviderError(op, message, resp.StatusCode)
		}
		return nil, err
	}
	return body, nil
}

func statusError(op string, status int) error {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Op: op, Kind: "api key", Status: status}
	case status == http.StatusTooManyRequests:
		return &Error{Op: op, Kind: "rate limited", Status: status, Retry: true}
	case status >= http.StatusInternalServerError:
		return &Error{Op: op, Kind: "unavailable", Status: status, Retry: true}
	default:
		return &Error{Op: op, Kind: "request failed", Status: status}
	}
}
