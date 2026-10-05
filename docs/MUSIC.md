# Music

The music vertical manages artists, albums, and tracks: metadata from MusicBrainz,
release search through NZBGeek's music categories, built-in Usenet downloads, and
crash-safe imports into configured library roots with ffprobe verification.

## Configuration

Configuration is stored in the database (`music_config`) and managed through
`/api/v1/music/config`. There are no per-user keys; the indexer endpoint and key come
from the shared download settings.

| Setting | Default | Notes |
| --- | --- | --- |
| `rootFolders` | `<DOWNLOAD_DIR>/library/music` | Absolute paths only, at most 32 |
| `folderTemplate` | `{artist}/{album} ({year})` | Tokens below; no file-level tokens |
| `fileTemplate` | `{track:02} {title}` | Multidisc default becomes `{disc}-{track:02} {title}` |
| `importMode` | `copy` | `copy`, `hardlink`, or `move`; hardlink keeps torrent sources seeding |
| `ffprobePath` | `FFPROBE_PATH` or `ffprobe` | Real metadata is preferred; file names are the fallback |
| `musicBrainzURL` | `https://musicbrainz.org/ws/2/` | Official JSON service, configurable host |
| `musicBrainzRateMs` | `1100` | Minimum spacing between MusicBrainz requests |
| `coverArtURL` | `https://coverartarchive.org/` | Empty disables the optional image provider |
| `pollMinutes` | `30` | Feed/monitor interval |
| `searchHours` | `12` | Per-album scheduled search interval |
| `retryFailed` | `true` | Retry wanted albums after a failure with another release |
| `qualityProfiles` | Standard, Lossless, Any audio | Format preference, lossless floor, bitrate floor, cutoff, upgrades |
| `defaultProfileId` | `standard` | Profile used when an artist or album does not name one |

Template tokens: `{artist}` (album artist), `{trackArtist}`, `{album}`, `{year}`,
`{date}`, `{type}`, `{format}`, `{quality}`, `{track}` (`{track:02}` pads), `{disc}`,
`{title}`, `{mbAlbumId}`, `{mbArtistId}`, `{original}`. Values are sanitized, so
`AC/DC` becomes `AC DC` instead of another folder level. Folder templates may use `/`
for nesting; file templates may not.

## API

All routes live below `/api/v1/music` and use JSON. Errors are `{"error": "..."}` with
`400` for invalid input, `404` for unknown IDs, `409` for state conflicts, and `503`
when a provider is not configured.

| Method | Route | Purpose |
| --- | --- | --- |
| GET/PUT | `/music/config` | Read or replace configuration |
| POST | `/music/config/test` | Test MusicBrainz, the indexer, and ffprobe |
| GET/POST | `/music/artists` | List artists with their albums; add an artist (MusicBrainz ID or manual name) |
| GET/PUT/DELETE | `/music/artists/{id}` | Artist detail; update monitoring/profile/root/name; delete (`?deleteFiles=true` recycles files) |
| POST | `/music/artists/{id}/monitor` | Set `{monitored, monitorOption}` (`all`, `future`, `missing`, `none`) |
| POST | `/music/artists/{id}/refresh` | Re-read metadata and pick up new releases |
| GET | `/music/artists/{id}/history` | Artist events |
| GET/POST | `/music/albums` | List the album library; add an album for an artist |
| GET/PUT/DELETE | `/music/albums/{id}` | Album detail (tracks + files); update; delete (`?deleteFiles=true` recycles files) |
| POST | `/music/albums/{id}/monitor` | Set `{monitored, profileId}` |
| POST | `/music/albums/{id}/search` | Scored release candidates with quality decisions |
| POST | `/music/albums/{id}/grab` | `{releaseId, override}` queues the download and records ownership |
| POST | `/music/albums/{id}/refresh` | Re-read release metadata and the tracklist |
| POST | `/music/albums/{id}/rename` | `{preview}` renders or applies the naming templates |
| GET | `/music/albums/{id}/history` | Album events |
| GET | `/music/albums/{id}/cover` | Proxied Cover Art Archive image (when enabled) |
| GET | `/music/history` | Newest events across the library |
| GET | `/music/discover?q=&page=` | MusicBrainz artist and album search |
| GET | `/music/search?q=&albumId=` | Manual release search; `albumId` applies the album profile and duplicate checks |
| GET | `/music/wanted` | Monitored albums missing files or below cutoff |
| GET | `/music/calendar` | Album releases within a thirty-day window backwards and one year forwards |
| POST | `/music/scan` | `{rootId}` lists folder candidates with their catalog match |
| POST | `/music/import` | `{path, albumId?}` imports a folder or file below a music root, the download directory, or a torrent payload folder |
| POST | `/music/sync` | `{force}` runs the automation pass on demand |

## Behaviour

- **Search.** Release search merges the Newznab audio search (`t=music`, category
  `3000`, falling back to a category `3000` search) with the configured torrent
  indexers. Half of the pair answering is a success: results are merged, deduplicated,
  and a single provider failure is silent while both failing returns the provider
  errors. Non-audio Newznab categories are rejected, and torrent names that look like
  video or ebooks are dropped, so films and shows never leak into music. Releases carry
  `protocol`, `source`, and `seeders`; torrents without seeders are offered but marked
  "the torrent has no seeders" and are never grabbed automatically. Decisions compare
  format, lossless flag, bitrate, size, and the files already imported; with files in
  place only a genuine improvement is allowed, and equal quality is ordered by seeders.
- **Monitoring.** The service runs a monitor loop without a browser: it imports
  finished downloads, retries failed albums with another release, refreshes monitored
  artists for new releases, matches the recent audio feed, and runs scheduled searches
  and quality upgrades. Failed releases are blocklisted automatically.
- **Concurrent writes.** Every writer patches only the fields it owns through
  `PatchAlbum`/`PatchArtistData`, so a slow provider search or refresh can never replace
  an import that landed meanwhile, and an import never overwrites a monitoring, profile,
  or root change. Provider calls always happen outside the database lock; only the merge
  of current state is serialized per album.
- **Ownership.** A grab stores a durable `music_acquisitions` row and marks the
  download `media_type = 'music'`. Movie and TV adoption triggers refuse music
  downloads, and music refuses downloads another library already owns, with the
  original ownership preserved.
- **Imports.** `copy`, `hardlink` (torrent friendly) and `move` modes stage every file
  through a temp name and publish it without replacing a concurrent arrival. Every
  acquisition import asks `downloads.Manager.ImportMode` for the effective mode, so a
  torrent `move` becomes a `hardlink` and the payload keeps seeding; manual imports
  from a torrent payload folder are protected the same way. Replaced
  originals move to `.recycle` instead of being deleted. Importing is idempotent, so an
  interrupted (crashed) import is retried on the next pass and identical files are
  adopted rather than duplicated. Files whose probe shows a video stream are rejected,
  as are symlinks, size changes after planning, and sources naming two albums. Cue
  sheets, `.lrc` lyrics, and rip logs travel next to the imported audio, including
  album-level files. An import
  that keeps failing gives up after three attempts, blocklists the release, and leaves
  the album ready for another candidate.
- **Identity.** Tracks match by MusicBrainz recording ID first, then disc/number, then
  a unique normalized title; unmatched files become bonus tracks. Ambiguous files fail
  the import instead of guessing. Multidisc layouts keep their disc number.
- **Cover art.** Source images win; otherwise the configured Cover Art Archive image
  is stored as `cover.jpg`/`cover.png` in the album folder.

## Tests

Unit contract tests (`internal/music/*_test.go`) use `httptest` for MusicBrainz and
Newznab, plus real `ffprobe`/`ffmpeg` fixtures when the helpers are installed (skipped
otherwise). Integration tests need an isolated PostgreSQL database:

```sh
TEST_DATABASE_URL='postgresql://user:pass@127.0.0.1:5432/music_test?sslmode=disable' \
  go -C backend test ./internal/music/ -count=1 -v
```

They create and drop their own schema, so no existing data is touched.

## Permissions

The music page and its routes follow the shared permission model; the server rules are
the source of truth and the UI hides what it cannot do.

| Permission | Music capability |
| --- | --- |
| `library.read` | Browse artists, albums, tracks, wanted, calendar, history, and read-only release search (`GET /music/search`) |
| `library.write` | Add, edit, monitor, refresh, rename, delete, scan, import, sync, the album release search (`POST /music/albums/{id}/search`), and grabbing a release (`POST /music/albums/{id}/grab`) |
| `settings.read` | Read `/music/config`, see the Settings tab, and open Discover/Scan (they need profile and root lists) |
| `settings.write` | Save configuration and run `/music/config/test` |

Album grabs use `library.write`, exactly like movies and TV and the central route rules;
`downloads.write` stays reserved for direct queue controls such as retries and torrent
actions.

A viewer with `library.read` browses the library without any configuration request, so
no settings error appears. Accounts without `library.read` see a short explanation
instead of a failing request.

## Application integration

- `cmd/constellarr` builds the service with `music.New(ctx, pool, manager)`, starts its
  automation, and closes it on shutdown; the service needs no movie store and no browser.
- `internal/api` serves the routes through `service.Register(mux)`; the shared wrapper adds
  host, origin, and content-type checks, and the permission rules gate each route.
- The web app renders `MusicPage` on the music route (library, wanted, calendar, history,
  settings) and mounts `MusicSettings` in the Connections and Storage pages, where
  `<MusicSettings section="connections" />` shows providers, automation intervals, and
  quality profiles while `<MusicSettings section="storage" />` shows roots, naming, import
  mode, and the ffprobe helper. Each card loads its own configuration and hides itself
  without `settings.read`.
- Searches reach a real library through the download manager: usenet grabs use the Newznab
  audio search and torrent grabs use the configured torrent indexers, both recorded as
  music acquisitions before the import runs.
- A selected-album request (`AddReleaseGroup`) monitors only that album: the artist stays
  unmonitored with monitor option `none`, while automation still searches and grabs the
  album because scheduling is album-driven.
