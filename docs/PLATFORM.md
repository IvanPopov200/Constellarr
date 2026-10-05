# Platform boundaries

Constellarr manages movies, television, music, subtitles, acquisition, requests,
and access in one application. Jellyfin provides playback. PostgreSQL stores
catalogs, configuration, permissions, task progress, and audit history. The Go
server owns automation independently of browser sessions.

## Library and acquisition

Each media type has configurable library roots and naming rules. Imports validate
identity and quality, preserve existing files until replacement succeeds, and
retain enough state to recover after interruption. Torrent imports preserve the
source while seeding. Subtitle sidecars share the video basename and remain with
the video through moves and renames.

Usenet and torrent transfers run within the distributed application. Indexers,
metadata sources, subtitle providers, Jellyfin, and optional OpenAI-compatible
services have configurable URLs and credentials. Secrets are write-only in API
responses and are never included in provider errors or ordinary logs.

## Access

Initial setup creates the first administrator. Password accounts, revocable
sessions, scoped API tokens, built-in roles, and custom permission sets govern
server access. Permissions are enforced by the API; hiding an action in the
interface is supplementary. The last active administrator cannot be removed.

Permissions cover library reads and edits, downloads, requests and approvals,
subtitles, configuration, users, migration, backups, and monitoring. Requesters
can track their own requests; approval and server management require separate
permissions. Administrative writes have a durable audit trail.

## Migration and recovery

Migration uses a connection check, a preview, explicit path and profile mappings,
and an apply result with per-item errors. Existing applications and media remain
available throughout migration. Repeating a migration must not duplicate catalog
items. Unsupported settings are reported rather than silently discarded.

Backups contain database configuration and catalogs, with a versioned manifest,
integrity checks, download, retention, and a tested restore path. Large media
files are managed separately. Restores validate archives before replacing state
and provide a recovery copy of the previous database.

## Operations and discovery

Prometheus-compatible metrics, structured logs, health checks, and configurable
alerts expose transfer failures, provider failures, import failures, storage
pressure, and task status. Metric labels avoid titles, paths, and credentials.

A combined calendar covers upcoming movies, episodes, and album releases.
Recommendations are optional and explain their source. AI subtitle translation
preserves cue identity, timing, ordering, and formatting; synchronization supports
offsets, frame-rate changes, and alignment against audio or reference subtitles.
