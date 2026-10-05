package music

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

const maxMusicBodyBytes = 1 << 20

// Register mounts the music API below /api/v1/music on the shared server mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/music/config", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := s.ConfigView(r.Context())
		respond(w, http.StatusOK, cfg, err)
	})
	mux.HandleFunc("PUT /api/v1/music/config", bodyHandler(http.StatusOK, true, func(ctx context.Context, _ string, input Config) (any, error) {
		return s.SetConfig(ctx, input)
	}))
	mux.HandleFunc("POST /api/v1/music/config/test", bodyHandler(http.StatusOK, false, func(ctx context.Context, _ string, _ struct{}) (any, error) {
		return s.TestConnections(ctx)
	}))

	mux.HandleFunc("GET /api/v1/music/artists", func(w http.ResponseWriter, r *http.Request) {
		artists, err := s.Artists(r.Context())
		respond(w, http.StatusOK, artists, err)
	})
	mux.HandleFunc("POST /api/v1/music/artists", bodyHandler(http.StatusCreated, true, func(ctx context.Context, _ string, input AddArtistInput) (any, error) {
		return s.AddArtist(ctx, input)
	}))
	mux.HandleFunc("GET /api/v1/music/artists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedID(w, r)
		if !ok {
			return
		}
		artist, err := s.Artist(r.Context(), id)
		respond(w, http.StatusOK, artist, err)
	})
	mux.HandleFunc("PUT /api/v1/music/artists/{id}", bodyHandler(http.StatusOK, true, func(ctx context.Context, id string, input Artist) (any, error) {
		return s.UpdateArtist(ctx, id, input)
	}))
	mux.HandleFunc("DELETE /api/v1/music/artists/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedID(w, r)
		if !ok {
			return
		}
		deleteFiles, ok := boolQuery(w, r, "deleteFiles")
		if !ok {
			return
		}
		respond(w, http.StatusOK, map[string]bool{"ok": true}, s.RemoveArtist(r.Context(), id, deleteFiles))
	})
	mux.HandleFunc("POST /api/v1/music/artists/{id}/monitor", bodyHandler(http.StatusOK, true, func(ctx context.Context, id string, input MonitorInput) (any, error) {
		return s.MonitorArtist(ctx, id, input)
	}))
	mux.HandleFunc("POST /api/v1/music/artists/{id}/refresh", bodyHandler(http.StatusOK, false, func(ctx context.Context, id string, _ struct{}) (any, error) {
		return s.RefreshArtist(ctx, id)
	}))
	mux.HandleFunc("GET /api/v1/music/artists/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedID(w, r)
		if !ok {
			return
		}
		entries, err := s.ArtistHistory(r.Context(), id)
		respond(w, http.StatusOK, entries, err)
	})

	mux.HandleFunc("GET /api/v1/music/albums", func(w http.ResponseWriter, r *http.Request) {
		albums, err := s.Albums(r.Context())
		respond(w, http.StatusOK, albums, err)
	})
	mux.HandleFunc("POST /api/v1/music/albums", bodyHandler(http.StatusCreated, true, func(ctx context.Context, _ string, input AddAlbumInput) (any, error) {
		return s.AddAlbum(ctx, input)
	}))
	mux.HandleFunc("GET /api/v1/music/albums/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedID(w, r)
		if !ok {
			return
		}
		album, err := s.Album(r.Context(), id)
		respond(w, http.StatusOK, album, err)
	})
	mux.HandleFunc("PUT /api/v1/music/albums/{id}", bodyHandler(http.StatusOK, true, func(ctx context.Context, id string, input Album) (any, error) {
		return s.UpdateAlbum(ctx, id, input)
	}))
	mux.HandleFunc("DELETE /api/v1/music/albums/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedID(w, r)
		if !ok {
			return
		}
		deleteFiles, ok := boolQuery(w, r, "deleteFiles")
		if !ok {
			return
		}
		respond(w, http.StatusOK, map[string]bool{"ok": true}, s.RemoveAlbum(r.Context(), id, deleteFiles))
	})
	mux.HandleFunc("POST /api/v1/music/albums/{id}/monitor", bodyHandler(http.StatusOK, true, func(ctx context.Context, id string, input AlbumMonitorInput) (any, error) {
		return s.MonitorAlbum(ctx, id, input)
	}))
	mux.HandleFunc("POST /api/v1/music/albums/{id}/search", bodyHandler(http.StatusOK, false, func(ctx context.Context, id string, _ struct{}) (any, error) {
		return s.Search(ctx, id)
	}))
	mux.HandleFunc("POST /api/v1/music/albums/{id}/grab", bodyHandler(http.StatusAccepted, true, func(ctx context.Context, id string, input struct {
		ReleaseID string `json:"releaseId"`
		Override  bool   `json:"override"`
	}) (any, error) {
		return s.Grab(ctx, id, input.ReleaseID, input.Override)
	}))
	mux.HandleFunc("POST /api/v1/music/albums/{id}/refresh", bodyHandler(http.StatusOK, false, func(ctx context.Context, id string, _ struct{}) (any, error) {
		return s.RefreshAlbum(ctx, id)
	}))
	mux.HandleFunc("POST /api/v1/music/albums/{id}/rename", bodyHandler(http.StatusOK, true, func(ctx context.Context, id string, input struct {
		Preview bool `json:"preview"`
	}) (any, error) {
		return s.Rename(ctx, id, input.Preview)
	}))
	mux.HandleFunc("GET /api/v1/music/albums/{id}/history", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedID(w, r)
		if !ok {
			return
		}
		entries, err := s.History(r.Context(), id)
		respond(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("GET /api/v1/music/albums/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		id, ok := boundedID(w, r)
		if !ok {
			return
		}
		data, contentType, err := s.Cover(r.Context(), id)
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	})

	mux.HandleFunc("GET /api/v1/music/history", func(w http.ResponseWriter, r *http.Request) {
		entries, err := s.History(r.Context(), "")
		respond(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("GET /api/v1/music/discover", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		page, ok := intQuery(w, r, "page", 1, maxDiscoverPage)
		if !ok {
			return
		}
		if query == "" || utf8.RuneCountInString(query) > maxQueryRunes {
			respond(w, 0, nil, ErrInvalid)
			return
		}
		results, err := s.Discover(r.Context(), query, page)
		respond(w, http.StatusOK, results, err)
	})
	mux.HandleFunc("GET /api/v1/music/search", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		albumID := strings.TrimSpace(r.URL.Query().Get("albumId"))
		if utf8.RuneCountInString(query) > maxQueryRunes || len(albumID) > maxIDBytes {
			respond(w, 0, nil, ErrInvalid)
			return
		}
		if query == "" && albumID == "" {
			respond(w, 0, nil, ErrInvalid)
			return
		}
		releases, err := s.SearchReleases(r.Context(), query, albumID)
		respond(w, http.StatusOK, releases, err)
	})
	mux.HandleFunc("GET /api/v1/music/wanted", func(w http.ResponseWriter, r *http.Request) {
		albums, err := s.Wanted(r.Context())
		respond(w, http.StatusOK, albums, err)
	})
	mux.HandleFunc("GET /api/v1/music/calendar", func(w http.ResponseWriter, r *http.Request) {
		entries, err := s.Calendar(r.Context())
		respond(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("POST /api/v1/music/scan", bodyHandler(http.StatusOK, true, func(ctx context.Context, _ string, input struct {
		RootID string `json:"rootId"`
	}) (any, error) {
		return s.Scan(ctx, input.RootID)
	}))
	mux.HandleFunc("POST /api/v1/music/import", bodyHandler(http.StatusOK, true, func(ctx context.Context, _ string, input ImportInput) (any, error) {
		return s.Import(ctx, input)
	}))
	mux.HandleFunc("POST /api/v1/music/sync", bodyHandler(http.StatusOK, false, func(ctx context.Context, _ string, input struct {
		Force *bool `json:"force"`
	}) (any, error) {
		force := false
		if input.Force != nil {
			force = *input.Force
		}
		return s.Sync(ctx, force)
	}))
}

// bodyHandler decodes one bounded JSON document before calling the service.
func bodyHandler[T any](status int, required bool, action func(context.Context, string, T) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if len(id) > maxIDBytes {
			respond(w, 0, nil, ErrInvalid)
			return
		}
		var input T
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMusicBodyBytes))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&input)
		if errors.Is(err, io.EOF) && !required {
			result, err := action(r.Context(), id, input)
			respond(w, status, result, err)
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid music request"})
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send one music request"})
			return
		}
		result, err := action(r.Context(), id, input)
		respond(w, status, result, err)
	}
}

func boundedID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if id == "" || len(id) > maxIDBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid music id"})
		return "", false
	}
	return id, true
}

func boolQuery(w http.ResponseWriter, r *http.Request, name string) (bool, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return false, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid " + name + " value"})
		return false, false
	}
	return value, true
}

func intQuery(w http.ResponseWriter, r *http.Request, name string, min, max int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return min, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid " + name + " value"})
		return 0, false
	}
	return value, true
}

func respond(w http.ResponseWriter, status int, body any, err error) {
	if err == nil {
		writeJSON(w, status, body)
		return
	}
	code := http.StatusBadGateway
	switch {
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrConflict), errors.Is(err, errAlbumBusy), errors.Is(err, downloads.ErrConflict):
		code = http.StatusConflict
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrUnsafe), errors.Is(err, ErrTemplate), errors.Is(err, downloads.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, ErrNotConfigured), errors.Is(err, downloads.ErrNotConfigured):
		code = http.StatusServiceUnavailable
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = http.StatusGatewayTimeout
	}
	writeJSON(w, code, map[string]string{"error": truncate(err.Error(), maxTextRunes)})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
