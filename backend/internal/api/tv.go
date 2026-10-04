package api

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

func tvBody[T any](status int, required bool, action func(context.Context, string, T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if len(id) > 64 {
			respond(w, 0, nil, tv.ErrInvalid)
			return
		}
		var input T
		if !decodeMovieBody(w, r, &input, required, "TV") {
			return
		}
		result, err := action(r.Context(), id, input)
		respond(w, status, result, err)
	}
}

func registerTV(mux *http.ServeMux, service *tv.Service) {
	mux.HandleFunc("GET /api/v1/tv", func(w http.ResponseWriter, r *http.Request) {
		list, err := service.List(r.Context())
		respond(w, http.StatusOK, list, err)
	})
	mux.HandleFunc("GET /api/v1/tv/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "series")
		if !ok {
			return
		}
		series, err := service.Get(r.Context(), id)
		respond(w, http.StatusOK, series, err)
	})
	mux.HandleFunc("POST /api/v1/tv", tvBody(http.StatusCreated, true, func(ctx context.Context, _ string, input tv.AddInput) (any, error) {
		return service.Add(ctx, input)
	}))
	mux.HandleFunc("PUT /api/v1/tv/{id}", tvBody(http.StatusOK, true, func(ctx context.Context, id string, input tv.Series) (any, error) {
		return service.Update(ctx, id, input)
	}))
	mux.HandleFunc("DELETE /api/v1/tv/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "series")
		if !ok {
			return
		}
		deleteFiles := false
		if raw := r.URL.Query().Get("deleteFiles"); raw != "" {
			parsed, err := strconv.ParseBool(raw)
			if err != nil {
				respond(w, 0, nil, tv.ErrInvalid)
				return
			}
			deleteFiles = parsed
		}
		err := service.Remove(r.Context(), id, deleteFiles)
		respond(w, http.StatusOK, map[string]bool{"ok": true}, err)
	})
	mux.HandleFunc("POST /api/v1/tv/bulk", tvBody(http.StatusOK, true, func(ctx context.Context, _ string, input tv.BulkInput) (any, error) {
		return service.Bulk(ctx, input)
	}))
	mux.HandleFunc("GET /api/v1/tv/discover", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		page := 1
		var err error
		if raw := r.URL.Query().Get("page"); raw != "" {
			page, err = strconv.Atoi(raw)
		}
		if err != nil || page < 1 || page > 100 || query == "" || utf8.RuneCountInString(query) > 256 {
			respond(w, 0, nil, tv.ErrInvalid)
			return
		}
		titles, err := service.Discover(r.Context(), query, page)
		respond(w, http.StatusOK, titles, err)
	})
	mux.HandleFunc("POST /api/v1/tv/{id}/refresh", tvBody(http.StatusOK, false, func(ctx context.Context, id string, _ struct{}) (any, error) {
		return service.Refresh(ctx, id)
	}))
	mux.HandleFunc("POST /api/v1/tv/{id}/episodes", tvBody(http.StatusCreated, true, func(ctx context.Context, id string, episode tv.Episode) (any, error) {
		return service.AddEpisode(ctx, id, episode)
	}))
	mux.HandleFunc("POST /api/v1/tv/{id}/monitor", tvBody(http.StatusOK, true, func(ctx context.Context, id string, input tv.MonitorInput) (any, error) {
		return service.Monitor(ctx, id, input)
	}))
	mux.HandleFunc("POST /api/v1/tv/{id}/search", tvBody(http.StatusOK, true, func(ctx context.Context, id string, target tv.Target) (any, error) {
		return service.Search(ctx, id, target)
	}))
	mux.HandleFunc("POST /api/v1/tv/{id}/grab", tvBody(http.StatusAccepted, true, func(ctx context.Context, id string, input tv.GrabInput) (any, error) {
		return service.Grab(ctx, id, input)
	}))
	mux.HandleFunc("POST /api/v1/tv/{id}/rename", tvBody(http.StatusOK, true, func(ctx context.Context, id string, input struct {
		Preview bool `json:"preview"`
	}) (any, error) {
		return service.Rename(ctx, id, input.Preview)
	}))
	mux.HandleFunc("GET /api/v1/tv/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "series")
		if !ok {
			return
		}
		if _, err := service.Store.Get(r.Context(), id); err != nil {
			respond(w, 0, nil, err)
			return
		}
		entries, err := service.Store.History(r.Context(), id)
		respond(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("GET /api/v1/tv/history", func(w http.ResponseWriter, r *http.Request) {
		entries, err := service.Store.History(r.Context(), "")
		respond(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("GET /api/v1/tv/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedPathID(w, r, "series")
		if !ok {
			return
		}
		file, err := service.OpenFile(r.Context(), id, r.URL.Query().Get("path"))
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			respond(w, 0, nil, tv.ErrNotFound)
			return
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(info.Name())}))
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(info.Name())))
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		http.ServeContent(w, r, info.Name(), info.ModTime(), file)
	})
	mux.HandleFunc("POST /api/v1/tv/scan", tvBody(http.StatusOK, true, func(ctx context.Context, _ string, input struct {
		RootID string `json:"rootId"`
	}) (any, error) {
		return service.Scan(ctx, input.RootID)
	}))
	mux.HandleFunc("POST /api/v1/tv/import", tvBody(http.StatusOK, true, func(ctx context.Context, _ string, input tv.ImportInput) (any, error) {
		return service.Import(ctx, input)
	}))
	mux.HandleFunc("POST /api/v1/tv/sync", tvBody(http.StatusOK, false, func(ctx context.Context, _ string, _ struct{}) (any, error) {
		return service.Sync(ctx, true)
	}))
	mux.HandleFunc("GET /api/v1/tv-config", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := service.Config(r.Context())
		respond(w, http.StatusOK, cfg, err)
	})
	mux.HandleFunc("PUT /api/v1/tv-config", tvBody(http.StatusOK, true, func(ctx context.Context, _ string, cfg tv.Config) (any, error) {
		return service.SetConfig(ctx, cfg)
	}))
	mux.HandleFunc("GET /api/v1/tv/calendar", func(w http.ResponseWriter, r *http.Request) {
		entries, err := service.Calendar(r.Context())
		respond(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("GET /api/v1/tv/calendar.ics", func(w http.ResponseWriter, r *http.Request) {
		entries, err := service.Calendar(r.Context())
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="tv.ics"`)
		_, _ = w.Write(tvCalendarICS(entries))
	})
}

func tvCalendarICS(entries []tv.CalendarEntry) []byte {
	var out bytes.Buffer
	for _, line := range []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Constellarr//TV//EN", "CALSCALE:GREGORIAN", "METHOD:PUBLISH", "X-WR-CALNAME:TV Shows"} {
		appendICalLine(&out, line)
	}
	for _, entry := range entries {
		date, err := time.Parse("2006-01-02", entry.AirDate)
		if err != nil {
			continue
		}
		for _, line := range []string{
			"BEGIN:VEVENT", "UID:" + escapeICal(entry.ID+"@constellarr"),
			"DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z"),
			"DTSTART;VALUE=DATE:" + date.Format("20060102"),
			"DTEND;VALUE=DATE:" + date.AddDate(0, 0, 1).Format("20060102"),
			"SUMMARY:" + escapeICal(fmt.Sprintf("%s — S%02dE%02d %s", entry.SeriesTitle, entry.Season, entry.Number, entry.Title)),
			"END:VEVENT",
		} {
			appendICalLine(&out, line)
		}
	}
	appendICalLine(&out, "END:VCALENDAR")
	return out.Bytes()
}
