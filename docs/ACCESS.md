# Access control

Accounts, sessions, scoped API tokens, roles, permissions, and the audit trail
live in `backend/internal/auth`. The service validates credentials, resolves the
actor for every API request, and enforces a central route permission map before
handlers run.

## Wiring

The auth tables come from the shared migration chain, so the downloads manager
must be created first. `auth.New` verifies the schema and fails with a clear
error when the migration has not run:

```go
manager, err := downloads.New(ctx, pool, settings)  // applies downloads/migrations/007_auth.sql
if err != nil { return err }
access, err := auth.New(ctx, pool)                  // verifies tables, seeds built-in roles
if err != nil { return err }
access.Register(mux)                                // /api/v1/auth/*, users, roles, audit
access.Start(ctx)                                   // optional: hourly cleanup
defer access.Close()
handler := access.Middleware(mux)                   // protects /api/* and /metrics
```

`downloads/migrations/007_auth.sql` is the single source of truth for the auth
DDL; the auth package embeds no schema of its own, and built-in role grants are
re-seeded from code at every start.

`Middleware` passes static assets and `/healthz` through, allows
`/api/v1/auth/status`, `setup`, `login`, and `logout` without credentials, and
requires a valid session or API token for every other `/api` route and for
`/metrics`. Unknown API routes are denied by default; a mapped route without a
handler returns 404, and a mapped path with an unmapped method returns 405.
Literal patterns win over `{id}` patterns, so `/api/v1/torrents/settings` can
never fall through to `/api/v1/torrents/{id}`. `Require(permission)` adds a
stricter per-handler check where one route needs it.

## Account credentials versus API tokens

Only a cookie session may manage account credentials:

- `GET/POST /api/v1/auth/tokens`, `PUT/DELETE /api/v1/auth/tokens/{id}`
- `PUT /api/v1/auth/password`

A bearer token calling any of these receives 403, whatever its scope. Without
that rule a limited token could mint a new token with broader permissions than
it holds, because scope checks are relative to the account's roles. `GET
/api/v1/auth/me` and `GET /api/v1/permissions` stay available to bearer tokens
so integrations can read their own identity and the permission catalog. Bearer
tokens can still call any other route their scopes and roles allow, including
`/api/v1/users` when the account holds `users.manage`.

## Routes and permissions

| Route | Permission |
| --- | --- |
| `GET /api/v1/auth/status` | public: `setupRequired`, `authenticated`, `user` |
| `POST /api/v1/auth/setup` | public until the first account exists |
| `POST /api/v1/auth/login` | public; 400 `the name or password is incorrect`, 429 when rate limited |
| `POST /api/v1/auth/logout` | public; clears the session cookie |
| `GET /api/v1/auth/me`, `GET /api/v1/permissions` | authenticated |
| `PUT /api/v1/auth/password` | authenticated cookie session; revokes all sessions |
| `GET/POST/PUT/DELETE /api/v1/auth/tokens*` | authenticated cookie session, self-scoped |
| `GET/POST/PUT/DELETE /api/v1/users`, `/users/{id}`, `PUT /users/{id}/password` | `users.manage` |
| `GET/POST/PUT/DELETE /api/v1/roles`, `/roles/{id}`, `GET /api/v1/audit` | `users.manage` |
| movies, tv, music, calendar, recommendations, posters, library files | `library.read` (GET) / `library.write` (writes, scans, imports, sync, grab, refresh, rename, monitor) |
| subtitles, subtitle config, subtitle profiles, subtitle jobs and outputs | `subtitles.read` (GET, streams, downloads) / `subtitles.write` (search, extract, sync, translate, apply, assignments, cancel, config, profiles) |
| downloads, releases, torrents, torrent files | `downloads.read` (GET, search, health) / `downloads.write` (create, retry, pause, resume, recheck, limits, delete) |
| `GET /api/v1/requests`, `/requests/discover`, `/requests/{id}` | `requests.read` |
| `POST /api/v1/requests`, `/requests/{id}/cancel`, `/requests/{id}/comments`, `POST /api/v1/recommendations/{id}/accept` | `requests.write` |
| `POST /api/v1/requests/{id}/approve`, `/requests/{id}/reject` | `requests.approve` |
| settings, sources, movie/tv/music config, profiles, watchlists, torrent settings, torrent-sources, `ai/config`, `ai/models` | `settings.read` (GET) / `settings.write` (writes and connection tests) |
| `GET /api/v1/operations/alerts/config` | `monitoring.read`; `PUT` uses `settings.write` |
| migration plans, preview, connection tests, apply | `migration.manage` |
| `GET/POST/PUT/DELETE /api/v1/operations/backups`, config, import, download, restore | `backups.manage` |
| health, `/metrics`, operations status, events, alerts, alert history | `monitoring.read` |

The map is the single source of truth in `internal/auth/permissions.go` and
mirrors the routes registered across `internal/api` and the service packages
under `internal/*`. A unit test re-scans those packages and fails when a
registered route has no rule, so a new endpoint cannot ship unprotected.

No module registers these paths today, so they stay denied until a rule is
added: `/api/v1/downloads/{id}` (GET, DELETE), `/api/v1/releases/{id}`,
`/api/v1/migration` status or plan creation, alert acknowledgement or mute
routes, `/api/v1/torrents/sources*` (renamed to `/api/v1/torrent-sources`),
`/api/v1/subtitles/{kind}` without an id, and any `/api/v1/system` or
`/api/v1/metrics` alias of the operations routes.

## Permissions and roles

`library.read`, `library.write`, `downloads.read`, `downloads.write`,
`requests.read`, `requests.write`, `requests.approve`, `subtitles.read`,
`subtitles.write`, `settings.read`, `settings.write`, `users.manage`,
`migration.manage`, `backups.manage`, `monitoring.read`.

Built-in roles are immutable and re-seeded from code at every start:

- `admin`: every permission.
- `operator`: every permission except `users.manage`, `migration.manage`, and `backups.manage`.
- `requester`: `library.read`, `requests.read`, `requests.write`.
- `viewer`: `library.read`, `downloads.read`.

Custom roles hold any subset of the catalog, users may hold several roles, and
permissions are the union of their roles. Disabling or deleting an account
revokes its sessions and tokens. The last active administrator cannot be
disabled, demoted, or deleted; concurrent account mutations are serialized by an
advisory lock inside the mutation transaction. Built-in roles cannot be renamed,
edited, or deleted, and a role still assigned to users cannot be deleted.

## Sessions and API tokens

- Sessions use the HttpOnly `constellarr_session` cookie (SameSite=Lax, Path=/,
  30-day expiry). Only the SHA-256 hash of the token is stored. `Secure` is set
  for direct TLS or when `AUTH_SECURE_COOKIES=true` (or
  `auth.WithSecureCookies(true)`); forwarded headers are never trusted.
- Sign-in returns 400 for bad credentials, 401 for a missing or invalid session
  on protected routes, 403 for a missing permission or a bearer token on an
  account-credential route, and 429 with `Retry-After` after 8 failed attempts.
- Rate limits are tracked per account name and per client address in two bounded
  maps. At capacity the limiter drops expired entries first and then the newest
  entry, so rotating usernames or addresses cannot flush an existing block or
  grow memory without limit.
- Password changes and administrator password resets update the hash and revoke
  every session of that account. Sign-out deletes the session row.
- API tokens are named, carry an expiry or none, and their plaintext value
  (`ctlr_…`) is returned once at creation. A token's effective permissions are
  the intersection of its stored scope and the owner's current role grants, so
  removing a role immediately narrows every token of that account. A token
  cannot be created or updated with permissions the owner does not hold;
  omitting `permissions` defaults to the owner's full set.

## Setup

`GET /api/v1/auth/status` reports `setupRequired` until the first account exists.
`POST /api/v1/auth/setup` claims it inside one transaction under an advisory
lock, so concurrent first-run requests cannot both succeed; later attempts return
409. The first account becomes an active administrator and is signed in. There is
no default password and no authentication bypass. Deployments beyond a trusted
network should complete setup immediately.

## Audit

Every authenticated non-GET request on a route that does not record its own
event is audited by the middleware with the HTTP method, the matched route
pattern (no ids or query strings), and the response status, for example
`{action: "POST", target: "/api/v1/downloads", outcome: "202"}`. Query strings,
bodies, and secrets are never recorded.

`GET /api/v1/audit?limit=200` returns the newest `{at, actorId, actorName,
action, target, outcome}` entries: setup, sign-in success and failure, sign-out,
password changes, account, role, and token mutations. Routes in the auth,
users, and roles groups record their specific events and are not double-audited.
Other services record their own administrative writes with the exported hook,
which takes the actor from the request context:

```go
access.Audit(r.Context(), "settings.update", "usenet", "success")
```

Other services read the actor with `auth.FromContext(ctx)`, `auth.UserID(ctx)`,
`auth.Can(ctx, permission)`, or `principal.Can(permission)`; `principal.IsAdmin()`
is available for administrator-only behavior. Requests remains responsible for
filtering its own records to the caller unless `requests.approve` is held.

## Tests

`backend/internal/auth/auth_test.go` covers the route map, the registered-route
scan, built-in roles, password hashing, and the limiter including username-churn
evasion. The integration suite creates an isolated PostgreSQL schema, applies
`downloads/migrations/007_auth.sql`, and requires `TEST_DATABASE_URL`:

```sh
TEST_DATABASE_URL=postgresql://... go test ./internal/auth/ -count=1
```

It covers setup racing, sign-in and session lifecycle, cookie security modes,
scoped, expired, and cross-account tokens, bearer-token credential escalation
attempts, disabled accounts, password rotation, the concurrent
last-administrator guard, custom roles, mutation auditing, and unauthenticated
access attempts. It also runs the real startup path with `downloads.New` before
`auth.New`.
