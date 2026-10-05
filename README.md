# Constellarr

A self-hosted media discovery, acquisition, and management platform for home
servers. Constellarr manages the media workflow; Jellyfin handles playback.

Movies, TV, and music combine metadata catalogs, monitoring, quality profiles,
release selection, and organized libraries. Built-in Usenet and torrent engines
handle acquisition, recovery, extraction, and seeding. Subtitles support provider
search, embedded-track extraction, timing alignment, and optional AI translation.

The workspace also includes requests and approvals, a combined calendar, optional
recommendations, account permissions, migration from existing media services,
metrics and alerts, and database backups. Jellyfin handles streaming and playback;
separate download clients are not required.

## Local development

Install Node.js 22.23.1, Go 1.27+, Python 3, a C compiler, Make, and Docker with Compose.
Native media processing also uses `par2`, `ffmpeg`/`ffprobe`, and `ffsubsync` 0.5.1.
Database backup and restore use PostgreSQL 18 client tools (`pg_dump`, `pg_restore`).
On macOS, Xcode Command Line Tools provide the compiler; `brew install par2`
provides repair. On Debian/Ubuntu use `build-essential` and `par2`.
`make setup` installs ffsubsync in the ignored `bin/subtitle-tools` virtual
environment, which `make dev` adds to `PATH`. Use `make subtitle-tools` to install
it separately and `make doctor` to check all native tools. The distributed image
includes these helpers.
Docker Desktop or Docker with Colima works on macOS. Start the Docker runtime
before development (`colima start` when using Colima).

```sh
make setup
```

This installs dependencies and creates an ignored `.env` with a generated
database password. Configure providers in System → Connections, or supply initial
`NZBGEEK_API_KEY`, `USENET_USERNAME`, and `USENET_PASSWORD` values in that local file.
Environment defaults include `USENET_HOST`, TLS `USENET_PORT`,
`USENET_CONNECTIONS`, and comma-separated `USENET_FALLBACK_HOSTS`.
Connections use verified TLS certificates. Add Frugal's bonus host as a fallback
only if your account includes access to it.

```sh
make dev
```

This starts PostgreSQL, the API at `http://127.0.0.1:8080`, and Vite at
`http://127.0.0.1:5173`. Open Vite, create the first administrator account, test
the source connections, search a movie,
and choose a release to download. Frontend changes reload automatically;
restart `make dev` after backend or `.env` changes. Ctrl+C stops the development
processes and leaves PostgreSQL running.

Adjust `FRONTEND_PORT` or `APP_PORT` in `.env` if a port is occupied.
`DATABASE_URL` can point to an existing PostgreSQL instance; use
`node scripts/dev.mjs --no-db` to leave it under your own management.
Keep `.env` and `data/` private. Provider credentials stay on the server.

## Server settings

Save the NZBGeek API URL/key and Usenet host, TLS port, account, connection limit,
and fallback servers in System → Connections. Leave credential fields blank to
keep their saved values, then use Test connections to check both services.
Changes apply to new operations; active downloads keep the configuration they
started with.

Saved connection settings live in PostgreSQL and take precedence over environment
defaults on restart. Keys and passwords are never returned by the API.
Changing a metadata or Jellyfin server requires supplying its key again.
Connections also configures OMDb metadata and poster access, optional Jellyfin
refresh, and an import webhook. Storage & Paths manages movie, TV, and music root folders,
naming templates, and copy, move, or hardlink imports. Root paths refer to the
server filesystem; mount NAS media folders into the app container before adding
their paths. Music → Settings also provides its catalog and automation settings.
`DOWNLOAD_DIR` remains deployment configuration. System shows health, metrics,
events, and alerts. AI provider settings are shared by recommendations and subtitle translation.
For access through a custom hostname, set `ALLOWED_HOSTS` to a comma-separated
list of exact names. Localhost and IP addresses work by default.

## Movie library

Add movies through metadata search, an IMDb ID, or manual title/year entry when
metadata is unavailable. Configure an `OMDB_API_KEY` in Connections or `.env` for
IMDb ratings, release dates, directors, cast, genres, runtime, and posters. Missing
metadata stays unknown; saved metadata remains browsable offline. Poster requests
are proxied and cached by the server so provider credentials stay private.

The library supports poster and table views, metadata sorting and filters, tags,
collections, and bulk editing. Monitored movies are searched on the server, with
RSS polling and scheduled searches continuing after browser closure or restart.
Quality profiles order allowed qualities best first, set an upgrade cutoff,
limit size/language, and score or reject release patterns. Interactive search
explains rejected releases and allows an explicit override of quality rules.

Completed downloads import automatically. The default destination is
`<DOWNLOAD_DIR>/library/movies`; hardlinks avoid duplicating media when both paths
share a filesystem and fall back to copying across filesystems. Move imports
delete source media after successful publication. Replacements and removed movie
files are retained under `.recycle` in their root folder. Scan existing roots to
match and import local files, or preview a rename before applying it.

Movies also includes wanted and calendar views, an iCalendar export, history,
failed-release blocking, and watchlist imports with scheduled synchronization.
Optional NFO sidecars, Jellyfin refresh, and webhook notifications run after imports.
See [the movie-management scope](docs/MOVIES.md) for the shared package boundaries.

## TV library

Add series through metadata search or manual entry. Series, seasons, and individual
episodes can be monitored independently. Choose all, future, missing, existing,
first-season, latest-season, or no episodes when adding or editing a series.
Episode catalogs load in bounded batches and resume after restarts.

TV shares the metadata and Jellyfin connections with Movies. Both workspaces
can edit the same quality profiles. TV storage and naming are separate; the default root is
`<DOWNLOAD_DIR>/library/tv`, with a folder per series and season.
Interactive searches support episodes and season packs, show rejection reasons,
and queue built-in Usenet or torrent downloads. Server-side RSS and scheduled searches acquire
aired, monitored episodes and apply the selected upgrade cutoff.

Imports match episode numbers or a unique air date, preserve unrelated episodes,
and retain replacement files under `.recycle`. Scans and manual matching handle
existing libraries and ambiguous files. Specials can be added manually when the
metadata provider lacks them; absolute episode numbering requires manual matching.
TV includes wanted episodes, an air-date calendar with iCalendar export, history,
rename previews, and Jellyfin-compatible series and episode NFO files.
See [the TV-management scope](docs/TV.md).

## Music, subtitles, and discovery

- [Music](docs/MUSIC.md): MusicBrainz artist and album discovery, track metadata,
  quality profiles, monitored releases, imports, scanning, and naming.
- [Subtitles](docs/SUBTITLES.md): OpenSubtitles search, language profiles, wanted
  automation, embedded extraction, offset and frame-rate correction, audio or
  reference alignment, and reviewed AI translations. Translation uses the
  OpenAI-compatible provider configured in Connections.
- [Requests and discovery](docs/DISCOVERY.md): movie, series, and album requests
  with approval and status tracking, a combined calendar, and optional AI recommendations.

AI features are optional. Existing catalogs, search, downloads, and automation
continue to work without an AI provider.

## Accounts and administration

The first visit creates the administrator account. Users & Access manages
accounts, built-in or custom roles, personal API tokens, and an audit trail.
Permissions apply to both the interface and API. Native clients and scripts use
scoped bearer tokens; browser sessions use an HTTP-only cookie.

[Migration](docs/MIGRATION.md) tests source connections, previews selected imports
and path mappings, and records resumable import progress. Review paths before
applying a plan so existing media remains available to both installations.

[System and backups](docs/OPERATIONS.md) provides Prometheus metrics, alert rules,
webhook delivery, scheduled database backups, backup import, and verified restores.
A restore pauses automation and reloads services afterward. Media files require
their own backup; database backups preserve catalog state and credentials.

## Downloads and storage

`DOWNLOAD_DIR` defaults to `./data`. Jobs use
`data/downloads/<job-id>/input` for verified article caches and assembled files,
and `data/downloads/<job-id>/output` for extracted media. PostgreSQL stores the
NZB, queue, processing state, and output manifest. Versioned SQL migrations run
at startup. One job runs at a time; duplicate release selections reuse its job.
Failed jobs can be retried without fetching verified article parts again.
Interrupted jobs resume after the server restarts.

[Torrents](docs/TORRENTS.md) supports magnets, `.torrent` files, Torznab sources,
pause/resume/recheck, bandwidth limits, and seeding limits. Torrent library imports
keep the seeding payload in place, using hardlinks or copies. Public and private
torrents use separate listeners; private transfers disable DHT and peer exchange.

Keep both the PostgreSQL volume and download directory when moving installations
or making backups. Completed downloads retain input/cache files for recovery;
automatic download-cache cleanup is not implemented yet. Imported library files
and the `.recycle` directory also need to be included in backups.
Encrypted archives are currently unsupported.

## Verification

```sh
make check
make test-integration
```

Checks cover frontend lint/build, Go vet/tests, indexer and TLS NNTP contracts,
local torrent peers, metadata, media imports, subtitle helpers and providers,
AI response validation, permissions, migration, backup recovery, and webhooks.
PostgreSQL tests run when `TEST_DATABASE_URL` is supplied; `make test-integration`
uses that server when supplied, otherwise creates and removes a temporary database
on the development PostgreSQL service. `make media-fixture` creates a synthetic
video for the real subtitle-helper tests; CI installs and exercises these helpers.
This keeps recovery tests separate from the running application. Real-service checks use local
credentials and are deliberately outside the automated suite.
GitHub Actions runs the checks with PostgreSQL and builds the container.

Add UI components from `frontend/` with `npx shadcn@latest add <component>`.

## Production build

Copy `.env.example` to `.env`, set a private `POSTGRES_PASSWORD`, and set
`APP_UID`/`APP_GID` to the account that owns your media folders. Docker Compose is
the only runtime needed for this deployment. Existing development setups can
reuse their generated `.env`.

```sh
docker compose up -d --build --wait
```

Compose builds Constellarr and starts PostgreSQL with persistent storage. Open
`http://127.0.0.1:8080`. HTTP is bound to localhost; torrent listeners expose TCP
and UDP ports 51413 and 51414, configurable with `TORRENT_PORT` and
`TORRENT_PRIVATE_PORT`. PostgreSQL stays on the internal network. The development override exposes PostgreSQL to
localhost. `make down` stops containers and preserves the database volume.
Stop `make dev` before switching to `make up`; both use the same API port.

The image includes the compiled React frontend, Go API, media and subtitle
helpers, and PostgreSQL backup tools.
It runs unprivileged, using `APP_UID`/`APP_GID` for access to bind-mounted media.
Local development setup chooses your current user; set those IDs explicitly
when deploying onto a NAS with a different media owner.

`make build` produces `bin/constellarr` for native deployment. Supply
`DATABASE_URL`, provider settings, and `DOWNLOAD_DIR` when running it, with the
native helpers on `PATH`. The fast yEnc decoder uses CGo; no Node runtime is required by the
built application. Dependency notices are in `THIRD_PARTY_NOTICES.md`.

## Layout

```text
frontend/         React, TypeScript, Vite, shadcn/ui, Tailwind
backend/          Go API, queue, migrations, indexer, Usenet, media processing
scripts/          Local environment setup and development runner
compose.yaml      Constellarr and PostgreSQL distribution
compose.dev.yaml  Local database port override
```

`GET /api/v1/health` reports PostgreSQL readiness; `GET /healthz` reports process
liveness. `GET` and `PUT /api/v1/settings` read and save connection configuration.
`/api/v1/sources`, `/releases`, and `/downloads` expose the first
workflow. `/api/v1/movies`, `/movie-profiles`, `/movie-config`, and `/movie-watchlists`
expose catalog, profiles, import settings, and watchlists. `/api/v1/tv` and `/tv-config`
expose series, episode monitoring, searches, imports, and TV settings.
Completed file endpoints support HTTP range requests. Unknown API
routes return JSON errors; production browser routes use the embedded frontend.
