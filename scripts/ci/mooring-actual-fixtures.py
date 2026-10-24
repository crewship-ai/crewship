#!/usr/bin/env python3
"""Build actual public-source integration fixtures into a NEW owned directory.

Run inside the normal build/test scheduler. Requires the pinned Rust toolchain,
wasm32 target and wasm-bindgen CLI; performs no tool installation or deployment.
The manifest records bytes produced by this build, not application authority.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess


ROOT = Path(__file__).resolve().parents[2]


def run(*args):
    subprocess.run(args, cwd=ROOT, check=True)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_identity():
    # A dirty local build must never be labelled as bytes of a committed HEAD.
    git_env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    def git(*args):
        return subprocess.check_output(["git", *args], cwd=ROOT, env=git_env, text=True).strip()
    if git("status", "--porcelain", "--untracked-files=no"):
        raise ValueError("actual fixtures require a clean tracked source tree")
    return {"commit": git("rev-parse", "HEAD"), "tree": git("rev-parse", "HEAD^{tree}")}


def cargo_target():
    target = Path(os.environ.get("CARGO_TARGET_DIR", "mooring/target"))
    return (target if target.is_absolute() else ROOT / target).resolve()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    if subprocess.check_output(["wasm-bindgen", "--version"], text=True).strip() != "wasm-bindgen 0.2.129":
        parser.error("requires pinned wasm-bindgen 0.2.129")
    identity = source_identity()
    source = identity["commit"]
    cargo = ["cargo", "build", "--manifest-path", "mooring/Cargo.toml", "--config",
             "mooring/.cargo/config.toml", "--locked", "--offline", "--release"]
    run(*cargo, "-p", "mooring-collab")
    run(*cargo, "-p", "mooring-wordcraft", "--example", "roundtrip")
    run(*cargo, "--target", "wasm32-unknown-unknown", "-p", "mooring-wordcraft-client")
    target = cargo_target()
    sdk = output / "sdk"
    sdk.mkdir()
    run("wasm-bindgen", str(target / "wasm32-unknown-unknown/release/mooring_wordcraft_client.wasm"),
        "--target", "web", "--out-dir", str(sdk))
    binary = output / "mooring-collab"
    binary.write_bytes((target / "release/mooring-collab").read_bytes())
    binary.chmod(0o500)
    checkpoints = {}
    for document in ("d1", "d3"):
        native = ROOT / f"mooring/crates/mooring-wordcraft/tests/fixtures/{document}-engine-canonical.json"
        count = len(json.loads(native.read_text())["body"])
        if count == 0:
            raise ValueError("empty public native fixture")
        fixture = output / document
        fixture.mkdir()
        ids = fixture / "stable-ids.json"
        ids.write_text(json.dumps([f"qualified-{document}-{i}" for i in range(count)]))
        run(str(target / "release/examples/roundtrip"), str(native), str(ids), str(fixture))
        checkpoint = fixture / "checkpoint.json"
        checkpoints[document] = {"path": str(checkpoint), "sha256": digest(checkpoint)}
    payload = output / "payload.json"
    payload.write_text(json.dumps({"kind": "text.insert", "target_id": "qualified-d1-0",
                                  "at": {"kind": "end"}, "text": " hosted actual",
                                  "properties": {"bold": True, "italic": False}}))
    reads = output / "reads.json"
    reads.write_text(json.dumps([{"target_id": "qualified-d1-0", "revision": "0",
                                 "structure_revision": "0"}]))
    assets = {p.name: digest(p) for p in sorted(sdk.iterdir()) if p.suffix in {".js", ".wasm"}}
    if set(assets) != {"mooring_wordcraft_client.js", "mooring_wordcraft_client_bg.wasm"}:
        raise ValueError("unexpected generated SDK asset set")
    sdk_digest = hashlib.sha256(json.dumps(assets, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    if source_identity() != identity:
        raise ValueError("source changed while building actual fixtures")
    manifest = {"format": "mooring.actual-fixtures.v1", "source": source, "source_tree": identity["tree"],
                "binary": {"path": str(binary), "sha256": digest(binary)},
                "checkpoints": checkpoints, "sdk": {"directory": str(sdk), "assets": assets,
                "prefix": "/mooring-sdk/generated/" + sdk_digest},
                "payload": {"path": str(payload), "sha256": digest(payload)},
                "reads": {"path": str(reads), "sha256": digest(reads)}}
    (output / "fixtures.json").write_text(json.dumps(manifest, sort_keys=True, indent=2) + "\n")
    print(output / "fixtures.json")


if __name__ == "__main__":
    main()
