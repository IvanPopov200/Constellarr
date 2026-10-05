package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	dateLayout       = "2006-01-02"
	defaultRangeDays = 180
	maxRangeDays     = 730
	maxCalendarItems = 1000
)

// Calendar merges the movie, TV, and optional music calendars into one bounded date range.
func (s *Service) Calendar(ctx context.Context, query CalendarQuery) (CalendarView, error) {
	from, to, err := s.calendarRange(query)
	if err != nil {
		return CalendarView{}, err
	}
	types, err := calendarSelection(query.Types, []string{MediaMovie, MediaTV, MediaMusic}, "media type")
	if err != nil {
		return CalendarView{}, err
	}
	sources, err := calendarSelection(query.Sources, []string{"movies", "tv", "music"}, "source")
	if err != nil {
		return CalendarView{}, err
	}
	view := CalendarView{
		Entries: []CalendarEntry{},
		From:    from.Format(dateLayout),
		To:      to.Format(dateLayout),
		Sources: []CalendarSource{
			{ID: "movies", MediaType: MediaMovie, Available: s.movies != nil},
			{ID: "tv", MediaType: MediaTV, Available: s.tv != nil},
			{ID: "music", MediaType: MediaMusic, Available: s.music != nil},
		},
	}
	if types[MediaMovie] && sources["movies"] {
		entries, err := s.movieCalendar(ctx, from, to)
		if err != nil {
			return CalendarView{}, err
		}
		view.Entries = append(view.Entries, entries...)
	}
	if types[MediaTV] && sources["tv"] {
		entries, err := s.tvCalendar(ctx, from, to)
		if err != nil {
			return CalendarView{}, err
		}
		view.Entries = append(view.Entries, entries...)
	}
	if types[MediaMusic] && sources["music"] && s.music != nil {
		entries, err := s.musicCalendar(ctx, from, to)
		if err != nil {
			return CalendarView{}, err
		}
		view.Entries = append(view.Entries, entries...)
	}
	sort.SliceStable(view.Entries, func(i, j int) bool {
		if view.Entries[i].Date != view.Entries[j].Date {
			return view.Entries[i].Date < view.Entries[j].Date
		}
		if view.Entries[i].MediaType != view.Entries[j].MediaType {
			return view.Entries[i].MediaType < view.Entries[j].MediaType
		}
		if view.Entries[i].Title != view.Entries[j].Title {
			return view.Entries[i].Title < view.Entries[j].Title
		}
		return view.Entries[i].ID < view.Entries[j].ID
	})
	if len(view.Entries) > maxCalendarItems {
		view.Entries = view.Entries[:maxCalendarItems]
	}
	return view, nil
}

func (s *Service) calendarRange(query CalendarQuery) (time.Time, time.Time, error) {
	today := s.now().UTC()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	from := today
	if raw := strings.TrimSpace(query.From); raw != "" {
		parsed, err := time.Parse(dateLayout, raw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("%w: from must be a date like 2026-01-31", ErrInvalid)
		}
		from = parsed
	}
	to := from.AddDate(0, 0, defaultRangeDays)
	if raw := strings.TrimSpace(query.To); raw != "" {
		parsed, err := time.Parse(dateLayout, raw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("%w: to must be a date like 2026-01-31", ErrInvalid)
		}
		to = parsed
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: the end date must not precede the start date", ErrInvalid)
	}
	if to.Sub(from) > time.Duration(maxRangeDays)*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: choose a range of at most %d days", ErrInvalid, maxRangeDays)
	}
	return from, to, nil
}

// calendarSelection turns comma-separated filters into a set; an empty filter selects every known value.
func calendarSelection(values, known []string, name string) (map[string]bool, error) {
	selected := make(map[string]bool, len(known))
	if len(values) == 0 {
		for _, value := range known {
			selected[value] = true
		}
		return selected, nil
	}
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			value := strings.ToLower(strings.TrimSpace(part))
			if value == "" {
				continue
			}
			valid := false
			for _, candidate := range known {
				if candidate == value {
					valid = true
					break
				}
			}
			if !valid {
				return nil, fmt.Errorf("%w: unknown calendar %s %q", ErrInvalid, name, value)
			}
			selected[value] = true
		}
	}
	return selected, nil
}

func inRange(date string, from, to time.Time) bool {
	parsed, err := time.Parse(dateLayout, strings.TrimSpace(date))
	if err != nil {
		return false
	}
	return !parsed.Before(from) && !parsed.After(to)
}

func (s *Service) movieCalendar(ctx context.Context, from, to time.Time) ([]CalendarEntry, error) {
	movies, err := s.movies.Calendar(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]CalendarEntry, 0, len(movies))
	for _, movie := range movies {
		date, ok := releaseDate(movie.Metadata.Released)
		if !ok || !inRange(date.Format(dateLayout), from, to) {
			continue
		}
		id := "movie-" + movie.ID
		if movie.Metadata.IMDbID != "" {
			id = "movie-" + movie.Metadata.IMDbID
		}
		title := strings.TrimSpace(movie.Metadata.Title)
		if title == "" {
			title = "Movie"
		}
		entries = append(entries, CalendarEntry{
			ID: id, MediaType: MediaMovie, Source: "movies", Title: title,
			Subtitle: movieStatusLabel(movie.Status), Date: date.Format(dateLayout), Year: movie.Metadata.Year,
			Poster: movie.Metadata.Poster, LibraryID: movie.ID, Status: movie.Status, IMDbID: movie.Metadata.IMDbID,
		})
	}
	return entries, nil
}

// releaseDate accepts the stored metadata layouts, treating partial dates as their first day.
func releaseDate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{dateLayout, "02 Jan 2006", "Jan 2, 2006", "2006/01/02", "2006-01", "2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func movieStatusLabel(status string) string {
	switch status {
	case "wanted", "missing":
		return "Not in the library yet"
	case "downloading":
		return "Downloading"
	case "importing":
		return "Importing"
	case "available", "cutoff-unmet":
		return "In the library"
	default:
		return ""
	}
}

func (s *Service) tvCalendar(ctx context.Context, from, to time.Time) ([]CalendarEntry, error) {
	entries, err := s.tv.Calendar(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]CalendarEntry, 0, len(entries))
	for _, entry := range entries {
		if !inRange(entry.AirDate, from, to) {
			continue
		}
		id := "tv-" + entry.ID
		if entry.IMDbID != "" {
			id = "tv-" + entry.IMDbID
		}
		title := strings.TrimSpace(entry.SeriesTitle)
		if title == "" {
			title = "TV episode"
		}
		items = append(items, CalendarEntry{
			ID: id, MediaType: MediaTV, Source: "tv", Title: title,
			Subtitle: fmt.Sprintf("S%02dE%02d %s", entry.Season, entry.Number, strings.TrimSpace(entry.Title)),
			Date:     entry.AirDate, Poster: entry.Poster, LibraryID: entry.SeriesID,
			Status: entry.Status, IMDbID: entry.SeriesIMDbID, Season: entry.Season, Episode: entry.Number,
		})
	}
	return items, nil
}

func (s *Service) musicCalendar(ctx context.Context, from, to time.Time) ([]CalendarEntry, error) {
	releases, err := s.music.Calendar(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: the music calendar could not be loaded", ErrUpstream)
	}
	items := make([]CalendarEntry, 0, len(releases))
	for _, release := range releases {
		date := strings.TrimSpace(release.ReleaseDate)
		if date == "" || !inRange(date, from, to) {
			continue
		}
		title := strings.TrimSpace(release.Title)
		if title == "" {
			title = "Release"
		}
		items = append(items, CalendarEntry{
			ID: "music-" + release.ID, MediaType: MediaMusic, Source: "music", Title: title,
			Subtitle: strings.TrimSpace(release.Type), Artist: strings.TrimSpace(release.Artist),
			Date: date, Year: release.Year, Poster: release.Poster, LibraryID: release.ID,
		})
	}
	return items, nil
}
