# Subtitles

The subtitle service inventories sidecar files for catalog movies and episodes,
searches and downloads from subtitle providers, synchronizes timing with the bundled
`ffsubsync` helper, translates with an OpenAI-compatible service, and persists wanted
state, jobs, history, and failures so automation works without a browser.

Backend package: `backend/internal/subtitles`
Migration: `backend/internal/downloads/migrations/009_subtitles.sql`
Frontend: `frontend/src/lib/subtitles-api.ts`, `frontend/src/components/subtitles-*.tsx`

## Integration

The parent server owns lifecycle wiring and route registration. A typical setup:

```go
subtitlesService, err := subtitles.New(ctx, pool, subtitles.Options{
    Movies: movieLibrary, // *movies.Service, optional
    TV:     tvLibrary,    // *tv.Service, optional
})
if err != nil {
    return err
}
subtitlesService.Start(ctx)      // durable job worker + recurring scanner/search
defer subtitlesService.Close()
subtitlesService.Register(mux)   // routes below /api/v1
```

- `New(ctx, pool, opts)` seeds `subtitle_config` and a default `en` language profile.
  It must run after the shared migrations are applied (`downloads.New` runs them).
- `Options.Catalog` overrides the movie/TV adapters and `Options.Translator` injects a
  shared AI provider; `Service.SetTranslator` replaces it at runtime. When no translator
  is injected, the service builds one from its own `ai` configuration.
- `Start` recovers jobs left `running` by a previous process, then runs one job at a time
  and checks the scan/search schedule every 30 seconds. `Close` cancels the worker and
  any running helper process group.
- The parent `api.New` already enforces host, origin, and content-type checks before the
  handlers registered by `Register`.

## Sidecar rules

Sidecars sit next to the video and share its stem:

- `video.en.srt`, `video.pt-BR.forced.srt`, `video.en.hi.vtt`, `video.en.sdh.ass` (`sdh` is an `hi` alias)
- `video.srt` is tracked as a language-less sidecar and never satisfies a wanted language
- Languages are canonicalized to BCP-47 with `golang.org/x/text/language`, including ISO 639-2/3
  and bibliographic aliases (`eng` -> `en`, `gre` -> `el`, `rum` -> `ro`, `chi` -> `zh`), while
  region and script subtags are preserved (`pt_br` -> `pt-BR`, `zh-Hans` stays `zh-Hans`)
- A hearing-impaired file satisfies a plain request; a plain file never satisfies a HI request,
  and forced subtitles stay an exact variant
- Recognized formats: `.srt`, `.vtt`, `.ass`, `.ssa`; payloads must be NUL-free UTF-8
- Writes stage a temporary file, validate it, recycle the previous file into `root/.recycle`,
  then rename into place; a failed rename restores the recycled file
- Paths are resolved through `os.Root` and pinned, so traversal, symlinks, absolute
  paths, and files that do not belong to the video are rejected

Reusable helpers for parent move/rename integration (no library package changes):

- `ParseSidecarName(videoRel, name)`, `SidecarFileName(videoRel, language, forced, hi, format)`
- `MoveSidecars(rootPath, fromVideo, toVideo)` renames a video's sidecars after the video moved
- `Service.MoveVideoSidecars(ctx, kind, id, from, to)` does the same and updates stored paths
- `ValidateSidecarPath`, `ResolveRelativePath`, `NormalizeLanguage`

## Providers

One adapter is implemented: OpenSubtitles.com REST (`type: "opensubtitles"`), with a
configurable endpoint, username/password, and API key. Search reuses catalog IMDb
identifiers (`imdb_id` for movies, `parent_imdb_id` + season/episode for episodes) with a
title fallback. Downloads are bounded (8 MiB), non-UTF-8 or unparseable payloads are
rejected, and quota (HTTP 406), rate limit (HTTP 429), and authentication failures map to
typed errors with credential-free messages. Tokens are cached in memory and refreshed once
after a 401. No unofficial no-key provider is included because none is officially supported.

Results are scored by language/region match, variant match (`forced`, HI), format,
downloads, and rating; the auto-download cutoff defaults to 55.

## Synchronization

Modes: `offset` and `fps` run in-process (byte-exact rewrite of timestamp fields only);
`audio` and `reference` run `ffsubsync` with a video/audio reference or a reference
subtitle. The helper is invoked with fixed arguments through `exec.CommandContext`
(never a shell), a configurable timeout, a killed process group on timeout, and 64 KiB of
captured output:

```
ffsubsync <reference> -i <input> -o <output> --max-offset-seconds N --output-encoding utf-8
  [--skip-sync-on-low-quality --min-score S --quality-max-offset-seconds Q]
  [--max-framerate-deviation D] [--no-fix-framerate] [--gss] [--vad M] [--ffmpeg-path DIR]
```

- `maxOffsetSeconds` bounds the search; `minScore` and `qualityMaxOffsetSeconds` enable the
  low-quality guard. A rejected alignment fails the job and nothing is published, even though
  ffsubsync still writes an unmodified output file on rejection.
- `audioStream` is optional: omitted or negative means automatic. An explicit index extracts
  that embedded audio track with ffmpeg (bounded by `sync.audioReferenceSeconds`) first.
- `audioStream >= 0` extracts a bounded mono 16 kHz WAV with `ffmpeg` first, then syncs
  against it; otherwise the video is passed directly for VAD.
- `ffmpeg`/`ffprobe` also power `GET .../streams` and embedded subtitle extraction
  (`-map 0:s:N -f srt`). Temporary files live in the system temp directory and are removed.
- Embedded stream variants come from the stream disposition plus title tokens (`SDH`, `HI`,
  `hearing impaired`, `forced`), so extracting an English SDH track stages `video.en.hi.srt`
  and a forced Korean track writes `video.ko.forced.srt`; the flags survive preview, apply, and
  direct extraction alike.
- When `ffsubsync` is missing, the service returns an actionable unavailable error naming
  the helper path setting; it never reports a fake success.
- Provider requests carry a finite client timeout, and a redirect that would replay the
  `Api-Key`/`Authorization` header to another origin is refused (same-origin redirects such as
  signed download links still work).
- Verified against the bundled real helper `ffsubsync 0.5.1` plus `ffmpeg 9.0.2`: reference
  sync, the default `subs_then_webrtc` VAD, an explicit `--vad webrtc` audio pass,
  `--ffmpeg-path`, low-quality rejection, and embedded stream extraction/audio extraction on a
  1080p MKV. ffsubsync's rich logger hard-wraps long warnings, so metric and low-quality
  parsing normalizes the log before matching. `--vad` accepts the 0.5.x names including
  `subs_then_webrtc`, `subs_then_auditok`, and `subs_then_silero`.

## Translation

Translation uses the shared OpenAI-compatible provider configured once under
Connections → AI (`/api/v1/ai/config`). The parent injects it through
`subtitles.Options{Translator: ...}` (see `cmd/constellarr/shared_translator.go`); an injected
translator always wins, so translation works even when the legacy subtitle AI fields are
disabled. The subtitle settings UI only reads the shared provider status and links to
`#connections`; it retains subtitle-specific budgets and chunking. For deployments without a
shared provider, the legacy `ai.baseURL`/`ai.apiKey`/`ai.model` fields still act as a
fallback when `ai.enabled` is set (API-only, no longer exposed in the UI).

Cues are chunked by characters and count; the model must return
`{"language": "...","cues":[{"id","text"}]}`.

Integrity rules enforced before anything is saved:

- every requested cue ID appears exactly once; extra, duplicate, or missing cues are rejected
- the reported language must match the requested language (region-tolerant)
- timestamps and cue order are never sent to the model and are byte-identical in the output;
  only cue text fields are replaced, so formatting outside the text stays byte-exact
- inline markup (`<i>`, `{...}`) and uppercase bracketed labels such as `[MUSIC]` must survive
- invalid replies are retried at most twice with a corrective instruction, then the job fails
  closed with no partial output

Budgets (`ai.maxRequests`, `ai.maxTotalTokens`, `ai.maxTokens`, `ai.maxCharacters`) abort a
job when exceeded. Finished translations are staged as `subtitle_outputs` and must be applied
through `POST /api/v1/subtitles/outputs/{id}/apply` (atomic publish with recycle) or discarded.

## Automation

`Start` schedules a library scan every `scanMinutes` and searches due wanted variants.
Desired languages come from a language profile (`languages[].forced`/`hi` add forced and
hearing-impaired variants; `cutoff` stops searching once enough languages are satisfied).
A download is queued only when `autoDownload` is enabled and the best score reaches
`cutoffScore`. Failures are recorded per variant with attempt counters and a bounded retry
backoff; provider quota/rate-limit errors defer the retry instead of hot-looping. Wanted rows
are recomputed on every scan, and `satisfied`/`cutoff`/`ignored` variants are not searched.

## HTTP API

All routes are below `/api/v1`. Errors use `{"error": "..."}`; typed failures map to 400
(invalid/unsafe), 404, 409 (conflict/not configured/budget), 422 (cue integrity, language,
low-quality alignment), 429 (quota/rate limit), 503 (missing helper), 504 (timeout).

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/subtitles` | library inventory; filters `q`, `kind`, `status`, `missing`, `limit` |
| POST | `/subtitles/scan` | start an inventory scan (202; 409 when one is running) |
| GET | `/subtitles/wanted` | pending wanted variants with video identity |
| GET | `/subtitles/history` | history; optional `kind`, `id`, `limit` |
| GET | `/subtitles/jobs`, `/subtitles/jobs/{id}` | durable jobs (add `?active=1` for open ones) |
| POST | `/subtitles/jobs/{id}/cancel` | cancel a queued or running job |
| GET | `/subtitles/providers`, POST `/subtitles/providers/test` | provider health and connectivity checks |
| GET/PUT | `/subtitle-config`, POST `/subtitle-config/test` | configuration; secrets are write-only |
| GET/PUT | `/subtitle-profiles`, DELETE `/subtitle-profiles/{id}` | language profiles |
| GET | `/subtitles/{kind}/{id}` | detail: sidecars, wanted, staged outputs, history |
| POST | `/subtitles/{kind}/{id}/search` | provider search; optional `languages` override |
| POST | `/subtitles/{kind}/{id}/download` | queue a provider result download |
| POST | `/subtitles/{kind}/{id}/sync` | queue an offset/fps/audio/reference synchronization |
| POST | `/subtitles/{kind}/{id}/translate` | queue an AI translation (staged for review) |
| POST | `/subtitles/{kind}/{id}/extract` | extract an embedded subtitle stream |
| GET | `/subtitles/{kind}/{id}/streams` | embedded audio/subtitle tracks |
| GET | `/subtitles/{kind}/{id}/file?path=...` | download a sidecar scoped to the video |
| POST/DELETE | `/subtitles/{kind}/{id}/assignment` | bind or reset the language profile |
| GET | `/subtitles/outputs/{id}`, POST `/subtitles/outputs/{id}/apply`, DELETE `/subtitles/outputs/{id}` | review staged subtitles |

`kind` is `movie` or `episode`. Configuration responses replace `password`/`apiKey` with
`passwordSet`/`apiKeySet`; blank secrets on save keep the stored value.

## Persistence

`subtitle_config`, `subtitle_profiles`, `subtitle_assignments`, `subtitle_sidecars`,
`subtitle_wanted`, `subtitle_history`, `subtitle_jobs`, `subtitle_outputs`,
`subtitle_failures`. Jobs and outputs are durable, so translation and download work survives
a restart; running jobs are requeued at startup. Configuration rows loaded from the database
are filled with the same defaults as a fresh seed, so a stored zero (for example
`sync.maxEmbeddedStreamIndex`, default 64) behaves like an unset value. Job and output state is
always available through `GET /api/v1/subtitles/jobs/{id}` and `GET /api/v1/subtitles/outputs/{id}`
even when a browser dialog was reloaded or closed.

Manual `POST /api/v1/subtitles/scan` runs under the service context and is tracked, so `Close`
cancels it and waits for it to finish; scans launched after shutdown are refused. A restore that
drains HTTP and then stops workers cannot leave a subtitle scan writing to the database.

## Permissions

The UI gates itself with the central permissions: inventory, detail, history, jobs, wanted,
streams, and file reads need `subtitles.read`; search, download, sync, translate, extract,
scan, job cancel, output apply/discard, and assignments need `subtitles.write`; subtitle
configuration, language profiles, and provider settings need `settings.read` /
`settings.write`. Read-only roles never request config, provider, or profile endpoints.
Necessary parent update: `internal/auth/permissions.go` currently maps
`/api/v1/subtitle-config`, `/api/v1/subtitle-profiles`, and `/api/v1/subtitles/providers` to
`subtitles.read`/`subtitles.write`; those routes should move to `settings.read`/`settings.write`
(`/subtitles/providers/test` to `settings.write`) so the UI and server agree.

## Verification

- `go test ./internal/subtitles/` runs provider `httptest` contracts, cue-integrity and
  adversarial-path tests, offset/fps rewrites, scripted helper runs, and (when `ffmpeg` is
  installed) real stream probing/extraction.
- `TEST_FFSUBSYNC_PATH=/path/to/ffsubsync [TEST_MEDIA_VIDEO=/path/video.mkv] go test ./internal/subtitles/ -run TestRealHelper`
  is the opt-in real-helper check; the video is only read and outputs go to temporary directories.
- `TestCatalogExtractionSatisfiesWanted` builds a real movie catalog entry and a small MKV with
  plain/eng-SDH/forced tracks, then verifies stream flags, extraction, apply, and the wanted
  count dropping to zero; `TestLegacyZeroStreamIndexConfigStillExtracts` and the tracked-scan
  shutdown tests cover the configuration and lifecycle regressions.
- `TEST_DATABASE_URL=... go test ./internal/subtitles/` adds isolated-schema integration
  tests for the scan/wanted/download/sync/translate/apply/cancel flows and the HTTP handlers.
  The integration tests create and drop their own schema; they never touch existing data.
- Frontend: `npm --prefix frontend run lint && npm --prefix frontend run build`.

## Limitations

- Only the OpenSubtitles.com provider is implemented; other providers need new adapters
  (`provider` interface) and configuration types.
- Subtitle payloads must already be UTF-8; legacy encodings are rejected rather than converted.
- Audio VAD sync requires the bundled `ffsubsync`; only offset/fps edits work without it.
- The subtitle UI no longer accepts a separate AI key; translation depends on the shared AI
  provider from Connections unless the legacy API configuration is set.
- Language detection of existing sidecars is filename-based (canonicalized as above).
