# Operations, monitoring, and backups

The operations service owns Prometheus metrics, structured events, alert rules with
history, and managed database backups with a guarded restore path. It is implemented
in `backend/internal/operations` and stores its state in the `operations_*` tables
created by `backend/internal/downloads/migrations/012_operations.sql`.

## Wiring

```go
ops, err := operations.New(ctx, pool, operations.Options{
    DataDir:     downloadDirectory,        // defaults to DOWNLOAD_DIR
    DatabaseURL: os.Getenv("DATABASE_URL"), // defaults to the pool connection string
    PGDumpPath:  "",                        // optional pg_dump / pg_restore overrides
    Quiesce:     workers.Pause,            // required for live restores
    Resume:      workers.Resume,           // must make services reconnect or restart
    AllowLiveRestore: true,                // explicit opt-in for replacing the live database
    AutoRestart:      true,                // Resume restarts or reconnects services itself
})
ops.Start(ctx)                             // collection, alerts, webhooks, schedule
ops.Register(mux)                          // /metrics and /api/v1/operations/*
handler := ops.WrapHTTP(mux)               // HTTP request metrics for the whole server
```

The migration ships with the downloads migration set and is applied by the existing
runner, so `downloads.New` (or an equivalent migration step) must run before
`operations.New`. `operations.New` fails with a clear message when the schema is
missing. `Start` is optional for tests but required for collection, alert evaluation,
webhook delivery, and scheduled backups.

## Permissions

The API security layer (`backend/internal/auth`) protects every route. The operations
routes map to:

- `monitoring.read` (GET only) — `/metrics`, `GET /api/v1/operations/status`, `events`,
  `alerts`, `alerts/config`, and `alerts/history`. A scoped API token with
  `monitoring.read` can scrape `/metrics` with `Authorization: Bearer ...`.
- `settings.write` (write only) — `PUT /api/v1/operations/alerts/config` (rules and
  webhook settings).
- `backups.manage` (read and write) — every `/api/v1/operations/backups*` route,
  including listing, detail, download, import, deletion, schedule changes, and restores.
  Backups contain the settings table with stored credentials, so these routes are
  administrative even for reads.

The metrics handler itself performs no authentication; the security layer in front of it
does.

## Metrics

`GET /metrics` serves the Prometheus text exposition format:

- Go runtime (`go_*`) and process metrics (`process_*`), including CPU, resident
  memory, and open file descriptors.
- Pool statistics: `constellarr_db_pool_connections{state="total|idle|acquired|max|constructing"}`,
  acquisition counters, `constellarr_db_up`.
- Transfers: `constellarr_downloads{state}` (Usenet rows only),
  `constellarr_download_bytes_done{state}`, `constellarr_download_bytes_total{state}`,
  `constellarr_jobs_queued`, `constellarr_jobs_active`.
- Torrents: `constellarr_torrents{state}` (queued, metadata, checking, downloading,
  seeding, paused, completed, failed), `constellarr_torrent_bytes_done{state}`,
  `constellarr_torrent_bytes_total{state}`, `constellarr_torrents_queued`,
  `constellarr_torrents_active`, `constellarr_torrents_seeding`. Torrent shadow rows in
  the `downloads` table are excluded from the download gauges, and Usenet rows are
  excluded from the torrent gauges, so nothing is counted twice.
- Failures: `constellarr_download_failures_total`, `constellarr_import_errors_total`,
  `constellarr_provider_failures_total` (durable event counts).
- Catalog and storage: `constellarr_library_items{kind="movies|series|episodes|artists|albums|tracks"}`,
  `constellarr_storage_free_bytes{volume="data"}`, `constellarr_storage_total_bytes{volume="data"}`
  (last known values survive a failed filesystem probe).
- Alerts and backups: `constellarr_alerts_firing`,
  `constellarr_backup_last_success_timestamp_seconds`.
- HTTP requests through `WrapHTTP`: `constellarr_http_requests_total{method,code}`,
  `constellarr_http_request_duration_seconds{method}` histogram,
  `constellarr_http_in_flight_requests`.

Labels are bounded: states, methods, status codes, and fixed kind values only. Titles,
paths, hosts, and user input never appear in labels or metric help text.

## Events and alerts

`operations_events` is the structured log for download failures, import errors, provider
failures, alerts, backup and restore activity, and webhook failures. Other packages can
record failures directly:

```go
ops.RecordImportError(ctx, "movies", message)
ops.RecordProviderFailure(ctx, "nzbfinder", message)
ops.RecordEvent(ctx, operations.Event{Kind: operations.KindImportError, ...})
```

Failed downloads are detected by polling the `downloads` table, and each job produces
exactly one `download_failed` event; torrent shadow rows in that table are attributed
to `source=torrents`. Media import failures are detected the same way from
`movie_history` and `tv_history` (`type = 'import-failed'`) and from `music_history`
(`type = 'error'` with an `Import failed:` message), with references namespaced per
source so every failure is stored once.

Other packages report failures through the exported hooks; the minimal integration is:

- `ops.RecordProviderFailure(ctx, name, message)` from indexer, metadata, subtitle, and
  music provider error paths (for example `nzbfinder`, `omdb`, `opensubtitles`,
  `musicbrainz`).
- `ops.RecordRequestFailure(ctx, message)` from request approval and delivery failures
  in the requests service; it is recorded as a provider failure with `source=requests`
  and feeds the `provider_failures` rule.
- `ops.RecordImportError(ctx, source, message)` for import failures that are not
  written to a library history table.

Alert rules (persisted in `operations_alert_rules`, state in `operations_alert_state`,
transitions in `operations_alert_events`):

| Rule | Default | Fires when |
| --- | --- | --- |
| `filesystem_low_space` | 10 GiB or 10% free | Free space below either threshold |
| `provider_failures` | 3 in 60 minutes | More provider failures than the threshold in the window |
| `download_import_failures` | 3 in 60 minutes | More download/import failures than the threshold |
| `job_stuck` | 120 minutes | Any active job has not progressed for the configured time |
| `db_health` | 2 checks | Consecutive pool health checks fail |

Rules, severities, and webhook settings are read from `GET /api/v1/operations/alerts/config`
and updated with `PUT` on the same route. Active rules are listed by
`GET /api/v1/operations/alerts`, transitions by `GET /api/v1/operations/alerts/history`.

Webhook notifications are optional (URL, secret header, timeout, minimum interval,
recovery switch). One JSON message is sent per transition, repeated notifications are
suppressed for the configured interval, and recovery messages follow the switch.
Delivery failures are logged and recorded without the URL, the secret, or response
bodies.

## Backups

Backups live under `<DataDir>/backups/<id>/` (independent of media roots) as
`manifest.json`, `database.dump` (custom-format `pg_dump`), and `files.tar`
(configuration files from the data directory; media, downloads, posters, and probes
are excluded). All files are `0600` and directories `0700`.

The manifest records the format version, the database schema version, the dump
SHA-256, the creation time, the origin (`manual`, `scheduled`, `rollback`, `imported`),
and the included and excluded data lists shown in the interface.

- `GET /api/v1/operations/backups` — list (manifest read only).
- `POST /api/v1/operations/backups` — create now; consistent custom-format dump with
  `pg_dump`, validated with `pg_restore --list` before it is stored.
- `GET /api/v1/operations/backups/{id}` — detail with both checksums re-verified.
- `GET /api/v1/operations/backups/{id}/download` — tar stream of the three entries.
- `DELETE /api/v1/operations/backups/{id}` — remove one backup.
- `POST /api/v1/operations/backups/import` — upload a tar, tar.gz, or zip archive as
  JSON base64 (up to 64 MB decoded) or a raw request stream. Entry names, duplicates,
  symlinks, path escapes, compression ratios, per-entry and total sizes, manifest
  version, schema version, checksums, and dump readability are validated before the
  archive is stored; no SQL from the upload is executed during import.
- `GET|PUT /api/v1/operations/backups/config` — schedule (enabled, interval hours) and
  retention (backup count). Retention prunes the oldest automatic and manual backups;
  rollback copies are never pruned automatically.

`pg_dump` and `pg_restore` are resolved from `Options`, `PG_DUMP_PATH`/`PG_RESTORE_PATH`,
or `PATH`. The client major version must be at least the server major version, matching
PostgreSQL's documented requirement. Connection details are passed to the tools through
`PG*` environment variables, never through process arguments, and tool output is
sanitized before it reaches logs or API responses.

List routes answer with one named envelope (`{"backups": []}`, `{"events": []}`), and the
interface unwraps it strictly by name (`frontend/src/lib/operations-envelope.ts`), so a
contract drift raises a visible error instead of silently rendering zero rows. Both sides
are pinned by tests: `TestListRouteEnvelopes` for the JSON shape and
`node --test frontend/src/lib/operations-envelope.test.ts` for the unwrap.

## Restore model

`POST /api/v1/operations/backups/{id}/restore` takes `{"preview": true}` for a plan or
`{"confirm": "<backup id>"}` for execution. The identifier must be typed exactly; a
mismatch is rejected.

A restore is full administration: the API security layer requires `backups.manage` and
`users.manage` in a cookie session (scoped API tokens are refused), because a restore
replaces accounts, sessions, API tokens, and stored provider credentials and because a
custom-format dump can contain SQL that runs during the restore. The interface mirrors
that gate with `useAuth().can('backups.manage') && can('users.manage')`, disables the
restore actions without both permissions, and names the trusted-instance warning in the
confirmation: a backup must come from an instance the operator trusts. The checksum,
manifest, archive, and `pg_restore --list` checks are integrity and sanity checks, not a
security boundary against a crafted archive.

A live restore refuses to run unless the server configured both a `Quiesce` callback and
`AllowLiveRestore`. Admission happens before any blocking lock, so simultaneous restore
requests are refused with `409` immediately instead of queueing a second restore. The
sequence is:

1. Admit one restore (`maintenance` flag) and reject further requests.
2. Acquire the exclusive cross-process advisory lock (`operations` maintenance key).
3. Re-verify checksums, schema version, and `pg_restore --list` (rejecting
   cluster-level `DATABASE`/`TABLESPACE` entries and dumps without known tables).
4. Call `Quiesce` so every automation worker stops writing.
5. Create a rollback backup of the current database (`origin=rollback`).
6. Restore the dump into an isolated scratch database, confirm the expected tables
   exist, and drop the scratch database (`DisableRestoreVerification` skips this).
7. `pg_restore --clean --if-exists --no-owner --no-privileges --exit-on-error
   --single-transaction` into the live database. A failure inside the transaction
   leaves the previous state in place.
8. Reload operations configuration, record a `restore` event, call `Resume`, and
   respond with `restartRequired: true`, `autoRestart`, the rollback backup identifier,
   and notes.

`Resume` must make services reconnect and reload caches, or
trigger a process restart. Set `Options.AutoRestart` when `Resume` restarts or
reconnects services by itself; the restore preview and result then report
`autoRestart: true` and the interface waits for the server to come back, refreshes the
session, and reloads data instead of telling an operator to restart manually. The
response still reports `restartRequired: true` so no client keeps stale pool or schema
assumptions. During the restore the operations collector pauses, and no request-supplied
host, path, or SQL is ever used: the target comes from the configured `DATABASE_URL`
only, and the backup directory is resolved from the managed backup root with strictly
validated identifiers.

Backup creation, import, and restore requests lift the server's global read and write
deadlines through `http.ResponseController` for the duration of `BackupTimeout` or
`RestoreTimeout`, and the interface uses matching request timeouts, so long dumps are
not cut off by the default HTTP timeouts.

Custom-format archives can contain SQL objects such as functions and views. Restore
accepts trusted exports, runs as the configured database role with `--no-owner` and
`--no-privileges`, and uses TOC checks to refuse cluster-level or obviously foreign dumps.
All application settings live in the restored database. Extra configuration files in
`files.tar` remain available in the downloaded archive for manual recovery; live restore
does not overwrite files in the data directory.

## Interface

- `#system` mounts `OperationsPage` (title "System"): status, queues, library and
  failure counters, metrics pointer, recent events, and `AlertsPanel`.
- `#backups` mounts `BackupPanel`: schedule, list, create, download, import, delete, and
  the guarded restore dialog (preview, typed confirmation, included/excluded data,
  rollback copy, trusted-instance warning, automatic reconnect).
- `AlertsPanel` reads active alerts and history with `monitoring.read`. It only calls
  `/api/v1/operations/alerts/config` when the session has `settings.read`, and the rule
  and webhook editors plus the save action require `settings.write`; without those
  permissions it renders a read-only view instead of triggering `403` responses.
- Every backup route, including reads and downloads, requires `backups.manage`;
  restoring additionally requires `users.manage` in a cookie session, and `BackupPanel`
  disables and explains the restore actions when either permission is missing.

## Configuration notes

- `Options.DataDir` defaults to `DOWNLOAD_DIR`; backups are created under
  `<DataDir>/backups` and never inside media roots.
- `Options.AutoRestart` reports that `Resume` restarts services; the main server wires
  it together with `AllowLiveRestore` and the `Quiesce`/`Resume` pair.
- `Options.MonitorInterval` defaults to 15 seconds, `BackupTimeout` and `RestoreTimeout`
  to 30 minutes and bound both the client-tool runtime and the extended HTTP deadlines.
- Webhook URLs must be absolute HTTP(S) URLs without embedded credentials; the secret
  is write-only in API responses (`secretConfigured` only) and is stored in the
  `operations_config` table. Redirects are followed only within the configured origin,
  so the secret header can never reach another host (`CheckRedirect` is installed on the
  client the service builds, including when `Options.HTTPClient` is supplied).
- The webhook client uses a finite timeout, reads at most 4 KB of the response, and
  never logs response bodies.

## Verification

- `go test ./internal/operations/` covers archive validation (path escapes, symlinks,
  duplicate and unknown entries, corruption, tar/zip/gzip bombs), manifest validation,
  checksum tampering, alert evaluation and transitions, webhook payload/secret/rate
  limit/recovery handling, webhook redirect refusal across origins, metrics exposition
  and label bounds, the list-route JSON envelopes, API validation, backup
  creation/import/deletion, retention, scheduled backups, media import failure events,
  Usenet/torrent metric separation, concurrent restore admission, canceled restore
  requests, HTTP deadline extension, and a full PostgreSQL dump-and-restore round-trip
  in an isolated database. Database tests need `TEST_DATABASE_URL` (see
  `/tmp/constellarr-platform-test.json`), PostgreSQL client tools (18 or newer), and
  permission to create databases for the restore tests; otherwise those tests skip.
- `frontend`: `npm run lint` and `npm run build` cover `OperationsPage`, `AlertsPanel`,
  and `BackupPanel`; `node --test src/lib/operations-envelope.test.ts` runs the list
  envelope regression against the shapes the Go handlers send.
