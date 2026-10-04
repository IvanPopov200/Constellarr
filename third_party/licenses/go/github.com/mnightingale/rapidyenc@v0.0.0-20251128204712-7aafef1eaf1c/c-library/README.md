# rapidyenc bundled C library components

The pinned Go module `github.com/mnightingale/rapidyenc`
(`v0.0.0-20251128204712-7aafef1eaf1c`, the CGO backend selected for
`github.com/javi11/nntppool/v5` v5.0.0) links prebuilt static libraries that are
compiled from the C/C++ library at https://github.com/animetosho/rapidyenc:
`librapidyenc_darwin.a`, `librapidyenc_linux_amd64.a`,
`librapidyenc_linux_arm64.a`, `librapidyenc_windows_amd64.a` (see the module's
`cgo.go`). The Go wrapper itself is MIT-licensed; its text is `../LICENSE`.

The upstream C library repository has no root `LICENSE` file; its README
("License" section) states:

> This module is Public Domain or [CC0](https://creativecommons.org/publicdomain/zero/1.0/legalcode) (or equivalent) if PD isn't recognised.
>
> [crcutil](https://code.google.com/p/crcutil/), used for CRC32 calculation, is licensed under the [Apache License 2.0](http://www.apache.org/licenses/LICENSE-2.0)
>
> [zlib-ng](https://github.com/Dead2/zlib-ng), from where the CRC32 calculation using folding approach was stolen, is under a [zlib license](https://github.com/Dead2/zlib-ng/blob/develop/LICENSE.md)

Texts reproduced in this directory (retrieved 2026-10-03):

| File | Component | Retrieved from |
| --- | --- | --- |
| `CC0-1.0.txt` | animetosho/rapidyenc | https://creativecommons.org/publicdomain/zero/1.0/legalcode.txt |
| `Apache-2.0.txt` | crcutil | https://www.apache.org/licenses/LICENSE-2.0.txt |
| `zlib-ng-LICENSE.md` | zlib-ng (CRC32 folding approach) | https://raw.githubusercontent.com/Dead2/zlib-ng/develop/LICENSE.md |
