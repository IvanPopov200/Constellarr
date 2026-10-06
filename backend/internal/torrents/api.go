package torrents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxAddBodyBytes    = 16 << 20
	maxSmallBodyBytes  = 16 << 10
	searchHTTPTimeout  = 25 * time.Second
	requestHTTPTimeout = 10 * time.Second
)

// Register adds the torrent API under /api/v1; the parent server applies origin and host checks.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/torrents", s.handleList)
	mux.HandleFunc("POST /api/v1/torrents", s.handleAdd)
	mux.HandleFunc("GET /api/v1/torrents/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/torrents/search", s.handleSearch)
	mux.HandleFunc("GET /api/v1/torrents/settings", s.handleSettings)
	mux.HandleFunc("PUT /api/v1/torrents/settings", s.handleUpdateSettings)
	mux.HandleFunc("GET /api/v1/torrent-sources", s.handleListSources)
	mux.HandleFunc("POST /api/v1/torrent-sources", s.handleCreateSource)
	mux.HandleFunc("PUT /api/v1/torrent-sources/{id}", s.handleUpdateSource)
	mux.HandleFunc("DELETE /api/v1/torrent-sources/{id}", s.handleDeleteSource)
	mux.HandleFunc("POST /api/v1/torrent-sources/{id}/test", s.handleTestSource)
	mux.HandleFunc("GET /api/v1/torrents/{id}", s.handleDetail)
	mux.HandleFunc("DELETE /api/v1/torrents/{id}", s.handleDelete)
	mux.HandleFunc("POST /api/v1/torrents/{id}/pause", s.handlePause)
	mux.HandleFunc("POST /api/v1/torrents/{id}/resume", s.handleResume)
	mux.HandleFunc("POST /api/v1/torrents/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /api/v1/torrents/{id}/recheck", s.handleRecheck)
	mux.HandleFunc("PUT /api/v1/torrents/{id}/limits", s.handleLimits)
	mux.HandleFunc("GET /api/v1/torrents/{id}/file", s.handleFile)
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.List(r.Context())
	respond(w, http.StatusOK, struct {
		Jobs []Job `json:"jobs"`
	}{Jobs: jobs}, err)
}

func (s *Service) handleAdd(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Magnet    string `json:"magnet"`
		Torrent   string `json:"torrent"`
		Filename  string `json:"filename"`
		SourceID  string `json:"sourceId"`
		ResultID  string `json:"resultId"`
		Source    string `json:"source"`
		ReleaseID string `json:"releaseId"`
		Title     string `json:"title"`
	}
	if !decodeBody(w, r, maxAddBodyBytes, &input) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestHTTPTimeout)
	defer cancel()
	var (
		job Job
		err error
	)
	switch {
	case strings.TrimSpace(input.Magnet) != "":
		job, err = s.Add(ctx, AddInput{Magnet: input.Magnet, Source: sourceMagnet, Title: input.Title})
	case strings.TrimSpace(input.Torrent) != "":
		data, decodeErr := decodeBase64Torrent(input.Torrent)
		if decodeErr != nil {
			respond(w, 0, nil, decodeErr)
			return
		}
		job, err = s.Add(ctx, AddInput{Torrent: data, Filename: input.Filename, Source: sourceFile,
			ReleaseID: input.ReleaseID, Title: input.Title})
	case strings.TrimSpace(input.SourceID) != "" && strings.TrimSpace(input.ResultID) != "":
		ctx, cancel := context.WithTimeout(r.Context(), 2*requestHTTPTimeout)
		defer cancel()
		job, err = s.AddFromResult(ctx, input.SourceID, input.ResultID, input.Title)
	default:
		writeError(w, http.StatusBadRequest, "send a magnet link, a torrent file or a search result")
		return
	}
	respond(w, http.StatusCreated, job, err)
}

func (s *Service) handleDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.Get(r.Context(), r.PathValue("id"))
	respond(w, http.StatusOK, detail, err)
}

func (s *Service) handlePause(w http.ResponseWriter, r *http.Request) {
	job, err := s.Pause(r.Context(), r.PathValue("id"))
	respond(w, http.StatusOK, job, err)
}

func (s *Service) handleResume(w http.ResponseWriter, r *http.Request) {
	job, err := s.Resume(r.Context(), r.PathValue("id"))
	respond(w, http.StatusOK, job, err)
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	job, err := s.Cancel(r.Context(), r.PathValue("id"))
	respond(w, http.StatusOK, job, err)
}

func (s *Service) handleRecheck(w http.ResponseWriter, r *http.Request) {
	job, err := s.Recheck(r.Context(), r.PathValue("id"))
	respond(w, http.StatusOK, job, err)
}

func (s *Service) handleLimits(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SeedRatioLimit       float64 `json:"seedRatioLimit"`
		SeedTimeLimitMinutes int     `json:"seedTimeLimitMinutes"`
	}
	if !decodeBody(w, r, maxSmallBodyBytes, &input) {
		return
	}
	job, err := s.UpdateLimits(r.Context(), r.PathValue("id"), input.SeedRatioLimit, input.SeedTimeLimitMinutes)
	respond(w, http.StatusOK, job, err)
}

func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	removeFiles := r.URL.Query().Get("files") == "true"
	id := r.PathValue("id")
	removed, err := s.Delete(r.Context(), id, removeFiles)
	respond(w, http.StatusOK, struct {
		ID           string `json:"id"`
		FilesRemoved bool   `json:"filesRemoved"`
	}{ID: id, FilesRemoved: removed}, err)
}

func (s *Service) handleFile(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	file, err := s.OpenFile(r.Context(), r.PathValue("id"), name)
	if err != nil {
		respond(w, 0, nil, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		respond(w, 0, nil, ErrNotFound)
		return
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(name)}))
	w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(name)))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, filepath.Base(name), info.ModTime(), file)
}

func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), searchHTTPTimeout)
	defer cancel()
	response, err := s.Search(ctx, r.URL.Query().Get("q"), r.URL.Query().Get("source"))
	respond(w, http.StatusOK, response, err)
}

func (s *Service) handleSettings(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, s.Settings(), nil)
}

func (s *Service) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ListenPort           int     `json:"listenPort"`
		DHTEnabled           bool    `json:"dhtEnabled"`
		PEXEnabled           bool    `json:"pexEnabled"`
		MaxActiveJobs        int     `json:"maxActiveJobs"`
		DownloadLimitKBps    int     `json:"downloadLimitKBps"`
		UploadLimitKBps      int     `json:"uploadLimitKBps"`
		SeedRatioLimit       float64 `json:"seedRatioLimit"`
		SeedTimeLimitMinutes int     `json:"seedTimeLimitMinutes"`
	}
	if !decodeBody(w, r, maxSmallBodyBytes, &input) {
		return
	}
	settings, err := s.UpdateSettings(r.Context(), SettingsUpdate{
		ListenPort: input.ListenPort, DHTEnabled: input.DHTEnabled, PEXEnabled: input.PEXEnabled,
		MaxActiveJobs: input.MaxActiveJobs, DownloadLimitKBps: input.DownloadLimitKBps,
		UploadLimitKBps: input.UploadLimitKBps, SeedRatioLimit: input.SeedRatioLimit,
		SeedTimeLimitMinutes: input.SeedTimeLimitMinutes,
	})
	respond(w, http.StatusOK, settings, err)
}

func (s *Service) handleListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.ListSources(r.Context())
	respond(w, http.StatusOK, struct {
		Sources []Source `json:"sources"`
	}{Sources: sources}, err)
}

type sourceBody struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	APIKey     string `json:"apiKey"`
	Categories []int  `json:"categories"`
	Enabled    bool   `json:"enabled"`
}

func (s *Service) handleCreateSource(w http.ResponseWriter, r *http.Request) {
	var input sourceBody
	if !decodeBody(w, r, maxSmallBodyBytes, &input) {
		return
	}
	source, err := s.CreateSource(r.Context(), input.toInput())
	respond(w, http.StatusCreated, source, err)
}

func (s *Service) handleUpdateSource(w http.ResponseWriter, r *http.Request) {
	var input sourceBody
	if !decodeBody(w, r, maxSmallBodyBytes, &input) {
		return
	}
	source, err := s.UpdateSource(r.Context(), r.PathValue("id"), input.toInput())
	respond(w, http.StatusOK, source, err)
}

func (s *Service) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	respond(w, http.StatusOK, struct {
		ID string `json:"id"`
	}{ID: id}, s.DeleteSource(r.Context(), id))
}

func (s *Service) handleTestSource(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), searchHTTPTimeout)
	defer cancel()
	result, err := s.TestSource(ctx, r.PathValue("id"))
	respond(w, http.StatusOK, result, err)
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, s.Health(r.Context()), nil)
}

func (body sourceBody) toInput() SourceInput {
	return SourceInput{Name: body.Name, URL: body.URL, APIKey: body.APIKey, Categories: body.Categories, Enabled: body.Enabled}
}

func decodeBody(w http.ResponseWriter, r *http.Request, limit int64, dest any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dest); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "send one JSON request")
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, body any, err error) {
	if err == nil {
		writeJSON(w, status, body)
		return
	}
	switch {
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, ErrNotConfigured):
		status = http.StatusServiceUnavailable
	case errors.Is(err, ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, ErrSchema):
		status = http.StatusServiceUnavailable
	default:
		status = http.StatusBadGateway
	}
	writeError(w, status, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
