// Package auth implements password accounts, sessions, scoped API tokens, roles,
// and permission enforcement for the Constellarr API.
package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	sessionCookie     = "constellarr_session"
	sessionTTL        = 30 * 24 * time.Hour
	tokenPrefix       = "ctlr_"
	auditTimeout      = 3 * time.Second
	cleanupInterval   = time.Hour
	defaultAuditLimit = 50
	maxAuditLimit     = 200
	nameMinRunes      = 2
	nameMaxRunes      = 64
	maxUserAgentRunes = 256
)

type Service struct {
	pool          *pgxpool.Pool
	limiter       *loginLimiter
	secureCookies bool
	dummyHash     string

	backgroundMu     sync.Mutex
	backgroundCancel context.CancelFunc
	backgroundDone   chan struct{}
}

type Option func(*Service)

// WithSecureCookies marks session cookies Secure when a trusted proxy terminates TLS.
func WithSecureCookies(secure bool) Option {
	return func(s *Service) { s.secureCookies = secure }
}

// New verifies the migrated auth schema, seeds built-in roles, and prepares the limiter.
func New(ctx context.Context, pool *pgxpool.Pool, options ...Option) (*Service, error) {
	if pool == nil {
		return nil, errors.New("auth: a PostgreSQL pool is required")
	}
	service := &Service{pool: pool, limiter: newLoginLimiter(), secureCookies: envFlag("AUTH_SECURE_COOKIES")}
	for _, option := range options {
		option(service)
	}
	if err := service.verifySchema(ctx); err != nil {
		return nil, err
	}
	if err := seedBuiltinRoles(ctx, pool); err != nil {
		return nil, err
	}
	// Unknown accounts are verified against a dummy hash so login timing stays uniform.
	hash, err := hashPassword("constellarr-timing-equalizer")
	if err != nil {
		return nil, err
	}
	service.dummyHash = hash
	return service, nil
}

func envFlag(name string) bool {
	value := strings.TrimSpace(os.Getenv(name))
	return strings.EqualFold(value, "true") || value == "1"
}

type Principal struct {
	UserID      string
	Name        string
	Roles       []string
	Permissions []string
	SessionID   string
	TokenID     string
}

func (p Principal) Can(permission string) bool {
	for _, held := range p.Permissions {
		if held == permission {
			return true
		}
	}
	return false
}

func (p Principal) IsAdmin() bool {
	return contains(p.Roles, roleAdmin)
}

func (p Principal) sessionCredential() bool {
	return p.SessionID != "" && p.TokenID == ""
}

type principalKey struct{}

func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok
}

func UserID(ctx context.Context) (string, bool) {
	principal, ok := FromContext(ctx)
	if !ok || principal.UserID == "" {
		return "", false
	}
	return principal.UserID, true
}

func Can(ctx context.Context, permission string) bool {
	principal, ok := FromContext(ctx)
	return ok && principal.Can(permission)
}

func withPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// Middleware authenticates API and metrics requests and enforces the route map.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !protectedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		principal, authenticated := Principal{}, false
		if credentialsPresent(r) {
			principal, authenticated = s.authenticate(r.Context(), r)
		}
		if publicRoute(r.Method, r.URL.Path) {
			if authenticated {
				r = r.WithContext(withPrincipal(r.Context(), principal))
			}
			next.ServeHTTP(w, r)
			return
		}
		if !authenticated {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		match, pathKnown, methodKnown := routePermission(r.Method, r.URL.Path)
		if !pathKnown {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		if !methodKnown {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if match.sessionOnly && !principal.sessionCredential() {
			writeError(w, http.StatusForbidden, "sign in to use this endpoint")
			return
		}
		if (match.permission != "" && !principal.Can(match.permission)) || (match.requires != "" && !principal.Can(match.requires)) {
			writeError(w, http.StatusForbidden, "permission denied")
			return
		}
		ctx := withPrincipal(r.Context(), principal)
		if match.selfAudited || !mutating(r.Method) {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r.WithContext(ctx))
		if recorder.status != 0 {
			s.recordAudit(ctx, principal.UserID, principal.Name, r.Method, match.pattern, strconv.Itoa(recorder.status))
		}
	})
}

// Require enforces a permission for handlers outside the central route map.
func (s *Service) Require(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := FromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "authentication required")
				return
			}
			if permission != "" && !principal.Can(permission) {
				writeError(w, http.StatusForbidden, "permission denied")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Audit records an event for the request actor; other services call it after mutations.
func (s *Service) Audit(ctx context.Context, action, target, outcome string) {
	principal, _ := FromContext(ctx)
	s.recordAudit(ctx, principal.UserID, principal.Name, action, target, outcome)
}

func (s *Service) Start(ctx context.Context) {
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	if s.backgroundCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.backgroundCancel = cancel
	s.backgroundDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.cleanup(ctx)
			}
		}
	}()
}

// Close is safe to call without Start.
func (s *Service) Close() {
	s.backgroundMu.Lock()
	cancel, done := s.backgroundCancel, s.backgroundDone
	s.backgroundCancel, s.backgroundDone = nil, nil
	s.backgroundMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

func protectedPath(path string) bool {
	return path == "/metrics" || strings.HasPrefix(path, "/metrics/") ||
		path == "/api" || strings.HasPrefix(path, "/api/")
}

// publicRoute lists the only routes reachable without a session or token.
func publicRoute(method, path string) bool {
	if method == http.MethodHead {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet:
		return path == "/api/v1/auth/status"
	case http.MethodPost:
		return path == "/api/v1/auth/setup" || path == "/api/v1/auth/login" || path == "/api/v1/auth/logout"
	}
	return false
}

func credentialsPresent(r *http.Request) bool {
	if r.Header.Get("Authorization") != "" {
		return true
	}
	cookie, err := r.Cookie(sessionCookie)
	return err == nil && cookie.Value != ""
}

func (s *Service) authenticate(ctx context.Context, r *http.Request) (Principal, bool) {
	if header := r.Header.Get("Authorization"); header != "" {
		secret, ok := bearerToken(header)
		if !ok {
			return Principal{}, false
		}
		principal, err := s.tokenPrincipal(ctx, secret)
		if err != nil {
			return Principal{}, false
		}
		return principal, true
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return Principal{}, false
	}
	principal, err := s.sessionPrincipal(ctx, cookie.Value)
	if err != nil {
		return Principal{}, false
	}
	return principal, true
}

func bearerToken(header string) (string, bool) {
	scheme, value, ok := strings.Cut(strings.TrimSpace(header), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return "", false
	}
	return value, true
}

func mutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// statusRecorder captures the response status and keeps streaming interfaces usable.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.status == 0 {
		recorder.status = status
	}
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(data []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.ResponseWriter.Write(data)
}

func (recorder *statusRecorder) Flush() {
	if flusher, ok := recorder.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// ReadFrom keeps sendfile-style copies available for file downloads.
func (recorder *statusRecorder) ReadFrom(source io.Reader) (int64, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	if reader, ok := recorder.ResponseWriter.(io.ReaderFrom); ok {
		return reader.ReadFrom(source)
	}
	return io.Copy(recorder.ResponseWriter, source)
}

func (recorder *statusRecorder) Unwrap() http.ResponseWriter {
	return recorder.ResponseWriter
}

func (s *Service) setSessionCookie(w http.ResponseWriter, r *http.Request, secret string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    secret,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure(r),
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	})
}

func (s *Service) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure(r),
		MaxAge:   -1,
	})
}

// direct TLS or explicit configuration only; forwarded headers are never trusted.
func (s *Service) secure(r *http.Request) bool {
	return s.secureCookies || r.TLS != nil
}
