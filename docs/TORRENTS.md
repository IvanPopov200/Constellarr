# Torrent downloads

Constellarr ships a built-in BitTorrent engine (`backend/internal/torrents`, built on
`github.com/anacrolix/torrent`). No qBittorrent, Transmission or rTorrent installation is
required. Jobs, settings, resume state and indexer sources live in PostgreSQL; payloads stay
on disk under the download directory.

## Wiring contract

```go
service, err := torrents.New(ctx, pool, torrents.Options{Directory: downloadDir})
service.Start(ctx)          // starts the coordinator and engine clients
defer service.Close()       // flushes resume state and drops torrents
service.Register(mux)       // adds the routes below under /api/v1
```

`New` requires the platform migrations to have run: the schema is owned by
`backend/internal/downloads/migrations/011_torrents.sql` and
`015_torrent_processing.sql`, and is applied by the downloads service migration runner, so
construct the downloads manager before the torrent service.
`Options.Testing` binds the engine to loopback and disables DHT, trackers and PEX; tests and
local fixtures use it, production does not.

## API

All responses are JSON; errors use `{"error": string}`. Secrets (magnet URIs, torrent
metadata, piece bitmaps and indexer API keys) are never returned.

- `GET /api/v1/torrents` – queue with live transfer state.
- `POST /api/v1/torrents` – add by `{magnet}`, `{torrent: <base64>, filename}`, or
  `{sourceId, resultId}` from a search result.
- `GET /api/v1/torrents/{id}` – `{job, peers}` including files and peer connections.
- `POST /api/v1/torrents/{id}/pause|resume|recheck` – transfer control; recheck clears
  verified pieces and rehashes the data on disk before resuming.
- `PUT /api/v1/torrents/{id}/limits` – `{seedRatioLimit, seedTimeLimitMinutes}`.
- `DELETE /api/v1/torrents/{id}?files=true` – keeps files by default.
- `GET /api/v1/torrents/{id}/file?name=` – payload file, or an extracted file once
  preparation finished.
- `GET /api/v1/torrents/search?q=&source=` – Torznab search results.
- `GET|POST /api/v1/torrent-sources`, `PUT|DELETE /api/v1/torrent-sources/{id}`,
  `POST /api/v1/torrent-sources/{id}/test` – Torznab source configuration.
- `GET|PUT /api/v1/torrents/settings` – engine settings.
- `GET /api/v1/torrents/health` – engine state for dashboards and health checks.

## Seeding, limits and resume

A download becomes `seeding` once its pieces are complete and `completed` when the seeding
policy ends. `seedRatioLimit` is measured as uploaded/downloaded; `0` stops seeding as soon
as the download completes. `seedTimeLimitMinutes` counts time spent seeding; `0` means no
time limit. Seeding is stopped as soon as either configured limit is reached, and the files
stay on disk. Pause keeps the torrent attached but disallows transfers. Verified pieces are
persisted per job (`piece_bits`), so a restarted engine resumes without rehashing and a
recheck re-verifies data that was changed outside Constellarr.

DHT, PEX, the listening port, rate limits, active-job count and default seed limits are
stored in `torrent_settings`. Rate limits and the active-job count apply immediately; a
listening port, DHT or PEX change is reported with `restartRequired: true` and applies the
next time the engine starts. Private torrents run on a dedicated client without DHT or PEX
and listen on the next port after the configured one.

## Safety

- Torrent file paths are validated on add: absolute paths, `..`, backslashes, control
  characters, duplicate names and case-insensitive collisions are rejected, and the job
  fails before anything is written. Symlinked job directories are refused.
- Payloads are stored per info hash below `<download-dir>/torrents/<infohash>/`; deleting a
  job only ever removes that directory, and only when `files=true`.
- Imports use the shared library pipeline. `ImportMode()` returns `hardlink`, so library
  imports never move files that are still seeding; `CompletedJobs`, `DownloadJob`,
  `OutputDirectory` and `OpenFile` expose finished torrents in the same shape as Usenet
  downloads for the movie, TV and music importers.
- Torznab result identifiers are opaque server-side handles. Download URLs and API keys
  stay on the server, are redacted from errors, and source fetches have bounded size and
  timeouts. A source API key is only attached to requests for the indexer's own origin
  (scheme, host and port, allowing an HTTP to HTTPS upgrade); download links on another
  host and cross-origin redirects are fetched without it, and a redirect that would carry
  the parameter to another origin has it stripped.
- Feed magnets keep their original tracker list (including private-tracker passkeys) in the
  server-side result cache only. The search response exposes at most a tracker-free magnet
  for public results and no magnet for results the feed marks private, so passkeys never
  reach the browser or an error message.
- Magnets that carry trackers resolve their metadata on the client without DHT or PEX, so
  private swarms never announce their info hash early. A result the feed marks private is
  pinned to that client from the first attach, before and after its metadata arrives, even
  when the metadata carries no private flag. A magnet
  without trackers uses the public client for DHT discovery, which keeps pasted trackerless
  magnets working as a public choice; a private result without a tracker cannot reach its
  swarm and is refused with a message to supply the torrent file instead. Torrents whose
  metadata arrives as private stay on the dedicated private client.

## Archive downloads

A completed payload that only contains RAR or ZIP archives is extracted through the shared
media pipeline (`internal/media`, the same code Usenet downloads use) into
`<download-dir>/torrents/processed/<infohash>/`, leaving the seeding payload untouched; the
importers then read the extracted directory. Payloads that already contain playable media
are imported in place, so large loose files are never duplicated. Extraction runs on a
single bounded background worker outside request handlers and its state
(`pending`, `running`, `completed`, `failed`, `skipped`) is stored per job in
`torrent_processing`; interrupted extractions resume after a restart and a failed one is
retried through the existing resume/retry route. Until preparation completes the downloads
bridge reports `extracting` (and `failed` with the reason if extraction fails), so the
movie, TV and music importers never read a half-prepared directory. `GET .../torrents/{id}`
exposes the state as `processing` for the UI.

## Permissions and UI

The Torrents page mirrors the API permissions: `downloads.read` shows the queue, details and
search; `downloads.write` enables adding, pause, resume, recheck, retry, limits and delete;
`settings.read` shows engine settings and Torznab sources; `settings.write` changes them. A
role without a permission never issues the matching request, so a read-only viewer sees
values instead of errors. Engine health (`GET /api/v1/torrents/health`) is registered by the
auth layer under `downloads.read`, so the health line is shown to roles that can read the
queue.

## Operations

Docker exposes the BitTorrent listening port (TCP and UDP, default 51413) in addition to the
HTTP port, and reuses the existing download volume. Archive extraction needs free space for
the extracted media next to the seeding payload (the original archives are kept) and uses the
bundled `par2` helper when a payload ships PAR2 data; payloads without PAR2 verify and
extract without it. Integration tests need `TEST_DATABASE_URL` and point at an isolated
schema; they create and drop their own schema and never touch existing data. Constellarr only
supports BitTorrent v1 magnets. Trackers on a user's own network are allowed for Torznab
sources, so a LAN Prowlarr or indexer works without exposing the API. The engine depends on
`github.com/anacrolix/torrent` and its DHT package, both MPL-2.0 (not AGPL); they are linked
into the binary, so third-party notices must retain the MPL text.
