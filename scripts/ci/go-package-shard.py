#!/usr/bin/env python3
"""Run one package partition of the full (non-race) Go suite.

run INDEX COUNT [--split-api] [go test args...] lists every package, splits them
largest-first by measured cost into COUNT partitions, proves the partitions cover
the listing exactly once, and runs partition INDEX through scripts/ci/go-test.sh.
Costs only decide placement; every listed package always runs in exactly one
partition.

--split-api takes internal/api out of the package partitions and instead runs
top-level test slice INDEX of COUNT of it (the same exact-cover partition the
API race lane uses), concurrently with the package partition. Every top-level
Test/Example/Fuzz of internal/api runs in exactly one shard; the shard proves
from go test's own events that it executed exactly its slice. Flags that would
change the selection (-run, -skip, -shuffle, -list) are refused: the shuffle
lane keeps the whole package for order-dependence coverage.
"""
import importlib.util
import json
import math
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PREFIX = "github.com/crewship-ai/crewship/"
API = PREFIX + "internal/api"
MIN_PACKAGES = 90
# Packages the race costs file does not carry (they have dedicated race jobs).
EXTRA_COSTS = {API: 2300, PREFIX + "cmd/crewship": 880}
SELECTION_FLAGS = ("-run", "-skip", "-shuffle", "-list")


def _api_race_shard():
    spec = importlib.util.spec_from_file_location("api_race_shard", Path(__file__).with_name("api-race-shard.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    if module.PACKAGE != API:
        raise ValueError("API test slices require RACE_SHARD_PACKAGE to be unset or internal/api")
    return module


def partition(packages, count, costs, default, preload=None):
    if count < 1:
        raise ValueError("shard count must be positive")
    if len(packages) < MIN_PACKAGES or len(packages) != len(set(packages)):
        raise ValueError(f"go list returned {len(packages)} packages (need >= {MIN_PACKAGES}, unique)")
    if any(not p.startswith(PREFIX) or any(c.isspace() for c in p) for p in packages):
        raise ValueError("unexpected package in go list inventory")
    if not isinstance(default, (int, float)) or not math.isfinite(default) or default <= 0:
        raise ValueError("default cost must be finite and positive")
    weight = lambda p: max(1, costs.get(p, default))
    groups = [[] for _ in range(count)]
    totals = [float(t) for t in preload] if preload is not None else [0.0] * count
    if len(totals) != count:
        raise ValueError("preload must give one weight per shard")
    for package in sorted(packages, key=lambda p: (-weight(p), p)):
        index = min(range(count), key=lambda i: (totals[i], i))
        groups[index].append(package)
        totals[index] += weight(package)
    flat = [p for g in groups for p in g]
    if sorted(flat) != sorted(packages) or not all(groups):
        raise ValueError("partition does not cover every package exactly once")
    return groups, totals


def split_plan(packages, api_listing, count, costs, default):
    """Package partitions without internal/api, plus its top-level test slices."""
    if API not in packages:
        raise ValueError("internal/api missing from go list; refusing to drop its tests")
    names, slices = _api_race_shard().partition(api_listing, count)
    selected = [name for chunk in slices for name in chunk]
    if len(selected) != len(set(selected)) or sorted(selected) != sorted(names):
        raise ValueError("internal/api slices do not cover its tests exactly once")
    share = max(1, costs.get(API, default)) / count
    groups, totals = partition([p for p in packages if p != API], count, costs, default, [share] * count)
    return groups, totals, names, slices


def verify_api_execution(directory, selected):
    """Package seconds when go test executed exactly `selected` and passed."""
    return _api_race_shard().completed_seconds(Path(directory), selected)


def run(index, count, extra, split_api=False):
    if not 0 <= index < count:
        raise ValueError("require 0 <= index < count")
    if split_api and any(a == f or a.startswith(f + "=") for a in extra for f in SELECTION_FLAGS):
        raise ValueError("--split-api owns test selection; drop " + "/".join(SELECTION_FLAGS))
    baseline = json.loads((ROOT / "scripts/ci/general-race-costs.json").read_text())
    costs = dict(baseline["packages"], **EXTRA_COSTS)
    listed = subprocess.run(["go", "list", "./..."], cwd=ROOT, text=True, stdout=subprocess.PIPE, check=True)
    packages = listed.stdout.split()
    if not split_api:
        groups, totals = partition(packages, count, costs, baseline["default_seconds"])
        print(f"Go package shard {index + 1}/{count}: {len(groups[index])} of {len(packages)} packages "
              f"(estimated weight {totals[index]:.0f}).", flush=True)
        return subprocess.run(["bash", "scripts/ci/go-test.sh", *groups[index], *extra], cwd=ROOT).returncode

    # Same build configuration as the run (no -race), so build-tagged tests match.
    listing = subprocess.run(["go", "test", "./internal/api", "-list", "."], cwd=ROOT, text=True,
                             stdout=subprocess.PIPE, check=True).stdout
    groups, totals, names, slices = split_plan(packages, listing, count, costs, baseline["default_seconds"])
    race = _api_race_shard()
    selected = slices[index]
    pattern = race.selection_pattern(selected)
    print(f"Go package shard {index + 1}/{count}: {len(groups[index])} of {len(packages) - 1} packages "
          f"+ internal/api slice of {len(selected)} of {len(names)} top-level tests "
          f"(inventory sha256 {race.fingerprint(names)}; estimated weight {totals[index]:.0f}).", flush=True)
    results = Path(os.environ.get("CI_RESULTS_DIR", ROOT / ".ci-results")).resolve()
    api_dir, packages_dir = results / "api", results / "packages"
    for directory in (api_dir, packages_dir):
        directory.mkdir(parents=True, exist_ok=True)
        (directory / "go-test.jsonl").unlink(missing_ok=True)
    # internal/api is ~90% serial, so its slice runs beside the package
    # partition; its output is buffered and printed after, never interleaved.
    api_log = api_dir / "go-test.log"
    with api_log.open("w") as log:
        api = subprocess.Popen(["bash", "scripts/ci/go-test.sh", API, *extra, "-run", pattern], cwd=ROOT,
                               env=dict(os.environ, CI_RESULTS_DIR=str(api_dir)),
                               stdout=log, stderr=subprocess.STDOUT)
        try:
            code = subprocess.run(["bash", "scripts/ci/go-test.sh", *groups[index], *extra], cwd=ROOT,
                                  env=dict(os.environ, CI_RESULTS_DIR=str(packages_dir))).returncode
        finally:
            api_code = api.wait()
    print(f"\n===== internal/api slice {index + 1}/{count} output =====", flush=True)
    sys.stdout.write(api_log.read_text())
    sys.stdout.flush()
    if api_code == 0:
        seconds = verify_api_execution(api_dir, selected)
        print(f"internal/api slice {index + 1}/{count}: exactly the {len(selected)} selected top-level tests "
              f"executed and passed ({seconds:.1f}s).", flush=True)
    else:
        print(f"::error::internal/api slice {index + 1}/{count} failed (exit {api_code}); output above.")
    return code or api_code


if __name__ == "__main__":
    try:
        if len(sys.argv) < 4 or sys.argv[1] != "run":
            raise ValueError("usage: go-package-shard.py run INDEX COUNT [--split-api] [go test args...]")
        split = len(sys.argv) > 4 and sys.argv[4] == "--split-api"
        sys.exit(run(int(sys.argv[2]), int(sys.argv[3]), sys.argv[5 if split else 4:], split_api=split))
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f"::error::{error}", file=sys.stderr)
        sys.exit(1)
