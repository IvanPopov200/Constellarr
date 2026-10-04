package metadata

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func ValidIMDbID(id string) bool {
	if len(id) < 9 || len(id) > 14 || id[0] != 't' || id[1] != 't' {
		return false
	}
	for i := 2; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

type searchResponse struct {
	Search   []searchItem `json:"Search"`
	Response string       `json:"Response"`
	Error    string       `json:"Error"`
}

type searchItem struct {
	Title  string `json:"Title"`
	Year   string `json:"Year"`
	IMDbID string `json:"imdbID"`
	Type   string `json:"Type"`
	Poster string `json:"Poster"`
}

type titleResponse struct {
	Title      string `json:"Title"`
	Year       string `json:"Year"`
	Released   string `json:"Released"`
	Runtime    string `json:"Runtime"`
	Genre      string `json:"Genre"`
	Director   string `json:"Director"`
	Actors     string `json:"Actors"`
	Language   string `json:"Language"`
	Country    string `json:"Country"`
	Rated      string `json:"Rated"`
	Poster     string `json:"Poster"`
	Plot       string `json:"Plot"`
	IMDbRating string `json:"imdbRating"`
	IMDbVotes  string `json:"imdbVotes"`
	IMDbID     string `json:"imdbID"`
	Type       string `json:"Type"`
	Response   string `json:"Response"`
	Error      string `json:"Error"`
}

func decodeSearch(op string, body []byte) ([]Title, error) {
	var payload searchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &Error{Op: op, Kind: "invalid response"}
	}
	if providerFailed(payload.Response, payload.Error) {
		if notFound(payload.Error) {
			return []Title{}, nil
		}
		return nil, classifyProviderError(op, payload.Error, 0)
	}
	if payload.Response == "" && payload.Search == nil {
		return nil, &Error{Op: op, Kind: "invalid response"}
	}
	titles := make([]Title, 0, len(payload.Search))
	for _, item := range payload.Search {
		id := strings.TrimSpace(item.IMDbID)
		if !ValidIMDbID(id) || (item.Type != "" && item.Type != "movie") {
			continue
		}
		titles = append(titles, Title{
			IMDbID: id,
			Title:  strings.TrimSpace(item.Title),
			Year:   parseYear(item.Year),
			Type:   item.Type,
			Poster: clean(item.Poster),
		})
	}
	return titles, nil
}

func decodeTitle(op, imdbID string, body []byte) (Title, error) {
	var payload titleResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return Title{}, &Error{Op: op, Kind: "invalid response"}
	}
	if providerFailed(payload.Response, payload.Error) {
		return Title{}, classifyProviderError(op, payload.Error, 0)
	}
	if payload.Response == "" && payload.Title == "" && payload.IMDbID == "" {
		return Title{}, &Error{Op: op, Kind: "invalid response"}
	}
	if payload.Type != "" && payload.Type != "movie" {
		return Title{}, &Error{Op: op, Kind: "not a movie"}
	}
	title := mapTitle(payload)
	if ValidIMDbID(title.IMDbID) && title.IMDbID != imdbID {
		return Title{}, &Error{Op: op, Kind: "invalid response"}
	}
	if !ValidIMDbID(title.IMDbID) {
		title.IMDbID = imdbID
	}
	return title, nil
}

func providerFailed(response, message string) bool {
	return strings.EqualFold(strings.TrimSpace(response), "false") || strings.TrimSpace(message) != ""
}

func notFound(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "not found") || strings.Contains(message, "does not exist") ||
		strings.Contains(message, "incorrect imdb id")
}

func envelopeError(body []byte) string {
	var payload struct {
		Response string `json:"Response"`
		Error    string `json:"Error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.Error)
}

func classifyProviderError(op, message string, status int) error {
	message = strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.Contains(message, "limit") || strings.Contains(message, "too many"):
		return &Error{Op: op, Kind: "rate limited", Status: status, Retry: true}
	case strings.Contains(message, "invalid api key") || strings.Contains(message, "invalid key"):
		return &Error{Op: op, Kind: "api key", Status: status}
	case notFound(message):
		return &Error{Op: op, Kind: "not found", Status: status}
	case status == http.StatusTooManyRequests:
		return &Error{Op: op, Kind: "rate limited", Status: status, Retry: true}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Op: op, Kind: "api key", Status: status}
	case status >= http.StatusInternalServerError:
		return &Error{Op: op, Kind: "unavailable", Status: status, Retry: true}
	default:
		return &Error{Op: op, Kind: "request failed", Status: status}
	}
}

func mapTitle(payload titleResponse) Title {
	return Title{
		IMDbID:        strings.TrimSpace(payload.IMDbID),
		Title:         strings.TrimSpace(payload.Title),
		Year:          parseYear(payload.Year),
		Type:          payload.Type,
		Released:      parseDate(payload.Released),
		Rating:        parseRating(payload.IMDbRating),
		Votes:         parseVotes(payload.IMDbVotes),
		Runtime:       parseRuntime(payload.Runtime),
		Directors:     splitList(payload.Director),
		Cast:          splitList(payload.Actors),
		Genres:        splitList(payload.Genre),
		Languages:     splitList(payload.Language),
		Countries:     splitList(payload.Country),
		Certification: clean(payload.Rated),
		Poster:        clean(payload.Poster),
		Plot:          clean(payload.Plot),
	}
}

var releaseLayouts = []string{"2006-01-02", "02 Jan 2006", "2 Jan 2006"}

func parseDate(raw string) string {
	raw = clean(raw)
	for _, layout := range releaseLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	return ""
}

func parseYear(raw string) int {
	raw = strings.TrimSpace(raw)
	if len(raw) > 4 {
		raw = raw[:4]
	}
	year, err := strconv.Atoi(raw)
	if err != nil || year < 1870 || year > 2200 {
		return 0
	}
	return year
}

func parseRating(raw string) *float64 {
	raw = clean(raw)
	if raw == "" {
		return nil
	}
	rating, err := strconv.ParseFloat(raw, 64)
	if err != nil || rating < 0 || rating > 10 {
		return nil
	}
	return &rating
}

func parseVotes(raw string) int {
	raw = clean(raw)
	if raw == "" {
		return 0
	}
	votes, err := strconv.Atoi(strings.NewReplacer(",", "", " ", "").Replace(raw))
	if err != nil || votes < 0 {
		return 0
	}
	return votes
}

func parseRuntime(raw string) int {
	raw = clean(raw)
	if raw == "" {
		return 0
	}
	minutes, err := strconv.Atoi(strings.Fields(raw)[0])
	if err != nil || minutes <= 0 {
		return 0
	}
	return minutes
}

func splitList(raw string) []string {
	raw = clean(raw)
	if raw == "" {
		return nil
	}
	values := make([]string, 0, strings.Count(raw, ",")+1)
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func clean(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "N/A") {
		return ""
	}
	return raw
}
