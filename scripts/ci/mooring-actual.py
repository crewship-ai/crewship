#!/usr/bin/env python3
"""Execute every inventoried Linux actual integration test, without optional skips.

Fixtures must be produced by mooring-actual-fixtures.py from public source.
The test inventory is checked against all integration-tagged Go source files;
adding a test requires an explicit fixture assignment and inventory update.
"""

import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("actual_results", Path(__file__).with_name("mooring-actual-results.py"))
results = importlib.util.module_from_spec(spec)
spec.loader.exec_module(results)
MODULE = "github.com/crewship-ai/crewship/"
READONLY = "TestCollaborationReadonlyOriginInventoryRejectsUnsupportedDocument"


def inventory_data():
    return json.loads(Path(__file__).with_name("mooring-actual-tests.json").read_text())


def cases():
    inventory = inventory_data()
    if inventory.get("format") != "mooring.actual-tests.v1":
        raise ValueError("unknown actual test inventory")
    declared = {}
    for item in inventory["files"]:
        for test in item["tests"]:
            if test in declared or not re.fullmatch(r"Test[A-Za-z0-9_]+", test):
                raise ValueError("duplicate or invalid actual test name")
            declared[test] = item["path"]
    # Go's AST and build-constraint parser handle comments, whitespace and
    # legal declaration spelling. Never silently exclude an unparsed test.
    helper = Path(__file__).with_name("mooring-actual-discover") / "main.go"
    process = subprocess.run(["go", "run", str(helper), str(ROOT)],
                             cwd=ROOT, text=True, capture_output=True)
    if process.returncode:
        raise ValueError("actual test discovery failed: " + process.stderr.strip())
    discovered = json.loads(process.stdout)
    if not declared or discovered != declared:
        raise ValueError(f"actual test inventory drift: missing={sorted(discovered.keys()-declared.keys())}, stale={sorted(declared.keys()-discovered.keys())}")
    return declared


def capture_go_test(command, env, events):
    # Go emits module-download and tool diagnostics on stderr, outside its
    # -json event stream. Preserve them separately; the JSON verifier remains
    # strict and the caller still checks the actual process exit status.
    with events.open("w") as log, events.with_name("go-test.stderr.log").open("w") as diagnostics:
        return subprocess.run(command, cwd=ROOT, env=env, stdout=log,
                              stderr=diagnostics).returncode


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixtures", type=Path, required=True)
    parser.add_argument("--results", type=Path, required=True)
    args = parser.parse_args()
    declared = cases()
    fixture_path = args.fixtures.resolve()
    fixture_root = fixture_path.parent
    fixture = json.loads(fixture_path.read_text())
    fixture_spec = importlib.util.spec_from_file_location("actual_fixtures", Path(__file__).with_name("mooring-actual-fixtures.py"))
    fixture_module = importlib.util.module_from_spec(fixture_spec)
    fixture_spec.loader.exec_module(fixture_module)
    identity = fixture_module.source_identity()
    source = identity["commit"]
    if (fixture.get("format") != "mooring.actual-fixtures.v1" or fixture.get("source") != source
            or fixture.get("source_tree") != identity["tree"]):
        raise ValueError("fixtures are not from this source build")

    def pinned(path, want):
        p = Path(path).resolve()
        if not p.is_relative_to(fixture_root) or not p.is_file() or not re.fullmatch(r"[0-9a-f]{64}", want):
            raise ValueError("invalid owned fixture path or hash")
        if p.stat().st_size > 64 << 20 or hashlib.sha256(p.read_bytes()).hexdigest() != want:
            raise ValueError("fixture hash or size mismatch")
        return str(p)

    binary = pinned(**dict(path=fixture["binary"]["path"], want=fixture["binary"]["sha256"]))
    sdk = fixture["sdk"]
    js_hash = sdk["assets"]["mooring_wordcraft_client.js"]
    wasm_hash = sdk["assets"]["mooring_wordcraft_client_bg.wasm"]
    js = pinned(Path(sdk["directory"]) / "mooring_wordcraft_client.js", js_hash)
    wasm = pinned(Path(sdk["directory"]) / "mooring_wordcraft_client_bg.wasm", wasm_hash)
    checkpoints = {name: pinned(item["path"], item["sha256"])
                   for name, item in fixture["checkpoints"].items()}
    if set(checkpoints) != {"d1", "d3"}:
        raise ValueError("requires both public D1 and D3 checkpoints")
    payload = pinned(fixture["payload"]["path"], fixture["payload"]["sha256"])
    reads = pinned(fixture["reads"]["path"], fixture["reads"]["sha256"])
    output = args.results.resolve()
    output.mkdir(mode=0o700, parents=False, exist_ok=False)
    completed = []
    for name, path in declared.items():
        document = "d3" if name == READONLY else "d1"
        case_dir = output / name
        case_dir.mkdir()
        env = dict(os.environ)
        # Never inherit private fixture selections or an old source/binary pin.
        env = {key: value for key, value in env.items() if not key.startswith("MOORING_")}
        env.update({
            "MOORING_SESSION_BINARY": binary,
            "MOORING_SESSION_BINARY_SHA256": fixture["binary"]["sha256"],
            "MOORING_SESSION_RUST_SOURCE": source,
            "MOORING_SESSION_CHECKPOINT": checkpoints[document],
            "MOORING_SESSION_CHECKPOINT_SHA256": fixture["checkpoints"][document]["sha256"],
            "MOORING_SESSION_OUTPUT_DIR": str(case_dir),
            "MOORING_TEST_RUST_BINARY": binary,
            "MOORING_TEST_RUST_SHA256": fixture["binary"]["sha256"],
            "MOORING_TEST_RUST_SOURCE": source,
            "MOORING_TEST_WORDCRAFT_CHECKPOINT": checkpoints[document],
            "MOORING_TEST_WORDCRAFT_CHECKPOINT_SHA256": fixture["checkpoints"][document]["sha256"],
            "MOORING_TEST_WORDCRAFT_TARGET_ID": "qualified-d1-0",
            "MOORING_TEST_WORDCRAFT_PAYLOAD": payload,
            "MOORING_TEST_WORDCRAFT_READS": reads,
            "MOORING_TEST_OUTPUT_DIRECTORY": str(case_dir),
            "MOORING_SDK_JS": js, "MOORING_SDK_WASM": wasm,
            "MOORING_SDK_JS_SHA256": js_hash, "MOORING_SDK_WASM_SHA256": wasm_hash,
        })
        package = str(Path(path).parent)
        command = ["go", "test", "-json", "-race", "-tags", "mooring_rust_integration",
                   "./" + package, "-run", "^" + name + "$", "-count=1", "-timeout=10m"]
        print(f"actual integration: {package} {name}", flush=True)
        events = case_dir / "go-test.jsonl"
        returncode = capture_go_test(command, env, events)
        with events.open() as log:
            qualification = results.verify(log, MODULE + package, [name])
        if returncode != 0:
            raise ValueError(f"actual Go process failed: {name}: exit {returncode}")
        completed.append(qualification)
        (output / "completed.json").write_text(json.dumps(completed, indent=2) + "\n")
    if len(completed) != len(declared):
        raise ValueError("incomplete actual integration run")
    print(json.dumps({"actual_tests_passed": len(completed), "skipped": 0, "source": source}))


if __name__ == "__main__":
    main()
