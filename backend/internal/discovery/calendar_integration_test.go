package discovery_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery"
	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

// calendarEnvironment seeds one movie, one episode, and optionally one music release around today.
func calendarEnvironment(t *testing.T, withMusic bool) (*environment, time.Time) {
	t.Helper()
	options := envOptions{withActor: true}
	if withMusic {
		options.music = newStubMusic()
	}
	env := newEnvironment(t, options)
	ctx := context.Background()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	movieDay := today.AddDate(0, 0, 5)
	episodeDay := today.AddDate(0, 0, 3)
	if _, err := env.movies.Add(ctx, movies.AddInput{
		Metadata:  metadata.Title{Title: "Synthetic Calendar Movie", Year: 2026, Type: "movie", Released: movieDay.Format("2006-01-02")},
		Monitored: true,
	}); err != nil {
		t.Fatalf("add movie: %v", err)
	}
	series, err := env.tv.Store.Create(ctx, tv.Series{
		Metadata:  metadata.Title{IMDbID: "tt2234567", Title: "Synthetic Calendar Series", Year: 2026, Type: "series"},
		Monitored: true, MonitorMode: "all",
	})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	if _, err := env.tv.Store.UpsertEpisode(ctx, tv.Episode{
		SeriesID: series.ID, IMDbID: "tt3234567", Title: "Synthetic Pilot",
		Season: 1, Number: 1, AirDate: episodeDay.Format("2006-01-02"), Monitored: true,
	}); err != nil {
		t.Fatalf("add episode: %v", err)
	}
	if withMusic {
		env.music.releases = []discovery.MusicRelease{
			{ID: "music-in-range", Title: "Synthetic Album", Artist: "Synthetic Artist", ReleaseDate: today.AddDate(0, 0, 10).Format("2006-01-02"), Year: 2026},
			{ID: "music-far", Title: "Far Future Album", Artist: "Synthetic Artist", ReleaseDate: today.AddDate(0, 0, 400).Format("2006-01-02"), Year: 2027},
		}
	}
	return env, today
}

func TestCalendarCombinesSourcesAndFilters(t *testing.T) {
	env, today := calendarEnvironment(t, true)

	status, raw := env.call(t, http.MethodGet, "/api/v1/calendar", "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("GET calendar: status %d, body %s", status, raw)
	}
	view := decode[discovery.CalendarView](t, raw)
	if view.From != today.Format("2006-01-02") || view.To != today.AddDate(0, 0, 180).Format("2006-01-02") {
		t.Fatalf("calendar range = %s..%s", view.From, view.To)
	}
	if len(view.Entries) != 3 {
		t.Fatalf("default calendar entries = %+v; want the movie, episode, and near music release", view.Entries)
	}
	for i := 1; i < len(view.Entries); i++ {
		if view.Entries[i-1].Date > view.Entries[i].Date {
			t.Fatalf("calendar is not sorted by date: %+v", view.Entries)
		}
	}
	types := map[string]int{}
	for _, entry := range view.Entries {
		types[entry.MediaType]++
		if entry.ID == "" || entry.Title == "" {
			t.Fatalf("calendar entry is incomplete: %+v", entry)
		}
	}
	if types[discovery.MediaMovie] != 1 || types[discovery.MediaTV] != 1 || types[discovery.MediaMusic] != 1 {
		t.Fatalf("calendar media types = %v", types)
	}
	sources := map[string]bool{}
	for _, source := range view.Sources {
		sources[source.ID] = source.Available
	}
	if !sources["movies"] || !sources["tv"] || !sources["music"] {
		t.Fatalf("calendar sources = %+v", view.Sources)
	}

	for _, probe := range []struct {
		query string
		want  string
	}{
		{"types=movie", discovery.MediaMovie},
		{"types=tv", discovery.MediaTV},
		{"sources=music", discovery.MediaMusic},
	} {
		status, raw := env.call(t, http.MethodGet, "/api/v1/calendar?"+probe.query, "", callOptions{user: testUserA})
		if status != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", probe.query, status, raw)
		}
		filtered := decode[discovery.CalendarView](t, raw)
		if len(filtered.Entries) != 1 || filtered.Entries[0].MediaType != probe.want {
			t.Fatalf("%s filtered to %+v", probe.query, filtered.Entries)
		}
	}
	status, raw = env.call(t, http.MethodGet, "/api/v1/calendar?types=movie&sources=tv", "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("combined filters: status %d, body %s", status, raw)
	}
	if filtered := decode[discovery.CalendarView](t, raw); len(filtered.Entries) != 0 {
		t.Fatalf("combined filters returned %+v", filtered.Entries)
	}

	// Date bounds exclude the far release and keep the requested window.
	from := today.AddDate(0, 0, 200).Format("2006-01-02")
	to := today.AddDate(0, 0, 500).Format("2006-01-02")
	status, raw = env.call(t, http.MethodGet, "/api/v1/calendar?from="+from+"&to="+to, "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("bounded calendar: status %d, body %s", status, raw)
	}
	bounded := decode[discovery.CalendarView](t, raw)
	if len(bounded.Entries) != 1 || bounded.Entries[0].LibraryID != "music-far" {
		t.Fatalf("bounded calendar = %+v", bounded.Entries)
	}
}

func TestCalendarBoundsAndValidation(t *testing.T) {
	env, today := calendarEnvironment(t, true)
	cases := []struct {
		name  string
		query string
	}{
		{"invalid from", "from=last-tuesday"},
		{"invalid to", "to=2026-13-45"},
		{"end before start", "from=" + today.Format("2006-01-02") + "&to=" + today.AddDate(0, 0, -5).Format("2006-01-02")},
		{"range too large", "from=" + today.Format("2006-01-02") + "&to=" + today.AddDate(0, 0, 900).Format("2006-01-02")},
		{"unknown type", "types=audiobook"},
		{"unknown source", "sources=plex"},
	}
	for _, tc := range cases {
		status, raw := env.call(t, http.MethodGet, "/api/v1/calendar?"+tc.query, "", callOptions{user: testUserA})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s; want 400", tc.name, status, raw)
		}
	}
	// The maximum accepted span still works.
	from := today.Format("2006-01-02")
	to := today.AddDate(0, 0, 730).Format("2006-01-02")
	if status, raw := env.call(t, http.MethodGet, "/api/v1/calendar?from="+from+"&to="+to, "", callOptions{user: testUserA}); status != http.StatusOK {
		t.Fatalf("730-day range: status %d, body %s", status, raw)
	}
}

func TestCalendarWithoutMusicDoesNotPretendToWork(t *testing.T) {
	env, _ := calendarEnvironment(t, false)
	status, raw := env.call(t, http.MethodGet, "/api/v1/calendar", "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("GET calendar: status %d, body %s", status, raw)
	}
	view := decode[discovery.CalendarView](t, raw)
	for _, source := range view.Sources {
		if source.ID == "music" && source.Available {
			t.Fatal("music calendar source is reported as available without a music module")
		}
	}
	for _, entry := range view.Entries {
		if entry.MediaType == discovery.MediaMusic {
			t.Fatalf("music entry without a music module: %+v", entry)
		}
	}
}

func TestCalendarICSStableUIDsAndEscaping(t *testing.T) {
	env, today := calendarEnvironment(t, true)
	path := "/api/v1/calendar.ics?from=" + today.Format("2006-01-02") + "&to=" + today.AddDate(0, 0, 200).Format("2006-01-02")
	status, raw := env.call(t, http.MethodGet, path, "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("GET calendar.ics: status %d, body %s", status, raw)
	}
	body := string(raw)
	if !strings.HasPrefix(body, "BEGIN:VCALENDAR\r\n") || !strings.HasSuffix(body, "END:VCALENDAR\r\n") {
		t.Fatalf("calendar is not a complete iCalendar document: %q", body)
	}
	if strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n") {
		t.Fatal("calendar contains line feeds without carriage returns")
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Fatalf("calendar line exceeds 75 octets: %q", line)
		}
	}
	uids := icsUIDs(body)
	if len(uids) != 3 {
		t.Fatalf("calendar UIDs = %v; want one per entry", uids)
	}
	for _, uid := range uids {
		if !strings.HasPrefix(uid, "discovery-") || !strings.HasSuffix(uid, "@constellarr") {
			t.Fatalf("unstable calendar UID %q", uid)
		}
	}
	status, again := env.call(t, http.MethodGet, path, "", callOptions{user: testUserA})
	if status != http.StatusOK {
		t.Fatalf("second calendar.ics: status %d", status)
	}
	if got := icsUIDs(string(again)); strings.Join(got, "|") != strings.Join(uids, "|") {
		t.Fatalf("calendar UIDs changed between requests: %v vs %v", uids, got)
	}
	if !strings.Contains(body, "DTSTART;VALUE=DATE:") || !strings.Contains(body, "CATEGORIES:") {
		t.Fatalf("calendar lacks date-only events:\n%s", body)
	}
	if status, raw := env.call(t, http.MethodGet, "/api/v1/calendar.ics?types=video", "", callOptions{user: testUserA}); status != http.StatusBadRequest {
		t.Fatalf("invalid ICS filter: status %d, body %s; want 400", status, raw)
	}
}

// TestCalendarICSNeverInjectsHeaderLines covers titles that try to break out of their property.
func TestCalendarICSNeverInjectsHeaderLines(t *testing.T) {
	injection := "Evil\r\nX-EVIL: injected\r\nSUMMARY:spoofed, with; punctuation"
	view := discovery.CalendarView{
		From: "2026-01-01", To: "2026-01-31",
		Entries: []discovery.CalendarEntry{{
			ID: "movie-tt0133093", MediaType: discovery.MediaMovie, Source: "movies",
			Title: injection, Subtitle: injection, Date: "2026-01-15",
		}},
	}
	body := string(discovery.CalendarICS(view, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)))
	if strings.Contains(body, "\r\nX-EVIL") || strings.Contains(body, "\r\nSUMMARY:spoofed") {
		t.Fatalf("calendar allowed header injection:\n%s", body)
	}
	if strings.Count(body, "\r\nSUMMARY:") != 1 || strings.Count(body, "\r\nUID:") != 1 {
		t.Fatalf("calendar has unexpected properties:\n%s", body)
	}
	for _, line := range strings.Split(body, "\r\n") {
		if strings.HasPrefix(line, "X-EVIL") {
			t.Fatalf("calendar emitted an injected property line: %q", line)
		}
	}
	unfolded := strings.ReplaceAll(body, "\r\n ", "")
	if !strings.Contains(unfolded, `\,`) || !strings.Contains(unfolded, `\;`) || !strings.Contains(unfolded, `\n`) {
		t.Fatalf("calendar did not escape text:\n%s", body)
	}
	if _, err := time.Parse("20060102", "20260115"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "DTEND;VALUE=DATE:20260116\r\n") {
		t.Fatalf("calendar end date is not the day after:\n%s", body)
	}
}

func icsUIDs(body string) []string {
	uids := []string{}
	for _, line := range strings.Split(body, "\r\n") {
		if strings.HasPrefix(line, "UID:") {
			uids = append(uids, strings.TrimPrefix(line, "UID:"))
		}
	}
	return uids
}

func TestCalendarHTTPRouteRequiresIdentity(t *testing.T) {
	env := newEnvironment(t, envOptions{})
	status, raw := env.call(t, http.MethodGet, "/api/v1/calendar", "", callOptions{})
	if status != http.StatusUnauthorized {
		t.Fatalf("calendar without identity: status %d, body %s; want 401", status, raw)
	}
	if !strings.Contains(string(raw), `"error"`) {
		t.Fatalf("error body = %s", raw)
	}
	query := url.Values{"types": {"movie"}}
	if status, _ := env.call(t, http.MethodGet, "/api/v1/calendar?"+query.Encode(), "", callOptions{}); status != http.StatusUnauthorized {
		t.Fatalf("filtered calendar without identity: status %d", status)
	}
}
