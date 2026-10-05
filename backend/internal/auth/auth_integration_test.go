package auth_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IvanPopov200/Constellarr/backend/internal/auth"
	"github.com/IvanPopov200/Constellarr/backend/internal/downloads"
)

const (
	adminPassword  = "orbit-lantern-42"
	viewerPassword = "viewer-lantern-42"
	managerPass    = "manager-lantern-42"
)

// authSchemaPool creates an isolated schema so auth tests never touch real tables.
func authSchemaPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to a reachable PostgreSQL server to run this test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("cannot create a pool from TEST_DATABASE_URL: %v", err)
	}
	schema := "auth_test_" + strings.ToLower(rand.Text())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("cannot create an isolated test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatalf("TEST_DATABASE_URL is invalid: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatalf("cannot create a pool for the test schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("cannot drop the test schema: %v", err)
		}
		admin.Close()
	})
	return pool
}

type env struct {
	service *auth.Service
	handler http.Handler
	pool    *pgxpool.Pool
}

// applyAuthMigration runs the canonical DDL that the downloads migration chain carries.
func applyAuthMigration(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	body, err := os.ReadFile("../downloads/migrations/007_auth.sql")
	if err != nil {
		t.Fatalf("read 007_auth.sql: %v", err)
	}
	if _, err := pool.Exec(context.Background(), string(body)); err != nil {
		t.Fatalf("apply 007_auth.sql: %v", err)
	}
}

// newEnv wires the real middleware around stub handlers for existing API routes.
func newEnv(t *testing.T, options ...auth.Option) *env {
	t.Helper()
	pool := authSchemaPool(t)
	applyAuthMigration(t, pool)
	service, err := auth.New(context.Background(), pool, options...)
	if err != nil {
		t.Fatalf("auth.New: %v", err)
	}
	service.Start(context.Background())
	t.Cleanup(service.Close)

	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	mux := http.NewServeMux()
	service.Register(mux)
	mux.HandleFunc("GET /api/v1/movies", ok)
	mux.HandleFunc("POST /api/v1/movies", ok)
	mux.HandleFunc("GET /api/v1/movie-poster", ok)
	mux.HandleFunc("GET /api/v1/operations/status", func(w http.ResponseWriter, r *http.Request) {
		service.Audit(r.Context(), "test.hook", "target", "acted")
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("POST /api/v1/downloads", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("DELETE /api/v1/torrents/{id}", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	})
	mux.HandleFunc("GET /metrics", ok)
	mux.HandleFunc("GET /api/v1/operations/backups", ok)
	mux.HandleFunc("POST /api/v1/operations/backups/{id}/restore", ok)
	mux.HandleFunc("GET /healthz", ok)
	mux.Handle("/", http.HandlerFunc(ok))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	})
	return &env{service: service, handler: service.Middleware(mux), pool: pool}
}

type credentials struct {
	cookie string
	bearer string
	tls    bool
}

func rawRequest(handler http.Handler, method, target string, body []byte, creds credentials, authorization string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if creds.cookie != "" {
		request.AddCookie(&http.Cookie{Name: "constellarr_session", Value: creds.cookie})
	}
	if creds.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+creds.bearer)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if creds.tls {
		request.TLS = &tls.ConnectionState{}
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func (e *env) do(t *testing.T, method, target string, body any, creds credentials) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		raw = encoded
	}
	return rawRequest(e.handler, method, target, raw, creds, "")
}

// check asserts a status for a request with no body.
func (e *env) check(t *testing.T, creds credentials, method, target string, want int) {
	t.Helper()
	if recorder := e.do(t, method, target, nil, creds); recorder.Code != want {
		t.Fatalf("%s %s: status %d, want %d (%s)", method, target, recorder.Code, want, recorder.Body.String())
	}
}

func sessionCookie(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "constellarr_session" && cookie.Value != "" {
			return cookie.Value
		}
	}
	return ""
}

func responseCookie(recorder *httptest.ResponseRecorder) *http.Cookie {
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == "constellarr_session" {
			return cookie
		}
	}
	return nil
}

func decode[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("response is not JSON: %s", recorder.Body.String())
	}
	return value
}

// setupAdmin completes first-run setup and returns the administrator session.
func (e *env) setupAdmin(t *testing.T) (credentials, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := e.do(t, http.MethodPost, "/api/v1/auth/setup",
		map[string]string{"name": "admin", "password": adminPassword}, credentials{})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("setup: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	cookie := sessionCookie(t, recorder)
	if cookie == "" {
		t.Fatal("setup did not return a session cookie")
	}
	return credentials{cookie: cookie}, recorder
}

func (e *env) login(t *testing.T, name, password string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"name": name, "password": password}, credentials{})
}

func (e *env) createUser(t *testing.T, admin credentials, name, password string, roles []string) auth.User {
	t.Helper()
	recorder := e.do(t, http.MethodPost, "/api/v1/users",
		map[string]any{"name": name, "password": password, "roles": roles}, admin)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create user %s: status %d, body %s", name, recorder.Code, recorder.Body.String())
	}
	return decode[auth.User](t, recorder)
}

type tokenResponse struct {
	auth.Token
	Secret string `json:"token"`
}

func (e *env) createToken(t *testing.T, creds credentials, body map[string]any) tokenResponse {
	t.Helper()
	recorder := e.do(t, http.MethodPost, "/api/v1/auth/tokens", body, creds)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create token: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	return decode[tokenResponse](t, recorder)
}

type statusResponse struct {
	SetupRequired bool       `json:"setupRequired"`
	Authenticated bool       `json:"authenticated"`
	User          *auth.User `json:"user"`
}

func TestSetupSessionLifecycle(t *testing.T) {
	env := newEnv(t)

	status := decode[statusResponse](t, env.do(t, http.MethodGet, "/api/v1/auth/status", nil, credentials{}))
	if !status.SetupRequired || status.Authenticated || status.User != nil {
		t.Fatalf("fresh status = %+v", status)
	}
	env.check(t, credentials{}, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)

	admin, setup := env.setupAdmin(t)
	cookie := responseCookie(setup)
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Secure || cookie.MaxAge <= 0 {
		t.Fatalf("session cookie attributes = %+v", cookie)
	}

	status = decode[statusResponse](t, env.do(t, http.MethodGet, "/api/v1/auth/status", nil, admin))
	if status.SetupRequired || !status.Authenticated || status.User == nil || status.User.Name != "admin" {
		t.Fatalf("authenticated status = %+v", status)
	}

	me := decode[auth.User](t, env.do(t, http.MethodGet, "/api/v1/auth/me", nil, admin))
	if me.Name != "admin" || !slices.Contains(me.Roles, "admin") || len(me.Permissions) != len(auth.PermissionCatalog) {
		t.Fatalf("admin account = %+v", me)
	}

	env.check(t, admin, http.MethodGet, "/api/v1/users", http.StatusOK)
	env.check(t, admin, http.MethodGet, "/api/v1/movies", http.StatusOK)

	// A second setup attempt cannot claim the server.
	second := env.do(t, http.MethodPost, "/api/v1/auth/setup", map[string]string{"name": "intruder", "password": adminPassword}, credentials{})
	if second.Code != http.StatusConflict {
		t.Fatalf("second setup: status %d", second.Code)
	}

	// Only the session hash is stored.
	var stored []byte
	if err := env.pool.QueryRow(context.Background(), `SELECT token_hash FROM auth_sessions`).Scan(&stored); err != nil {
		t.Fatalf("load session hash: %v", err)
	}
	sum := sha256.Sum256([]byte(admin.cookie))
	if !bytes.Equal(stored, sum[:]) || strings.Contains(string(stored), admin.cookie) {
		t.Fatal("session secret is not stored as a SHA-256 hash")
	}

	logout := env.do(t, http.MethodPost, "/api/v1/auth/logout", nil, admin)
	if logout.Code != http.StatusNoContent {
		t.Fatalf("logout: status %d", logout.Code)
	}
	if cleared := responseCookie(logout); cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 {
		t.Fatalf("logout cookie = %+v", cleared)
	}
	env.check(t, admin, http.MethodGet, "/api/v1/auth/me", http.StatusUnauthorized)
	env.check(t, credentials{}, http.MethodPost, "/api/v1/auth/logout", http.StatusNoContent)

	// Expired sessions are rejected.
	admin, _ = env.setupAdminThroughLogin(t)
	env.pool.Exec(context.Background(), `UPDATE auth_sessions SET expires_at = now() - interval '1 minute'`)
	env.check(t, admin, http.MethodGet, "/api/v1/auth/me", http.StatusUnauthorized)
}

// setupAdminThroughLogin signs in again after the setup session was revoked.
func (e *env) setupAdminThroughLogin(t *testing.T) (credentials, *httptest.ResponseRecorder) {
	t.Helper()
	recorder := e.login(t, "admin", adminPassword)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	cookie := sessionCookie(t, recorder)
	if cookie == "" {
		t.Fatal("login did not return a session cookie")
	}
	return credentials{cookie: cookie}, recorder
}

func TestLoginErrorsAreGeneric(t *testing.T) {
	env := newEnv(t)
	env.setupAdmin(t)

	wrong := env.login(t, "admin", "definitely-wrong")
	unknown := env.login(t, "no-such-account", "definitely-wrong")
	if wrong.Code != http.StatusBadRequest || unknown.Code != http.StatusBadRequest {
		t.Fatalf("statuses = %d, %d", wrong.Code, unknown.Code)
	}
	if wrong.Body.String() != unknown.Body.String() {
		t.Fatalf("credential errors differ: %s vs %s", wrong.Body.String(), unknown.Body.String())
	}
	if !strings.Contains(wrong.Body.String(), "the name or password is incorrect") {
		t.Fatalf("unexpected error body: %s", wrong.Body.String())
	}

	// Malformed and oversized payloads are rejected without reaching the account store.
	env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"name": "admin", "password": adminPassword, "extra": "x"}, credentials{})
	large := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"name": "admin", "password": strings.Repeat("x", 4096)}, credentials{})
	if large.Code != http.StatusBadRequest {
		t.Fatalf("oversized password: status %d", large.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	env := newEnv(t)
	env.setupAdmin(t)

	for attempt := 0; attempt < 8; attempt++ {
		if recorder := env.login(t, "admin", "wrong-password"); recorder.Code != http.StatusBadRequest {
			t.Fatalf("attempt %d: status %d", attempt, recorder.Code)
		}
	}
	blocked := env.login(t, "admin", "wrong-password")
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("blocked login: status %d, headers %v", blocked.Code, blocked.Header())
	}
	if correct := env.login(t, "admin", adminPassword); correct.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit did not apply to a correct password: status %d", correct.Code)
	}
}

func TestSetupIsSingleAtomicClaimant(t *testing.T) {
	env := newEnv(t)
	const attempts = 6
	statuses := make([]int, attempts)
	var claimants sync.WaitGroup
	for index := range attempts {
		claimants.Add(1)
		go func(index int) {
			defer claimants.Done()
			body := []byte(`{"name":"admin` + string(rune('a'+index)) + `","password":"atomic-claim-42"}`)
			recorder := rawRequest(env.handler, http.MethodPost, "/api/v1/auth/setup", body, credentials{}, "")
			statuses[index] = recorder.Code
		}(index)
	}
	claimants.Wait()

	created, conflicts := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != attempts-1 {
		t.Fatalf("setup statuses = %v", statuses)
	}
	var users int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth_users`).Scan(&users); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if users != 1 {
		t.Fatalf("setup created %d accounts", users)
	}
}

func TestRolesTokensAndScopedAccess(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	env.createUser(t, admin, "viewer", viewerPassword, []string{"viewer"})

	login := env.login(t, "viewer", viewerPassword)
	viewer := credentials{cookie: sessionCookie(t, login)}
	if viewer.cookie == "" {
		t.Fatal("viewer login returned no cookie")
	}

	me := decode[auth.User](t, env.do(t, http.MethodGet, "/api/v1/auth/me", nil, viewer))
	if !slices.Equal(me.Permissions, []string{"library.read", "downloads.read"}) {
		t.Fatalf("viewer permissions = %v", me.Permissions)
	}

	// Vertical access: the viewer role cannot write or manage.
	env.check(t, viewer, http.MethodGet, "/api/v1/movies", http.StatusOK)
	env.check(t, viewer, http.MethodGet, "/api/v1/movie-poster", http.StatusOK)
	env.check(t, viewer, http.MethodPost, "/api/v1/movies", http.StatusForbidden)
	env.check(t, viewer, http.MethodGet, "/metrics", http.StatusForbidden)
	env.check(t, viewer, http.MethodGet, "/api/v1/users", http.StatusForbidden)
	env.check(t, viewer, http.MethodGet, "/api/v1/settings", http.StatusForbidden)
	env.check(t, viewer, http.MethodGet, "/api/v1/permissions", http.StatusOK)

	// A token without an explicit scope inherits the owner's permissions.
	full := env.createToken(t, viewer, map[string]any{"name": "full"})
	if !strings.HasPrefix(full.Secret, "ctlr_") {
		t.Fatalf("token secret = %q", full.Secret)
	}
	fullCreds := credentials{bearer: full.Secret}
	env.check(t, fullCreds, http.MethodGet, "/api/v1/movies", http.StatusOK)
	env.check(t, fullCreds, http.MethodPost, "/api/v1/movies", http.StatusForbidden)
	env.check(t, fullCreds, http.MethodGet, "/api/v1/users", http.StatusForbidden)

	var stored []byte
	if err := env.pool.QueryRow(context.Background(), `SELECT token_hash FROM auth_tokens WHERE id = $1`, full.ID).Scan(&stored); err != nil {
		t.Fatalf("load token hash: %v", err)
	}
	sum := sha256.Sum256([]byte(full.Secret))
	if !bytes.Equal(stored, sum[:]) {
		t.Fatal("token secret is not stored as a SHA-256 hash")
	}
	listed := env.do(t, http.MethodGet, "/api/v1/auth/tokens", nil, viewer)
	if strings.Contains(listed.Body.String(), full.Secret) {
		t.Fatal("token list exposes the secret")
	}
	if len(decode[[]auth.Token](t, listed)) != 1 {
		t.Fatal("token list is missing the created token")
	}

	// Token scope is intersected with the owner's current grants.
	scoped := env.createToken(t, viewer, map[string]any{"name": "queue only", "permissions": []string{"downloads.read"}})
	env.check(t, credentials{bearer: scoped.Secret}, http.MethodGet, "/api/v1/movies", http.StatusForbidden)
	env.check(t, credentials{bearer: scoped.Secret}, http.MethodGet, "/api/v1/movie-poster", http.StatusForbidden)
	scopedMe := decode[auth.User](t, env.do(t, http.MethodGet, "/api/v1/auth/me", nil, credentials{bearer: scoped.Secret}))
	if !slices.Equal(scopedMe.Permissions, []string{"downloads.read"}) {
		t.Fatalf("token session permissions = %v", scopedMe.Permissions)
	}

	// Another account cannot read, update, or revoke this token.
	crossUpdate := env.do(t, http.MethodPut, "/api/v1/auth/tokens/"+scoped.ID, map[string]any{"name": "taken over"}, admin)
	if crossUpdate.Code != http.StatusNotFound {
		t.Fatalf("cross-account token update: status %d, body %s", crossUpdate.Code, crossUpdate.Body.String())
	}
	crossDelete := env.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+scoped.ID, nil, admin)
	if crossDelete.Code != http.StatusNotFound {
		t.Fatalf("cross-account token delete: status %d", crossDelete.Code)
	}
	env.check(t, credentials{bearer: scoped.Secret}, http.MethodGet, "/api/v1/auth/tokens", http.StatusForbidden)
	adminTokens := decode[[]auth.Token](t, env.do(t, http.MethodGet, "/api/v1/auth/tokens", nil, admin))
	if len(adminTokens) != 0 {
		t.Fatalf("admin token list leaked %d foreign tokens", len(adminTokens))
	}

	// A token cannot exceed the owner's permissions.
	exceeding := env.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "elevated", "permissions": []string{"library.write"}}, viewer)
	if exceeding.Code != http.StatusBadRequest {
		t.Fatalf("token escalation: status %d, body %s", exceeding.Code, exceeding.Body.String())
	}
	unknown := env.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "unknown", "permissions": []string{"admin.everything"}}, viewer)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown permission: status %d", unknown.Code)
	}

	// Expiry and deletion revoke access.
	expiring := env.createToken(t, viewer, map[string]any{"name": "expiring", "permissions": []string{"library.read"}, "expiresAt": time.Now().Add(time.Hour).UTC()})
	if _, err := env.pool.Exec(context.Background(), `UPDATE auth_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`, expiring.ID); err != nil {
		t.Fatalf("expire token: %v", err)
	}
	env.check(t, credentials{bearer: expiring.Secret}, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)

	revoke := env.do(t, http.MethodDelete, "/api/v1/auth/tokens/"+full.ID, nil, viewer)
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("delete token: status %d", revoke.Code)
	}
	env.check(t, fullCreds, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)
	env.check(t, viewer, http.MethodDelete, "/api/v1/auth/tokens/"+full.ID, http.StatusNotFound)

	// A stolen token cannot act for someone else's token or an unknown secret.
	env.check(t, credentials{bearer: "ctlr_not-a-real-token"}, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)
	env.check(t, viewer, http.MethodGet, "/api/v1/auth/tokens", http.StatusOK)

	// Malformed authorization headers never authenticate.
	for _, header := range []string{"Bearer", "Bearer ", "Basic YWRtaW46YWRtaW4=", "Token ctlr_x"} {
		recorder := rawRequest(env.handler, http.MethodGet, "/api/v1/movies", nil, credentials{}, header)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q: status %d", header, recorder.Code)
		}
	}
}

func TestDisabledAccountsAndPasswordResetRevokeCredentials(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	operator := env.createUser(t, admin, "operator", managerPass, []string{"operator"})

	login := env.login(t, "operator", managerPass)
	operatorCreds := credentials{cookie: sessionCookie(t, login)}
	token := env.createToken(t, operatorCreds, map[string]any{"name": "operator token"})
	env.check(t, operatorCreds, http.MethodGet, "/api/v1/movies", http.StatusOK)

	disable := env.do(t, http.MethodPut, "/api/v1/users/"+operator.ID, map[string]any{"active": false}, admin)
	if disable.Code != http.StatusOK || decode[auth.User](t, disable).Active {
		t.Fatalf("disable user: status %d, body %s", disable.Code, disable.Body.String())
	}
	env.check(t, operatorCreds, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)
	env.check(t, credentials{bearer: token.Secret}, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)
	var tokens int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth_tokens WHERE user_id = $1`, operator.ID).Scan(&tokens); err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if tokens != 0 {
		t.Fatalf("disabled account kept %d tokens", tokens)
	}

	enable := env.do(t, http.MethodPut, "/api/v1/users/"+operator.ID, map[string]any{"active": true}, admin)
	if enable.Code != http.StatusOK {
		t.Fatalf("enable user: status %d", enable.Code)
	}
	env.check(t, operatorCreds, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)
	fresh := env.login(t, "operator", managerPass)
	env.check(t, credentials{cookie: sessionCookie(t, fresh)}, http.MethodGet, "/api/v1/movies", http.StatusOK)

	// An administrator password reset revokes the account's sessions.
	reset := env.do(t, http.MethodPut, "/api/v1/users/"+operator.ID+"/password", map[string]string{"password": "rotated-lantern-99"}, admin)
	if reset.Code != http.StatusNoContent {
		t.Fatalf("password reset: status %d, body %s", reset.Code, reset.Body.String())
	}
	env.check(t, credentials{cookie: sessionCookie(t, fresh)}, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)
	if old := env.login(t, "operator", managerPass); old.Code != http.StatusBadRequest {
		t.Fatalf("old password still works: status %d", old.Code)
	}
	if current := env.login(t, "operator", "rotated-lantern-99"); current.Code != http.StatusOK {
		t.Fatalf("new password rejected: status %d", current.Code)
	}
}

func TestPasswordChangeRevokesSessions(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	second := env.login(t, "admin", adminPassword)
	other := credentials{cookie: sessionCookie(t, second)}

	wrong := env.do(t, http.MethodPut, "/api/v1/auth/password",
		map[string]string{"currentPassword": "not-the-password", "newPassword": "replacement-42"}, admin)
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("wrong current password: status %d", wrong.Code)
	}
	weak := env.do(t, http.MethodPut, "/api/v1/auth/password",
		map[string]string{"currentPassword": adminPassword, "newPassword": "short"}, admin)
	if weak.Code != http.StatusBadRequest {
		t.Fatalf("weak password: status %d", weak.Code)
	}

	changed := env.do(t, http.MethodPut, "/api/v1/auth/password",
		map[string]string{"currentPassword": adminPassword, "newPassword": "replacement-lantern-42"}, admin)
	if changed.Code != http.StatusNoContent {
		t.Fatalf("password change: status %d, body %s", changed.Code, changed.Body.String())
	}
	env.check(t, admin, http.MethodGet, "/api/v1/auth/me", http.StatusUnauthorized)
	env.check(t, other, http.MethodGet, "/api/v1/auth/me", http.StatusUnauthorized)
	if old := env.login(t, "admin", adminPassword); old.Code != http.StatusBadRequest {
		t.Fatalf("old password still works: status %d", old.Code)
	}
	if fresh := env.login(t, "admin", "replacement-lantern-42"); fresh.Code != http.StatusOK {
		t.Fatalf("new password rejected: status %d", fresh.Code)
	}
}

func TestLastAdministratorGuard(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	first := decode[auth.User](t, env.do(t, http.MethodGet, "/api/v1/auth/me", nil, admin))
	second := env.createUser(t, admin, "admin2", adminPassword, []string{"admin"})

	// A manager role without the admin role can manage accounts but is not an administrator.
	created := env.do(t, http.MethodPost, "/api/v1/roles", map[string]any{"name": "manager", "permissions": []string{"users.manage"}}, admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("create manager role: status %d, body %s", created.Code, created.Body.String())
	}
	managerRole := decode[auth.Role](t, created)
	env.createUser(t, admin, "manager", managerPass, []string{managerRole.ID})
	managerCreds := credentials{cookie: sessionCookie(t, env.login(t, "manager", managerPass))}

	// Two concurrent removals of the last two administrators cannot both succeed.
	statuses := make([]int, 2)
	var guards sync.WaitGroup
	for index, id := range []string{first.ID, second.ID} {
		guards.Add(1)
		go func(index int, id string) {
			defer guards.Done()
			body := []byte(`{"active":false}`)
			recorder := rawRequest(env.handler, http.MethodPut, "/api/v1/users/"+id, body, managerCreds, "")
			statuses[index] = recorder.Code
		}(index, id)
	}
	guards.Wait()
	successes, conflicts := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent administrator removal statuses = %v", statuses)
	}
	var activeAdmins int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth_users u WHERE u.active AND EXISTS (
		SELECT 1 FROM auth_user_roles ur WHERE ur.user_id = u.id AND ur.role_id = 'admin')`).Scan(&activeAdmins); err != nil {
		t.Fatalf("count administrators: %v", err)
	}
	if activeAdmins != 1 {
		t.Fatalf("active administrators = %d, want 1", activeAdmins)
	}

	// The surviving administrator cannot disable, demote, or delete itself.
	var survivor string
	if err := env.pool.QueryRow(context.Background(), `SELECT u.id FROM auth_users u WHERE u.active AND EXISTS (
		SELECT 1 FROM auth_user_roles ur WHERE ur.user_id = u.id AND ur.role_id = 'admin')`).Scan(&survivor); err != nil {
		t.Fatalf("find administrator: %v", err)
	}
	disable := env.do(t, http.MethodPut, "/api/v1/users/"+survivor, map[string]any{"active": false}, managerCreds)
	if disable.Code != http.StatusConflict {
		t.Fatalf("disable last administrator: status %d, body %s", disable.Code, disable.Body.String())
	}
	demote := env.do(t, http.MethodPut, "/api/v1/users/"+survivor, map[string]any{"roles": []string{"viewer"}}, managerCreds)
	if demote.Code != http.StatusConflict {
		t.Fatalf("demote last administrator: status %d", demote.Code)
	}
	if removed := env.do(t, http.MethodDelete, "/api/v1/users/"+survivor, nil, managerCreds); removed.Code != http.StatusConflict {
		t.Fatalf("delete last administrator: status %d", removed.Code)
	}
	if role := env.do(t, http.MethodDelete, "/api/v1/roles/admin", nil, managerCreds); role.Code != http.StatusForbidden {
		t.Fatalf("delete built-in admin role: status %d", role.Code)
	}
	env.check(t, managerCreds, http.MethodGet, "/api/v1/users", http.StatusOK)
}

func TestBuiltinAndCustomRoles(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)

	roles := decode[[]auth.Role](t, env.do(t, http.MethodGet, "/api/v1/roles", nil, admin))
	if len(roles) != 4 {
		t.Fatalf("built-in roles = %+v", roles)
	}
	for _, role := range roles {
		if !role.Builtin {
			t.Fatalf("role %s is not marked built-in", role.ID)
		}
	}
	for _, test := range []struct {
		id   string
		want []string
	}{
		{"admin", auth.PermissionCatalog},
		{"requester", []string{"library.read", "requests.read", "requests.write"}},
		{"viewer", []string{"library.read", "downloads.read"}},
	} {
		role, ok := findRole(roles, test.id)
		if !ok || !slices.Equal(role.Permissions, test.want) {
			t.Fatalf("role %s = %+v", test.id, role)
		}
	}
	if role, _ := findRole(roles, "operator"); slices.Contains(role.Permissions, "users.manage") {
		t.Fatal("operator must not manage users")
	}

	// Built-in roles are immutable.
	update := env.do(t, http.MethodPut, "/api/v1/roles/admin", map[string]any{"permissions": []string{"library.read"}}, admin)
	if update.Code != http.StatusForbidden {
		t.Fatalf("update built-in role: status %d", update.Code)
	}

	// Custom roles carry granular grants and can be granted to users.
	created := env.do(t, http.MethodPost, "/api/v1/roles", map[string]any{"name": "librarian", "permissions": []string{"library.write", "library.read"}}, admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("create role: status %d, body %s", created.Code, created.Body.String())
	}
	librarian := decode[auth.Role](t, created)
	if !slices.Equal(librarian.Permissions, []string{"library.read", "library.write"}) {
		t.Fatalf("librarian permissions = %v", librarian.Permissions)
	}
	unknown := env.do(t, http.MethodPost, "/api/v1/roles", map[string]any{"name": "ghost", "permissions": []string{"library.destroy"}}, admin)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown permission in role: status %d", unknown.Code)
	}
	duplicate := env.do(t, http.MethodPost, "/api/v1/roles", map[string]any{"name": "Librarian", "permissions": []string{}}, admin)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate role name: status %d", duplicate.Code)
	}

	member := env.createUser(t, admin, "librarian-user", viewerPassword, []string{librarian.ID})
	login := env.login(t, "librarian-user", viewerPassword)
	memberCreds := credentials{cookie: sessionCookie(t, login)}
	env.check(t, memberCreds, http.MethodPost, "/api/v1/movies", http.StatusOK)
	env.check(t, memberCreds, http.MethodGet, "/api/v1/movies", http.StatusOK)
	env.check(t, memberCreds, http.MethodGet, "/api/v1/users", http.StatusForbidden)

	// A role that is still assigned cannot be deleted.
	env.check(t, admin, http.MethodDelete, "/api/v1/roles/"+librarian.ID, http.StatusConflict)
	free := env.do(t, http.MethodPut, "/api/v1/users/"+member.ID, map[string]any{"roles": []string{}}, admin)
	if free.Code != http.StatusOK || len(decode[auth.User](t, free).Permissions) != 0 {
		t.Fatalf("clear roles: status %d, body %s", free.Code, free.Body.String())
	}
	env.check(t, admin, http.MethodDelete, "/api/v1/roles/"+librarian.ID, http.StatusNoContent)
	memberStillReads := credentials{cookie: sessionCookie(t, env.login(t, "librarian-user", viewerPassword))}
	env.check(t, memberStillReads, http.MethodPost, "/api/v1/movies", http.StatusForbidden)

	// Users cannot be assigned unknown roles.
	invalid := env.do(t, http.MethodPost, "/api/v1/users", map[string]any{"name": "nobody", "password": viewerPassword, "roles": []string{"missing-role"}}, admin)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("unknown role assignment: status %d", invalid.Code)
	}
}

func TestAuditTrailAndHook(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	env.createUser(t, admin, "viewer", viewerPassword, []string{"viewer"})
	env.login(t, "viewer", "wrong-password")

	// The exported hook records the acting principal from the request context.
	hook := rawRequest(env.handler, http.MethodGet, "/api/v1/operations/status", nil, credentials{cookie: sessionCookie(t, env.login(t, "viewer", viewerPassword))}, "")
	if hook.Code != http.StatusForbidden {
		t.Fatalf("viewer reached the monitoring hook: status %d", hook.Code)
	}
	env.check(t, admin, http.MethodGet, "/api/v1/operations/status", http.StatusOK)

	entries := decode[[]auth.AuditEntry](t, env.do(t, http.MethodGet, "/api/v1/audit", nil, admin))
	actions := map[string]auth.AuditEntry{}
	for _, entry := range entries {
		actions[entry.Action] = entry
	}
	for _, want := range []string{"setup", "user.create", "login", "test.hook"} {
		if _, ok := actions[want]; !ok {
			t.Fatalf("audit is missing %q: %+v", want, entries)
		}
	}
	if entry := actions["login"]; entry.Outcome != "failure" || entry.ActorName != "viewer" {
		t.Fatalf("failed login audit = %+v", entry)
	}
	if entry := actions["test.hook"]; entry.ActorName != "admin" || entry.Target != "target" || entry.Outcome != "acted" {
		t.Fatalf("hook audit = %+v", entry)
	}
	raw := env.do(t, http.MethodGet, "/api/v1/audit", nil, admin).Body.String()
	for _, secret := range []string{adminPassword, viewerPassword, "pbkdf2"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("audit response exposes %q", secret)
		}
	}

	viewerCreds := credentials{cookie: sessionCookie(t, env.login(t, "viewer", viewerPassword))}
	env.check(t, viewerCreds, http.MethodGet, "/api/v1/audit", http.StatusForbidden)

	limited := env.do(t, http.MethodGet, "/api/v1/audit?limit=1", nil, admin)
	if len(decode[[]auth.AuditEntry](t, limited)) != 1 {
		t.Fatalf("audit limit ignored: %s", limited.Body.String())
	}
	env.check(t, admin, http.MethodGet, "/api/v1/audit?limit=0", http.StatusBadRequest)
	env.check(t, admin, http.MethodGet, "/api/v1/audit?limit=none", http.StatusBadRequest)
}

func TestUnauthenticatedAndUnknownRoutes(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)

	for _, target := range []string{"/api/v1/users", "/api/v1/auth/me", "/api/v1/movie-poster", "/metrics",
		"/api/v1/subtitles", "/api/v1/definitely-unknown", "/api/v1/backups"} {
		env.check(t, credentials{}, http.MethodGet, target, http.StatusUnauthorized)
	}
	env.check(t, credentials{}, http.MethodGet, "/healthz", http.StatusOK)
	env.check(t, credentials{}, http.MethodGet, "/", http.StatusOK)
	env.check(t, credentials{}, http.MethodGet, "/apifoo", http.StatusOK)

	inactive := credentials{cookie: "not-a-real-session"}
	env.check(t, inactive, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)
	status := decode[statusResponse](t, env.do(t, http.MethodGet, "/api/v1/auth/status", nil, inactive))
	if status.Authenticated {
		t.Fatalf("invalid session reported as authenticated: %+v", status)
	}

	// Unknown API routes are denied by default, and mapped routes without a handler are 404.
	env.check(t, admin, http.MethodGet, "/api/v1/definitely-unknown", http.StatusForbidden)
	env.check(t, admin, http.MethodGet, "/api/v1/subtitles", http.StatusNotFound)
	env.check(t, admin, http.MethodGet, "/api/v1/settings", http.StatusNotFound)
	env.check(t, admin, http.MethodOptions, "/api/v1/settings", http.StatusMethodNotAllowed)
	env.check(t, admin, http.MethodGet, "/api/v1/auth/status", http.StatusOK)
}

func TestSecureCookieModes(t *testing.T) {
	plain := newEnv(t)
	_, setup := plain.setupAdmin(t)
	if cookie := responseCookie(setup); cookie == nil || cookie.Secure {
		t.Fatalf("plain cookie = %+v", cookie)
	}

	secure := newEnv(t, auth.WithSecureCookies(true))
	_, configured := secure.setupAdmin(t)
	if cookie := responseCookie(configured); cookie == nil || !cookie.Secure {
		t.Fatalf("configured secure cookie = %+v", cookie)
	}

	tlsEnv := newEnv(t)
	recorder := rawRequest(tlsEnv.handler, http.MethodPost, "/api/v1/auth/setup",
		[]byte(`{"name":"admin","password":"orbit-lantern-42"}`), credentials{tls: true}, "")
	if recorder.Code != http.StatusCreated {
		t.Fatalf("TLS setup: status %d", recorder.Code)
	}
	if cookie := responseCookie(recorder); cookie == nil || !cookie.Secure {
		t.Fatalf("direct TLS cookie = %+v", cookie)
	}
}

func TestRequireWrapper(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	env.createUser(t, admin, "viewer", viewerPassword, []string{"viewer"})
	viewer := credentials{cookie: sessionCookie(t, env.login(t, "viewer", viewerPassword))}

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/movies", env.service.Require(auth.PermLibraryWrite)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	handler := env.service.Middleware(mux)
	if recorder := rawRequest(handler, http.MethodGet, "/api/v1/movies", nil, viewer, ""); recorder.Code != http.StatusForbidden {
		t.Fatalf("viewer passed Require: status %d", recorder.Code)
	}
	if recorder := rawRequest(handler, http.MethodGet, "/api/v1/movies", nil, admin, ""); recorder.Code != http.StatusOK {
		t.Fatalf("admin failed Require: status %d", recorder.Code)
	}
}

// Account credentials require a cookie session, even when an API token has broad permissions.
func TestAPITokensCannotManageAccountCredentials(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	limited := env.createToken(t, admin, map[string]any{"name": "read only", "permissions": []string{"library.read"}})
	full := env.createToken(t, admin, map[string]any{"name": "everything"})
	limitedCreds := credentials{bearer: limited.Secret}
	fullCreds := credentials{bearer: full.Secret}

	for name, creds := range map[string]credentials{"limited": limitedCreds, "full": fullCreds} {
		env.check(t, creds, http.MethodGet, "/api/v1/auth/tokens", http.StatusForbidden)
		env.check(t, creds, http.MethodDelete, "/api/v1/auth/tokens/"+full.ID, http.StatusForbidden)
		env.check(t, creds, http.MethodPut, "/api/v1/auth/password", http.StatusForbidden)
		minted := env.do(t, http.MethodPost, "/api/v1/auth/tokens", map[string]any{"name": "minted", "permissions": []string{"library.read"}}, creds)
		if minted.Code != http.StatusForbidden {
			t.Fatalf("%s token minted a token: status %d, body %s", name, minted.Code, minted.Body.String())
		}
		renamed := env.do(t, http.MethodPut, "/api/v1/auth/tokens/"+limited.ID, map[string]any{"name": "renamed"}, creds)
		if renamed.Code != http.StatusForbidden {
			t.Fatalf("%s token changed a token: status %d", name, renamed.Code)
		}
	}

	// The refused requests changed nothing, and neither token was revoked.
	tokens := decode[[]auth.Token](t, env.do(t, http.MethodGet, "/api/v1/auth/tokens", nil, admin))
	if len(tokens) != 2 || tokens[0].Name == "renamed" || tokens[1].Name == "renamed" {
		t.Fatalf("tokens after refusal = %+v", tokens)
	}
	env.check(t, limitedCreds, http.MethodGet, "/api/v1/movies", http.StatusOK)
	env.check(t, limitedCreds, http.MethodGet, "/api/v1/movie-poster", http.StatusOK)
	env.check(t, fullCreds, http.MethodGet, "/api/v1/users", http.StatusOK)

	// Bearer callers keep the read-only account surfaces.
	for name, creds := range map[string]credentials{"limited": limitedCreds, "full": fullCreds} {
		env.check(t, creds, http.MethodGet, "/api/v1/auth/me", http.StatusOK)
		env.check(t, creds, http.MethodGet, "/api/v1/permissions", http.StatusOK)
		if name == "full" {
			env.check(t, creds, http.MethodGet, "/api/v1/auth/tokens", http.StatusForbidden)
		}
	}

	// The session that created the tokens still manages them.
	env.check(t, admin, http.MethodDelete, "/api/v1/auth/tokens/"+limited.ID, http.StatusNoContent)
	env.check(t, limitedCreds, http.MethodGet, "/api/v1/movies", http.StatusUnauthorized)
}

func TestRestoreRequiresAccountAdministrationAndSession(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	created := env.do(t, http.MethodPost, "/api/v1/roles", map[string]any{
		"name": "backup-manager", "permissions": []string{auth.PermBackupsManage},
	}, admin)
	if created.Code != http.StatusCreated {
		t.Fatalf("create backup role: %d", created.Code)
	}
	role := decode[auth.Role](t, created)
	env.createUser(t, admin, "backup-user", viewerPassword, []string{role.ID})
	manager := credentials{cookie: sessionCookie(t, env.login(t, "backup-user", viewerPassword))}
	env.check(t, manager, http.MethodGet, "/api/v1/operations/backups", http.StatusOK)
	env.check(t, manager, http.MethodPost, "/api/v1/operations/backups/example/restore", http.StatusForbidden)
	full := env.createToken(t, admin, map[string]any{"name": "admin-token"})
	env.check(t, credentials{bearer: full.Secret}, http.MethodPost, "/api/v1/operations/backups/example/restore", http.StatusForbidden)
	env.check(t, admin, http.MethodPost, "/api/v1/operations/backups/example/restore", http.StatusOK)
}

func TestGenericMutationAudit(t *testing.T) {
	env := newEnv(t)
	admin, _ := env.setupAdmin(t)
	before := len(decode[[]auth.AuditEntry](t, env.do(t, http.MethodGet, "/api/v1/audit?limit=200", nil, admin)))

	accepted := env.do(t, http.MethodPost, "/api/v1/downloads?token=super-secret", map[string]string{"releaseId": "secret-release"}, admin)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("download create: status %d", accepted.Code)
	}
	env.check(t, admin, http.MethodGet, "/api/v1/movies", http.StatusOK)
	if missing := env.do(t, http.MethodDelete, "/api/v1/torrents/abcdef", nil, admin); missing.Code != http.StatusNotFound {
		t.Fatalf("torrent delete: status %d", missing.Code)
	}

	entries := decode[[]auth.AuditEntry](t, env.do(t, http.MethodGet, "/api/v1/audit?limit=200", nil, admin))
	if len(entries) != before+2 {
		t.Fatalf("audit recorded %d of %d mutations: %+v", len(entries)-before, 2, entries[:4])
	}
	recorded := map[string]auth.AuditEntry{}
	for _, entry := range entries {
		recorded[entry.Action+" "+entry.Target] = entry
	}
	if entry, ok := recorded["POST /api/v1/downloads"]; !ok || entry.Outcome != "202" || entry.ActorName != "admin" {
		t.Fatalf("download audit = %+v", entry)
	}
	if entry, ok := recorded["DELETE /api/v1/torrents/{id}"]; !ok || entry.Outcome != "404" {
		t.Fatalf("torrent audit = %+v", entry)
	}
	raw := env.do(t, http.MethodGet, "/api/v1/audit?limit=200", nil, admin).Body.String()
	for _, secret := range []string{"super-secret", "secret-release", "abcdef"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("audit exposes %q", secret)
		}
	}

	// Routes with their own audit events are not double-recorded.
	env.createUser(t, admin, "viewer", viewerPassword, []string{"viewer"})
	entries = decode[[]auth.AuditEntry](t, env.do(t, http.MethodGet, "/api/v1/audit?limit=200", nil, admin))
	creates := 0
	for _, entry := range entries {
		if entry.Action == "user.create" {
			creates++
		}
		if entry.Action == "POST" && entry.Target == "/api/v1/users" {
			t.Fatal("the users route was audited twice")
		}
	}
	if creates != 1 {
		t.Fatalf("user.create was audited %d times", creates)
	}
}

// TestNewRequiresMigratedSchema documents the wiring order: downloads migrations first.
func TestNewRequiresMigratedSchema(t *testing.T) {
	pool := authSchemaPool(t)
	if _, err := auth.New(context.Background(), pool); err == nil {
		t.Fatal("auth.New accepted a schema without the auth tables")
	} else if !strings.Contains(err.Error(), "migrations") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestServiceStartsAfterDownloadsMigrations exercises the real startup path.
func TestServiceStartsAfterDownloadsMigrations(t *testing.T) {
	pool := authSchemaPool(t)
	manager, err := downloads.New(context.Background(), pool, downloads.Config{Directory: t.TempDir()})
	if err != nil {
		t.Fatalf("downloads.New: %v", err)
	}
	_ = manager
	service, err := auth.New(context.Background(), pool)
	if err != nil {
		t.Fatalf("auth.New after the shared migrations: %v", err)
	}
	defer service.Close()

	mux := http.NewServeMux()
	service.Register(mux)
	recorder := rawRequest(service.Middleware(mux), http.MethodGet, "/api/v1/auth/status", nil, credentials{}, "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"setupRequired":true`) {
		t.Fatalf("status after migrations: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestBackgroundLifecycleRestarts(t *testing.T) {
	env := newEnv(t)
	for range 50 {
		env.service.Start(context.Background())
		env.service.Close()
	}
}

func findRole(roles []auth.Role, id string) (auth.Role, bool) {
	for _, role := range roles {
		if role.ID == id {
			return role, true
		}
	}
	return auth.Role{}, false
}
