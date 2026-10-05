# Alpine 3.24 runtime packages

Constellarr's runtime image installs its Alpine packages from
`https://dl-cdn.alpinelinux.org/alpine/v3.24/{main,community}`. For every
package origin in the image this directory stores:

- `<repo>/<origin>/APKBUILD-3.24-stable` — the verbatim recipe from the
  `3.24-stable` branch of https://gitlab.alpinelinux.org/alpine/aports, with
  the declared license, upstream source URL(s), and checksums; and
- `<repo>/<origin>/` — the upstream license text(s) for the pinned version.
  Origins whose upstream publishes no license file carry a `README.md`
  describing where the license grant appears.

Shared license texts are in `common/` (GPL, LGPL, MPL, FTL). The package and
license inventory is in `THIRD_PARTY_NOTICES.md`; corresponding-source URLs and
checksums for the GPL/LGPL origins are in `../source-manifest.json`.
