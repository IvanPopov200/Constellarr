package auth

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxRequestBody = 16 << 10

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/auth/status", s.handleStatus)
	mux.HandleFunc("POST /api/v1/auth/setup", s.handleSetup)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	mux.HandleFunc("PUT /api/v1/auth/password", s.handlePasswordChange)
	mux.HandleFunc("GET /api/v1/auth/tokens", s.handleTokenList)
	mux.HandleFunc("POST /api/v1/auth/tokens", s.handleTokenCreate)
	mux.HandleFunc("PUT /api/v1/auth/tokens/{id}", s.handleTokenUpdate)
	mux.HandleFunc("DELETE /api/v1/auth/tokens/{id}", s.handleTokenDelete)
	mux.HandleFunc("GET /api/v1/users", s.handleUserList)
	mux.HandleFunc("POST /api/v1/users", s.handleUserCreate)
	mux.HandleFunc("GET /api/v1/users/{id}", s.handleUserGet)
	mux.HandleFunc("PUT /api/v1/users/{id}", s.handleUserUpdate)
	mux.HandleFunc("DELETE /api/v1/users/{id}", s.handleUserDelete)
	mux.HandleFunc("PUT /api/v1/users/{id}/password", s.handleUserPassword)
	mux.HandleFunc("GET /api/v1/roles", s.handleRoleList)
	mux.HandleFunc("POST /api/v1/roles", s.handleRoleCreate)
	mux.HandleFunc("PUT /api/v1/roles/{id}", s.handleRoleUpdate)
	mux.HandleFunc("DELETE /api/v1/roles/{id}", s.handleRoleDelete)
	mux.HandleFunc("GET /api/v1/permissions", s.handlePermissionList)
	mux.HandleFunc("GET /api/v1/audit", s.handleAuditList)
}

// handleStatus is reachable anonymously and reports first-run state.
func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	required, err := s.setupRequired(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	response := struct {
		SetupRequired bool  `json:"setupRequired"`
		Authenticated bool  `json:"authenticated"`
		User          *User `json:"user"`
	}{SetupRequired: required}
	if principal, ok := FromContext(r.Context()); ok {
		response.Authenticated = true
		user, err := s.userByID(r.Context(), s.pool, principal.UserID)
		if err != nil {
			s.fail(w, err)
			return
		}
		user.Permissions = principal.Permissions
		response.User = &user
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Service) handleSetup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	user, secret, expires, err := s.setup(r.Context(), strings.TrimSpace(input.Name), input.Password, clientIP(r), r.UserAgent())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.setSessionCookie(w, r, secret, expires)
	writeJSON(w, http.StatusCreated, user)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	ip := clientIP(r)
	if wait := s.limiter.blocked(name, ip); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many attempts; try again later")
		return
	}
	password := input.Password
	if len(password) > passwordMaxBytes {
		password = ""
	}
	user, hash, err := s.accountByName(r.Context(), name)
	valid := err == nil && verifyPassword(password, hash)
	if err != nil {
		// Unknown accounts are verified against a dummy hash so login timing stays uniform.
		verifyPassword(password, s.dummyHash)
	}
	if !valid || !user.Active {
		s.limiter.fail(name, ip)
		s.recordAudit(r.Context(), "", name, "login", name, "failure")
		writeError(w, http.StatusBadRequest, "the name or password is incorrect")
		return
	}
	s.limiter.reset(name, ip)
	secret, expires, err := s.createUserSession(r.Context(), user.ID, ip, r.UserAgent())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.setSessionCookie(w, r, secret, expires)
	s.recordAudit(r.Context(), user.ID, user.Name, "login", user.Name, "success")
	writeJSON(w, http.StatusOK, user)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	principal, _ := FromContext(r.Context())
	if principal.SessionID != "" {
		if err := s.deleteSession(r.Context(), principal.SessionID); err != nil {
			s.fail(w, err)
			return
		}
		s.recordAudit(r.Context(), principal.UserID, principal.Name, "logout", principal.Name, "success")
	} else if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		if err := s.deleteSessionBySecret(r.Context(), cookie.Value); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := FromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	user, err := s.userByID(r.Context(), s.pool, principal.UserID)
	if err != nil {
		s.fail(w, err)
		return
	}
	user.Permissions = principal.Permissions
	writeJSON(w, http.StatusOK, user)
}

func (s *Service) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	principal, ok := FromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var input struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decode(w, r, &input) {
		return
	}
	limitName := "password:" + principal.UserID
	limitIP := clientIP(r)
	if wait := s.limiter.blocked(limitName, limitIP); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too many attempts; try again later")
		return
	}
	_, hash, err := s.accountByName(r.Context(), principal.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !verifyPassword(input.CurrentPassword, hash) {
		s.limiter.fail(limitName, limitIP)
		s.recordAudit(r.Context(), principal.UserID, principal.Name, "password.change", principal.Name, "failure")
		writeError(w, http.StatusBadRequest, "the current password is incorrect")
		return
	}
	s.limiter.reset(limitName, limitIP)
	if err := s.setUserPassword(r.Context(), principal.UserID, input.NewPassword); err != nil {
		s.fail(w, err)
		return
	}
	s.clearSessionCookie(w, r)
	s.recordAudit(r.Context(), principal.UserID, principal.Name, "password.change", principal.Name, "success")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleTokenList(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.listTokens(r.Context(), actorID(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

func (s *Service) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string     `json:"name"`
		Permissions []string   `json:"permissions"`
		ExpiresAt   *time.Time `json:"expiresAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	token, secret, err := s.createToken(r.Context(), actorID(r), strings.TrimSpace(input.Name), input.Permissions, input.ExpiresAt)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "token.create", token.Name, "success")
	writeJSON(w, http.StatusCreated, struct {
		Token
		Secret string `json:"token"`
	}{token, secret})
}

func (s *Service) handleTokenUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        *string   `json:"name"`
		Permissions *[]string `json:"permissions"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Name != nil {
		trimmed := strings.TrimSpace(*input.Name)
		input.Name = &trimmed
	}
	token, err := s.updateToken(r.Context(), actorID(r), r.PathValue("id"), input.Name, input.Permissions)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "token.update", token.Name, "success")
	writeJSON(w, http.StatusOK, token)
}

func (s *Service) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deleteToken(r.Context(), actorID(r), id); err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "token.delete", id, "success")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleUserList(w http.ResponseWriter, r *http.Request) {
	users, err := s.listUsers(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Service) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name     string   `json:"name"`
		Password string   `json:"password"`
		Active   *bool    `json:"active"`
		Roles    []string `json:"roles"`
	}
	if !decode(w, r, &input) {
		return
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	user, err := s.createUser(r.Context(), strings.TrimSpace(input.Name), input.Password, active, input.Roles)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "user.create", user.Name, "success")
	writeJSON(w, http.StatusCreated, user)
}

func (s *Service) handleUserGet(w http.ResponseWriter, r *http.Request) {
	user, err := s.userByID(r.Context(), s.pool, r.PathValue("id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (s *Service) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   *string   `json:"name"`
		Active *bool     `json:"active"`
		Roles  *[]string `json:"roles"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Name != nil {
		trimmed := strings.TrimSpace(*input.Name)
		input.Name = &trimmed
	}
	user, err := s.updateUser(r.Context(), r.PathValue("id"), UserUpdate{Name: input.Name, Active: input.Active, Roles: input.Roles})
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "user.update", user.Name, "success")
	writeJSON(w, http.StatusOK, user)
}

func (s *Service) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deleteUser(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "user.delete", id, "success")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	id := r.PathValue("id")
	if err := s.setUserPassword(r.Context(), id, input.Password); err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "user.password", id, "success")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleRoleList(w http.ResponseWriter, r *http.Request) {
	roles, err := s.listRoles(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roles)
}

func (s *Service) handleRoleCreate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if !decode(w, r, &input) {
		return
	}
	role, err := s.createRole(r.Context(), strings.TrimSpace(input.Name), input.Permissions)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "role.create", role.Name, "success")
	writeJSON(w, http.StatusCreated, role)
}

func (s *Service) handleRoleUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name        *string   `json:"name"`
		Permissions *[]string `json:"permissions"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Name != nil {
		trimmed := strings.TrimSpace(*input.Name)
		input.Name = &trimmed
	}
	role, err := s.updateRole(r.Context(), r.PathValue("id"), input.Name, input.Permissions)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "role.update", role.Name, "success")
	writeJSON(w, http.StatusOK, role)
}

func (s *Service) handleRoleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.deleteRole(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.auditActor(r, "role.delete", id, "success")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handlePermissionList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Permissions []string `json:"permissions"`
	}{Permissions: PermissionCatalog})
}

func (s *Service) handleAuditList(w http.ResponseWriter, r *http.Request) {
	limit := defaultAuditLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAuditLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	entries, err := s.listAudit(r.Context(), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func actorID(r *http.Request) string {
	principal, _ := FromContext(r.Context())
	return principal.UserID
}

func (s *Service) auditActor(r *http.Request, action, target, outcome string) {
	principal, _ := FromContext(r.Context())
	s.recordAudit(r.Context(), principal.UserID, principal.Name, action, target, outcome)
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "send one JSON object")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// fail maps expected errors to client responses and hides internal failures.
func (s *Service) fail(w http.ResponseWriter, err error) {
	var failure *apiError
	if errors.As(err, &failure) {
		writeError(w, failure.status, failure.message)
		return
	}
	slog.Warn("auth: request failed", "error", err)
	writeError(w, http.StatusInternalServerError, "auth request failed")
}
