#!/usr/bin/env python3

import argparse
import hashlib
import json
import os
import shutil
import sys
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
MANIFEST = REPO_ROOT / "third_party" / "source-manifest.json"
USER_AGENT = "constellarr-source-collector/1"


def fail(msg, code=1):
    print(f"error: {msg}", file=sys.stderr)
    raise SystemExit(code)


def sha(path, algo):
    h = hashlib.new(algo)
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def file_checksum(rec):
    for algo in ("sha512", "sha256"):
        if algo in rec:
            return algo, rec[algo]
    return None, None


def safe_name(kind, value):
    # Names are joined onto the output directory, so reject anything that could escape it.
    if not isinstance(value, str) or value in ("", ".", "..") or "/" in value or "\\" in value:
        fail(f"missing or unsafe {kind} in manifest: {value!r}")
    return value


def validate_entries(entries):
    for e in entries:
        if not isinstance(e, dict):
            fail(f"manifest entry is not an object: {e!r}")
        origin = safe_name("origin", e.get("origin"))
        if not isinstance(e.get("license"), str) or not e["license"]:
            fail(f"manifest entry {origin!r} has no license")
        files = e.get("files")
        if not isinstance(files, list) or not files:
            fail(f"manifest entry {origin!r} has no files")
        seen = set()
        for rec in files:
            rec = rec if isinstance(rec, dict) else {}
            name = safe_name(f"file name for {origin}", rec.get("name"))
            if not isinstance(rec.get("url"), str) or not rec["url"]:
                fail(f"manifest file {origin}/{name} has no url")
            if name in seen:
                fail(f"duplicate file {origin}/{name} in manifest")
            seen.add(name)
            if file_checksum(rec)[0] is None:
                fail(f"manifest file {origin}/{name} has no sha512 or sha256 checksum")


def load_manifest():
    try:
        entries = json.loads(MANIFEST.read_text())["entries"]
    except (OSError, ValueError, TypeError, KeyError) as exc:
        fail(f"cannot read {MANIFEST}: {exc}")
    if not isinstance(entries, list):
        fail(f"cannot read {MANIFEST}: entries must be a list")
    validate_entries(entries)
    return entries


def download(url, dest, timeout=300, attempts=3):
    last = None
    for attempt in range(attempts):
        tmp = dest.with_suffix(dest.suffix + ".part")
        try:
            req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
            with urllib.request.urlopen(req, timeout=timeout) as r, open(tmp, "wb") as fh:
                while True:
                    chunk = r.read(1 << 20)
                    if not chunk:
                        break
                    fh.write(chunk)
            os.replace(tmp, dest)
            return None
        except Exception as exc:  # network/TLS/HTTP errors
            last = exc
            if tmp.exists():
                tmp.unlink()
            if attempt + 1 < attempts:
                time.sleep(2 * (attempt + 1))
    return last


def collect_file(outdir, origin, rec, verify_only):
    name, url = rec["name"], rec["url"]
    algo, want = file_checksum(rec)
    dest = Path(outdir) / origin / name
    if verify_only:
        if not dest.is_file():
            return origin, name, "missing", None
        if sha(dest, algo) != want:
            return origin, name, "checksum-mismatch", f"expected {algo} {want}"
        return origin, name, "cached", None
    dest.parent.mkdir(parents=True, exist_ok=True)
    if dest.is_file() and sha(dest, algo) == want:
        return origin, name, "cached", None
    err = download(url, dest)
    if err is not None:
        return origin, name, "failed", f"{type(err).__name__}: {err}"
    got = sha(dest, algo)
    if got != want:
        dest.unlink(missing_ok=True)
        return origin, name, "checksum-mismatch", f"expected {algo} {want}, got {got}"
    return origin, name, "downloaded", None


def main():
    ap = argparse.ArgumentParser(
        description="Download and checksum-verify the corresponding-source files pinned in "
                    "third_party/source-manifest.json.",
        epilog=(
            "examples:\n"
            "  %(prog)s --list\n"
            "  %(prog)s --output /path/outside/the/repo\n"
            "  %(prog)s --output DIR --include-elective\n"
            "  %(prog)s --output DIR --only ffmpeg gcc\n"
            "  %(prog)s --output DIR --verify-only\n"
            "\n"
            "exit status: 0 all files verified, 1 download/verification failure, 2 usage error\n"
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("--output", help="directory for the collected sources; must be outside the repository")
    ap.add_argument("--include-elective", action="store_true",
                    help="also collect origins whose license expression offers a non-GPL/LGPL alternative")
    ap.add_argument("--only", nargs="*", metavar="ORIGIN", default=None,
                    help="collect only these origins; at least one is required (see --list)")
    ap.add_argument("--list", action="store_true", help="list the selected manifest entries and exit")
    ap.add_argument("--verify-only", action="store_true",
                    help="verify files already collected in --output without downloading")
    ap.add_argument("--jobs", type=int, default=4, help="parallel downloads (default: 4)")
    args = ap.parse_args()

    entries = load_manifest()
    if args.only is not None:
        if not args.only:
            ap.error("--only requires at least one origin; run --list to see available origins")
        unknown = sorted(set(args.only) - {e["origin"] for e in entries})
        if unknown:
            ap.error(f"unknown origin(s): {', '.join(unknown)}; run --list to see available origins")
        entries = [e for e in entries if e["origin"] in set(args.only)]
    elif not args.include_elective:
        entries = [e for e in entries if not e.get("elective_alternative")]

    if args.list:
        for e in entries:
            files = ", ".join(f["name"] for f in e["files"])
            print(f"{e['origin']}  [{e['license']}]  {files}")
        return 0
    if not args.output:
        ap.error("--output is required unless --list is used")
    if not entries:
        fail("no manifest entries selected; run --list to inspect the manifest")

    out = Path(args.output).expanduser().resolve()
    if out == REPO_ROOT or REPO_ROOT in out.parents:
        ap.error("output directory must be outside the repository")
    if out.exists() and not out.is_dir():
        ap.error(f"output path exists and is not a directory: {out}")
    out.mkdir(parents=True, exist_ok=True)

    if not args.verify_only:
        # keep the recipes next to the collected files
        for e in entries:
            apkbuild = e.get("apkbuild")
            recipe = (REPO_ROOT / apkbuild).resolve() if apkbuild else None
            if recipe and REPO_ROOT in recipe.parents and recipe.is_file():
                dest = out / e["origin"] / "APKBUILD-3.24-stable"
                dest.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(recipe, dest)

    results = []
    if entries:
        with ThreadPoolExecutor(max_workers=max(1, args.jobs)) as pool:
            jobs = [pool.submit(collect_file, out, e["origin"], rec, args.verify_only)
                    for e in entries for rec in e["files"]]
            for fut in as_completed(jobs):
                results.append(fut.result())

    ok = [r for r in results if r[2] in ("cached", "downloaded")]
    failed = [r for r in results if r[2] not in ("cached", "downloaded")]
    for origin, name, status, detail in sorted(failed):
        print(f"FAIL {origin}/{name}: {status} {detail or ''}".rstrip(), file=sys.stderr)
    print(f"{'verified' if args.verify_only else 'collected'} {len(ok)}/{len(results)} files "
          f"for {len(entries)} origins in {out}")
    if failed:
        lines = [f"{o}/{n}\t{s}\t{d or ''}" for o, n, s, d in sorted(failed)]
        (out / "FAILURES.txt").write_text("\n".join(lines) + "\n")
        return 1
    (out / "FAILURES.txt").unlink(missing_ok=True)
    if not args.verify_only:
        shutil.copy2(MANIFEST, out / "source-manifest.json")
        index = [
            "Corresponding source files for GPL/LGPL components distributed in",
            "Constellarr runtime artifacts. Collected by third_party/collect_sources.py",
            "from third_party/source-manifest.json; each file was verified against the",
            "checksum pinned there (from the shipped Alpine APKBUILDs and PyPI release",
            "metadata). Files are stored byte-for-byte and are not executed.",
            "",
        ]
        for e in sorted(entries, key=lambda x: x["origin"]):
            index.append(f"[{e['origin']}] {e['license']}")
            for rec in e["files"]:
                algo, digest = file_checksum(rec)
                index.append(f"  {rec['role']:9s} {rec['name']}  {algo}:{digest}")
                index.append(f"            {rec['url']}")
            index.append("")
        (out / "SOURCES.txt").write_text("\n".join(index))
    return 0


if __name__ == "__main__":
    sys.exit(main())
