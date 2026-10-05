package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMusicBrainzURL = "https://musicbrainz.org/ws/2/"
	defaultCoverArtURL    = "https://coverartarchive.org/"
	brainzTimeout         = 20 * time.Second
	maxBrainzBytes        = 8 << 20
	maxCoverBytes         = 8 << 20
	maxBrainzSearch       = 50
	brainzUserAgent       = "Constellarr/1.0 (self-hosted media server)"
)

var errCoverUnavailable = errors.New("music: cover art is unavailable")

// brainzClient talks to the official MusicBrainz JSON service and honors its one-request-per-second policy.
type brainzClient struct {
	base     *url.URL
	http     *http.Client
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

func newBrainz(rawURL string, interval time.Duration) (*brainzClient, error) {
	base, err := apiBaseURL(rawURL, defaultMusicBrainzURL)
	if err != nil {
		return nil, err
	}
	if interval < 0 {
		return nil, errors.New("music: the MusicBrainz rate limit is invalid")
	}
	return &brainzClient{base: base, interval: interval, http: &http.Client{Timeout: brainzTimeout, CheckRedirect: sameOriginRedirect(base)}}, nil
}

func apiBaseURL(rawURL, fallback string) (*url.URL, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		raw = fallback
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("music: the provider URL must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("music: the provider URL must not include credentials, a query, or a fragment")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/"
	parsed.RawPath = ""
	return parsed, nil
}

func sameOriginRedirect(base *url.URL) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("music: too many redirects")
		}
		if !strings.EqualFold(req.URL.Scheme, base.Scheme) || !strings.EqualFold(req.URL.Host, base.Host) {
			return errors.New("music: redirect left the configured origin")
		}
		return nil
	}
}

func (c *brainzClient) wait(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if delay := time.Until(c.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.next = time.Now().Add(c.interval)
	return nil
}

func (c *brainzClient) get(ctx context.Context, endpoint string, params url.Values, maxBytes int64) ([]byte, error) {
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	// Lookup endpoints pass nil; assigning into a nil map would panic.
	if params == nil {
		params = url.Values{}
	}
	params.Set("fmt", "json")
	target := *c.base
	target.Path += strings.TrimPrefix(endpoint, "/")
	target.RawQuery = params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, errors.New("music: the provider request could not be built")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", brainzUserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("music: the metadata provider could not be reached")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("music: the metadata provider returned HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("music: the metadata response could not be read")
	}
	if int64(len(body)) > maxBytes {
		return nil, errors.New("music: the metadata response is too large")
	}
	return body, nil
}

type mbArtist struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SortName       string `json:"sort-name"`
	Disambiguation string `json:"disambiguation"`
	Country        string `json:"country"`
	Type           string `json:"type"`
	Score          int    `json:"score"`
}

type mbArtistCredit struct {
	Name   string `json:"name"`
	Artist struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		SortName string `json:"sort-name"`
	} `json:"artist"`
}

type mbReleaseGroup struct {
	ID               string           `json:"id"`
	Title            string           `json:"title"`
	PrimaryType      string           `json:"primary-type"`
	SecondaryTypes   []string         `json:"secondary-types"`
	FirstReleaseDate string           `json:"first-release-date"`
	Score            int              `json:"score"`
	ArtistCredit     []mbArtistCredit `json:"artist-credit"`
	Releases         []struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Date   string `json:"date"`
		Status string `json:"status"`
	} `json:"releases"`
}

type mbTrack struct {
	ID           string           `json:"id"`
	Number       string           `json:"number"`
	Position     int              `json:"position"`
	Title        string           `json:"title"`
	Length       int              `json:"length"`
	ArtistCredit []mbArtistCredit `json:"artist-credit"`
}

type mbRelease struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Date    string `json:"date"`
	Country string `json:"country"`
	Media   []struct {
		Position   int       `json:"position"`
		Format     string    `json:"format"`
		TrackCount int       `json:"track-count"`
		Tracks     []mbTrack `json:"tracks"`
	} `json:"media"`
}

func (c *brainzClient) searchArtists(ctx context.Context, query string, limit, offset int) ([]ArtistResult, error) {
	body, err := c.get(ctx, "artist",
		url.Values{"query": {query}, "limit": {strconv.Itoa(boundLimit(limit))}, "offset": {strconv.Itoa(max(offset, 0))}}, maxBrainzBytes)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Artists []mbArtist `json:"artists"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("music: the metadata provider returned an unusable response")
	}
	results := make([]ArtistResult, 0, len(payload.Artists))
	for _, artist := range payload.Artists {
		if artist.ID == "" || strings.TrimSpace(artist.Name) == "" {
			continue
		}
		results = append(results, ArtistResult{
			MusicBrainzID: artist.ID, Name: artist.Name, SortName: artist.SortName,
			Disambiguation: artist.Disambiguation, Country: artist.Country, Type: artist.Type, Score: artist.Score,
		})
	}
	return results, nil
}

func (c *brainzClient) searchAlbums(ctx context.Context, query string, limit, offset int) ([]AlbumResult, error) {
	body, err := c.get(ctx, "release-group",
		url.Values{"query": {query}, "limit": {strconv.Itoa(boundLimit(limit))}, "offset": {strconv.Itoa(max(offset, 0))}}, maxBrainzBytes)
	if err != nil {
		return nil, err
	}
	var payload struct {
		ReleaseGroups []mbReleaseGroup `json:"release-groups"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("music: the metadata provider returned an unusable response")
	}
	results := make([]AlbumResult, 0, len(payload.ReleaseGroups))
	for _, group := range payload.ReleaseGroups {
		if group.ID == "" || strings.TrimSpace(group.Title) == "" {
			continue
		}
		result := AlbumResult{MusicBrainzID: group.ID, Title: group.Title, Type: releaseGroupType(group), Score: group.Score}
		result.Year = yearFromDate(group.FirstReleaseDate)
		if len(group.ArtistCredit) > 0 {
			result.ArtistName = strings.TrimSpace(group.ArtistCredit[0].Name)
			result.ArtistID = group.ArtistCredit[0].Artist.ID
		}
		results = append(results, result)
	}
	return results, nil
}

func (c *brainzClient) lookupArtist(ctx context.Context, mbid string) (Artist, error) {
	if !validMBID(mbid) {
		return Artist{}, fmt.Errorf("%w: the MusicBrainz artist ID is invalid", ErrInvalid)
	}
	body, err := c.get(ctx, "artist/"+mbid, nil, maxBrainzBytes)
	if err != nil {
		return Artist{}, err
	}
	var artist mbArtist
	if err := json.Unmarshal(body, &artist); err != nil || artist.ID == "" {
		return Artist{}, errors.New("music: the metadata provider returned an unusable response")
	}
	return Artist{
		ID: newID(), MusicBrainzID: artist.ID, Name: artist.Name, SortName: artist.SortName,
		Disambiguation: artist.Disambiguation, Country: artist.Country, Type: artist.Type,
	}, nil
}

// artistAlbums lists release groups so a monitored artist can pick up new releases.
func (c *brainzClient) artistAlbums(ctx context.Context, mbid string, limit int) ([]AlbumResult, error) {
	if !validMBID(mbid) {
		return nil, fmt.Errorf("%w: the MusicBrainz artist ID is invalid", ErrInvalid)
	}
	body, err := c.get(ctx, "release-group",
		url.Values{"artist": {mbid}, "limit": {strconv.Itoa(boundLimit(limit))}}, maxBrainzBytes)
	if err != nil {
		return nil, err
	}
	var payload struct {
		ReleaseGroups []mbReleaseGroup `json:"release-groups"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("music: the metadata provider returned an unusable response")
	}
	results := make([]AlbumResult, 0, len(payload.ReleaseGroups))
	for _, group := range payload.ReleaseGroups {
		if group.ID == "" || strings.TrimSpace(group.Title) == "" {
			continue
		}
		results = append(results, AlbumResult{
			MusicBrainzID: group.ID, Title: group.Title, ArtistID: mbid,
			Year: yearFromDate(group.FirstReleaseDate), Type: releaseGroupType(group),
		})
	}
	return results, nil
}

// lookupAlbum resolves a release group to a concrete release, its identity, and its tracklist.
func (c *brainzClient) lookupAlbum(ctx context.Context, releaseGroupID string) (Album, error) {
	if !validMBID(releaseGroupID) {
		return Album{}, fmt.Errorf("%w: the MusicBrainz release group ID is invalid", ErrInvalid)
	}
	body, err := c.get(ctx, "release-group/"+releaseGroupID, url.Values{"inc": {"releases+artist-credits"}}, maxBrainzBytes)
	if err != nil {
		return Album{}, err
	}
	var group mbReleaseGroup
	if err := json.Unmarshal(body, &group); err != nil || group.ID == "" {
		return Album{}, errors.New("music: the metadata provider returned an unusable response")
	}
	album := Album{
		MusicBrainzID: group.ID, Title: group.Title, Type: releaseGroupType(group),
		ReleaseDate: group.FirstReleaseDate, Year: yearFromDate(group.FirstReleaseDate),
	}
	if len(group.ArtistCredit) > 0 {
		album.ArtistName = strings.TrimSpace(group.ArtistCredit[0].Name)
		album.ArtistID = group.ArtistCredit[0].Artist.ID
	}
	releaseID := ""
	for _, release := range group.Releases {
		if release.Status == "Official" || releaseID == "" {
			releaseID = release.ID
			if release.Status == "Official" {
				break
			}
		}
	}
	if releaseID == "" {
		return album, nil
	}
	if err := c.albumTracks(ctx, releaseID, &album); err != nil {
		return album, err
	}
	return album, nil
}

func (c *brainzClient) albumTracks(ctx context.Context, releaseID string, album *Album) error {
	body, err := c.get(ctx, "release/"+releaseID, url.Values{"inc": {"recordings+artist-credits"}}, maxBrainzBytes)
	if err != nil {
		return err
	}
	var release mbRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return errors.New("music: the metadata provider returned an unusable response")
	}
	for _, medium := range release.Media {
		disc := medium.Position
		if disc < 1 {
			disc = 1
		}
		for index, track := range medium.Tracks {
			number := track.Position
			if number < 1 {
				number, _ = strconv.Atoi(strings.TrimSpace(track.Number))
			}
			if number < 1 {
				number = index + 1
			}
			artist := ""
			if len(track.ArtistCredit) > 0 {
				artist = strings.TrimSpace(track.ArtistCredit[0].Name)
			}
			album.Tracks = append(album.Tracks, Track{
				ID: newID(), MusicBrainzID: track.ID, Disc: disc, Number: number,
				Title: track.Title, Artist: artist, DurationMS: track.Length,
			})
		}
	}
	if release.Date != "" {
		album.ReleaseDate = release.Date
		album.Year = yearFromDate(release.Date)
	}
	if strings.TrimSpace(album.Title) == "" {
		album.Title = release.Title
	}
	return nil
}

// cover fetches the release group front image when CoverArtArchive is configured.
func (c *brainzClient) cover(ctx context.Context, imageBase *url.URL, releaseGroupID string) ([]byte, string, error) {
	if imageBase == nil {
		return nil, "", ErrNotConfigured
	}
	if !validMBID(releaseGroupID) {
		return nil, "", fmt.Errorf("%w: the MusicBrainz release group ID is invalid", ErrInvalid)
	}
	if err := c.wait(ctx); err != nil {
		return nil, "", err
	}
	target := *imageBase
	target.Path += "release-group/" + releaseGroupID + "/front-500"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, "", errors.New("music: the cover request could not be built")
	}
	req.Header.Set("User-Agent", brainzUserAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", errors.New("music: the cover provider could not be reached")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", errCoverUnavailable
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("music: the cover provider returned HTTP status %d", resp.StatusCode)
	}
	contentType, ok := coverContentType(resp.Header.Get("Content-Type"))
	if !ok {
		return nil, "", errors.New("music: the cover provider returned an unsupported image")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
	if err != nil {
		return nil, "", errors.New("music: the cover response could not be read")
	}
	if len(body) == 0 || int64(len(body)) > maxCoverBytes {
		return nil, "", errors.New("music: the cover response is unusable")
	}
	return body, contentType, nil
}

func coverContentType(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(strings.Split(raw, ";")[0]))
	switch value {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return value, true
	default:
		return "", false
	}
}

func coverImageBase(rawURL string) (*url.URL, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, nil
	}
	base, err := apiBaseURL(rawURL, defaultCoverArtURL)
	if err != nil {
		return nil, err
	}
	return base, nil
}

func boundLimit(limit int) int {
	if limit < 1 {
		return 10
	}
	if limit > maxBrainzSearch {
		return maxBrainzSearch
	}
	return limit
}

func releaseGroupType(group mbReleaseGroup) string {
	value := strings.ToLower(strings.TrimSpace(group.PrimaryType))
	for _, secondary := range group.SecondaryTypes {
		if strings.EqualFold(secondary, "compilation") {
			return "compilation"
		}
		if strings.EqualFold(secondary, "live") {
			return "live"
		}
	}
	if value == "" {
		return "other"
	}
	return value
}

func yearFromDate(value string) int {
	if len(value) < 4 {
		return 0
	}
	year, err := strconv.Atoi(value[:4])
	if err != nil || year < 1900 || year > time.Now().Year()+5 {
		return 0
	}
	return year
}

func validMBID(id string) bool {
	id = strings.TrimSpace(id)
	if len(id) != 36 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c == '-' {
			if i != 8 && i != 13 && i != 18 && i != 23 {
				return false
			}
			continue
		}
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}
