# Third-Party Notices

Constellarr redistributes the third-party components listed below in its runtime
artifacts: the `constellarr` server binary (Go, built with `CGO_ENABLED=1`), the
frontend assets embedded in that binary, and the Alpine `par2cmdline` package
installed into the container image.

Pinned versions are taken from `backend/go.mod`, `backend/go.sum`,
`frontend/package-lock.json`, and the Alpine 3.24 `aports` recipe
(`community/par2cmdline`). The upstream license/copyright texts required by those
components are reproduced verbatim under `third_party/licenses/`.

## Layout

- `third_party/licenses/go/<module>@<version>/` — license text(s) copied from the
  module sources pinned in `backend/go.mod`.
- `third_party/licenses/npm/<package>@<version>/` — license text(s) copied from
  the package tarball pinned in `frontend/package-lock.json`, using the original
  upstream file name (`LICENSE`, `LICENSE.txt`, ...).
- `third_party/licenses/par2cmdline/` — GPL v2 text, the Alpine 3.24 build
  recipe, and the upstream source archive it references for the `par2cmdline`
  package.

## Backend — Go modules linked into the server binary

Build configuration (see `Dockerfile`): `CGO_ENABLED=1`. `github.com/javi11/nntppool/v5`
v5.0.0 links `github.com/mnightingale/rapidyenc` pinned at
`v0.0.0-20251128204712-7aafef1eaf1c` (CGO, prebuilt C static libraries);
`github.com/nwaples/rardecode/v2` v2.4.1 is pure Go; `github.com/jackc/pgx/v5`
is pinned at v5.11.0. Module versions are pinned by `backend/go.mod` and
`backend/go.sum`; the module list below is the complete set of third-party
modules in the binary's build list (`go list -deps ./cmd/constellarr`).

Full license texts: `third_party/licenses/go/<module>@<version>/LICENSE`.

| Module | Version | License | Source |
| --- | --- | --- | --- |
| `github.com/jackc/pgx/v5` | v5.11.0 | MIT | https://pkg.go.dev/github.com/jackc/pgx/v5@v5.11.0 |
| `github.com/jackc/pgpassfile` | v1.0.0 | MIT | https://pkg.go.dev/github.com/jackc/pgpassfile@v1.0.0 |
| `github.com/jackc/pgservicefile` | v0.0.0-20240606120523-5a60cdf6a761 | MIT | https://pkg.go.dev/github.com/jackc/pgservicefile@v0.0.0-20240606120523-5a60cdf6a761 |
| `github.com/jackc/puddle/v2` | v2.2.2 | MIT | https://pkg.go.dev/github.com/jackc/puddle/v2@v2.2.2 |
| `github.com/javi11/nntppool/v5` | v5.0.0 | MIT | https://pkg.go.dev/github.com/javi11/nntppool/v5@v5.0.0 |
| `github.com/mnightingale/rapidyenc` | v0.0.0-20251128204712-7aafef1eaf1c | MIT (Go wrapper); bundled C library components as noted below | https://pkg.go.dev/github.com/mnightingale/rapidyenc@v0.0.0-20251128204712-7aafef1eaf1c |
| `github.com/nwaples/rardecode/v2` | v2.4.1 | BSD-2-Clause | https://pkg.go.dev/github.com/nwaples/rardecode/v2@v2.4.1 |
| `golang.org/x/sync` | v0.19.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/sync@v0.19.0 |
| `golang.org/x/text` | v0.31.0 | BSD-3-Clause | https://pkg.go.dev/golang.org/x/text@v0.31.0 |

`golang.org/x/sync` and `golang.org/x/text` also ship an upstream `PATENTS`
additional-IP-rights grant file, preserved next to each module's `LICENSE`:
`third_party/licenses/go/golang.org/x/sync@v0.19.0/PATENTS` and
`third_party/licenses/go/golang.org/x/text@v0.31.0/PATENTS`.

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

## Frontend — npm packages in the production build

`frontend/dist` is produced by `npm ci && npm run build` from
`frontend/package-lock.json`. The table lists every package that the lockfile
marks as a production (non-dev) dependency — 86 packages — at the exact version
pinned by the lockfile. Packages in the lockfile's `dev` set (Vite, TypeScript,
ESLint, Tailwind tooling, and other build-only tooling) are not part of the
distributed build output and are not listed.

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

## Container image — Alpine 3.24 `par2cmdline`

The runtime image installs `par2cmdline` from Alpine 3.24. The Alpine `aports`
recipe for `community/par2cmdline` on the `3.24-stable` branch declares
`pkgver=1.1.1`, `pkgrel=0` and `license="GPL-2.0-or-later"`, and builds the
upstream release tarball without patches.

| Component | Version | License | Source |
| --- | --- | --- | --- |
| par2cmdline | 1.1.1-r0 (Alpine 3.24, `community`) | GPL-2.0-or-later (declared by the Alpine APKBUILD) | `third_party/licenses/par2cmdline/par2cmdline-1.1.1.tar.bz2` (upstream: https://github.com/Parchive/par2cmdline/releases/download/v1.1.1/par2cmdline-1.1.1.tar.bz2) |

Corresponding-source material preserved in `third_party/licenses/par2cmdline/`:

- `par2cmdline-1.1.1.tar.bz2` — the upstream v1.1.1 release tarball, stored
  verbatim so the corresponding source is available locally. Its sha512 is
  `13b78bb16958e808123ed47c5248f205e0f23b51c507d4d2ff6efccc4a4448970bddd0e4bdd9825b77ff9e26e16f847f8b7f76b19eedc18d4d6f23e8d7cf9acd`,
  equal to the `sha512sums` value in `APKBUILD-3.24-stable`. Re-verify with
  `shasum -a 512 third_party/licenses/par2cmdline/par2cmdline-1.1.1.tar.bz2`.
- `APKBUILD-3.24-stable` — verbatim Alpine 3.24 build recipe, including the
  source URL and its `sha512sum`; the recipe defines no patches. Retrieved from
  https://gitlab.alpinelinux.org/alpine/aports/-/blob/3.24-stable/community/par2cmdline/APKBUILD
- `COPYING` — the GNU General Public License version 2 text shipped in the
  upstream v1.1.1 source tree, byte-identical to `par2cmdline-1.1.1/COPYING`
  inside the bundled archive
  (https://github.com/Parchive/par2cmdline/blob/v1.1.1/COPYING).
- `README.md` — component/version/source mapping for this directory.

The license text, build recipe, and corresponding source archive are all stored
in this directory, so reading them does not depend on remote source
availability. The versioned `par2cmdline` binary packages themselves are Alpine's
and are published at
https://pkgs.alpinelinux.org/package/v3.24/community/x86_64/par2cmdline
(https://dl-cdn.alpinelinux.org/alpine/v3.24/community/).

## Keeping this file current

Versions here are pins, not ranges. When `backend/go.mod`,
`frontend/package-lock.json`, or the Alpine 3.24 `par2cmdline` recipe change,
update the corresponding table row and replace the matching files under
`third_party/licenses/`.
