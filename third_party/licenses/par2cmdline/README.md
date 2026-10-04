# par2cmdline (Alpine 3.24 package)

Component/version/source mapping for the `par2cmdline` binary installed by the
runtime stage of the image (`apk add ... par2cmdline`).

| Field | Value |
| --- | --- |
| Component | par2cmdline — PAR 2.0 compatible file verification and repair tool |
| Package | `community/par2cmdline`, Alpine 3.24 (`v3.24`), version `1.1.1-r0` |
| Upstream | https://github.com/Parchive/par2cmdline, tag `v1.1.1` |
| License | `GPL-2.0-or-later` (declared by the Alpine APKBUILD) |
| Source archive (bundled here) | `par2cmdline-1.1.1.tar.bz2` in this directory |
| Original download URL | https://github.com/Parchive/par2cmdline/releases/download/v1.1.1/par2cmdline-1.1.1.tar.bz2 |
| Source archive sha512 | `13b78bb16958e808123ed47c5248f205e0f23b51c507d4d2ff6efccc4a4448970bddd0e4bdd9825b77ff9e26e16f847f8b7f76b19eedc18d4d6f23e8d7cf9acd` |
| Patches | none (the APKBUILD defines no `patches` / no patch files) |

Files in this directory:

- `par2cmdline-1.1.1.tar.bz2` — the upstream release tarball, stored verbatim so
  that the corresponding source is available locally without depending on remote
  availability. Its sha512 equals the `sha512sums` value in
  `APKBUILD-3.24-stable` (and the table above). Re-verify with:
  `shasum -a 512 par2cmdline-1.1.1.tar.bz2`.
  The tarball's top-level directory is `par2cmdline-1.1.1/` and it contains the
  same `COPYING` file stored here next to it.
- `COPYING` — GNU General Public License version 2 text as shipped in the
  upstream `v1.1.1` source tree (byte-identical to
  `par2cmdline-1.1.1/COPYING` inside the archive; also available from
  https://github.com/Parchive/par2cmdline/blob/v1.1.1/COPYING). It is the
  license text referenced by the APKBUILD's `GPL-2.0-or-later` declaration; the
  upstream sources carry the "version 2 or any later version" notice.
- `APKBUILD-3.24-stable` — verbatim Alpine 3.24 `aports` build recipe for
  `community/par2cmdline`, retrieved 2026-10-03 from
  https://gitlab.alpinelinux.org/alpine/aports/-/blob/3.24-stable/community/par2cmdline/APKBUILD
  (sha256 `04f89aad3841afd61a30dd1eca35c086253aa0cde8741aa9988ce5890cc294b6`). It records the exact source URL and its
  `sha512sum`, and contains the full `build`/`check`/`package` steps.
- `README.md` — this mapping file.

The recipe (`APKBUILD-3.24-stable`) and the source archive it references are both
in this directory, so the local GPL-2.0 text, the build recipe, and the
corresponding source are self-contained. Distribution package metadata (not
needed locally) is published at
https://pkgs.alpinelinux.org/package/v3.24/community/x86_64/par2cmdline.
