package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

type source struct {
	Name        string `json:"name"`
	Configured  bool   `json:"configured"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Connections int    `json:"connections,omitempty"`
}

func registerDownloads(mux *http.ServeMux, m *downloads.Manager) {
	registerSettings(mux, m)
	mux.HandleFunc("GET /api/v1/sources", func(w http.ResponseWriter, r *http.Request) {
		cfg := m.Config()
		writeJSON(w, http.StatusOK, struct {
			Indexer source `json:"indexer"`
			Usenet  source `json:"usenet"`
		}{
			Indexer: source{Name: "NZBGeek", Configured: cfg.APIKey != ""},
			Usenet:  source{Name: "Usenet", Configured: cfg.Usenet.Username != "" && cfg.Usenet.Password != "", Host: cfg.Usenet.Host, Port: cfg.Usenet.Port, Connections: cfg.Usenet.Connections},
		})
	})
	mux.HandleFunc("POST /api/v1/sources/test", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		type result struct {
			OK    bool   `json:"ok"`
			Error string `json:"error,omitempty"`
		}
		var indexer, usenet result
		var tests sync.WaitGroup
		for _, test := range []struct {
			call func(context.Context) error
			out  *result
		}{{m.TestIndexer, &indexer}, {m.TestUsenet, &usenet}} {
			tests.Go(func() {
				if err := test.call(ctx); err != nil {
					test.out.Error = err.Error()
				} else {
					test.out.OK = true
				}
			})
		}
		tests.Wait()
		writeJSON(w, http.StatusOK, struct {
			Indexer result `json:"indexer"`
			Usenet  result `json:"usenet"`
		}{indexer, usenet})
	})
	mux.HandleFunc("GET /api/v1/releases", func(w http.ResponseWriter, r *http.Request) {
		query := strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" || utf8.RuneCountInString(query) > 256 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enter a movie title up to 256 characters"})
			return
		}
		releases, err := m.Search(r.Context(), query)
		respond(w, http.StatusOK, releases, err)
	})
	mux.HandleFunc("GET /api/v1/downloads", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := m.List(r.Context())
		respond(w, http.StatusOK, jobs, err)
	})
	mux.HandleFunc("POST /api/v1/downloads", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ReleaseID string `json:"releaseId"`
			Title     string `json:"title"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid download request"})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send one download request"})
			return
		}
		job, err := m.Add(r.Context(), input.ReleaseID, input.Title)
		respond(w, http.StatusCreated, job, err)
	})
	mux.HandleFunc("POST /api/v1/downloads/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		job, err := m.Retry(r.Context(), r.PathValue("id"))
		respond(w, http.StatusOK, job, err)
	})
	mux.HandleFunc("GET /api/v1/downloads/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		file, err := m.OpenFile(r.Context(), r.PathValue("id"), name)
		if err != nil {
			respond(w, 0, nil, err)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			respond(w, 0, nil, downloads.ErrNotFound)
			return
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(name)}))
		w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		http.ServeContent(w, r, filepath.Base(name), info.ModTime(), file)
	})
}

func respond(w http.ResponseWriter, status int, body any, err error) {
	if err == nil {
		writeJSON(w, status, body)
		return
	}
	status = http.StatusBadGateway
	switch {
	case errors.Is(err, downloads.ErrNotFound), errors.Is(err, movies.ErrNotFound), errors.Is(err, tv.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, downloads.ErrConflict), errors.Is(err, movies.ErrConflict), errors.Is(err, tv.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, downloads.ErrNotConfigured):
		status = http.StatusServiceUnavailable
	case errors.Is(err, downloads.ErrInvalid), errors.Is(err, movies.ErrInvalid), errors.Is(err, tv.ErrInvalid):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
