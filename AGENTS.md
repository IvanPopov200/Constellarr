# Constellarr

## What we are building

Constellarr is a self-hosted media discovery, acquisition, and management platform
for home servers and NAS devices. The end goal is Constellarr plus Jellyfin:
movies, TV, music, and subtitles managed through one coherent experience.

Constellarr will own catalog management, monitoring, indexer search, release
selection, torrent and NZB/Usenet downloads, file imports, quality upgrades,
and subtitles.
Jellyfin remains responsible for streaming, transcoding, and playback state.
Downloads are built in: users manage one queue and configure their sources inside
Constellarr. Reusable protocol libraries or bundled internal workers are acceptable;
required separate download-client installations or configuration are not.
The long-term product must not require separate Sonarr, Radarr, Bazarr, Lidarr,
qBittorrent, SABnzbd, or NZBGet installations.

Provide a responsive web interface and an HTTP API usable by the native iOS app.
Automation must keep working without an open browser or phone app, recover after
restarts, and explain failures clearly. Preserve existing media during migration.

## Decisions

- Frontend: React and TypeScript with Vite, shadcn/ui, and Tailwind CSS.
- Backend: Go with the standard HTTP server and pgx for PostgreSQL access.
- Database: PostgreSQL is the recommended database for local development,
  integration tests, and deployed installations. Use the same schema and migrations
  across environments.
- Distribution: Docker Compose ships Constellarr with a separate PostgreSQL service
  and a persistent database volume.
- Downloads: built-in torrent and NZB/Usenet handling, including resumable transfers,
  torrent seeding controls, Usenet verification/repair, and archive extraction.
- Production embeds the built frontend in the Go binary. Local development uses
  Vite's API proxy and PostgreSQL from Compose.
- Usenet uses nntppool with its fast yEnc decoder; RAR extraction uses rardecode.
  PAR2 repair runs through the bundled par2cmdline helper. Keep external download
  clients out of the required stack.
- Treat stack recommendations and proposed milestones as proposals until agreed.

## Repository workflow

- `frontend/` owns the web interface; `backend/` owns the API and embedded assets.
- Run `make setup`, then `make dev` for local development. Native builds require
  a C compiler for the yEnc decoder and `par2` on PATH for verification/repair.
- Run `make check` for lint, builds, and Go checks; `make test-integration` exercises PostgreSQL.
- Run `make build` for the native binary or `make up` for the distributed Compose setup.
- Generated frontend assets, binaries, dependencies, and `.env` stay untracked.

## First milestone

Download a usable movie through NZBGeek and Frugal Usenet using Constellarr's
built-in Usenet pipeline. These are the initial reference integrations.

- Configure and test NZBGeek API access and Frugal Usenet access separately.
- Keep indexer URLs, NNTP hosts, TLS ports, and connection limits configurable.
- Search movie releases, select one manually, and retrieve its NZB.
- Download articles over authenticated NNTP with TLS, decode and assemble files,
  verify and repair where possible, and extract archives into a configured destination.
- Persist progress in PostgreSQL and on disk so an interrupted download can resume.
- Show transfer and processing stages accurately; finish only when the usable movie
  file is ready. Explain failures and support retry without duplicate work.
- Keep credentials in runtime configuration and out of repository files and logs.
- Torrent support, TV, music, and broader automation remain in the product scope;
  they are not prerequisites for this milestone.

## Public repository rules

- No assistant, model, or tool attribution in repository content, commits, pull
  requests, release notes, or branch names.
- Never add co-authorship trailers or generated-by signatures. Use the configured
  Git identity and plain, descriptive branch names without tool prefixes.
- Keep credentials, personal server details, private repository content, and real
  user data out of committed files, fixtures, screenshots, and logs.

## Implementation

- Finish the requested functionality or visual result with the fewest clear lines
  of code possible. Prefer deletion, reuse, and direct solutions.
- Keep ordinary formatting and readable names; do not minify code to reduce line count.
- Code comments must be single-line and explain only non-obvious intent. No block
  comments, explanatory essays, section banners, or comments that repeat the code.
- Add abstractions, dependencies, services, and configuration only when a concrete
  requirement justifies them. Avoid speculative frameworks and duplicate models.
- Preserve correct behavior, useful errors, data integrity, and accessible UI while
  keeping the implementation concise.

## Verification

- Prioritize integration tests for indexers, metadata providers, transfer engines,
  subtitle providers, Jellyfin, and the public API.
- Add focused regression tests for corner cases found during implementation or
  manual testing, especially interrupted transfers, damaged downloads, duplicate
  work, torrent seeding after import, and file imports.
- Do not test every helper, trivial component, or implementation detail. No coverage
  targets or redundant snapshots.
- Use small, sanitized fixtures for external contracts; real-service checks must
  be opt-in and use credentials supplied outside the repository.
- Run checks relevant to the change. Verify UI changes in the browser and report
  what was exercised and any remaining limitations.
