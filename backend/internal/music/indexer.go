package music

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

const (
	newznabTimeout    = 30 * time.Second
	maxNewznabBytes   = 16 << 20
	musicCategory     = 3000
	musicCategoryMax  = 3999
	musicSearchLimit  = 50
	maxMusicQueryLen  = 256
	maxReleaseIDBytes = 128
)

// newznabClient searches the configured Newznab endpoint for audio releases only.
type newznabClient struct {
	base *url.URL
	key  string
	http *http.Client
}

func newNewznab(cfg downloads.Config) (*newznabClient, error) {
	base, err := apiBaseURL(cfg.IndexerURL, "")
	if err != nil {
		return nil, ErrNotConfigured
	}
	client := &newznabClient{base: base, key: strings.TrimSpace(cfg.APIKey)}
	client.http = &http.Client{Timeout: newznabTimeout, CheckRedirect: sameOriginRedirect(base)}
	return client, nil
}

func (c *newznabClient) Test(ctx context.Context) error {
	_, err := c.search(ctx, url.Values{"q": {"music"}, "limit": {"1"}})
	return err
}

// SearchAlbum uses the Newznab audio search, falling back to a music-category search on providers without it.
func (c *newznabClient) SearchAlbum(ctx context.Context, artist, album string, year int) ([]Release, error) {
	artist = strings.TrimSpace(artist)
	album = strings.TrimSpace(album)
	if album == "" && artist == "" {
		return nil, fmt.Errorf("%w: an artist or album is required", ErrInvalid)
	}
	if utf8.RuneCountInString(artist) > maxMusicQueryLen || utf8.RuneCountInString(album) > maxMusicQueryLen {
		return nil, fmt.Errorf("%w: the music search query is too long", ErrInvalid)
	}
	// Some audio endpoints, including NZBGeek, only filter on q.
	params := url.Values{"q": {strings.TrimSpace(artist + " " + album)}}
	if album != "" {
		params.Set("album", album)
	}
	if artist != "" {
		params.Set("artist", artist)
	}
	if year > 0 {
		params.Set("year", strconv.Itoa(year))
	}
	return c.search(ctx, params)
}

func (c *newznabClient) SearchQuery(ctx context.Context, query string) ([]Release, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: a search query is required", ErrInvalid)
	}
	if utf8.RuneCountInString(query) > maxMusicQueryLen {
		return nil, fmt.Errorf("%w: the music search query is too long", ErrInvalid)
	}
	return c.search(ctx, url.Values{"q": {query}})
}

// Feed returns recent audio releases for scheduled RSS matching.
func (c *newznabClient) Feed(ctx context.Context) ([]Release, error) {
	return c.search(ctx, url.Values{"limit": {"100"}})
}

// search retries through the music category when the provider lacks the Newznab audio function.
func (c *newznabClient) search(ctx context.Context, params url.Values) ([]Release, error) {
	releases, err := c.parseRequest(ctx, "music", params)
	if err == nil {
		return releases, nil
	}
	var apiErr *newznabError
	if errors.As(err, &apiErr) && apiErr.Code != 0 {
		return c.parseRequest(ctx, "search", params)
	}
	return nil, err
}

func (c *newznabClient) parseRequest(ctx context.Context, function string, params url.Values) ([]Release, error) {
	body, err := c.request(ctx, function, params)
	if err != nil {
		return nil, err
	}
	return parseMusicSearch(body)
}

func (c *newznabClient) request(ctx context.Context, function string, params url.Values) ([]byte, error) {
	query := url.Values{
		"t":        {function},
		"cat":      {strconv.Itoa(musicCategory)},
		"extended": {"1"},
		"limit":    {strconv.Itoa(musicSearchLimit)},
		"apikey":   {c.key},
	}
	for key, values := range params {
		for _, value := range values {
			if value != "" {
				query.Set(key, value)
			}
		}
	}
	target := *c.base
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, errors.New("music: the indexer request could not be built")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("music: the indexer could not be reached")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("music: the indexer returned HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxNewznabBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("music: the indexer response could not be read")
	}
	if int64(len(body)) > maxNewznabBytes {
		return nil, errors.New("music: the indexer response is too large")
	}
	return body, nil
}

type newznabError struct {
	Op   string
	Code int
}

func (e *newznabError) Error() string {
	if e.Code != 0 {
		return "music: the indexer rejected the " + e.Op + " request (API error " + strconv.Itoa(e.Code) + ")"
	}
	return "music: the indexer returned an unusable " + e.Op + " response"
}

type musicFeed struct {
	Channel struct {
		Items []musicItem `xml:"item"`
	} `xml:"channel"`
}

type musicItem struct {
	Title      string         `xml:"title"`
	GUID       string         `xml:"guid"`
	Link       string         `xml:"link"`
	PubDate    string         `xml:"pubDate"`
	Categories []string       `xml:"category"`
	Attributes []musicAttr    `xml:"attr"`
	Enclosure  musicEnclosure `xml:"enclosure"`
}

type musicAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type musicEnclosure struct {
	Length string `xml:"length,attr"`
}

func parseMusicSearch(body []byte) ([]Release, error) {
	root, attrs, err := firstElement(body)
	if err != nil {
		return nil, &newznabError{Op: "search"}
	}
	if root == "error" {
		code, _ := strconv.Atoi(attrs["code"])
		return nil, &newznabError{Op: "search", Code: code}
	}
	if root != "rss" {
		return nil, &newznabError{Op: "search"}
	}
	var feed musicFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, &newznabError{Op: "search"}
	}
	releases := make([]Release, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		if release, ok := item.release(); ok {
			releases = append(releases, release)
		}
	}
	return releases, nil
}

func (item musicItem) release() (Release, bool) {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return Release{}, false
	}
	id := normalizeReleaseID(item.attr("guid"))
	if !validReleaseID(id) {
		id = normalizeReleaseID(item.GUID)
	}
	if !validReleaseID(id) {
		id = normalizeReleaseID(item.Link)
	}
	if !validReleaseID(id) || !item.isMusic() {
		return Release{}, false
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(item.attr("size")), 10, 64)
	if size <= 0 {
		size, _ = strconv.ParseInt(strings.TrimSpace(item.Enclosure.Length), 10, 64)
	}
	if size < 0 {
		size = 0
	}
	release := Release{
		ID: id, Title: title, Size: size, Published: parseReleaseDate(item.PubDate),
		Artist: strings.TrimSpace(item.attr("artist")), Album: strings.TrimSpace(item.attr("album")),
	}
	if year, err := strconv.Atoi(strings.TrimSpace(item.attr("year"))); err == nil && year > 1900 && year < 2200 {
		release.Year = year
	}
	return release, true
}

func (item musicItem) attr(name string) string {
	for _, attr := range item.Attributes {
		if strings.EqualFold(strings.TrimSpace(attr.Name), name) {
			return attr.Value
		}
	}
	return ""
}

func (item musicItem) categoryValues() []string {
	values := make([]string, 0, len(item.Categories)+1)
	values = append(values, item.Categories...)
	for _, attr := range item.Attributes {
		if strings.EqualFold(strings.TrimSpace(attr.Name), "category") {
			values = append(values, attr.Value)
		}
	}
	return values
}

// isMusic accepts audio categories and rejects items explicitly tagged as another media type.
func (item musicItem) isMusic() bool {
	values := item.categoryValues()
	if len(values) == 0 {
		return true
	}
	audio := false
	for _, value := range values {
		for _, field := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '/' || r == ' ' }) {
			number, err := strconv.Atoi(strings.TrimSpace(field))
			if err != nil {
				continue
			}
			switch {
			case number >= musicCategory && number <= musicCategoryMax:
				audio = true
			case number >= 1000 && number < musicCategory, number > musicCategoryMax && number < 10000:
				return false
			}
		}
	}
	if !audio {
		// Text categories such as "Audio > MP3" are only usable when they mention audio.
		for _, value := range values {
			lower := strings.ToLower(value)
			if strings.Contains(lower, "audio") || strings.Contains(lower, "music") || strings.Contains(lower, "(3000") {
				return true
			}
		}
	}
	return audio
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

func normalizeReleaseID(raw string) string {
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

func validReleaseID(id string) bool {
	if id == "" || len(id) > maxReleaseIDBytes {
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

var releaseDateLayouts = []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.ANSIC}

func parseReleaseDate(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	for _, layout := range releaseDateLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
