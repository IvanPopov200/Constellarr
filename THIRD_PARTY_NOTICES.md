# Third-Party Notices

Constellarr redistributes the third-party components listed below in its runtime
artifacts: the `constellarr` server binary (Go, built with `CGO_ENABLED=1` for
linux/arm64 and linux/amd64), the frontend assets embedded in that binary, and
the Alpine 3.24 runtime image contents — the `par2cmdline`, `ffmpeg`,
PostgreSQL 18 client, Python 3.14, NumPy, and `ca-certificates` packages with
their shared-library dependencies, plus the pip-installed `ffsubsync` subtitle
tooling and its Python dependencies.

Pinned versions are taken from `backend/go.mod`, `backend/go.sum`,
`frontend/package-lock.json`, the `Dockerfile` package installs, the Alpine 3.24
`APKINDEX`/`aports` metadata, and the installed Python package metadata. The
upstream license/copyright texts required by those components are reproduced
under `third_party/licenses/`.

## Layout

- `third_party/licenses/go/<module>@<version>/` — license text(s) copied from
  the module sources pinned in `backend/go.mod` (Go module cache). Bundled
  subcomponents keep their own license files inside the module directory
  (`github.com/anacrolix/torrent/webtorrent/LICENSE`).
- `third_party/licenses/npm/<package>@<version>/` — license text(s) copied from
  the package tarball pinned in `frontend/package-lock.json`.
- `third_party/licenses/python/<package>@<version>/` — license text(s) taken
  from the installed package metadata or, where a wheel ships none, from the
  package's upstream repository.
- `third_party/licenses/alpine/<main|community>/<origin>/` — the verbatim
  Alpine 3.24 recipe (`APKBUILD-3.24-stable`) and the upstream license text(s)
  for that origin, for every runtime package origin. Shared license texts are
  in `third_party/licenses/alpine/common/`; origins whose upstream publishes no
  license file carry a `README.md` describing where the license grant appears.
- `third_party/source-manifest.json` — pinned URLs and checksums for the
  corresponding source archives, Alpine patches, and aports source files of
  the GPL/LGPL components (plus the Python `auditok` package).
- `third_party/collect_sources.py` — downloads and checksum-verifies those
  files to an external output directory; it does not extract or execute them.
- `third_party/licenses/par2cmdline/` — GPL v2 text, the Alpine 3.24 build
  recipe, and the upstream source archive for `par2cmdline`.

## Backend — Go modules linked into the server binary

Build configuration (see `Dockerfile`): `CGO_ENABLED=1`, `GOOS=linux`. The table
is the complete set of third-party modules providing packages in the binary's
build list (`go list -deps ./cmd/constellarr` from the `backend` module); the
set is identical for `linux/arm64` and `linux/amd64` and includes the
`github.com/anacrolix/torrent` dependency tree. Re-verify with
`go list -deps ./cmd/constellarr` after any `go.mod`/`go.sum` change.

License identifiers are derived from the license texts shipped by the pinned
module sources (79 of the 80 modules ship one or more license files; the
exceptions are noted below).

| Module | Version | License | Source |
| --- | --- | --- | --- |
| `github.com/alecthomas/atomic` | v0.1.0-alpha2 | MIT | https://pkg.go.dev/github.com/alecthomas/atomic@v0.1.0-alpha2 |
| `github.com/anacrolix/btree` | v0.0.0-20251201064447-d86c3fa41bd8 | Apache-2.0 | https://pkg.go.dev/github.com/anacrolix/btree@v0.0.0-20251201064447-d86c3fa41bd8 |
| `github.com/anacrolix/chansync` | v0.7.0 | MIT | https://pkg.go.dev/github.com/anacrolix/chansync@v0.7.0 |
| `github.com/anacrolix/dht/v2` | v2.23.0 | MPL-2.0 | https://pkg.go.dev/github.com/anacrolix/dht/v2@v2.23.0 |
| `github.com/anacrolix/envpprof` | v1.4.0 | MIT | https://pkg.go.dev/github.com/anacrolix/envpprof@v1.4.0 |
| `github.com/anacrolix/generics` | v0.1.1-0.20251125230353-15d98d46693b | MPL-2.0 | https://pkg.go.dev/github.com/anacrolix/generics@v0.1.1-0.20251125230353-15d98d46693b |
| `github.com/anacrolix/go-libutp` | v1.3.2 | MIT | https://pkg.go.dev/github.com/anacrolix/go-libutp@v1.3.2 |
| `github.com/anacrolix/log` | v0.17.1-0.20251118025802-918f1157b7bb | MPL-2.0 | https://pkg.go.dev/github.com/anacrolix/log@v0.17.1-0.20251118025802-918f1157b7bb |
| `github.com/anacrolix/missinggo` | v1.3.0 | MIT | https://pkg.go.dev/github.com/anacrolix/missinggo@v1.3.0 |
| `github.com/anacrolix/missinggo/perf` | v1.0.0 | MIT | https://pkg.go.dev/github.com/anacrolix/missinggo/perf@v1.0.0 |
| `github.com/anacrolix/missinggo/v2` | v2.10.0 | MIT | https://pkg.go.dev/github.com/anacrolix/missinggo/v2@v2.10.0 |
| `github.com/anacrolix/mmsg` | v1.0.1 | MPL-2.0 | https://pkg.go.dev/github.com/anacrolix/mmsg@v1.0.1 |
| `github.com/anacrolix/multiless` | v0.4.0 | MPL-2.0 | https://pkg.go.dev/github.com/anacrolix/multiless@v0.4.0 |
| `github.com/anacrolix/stm` | v0.5.0 | MIT | https://pkg.go.dev/github.com/anacrolix/stm@v0.5.0 |
| `github.com/anacrolix/sync` | v0.5.5-0.20251119100342-d78dd1f686f1 | MPL-2.0 | https://pkg.go.dev/github.com/anacrolix/sync@v0.5.5-0.20251119100342-d78dd1f686f1 |
| `github.com/anacrolix/torrent` | v1.61.0 | MPL-2.0 (module); MIT (`webtorrent` package) | https://pkg.go.dev/github.com/anacrolix/torrent@v1.61.0 |
| `github.com/anacrolix/upnp` | v0.1.4 | MPL-2.0 | https://pkg.go.dev/github.com/anacrolix/upnp@v0.1.4 |
| `github.com/bahlo/generic-list-go` | v0.2.0 | BSD-3-Clause | https://pkg.go.dev/github.com/bahlo/generic-list-go@v0.2.0 |
| `github.com/benbjohnson/immutable` | v0.4.1-0.20221220213129-8932b999621d | MIT | https://pkg.go.dev/github.com/benbjohnson/immutable@v0.4.1-0.20221220213129-8932b999621d |
| `github.com/bradfitz/iter` | v0.0.0-20191230175014-e8f45d346db8 | BSD-3-Clause | https://pkg.go.dev/github.com/bradfitz/iter@v0.0.0-20191230175014-e8f45d346db8 |
| `github.com/cespare/xxhash` | v1.1.0 | MIT | https://pkg.go.dev/github.com/cespare/xxhash@v1.1.0 |
| `github.com/davecgh/go-spew` | v1.1.1 | ISC | https://pkg.go.dev/github.com/davecgh/go-spew@v1.1.1 |
| `github.com/dustin/go-humanize` | v1.0.0 | MIT | https://pkg.go.dev/github.com/dustin/go-humanize@v1.0.0 |
| `github.com/edsrzf/mmap-go` | v1.1.0 | BSD-3-Clause | https://pkg.go.dev/github.com/edsrzf/mmap-go@v1.1.0 |
| `github.com/go-llsqlite/adapter` | v0.2.0 | MPL-2.0 | https://pkg.go.dev/github.com/go-llsqlite/adapter@v0.2.0 |
| `github.com/go-llsqlite/crawshaw` | v0.5.6-0.20250312230104-194977a03421 | ISC | https://pkg.go.dev/github.com/go-llsqlite/crawshaw@v0.5.6-0.20250312230104-194977a03421 |
| `github.com/go-logr/logr` | v1.4.3 | Apache-2.0 | https://pkg.go.dev/github.com/go-logr/logr@v1.4.3 |
| `github.com/go-logr/stdr` | v1.2.2 | Apache-2.0 | https://pkg.go.dev/github.com/go-logr/stdr@v1.2.2 |
| `github.com/google/btree` | v1.1.2 | Apache-2.0 | https://pkg.go.dev/github.com/google/btree@v1.1.2 |
| `github.com/google/go-cmp` | v0.7.0 | BSD-3-Clause | https://pkg.go.dev/github.com/google/go-cmp@v0.7.0 |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | https://pkg.go.dev/github.com/google/uuid@v1.6.0 |
| `github.com/gorilla/websocket` | v1.5.0 | BSD-2-Clause | https://pkg.go.dev/github.com/gorilla/websocket@v1.5.0 |
| `github.com/huandu/xstrings` | v1.3.2 | MIT | https://pkg.go.dev/github.com/huandu/xstrings@v1.3.2 |
| `github.com/jackc/pgpassfile` | v1.0.0 | MIT | https://pkg.go.dev/github.com/jackc/pgpassfile@v1.0.0 |
| `github.com/jackc/pgservicefile` | v0.0.0-20240606120523-5a60cdf6a761 | MIT | https://pkg.go.dev/github.com/jackc/pgservicefile@v0.0.0-20240606120523-5a60cdf6a761 |
| `github.com/jackc/pgx/v5` | v5.11.0 | MIT | https://pkg.go.dev/github.com/jackc/pgx/v5@v5.11.0 |
| `github.com/jackc/puddle/v2` | v2.2.2 | MIT | https://pkg.go.dev/github.com/jackc/puddle/v2@v2.2.2 |
| `github.com/javi11/nntppool/v5` | v5.0.0 | MIT | https://pkg.go.dev/github.com/javi11/nntppool/v5@v5.0.0 |
| `github.com/klauspost/cpuid/v2` | v2.2.3 | MIT | https://pkg.go.dev/github.com/klauspost/cpuid/v2@v2.2.3 |
| `github.com/mnightingale/rapidyenc` | v0.0.0-20251128204712-7aafef1eaf1c | MIT (Go wrapper); bundled C components as noted below | https://pkg.go.dev/github.com/mnightingale/rapidyenc@v0.0.0-20251128204712-7aafef1eaf1c |
| `github.com/mr-tron/base58` | v1.2.0 | MIT | https://pkg.go.dev/github.com/mr-tron/base58@v1.2.0 |
| `github.com/multiformats/go-multihash` | v0.2.3 | MIT | https://pkg.go.dev/github.com/multiformats/go-multihash@v0.2.3 |
| `github.com/multiformats/go-varint` | v0.0.6 | MIT | https://pkg.go.dev/github.com/multiformats/go-varint@v0.0.6 |
| `github.com/nwaples/rardecode/v2` | v2.4.1 | BSD-2-Clause | https://pkg.go.dev/github.com/nwaples/rardecode/v2@v2.4.1 |
| `github.com/pion/datachannel` | v1.5.9 | MIT | https://pkg.go.dev/github.com/pion/datachannel@v1.5.9 |
| `github.com/pion/dtls/v3` | v3.0.3 | MIT | https://pkg.go.dev/github.com/pion/dtls/v3@v3.0.3 |
| `github.com/pion/ice/v4` | v4.0.2 | MIT | https://pkg.go.dev/github.com/pion/ice/v4@v4.0.2 |
| `github.com/pion/interceptor` | v0.1.40 | MIT | https://pkg.go.dev/github.com/pion/interceptor@v0.1.40 |
| `github.com/pion/logging` | v0.2.3 | MIT | https://pkg.go.dev/github.com/pion/logging@v0.2.3 |
| `github.com/pion/mdns/v2` | v2.0.7 | MIT | https://pkg.go.dev/github.com/pion/mdns/v2@v2.0.7 |
| `github.com/pion/randutil` | v0.1.0 | MIT | https://pkg.go.dev/github.com/pion/randutil@v0.1.0 |
| `github.com/pion/rtcp` | v1.2.15 | MIT | https://pkg.go.dev/github.com/pion/rtcp@v1.2.15 |
| `github.com/pion/rtp` | v1.8.18 | MIT | https://pkg.go.dev/github.com/pion/rtp@v1.8.18 |
| `github.com/pion/sctp` | v1.8.33 | MIT | https://pkg.go.dev/github.com/pion/sctp@v1.8.33 |
| `github.com/pion/sdp/v3` | v3.0.9 | MIT | https://pkg.go.dev/github.com/pion/sdp/v3@v3.0.9 |
| `github.com/pion/srtp/v3` | v3.0.4 | MIT | https://pkg.go.dev/github.com/pion/srtp/v3@v3.0.4 |
| `github.com/pion/stun/v3` | v3.0.0 | MIT | https://pkg.go.dev/github.com/pion/stun/v3@v3.0.0 |
| `github.com/pion/transport/v3` | v3.0.7 | MIT | https://pkg.go.dev/github.com/pion/transport/v3@v3.0.7 |
| `github.com/pion/turn/v4` | v4.0.0 | MIT | https://pkg.go.dev/github.com/pion/turn/v4@v4.0.0 |
| `github.com/pion/webrtc/v4` | v4.0.0 | MIT | https://pkg.go.dev/github.com/pion/webrtc/v4@v4.0.0 |
| `github.com/pkg/errors` | v0.9.1 | BSD-2-Clause | https://pkg.go.dev/github.com/pkg/errors@v0.9.1 |
| `github.com/protolambda/ctxlock` | v0.1.0 | MIT | https://pkg.go.dev/github.com/protolambda/ctxlock@v0.1.0 |
| `github.com/RoaringBitmap/roaring` | v1.2.3 | Apache-2.0 | https://pkg.go.dev/github.com/RoaringBitmap/roaring@v1.2.3 |
| `github.com/rs/dnscache` | v0.0.0-20211102005908-e0241e321417 | MIT | https://pkg.go.dev/github.com/rs/dnscache@v0.0.0-20211102005908-e0241e321417 |
| `github.com/spaolacci/murmur3` | v1.1.0 | BSD-3-Clause | https://pkg.go.dev/github.com/spaolacci/murmur3@v1.1.0 |
| `github.com/tidwall/btree` | v1.8.1 | MIT | https://pkg.go.dev/github.com/tidwall/btree@v1.8.1 |
| `github.com/wlynxg/anet` | v0.0.3 | BSD-3-Clause | https://pkg.go.dev/github.com/wlynxg/anet@v0.0.3 |
| `go.etcd.io/bbolt` | v1.3.6 | MIT | https://pkg.go.dev/go.etcd.io/bbolt@v1.3.6 |
| `go.opentelemetry.io/auto/sdk` | v1.2.1 | Apache-2.0 | https://pkg.go.dev/go.opentelemetry.io/auto/sdk@v1.2.1 |
| `go.opentelemetry.io/otel` | v1.38.0 | Apache-2.0 | https://pkg.go.dev/go.opentelemetry.io/otel@v1.38.0 |
| `go.opentelemetry.io/otel/metric` | v1.38.0 | Apache-2.0 | https://pkg.go.dev/go.opentelemetry.io/otel/metric@v1.38.0 |
| `go.opentelemetry.io/otel/trace` | v1.38.0 | Apache-2.0 | https://pkg.go.dev/go.opentelemetry.io/otel/trace@v1.38.0 |
| `golang.org/x/crypto` | v0.44.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/crypto@v0.44.0 |
| `golang.org/x/exp` | v0.0.0-20251113190631-e25ba8c21ef6 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/exp@v0.0.0-20251113190631-e25ba8c21ef6 |
| `golang.org/x/net` | v0.47.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/net@v0.47.0 |
| `golang.org/x/sync` | v0.19.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/sync@v0.19.0 |
| `golang.org/x/sys` | v0.39.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/sys@v0.39.0 |
| `golang.org/x/text` | v0.31.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/text@v0.31.0 |
| `golang.org/x/time` | v0.14.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/time@v0.14.0 |
| `lukechampine.com/blake3` | v1.1.6 | MIT | https://pkg.go.dev/lukechampine.com/blake3@v1.1.6 |

Notes on the Go module table:

- **MPL-2.0 modules.** `github.com/anacrolix/dht/v2`, `anacrolix/generics`,
  `anacrolix/log`, `anacrolix/mmsg`, `anacrolix/multiless`, `anacrolix/sync`,
  `anacrolix/torrent`, `anacrolix/upnp`, and `github.com/go-llsqlite/adapter`
  are under the Mozilla Public License 2.0. Their MPL-2.0 texts are preserved
  under `third_party/licenses/go/`, and their source code form is available at
  the upstream repositories (https://github.com/anacrolix/dht,
  https://github.com/anacrolix/generics, https://github.com/anacrolix/log,
  https://github.com/anacrolix/mmsg, https://github.com/anacrolix/multiless,
  https://github.com/anacrolix/sync, https://github.com/anacrolix/torrent,
  https://github.com/anacrolix/upnp, https://github.com/go-llsqlite/adapter)
  and through the Go module proxy
  (`https://proxy.golang.org/<module>/@v/<version>.zip`).
- `github.com/anacrolix/torrent` bundles the `webtorrent` package under the MIT
  license (Copyright (c) 2019 Michiel De Backker); the text is preserved at
  `third_party/licenses/go/github.com/anacrolix/torrent@v1.61.0/webtorrent/LICENSE`.
- `github.com/anacrolix/go-libutp` vendors the libutp C++ library (MIT,
  Copyright (c) 2010-2013 BitTorrent, Inc.), compiled into the binary via cgo;
  the module `LICENSE` covers it.
- `github.com/go-llsqlite/crawshaw` bundles the SQLite amalgamation
  (`c/sqlite3.c`); SQLite is in the public domain ("blessing"). The module's
  ISC license covers the Go code.
- The seven `golang.org/x/*` modules also ship an upstream `PATENTS`
  additional-IP-rights grant file, preserved next to each module's `LICENSE`.

### rapidyenc bundled C library

`github.com/mnightingale/rapidyenc` links prebuilt static libraries
(`librapidyenc_darwin.a`, `librapidyenc_linux_amd64.a`,
`librapidyenc_linux_arm64.a`, `librapidyenc_windows_amd64.a`) that are built from
the C/C++ library at https://github.com/animetosho/rapidyenc. That library's
README ("License" section) states the library is public domain or CC0 where PD is
not recognised, and that it uses crcutil (Apache License 2.0) for CRC32 and
zlib-ng (Zlib license) for the CRC32 folding approach.

Texts preserved in
`third_party/licenses/go/github.com/mnightingale/rapidyenc@v0.0.0-20251128204712-7aafef1eaf1c/c-library/`:

| Component | License | Text file |
| --- | --- | --- |
| animetosho/rapidyenc | Public domain / CC0-1.0 | `c-library/CC0-1.0.txt` |
| crcutil | Apache-2.0 | `c-library/Apache-2.0.txt` |
| zlib-ng (CRC32 folding approach) | Zlib | `c-library/zlib-ng-LICENSE.md` |

### Modules required by `go.mod` but not linked into the binary

The following modules appear in `backend/go.mod`/`go.sum` but provide no package
to the `./cmd/constellarr` build on linux (they are module-graph requirements,
for example alternative storage backends); they are not redistributed in the
binary: `github.com/anacrolix/utp`, `github.com/bits-and-blooms/bitset`,
`github.com/mattn/go-isatty`, `github.com/minio/sha256-simd`,
`github.com/mschoch/smat`, `github.com/remyoudompheng/bigfft`,
`modernc.org/libc`, `modernc.org/mathutil`, `modernc.org/memory`,
`modernc.org/sqlite`, `zombiezen.com/go/sqlite` (v0.13.1).

## Frontend — npm packages in the production build

`frontend/dist` is produced by `npm ci && npm run build` from
`frontend/package-lock.json`. The table lists every package that the lockfile
marks as a production (non-dev) dependency — 86 packages — at the exact version
pinned by the lockfile. Packages in the lockfile's `dev` set (Vite, TypeScript,
ESLint, Tailwind tooling, and other build-only tooling) are not part of the
distributed build output and are not listed. The set, versions, and license
fields were verified against `frontend/package-lock.json`; re-check them after
any lockfile change.

Full license texts: `third_party/licenses/npm/<package>@<version>/<original file name>`.
The `License` column is the license identifier recorded in `frontend/package-lock.json`
for that exact version; the source column repeats the tarball URL pinned there.
`@types/react`, `@types/react-dom`, and `csstype` contain TypeScript type
declarations only and contribute no runtime code to the build output.
`react-remove-scroll-bar@2.3.8` ships no license file in its npm tarball; its MIT
text was taken from the package's upstream repository
(https://github.com/theKashey/react-remove-scroll-bar).

| Package | License | Source tarball (pinned by `package-lock.json`) |
| --- | --- | --- |
| `@floating-ui/core@1.8.0` | MIT | https://registry.npmjs.org/@floating-ui/core/-/core-1.8.0.tgz |
| `@floating-ui/dom@1.8.0` | MIT | https://registry.npmjs.org/@floating-ui/dom/-/dom-1.8.0.tgz |
| `@floating-ui/react-dom@2.1.9` | MIT | https://registry.npmjs.org/@floating-ui/react-dom/-/react-dom-2.1.9.tgz |
| `@floating-ui/utils@0.2.12` | MIT | https://registry.npmjs.org/@floating-ui/utils/-/utils-0.2.12.tgz |
| `@fontsource-variable/inter@5.3.0` | OFL-1.1 | https://registry.npmjs.org/@fontsource-variable/inter/-/inter-5.3.0.tgz |
| `@radix-ui/number@1.1.3` | MIT | https://registry.npmjs.org/@radix-ui/number/-/number-1.1.3.tgz |
| `@radix-ui/primitive@1.1.7` | MIT | https://registry.npmjs.org/@radix-ui/primitive/-/primitive-1.1.7.tgz |
| `@radix-ui/react-accessible-icon@1.1.15` | MIT | https://registry.npmjs.org/@radix-ui/react-accessible-icon/-/react-accessible-icon-1.1.15.tgz |
| `@radix-ui/react-accordion@1.2.20` | MIT | https://registry.npmjs.org/@radix-ui/react-accordion/-/react-accordion-1.2.20.tgz |
| `@radix-ui/react-alert-dialog@1.1.23` | MIT | https://registry.npmjs.org/@radix-ui/react-alert-dialog/-/react-alert-dialog-1.1.23.tgz |
| `@radix-ui/react-arrow@1.1.15` | MIT | https://registry.npmjs.org/@radix-ui/react-arrow/-/react-arrow-1.1.15.tgz |
| `@radix-ui/react-aspect-ratio@1.1.15` | MIT | https://registry.npmjs.org/@radix-ui/react-aspect-ratio/-/react-aspect-ratio-1.1.15.tgz |
| `@radix-ui/react-avatar@1.2.6` | MIT | https://registry.npmjs.org/@radix-ui/react-avatar/-/react-avatar-1.2.6.tgz |
| `@radix-ui/react-checkbox@1.3.11` | MIT | https://registry.npmjs.org/@radix-ui/react-checkbox/-/react-checkbox-1.3.11.tgz |
| `@radix-ui/react-collapsible@1.1.20` | MIT | https://registry.npmjs.org/@radix-ui/react-collapsible/-/react-collapsible-1.1.20.tgz |
| `@radix-ui/react-collection@1.1.15` | MIT | https://registry.npmjs.org/@radix-ui/react-collection/-/react-collection-1.1.15.tgz |
| `@radix-ui/react-compose-refs@1.1.5` | MIT | https://registry.npmjs.org/@radix-ui/react-compose-refs/-/react-compose-refs-1.1.5.tgz |
| `@radix-ui/react-context@1.2.2` | MIT | https://registry.npmjs.org/@radix-ui/react-context/-/react-context-1.2.2.tgz |
| `@radix-ui/react-context-menu@2.3.7` | MIT | https://registry.npmjs.org/@radix-ui/react-context-menu/-/react-context-menu-2.3.7.tgz |
| `@radix-ui/react-dialog@1.1.23` | MIT | https://registry.npmjs.org/@radix-ui/react-dialog/-/react-dialog-1.1.23.tgz |
| `@radix-ui/react-direction@1.1.4` | MIT | https://registry.npmjs.org/@radix-ui/react-direction/-/react-direction-1.1.4.tgz |
| `@radix-ui/react-dismissable-layer@1.1.19` | MIT | https://registry.npmjs.org/@radix-ui/react-dismissable-layer/-/react-dismissable-layer-1.1.19.tgz |
| `@radix-ui/react-dropdown-menu@2.1.24` | MIT | https://registry.npmjs.org/@radix-ui/react-dropdown-menu/-/react-dropdown-menu-2.1.24.tgz |
| `@radix-ui/react-focus-guards@1.1.6` | MIT | https://registry.npmjs.org/@radix-ui/react-focus-guards/-/react-focus-guards-1.1.6.tgz |
| `@radix-ui/react-focus-scope@1.1.16` | MIT | https://registry.npmjs.org/@radix-ui/react-focus-scope/-/react-focus-scope-1.1.16.tgz |
| `@radix-ui/react-form@0.1.16` | MIT | https://registry.npmjs.org/@radix-ui/react-form/-/react-form-0.1.16.tgz |
| `@radix-ui/react-hover-card@1.1.23` | MIT | https://registry.npmjs.org/@radix-ui/react-hover-card/-/react-hover-card-1.1.23.tgz |
| `@radix-ui/react-id@1.1.4` | MIT | https://registry.npmjs.org/@radix-ui/react-id/-/react-id-1.1.4.tgz |
| `@radix-ui/react-label@2.1.15` | MIT | https://registry.npmjs.org/@radix-ui/react-label/-/react-label-2.1.15.tgz |
| `@radix-ui/react-menu@2.1.24` | MIT | https://registry.npmjs.org/@radix-ui/react-menu/-/react-menu-2.1.24.tgz |
| `@radix-ui/react-menubar@1.1.24` | MIT | https://registry.npmjs.org/@radix-ui/react-menubar/-/react-menubar-1.1.24.tgz |
| `@radix-ui/react-navigation-menu@1.2.22` | MIT | https://registry.npmjs.org/@radix-ui/react-navigation-menu/-/react-navigation-menu-1.2.22.tgz |
| `@radix-ui/react-one-time-password-field@0.1.16` | MIT | https://registry.npmjs.org/@radix-ui/react-one-time-password-field/-/react-one-time-password-field-0.1.16.tgz |
| `@radix-ui/react-password-toggle-field@0.1.11` | MIT | https://registry.npmjs.org/@radix-ui/react-password-toggle-field/-/react-password-toggle-field-0.1.11.tgz |
| `@radix-ui/react-popover@1.1.23` | MIT | https://registry.npmjs.org/@radix-ui/react-popover/-/react-popover-1.1.23.tgz |
| `@radix-ui/react-popper@1.3.7` | MIT | https://registry.npmjs.org/@radix-ui/react-popper/-/react-popper-1.3.7.tgz |
| `@radix-ui/react-portal@1.1.17` | MIT | https://registry.npmjs.org/@radix-ui/react-portal/-/react-portal-1.1.17.tgz |
| `@radix-ui/react-presence@1.1.10` | MIT | https://registry.npmjs.org/@radix-ui/react-presence/-/react-presence-1.1.10.tgz |
| `@radix-ui/react-primitive@2.1.10` | MIT | https://registry.npmjs.org/@radix-ui/react-primitive/-/react-primitive-2.1.10.tgz |
| `@radix-ui/react-progress@1.1.16` | MIT | https://registry.npmjs.org/@radix-ui/react-progress/-/react-progress-1.1.16.tgz |
| `@radix-ui/react-radio-group@1.4.7` | MIT | https://registry.npmjs.org/@radix-ui/react-radio-group/-/react-radio-group-1.4.7.tgz |
| `@radix-ui/react-roving-focus@1.1.19` | MIT | https://registry.npmjs.org/@radix-ui/react-roving-focus/-/react-roving-focus-1.1.19.tgz |
| `@radix-ui/react-scroll-area@1.2.18` | MIT | https://registry.npmjs.org/@radix-ui/react-scroll-area/-/react-scroll-area-1.2.18.tgz |
| `@radix-ui/react-select@2.3.7` | MIT | https://registry.npmjs.org/@radix-ui/react-select/-/react-select-2.3.7.tgz |
| `@radix-ui/react-separator@1.1.15` | MIT | https://registry.npmjs.org/@radix-ui/react-separator/-/react-separator-1.1.15.tgz |
| `@radix-ui/react-slider@1.4.7` | MIT | https://registry.npmjs.org/@radix-ui/react-slider/-/react-slider-1.4.7.tgz |
| `@radix-ui/react-slot@1.3.3` | MIT | https://registry.npmjs.org/@radix-ui/react-slot/-/react-slot-1.3.3.tgz |
| `@radix-ui/react-switch@1.3.7` | MIT | https://registry.npmjs.org/@radix-ui/react-switch/-/react-switch-1.3.7.tgz |
| `@radix-ui/react-tabs@1.1.21` | MIT | https://registry.npmjs.org/@radix-ui/react-tabs/-/react-tabs-1.1.21.tgz |
| `@radix-ui/react-toast@1.2.23` | MIT | https://registry.npmjs.org/@radix-ui/react-toast/-/react-toast-1.2.23.tgz |
| `@radix-ui/react-toggle@1.1.18` | MIT | https://registry.npmjs.org/@radix-ui/react-toggle/-/react-toggle-1.1.18.tgz |
| `@radix-ui/react-toggle-group@1.1.19` | MIT | https://registry.npmjs.org/@radix-ui/react-toggle-group/-/react-toggle-group-1.1.19.tgz |
| `@radix-ui/react-toolbar@1.1.19` | MIT | https://registry.npmjs.org/@radix-ui/react-toolbar/-/react-toolbar-1.1.19.tgz |
| `@radix-ui/react-tooltip@1.2.16` | MIT | https://registry.npmjs.org/@radix-ui/react-tooltip/-/react-tooltip-1.2.16.tgz |
| `@radix-ui/react-use-callback-ref@1.1.4` | MIT | https://registry.npmjs.org/@radix-ui/react-use-callback-ref/-/react-use-callback-ref-1.1.4.tgz |
| `@radix-ui/react-use-controllable-state@1.2.6` | MIT | https://registry.npmjs.org/@radix-ui/react-use-controllable-state/-/react-use-controllable-state-1.2.6.tgz |
| `@radix-ui/react-use-effect-event@0.0.5` | MIT | https://registry.npmjs.org/@radix-ui/react-use-effect-event/-/react-use-effect-event-0.0.5.tgz |
| `@radix-ui/react-use-escape-keydown@1.1.5` | MIT | https://registry.npmjs.org/@radix-ui/react-use-escape-keydown/-/react-use-escape-keydown-1.1.5.tgz |
| `@radix-ui/react-use-is-hydrated@0.1.3` | MIT | https://registry.npmjs.org/@radix-ui/react-use-is-hydrated/-/react-use-is-hydrated-0.1.3.tgz |
| `@radix-ui/react-use-layout-effect@1.1.4` | MIT | https://registry.npmjs.org/@radix-ui/react-use-layout-effect/-/react-use-layout-effect-1.1.4.tgz |
| `@radix-ui/react-use-previous@1.1.4` | MIT | https://registry.npmjs.org/@radix-ui/react-use-previous/-/react-use-previous-1.1.4.tgz |
| `@radix-ui/react-use-rect@1.1.4` | MIT | https://registry.npmjs.org/@radix-ui/react-use-rect/-/react-use-rect-1.1.4.tgz |
| `@radix-ui/react-use-size@1.1.4` | MIT | https://registry.npmjs.org/@radix-ui/react-use-size/-/react-use-size-1.1.4.tgz |
| `@radix-ui/react-visually-hidden@1.2.11` | MIT | https://registry.npmjs.org/@radix-ui/react-visually-hidden/-/react-visually-hidden-1.2.11.tgz |
| `@radix-ui/rect@1.1.3` | MIT | https://registry.npmjs.org/@radix-ui/rect/-/rect-1.1.3.tgz |
| `@types/react@19.3.0` | MIT | https://registry.npmjs.org/@types/react/-/react-19.3.0.tgz |
| `@types/react-dom@19.3.0` | MIT | https://registry.npmjs.org/@types/react-dom/-/react-dom-19.3.0.tgz |
| `aria-hidden@1.2.6` | MIT | https://registry.npmjs.org/aria-hidden/-/aria-hidden-1.2.6.tgz |
| `class-variance-authority@0.7.1` | Apache-2.0 | https://registry.npmjs.org/class-variance-authority/-/class-variance-authority-0.7.1.tgz |
| `clsx@2.1.1` | MIT | https://registry.npmjs.org/clsx/-/clsx-2.1.1.tgz |
| `cn@0.4.0` | MIT | https://registry.npmjs.org/cn/-/cn-0.4.0.tgz |
| `csstype@3.2.3` | MIT | https://registry.npmjs.org/csstype/-/csstype-3.2.3.tgz |
| `detect-node-es@1.1.0` | MIT | https://registry.npmjs.org/detect-node-es/-/detect-node-es-1.1.0.tgz |
| `get-nonce@1.0.1` | MIT | https://registry.npmjs.org/get-nonce/-/get-nonce-1.0.1.tgz |
| `lucide-react@1.51.0` | ISC | https://registry.npmjs.org/lucide-react/-/lucide-react-1.51.0.tgz |
| `radix-ui@1.6.7` | MIT | https://registry.npmjs.org/radix-ui/-/radix-ui-1.6.7.tgz |
| `react@19.3.0` | MIT | https://registry.npmjs.org/react/-/react-19.3.0.tgz |
| `react-dom@19.3.0` | MIT | https://registry.npmjs.org/react-dom/-/react-dom-19.3.0.tgz |
| `react-remove-scroll@2.7.2` | MIT | https://registry.npmjs.org/react-remove-scroll/-/react-remove-scroll-2.7.2.tgz |
| `react-remove-scroll-bar@2.3.8` | MIT | https://registry.npmjs.org/react-remove-scroll-bar/-/react-remove-scroll-bar-2.3.8.tgz |
| `react-style-singleton@2.2.3` | MIT | https://registry.npmjs.org/react-style-singleton/-/react-style-singleton-2.2.3.tgz |
| `scheduler@0.28.0` | MIT | https://registry.npmjs.org/scheduler/-/scheduler-0.28.0.tgz |
| `tslib@2.8.1` | 0BSD | https://registry.npmjs.org/tslib/-/tslib-2.8.1.tgz |
| `tw-animate-css@1.4.0` | MIT | https://registry.npmjs.org/tw-animate-css/-/tw-animate-css-1.4.0.tgz |
| `use-callback-ref@1.3.3` | MIT | https://registry.npmjs.org/use-callback-ref/-/use-callback-ref-1.3.3.tgz |
| `use-sidecar@1.1.3` | MIT | https://registry.npmjs.org/use-sidecar/-/use-sidecar-1.1.3.tgz |

## Container image — Alpine 3.24 runtime packages

The runtime stage installs `ca-certificates libstdc++ par2cmdline=1.1.1-r0
ffmpeg postgresql18-client python3 py3-numpy py3-pip` from Alpine 3.24 and then
creates the subtitle-tooling virtualenv, for a runtime filesystem of
145 Alpine packages. License identifiers below are those declared by the Alpine
3.24 APKINDEX for the installed versions. For every package origin (117
origins) the verbatim APKBUILD and the upstream license text(s) are preserved
under `third_party/licenses/alpine/<repo>/<origin>/`; where upstream publishes
no license file, a `README.md` there records where the grant appears. The
identifiers are distro metadata declarations and may not enumerate every
bundled component license (for example, `libjxl` is declared Apache-2.0 while
the upstream `LICENSE` bundled here is BSD-3-Clause).

Package-specific notes:

- **ffmpeg 8.1.2-r0** (and its seven library packages): the Alpine recipe
  declares `GPL-2.0-or-later AND LGPL-2.1-or-later` and builds with
  `--enable-gpl --enable-version3` (verified with `ffmpeg -version` in the
  image). Upstream FFmpeg documents that `--enable-version3` upgrades the
  (L)GPL to version 3. The upstream texts `LICENSE.md`, `COPYING.GPLv3`,
  `COPYING.LGPLv2.1`, and `COPYING.LGPLv3` are preserved under
  `third_party/licenses/alpine/community/ffmpeg/`.
- **GCC runtime libraries** `libgcc`, `libstdc++`, `libgomp`, `libgfortran`
  15.2.0-r5: `GPL-2.0-or-later AND LGPL-2.1-or-later` with the GCC Runtime
  Library Exception; `COPYING3` and `COPYING.RUNTIME` are preserved under
  `third_party/licenses/alpine/main/gcc/`.
- **PostgreSQL 18 client** `postgresql18-client` 18.6-r0 and `libpq` 18.6-r0:
  PostgreSQL License; the upstream `COPYRIGHT` is preserved under
  `third_party/licenses/alpine/main/postgresql18/`.
- **Python 3.14.8** (`python3` and its bytecode packages): PSF-2.0; the upstream
  `LICENSE` is preserved under `third_party/licenses/alpine/main/python3/`.
- **NumPy 2.4.6** (`py3-numpy`): BSD-3-Clause with bundled components
  (`BSD-3-Clause AND 0BSD AND MIT AND Zlib AND CC0-1.0`); the texts are under
  `third_party/licenses/python/numpy@2.4.6/`.
- **OpenBLAS 0.3.30-r2** (NumPy dependency): BSD-3-Clause; `LICENSE` preserved
  under `third_party/licenses/alpine/community/openblas/`.
- **ca-certificates(-bundle) 20260909-r0**: `MPL-2.0 AND MIT`; the MPL-2.0 text
  is in `third_party/licenses/alpine/common/`.
- **sqlite-libs 3.53.4-r0**: "blessing" (SQLite public-domain dedication).
- **par2cmdline 1.1.1-r0**: GPL-2.0-or-later; the Alpine recipe and the upstream
  v1.1.1 source archive are bundled under `third_party/licenses/par2cmdline/`
  (see below).
- **libcrypto3 / libssl3 3.5.8-r0**: Apache-2.0 (OpenSSL 3, `openssl` origin).
  The Alpine 3.24 APKINDEX has since moved to 3.5.9-r0; the installed version is
  listed below.

### par2cmdline corresponding source

The upstream v1.1.1 release tarball is stored verbatim under
`third_party/licenses/par2cmdline/par2cmdline-1.1.1.tar.bz2`, with a sha512 of
`13b78bb16958e808123ed47c5248f205e0f23b51c507d4d2ff6efccc4a4448970bddd0e4bdd9825b77ff9e26e16f847f8b7f76b19eedc18d4d6f23e8d7cf9acd`,
equal to the `sha512sums` value in the bundled `APKBUILD-3.24-stable`. Re-verify
with `shasum -a 512 third_party/licenses/par2cmdline/par2cmdline-1.1.1.tar.bz2`.
The Alpine recipe declares `pkgver=1.1.1`, `pkgrel=0`,
`license="GPL-2.0-or-later"` and builds the upstream release tarball without
patches; the GPL v2 `COPYING` from that source tree is in the same directory.

### Full runtime package inventory

| Package | Version | License (APKINDEX) | Repo |
| --- | --- | --- | --- |
| `alpine-baselayout` | 3.7.2-r1 | GPL-2.0-only | main |
| `alpine-baselayout-data` | 3.7.2-r1 | GPL-2.0-only | main |
| `alpine-keys` | 2.6-r0 | MIT | main |
| `alpine-release` | 3.24.2-r0 | MIT | main |
| `alsa-lib` | 1.2.15.3-r0 | LGPL-2.1-or-later | main |
| `aom-libs` | 3.14.1-r0 | BSD-2-Clause AND custom | main |
| `apk-tools` | 3.0.8-r0 | GPL-2.0-only | main |
| `brotli-libs` | 1.2.0-r1 | MIT | main |
| `busybox` | 1.37.0-r31 | GPL-2.0-only | main |
| `busybox-binsh` | 1.37.0-r31 | GPL-2.0-only | main |
| `ca-certificates` | 20260909-r0 | MPL-2.0 AND MIT | main |
| `ca-certificates-bundle` | 20260909-r0 | MPL-2.0 AND MIT | main |
| `cjson` | 1.7.19-r1 | MIT | main |
| `dbus-libs` | 1.16.2-r2 | AFL-2.1 OR GPL-2.0-or-later | main |
| `ffmpeg` | 8.1.2-r0 | GPL-2.0-or-later AND LGPL-2.1-or-later | community |
| `ffmpeg-libavcodec` | 8.1.2-r0 | GPL-2.0-or-later AND LGPL-2.1-or-later | community |
| `ffmpeg-libavdevice` | 8.1.2-r0 | GPL-2.0-or-later AND LGPL-2.1-or-later | community |
| `ffmpeg-libavfilter` | 8.1.2-r0 | GPL-2.0-or-later AND LGPL-2.1-or-later | community |
| `ffmpeg-libavformat` | 8.1.2-r0 | GPL-2.0-or-later AND LGPL-2.1-or-later | community |
| `ffmpeg-libavutil` | 8.1.2-r0 | GPL-2.0-or-later AND LGPL-2.1-or-later | community |
| `ffmpeg-libswresample` | 8.1.2-r0 | GPL-2.0-or-later AND LGPL-2.1-or-later | community |
| `ffmpeg-libswscale` | 8.1.2-r0 | GPL-2.0-or-later AND LGPL-2.1-or-later | community |
| `fontconfig` | 2.17.1-r1 | MIT | main |
| `freetype` | 2.14.3-r0 | FTL OR GPL-2.0-or-later | main |
| `fribidi` | 1.0.16-r3 | LGPL-2.1-or-later | main |
| `gdbm` | 1.26-r0 | GPL-3.0-or-later | main |
| `glib` | 2.88.1-r1 | LGPL-2.1-or-later | main |
| `glslang-libs` | 1.4.341.0-r0 | BSD-3-Clause AND BSD-2-Clause AND MIT AND Apache-2.0 AND GPL-3.0-or-later | main |
| `graphite2` | 1.3.14-r6 | LGPL-2.1-or-later OR MPL-1.1 | main |
| `harfbuzz` | 13.2.1-r0 | MIT | main |
| `hwdata-pci` | 0.408-r0 | GPL-2.0-or-later OR XFree86-1.1 | main |
| `json-c` | 0.18-r1 | MIT | main |
| `lame-libs` | 3.100-r5 | LGPL-2.0-or-later | main |
| `lcms2` | 2.19-r0 | MIT | main |
| `libapk` | 3.0.8-r0 | GPL-2.0-only | main |
| `libass` | 0.17.4-r1 | ISC | community |
| `libasyncns` | 0.8-r5 | LGPL-2.0-or-later | community |
| `libblkid` | 2.42.3-r1 | LGPL-2.1-or-later | main |
| `libbluray` | 1.4.0-r0 | LGPL-2.1-or-later | community |
| `libbsd` | 0.12.2-r0 | BSD-3-Clause | main |
| `libbz2` | 1.0.8-r6 | bzip2-1.0.6 | main |
| `libcrypto3` | 3.5.8-r0 | Apache-2.0 | main |
| `libdav1d` | 1.5.3-r0 | BSD-2-Clause | main |
| `libdovi` | 3.3.2-r0 | MIT | community |
| `libdrm` | 2.4.134-r0 | MIT | main |
| `libdvdcss` | 1.4.3-r0 | GPL-2.0-or-later | community |
| `libdvdnav` | 6.1.1-r1 | GPL-2.0-or-later | community |
| `libdvdread` | 6.1.3-r2 | GPL-2.0-or-later | community |
| `libeconf` | 0.8.3-r0 | MIT | main |
| `libexpat` | 2.8.5-r0 | MIT | main |
| `libffi` | 3.5.2-r1 | MIT | main |
| `libflac` | 1.4.3-r2 | BSD-3-Clause AND GPL-2.0-or-later | main |
| `libgcc` | 15.2.0-r5 | GPL-2.0-or-later AND LGPL-2.1-or-later | main |
| `libgfortran` | 15.2.0-r5 | GPL-2.0-or-later AND LGPL-2.1-or-later | main |
| `libgomp` | 15.2.0-r5 | GPL-2.0-or-later AND LGPL-2.1-or-later | main |
| `libhwy` | 1.3.0-r0 | Apache-2.0 | community |
| `libintl` | 1.0-r0 | LGPL-2.1-or-later | main |
| `libjpeg-turbo` | 3.1.3-r0 | BSD-3-Clause AND IJG AND Zlib | main |
| `libjxl` | 0.11.2-r1 | Apache-2.0 | community |
| `libltdl` | 2.6.0-r1 | LGPL-2.0-or-later AND GPL-2.0-or-later | main |
| `libmd` | 1.2.0-r0 | BSD-3-Clause AND BSD-2-Clause AND ISC AND Beerware AND Public Domain | main |
| `libmount` | 2.42.3-r1 | LGPL-2.1-or-later | main |
| `libncursesw` | 6.6_p20260516-r0 | X11 | main |
| `libogg` | 1.3.6-r0 | BSD-3-Clause | main |
| `libopenmpt` | 0.8.9-r0 | BSD-3-Clause | community |
| `libpanelw` | 6.6_p20260516-r0 | X11 | main |
| `libpciaccess` | 0.19-r0 | X11 | main |
| `libplacebo` | 7.360.1-r0 | LGPL-2.1-or-later | community |
| `libpng` | 1.6.59-r0 | Libpng | main |
| `libpq` | 18.6-r0 | PostgreSQL | main |
| `libpulse` | 17.0-r7 | LGPL-2.1-or-later | community |
| `librist` | 0.2.15-r0 | BSD-2-Clause | community |
| `libsharpyuv` | 1.6.0-r0 | BSD-3-Clause | main |
| `libsndfile` | 1.2.2-r2 | LGPL-2.1-or-later | main |
| `libsodium` | 1.0.22-r0 | ISC | main |
| `libsrt` | 1.5.3-r1 | MPL-2.0 | community |
| `libssh` | 0.12.2-r0 | LGPL-2.1-or-later BSD-2-Clause | community |
| `libssl3` | 3.5.8-r0 | Apache-2.0 | main |
| `libstdc++` | 15.2.0-r5 | GPL-2.0-or-later AND LGPL-2.1-or-later | main |
| `libSvtAv1Enc` | 4.1.0-r0 | BSD-3-Clause-Clear | community |
| `libtheora` | 1.2.0-r1 | BSD-3-Clause | main |
| `libudfread` | 1.2.0-r1 | LGPL-2.1-or-later | community |
| `libunibreak` | 6.1-r0 | Zlib | community |
| `libva` | 2.23.0-r0 | MIT | main |
| `libvdpau` | 1.5-r4 | MIT | main |
| `libvorbis` | 1.3.7-r2 | BSD-3-Clause | main |
| `libvpx` | 1.15.2-r1 | BSD-3-Clause | community |
| `libwebp` | 1.6.0-r0 | BSD-3-Clause | main |
| `libwebpmux` | 1.6.0-r0 | BSD-3-Clause | main |
| `libx11` | 1.8.13-r0 | X11 | main |
| `libxau` | 1.0.12-r0 | MIT | main |
| `libxcb` | 1.17.0-r2 | MIT | main |
| `libxdmcp` | 1.1.5-r1 | MIT | main |
| `libxext` | 1.3.7-r0 | MIT | main |
| `libxfixes` | 6.0.2-r0 | MIT | main |
| `libxml2` | 2.13.9-r2 | MIT | main |
| `libzmq` | 4.3.5-r2 | MPL-2.0 | main |
| `lilv-libs` | 0.24.26-r1 | ISC | community |
| `lz4-libs` | 1.10.0-r1 | BSD-2-Clause AND GPL-2.0-or-later | main |
| `mbedtls3` | 3.6.7-r0 | Apache-2.0 OR GPL-2.0-or-later | community |
| `mpdecimal` | 4.0.1-r0 | BSD-2-Clause | main |
| `mpg123-libs` | 1.33.5-r0 | LGPL-2.1-only | main |
| `musl` | 1.2.6-r2 | MIT | main |
| `musl-utils` | 1.2.6-r2 | MIT AND BSD-2-Clause AND GPL-2.0-or-later | main |
| `ncurses-terminfo-base` | 6.6_p20260516-r0 | X11 | main |
| `numactl` | 2.0.19-r0 | LGPL-2.1-only | main |
| `openblas` | 0.3.30-r2 | BSD-3-Clause | community |
| `opus` | 1.6.1-r0 | BSD-3-Clause | main |
| `orc` | 0.4.41-r0 | BSD-2-Clause | main |
| `par2cmdline` | 1.1.1-r0 | GPL-2.0-or-later | community |
| `pcre2` | 10.49-r0 | BSD-3-Clause | main |
| `postgresql-common` | 1.3-r0 | MIT | main |
| `postgresql18-client` | 18.6-r0 | PostgreSQL | main |
| `py3-numpy` | 2.4.6-r0 | BSD-3-Clause | community |
| `py3-numpy-pyc` | 2.4.6-r0 | BSD-3-Clause | community |
| `py3-numpy-tests` | 2.4.6-r0 | BSD-3-Clause | community |
| `pyc` | 3.14.8-r0 | PSF-2.0 | main |
| `python3` | 3.14.8-r0 | PSF-2.0 | main |
| `python3-pyc` | 3.14.8-r0 | PSF-2.0 | main |
| `python3-pycache-pyc0` | 3.14.8-r0 | PSF-2.0 | main |
| `rav1e-libs` | 0.8.1-r0 | BSD-2-Clause custom | community |
| `readline` | 8.3.3-r1 | GPL-3.0-or-later | main |
| `scanelf` | 1.3.9-r1 | GPL-2.0-only | main |
| `serd-libs` | 0.32.8-r0 | ISC | community |
| `shaderc` | 2026.1-r0 | Apache-2.0 | community |
| `sord-libs` | 0.16.22-r0 | ISC | community |
| `soxr` | 0.1.3-r7 | LGPL-2.1-or-later | community |
| `speexdsp` | 1.2.1-r2 | BSD-3-Clause | main |
| `spirv-tools` | 1.4.341.0-r0 | Apache-2.0 | main |
| `sqlite-libs` | 3.53.4-r0 | blessing | main |
| `sratom` | 0.6.20-r0 | ISC | community |
| `ssl_client` | 1.37.0-r31 | GPL-2.0-only | main |
| `tdb-libs` | 1.4.15-r1 | LGPL-3.0-or-later | main |
| `v4l-utils-libs` | 1.32.0-r1 | LGPL-2.0-or-later | community |
| `vidstab` | 1.1.1-r0 | GPL-2.0-or-later | community |
| `vulkan-loader` | 1.4.347-r0 | Apache-2.0 | main |
| `wayland-libs-client` | 1.25.0-r0 | MIT | main |
| `x264-libs` | 0.164.3108-r1 | GPL-2.0-or-later | community |
| `x265-libs` | 4.1-r0 | GPL-2.0-or-later | main |
| `xvidcore` | 1.3.7-r2 | GPL-2.0-or-later | community |
| `xz-libs` | 5.8.4-r0 | GPL-2.0-or-later AND 0BSD AND Public-Domain AND LGPL-2.1-or-later | main |
| `zimg` | 3.0.6-r0 | WTFPL | community |
| `zix-libs` | 0.8.0-r0 | ISC | community |
| `zlib` | 1.3.2-r0 | Zlib | main |
| `zstd-libs` | 1.5.7-r2 | BSD-3-Clause OR GPL-2.0-or-later | main |

### Corresponding source for GPL, LGPL, and MPL packages

The image includes packages under GPL, LGPL, and MPL terms. The table lists the
49 package origins whose packages declare such terms, with the license
expression(s) recorded by APKINDEX. Dual-licensed (`OR`) origins can be used
under the alternative permissive option; `AND` expressions include copyleft
terms for part of the package.

| Origin | Repo | Declared license expression(s) |
| --- | --- | --- |
| `ffmpeg` | community | GPL-2.0-or-later AND LGPL-2.1-or-later |
| `libasyncns` | community | LGPL-2.0-or-later |
| `libbluray` | community | LGPL-2.1-or-later |
| `libdvdcss` | community | GPL-2.0-or-later |
| `libdvdnav` | community | GPL-2.0-or-later |
| `libdvdread` | community | GPL-2.0-or-later |
| `libplacebo` | community | LGPL-2.1-or-later |
| `libsrt` | community | MPL-2.0 |
| `libssh` | community | LGPL-2.1-or-later BSD-2-Clause |
| `libudfread` | community | LGPL-2.1-or-later |
| `mbedtls3` | community | Apache-2.0 OR GPL-2.0-or-later |
| `par2cmdline` | community | GPL-2.0-or-later |
| `pulseaudio` | community | LGPL-2.1-or-later |
| `soxr` | community | LGPL-2.1-or-later |
| `v4l-utils` | community | LGPL-2.0-or-later |
| `vidstab` | community | GPL-2.0-or-later |
| `x264` | community | GPL-2.0-or-later |
| `xvidcore` | community | GPL-2.0-or-later |
| `alpine-baselayout` | main | GPL-2.0-only |
| `alsa-lib` | main | LGPL-2.1-or-later |
| `apk-tools` | main | GPL-2.0-only |
| `busybox` | main | GPL-2.0-only |
| `ca-certificates` | main | MPL-2.0 AND MIT |
| `dbus` | main | AFL-2.1 OR GPL-2.0-or-later |
| `flac` | main | BSD-3-Clause AND GPL-2.0-or-later |
| `freetype` | main | FTL OR GPL-2.0-or-later |
| `fribidi` | main | LGPL-2.1-or-later |
| `gcc` | main | GPL-2.0-or-later AND LGPL-2.1-or-later |
| `gdbm` | main | GPL-3.0-or-later |
| `gettext` | main | LGPL-2.1-or-later |
| `glib` | main | LGPL-2.1-or-later |
| `glslang` | main | BSD-3-Clause AND BSD-2-Clause AND MIT AND Apache-2.0 AND GPL-3.0-or-later |
| `graphite2` | main | LGPL-2.1-or-later OR MPL-1.1 |
| `hwdata` | main | GPL-2.0-or-later OR XFree86-1.1 |
| `lame` | main | LGPL-2.0-or-later |
| `libsndfile` | main | LGPL-2.1-or-later |
| `libtool` | main | LGPL-2.0-or-later AND GPL-2.0-or-later |
| `lz4` | main | BSD-2-Clause AND GPL-2.0-or-later |
| `mpg123` | main | LGPL-2.1-only |
| `musl` | main | MIT AND BSD-2-Clause AND GPL-2.0-or-later |
| `numactl` | main | LGPL-2.1-only |
| `pax-utils` | main | GPL-2.0-only |
| `readline` | main | GPL-3.0-or-later |
| `tdb` | main | LGPL-3.0-or-later |
| `util-linux` | main | LGPL-2.1-or-later |
| `x265` | main | GPL-2.0-or-later |
| `xz` | main | GPL-2.0-or-later AND 0BSD AND Public-Domain AND LGPL-2.1-or-later |
| `zeromq` | main | MPL-2.0 |
| `zstd` | main | BSD-3-Clause OR GPL-2.0-or-later |

The corresponding source archives, Alpine patches, and aports source files for
the GPL/LGPL origins in this table are pinned in
`third_party/source-manifest.json` (URLs and checksums copied from the shipped
APKBUILDs) and can be downloaded to an external directory with:

    python3 third_party/collect_sources.py --output /path/outside/the/repo

`collect_sources.py` verifies every file against the manifest checksum and
writes it byte-for-byte; it does not extract or execute downloaded code.
`--list` prints the selected manifest entries without writing anything. Use
`--include-elective` to also collect origins whose expression offers a
non-GPL/LGPL alternative (marked in the manifest), `--only` to select origins
(at least one; elective origins can be named explicitly), and `--verify-only`
to re-check an existing collection without downloading. The collector refuses
to start if a selected manifest entry has no `sha512`/`sha256` checksum or
names a file that could escape the output directory, and rejects unknown
`--only` origins (exit status 2). Download or verification failures are
written to `FAILURES.txt` in the output directory and exit 1; a successful run
removes that file. The MPL-2.0-only origins in the table (`libsrt`, `zeromq`,
`ca-certificates`) are not part of the manifest; their source is available at
the upstream repository URLs recorded in their license directories and
APKBUILDs. `par2cmdline`'s upstream release archive is additionally bundled
in-repo under `third_party/licenses/par2cmdline/`. The shared license texts for
the GPL, LGPL, and MPL families are under
`third_party/licenses/alpine/common/`.

## Subtitle tooling — Python packages in the image venv

The Dockerfile creates `/opt/subtitle-tools` with
`python3 -m venv --system-site-packages` and installs the subtitle tooling with
pip from `requirements-subtitles.txt` (which currently pins only
`ffsubsync==0.5.1`), so the transitive dependency versions below are resolved
at image build time and must be re-checked against a built image after
rebuilds. NumPy comes from the Alpine `py3-numpy` package through
`--system-site-packages`, and `pip` itself remains in the venv. The
corresponding source for the GPL `auditok` package is pinned in
`third_party/source-manifest.json` (entry `auditok`).

| Package | Version | License | License text(s) |
| --- | --- | --- | --- |
| `auditok` | 0.1.5 | GPL-3.0-only | `third_party/licenses/python/auditok@0.1.5/licenses/LICENSE` |
| `chardet` | 7.6.0 | 0BSD | `third_party/licenses/python/chardet@7.6.0/licenses/LICENSE` |
| `charset-normalizer` | 3.5.2 | MIT | `third_party/licenses/python/charset-normalizer@3.5.2/licenses/LICENSE` |
| `faust-cchardet` | 3.2.0 | MPL-1.1 OR GPL-2.0-or-later OR LGPL-2.1-or-later | `third_party/licenses/python/faust-cchardet@3.2.0/COPYING` |
| `ffmpeg-python` | 0.2.0 | Apache-2.0 | `third_party/licenses/python/ffmpeg-python@0.2.0/LICENSE` |
| `ffsubsync` | 0.5.1 | MIT | `third_party/licenses/python/ffsubsync@0.5.1/licenses/LICENSE` |
| `future` | 1.0.0 | MIT | `third_party/licenses/python/future@1.0.0/LICENSE.txt` |
| `markdown-it-py` | 4.2.0 | MIT | `third_party/licenses/python/markdown-it-py@4.2.0/licenses/LICENSE` and `licenses/LICENSE.markdown-it` |
| `mdurl` | 0.1.2 | MIT | `third_party/licenses/python/mdurl@0.1.2/LICENSE` |
| `numpy` | 2.4.6 | BSD-3-Clause AND 0BSD AND MIT AND Zlib AND CC0-1.0 | `third_party/licenses/python/numpy@2.4.6/LICENSE.txt` plus bundled `numpy/**` license files |
| `pip` | 26.2.1 | MIT (with vendored third-party licenses) | `third_party/licenses/python/pip@26.2.1/LICENSE.txt` plus `src/pip/_vendor/**` |
| `Pygments` | 2.21.0 | BSD-2-Clause | `third_party/licenses/python/Pygments@2.21.0/licenses/LICENSE` |
| `pysubs2` | 1.9.0 | MIT | `third_party/licenses/python/pysubs2@1.9.0/licenses/LICENSE.txt` |
| `rich` | 15.0.0 | MIT | `third_party/licenses/python/rich@15.0.0/licenses/LICENSE` |
| `srt` | 3.5.3 | MIT | `third_party/licenses/python/srt@3.5.3/licenses/LICENSE` |
| `tqdm` | 4.70.1 | MPL-2.0 AND MIT | `third_party/licenses/python/tqdm@4.70.1/licenses/LICENCE` |
| `typing_extensions` | 4.16.0 | PSF-2.0 | `third_party/licenses/python/typing_extensions@4.16.0/licenses/LICENSE` |
| `webrtcvad-wheels` | 2.0.14.post1 | MIT | `third_party/licenses/python/webrtcvad-wheels@2.0.14.post1/licenses/LICENSE` |

License notes:

- `auditok` 0.1.5 is GPL-3.0; the installed metadata declares "GNU General
  Public License v3 (GPLv3)" and the wheel ships the full GPLv3 text (no
  "or later" statement).
- `faust-cchardet` 3.2.0 ships no license file in its wheel; it is
  tri-licensed, and the upstream `COPYING` (MPL-1.1 + GPL-2.0 + LGPL-2.1
  texts) is preserved at the path above.
- `tqdm` 4.70.1 is `MPL-2.0 AND MIT`; `numpy` 2.4.6 combines
  `BSD-3-Clause AND 0BSD AND MIT AND Zlib AND CC0-1.0`; `chardet` 7.6.0 is
  0BSD; `typing_extensions` is PSF-2.0; `Pygments` is BSD-2-Clause;
  `ffmpeg-python` is Apache-2.0.

## Build-only components

The following are used to build the distributed artifacts but are not part of
the runtime filesystem:

- `node:22.23.1-alpine` (frontend build stage) and the 212 dev-only packages in
  `frontend/package-lock.json` (Vite, TypeScript, ESLint, Tailwind tooling).
- `golang:1.27.1-alpine` with `build-base` (Go build stage); only the compiled
  binary is copied into the runtime image.
- Alpine `build-base`, `python3-dev`, and `py3-pip`, which are installed and
  removed within the same `RUN` layer of the runtime image
  (`apk del .subtitle-build py3-pip`).

## Keeping this file current

Versions here are pins, not ranges. When `backend/go.mod`,
`frontend/package-lock.json`, or the Alpine/PyPI installs in the `Dockerfile`
change, update the corresponding table rows and refresh the files under
`third_party/licenses/`:

- Go modules: run `go list -deps ./cmd/constellarr` in `backend/` and copy the
  license files from the module cache for added or updated modules.
- Alpine origins: refresh `APKBUILD-3.24-stable` and the license text(s) from
  the exact upstream versions. Package and license data: the `APKINDEX` of
  `https://dl-cdn.alpinelinux.org/alpine/v3.24/{main,community}` (aarch64);
  recipes: the `3.24-stable` branch of
  `https://gitlab.alpinelinux.org/alpine/aports`. Then regenerate
  `third_party/source-manifest.json` for the GPL/LGPL origins (URLs and
  checksums come from the APKBUILD `source=`/`sha512sums=` entries); the
  collector re-verifies any collection against that manifest.
- Python: the Dockerfile installs from `requirements-subtitles.txt` (which
  currently pins only `ffsubsync==0.5.1`); transitive versions are resolved by
  pip at image build time, so the Python table and the license files under
  `third_party/licenses/python/` must be re-checked after rebuilds. Pinning the
  transitive dependencies would make the inventory reproducible.
