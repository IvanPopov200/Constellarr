# Discovery: requests, calendar, and recommendations

`backend/internal/discovery` owns media requests, the combined release calendar, and
optional AI recommendations. `backend/internal/discovery/ai` owns the shared
OpenAI-compatible provider used by recommendations and subtitle translation.

## Wiring

`cmd/constellarr/main.go` mounts `discovery.Service` alongside the other API modules,
with authentication, music, shared translation, and operations notification hooks:

```go
requests, err := discovery.New(shutdown, pool, movieLibrary, tvLibrary, discovery.Options{
    Actor: func(r *http.Request) (string, bool) {
        userID, _ := auth.UserID(r.Context())
        return userID, auth.Can(r.Context(), auth.PermRequestsApprove)
    },
    Can:    func(r *http.Request, permission string) bool { return auth.Can(r.Context(), permission) },
    Notify: notifier.RequestDecision,     // decisions, availability, reverted approvals, delivery failures
    UserName: access.DisplayName,         // one bounded lookup per account ID, never a directory dump
    Music:  musicSource{service: musicLibrary},
})
requests.Start(shutdown) // background recovery and library tracking
defer requests.Close()

api.New(pool, api.Services{
    Downloads: manager, Movies: movieLibrary, TV: tvLibrary,
    Modules: []api.Module{access, requests}, Guard: access.Middleware,
})
```

- Every route denies access with `401` while `Options.Actor` is unset and denies every
  extra permission while `Options.Can` is unset, so wiring both hooks is mandatory.
- `Register` mounts all routes under `/api/v1`, including `/ai/*`; `api.New` keeps its
  host, origin, and content-type checks in front of them.
- `Options.UserName` resolves one account ID to a display name for the interface. Responses
  keep the stable `userId`/`decidedBy`/`actor` fields and add `userName`, `decidedByName`,
  and `actorName`; lookups are memoised per response and capped at 250. Fallbacks: the
  automation actor reads `System`, an ID the directory no longer knows reads
  `Deleted user`, an unwired hook or an exhausted budget reads `Unknown user`, and the
  interface shows `You` for the signed-in account. A missing hook never exposes raw IDs.
- `Notify` also fires once when a request's delivery transitions into `failed` (download or
  import), so operations alerts do not repeat on every poll; reverted approvals already
  notify.
- The auth route map in `internal/auth/permissions.go` lists the discovery patterns
  (`requests*` → `requests.read`/`requests.write`/`requests.approve`, `ai/config` and
  `ai/test` → `settings.read`/`settings.write`, `calendar*` → library reads). The
  recommendation entries need this split instead of `library.write` on generate:
  `GET/POST /api/v1/recommendations`, `GET /api/v1/recommendations/{id}` and the base
  of `POST /api/v1/recommendations/{id}/accept` require `library.read`, so a requester
  can generate and read suggestions. `Options.Can` then enforces the branch inside
  `POST .../accept`: `requests.write` to turn a candidate into a request and
  `library.write` to add it to the library. Approval stays behind `requests.approve`.
- `Music: musicSource{service: musicLibrary}` (`cmd/constellarr/music_source.go`) implements
  `discovery.MusicLibrary` over the real music service: `Search` uses the MusicBrainz
  discovery results, `Add` stores exactly the selected release group through
  `music.Service.AddReleaseGroup`, `Status` maps album state to request delivery, and
  `Calendar` forwards the library release calendar. Search results report the library's
  real state: an album that is catalogued but still missing files stays `wanted` (and
  requestable), while only albums with files on disk are `available`. While the hook is
  nil, music is reported as unavailable in `GET /requests` (`types`) and `GET /calendar`
  (`sources`), and music requests get `503`; no route invents music results.
- Request approvals add only the requested album: its artist is created (or reused)
  with monitor option `none` and only the album is monitored. Repeats are idempotent,
  including after a restart, and records without a resolvable artist or title are refused
  instead of guessed. The approval also queues the music monitor pass (when the hook
  offers `Sync`), so the selected album is searched without waiting for the next tick;
  catalogued albums without files are reused instead of being added twice.
- `subtitles.Options{Translator: sharedTranslator{service: requests.AI}}`
  (`cmd/constellarr/shared_translator.go`) runs subtitle translation through the same
  stored provider as recommendations. The client is rebuilt per request, so saving a
  configuration applies to the next chunk, and subtitle chunk token/temperature budgets
  stay owned by the subtitle service. Without a configured endpoint or model the adapter
  returns `subtitles.ErrNotConfigured` with the location of the shared settings, so the
  subtitle interface shows an actionable message; a keyless local endpoint is supported.
- `ai.Service.Secret` exposes the stored provider configuration including the API key for
  in-process clients and must never be serialized; HTTP responses use `GetConfig`.
- `requests.AI` is the shared provider service: `ai.Service` exposes `GetConfig`,
  `SetConfig`, `Client`, `ConfiguredClient`, `Test`, `Models`, and `Complete` for
  subtitle translation. `ai.NewClient(cfg)` builds a standalone `*ai.Client`.

## Requests API

Display fields: `userName`, `decidedByName`, `actorName`, and comment `userName` carry names
only; account IDs stay unchanged in `userId`, `decidedBy`, and `actor`. The list query `q`
matches titles, provider IDs, stable account IDs, resolved names, and messages, scanning at
most 600 newest requests before the in-memory name match.

| Route | Purpose |
| --- | --- |
| `GET /api/v1/requests` | Own requests; approvers see every request. Returns `types`, `canApprove`, and `permissions`. |
| `POST /api/v1/requests` | Submit a request. `201` on create, `200` with the existing active request on a repeat. |
| `GET /api/v1/requests/discover` | Metadata search for requests (`type`, `q`, `page`). |
| `GET /api/v1/requests/{id}` | Request with comments, audit events, and `canApprove`. |
| `POST /api/v1/requests/{id}/approve` | Approver decision with profile, root, monitoring, and note edits. |
| `POST /api/v1/requests/{id}/reject` | Approver decision with a required reason. |
| `POST /api/v1/requests/{id}/cancel` | Requester withdraws a pending request. |
| `POST /api/v1/requests/{id}/comments` | Bounded plain-text comment. |

Request lifecycle: `pending -> approving -> approved -> available`, plus `rejected`
and `cancelled` (both allow the same media to be requested again). The active request
is unique per user and media through a partial index, so concurrent submissions cannot
duplicate; a duplicate submission returns the existing request instead of an error.

Approval is idempotent and never claims success early: the request moves to
`approving` under a row lock, the owning module's `Add` runs outside that lock, and
only a successful add records `approved` with the exact library ID. Failures keep the
request in `approving`, store a sanitized reason with the attempt count, and the
background pass resumes the add (idempotent through the module's identity lookup) or,
after five failed attempts, returns the request to `pending` with the reason. A
successful approval queues one monitor/search pass in the owning module, and the
background pass maps the library item's state onto `delivery` (phase, job progress,
episode counts, sanitized error) until the media is `available`.

## Calendar API

`GET /api/v1/calendar` merges the movie, TV, and optional music calendars sorted by
date. Query parameters: `from` and `to` as `YYYY-MM-DD` (default today plus 180 days,
maximum span 730 days, at most 1000 entries), `types` (`movie,tv,music`) and `sources`
(`movies,tv,music`). Dates stay date-only; no time zone shifting is applied.
`GET /api/v1/calendar.ics` renders the same selection as an iCalendar feed with
CRLF folding at 75 octets, escaped text, date-only events, and stable
`discovery-<identity>@constellarr` UIDs.

## AI configuration and recommendations API

| Route | Purpose |
| --- | --- |
| `GET/PUT /api/v1/ai/config` | Read (redacted) or write base URL, model, token budget, temperature, and the write-only API key. |
| `POST /api/v1/ai/test` | Connection test: `/models` when supported, otherwise a one-token completion. |
| `GET /api/v1/ai/models` | Optional model list from the provider. |
| `POST /api/v1/recommendations` | Generate candidates from a bounded taste summary. |
| `GET /api/v1/recommendations` | Recent runs for the caller; returns `permissions` for the interface. |
| `GET /api/v1/recommendations/{id}` | One stored run. |
| `POST /api/v1/recommendations/{id}/accept` | `request` needs `requests.write`, `add` needs `library.write`, for one verified candidate. |

The base URL must be an absolute `http`/`https` URL without credentials, query, or
fragment. Redirects stay on the configured origin, responses are limited to 1 MiB, and
requests time out after 60 seconds. Provider failures are redacted: no key, prompt, or
model output reaches an error response. The API key is optional so a private local
endpoint works without one; `apiKeyConfigured` always reports whether a key is stored,
and no provider call is made without a base URL and model.

Recommendations summarize genres and titles the user selects plus, when they choose it,
their own request history. The prompt is capped at 4000 characters and asks for strict
JSON: `{"candidates":[{"title","mediaType","year","reason"}]}`. Every candidate is then
resolved against the configured metadata provider or the music hook; confirmed
candidates carry the provider ID, unconfirmed ones stay `verified: false` with an empty
ID and cannot be accepted. Results are stored with model, inputs, candidates, warnings,
and the accepted action.

## Frontend

- `RequestsPage` (`requests-page.tsx`): filters, the approver queue, quick approve,
  detail dialog with approve/reject forms, comments, and the audit trail.
- `NewRequestDialog`: metadata search and submission.
- `CalendarPage` + `CalendarMonth`: upcoming, list, and month views with type filters,
  date bounds, and the `.ics` link.
- `RecommendationsPanel` and `AISettings`: taste selection, candidate acceptance, and
  provider configuration. All four are exported for the app shell to mount.
- `frontend/src/lib/discovery-api.ts` holds the typed client and the display helpers.
- The request list and detail dialog show names (with `You` for the signed-in account) and
  never print internal IDs as prose; the catalog action is labelled for its section
  (`Open Movies`, `Open TV Shows`, `Open Music`). Per-title deep links need a hook in the
  movie or TV page, which those modules own today.
- The approval notice reflects the monitoring choice: monitored approvals say the library
  searches and imports the title, unmonitored approvals say nothing runs until it is
  monitored.
- `AISettings` renders read-only without `settings.read`, disables its fields and hides
  save/test/model controls without `settings.write`, and reports "Configured" from the
  endpoint and model alone; the stored key is surfaced as a separate badge. It is the only
  AI credentials form: the subtitle settings show the shared provider read-only.

## State and tests

Migration `013_discovery.sql` adds `discovery_requests`, `discovery_request_events`,
`discovery_request_comments`, `discovery_ai_config`, `discovery_recommendations`, and
`discovery_automation`. Integration tests need `TEST_DATABASE_URL` and create a
disposable schema per test; they cover request ownership, dedupe, approval races,
failed-approval recovery, the calendar contract and ICS escaping, AI configuration
redaction, malformed model output, and upstream timeouts.
