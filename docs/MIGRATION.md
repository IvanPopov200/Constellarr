# Migration

The migration wizard imports an existing media stack into Constellarr. Source
applications keep running and are only read during discovery.

Supported sources: Radarr, Sonarr, Lidarr, Prowlarr, Bazarr, SABnzbd, NZBGet,
Transmission, and Jellyfin.

## Wizard flow

1. `POST /api/v1/migration/connections/test` verifies each connection and returns
   its version. Errors are sanitized and never include credentials or URLs.
2. `POST /api/v1/migration/preview` reads the sources, stores a plan pinned to the
   fetched data, and returns counts, warnings, candidates, and unsupported settings.
3. `GET /api/v1/migration/plans/{id}` returns the stored plan and per-item progress.
4. `POST /api/v1/migration/plans/{id}/apply` applies one bounded batch with explicit
   mappings, source selections, and choices, then returns the per-item report.

Plans expire after 30 minutes; later reads and applies return `410`. Apply is
idempotent per item (`done`, `failed`, `skipped`), never repeats finished work, and
a crashed apply is reclaimed after two minutes. `retryFailed` resets failed items so
they can be retried after the cause is fixed. Repeating a migration does not
duplicate catalog entries, profiles, torrent jobs, or subtitle profiles.

## Imported settings

- Movies and TV: identifiers, metadata, monitoring, tags, quality profiles, root
  folders, naming formats, and existing local files found under a mapped root. A
  source profile is offered for reuse only when an existing profile decides
  releases identically: the same qualities in the same preference order (profile
  rank follows the listed order), the same cutoff, upgrade flag, size limits, and
  language, and no custom rules or score thresholds. Music formats are compared the
  same way, because `formatRank` reads the listed order for preference and
  upgrades. A profile that merely shares the name is never
  reused: the source settings are created under a free name such as
  `HD (sonarr:2)`, the existing profile stays untouched, and a rerun keeps the same
  migrated ID, so user edits to it survive. Size limits and language from the
  source plan are preserved when a profile is created.
- Music (Lidarr): artists and albums with MusicBrainz identifiers, monitoring and
  per-album state, quality profiles mapped onto the music format list, root
  folders, and artist/track naming formats. Existing music files are not imported;
  use the library scan after migration.
- Indexers: every enabled Prowlarr indexer is listed. One NZB indexer can be
  selected for Constellarr's Usenet search; torrent indexers are offered as
  Torznab sources for the built-in torrent module and can be multi-selected.
- News servers: every enabled SABnzbd and NZBGet server is listed. One primary is
  selected explicitly; other sources are added as fallback hosts only when their
  credentials match the primary account, and incompatible sources are reported.
- Subtitles (Bazarr): language profiles, the default profile, score cutoff, search
  cadence, sync offset limit, and the OpenSubtitles account are applied. Providers
  without a Constellarr adapter, Bazarr's upgrade schedule, and its alignment
  quality threshold are listed as unsupported instead of being dropped.
- Torrents (Transmission): bandwidth limits, queue size, DHT/PEX, and seeding
  limits are applied. Torrents are imported paused with their magnet link, and
  existing data is hard-linked into the torrent job directory only after every
  file verifies by name and size; otherwise the item asks for an explicit
  redownload instead of silently losing progress.
- Jellyfin: connection from wizard input, applied to the shared movie config.

Unsupported settings are listed per area: download history, queue state, manual
import records, artist tags, extra news servers, private tracker passkeys that
Transmission's RPC cannot export, and multi-disc naming formats. Remote files are
never reported as available.

## Credentials

Connection credentials and discovered provider secrets live in server memory for
the life of the plan. They are never written to the plan, database, logs, or API
responses, and every item message is sanitized. Requests that carry credentials
never follow a cross-origin redirect or a scheme downgrade, redirect chains are
bounded, and the caller's HTTP client is copied rather than changed. After a
restart the plan stays usable, but provider-backed items need `connections` sent
again with the apply request; catalog items never do.

## Integration

`migration.New(ctx, pool, migration.Options{Movies, TV, Music, Subtitles, Torrents, Downloads})`
returns the service, `Register(mux)` adds the routes under `/api/v1/migration`, and
`Start(ctx)`/`Close()` run plan cleanup. Tables live in
`downloads/migrations/008_migration.sql` and are applied by the downloads
migrations, so the downloads service must start first.

Artist records are written through `music.Store.SaveArtist` from the Lidarr payload,
which already carries the MusicBrainz identity, sort name, type, monitoring, profile,
and root folder; no extra MusicBrainz lookup is needed during migration. Albums use
`music.AddAlbum`.

## Verification

- `go -C backend test ./internal/migration/...` runs adapter contract tests with
  sanitized fixtures plus retry, timeout, response-limit, and redaction checks.
- Integration tests need `TEST_DATABASE_URL`, use an isolated schema, and cover the
  full wizard, idempotency, bad mappings, expired plans, duplicate-apply
  prevention, restart resumption, bounded batches, and torrent reuse/redownload.
- `MIGRATION_LIVE_SOURCES=/path/private.json go -C backend test ./internal/migration -run TestLiveSourceContracts`
  checks adapters against real applications; `-run TestLiveDevStackPreviewApply`
  runs preview and apply against the development stack and prints a sanitized
  summary. Both keep credentials outside the repository and only write the
  isolated test schema.
