# Constellarr

A self-hosted media discovery, acquisition, and management platform for home
servers. Constellarr manages the media workflow; Jellyfin handles playback.

Movies combines a metadata catalog, monitoring, quality profiles, release selection,
and an organized library. Built-in Usenet downloads include durable progress, retry,
PAR2 verification and repair, and RAR/ZIP extraction. Completed movies import into
configured root folders and can refresh Jellyfin. Torrent handling, TV, music,
and subtitles remain future work.

The web workspace groups Overview, Requests, Movies, TV Shows, Music, and Subtitles;
Downloads contains Usenet and Torrents; System contains Connections, Storage & Paths,
Users & Access, and System. Movies provides library management, Usenet shows the queue,
and Overview shows downloaded movies, recent activity, and source status. Future sections are marked
as planned until their workflows are implemented.

## Local development

Install Node.js 22.23.1, Go 1.27+, a C compiler, Make, `par2`, and Docker with Compose.
On macOS, Xcode Command Line Tools provide the compiler; `brew install par2`
provides repair. On Debian/Ubuntu use `build-essential` and `par2`.
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
`http://127.0.0.1:5173`. Open Vite, test the source connections, search a movie,
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
refresh, and an import webhook. Storage & Paths manages movie root folders,
naming templates, and copy, move, or hardlink imports. Root paths refer to the
server filesystem; mount NAS media folders into the app container before adding
their paths. `DOWNLOAD_DIR` remains deployment configuration. System shows backend
and database health.
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

## Downloads and storage

`DOWNLOAD_DIR` defaults to `./data`. Jobs use
`data/downloads/<job-id>/input` for verified article caches and assembled files,
and `data/downloads/<job-id>/output` for extracted media. PostgreSQL stores the
NZB, queue, processing state, and output manifest. Versioned SQL migrations run
at startup. One job runs at a time; duplicate release selections reuse its job.
Failed jobs can be retried without fetching verified article parts again.
Interrupted jobs resume after the server restarts.

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

Checks cover frontend lint/build, Go vet/tests, synthetic indexer and TLS NNTP
contracts, metadata and posters, movie imports and automation, Jellyfin/webhooks,
damaged archive/PAR2 recovery, and isolated PostgreSQL schemas.
PostgreSQL tests run when `TEST_DATABASE_URL` is supplied; `make test-integration`
creates and removes a temporary database on the development PostgreSQL service.
This keeps recovery tests separate from the running application. Real-service checks use local
credentials and are deliberately outside the automated suite.
GitHub Actions runs the checks with PostgreSQL and builds the container.

Add UI components from `frontend/` with `npx shadcn@latest add <component>`.

## Production build

```sh
make setup
make up
```

Compose builds Constellarr and starts PostgreSQL with persistent storage. Open
`http://127.0.0.1:8080`. Only the app is exposed, bound to localhost; PostgreSQL
stays on the internal network. The development override exposes PostgreSQL to
localhost. `make down` stops containers and preserves the database volume.
Stop `make dev` before switching to `make up`; both use the same API port.

The image includes the compiled React frontend, Go API, and PAR2 repair helper.
It runs unprivileged; `make setup` sets `APP_UID`/`APP_GID` to your local user so
the bind-mounted download directory stays accessible. Set those IDs explicitly
when deploying onto a NAS with a different media owner.

`make build` produces `bin/constellarr` for native deployment. Supply
`DATABASE_URL`, provider settings, and `DOWNLOAD_DIR` when running it, with `par2`
on `PATH`. The fast yEnc decoder uses CGo; no Node runtime is required by the
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
expose catalog, profiles, import settings, and watchlists. Completed file endpoints support HTTP range requests. Unknown API
routes return JSON errors; production browser routes use the embedded frontend.
