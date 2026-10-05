package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxSubtitleRequestBytes = 64 << 10

// Register installs the subtitle API below /api/v1; the parent server applies host, origin, and content-type checks.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/subtitles", func(w http.ResponseWriter, r *http.Request) {
		filter, ok := libraryFilter(w, r)
		if !ok {
			return
		}
		items, err := s.Library(r.Context(), filter)
		respondSubtitle(w, http.StatusOK, items, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/scan", func(w http.ResponseWriter, r *http.Request) {
		if !decodeSubtitleBody(w, r, &struct{}{}, false, "scan") {
			return
		}
		if err := s.launchScan(); err != nil {
			respondSubtitle(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
	})
	mux.HandleFunc("GET /api/v1/subtitles/wanted", func(w http.ResponseWriter, r *http.Request) {
		items, err := s.Wanted(r.Context())
		respondSubtitle(w, http.StatusOK, items, err)
	})
	mux.HandleFunc("GET /api/v1/subtitles/history", func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id != "" && (kind == "" || len(id) > 128) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid history request"})
			return
		}
		limit, err := intParam(r, "limit", 1, maxHistoryLimit, 100)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		entries, err := s.History(r.Context(), kind, id, limit)
		respondSubtitle(w, http.StatusOK, entries, err)
	})
	mux.HandleFunc("GET /api/v1/subtitles/jobs", func(w http.ResponseWriter, r *http.Request) {
		activeOnly := r.URL.Query().Get("active") != ""
		limit, err := intParam(r, "limit", 1, maxJobLimit, 20)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		jobs, err := s.Jobs(r.Context(), activeOnly, limit)
		respondSubtitle(w, http.StatusOK, jobs, err)
	})
	mux.HandleFunc("GET /api/v1/subtitles/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		job, err := s.Job(r.Context(), r.PathValue("id"))
		respondSubtitle(w, http.StatusOK, job, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !decodeSubtitleBody(w, r, &struct{}{}, false, "cancel") {
			return
		}
		if err := s.CancelJob(r.Context(), r.PathValue("id")); err != nil {
			respondSubtitle(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/subtitles/providers", func(w http.ResponseWriter, r *http.Request) {
		statuses, err := s.Providers(r.Context())
		respondSubtitle(w, http.StatusOK, statuses, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/providers/test", func(w http.ResponseWriter, r *http.Request) {
		s.testProviders(w, r)
	})
	mux.HandleFunc("GET /api/v1/subtitle-config", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := s.Config(r.Context())
		respondSubtitle(w, http.StatusOK, cfg, err)
	})
	mux.HandleFunc("PUT /api/v1/subtitle-config", func(w http.ResponseWriter, r *http.Request) {
		var input Config
		if !decodeSubtitleBody(w, r, &input, true, "config") {
			return
		}
		cfg, err := s.SetConfig(r.Context(), input)
		respondSubtitle(w, http.StatusOK, cfg, err)
	})
	mux.HandleFunc("POST /api/v1/subtitle-config/test", func(w http.ResponseWriter, r *http.Request) {
		s.testProviders(w, r)
	})
	mux.HandleFunc("GET /api/v1/subtitle-profiles", func(w http.ResponseWriter, r *http.Request) {
		profiles, err := s.Profiles(r.Context())
		respondSubtitle(w, http.StatusOK, profiles, err)
	})
	mux.HandleFunc("PUT /api/v1/subtitle-profiles", func(w http.ResponseWriter, r *http.Request) {
		var profile Profile
		if !decodeSubtitleBody(w, r, &profile, true, "profile") {
			return
		}
		saved, err := s.SaveProfile(r.Context(), profile)
		respondSubtitle(w, http.StatusOK, saved, err)
	})
	mux.HandleFunc("DELETE /api/v1/subtitle-profiles/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DeleteProfile(r.Context(), r.PathValue("id")); err != nil {
			respondSubtitle(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/subtitles/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		limit, err := intParam(r, "historyLimit", 1, maxHistoryLimit, 50)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		detail, err := s.Detail(r.Context(), kind, id, limit)
		respondSubtitle(w, http.StatusOK, detail, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/{kind}/{id}/search", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		var input struct {
			Languages []LanguagePreference `json:"languages"`
		}
		if !decodeSubtitleBody(w, r, &input, false, "search") {
			return
		}
		outcome, err := s.Search(r.Context(), kind, id, input.Languages)
		respondSubtitle(w, http.StatusOK, outcome, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/{kind}/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		var input DownloadRequest
		if !decodeSubtitleBody(w, r, &input, true, "download") {
			return
		}
		job, err := s.Download(r.Context(), kind, id, input)
		respondSubtitle(w, http.StatusCreated, job, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/{kind}/{id}/sync", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		var input SyncRequest
		if !decodeSubtitleBody(w, r, &input, true, "sync") {
			return
		}
		job, err := s.Sync(r.Context(), kind, id, input)
		respondSubtitle(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/{kind}/{id}/translate", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		var input TranslateRequest
		if !decodeSubtitleBody(w, r, &input, true, "translation") {
			return
		}
		job, err := s.Translate(r.Context(), kind, id, input)
		respondSubtitle(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/{kind}/{id}/extract", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		var input ExtractRequest
		if !decodeSubtitleBody(w, r, &input, true, "extraction") {
			return
		}
		job, err := s.Extract(r.Context(), kind, id, input)
		respondSubtitle(w, http.StatusAccepted, job, err)
	})
	mux.HandleFunc("GET /api/v1/subtitles/{kind}/{id}/streams", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		streams, err := s.Streams(r.Context(), kind, id)
		respondSubtitle(w, http.StatusOK, streams, err)
	})
	mux.HandleFunc("GET /api/v1/subtitles/{kind}/{id}/file", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		rel := r.URL.Query().Get("path")
		if rel == "" || len(rel) > maxPathBytes {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a subtitle path is required"})
			return
		}
		file, err := s.OpenSidecar(r.Context(), kind, id, rel)
		if err != nil {
			respondSubtitle(w, 0, nil, err)
			return
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			respondSubtitle(w, 0, nil, ErrNotFound)
			return
		}
		name := filepath.Base(rel)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		w.Header().Set("Content-Type", contentTypeForSubtitle(name))
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		http.ServeContent(w, r, name, info.ModTime(), file)
	})
	mux.HandleFunc("POST /api/v1/subtitles/{kind}/{id}/assignment", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		var input struct {
			ProfileID string `json:"profileId"`
			Monitored bool   `json:"monitored"`
		}
		if !decodeSubtitleBody(w, r, &input, true, "assignment") {
			return
		}
		assignment, err := s.SetAssignment(r.Context(), kind, id, input.ProfileID, input.Monitored)
		respondSubtitle(w, http.StatusOK, assignment, err)
	})
	mux.HandleFunc("DELETE /api/v1/subtitles/{kind}/{id}/assignment", func(w http.ResponseWriter, r *http.Request) {
		kind, id, ok := videoPath(w, r)
		if !ok {
			return
		}
		if err := s.ResetAssignment(r.Context(), kind, id); err != nil {
			respondSubtitle(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/subtitles/outputs/{id}", func(w http.ResponseWriter, r *http.Request) {
		output, err := s.Output(r.Context(), r.PathValue("id"))
		respondSubtitle(w, http.StatusOK, output, err)
	})
	mux.HandleFunc("POST /api/v1/subtitles/outputs/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		if !decodeSubtitleBody(w, r, &struct{}{}, false, "apply") {
			return
		}
		sidecar, err := s.ApplyOutput(r.Context(), r.PathValue("id"))
		respondSubtitle(w, http.StatusOK, sidecar, err)
	})
	mux.HandleFunc("DELETE /api/v1/subtitles/outputs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DiscardOutput(r.Context(), r.PathValue("id")); err != nil {
			respondSubtitle(w, 0, nil, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}

func (s *Service) testProviders(w http.ResponseWriter, r *http.Request) {
	if !decodeSubtitleBody(w, r, &struct{}{}, false, "provider test") {
		return
	}
	results, err := s.TestProviders(r.Context())
	respondSubtitle(w, http.StatusOK, results, err)
}

func videoPath(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	kind := strings.ToLower(strings.TrimSpace(r.PathValue("kind")))
	id := r.PathValue("id")
	if kind != KindMovie && kind != KindEpisode {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "video kind must be movie or episode"})
		return "", "", false
	}
	if id == "" || len(id) > 128 || strings.ContainsAny(id, `/\`) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid video id"})
		return "", "", false
	}
	return kind, id, true
}

func libraryFilter(w http.ResponseWriter, r *http.Request) (LibraryFilter, bool) {
	query := r.URL.Query()
	filter := LibraryFilter{
		Query: strings.TrimSpace(query.Get("q")),
		Kind:  strings.ToLower(strings.TrimSpace(query.Get("kind"))),
	}
	if utf8.RuneCountInString(filter.Query) > 200 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "search text is too long"})
		return filter, false
	}
	if filter.Kind != "" && filter.Kind != KindMovie && filter.Kind != KindEpisode {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "video kind must be movie or episode"})
		return filter, false
	}
	switch status := strings.ToLower(strings.TrimSpace(query.Get("status"))); status {
	case "", "missing", "satisfied", "unmonitored":
		filter.Status = status
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be missing, satisfied, or unmonitored"})
		return filter, false
	}
	filter.Missing = query.Get("missing") == "1" || query.Get("missing") == "true"
	limit, err := intParam(r, "limit", 1, maxLibraryLimit, libraryLimit)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return filter, false
	}
	filter.Limit = limit
	return filter, true
}

func intParam(r *http.Request, name string, low, high, fallback int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < low || value > high {
		return 0, errors.New(name + " must be a number between " + strconv.Itoa(low) + " and " + strconv.Itoa(high))
	}
	return value, nil
}

// decodeSubtitleBody reads one bounded JSON document, rejecting unknown fields and trailing data.
func decodeSubtitleBody(w http.ResponseWriter, r *http.Request, body any, required bool, name string) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSubtitleRequestBytes))
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

func respondSubtitle(w http.ResponseWriter, status int, body any, err error) {
	if err != nil {
		code, message := subtitleError(err)
		writeJSON(w, code, map[string]string{"error": message})
		return
	}
	if status == 0 {
		status = http.StatusOK
	}
	writeJSON(w, status, body)
}

// subtitleError maps service failures to safe status codes and messages.
func subtitleError(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, publicError(err)
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrUnsafe):
		return http.StatusBadRequest, publicError(err)
	case errors.Is(err, ErrConflict), errors.Is(err, ErrNotConfigured), errors.Is(err, ErrNoTranslator), errors.Is(err, ErrBudget):
		return http.StatusConflict, publicError(err)
	case errors.Is(err, ErrQuota), errors.Is(err, ErrRateLimited):
		return http.StatusTooManyRequests, publicError(err)
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable, publicError(err)
	case errors.Is(err, ErrTimeout):
		return http.StatusGatewayTimeout, publicError(err)
	case errors.Is(err, ErrCueIntegrity), errors.Is(err, ErrLanguageMismatch), errors.Is(err, ErrLowQualitySync):
		return http.StatusUnprocessableEntity, publicError(err)
	case errors.Is(err, context.Canceled):
		return http.StatusConflict, "the subtitle operation was cancelled"
	default:
		return http.StatusInternalServerError, "the subtitle operation failed"
	}
}

func contentTypeForSubtitle(name string) string {
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); value != "" {
		return value
	}
	return "text/plain; charset=utf-8"
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
