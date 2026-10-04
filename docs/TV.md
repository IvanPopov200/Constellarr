# TV management

TV Shows manages series, seasons, specials, and episodes through Constellarr's
built-in Usenet queue. Jellyfin remains the player.

- Series discovery, IMDb metadata and posters, episode air dates, and metadata refresh.
- Series, season, and individual episode monitoring; all, future, missing, existing,
  first-season, latest-season, and unmonitored selection when adding a series.
- Poster library, metadata filters and sorting, bulk edits, wanted episodes,
  an air-date calendar, and acquisition history.
- Shared quality profiles and scored release rules, interactive episode and season-pack
  searches, clear rejection reasons, and automatic RSS and scheduled acquisition.
- Episode-aware imports, season folders, configurable naming, rename previews,
  existing-library scans, manual episode matching, and recoverable quality upgrades.
- Persistent download ownership and import journals that survive restarts and keep
  TV files out of movie adoption. Imports preserve unrelated episodes and source media.
- Jellyfin refresh and TV and episode NFO sidecars after successful imports.

TV reuses the movie workspace's metadata and Jellyfin connection settings and shared
quality profiles. TV storage roots and naming are configured separately.
Ambiguous files and unsupported absolute episode numbering require manual matching;
unknown air dates remain unknown. Automation runs on the server without an open browser.

Metadata contract: [OMDb API](https://www.omdbapi.com/).
