package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/discovery/ai"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

const maxBodyBytes = 256 << 10

// Register mounts the discovery API; identity and permissions come from Options.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/requests", s.handleRequestList)
	mux.HandleFunc("POST /api/v1/requests", s.handleRequestCreate)
	mux.HandleFunc("GET /api/v1/requests/discover", s.handleRequestDiscover)
	mux.HandleFunc("GET /api/v1/requests/{id}", s.handleRequestDetail)
	mux.HandleFunc("POST /api/v1/requests/{id}/approve", s.handleRequestApprove)
	mux.HandleFunc("POST /api/v1/requests/{id}/reject", s.handleRequestReject)
	mux.HandleFunc("POST /api/v1/requests/{id}/cancel", s.handleRequestCancel)
	mux.HandleFunc("POST /api/v1/requests/{id}/comments", s.handleRequestComment)
	mux.HandleFunc("GET /api/v1/calendar", s.handleCalendar)
	mux.HandleFunc("GET /api/v1/calendar.ics", s.handleCalendarICS)
	mux.HandleFunc("GET /api/v1/ai/config", s.handleAIConfig)
	mux.HandleFunc("PUT /api/v1/ai/config", s.handleAISaveConfig)
	mux.HandleFunc("POST /api/v1/ai/test", s.handleAITest)
	mux.HandleFunc("GET /api/v1/ai/models", s.handleAIModels)
	mux.HandleFunc("POST /api/v1/recommendations", s.handleRecommendCreate)
	mux.HandleFunc("GET /api/v1/recommendations", s.handleRecommendList)
	mux.HandleFunc("GET /api/v1/recommendations/{id}", s.handleRecommendGet)
	mux.HandleFunc("POST /api/v1/recommendations/{id}/accept", s.handleRecommendAccept)
}

func (s *Service) handleRequestList(w http.ResponseWriter, r *http.Request) {
	actor, canApprove, ok := s.caller(w, r)
	if !ok {
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxListSize {
			s.respond(w, 0, nil, ErrInvalid)
			return
		}
		limit = parsed
	}
	list, err := s.List(r.Context(), actor, s.permissions(r, canApprove), ListFilter{
		Status:    r.URL.Query().Get("status"),
		MediaType: r.URL.Query().Get("type"),
		UserID:    r.URL.Query().Get("user"),
		Query:     r.URL.Query().Get("q"),
		Limit:     limit,
	})
	s.respond(w, http.StatusOK, list, err)
}

func (s *Service) handleRequestCreate(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := s.caller(w, r)
	if !ok {
		return
	}
	var input CreateInput
	if !decodeBody(w, r, &input, true, "request") {
		return
	}
	request, created, err := s.Create(r.Context(), actor, input)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.respond(w, status, request, err)
}

func (s *Service) handleRequestDiscover(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxDiscoverPage {
			s.respond(w, 0, nil, ErrInvalid)
			return
		}
		page = parsed
	}
	results, err := s.Discover(r.Context(), r.URL.Query().Get("type"), r.URL.Query().Get("q"), page)
	s.respond(w, http.StatusOK, results, err)
}

func (s *Service) handleRequestDetail(w http.ResponseWriter, r *http.Request) {
	actor, canApprove, ok := s.caller(w, r)
	if !ok {
		return
	}
	id, ok := boundedID(w, r, "request")
	if !ok {
		return
	}
	detail, err := s.Detail(r.Context(), actor, s.permissions(r, canApprove), id)
	s.respond(w, http.StatusOK, detail, err)
}

func (s *Service) handleRequestApprove(w http.ResponseWriter, r *http.Request) {
	actor, canApprove, ok := s.caller(w, r)
	if !ok {
		return
	}
	if !s.canApproveRequest(r, canApprove) {
		s.respond(w, 0, nil, ErrForbidden)
		return
	}
	id, ok := boundedID(w, r, "request")
	if !ok {
		return
	}
	var input ApproveInput
	if !decodeBody(w, r, &input, false, "approval") {
		return
	}
	request, err := s.Approve(r.Context(), actor, id, input)
	s.respond(w, http.StatusOK, request, err)
}

func (s *Service) handleRequestReject(w http.ResponseWriter, r *http.Request) {
	actor, canApprove, ok := s.caller(w, r)
	if !ok {
		return
	}
	if !s.canApproveRequest(r, canApprove) {
		s.respond(w, 0, nil, ErrForbidden)
		return
	}
	id, ok := boundedID(w, r, "request")
	if !ok {
		return
	}
	var input RejectInput
	if !decodeBody(w, r, &input, true, "decision") {
		return
	}
	request, err := s.Reject(r.Context(), actor, id, input)
	s.respond(w, http.StatusOK, request, err)
}

func (s *Service) handleRequestCancel(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := s.caller(w, r)
	if !ok {
		return
	}
	id, ok := boundedID(w, r, "request")
	if !ok {
		return
	}
	if !decodeBody(w, r, &struct{}{}, false, "cancellation") {
		return
	}
	request, err := s.Cancel(r.Context(), actor, id)
	s.respond(w, http.StatusOK, request, err)
}

func (s *Service) handleRequestComment(w http.ResponseWriter, r *http.Request) {
	actor, canApprove, ok := s.caller(w, r)
	if !ok {
		return
	}
	id, ok := boundedID(w, r, "request")
	if !ok {
		return
	}
	var input CommentInput
	if !decodeBody(w, r, &input, true, "comment") {
		return
	}
	comment, err := s.Comment(r.Context(), actor, s.permissions(r, canApprove), id, input)
	s.respond(w, http.StatusCreated, comment, err)
}

func (s *Service) handleCalendar(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	query, err := calendarQuery(r)
	if err != nil {
		s.respond(w, 0, nil, err)
		return
	}
	view, err := s.Calendar(r.Context(), query)
	s.respond(w, http.StatusOK, view, err)
}

func (s *Service) handleCalendarICS(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	query, err := calendarQuery(r)
	if err != nil {
		s.respond(w, 0, nil, err)
		return
	}
	view, err := s.Calendar(r.Context(), query)
	if err != nil {
		s.respond(w, 0, nil, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "constellarr.ics"}))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(CalendarICS(view, s.now()))
}

// calendarQuery accepts both the plural filters and their singular aliases.
func calendarQuery(r *http.Request) (CalendarQuery, error) {
	values := r.URL.Query()
	query := CalendarQuery{From: values.Get("from"), To: values.Get("to")}
	for _, entry := range append(values["types"], values["type"]...) {
		for _, part := range strings.Split(entry, ",") {
			if part = strings.TrimSpace(part); part != "" {
				query.Types = append(query.Types, part)
			}
		}
	}
	for _, entry := range append(values["sources"], values["source"]...) {
		for _, part := range strings.Split(entry, ",") {
			if part = strings.TrimSpace(part); part != "" {
				query.Sources = append(query.Sources, part)
			}
		}
	}
	if len(query.Types) > 10 || len(query.Sources) > 10 ||
		len(query.From) > 32 || len(query.To) > 32 {
		return CalendarQuery{}, ErrInvalid
	}
	return query, nil
}

func (s *Service) handleAIConfig(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	config, err := s.AI.GetConfig(r.Context())
	s.respond(w, http.StatusOK, config, err)
}

func (s *Service) handleAISaveConfig(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	var input ai.Config
	if !decodeBody(w, r, &input, true, "AI configuration") {
		return
	}
	config, err := s.AI.SetConfig(r.Context(), input)
	s.respond(w, http.StatusOK, config, err)
}

func (s *Service) handleAITest(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	if !decodeBody(w, r, &struct{}{}, false, "AI test") {
		return
	}
	result, err := s.AI.Test(r.Context())
	s.respond(w, http.StatusOK, result, err)
}

func (s *Service) handleAIModels(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.caller(w, r); !ok {
		return
	}
	models, err := s.AI.Models(r.Context())
	if err != nil {
		s.respond(w, 0, nil, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"models": models})
}

func (s *Service) handleRecommendCreate(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := s.caller(w, r)
	if !ok || !s.requireLibraryRead(w, r) {
		return
	}
	var input RecommendationInput
	if !decodeBody(w, r, &input, true, "recommendation") {
		return
	}
	rec, err := s.Generate(r.Context(), actor, input)
	s.respond(w, http.StatusCreated, rec, err)
}

func (s *Service) handleRecommendList(w http.ResponseWriter, r *http.Request) {
	actor, canApprove, ok := s.caller(w, r)
	if !ok || !s.requireLibraryRead(w, r) {
		return
	}
	list, err := s.Recommendations(r.Context(), actor, s.permissions(r, canApprove))
	s.respond(w, http.StatusOK, list, err)
}

func (s *Service) handleRecommendGet(w http.ResponseWriter, r *http.Request) {
	actor, _, ok := s.caller(w, r)
	if !ok || !s.requireLibraryRead(w, r) {
		return
	}
	id, ok := boundedID(w, r, "recommendation")
	if !ok {
		return
	}
	rec, err := s.Recommendation(r.Context(), actor, id)
	s.respond(w, http.StatusOK, rec, err)
}

func (s *Service) handleRecommendAccept(w http.ResponseWriter, r *http.Request) {
	actor, canApprove, ok := s.caller(w, r)
	if !ok || !s.requireLibraryRead(w, r) {
		return
	}
	id, ok := boundedID(w, r, "recommendation")
	if !ok {
		return
	}
	var input AcceptInput
	if !decodeBody(w, r, &input, true, "acceptance") {
		return
	}
	result, err := s.Accept(r.Context(), actor, s.permissions(r, canApprove), id, input)
	s.respond(w, http.StatusOK, result, err)
}

// canApproveRequest accepts either the actor hook or the operation hook as the approval source.
func (s *Service) canApproveRequest(r *http.Request, canApprove bool) bool {
	return canApprove || s.permits(r, PermissionRequestsApprove)
}

// requireLibraryRead gates recommendation routes; the accept branch checks its own operation right.
func (s *Service) requireLibraryRead(w http.ResponseWriter, r *http.Request) bool {
	if s.permits(r, PermissionLibraryRead) {
		return true
	}
	s.respond(w, 0, nil, fmt.Errorf("%w: reading recommendations needs the library.read permission", ErrForbidden))
	return false
}

// caller resolves the authenticated identity; without a hook every route denies access.
func (s *Service) caller(w http.ResponseWriter, r *http.Request) (string, bool, bool) {
	if s.actorFn == nil {
		s.respond(w, 0, nil, ErrUnauthenticated)
		return "", false, false
	}
	userID, canApprove := s.actorFn(r)
	userID = strings.TrimSpace(userID)
	if userID == "" || len(userID) > identifierBytes {
		s.respond(w, 0, nil, ErrUnauthenticated)
		return "", false, false
	}
	return userID, canApprove, true
}

func boundedID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	id := r.PathValue("id")
	if id == "" || len(id) > identifierBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid " + name + " id"})
		return "", false
	}
	return id, true
}

// decodeBody reads one bounded JSON document, rejecting unknown fields and trailing data.
func decodeBody(w http.ResponseWriter, r *http.Request, body any, required bool, name string) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
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

func (s *Service) respond(w http.ResponseWriter, status int, body any, err error) {
	if err == nil {
		writeJSON(w, status, body)
		return
	}
	code := http.StatusBadGateway
	switch {
	case errors.Is(err, ErrUnauthenticated):
		code = http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, ErrInvalid), isAIConfigError(err):
		code = http.StatusBadRequest
	case errors.Is(err, ErrConflict):
		code = http.StatusConflict
	case errors.Is(err, ErrNotConfigured), errors.Is(err, downloads.ErrNotConfigured), errors.Is(err, ai.ErrNotConfigured):
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]string{"error": sanitize(err.Error())})
}

func isAIConfigError(err error) bool {
	var providerErr *ai.Error
	return errors.As(err, &providerErr) && providerErr.Kind == "invalid config"
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
