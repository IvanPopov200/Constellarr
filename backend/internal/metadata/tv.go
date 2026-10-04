package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	movieType  = "movie"
	seriesType = "series"
	// OMDb season numbering and season sizes stay within these bounds.
	maxSeasons        = 100
	maxSeasonEpisodes = 1000
)

func (c *Client) SearchSeries(ctx context.Context, query string, page int) ([]Title, error) {
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
	params := url.Values{"s": {query}, "type": {seriesType}, "page": {strconv.Itoa(page)}}
	body, err := c.get(ctx, "series search", params, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	return decodeSearch("series search", body, seriesType)
}

func (c *Client) LookupSeries(ctx context.Context, imdbID string) (Title, error) {
	id := strings.ToLower(strings.TrimSpace(imdbID))
	if !ValidIMDbID(id) {
		return Title{}, errors.New("metadata: an IMDb ID must be tt followed by 7 to 12 digits")
	}
	body, err := c.get(ctx, "series lookup", url.Values{"i": {id}, "plot": {"full"}}, maxResponseBytes)
	if err != nil {
		return Title{}, err
	}
	return decodeTitle("series lookup", id, body, seriesType)
}

// Season returns the episodes of one series season; season 0 requests specials.
func (c *Client) Season(ctx context.Context, seriesIMDbID string, season int) ([]Episode, error) {
	id := strings.ToLower(strings.TrimSpace(seriesIMDbID))
	if !ValidIMDbID(id) {
		return nil, errors.New("metadata: an IMDb ID must be tt followed by 7 to 12 digits")
	}
	if season < 0 || season > maxSeasons {
		return nil, errors.New("metadata: a season must be between 0 and " + strconv.Itoa(maxSeasons))
	}
	params := url.Values{"i": {id}, "Season": {strconv.Itoa(season)}}
	body, err := c.get(ctx, "season", params, maxResponseBytes)
	if err != nil {
		return nil, err
	}
	return decodeSeason("season", id, season, body)
}

type seasonResponse struct {
	Season   string        `json:"Season"`
	SeriesID string        `json:"seriesID"`
	IMDbID   string        `json:"imdbID"`
	Episodes []episodeItem `json:"Episodes"`
	Response string        `json:"Response"`
	Error    string        `json:"Error"`
}

type episodeItem struct {
	Title      string `json:"Title"`
	Released   string `json:"Released"`
	Episode    string `json:"Episode"`
	IMDbRating string `json:"imdbRating"`
	IMDbID     string `json:"imdbID"`
}

// decodeSeason maps one season, rejecting a different series, season, or inconsistent episode list.
func decodeSeason(op, imdbID string, season int, body []byte) ([]Episode, error) {
	var payload seasonResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &Error{Op: op, Kind: "invalid response"}
	}
	if providerFailed(payload.Response, payload.Error) {
		return nil, classifyProviderError(op, payload.Error, 0)
	}
	if number, err := strconv.Atoi(strings.TrimSpace(payload.Season)); err != nil || number != season {
		return nil, &Error{Op: op, Kind: "invalid response"}
	}
	for _, candidate := range []string{payload.SeriesID, payload.IMDbID} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && (!ValidIMDbID(candidate) || candidate != imdbID) {
			return nil, &Error{Op: op, Kind: "invalid response"}
		}
	}
	if len(payload.Episodes) > maxSeasonEpisodes {
		return nil, &Error{Op: op, Kind: "invalid response"}
	}
	episodes := make([]Episode, 0, len(payload.Episodes))
	seen := make(map[int]bool, len(payload.Episodes))
	for _, item := range payload.Episodes {
		id := strings.TrimSpace(item.IMDbID)
		if !ValidIMDbID(id) {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSpace(item.Episode))
		if err != nil || number <= 0 || number > maxSeasonEpisodes || seen[number] {
			return nil, &Error{Op: op, Kind: "invalid response"}
		}
		seen[number] = true
		episodes = append(episodes, Episode{
			IMDbID:  id,
			Title:   strings.TrimSpace(item.Title),
			Season:  season,
			Number:  number,
			AirDate: parseDate(item.Released),
			Rating:  parseRating(item.IMDbRating),
		})
	}
	return episodes, nil
}

func parseTotalSeasons(raw string) int {
	seasons, err := strconv.Atoi(clean(raw))
	if err != nil || seasons < 1 {
		return 0
	}
	if seasons > maxSeasons {
		return maxSeasons
	}
	return seasons
}
