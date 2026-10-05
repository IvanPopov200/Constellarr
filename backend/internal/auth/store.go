package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Distinct transaction-scoped lock keys for setup, administrator guards, and role seeding.
const (
	schemaLock int64 = 0x436F6E7374656C6C + 7
	setupLock  int64 = schemaLock + 1
	adminGuard int64 = schemaLock + 2
)

// Sentinels mark expected outcomes; apiError adds the client status and message.
var (
	ErrNotFound      = errors.New("auth: not found")
	ErrConflict      = errors.New("auth: conflict")
	ErrInvalid       = errors.New("auth: invalid input")
	ErrForbidden     = errors.New("auth: forbidden")
	ErrLastAdmin     = errors.New("auth: the last active administrator cannot be removed")
	ErrSetupComplete = errors.New("auth: setup has already been completed")
)

var (
	errDatabase         = errors.New("database operation failed")
	errNotAuthenticated = errors.New("auth: invalid credentials")
)

// apiError carries a client-safe status and message; anything else becomes a 500.
type apiError struct {
	status  int
	message string
	cause   error
}

func (e *apiError) Error() string { return e.message }
func (e *apiError) Unwrap() error { return e.cause }

func clientError(status int, message string, cause error) error {
	return &apiError{status: status, message: message, cause: cause}
}

// User permissions are the union of the grants of its roles.
type User struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Active      bool       `json:"active"`
	Roles       []string   `json:"roles"`
	Permissions []string   `json:"permissions"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastLoginAt *time.Time `json:"lastLoginAt"`
}

type Role struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Builtin     bool     `json:"builtin"`
	Permissions []string `json:"permissions"`
}

// Token hides the secret, which is only returned at creation.
type Token struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Permissions []string   `json:"permissions"`
	CreatedAt   time.Time  `json:"createdAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt"`
}

type AuditEntry struct {
	At        time.Time `json:"at"`
	ActorID   string    `json:"actorId"`
	ActorName string    `json:"actorName"`
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	Outcome   string    `json:"outcome"`
}

type querier interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type rowScanner interface {
	Scan(dest ...any) error
}

const (
	userRolesSQL = `coalesce((SELECT array_agg(r.name ORDER BY r.name) FROM auth_user_roles ur
		JOIN auth_roles r ON r.id = ur.role_id WHERE ur.user_id = u.id), '{}')`
	userPermissionsSQL = `coalesce((SELECT array_agg(DISTINCT rp.permission ORDER BY rp.permission) FROM auth_user_roles ur
		JOIN auth_role_permissions rp ON rp.role_id = ur.role_id WHERE ur.user_id = u.id), '{}')`
	userColumns = `u.id, u.name, u.active, u.created_at, u.last_login_at, ` + userRolesSQL + `, ` + userPermissionsSQL
)

// dbFailure keeps database details out of caller-visible errors.
func dbFailure(op string, cause error) error {
	var pgErr *pgconn.PgError
	if errors.As(cause, &pgErr) {
		return fmt.Errorf("auth: %s: database error %s: %w", op, pgErr.Code, errDatabase)
	}
	return fmt.Errorf("auth: %s: %w", op, errDatabase)
}

func uniqueViolation(cause error) bool {
	var pgErr *pgconn.PgError
	return errors.As(cause, &pgErr) && pgErr.Code == "23505"
}

func scanUser(row rowScanner) (User, error) {
	var user User
	if err := row.Scan(&user.ID, &user.Name, &user.Active, &user.CreatedAt, &user.LastLoginAt,
		&user.Roles, &user.Permissions); err != nil {
		return User{}, err
	}
	user.Roles = nonNil(user.Roles)
	user.Permissions = orderPermissions(nonNil(user.Permissions))
	return user, nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// verifySchema fails fast when the shared downloads migration chain has not created the auth tables.
func (s *Service) verifySchema(ctx context.Context) error {
	var missing bool
	err := s.pool.QueryRow(ctx, `SELECT to_regclass('auth_users') IS NULL
		OR to_regclass('auth_roles') IS NULL
		OR to_regclass('auth_role_permissions') IS NULL
		OR to_regclass('auth_user_roles') IS NULL
		OR to_regclass('auth_sessions') IS NULL
		OR to_regclass('auth_tokens') IS NULL
		OR to_regclass('auth_audit') IS NULL`).Scan(&missing)
	if err != nil {
		return dbFailure("verify schema", err)
	}
	if missing {
		return errors.New("auth: the auth schema is missing; run the downloads migrations (007_auth.sql) first")
	}
	return nil
}

// seedBuiltinRoles recreates the fixed permission sets of built-in roles.
func seedBuiltinRoles(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return dbFailure("seed roles", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, schemaLock); err != nil {
		return dbFailure("seed roles", err)
	}
	for _, role := range builtinRoles {
		if _, err := tx.Exec(ctx, `INSERT INTO auth_roles (id, name, name_key, builtin) VALUES ($1, $1, $1, true)
			ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, name_key = EXCLUDED.name_key, builtin = true`, role.id); err != nil {
			return dbFailure("seed roles", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_role_permissions WHERE role_id = $1`, role.id); err != nil {
			return dbFailure("seed roles", err)
		}
		for _, permission := range role.permissions {
			if _, err := tx.Exec(ctx, `INSERT INTO auth_role_permissions (role_id, permission) VALUES ($1, $2)`, role.id, permission); err != nil {
				return dbFailure("seed roles", err)
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *Service) setupRequired(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_users)`).Scan(&exists); err != nil {
		return false, dbFailure("check setup", err)
	}
	return !exists, nil
}

// setup atomically claims first-run setup and returns the initial administrator session.
func (s *Service) setup(ctx context.Context, name, password, clientIP, userAgent string) (User, string, time.Time, error) {
	if err := validName(name); err != nil {
		return User{}, "", time.Time{}, err
	}
	if err := validPassword(password); err != nil {
		return User{}, "", time.Time{}, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, "", time.Time{}, dbFailure("setup", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, setupLock); err != nil {
		return User{}, "", time.Time{}, dbFailure("setup", err)
	}
	var claimed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_users)`).Scan(&claimed); err != nil {
		return User{}, "", time.Time{}, dbFailure("setup", err)
	}
	if claimed {
		return User{}, "", time.Time{}, clientError(409, "setup has already been completed", ErrSetupComplete)
	}
	id := rand.Text()
	if _, err := tx.Exec(ctx, `INSERT INTO auth_users (id, name, name_key, password_hash, active) VALUES ($1, $2, $3, $4, true)`,
		id, name, strings.ToLower(name), hash); err != nil {
		return User{}, "", time.Time{}, dbFailure("setup", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO auth_user_roles (user_id, role_id) VALUES ($1, $2)`, id, roleAdmin); err != nil {
		return User{}, "", time.Time{}, dbFailure("setup", err)
	}
	secret, expires, err := insertSession(ctx, tx, id, clientIP, userAgent)
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	if err := insertAudit(ctx, tx, id, name, "setup", name, "success"); err != nil {
		return User{}, "", time.Time{}, dbFailure("setup", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, "", time.Time{}, dbFailure("setup", err)
	}
	user, err := s.userByID(ctx, s.pool, id)
	if err != nil {
		return User{}, "", time.Time{}, err
	}
	return user, secret, expires, nil
}

func (s *Service) userByID(ctx context.Context, q querier, id string) (User, error) {
	user, err := scanUser(q.QueryRow(ctx, `SELECT `+userColumns+` FROM auth_users u WHERE u.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, clientError(404, "user not found", ErrNotFound)
	}
	if err != nil {
		return User{}, dbFailure("load user", err)
	}
	return user, nil
}

func (s *Service) accountByName(ctx context.Context, name string) (User, string, error) {
	var (
		user User
		hash string
	)
	err := s.pool.QueryRow(ctx, `SELECT `+userColumns+`, u.password_hash FROM auth_users u WHERE u.name_key = $1`,
		strings.ToLower(name)).Scan(&user.ID, &user.Name, &user.Active, &user.CreatedAt, &user.LastLoginAt,
		&user.Roles, &user.Permissions, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", clientError(404, "account not found", ErrNotFound)
	}
	if err != nil {
		return User{}, "", dbFailure("load account", err)
	}
	user.Roles = nonNil(user.Roles)
	user.Permissions = orderPermissions(nonNil(user.Permissions))
	return user, hash, nil
}

func (s *Service) listUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+` FROM auth_users u ORDER BY lower(u.name)`)
	if err != nil {
		return nil, dbFailure("list users", err)
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, dbFailure("list users", err)
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, dbFailure("list users", err)
	}
	return users, nil
}

func (s *Service) createUser(ctx context.Context, name, password string, active bool, roleIDs []string) (User, error) {
	if err := validName(name); err != nil {
		return User{}, err
	}
	if err := validPassword(password); err != nil {
		return User{}, err
	}
	roleIDs = normalizeIDs(roleIDs)
	hash, err := hashPassword(password)
	if err != nil {
		return User{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, dbFailure("create user", err)
	}
	defer tx.Rollback(ctx)
	if err := ensureRoles(ctx, tx, roleIDs); err != nil {
		return User{}, err
	}
	id := rand.Text()
	if _, err := tx.Exec(ctx, `INSERT INTO auth_users (id, name, name_key, password_hash, active) VALUES ($1, $2, $3, $4, $5)`,
		id, name, strings.ToLower(name), hash, active); err != nil {
		if uniqueViolation(err) {
			return User{}, clientError(409, "a user with that name already exists", ErrConflict)
		}
		return User{}, dbFailure("create user", err)
	}
	if err := replaceUserRoles(ctx, tx, id, roleIDs); err != nil {
		return User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, dbFailure("create user", err)
	}
	return s.userByID(ctx, s.pool, id)
}

// UserUpdate describes an account change; nil fields keep their current value.
type UserUpdate struct {
	Name   *string
	Active *bool
	Roles  *[]string
}

func (s *Service) updateUser(ctx context.Context, id string, update UserUpdate) (User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return User{}, dbFailure("update user", err)
	}
	defer tx.Rollback(ctx)
	current, err := s.userByID(ctx, tx, id)
	if err != nil {
		return User{}, err
	}
	name := current.Name
	if update.Name != nil {
		if err := validName(*update.Name); err != nil {
			return User{}, err
		}
		name = *update.Name
	}
	roleIDs, err := userRoleIDs(ctx, tx, id)
	if err != nil {
		return User{}, err
	}
	if update.Roles != nil {
		roleIDs = normalizeIDs(*update.Roles)
		if err := ensureRoles(ctx, tx, roleIDs); err != nil {
			return User{}, err
		}
	}
	active := current.Active
	if update.Active != nil {
		active = *update.Active
	}
	if err := guardLastAdmin(ctx, tx, id, active && contains(roleIDs, roleAdmin)); err != nil {
		return User{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_users SET name = $2, name_key = $3, active = $4, updated_at = now() WHERE id = $1`,
		id, name, strings.ToLower(name), active); err != nil {
		if uniqueViolation(err) {
			return User{}, clientError(409, "a user with that name already exists", ErrConflict)
		}
		return User{}, dbFailure("update user", err)
	}
	if update.Roles != nil {
		if err := replaceUserRoles(ctx, tx, id, roleIDs); err != nil {
			return User{}, err
		}
	}
	if !active {
		if err := revokeUserCredentials(ctx, tx, id); err != nil {
			return User{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, dbFailure("update user", err)
	}
	return s.userByID(ctx, s.pool, id)
}

func (s *Service) deleteUser(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbFailure("delete user", err)
	}
	defer tx.Rollback(ctx)
	if err := guardLastAdmin(ctx, tx, id, false); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM auth_users WHERE id = $1`, id)
	if err != nil {
		return dbFailure("delete user", err)
	}
	if tag.RowsAffected() == 0 {
		return clientError(404, "user not found", ErrNotFound)
	}
	return tx.Commit(ctx)
}

// setUserPassword replaces the password hash and revokes the account's sessions.
func (s *Service) setUserPassword(ctx context.Context, id, password string) error {
	if err := validPassword(password); err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbFailure("set password", err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE auth_users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
	if err != nil {
		return dbFailure("set password", err)
	}
	if tag.RowsAffected() == 0 {
		return clientError(404, "user not found", ErrNotFound)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id = $1`, id); err != nil {
		return dbFailure("set password", err)
	}
	return tx.Commit(ctx)
}

func userRoleIDs(ctx context.Context, q querier, userID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT role_id FROM auth_user_roles WHERE user_id = $1 ORDER BY role_id`, userID)
	if err != nil {
		return nil, dbFailure("load user roles", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, dbFailure("load user roles", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, dbFailure("load user roles", err)
	}
	return ids, nil
}

func ensureRoles(ctx context.Context, q querier, roleIDs []string) error {
	if len(roleIDs) == 0 {
		return nil
	}
	var found int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM auth_roles WHERE id = ANY($1)`, roleIDs).Scan(&found); err != nil {
		return dbFailure("load roles", err)
	}
	if found != len(roleIDs) {
		return clientError(400, "unknown role", ErrInvalid)
	}
	return nil
}

func replaceUserRoles(ctx context.Context, tx pgx.Tx, userID string, roleIDs []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM auth_user_roles WHERE user_id = $1`, userID); err != nil {
		return dbFailure("update user roles", err)
	}
	if len(roleIDs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO auth_user_roles (user_id, role_id) SELECT $1, unnest($2::text[])`, userID, roleIDs); err != nil {
		return dbFailure("update user roles", err)
	}
	return nil
}

// guardLastAdmin rejects changes that would leave no active administrator.
func guardLastAdmin(ctx context.Context, tx pgx.Tx, userID string, nextIsActiveAdmin bool) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, adminGuard); err != nil {
		return dbFailure("guard administrator", err)
	}
	var targetIsActiveAdmin bool
	err := tx.QueryRow(ctx, `SELECT u.active AND EXISTS (
		SELECT 1 FROM auth_user_roles ur WHERE ur.user_id = u.id AND ur.role_id = $2)
		FROM auth_users u WHERE u.id = $1`, userID, roleAdmin).Scan(&targetIsActiveAdmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return clientError(404, "user not found", ErrNotFound)
	}
	if err != nil {
		return dbFailure("guard administrator", err)
	}
	if !targetIsActiveAdmin || nextIsActiveAdmin {
		return nil
	}
	var activeAdmins int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_users u WHERE u.active AND EXISTS (
		SELECT 1 FROM auth_user_roles ur WHERE ur.user_id = u.id AND ur.role_id = $1)`, roleAdmin).Scan(&activeAdmins); err != nil {
		return dbFailure("guard administrator", err)
	}
	if activeAdmins <= 1 {
		return clientError(409, "the last active administrator cannot be removed", ErrLastAdmin)
	}
	return nil
}

func revokeUserCredentials(ctx context.Context, tx pgx.Tx, userID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM auth_sessions WHERE user_id = $1`, userID); err != nil {
		return dbFailure("revoke sessions", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_tokens WHERE user_id = $1`, userID); err != nil {
		return dbFailure("revoke tokens", err)
	}
	return nil
}

func insertSession(ctx context.Context, tx pgx.Tx, userID, clientIP, userAgent string) (string, time.Time, error) {
	secret, err := newSecret("")
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().Add(sessionTTL)
	if _, err := tx.Exec(ctx, `INSERT INTO auth_sessions (id, user_id, token_hash, expires_at, client_ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6)`, rand.Text(), userID, tokenHash(secret), expires,
		truncateRunes(clientIP, 45), truncateRunes(userAgent, maxUserAgentRunes)); err != nil {
		return "", time.Time{}, dbFailure("create session", err)
	}
	return secret, expires, nil
}

func (s *Service) createUserSession(ctx context.Context, userID, clientIP, userAgent string) (string, time.Time, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", time.Time{}, dbFailure("create session", err)
	}
	defer tx.Rollback(ctx)
	secret, expires, err := insertSession(ctx, tx, userID, clientIP, userAgent)
	if err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_users SET last_login_at = now() WHERE id = $1`, userID); err != nil {
		return "", time.Time{}, dbFailure("create session", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", time.Time{}, dbFailure("create session", err)
	}
	return secret, expires, nil
}

func (s *Service) sessionPrincipal(ctx context.Context, secret string) (Principal, error) {
	var (
		principal Principal
		active    bool
	)
	err := s.pool.QueryRow(ctx, `SELECT s.id, u.id, u.name, u.active, `+userRolesSQL+`, `+userPermissionsSQL+`
		FROM auth_sessions s JOIN auth_users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash(secret)).Scan(
		&principal.SessionID, &principal.UserID, &principal.Name, &active, &principal.Roles, &principal.Permissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, errNotAuthenticated
	}
	if err != nil {
		return Principal{}, dbFailure("load session", err)
	}
	if !active {
		return Principal{}, errNotAuthenticated
	}
	principal.Roles = nonNil(principal.Roles)
	principal.Permissions = orderPermissions(nonNil(principal.Permissions))
	_, _ = s.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = now() WHERE id = $1 AND last_seen_at < now() - interval '5 minutes'`,
		principal.SessionID)
	return principal, nil
}

func (s *Service) deleteSession(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE id = $1`, id); err != nil {
		return dbFailure("delete session", err)
	}
	return nil
}

func (s *Service) deleteSessionBySecret(ctx context.Context, secret string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE token_hash = $1`, tokenHash(secret)); err != nil {
		return dbFailure("delete session", err)
	}
	return nil
}

func (s *Service) createToken(ctx context.Context, userID, name string, permissions []string, expiresAt *time.Time) (Token, string, error) {
	if err := validTokenName(name); err != nil {
		return Token{}, "", err
	}
	user, err := s.userByID(ctx, s.pool, userID)
	if err != nil {
		return Token{}, "", err
	}
	if permissions == nil {
		permissions = user.Permissions
	}
	normalized, err := normalizePermissions(permissions)
	if err != nil {
		return Token{}, "", err
	}
	if err := withinPermissions(normalized, user.Permissions); err != nil {
		return Token{}, "", err
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return Token{}, "", clientError(400, "token expiry must be in the future", ErrInvalid)
	}
	secret, err := newSecret(tokenPrefix)
	if err != nil {
		return Token{}, "", err
	}
	id := rand.Text()
	if _, err := s.pool.Exec(ctx, `INSERT INTO auth_tokens (id, user_id, name, token_hash, permissions, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, id, userID, name, tokenHash(secret), normalized, expiresAt); err != nil {
		return Token{}, "", dbFailure("create token", err)
	}
	token, err := s.tokenByID(ctx, s.pool, userID, id)
	if err != nil {
		return Token{}, "", err
	}
	return token, secret, nil
}

// withinPermissions rejects token scopes that exceed the owner's current grants.
func withinPermissions(requested, held []string) error {
	granted := make(map[string]bool, len(held))
	for _, permission := range held {
		granted[permission] = true
	}
	for _, permission := range requested {
		if !granted[permission] {
			return clientError(400, "a token cannot exceed your own permissions", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) tokenByID(ctx context.Context, q querier, userID, id string) (Token, error) {
	var token Token
	err := q.QueryRow(ctx, `SELECT id, name, permissions, created_at, expires_at, last_used_at
		FROM auth_tokens WHERE id = $1 AND user_id = $2`, id, userID).Scan(
		&token.ID, &token.Name, &token.Permissions, &token.CreatedAt, &token.ExpiresAt, &token.LastUsedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, clientError(404, "token not found", ErrNotFound)
	}
	if err != nil {
		return Token{}, dbFailure("load token", err)
	}
	token.Permissions = orderPermissions(nonNil(token.Permissions))
	return token, nil
}

func (s *Service) listTokens(ctx context.Context, userID string) ([]Token, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, permissions, created_at, expires_at, last_used_at
		FROM auth_tokens WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, dbFailure("list tokens", err)
	}
	defer rows.Close()
	tokens := []Token{}
	for rows.Next() {
		var token Token
		if err := rows.Scan(&token.ID, &token.Name, &token.Permissions, &token.CreatedAt, &token.ExpiresAt, &token.LastUsedAt); err != nil {
			return nil, dbFailure("list tokens", err)
		}
		token.Permissions = orderPermissions(nonNil(token.Permissions))
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, dbFailure("list tokens", err)
	}
	return tokens, nil
}

func (s *Service) updateToken(ctx context.Context, userID, id string, name *string, permissions *[]string) (Token, error) {
	token, err := s.tokenByID(ctx, s.pool, userID, id)
	if err != nil {
		return Token{}, err
	}
	if name != nil {
		if err := validTokenName(*name); err != nil {
			return Token{}, err
		}
		token.Name = *name
	}
	if permissions != nil {
		user, err := s.userByID(ctx, s.pool, userID)
		if err != nil {
			return Token{}, err
		}
		normalized, err := normalizePermissions(*permissions)
		if err != nil {
			return Token{}, err
		}
		if err := withinPermissions(normalized, user.Permissions); err != nil {
			return Token{}, err
		}
		token.Permissions = normalized
	}
	if _, err := s.pool.Exec(ctx, `UPDATE auth_tokens SET name = $3, permissions = $4 WHERE id = $1 AND user_id = $2`,
		id, userID, token.Name, token.Permissions); err != nil {
		return Token{}, dbFailure("update token", err)
	}
	return s.tokenByID(ctx, s.pool, userID, id)
}

func (s *Service) deleteToken(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM auth_tokens WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return dbFailure("delete token", err)
	}
	if tag.RowsAffected() == 0 {
		return clientError(404, "token not found", ErrNotFound)
	}
	return nil
}

func (s *Service) tokenPrincipal(ctx context.Context, secret string) (Principal, error) {
	var (
		principal Principal
		scope     []string
		active    bool
	)
	err := s.pool.QueryRow(ctx, `SELECT t.id, u.id, u.name, u.active, t.permissions, `+userRolesSQL+`, `+userPermissionsSQL+`
		FROM auth_tokens t JOIN auth_users u ON u.id = t.user_id
		WHERE t.token_hash = $1 AND (t.expires_at IS NULL OR t.expires_at > now())`, tokenHash(secret)).Scan(
		&principal.TokenID, &principal.UserID, &principal.Name, &active, &scope, &principal.Roles, &principal.Permissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, errNotAuthenticated
	}
	if err != nil {
		return Principal{}, dbFailure("load token", err)
	}
	if !active {
		return Principal{}, errNotAuthenticated
	}
	principal.Roles = nonNil(principal.Roles)
	principal.Permissions = orderPermissions(intersectPermissions(nonNil(principal.Permissions), scope))
	_, _ = s.pool.Exec(ctx, `UPDATE auth_tokens SET last_used_at = now() WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '5 minutes')`,
		principal.TokenID)
	return principal, nil
}

func (s *Service) listRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `SELECT r.id, r.name, r.builtin, coalesce((
		SELECT array_agg(rp.permission ORDER BY rp.permission) FROM auth_role_permissions rp WHERE rp.role_id = r.id), '{}')
		FROM auth_roles r ORDER BY r.builtin DESC, lower(r.name)`)
	if err != nil {
		return nil, dbFailure("list roles", err)
	}
	defer rows.Close()
	roles := []Role{}
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.Name, &role.Builtin, &role.Permissions); err != nil {
			return nil, dbFailure("list roles", err)
		}
		role.Permissions = orderPermissions(nonNil(role.Permissions))
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, dbFailure("list roles", err)
	}
	return roles, nil
}

func (s *Service) roleByID(ctx context.Context, q querier, id string) (Role, error) {
	var role Role
	err := q.QueryRow(ctx, `SELECT r.id, r.name, r.builtin, coalesce((
		SELECT array_agg(rp.permission ORDER BY rp.permission) FROM auth_role_permissions rp WHERE rp.role_id = r.id), '{}')
		FROM auth_roles r WHERE r.id = $1`, id).Scan(&role.ID, &role.Name, &role.Builtin, &role.Permissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return Role{}, clientError(404, "role not found", ErrNotFound)
	}
	if err != nil {
		return Role{}, dbFailure("load role", err)
	}
	role.Permissions = orderPermissions(nonNil(role.Permissions))
	return role, nil
}

func (s *Service) createRole(ctx context.Context, name string, permissions []string) (Role, error) {
	if err := validRoleName(name); err != nil {
		return Role{}, err
	}
	normalized, err := normalizePermissions(permissions)
	if err != nil {
		return Role{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Role{}, dbFailure("create role", err)
	}
	defer tx.Rollback(ctx)
	id := rand.Text()
	if _, err := tx.Exec(ctx, `INSERT INTO auth_roles (id, name, name_key, builtin) VALUES ($1, $2, $3, false)`,
		id, name, strings.ToLower(name)); err != nil {
		if uniqueViolation(err) {
			return Role{}, clientError(409, "a role with that name already exists", ErrConflict)
		}
		return Role{}, dbFailure("create role", err)
	}
	for _, permission := range normalized {
		if _, err := tx.Exec(ctx, `INSERT INTO auth_role_permissions (role_id, permission) VALUES ($1, $2)`, id, permission); err != nil {
			return Role{}, dbFailure("create role", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Role{}, dbFailure("create role", err)
	}
	return s.roleByID(ctx, s.pool, id)
}

func (s *Service) updateRole(ctx context.Context, id string, name *string, permissions *[]string) (Role, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Role{}, dbFailure("update role", err)
	}
	defer tx.Rollback(ctx)
	role, err := s.roleByID(ctx, tx, id)
	if err != nil {
		return Role{}, err
	}
	if role.Builtin {
		return Role{}, clientError(403, "built-in roles cannot be changed", ErrForbidden)
	}
	nextName := role.Name
	if name != nil {
		if err := validRoleName(*name); err != nil {
			return Role{}, err
		}
		nextName = *name
	}
	nextPermissions := role.Permissions
	if permissions != nil {
		if nextPermissions, err = normalizePermissions(*permissions); err != nil {
			return Role{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE auth_roles SET name = $2, name_key = $3, updated_at = now() WHERE id = $1`,
		id, nextName, strings.ToLower(nextName)); err != nil {
		if uniqueViolation(err) {
			return Role{}, clientError(409, "a role with that name already exists", ErrConflict)
		}
		return Role{}, dbFailure("update role", err)
	}
	if permissions != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM auth_role_permissions WHERE role_id = $1`, id); err != nil {
			return Role{}, dbFailure("update role", err)
		}
		for _, permission := range nextPermissions {
			if _, err := tx.Exec(ctx, `INSERT INTO auth_role_permissions (role_id, permission) VALUES ($1, $2)`, id, permission); err != nil {
				return Role{}, dbFailure("update role", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Role{}, dbFailure("update role", err)
	}
	return s.roleByID(ctx, s.pool, id)
}

func (s *Service) deleteRole(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return dbFailure("delete role", err)
	}
	defer tx.Rollback(ctx)
	role, err := s.roleByID(ctx, tx, id)
	if err != nil {
		return err
	}
	if role.Builtin {
		return clientError(403, "built-in roles cannot be deleted", ErrForbidden)
	}
	var assigned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth_user_roles WHERE role_id = $1)`, id).Scan(&assigned); err != nil {
		return dbFailure("delete role", err)
	}
	if assigned {
		return clientError(409, "the role is assigned to users", ErrConflict)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM auth_roles WHERE id = $1`, id); err != nil {
		return dbFailure("delete role", err)
	}
	return tx.Commit(ctx)
}

func insertAudit(ctx context.Context, q querier, actorID, actorName, action, target, outcome string) error {
	_, err := q.Exec(ctx, `INSERT INTO auth_audit (actor_id, actor_name, action, target, outcome) VALUES ($1, $2, $3, $4, $5)`,
		truncateRunes(actorID, 64), truncateRunes(actorName, 64), truncateRunes(action, 64), truncateRunes(target, 128), truncateRunes(outcome, 32))
	return err
}

// recordAudit writes an audit row even when the request context is already canceled.
func (s *Service) recordAudit(ctx context.Context, actorID, actorName, action, target, outcome string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	if err := insertAudit(ctx, s.pool, actorID, actorName, action, target, outcome); err != nil {
		slog.Warn("auth: audit write failed", "error", err)
	}
}

func (s *Service) listAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit < 1 || limit > maxAuditLimit {
		limit = defaultAuditLimit
	}
	rows, err := s.pool.Query(ctx, `SELECT at, actor_id, actor_name, action, target, outcome
		FROM auth_audit ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, dbFailure("list audit", err)
	}
	defer rows.Close()
	entries := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		if err := rows.Scan(&entry.At, &entry.ActorID, &entry.ActorName, &entry.Action, &entry.Target, &entry.Outcome); err != nil {
			return nil, dbFailure("list audit", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, dbFailure("list audit", err)
	}
	return entries, nil
}

// cleanup drops expired sessions and long-expired tokens.
func (s *Service) cleanup(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM auth_sessions WHERE expires_at < now()`); err != nil && ctx.Err() == nil {
		slog.Warn("auth: session cleanup failed", "error", dbFailure("cleanup sessions", err))
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM auth_tokens WHERE expires_at IS NOT NULL AND expires_at < now() - interval '30 days'`); err != nil && ctx.Err() == nil {
		slog.Warn("auth: token cleanup failed", "error", dbFailure("cleanup tokens", err))
	}
	s.limiter.purge()
}

func normalizeIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	normalized := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	return normalized
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func validName(name string) error {
	if name != strings.TrimSpace(name) || utf8.RuneCountInString(name) < nameMinRunes || utf8.RuneCountInString(name) > nameMaxRunes {
		return clientError(400, "names must be 2 to 64 characters", ErrInvalid)
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return clientError(400, "names cannot contain control characters", ErrInvalid)
		}
	}
	return nil
}

func validRoleName(name string) error {
	if name != strings.TrimSpace(name) || utf8.RuneCountInString(name) < nameMinRunes || utf8.RuneCountInString(name) > nameMaxRunes {
		return clientError(400, "role names must be 2 to 64 characters", ErrInvalid)
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return clientError(400, "role names cannot contain control characters", ErrInvalid)
		}
	}
	return nil
}

func validTokenName(name string) error {
	if name != strings.TrimSpace(name) || name == "" || utf8.RuneCountInString(name) > nameMaxRunes {
		return clientError(400, "token names must be 1 to 64 characters", ErrInvalid)
	}
	return nil
}
