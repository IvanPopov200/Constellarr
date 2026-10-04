# Movie management

The Movies workspace owns the complete path from discovery to an organized library.
The implementation scope is:

- Poster and table libraries showing downloaded, missing, wanted, and upgrading movies.
- Metadata search, IMDb identity and ratings, cast, directors, dates, genres,
  runtime, languages, certification, posters, and refresh.
- Sorting and filters over metadata, monitoring, tags, quality, and availability.
- Individual and bulk monitoring, release availability, scheduled searches,
  RSS discovery, and an upcoming-release calendar.
- Ordered quality profiles, upgrade cutoffs, size and language limits, and scored
  release rules for codecs, audio, HDR, editions, and preferred or rejected terms.
- Interactive search with rejection explanations and automatic release selection.
- Automatic imports, root folders, configurable folder/file naming, rename previews,
  cross-filesystem handling, and recoverable quality replacement.
- Existing-library scans, manual matching, missing-file detection, and duplicate prevention.
- Download/import history, failed-release blocking, and alternate-release recovery.
- Bulk editing, tags, collections, and watchlist imports with scheduled synchronization.
- Jellyfin library refresh, NFO sidecars, and webhook notifications after imports.

Metadata and quality parsing are shared backend packages so TV, music, and subtitles
can reuse their contracts. Importing and safe path handling belong to the shared
library package. Movie scheduling stays server-side and survives restarts.

Metadata uses configurable OMDb access over HTTPS. Provider credentials stay on the
server. An IMDb rating remains distinct from other rating sources; unavailable
values stay unknown. Saved metadata supports browsing while the provider is offline.

Reference behavior: [Radarr settings](https://wiki.servarr.com/radarr/settings)
and [OMDb API](https://www.omdbapi.com/).
