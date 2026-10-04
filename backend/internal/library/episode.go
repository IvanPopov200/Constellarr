package library

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

const (
	maxEpisodeSeason  = 100
	maxEpisodeNumber  = 1000
	maxEpisodeNumbers = 100
)

type Episode struct {
	Season  int
	Numbers []int
	Title   string
	AirDate string
	IMDbID  string
	// Details optionally carries per-episode catalog identity for multi-episode files.
	Details []metadata.Episode
}

type EpisodeIdentity struct {
	Title   string `json:"title"`
	Year    int    `json:"year"`
	Season  int    `json:"season"`
	Numbers []int  `json:"episodes"`
	AirDate string `json:"airDate"`
	Pack    bool   `json:"pack"`
}

// ParseEpisode marks dated daily episodes with Season -1 and AirDate instead of numbers.
func ParseEpisode(name string) (EpisodeIdentity, bool) {
	stem := path.Base(strings.TrimSpace(filepath.ToSlash(name)))
	if ext := path.Ext(stem); videoExtensions[strings.ToLower(ext)] {
		stem = strings.TrimSuffix(stem, ext)
	}
	if stem == "" || stem == "." || stem == ".." {
		return EpisodeIdentity{}, false
	}
	if match := episodeMarker.FindStringSubmatchIndex(stem); match != nil {
		season, err := strconv.Atoi(stem[match[2]:match[3]])
		if err != nil || season < 0 || season > maxEpisodeSeason {
			return EpisodeIdentity{}, false
		}
		numbers, ok := parseEpisodeNumbers(stem[match[4]:match[5]], stem[match[6]:match[7]])
		if !ok {
			return EpisodeIdentity{}, false
		}
		title, year, _ := inferName(stem[:match[0]])
		return EpisodeIdentity{Title: title, Year: year, Season: season, Numbers: numbers}, true
	}
	if match := altEpisode.FindStringSubmatchIndex(stem); match != nil {
		season, err := strconv.Atoi(stem[match[2]:match[3]])
		number, numberErr := strconv.Atoi(stem[match[4]:match[5]])
		if err != nil || numberErr != nil || season > maxEpisodeSeason || number < 1 || number > maxEpisodeNumber {
			return EpisodeIdentity{}, false
		}
		title, year, _ := inferName(stem[:match[0]])
		return EpisodeIdentity{Title: title, Year: year, Season: season, Numbers: []int{number}}, true
	}
	if match := dailyMarker.FindStringSubmatchIndex(stem); match != nil {
		year, _ := strconv.Atoi(stem[match[2]:match[3]])
		month, _ := strconv.Atoi(stem[match[4]:match[5]])
		day, _ := strconv.Atoi(stem[match[6]:match[7]])
		if !validDate(year, month, day) {
			return EpisodeIdentity{}, false
		}
		title, prefixYear, _ := inferName(stem[:match[0]])
		return EpisodeIdentity{
			Title: title, Year: prefixYear, Season: -1,
			AirDate: fmt.Sprintf("%04d-%02d-%02d", year, month, day),
		}, true
	}
	if match := seasonPack.FindStringSubmatchIndex(stem); match != nil {
		var seasonText string
		if match[2] >= 0 {
			seasonText = stem[match[2]:match[3]]
		} else {
			seasonText = stem[match[4]:match[5]]
		}
		season, err := strconv.Atoi(seasonText)
		if err != nil || season < 0 || season > maxEpisodeSeason {
			return EpisodeIdentity{}, false
		}
		title, year, _ := inferName(stem[:match[0]])
		return EpisodeIdentity{Title: title, Year: year, Season: season, Pack: true}, true
	}
	return EpisodeIdentity{}, false
}

func validateEpisode(episode *Episode) error {
	if episode == nil {
		return nil
	}
	if episode.Season < 0 || episode.Season > maxEpisodeSeason {
		return fmt.Errorf("%w: season %d is outside 0..%d", ErrEpisode, episode.Season, maxEpisodeSeason)
	}
	if len(episode.Numbers) == 0 {
		return fmt.Errorf("%w: at least one episode number is required", ErrEpisode)
	}
	if len(episode.Numbers) > maxEpisodeNumbers {
		return fmt.Errorf("%w: %d episode numbers exceed the limit of %d", ErrEpisode, len(episode.Numbers), maxEpisodeNumbers)
	}
	seen := make(map[int]bool, len(episode.Numbers))
	for _, number := range episode.Numbers {
		if number < 1 || number > maxEpisodeNumber {
			return fmt.Errorf("%w: episode number %d is outside 1..%d", ErrEpisode, number, maxEpisodeNumber)
		}
		if seen[number] {
			return fmt.Errorf("%w: episode number %d is duplicated", ErrEpisode, number)
		}
		seen[number] = true
	}
	details := make(map[int]bool, len(episode.Details))
	for _, detail := range episode.Details {
		if detail.Season != episode.Season {
			return fmt.Errorf("%w: detail season %d does not match season %d", ErrEpisode, detail.Season, episode.Season)
		}
		if !seen[detail.Number] {
			return fmt.Errorf("%w: detail episode %d is not one of the episode numbers", ErrEpisode, detail.Number)
		}
		if details[detail.Number] {
			return fmt.Errorf("%w: detail episode %d is duplicated", ErrEpisode, detail.Number)
		}
		details[detail.Number] = true
	}
	return nil
}

func parseEpisodeNumbers(tokens, tail string) ([]int, bool) {
	var numbers []int
	for _, token := range episodeToken.FindAllStringSubmatch(tokens, -1) {
		number, err := strconv.Atoi(token[2][1:])
		if err != nil || number < 1 || number > maxEpisodeNumber {
			return nil, false
		}
		var ok bool
		if numbers, ok = appendEpisodeNumber(numbers, number, strings.Contains(token[1], "-")); !ok {
			return nil, false
		}
	}
	for _, token := range episodeRange.FindAllStringSubmatch(tail, -1) {
		number, err := strconv.Atoi(token[1])
		if err != nil || number < 1 || number > maxEpisodeNumber {
			return nil, false
		}
		var ok bool
		if numbers, ok = appendEpisodeNumber(numbers, number, true); !ok {
			return nil, false
		}
	}
	if len(numbers) == 0 {
		return nil, false
	}
	return numbers, true
}

func appendEpisodeNumber(numbers []int, number int, rangeEnd bool) ([]int, bool) {
	if rangeEnd && len(numbers) > 0 {
		previous := numbers[len(numbers)-1]
		if number < previous || len(numbers)+number-previous > maxEpisodeNumbers {
			return nil, false
		}
		for next := previous + 1; next <= number; next++ {
			numbers = append(numbers, next)
		}
		return numbers, true
	}
	for _, existing := range numbers {
		if existing == number {
			return numbers, true
		}
	}
	if len(numbers) >= maxEpisodeNumbers {
		return nil, false
	}
	return append(numbers, number), true
}

func validDate(year, month, day int) bool {
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return date.Year() == year && int(date.Month()) == month && date.Day() == day
}

func renderEpisodeToken(numbers []int) string {
	if len(numbers) == 0 {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "%02d", numbers[0])
	for _, number := range numbers[1:] {
		fmt.Fprintf(&builder, "-E%02d", number)
	}
	return builder.String()
}

func renderEpisodeCode(season int, numbers []int) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "S%02d", season)
	for _, number := range numbers {
		fmt.Fprintf(&builder, "E%02d", number)
	}
	return builder.String()
}

var (
	episodeMarker = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})((?:[ ._-]*-?[ ._-]*e\d{1,4})+)((?:[ ._-]*-[ ._-]*\d{2,3})*)(?:[^a-z0-9]|$)`)
	episodeToken  = regexp.MustCompile(`(?i)((?:[ ._-]*-[ ._-]*)|[ ._-]*)(e\d{1,4})`)
	episodeRange  = regexp.MustCompile(`[ ._-]*-[ ._-]*(\d{2,3})`)
	altEpisode    = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(\d{1,2})x(\d{1,3})(?:[^a-z0-9]|$)`)
	dailyMarker   = regexp.MustCompile(`(?:^|[^a-z0-9])((?:19|20)\d{2})[ ._-](\d{1,2})[ ._-](\d{1,2})(?:[^a-z0-9]|$)`)
	seasonPack    = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:s(\d{1,2})|season[ ._-]?(\d{1,2}))(?:[ ._-]*(?:complete|full|pack))?(?:[^a-z0-9]|$)`)
	seasonFolder  = regexp.MustCompile(`(?i)^(?:season[ ._-]?(\d{1,2})|s(\d{1,2})|specials)$`)
)
