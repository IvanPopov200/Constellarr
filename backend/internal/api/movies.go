package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/quality"
)

const maxMovieBytes = 256 << 10

func registerMovies(mux *http.ServeMux, service *movies.Service) {
	mux.HandleFunc("GET /api/v1/movies/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		movie, err := service.Get(r.Context(), id)
		respond(w, http.StatusOK, movie, err)
	})
	mux.HandleFunc("GET /api/v1/movies", func(w http.ResponseWriter, r *http.Request) {
		list, err := service.List(r.Context())
		respond(w, http.StatusOK, list, err)
	})
	mux.HandleFunc("POST /api/v1/movies", func(w http.ResponseWriter, r *http.Request) {
		var input movies.AddInput
		if !decodeMovieBody(w, r, &input, true, "movie") {
			return
		}
		movie, err := service.Add(r.Context(), input)
		respond(w, http.StatusCreated, movie, err)
	})
	mux.HandleFunc("POST /api/v1/movies/bulk", func(w http.ResponseWriter, r *http.Request) {
		var input movies.BulkInput
		if !decodeMovieBody(w, r, &input, true, "bulk edit") {
			return
		}
		list, err := service.Bulk(r.Context(), input)
		respond(w, http.StatusOK, list, err)
	})
	mux.HandleFunc("POST /api/v1/movies/scan", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			RootID string `json:"rootId"`
		}
		if !decodeMovieBody(w, r, &input, true, "scan") {
			return
		}
		candidates, err := service.Scan(r.Context(), input.RootID)
		respond(w, http.StatusOK, candidates, err)
	})
	mux.HandleFunc("POST /api/v1/movies/import", func(w http.ResponseWriter, r *http.Request) {
		var input movies.ImportInput
		if !decodeMovieBody(w, r, &input, true, "import") {
			return
		}
		movie, err := service.Import(r.Context(), input)
		respond(w, http.StatusOK, movie, err)
	})
	mux.HandleFunc("POST /api/v1/movies/sync", func(w http.ResponseWriter, r *http.Request) {
		if !decodeMovieBody(w, r, &struct{}{}, false, "sync") {
			return
		}
		result, err := service.Sync(r.Context(), true)
		respond(w, http.StatusOK, result, err)
	})
	mux.HandleFunc("GET /api/v1/movies/discover", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" || utf8.RuneCountInString(query) > 256 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enter a movie title up to 256 characters"})
			return
		}
		page := 1
		if raw := r.URL.Query().Get("page"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enter a page between 1 and 100"})
				return
			}
			page = parsed
		}
		titles, err := service.Discover(r.Context(), query, page)
		respond(w, http.StatusOK, titles, err)
	})
	mux.HandleFunc("GET /api/v1/movies/history", func(w http.ResponseWriter, r *http.Request) {
		entries, err := service.Store.History(r.Context(), "")
		respond(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("GET /api/v1/movies/calendar", func(w http.ResponseWriter, r *http.Request) {
		calendar, err := service.Calendar(r.Context())
		respond(w, http.StatusOK, calendar, err)
	})
	mux.HandleFunc("GET /api/v1/movies/calendar.ics", func(w http.ResponseWriter, r *http.Request) {
		calendar, err := service.Calendar(r.Context())
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(movieCalendarICS(calendar))
	})
	mux.HandleFunc("PUT /api/v1/movies/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		var input movies.Movie
		if !decodeMovieBody(w, r, &input, true, "movie") {
			return
		}
		movie, err := service.Update(r.Context(), id, input)
		respond(w, http.StatusOK, movie, err)
	})
	mux.HandleFunc("DELETE /api/v1/movies/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		deleteFiles := false
		if raw := r.URL.Query().Get("deleteFiles"); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "deleteFiles must be true or false"})
				return
			}
			deleteFiles = parsed
		}
		if err := service.Remove(r.Context(), id, deleteFiles); err != nil {
			respond(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/movies/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		entries, err := service.Store.History(r.Context(), id)
		respond(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("POST /api/v1/movies/{id}/search", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		if !decodeMovieBody(w, r, &struct{}{}, false, "search") {
			return
		}
		releases, err := service.Search(r.Context(), id)
		respond(w, http.StatusOK, releases, err)
	})
	mux.HandleFunc("POST /api/v1/movies/{id}/grab", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		var input struct {
			ReleaseID string `json:"releaseId"`
			Override  bool   `json:"override"`
		}
		if !decodeMovieBody(w, r, &input, true, "grab") {
			return
		}
		job, err := service.Grab(r.Context(), id, input.ReleaseID, input.Override)
		respond(w, http.StatusCreated, job, err)
	})
	mux.HandleFunc("POST /api/v1/movies/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		if !decodeMovieBody(w, r, &struct{}{}, false, "refresh") {
			return
		}
		movie, err := service.Refresh(r.Context(), id)
		respond(w, http.StatusOK, movie, err)
	})
	mux.HandleFunc("POST /api/v1/movies/{id}/rename", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		var input struct {
			Preview bool `json:"preview"`
		}
		if !decodeMovieBody(w, r, &input, false, "rename") {
			return
		}
		result, err := service.Rename(r.Context(), id, input.Preview)
		respond(w, http.StatusOK, result, err)
	})
	mux.HandleFunc("GET /api/v1/movies/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "movie")
		if !ok {
			return
		}
		path := r.URL.Query().Get("path")
		if path == "" || len(path) > 4096 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a file path is required"})
			return
		}
		file, err := service.OpenFile(r.Context(), id, path)
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			respond(w, 0, nil, movies.ErrNotFound)
			return
		}
		name := filepath.Base(path)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
	mux.HandleFunc("GET /api/v1/movie-profiles", func(w http.ResponseWriter, r *http.Request) {
		profiles, err := service.Store.Profiles(r.Context())
		respond(w, http.StatusOK, profiles, err)
	})
	mux.HandleFunc("PUT /api/v1/movie-profiles", func(w http.ResponseWriter, r *http.Request) {
		var profile quality.Profile
		if !decodeMovieBody(w, r, &profile, true, "profile") {
			return
		}
		saved, err := service.Store.SaveProfile(r.Context(), profile)
		respond(w, http.StatusOK, saved, err)
	})
	mux.HandleFunc("DELETE /api/v1/movie-profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "profile")
		if !ok {
			return
		}
		if err := service.Store.DeleteProfile(r.Context(), id); err != nil {
			respond(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/movie-config", func(w http.ResponseWriter, r *http.Request) {
		config, err := service.ConfigView(r.Context())
		respond(w, http.StatusOK, config, err)
	})
	mux.HandleFunc("PUT /api/v1/movie-config", func(w http.ResponseWriter, r *http.Request) {
		var input movies.Config
		if !decodeMovieBody(w, r, &input, true, "config") {
			return
		}
		config, err := service.SetConfig(r.Context(), input)
		respond(w, http.StatusOK, config, err)
	})
	mux.HandleFunc("POST /api/v1/movie-config/test", func(w http.ResponseWriter, r *http.Request) {
		if !decodeMovieBody(w, r, &struct{}{}, false, "config test") {
			return
		}
		tests, err := service.TestConnections(r.Context())
		respond(w, http.StatusOK, tests, err)
	})
	mux.HandleFunc("GET /api/v1/movie-watchlists", func(w http.ResponseWriter, r *http.Request) {
		watchlists, err := service.Store.Watchlists(r.Context())
		respond(w, http.StatusOK, watchlists, err)
	})
	mux.HandleFunc("PUT /api/v1/movie-watchlists", func(w http.ResponseWriter, r *http.Request) {
		var watchlist movies.Watchlist
		if !decodeMovieBody(w, r, &watchlist, true, "watchlist") {
			return
		}
		saved, err := service.Store.SaveWatchlist(r.Context(), watchlist)
		respond(w, http.StatusOK, saved, err)
	})
	mux.HandleFunc("DELETE /api/v1/movie-watchlists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "watchlist")
		if !ok {
			return
		}
		if err := service.Store.DeleteWatchlist(r.Context(), id); err != nil {
			respond(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/v1/movie-watchlists/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "watchlist")
		if !ok {
			return
		}
		if !decodeMovieBody(w, r, &struct{}{}, false, "watchlist sync") {
			return
		}
		added, err := service.SyncWatchlist(r.Context(), id)
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Added int `json:"added"`
		}{added})
	})
}

func boundedPathID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	id := r.PathValue("id")
	if id == "" || len(id) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid " + name + " id"})
		return "", false
	}
	return id, true
}

// decodeMovieBody reads one bounded JSON document and rejects unknown fields and trailing data; required=false accepts an omitted body.
func decodeMovieBody(w http.ResponseWriter, r *http.Request, body any, required bool, name string) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMovieBytes))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(body)
	if errors.Is(err, io.EOF) && !required {
		return true
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid " + name + " request"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send one " + name + " request"})
		return false
	}
	return true
}

func movieCalendarICS(calendar []movies.Movie) []byte {
	now := time.Now().UTC()
	var out bytes.Buffer
	appendICalLine(&out, "BEGIN:VCALENDAR")
	appendICalLine(&out, "VERSION:2.0")
	appendICalLine(&out, "PRODID:-//Constellarr//Movies//EN")
	appendICalLine(&out, "CALSCALE:GREGORIAN")
	appendICalLine(&out, "METHOD:PUBLISH")
	appendICalLine(&out, "X-WR-CALNAME:Movies")
	for _, movie := range calendar {
		released, ok := calendarDate(movie.Metadata.Released)
		if !ok {
			continue
		}
		appendICalLine(&out, "BEGIN:VEVENT")
		appendICalLine(&out, "UID:"+escapeICal(calendarUID(movie)))
		appendICalLine(&out, "DTSTAMP:"+now.Format("20060102T150405Z"))
		appendICalLine(&out, "DTSTART:"+released.Format("20060102T150405Z"))
		appendICalLine(&out, "SUMMARY:"+escapeICal(movieCalendarLabel(movie)))
		if movie.Metadata.IMDbID != "" {
			appendICalLine(&out, "URL:https://www.imdb.com/title/"+escapeICal(movie.Metadata.IMDbID)+"/")
		}
		appendICalLine(&out, "END:VEVENT")
	}
	appendICalLine(&out, "END:VCALENDAR")
	return out.Bytes()
}

func calendarUID(movie movies.Movie) string {
	if movie.Metadata.IMDbID != "" {
		return movie.Metadata.IMDbID + "@constellarr"
	}
	return movie.ID + "@constellarr"
}

func movieCalendarLabel(movie movies.Movie) string {
	title := strings.TrimSpace(movie.Metadata.Title)
	if title == "" {
		title = "Movie"
	}
	if movie.Metadata.Year > 0 {
		return fmt.Sprintf("%s (%d)", title, movie.Metadata.Year)
	}
	return title
}

func calendarDate(released string) (time.Time, bool) {
	released = strings.TrimSpace(released)
	// Matches the stored metadata date layouts, treating partial dates as their first day.
	for _, layout := range []string{"2006-01-02", "02 Jan 2006", "Jan 2, 2006", "2006/01/02", "2006-01", "2006", time.RFC3339} {
		if parsed, err := time.Parse(layout, released); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func escapeICal(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	return strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`).Replace(value)
}

// appendICalLine writes one CRLF-terminated line folded to the 75-octet limit.
func appendICalLine(out *bytes.Buffer, line string) {
	limit := 75
	for {
		cut := len(line)
		if cut > limit {
			cut = limit
			for cut > 0 && !utf8.RuneStart(line[cut]) {
				cut--
			}
		}
		out.WriteString(line[:cut])
		line = line[cut:]
		if line == "" {
			break
		}
		out.WriteString("\r\n ")
		limit = 74
	}
	out.WriteString("\r\n")
}
