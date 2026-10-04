package indexer

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	requestTimeout = 30 * time.Second
	maxSearchBytes = 16 << 20
	maxNZBBytes    = 32 << 20
	movieCategory  = 2000
	searchPageSize = 50
	maxQueryRunes  = 256
	maxIDBytes     = 128
)

type Release struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Size      int64     `json:"size"`
	Published time.Time `json:"published"`
}

type Error struct {
	Op     string // call that failed: "test", "search", or "nzb"
	Kind   string // stable category, for example "cross-origin redirect" or "invalid response"
	Status int    // HTTP status code when the server answered
	Code   int    // Newznab error code when the API returned an error envelope
}

func (e *Error) Error() string {
	switch {
	case e.Code != 0:
		return "indexer " + e.Op + ": API error " + strconv.Itoa(e.Code)
	case e.Status != 0:
		return "indexer " + e.Op + ": HTTP status " + strconv.Itoa(e.Status)
	default:
		return "indexer " + e.Op + ": " + e.Kind
	}
}

type Client struct {
	base   *url.URL
	apiKey string
	http   *http.Client
}

func New(baseURL, apiKey string) (*Client, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("indexer URL is not a valid absolute URL")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, errors.New("indexer URL must use http or https")
	}
	if base.User != nil {
		return nil, errors.New("indexer URL must not include credentials")
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("indexer URL must not include a query or fragment")
	}
	client := &Client{base: base, apiKey: apiKey}
	client.http = &http.Client{
		Timeout:       requestTimeout,
		CheckRedirect: client.checkRedirect,
	}
	return client, nil
}

func (c *Client) Test(ctx context.Context) error {
	params := url.Values{"t": {"search"}, "limit": {"1"}}
	body, err := c.get(ctx, "test", params, maxSearchBytes)
	if err != nil {
		return err
	}
	_, err = parseSearch(body, "test")
	return err
}

func (c *Client) Search(ctx context.Context, query string) ([]Release, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("search query is required")
	}
	if utf8.RuneCountInString(query) > maxQueryRunes {
		return nil, errors.New("search query is too long")
	}
	params := url.Values{
		"t":        {"search"},
		"q":        {query},
		"cat":      {strconv.Itoa(movieCategory)},
		"limit":    {strconv.Itoa(searchPageSize)},
		"extended": {"1"},
	}
	body, err := c.get(ctx, "search", params, maxSearchBytes)
	if err != nil {
		return nil, err
	}
	return parseSearch(body, "search")
}

func (c *Client) NZB(ctx context.Context, id string) ([]byte, error) {
	if !validID(id) {
		return nil, errors.New("release ID is invalid")
	}
	body, err := c.get(ctx, "nzb", url.Values{"t": {"get"}, "id": {id}}, maxNZBBytes)
	if err != nil {
		return nil, err
	}
	root, attrs, err := firstElement(body)
	if err != nil {
		return nil, &Error{Op: "nzb", Kind: "invalid response"}
	}
	if root == "error" {
		return nil, &Error{Op: "nzb", Kind: "api", Code: errorCode(attrs)}
	}
	if root != "nzb" {
		return nil, &Error{Op: "nzb", Kind: "invalid response"}
	}
	return body, nil
}

var errCrossOriginRedirect = errors.New("indexer redirect left the configured origin")

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("indexer returned too many redirects")
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
			return nil, &Error{Op: op, Kind: "timed out"}
		}
		return nil, &Error{Op: op, Kind: "request failed"}
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &Error{Op: op, Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &Error{Op: op, Kind: "request failed"}
	}
	if int64(len(body)) > maxBytes {
		return nil, &Error{Op: op, Kind: "response too large"}
	}
	return body, nil
}

func parseSearch(body []byte, op string) ([]Release, error) {
	root, attrs, err := firstElement(body)
	if err != nil {
		return nil, &Error{Op: op, Kind: "invalid response"}
	}
	if root == "error" {
		return nil, &Error{Op: op, Kind: "api", Code: errorCode(attrs)}
	}
	if root != "rss" {
		return nil, &Error{Op: op, Kind: "invalid response"}
	}
	var feed rss
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, &Error{Op: op, Kind: "invalid response"}
	}
	releases := make([]Release, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		if release, ok := item.release(); ok {
			releases = append(releases, release)
		}
	}
	return releases, nil
}

func firstElement(body []byte) (string, map[string]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", nil, err
		}
		if start, ok := token.(xml.StartElement); ok {
			attrs := make(map[string]string, len(start.Attr))
			for _, attr := range start.Attr {
				attrs[attr.Name.Local] = attr.Value
			}
			return start.Name.Local, attrs, nil
		}
	}
}

func errorCode(attrs map[string]string) int {
	code, _ := strconv.Atoi(attrs["code"])
	return code
}

type rss struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	PubDate   string `xml:"pubDate"`
	Enclosure struct {
		Length string `xml:"length,attr"`
	} `xml:"enclosure"`
	Attributes []rssAttribute `xml:"attr"`
}

type rssAttribute struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (item rssItem) release() (Release, bool) {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return Release{}, false
	}
	id := normalizeID(item.attribute("guid"))
	if !validID(id) {
		id = normalizeID(item.GUID)
	}
	if !validID(id) {
		return Release{}, false
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(item.attribute("size")), 10, 64)
	if size <= 0 {
		size, _ = strconv.ParseInt(strings.TrimSpace(item.Enclosure.Length), 10, 64)
	}
	if size < 0 {
		size = 0
	}
	return Release{ID: id, Title: title, Size: size, Published: parseDate(item.PubDate)}, true
}

func (item rssItem) attribute(name string) string {
	for _, attr := range item.Attributes {
		if attr.Name == name {
			return attr.Value
		}
	}
	return ""
}

func normalizeID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && (parsed.Host != "" || strings.HasPrefix(raw, "/")) {
		segment := path.Base(parsed.Path)
		if segment == "." || segment == "/" {
			return ""
		}
		raw = segment
	}
	if len(raw) > 4 && strings.EqualFold(raw[len(raw)-4:], ".nzb") {
		raw = raw[:len(raw)-4]
	}
	return raw
}

func validID(id string) bool {
	if id == "" || len(id) > maxIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

var dateLayouts = []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.ANSIC}

func parseDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range dateLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
