#!/usr/bin/env python3
"""Verify the Word and Sheet packages a nightly re-publishes.

The nightly does not rebuild the packages. It takes the pair from one earlier,
immutable release and publishes the same two files, so everything that makes
the pair trustworthy is checked again here, and any mismatch fails the run:

  1. the directory holds exactly the signed checksums, their signature and
     certificate, and the two packages named in the source file;
  2. the checksums are signed by the release workflow of the pinned repository,
     at the pinned tag and commit (Sigstore identity and workflow claims);
  3. each package's SHA-256 equals the source file, the signed checksums and
     the file itself;
  4. each package's manifest is the one this commit's catalog pins
     (tools/craft-components/catalog.txt); the workflow then runs
     check-craft-packages.sh, the release gate, on the staged packages too.

A new pair is needed when the catalog changes: build it with the release
workflow, then update the source file.

  verify-craft-source.py --source S --catalog C --dir D --out O [--receipt R]

COSIGN names the cosign binary (default: cosign).
"""
import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

ISSUER = "https://token.actions.githubusercontent.com"
SHA256 = re.compile(r"^[0-9a-f]{64}$")
COMMIT = re.compile(r"^[0-9a-f]{40}$")
SIGNED = ("checksums.txt", "checksums.txt.sig", "checksums.txt.pem")


class Fail(Exception):
    pass


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def load_source(path: Path) -> dict:
    try:
        src = json.loads(path.read_text())
    except (OSError, ValueError) as e:
        raise Fail(f"unreadable source file {path}: {e}")
    repo, tag, commit = src.get("repository"), src.get("tag"), src.get("commit")
    if not (isinstance(repo, str) and re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo)):
        raise Fail("source: bad repository")
    if not (isinstance(tag, str) and re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?", tag)):
        raise Fail("source: bad tag")
    if not (isinstance(commit, str) and COMMIT.fullmatch(commit)):
        raise Fail("source: bad commit")
    wf = src.get("workflow")
    if wf != ".github/workflows/release.yml":
        raise Fail("source: the signing workflow must be the release workflow")
    archives = src.get("archives")
    if not isinstance(archives, dict) or not archives:
        raise Fail("source: no archives")
    for name, digest in archives.items():
        if not re.fullmatch(r"crewship-craft-[a-z]+\.craftpkg", name) or not SHA256.fullmatch(str(digest)):
            raise Fail(f"source: bad archive entry {name}")
    return src


def verify_signature(src: dict, directory: Path) -> None:
    cosign = os.environ.get("COSIGN", "cosign")
    identity = f"https://github.com/{src['repository']}/{src['workflow']}@refs/tags/{src['tag']}"
    cmd = [
        cosign, "verify-blob",
        "--certificate", str(directory / "checksums.txt.pem"),
        "--signature", str(directory / "checksums.txt.sig"),
        "--certificate-identity", identity,
        "--certificate-oidc-issuer", ISSUER,
        "--certificate-github-workflow-repository", src["repository"],
        "--certificate-github-workflow-ref", f"refs/tags/{src['tag']}",
        "--certificate-github-workflow-sha", src["commit"],
        "--certificate-github-workflow-trigger", "push",
        str(directory / "checksums.txt"),
    ]
    r = subprocess.run(cmd, capture_output=True, text=True)
    if r.returncode != 0:
        raise Fail("the checksums are not signed by the release workflow at the pinned tag and commit: "
                   + (r.stderr.strip().splitlines() or ["cosign failed"])[-1])


def signed_digests(directory: Path) -> dict:
    out = {}
    for line in (directory / "checksums.txt").read_text().splitlines():
        m = re.fullmatch(r"([0-9a-f]{64})  (\S+)", line)
        if m:
            out[m.group(2)] = m.group(1)
    return out


def check_catalog(catalog: str, directory: Path) -> None:
    """The same rule as scripts/check-craft-packages.sh and craftassets.ParsePins."""
    pins = catalog.split(",")
    seen = set()
    for pin in pins:
        m = re.fullmatch(r"(wordcraft|gridcraft)=([A-Za-z0-9.+_-]{1,64})=([0-9a-f]{64})", pin)
        if not m:
            raise Fail(f"malformed catalog pin: {pin}")
        app, want = m.group(1), m.group(3)
        if app in seen:
            raise Fail(f"{app} is pinned twice")
        seen.add(app)
        pkg = directory / f"crewship-craft-{app}.craftpkg"
        if not pkg.is_file():
            raise Fail(f"no package for pin {app}")
        r = subprocess.run(["tar", "-xOf", str(pkg), "manifest.json"], capture_output=True)
        if r.returncode != 0 or hashlib.sha256(r.stdout).hexdigest() != want:
            raise Fail(f"the packages are not the ones this commit's catalog pins (a new verified pair is needed): {pkg.name}")
    if len(list(directory.iterdir())) != len(pins):
        raise Fail("the packages are not the ones this commit's catalog pins (a new verified pair is needed): file count")


def main(argv=None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--source", required=True, type=Path)
    ap.add_argument("--catalog", required=True, type=Path)
    ap.add_argument("--dir", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path)
    ap.add_argument("--receipt", type=Path)
    a = ap.parse_args(argv)
    try:
        src = load_source(a.source)
        expected = set(SIGNED) | set(src["archives"])
        present = {p.name for p in a.dir.iterdir()} if a.dir.is_dir() else set()
        if present != expected:
            raise Fail(f"unexpected files: missing {sorted(expected - present)}, extra {sorted(present - expected)}")
        verify_signature(src, a.dir)
        signed = signed_digests(a.dir)
        for name, want in sorted(src["archives"].items()):
            got = sha256(a.dir / name)
            if got != want:
                raise Fail(f"{name} does not match the pinned SHA-256 (got {got})")
            if signed.get(name) != want:
                raise Fail(f"{name} is not listed with that SHA-256 in the signed checksums")
        catalog = "".join(a.catalog.read_text().split())
        stage = a.out
        if stage.exists():
            shutil.rmtree(stage)
        stage.mkdir(parents=True)
        for name in src["archives"]:
            shutil.copyfile(a.dir / name, stage / name)
        try:
            check_catalog(catalog, stage)
        except Fail:
            shutil.rmtree(stage, ignore_errors=True)
            raise
        if a.receipt:
            a.receipt.write_text(json.dumps({
                "source_repository": src["repository"], "source_tag": src["tag"], "source_commit": src["commit"],
                "signing_workflow": src["workflow"], "catalog": catalog,
                "archives": {n: src["archives"][n] for n in sorted(src["archives"])},
            }, indent=2, sort_keys=True) + "\n")
    except Fail as e:
        print(f"::error::{e}", file=sys.stderr)
        return 1
    print(f"Verified Word and Sheet packages from {src['tag']} ({src['commit'][:12]})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
