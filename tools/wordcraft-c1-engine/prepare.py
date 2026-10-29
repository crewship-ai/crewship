#!/usr/bin/env python3
"""Prepare the public pinned C1 engine/helper in a NEW owned directory.
Run builds/tests separately through crewship-heavy; this script changes no host
configuration, existing checkout, vendor tree, service or environment.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    here = Path(__file__).resolve().parent
    spec = json.loads((here / "source.json").read_text())
    patch = here / "host-clock.patch"
    if sha(patch) != spec["patch_sha256"]:
        raise SystemExit("clock patch identity mismatch")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    source = output / "source"
    subprocess.run(["git", "init", "-q", str(source)], check=True)
    subprocess.run(["git", "-C", str(source), "remote", "add", "origin", spec["url"]], check=True)
    subprocess.run(["git", "-C", str(source), "fetch", "--depth=1", "origin", spec["rev"]], check=True)
    subprocess.run(["git", "-C", str(source), "checkout", "-q", "--detach", "FETCH_HEAD"], check=True)
    actual = subprocess.check_output(["git", "-C", str(source), "rev-parse", "HEAD"], text=True).strip()
    if actual != spec["rev"]:
        raise SystemExit("source revision mismatch")
    for path, hashes in spec["files"].items():
        if sha(source / path) != hashes["before_sha256"]:
            raise SystemExit("source input identity mismatch")
    subprocess.run(["git", "-C", str(source), "apply", "--check", str(patch)], check=True)
    subprocess.run(["git", "-C", str(source), "apply", str(patch)], check=True)
    for path, hashes in spec["files"].items():
        if sha(source / path) != hashes["after_sha256"]:
            raise SystemExit("source result identity mismatch")
    helper = output / "helper"
    helper.mkdir()
    for name in ["Cargo.toml", "Cargo.lock", "WORDCRAFT-LICENSE-MIT", "source.json"]:
        if (here / name).exists():
            shutil.copyfile(here / name, helper / name)
    shutil.copytree(here / "src", helper / "src")
    receipt = {**spec, "source_identity_verified": True, "patch_applied_verified": True,
               "license": {"choice": "MIT", "notice_sha256": sha(source / "LICENSE-MIT")},
               "scope": "Prepared source only; no build/test/production claim."}
    if sha(source / "LICENSE-MIT") != sha(here / "WORDCRAFT-LICENSE-MIT"):
        raise SystemExit("upstream license notice mismatch")
    (output / "source-receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(json.dumps(receipt))


if __name__ == "__main__":
    main()
