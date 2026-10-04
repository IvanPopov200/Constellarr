package tv

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/IvanPopov200/Constellarr/backend/internal/indexer"
	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

func releaseEpisodes(series Series, episodes []Episode, release indexer.Release) ([]Episode, bool, error) {
	identity, ok := library.ParseEpisode(release.Title)
	if !ok {
		return nil, false, fmt.Errorf("%w: the release name does not identify a TV episode", ErrInvalid)
	}
	if release.IMDbID != "" && !strings.EqualFold(strings.TrimSpace(release.IMDbID), strings.TrimSpace(series.Metadata.IMDbID)) {
		return nil, false, fmt.Errorf("%w: the release belongs to a different series", ErrInvalid)
	}
	if len(episodes) == 0 {
		return nil, false, fmt.Errorf("%w: the series has no catalog episodes yet", ErrInvalid)
	}
	imdbMatched := release.IMDbID != "" && series.Metadata.IMDbID != ""
	if !imdbMatched {
		if identity.Title == "" {
			return nil, false, fmt.Errorf("%w: the release name has no series title", ErrInvalid)
		}
		if series.Metadata.Title != "" && normalizeTVTitle(identity.Title) != normalizeTVTitle(series.Metadata.Title) {
			return nil, false, fmt.Errorf("%w: the release title does not match this series", ErrInvalid)
		}
		if identity.Year > 0 && series.Metadata.Year > 0 && identity.Year != series.Metadata.Year {
			return nil, false, fmt.Errorf("%w: the release year does not match this series", ErrInvalid)
		}
	}
	if identity.AirDate != "" {
		matched := make([]Episode, 0, 2)
		for _, episode := range episodes {
			if tvSameAirDate(episode.AirDate, identity.AirDate) {
				matched = append(matched, episode)
			}
		}
		if len(matched) != 1 {
			return nil, false, fmt.Errorf("%w: the release air date does not match exactly one episode", ErrInvalid)
		}
		return matched, false, nil
	}
	season := identity.Season
	if season < 0 {
		season = release.Season
	}
	if season < 0 || season > maxSeason {
		return nil, false, fmt.Errorf("%w: the release does not identify a season", ErrInvalid)
	}
	seasonEpisodes := make([]Episode, 0, len(episodes))
	for _, episode := range episodes {
		if episode.Season == season {
			seasonEpisodes = append(seasonEpisodes, episode)
		}
	}
	if len(seasonEpisodes) == 0 {
		return nil, false, fmt.Errorf("%w: no catalog episodes exist for season %d", ErrInvalid, season)
	}
	if identity.Pack {
		return seasonEpisodes, true, nil
	}
	if len(identity.Numbers) == 0 {
		return nil, false, fmt.Errorf("%w: the release does not identify an episode", ErrInvalid)
	}
	byNumber := make(map[int]Episode, len(seasonEpisodes))
	for _, episode := range seasonEpisodes {
		byNumber[episode.Number] = episode
	}
	matched := make([]Episode, 0, len(identity.Numbers))
	for _, number := range identity.Numbers {
		episode, ok := byNumber[number]
		if !ok {
			return nil, false, fmt.Errorf("%w: episode %d is not in the catalog", ErrInvalid, number)
		}
		matched = append(matched, episode)
	}
	sort.SliceStable(matched, func(i, j int) bool { return matched[i].Number < matched[j].Number })
	return matched, false, nil
}

func evaluateRelease(profile quality.Profile, series Series, episodes []Episode, release indexer.Release) Release {
	covered, pack, err := releaseEpisodes(series, episodes, release)
	item := Release{Release: release, EpisodeIDs: []string{}, Pack: pack}
	if err != nil {
		item.Decision = quality.Decision{Rank: -1, Reasons: []string{tvReleaseReason(err)}}
		return item
	}
	item.EpisodeIDs = make([]string, 0, len(covered))
	for _, episode := range covered {
		item.EpisodeIDs = append(item.EpisodeIDs, episode.ID)
	}
	size := release.Size
	if len(covered) > 1 {
		size = release.Size / int64(len(covered))
	}
	var chosen *quality.Decision
	for _, episode := range covered {
		decision := quality.Evaluate(profile, release.Title, size, bestCurrent(profile, tvUsableFiles(episode.Files)))
		if chosen == nil || tvBetterDecision(decision, *chosen) {
			candidate := decision
			chosen = &candidate
		}
	}
	if chosen == nil {
		item.Decision = quality.Decision{Rank: -1, Reasons: []string{"the release does not match any catalog episode"}}
		return item
	}
	item.Decision = *chosen
	return item
}

func Needed(profile quality.Profile, episode Episode) bool {
	if !episode.Monitored || !tvAired(episode.AirDate) {
		return false
	}
	files := tvUsableFiles(episode.Files)
	if len(files) == 0 {
		return true
	}
	if !profile.Upgrade {
		return false
	}
	current := bestCurrent(profile, files)
	return current == nil || !quality.Satisfied(profile, *current)
}

func tvBetterDecision(a, b quality.Decision) bool {
	if a.Allowed != b.Allowed {
		return a.Allowed
	}
	if a.Rank != b.Rank {
		if a.Rank < 0 {
			return false
		}
		if b.Rank < 0 {
			return true
		}
		return a.Rank < b.Rank
	}
	return a.Score > b.Score
}

func tvReleaseReason(err error) string {
	message := strings.TrimSpace(err.Error())
	return strings.TrimSpace(strings.TrimPrefix(message, ErrInvalid.Error()+": "))
}

func tvUsableFiles(files []movies.File) []movies.File {
	usable := make([]movies.File, 0, len(files))
	for _, file := range files {
		if file.Missing || file.Path == "" || file.Size <= 0 {
			continue
		}
		usable = append(usable, file)
	}
	return usable
}

func tvEpisodeAcquisition(episodeID string, acquisitions []Acquisition) *Acquisition {
	for i := range acquisitions {
		acquisition := &acquisitions[i]
		if !tvContains(acquisition.EpisodeIDs, episodeID) {
			continue
		}
		switch acquisition.Status {
		case "superseded", "imported":
			return nil
		}
		return acquisition
	}
	return nil
}

func tvContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func tvSameAirDate(left, right string) bool {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	a, okA := tvAirDate(left)
	b, okB := tvAirDate(right)
	return okA && okB && a.Equal(b)
}

func tvAirDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{dateLayout, "02 Jan 2006", "2 Jan 2006", "2006/01/02", "Jan 2, 2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func tvAired(airDate string) bool {
	date, ok := tvAirDate(airDate)
	if !ok {
		return false
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return !date.After(today)
}

// tvFutureDate is false for unknown dates; callers treat unknown separately from a future airing.
func tvFutureDate(airDate string) bool {
	date, ok := tvAirDate(airDate)
	if !ok {
		return false
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return date.After(today)
}

func tvUpcoming(airDate string) bool {
	date, ok := tvAirDate(airDate)
	if !ok {
		return false
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return !date.Before(today)
}

func normalizeTVTitle(title string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, title)
}

func tvFindRelease(releases []indexer.Release, releaseID string) (indexer.Release, bool) {
	for i := range releases {
		if releases[i].ID == releaseID {
			return releases[i], true
		}
	}
	return indexer.Release{}, false
}

func tvSortReleases(releases []Release) {
	sort.SliceStable(releases, func(i, j int) bool {
		a, b := releases[i].Decision, releases[j].Decision
		if (a.Rank < 0) != (b.Rank < 0) {
			return b.Rank < 0
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return releases[i].Published.After(releases[j].Published)
	})
}

func tvOverlap(left, right []string) bool {
	seen := make(map[string]bool, len(left))
	for _, id := range left {
		seen[id] = true
	}
	for _, id := range right {
		if seen[id] {
			return true
		}
	}
	return false
}

func tvMetadataNotFound(err error) bool {
	var providerErr *metadata.Error
	return errors.As(err, &providerErr) && providerErr.Kind == "not found"
}
