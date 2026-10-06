#!/usr/bin/env python3
"""Run one package partition of the full (non-race) Go suite.

run INDEX COUNT [go test args...] lists every package, splits them largest-first
by measured cost into COUNT partitions, proves the partitions cover the listing
exactly once, and runs partition INDEX through scripts/ci/go-test.sh. Costs only
decide placement; every listed package always runs in exactly one partition.
"""
import json
import math
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PREFIX = "github.com/crewship-ai/crewship/"
MIN_PACKAGES = 90
# Packages the race costs file does not carry (they have dedicated race jobs).
EXTRA_COSTS = {PREFIX + "internal/api": 2300, PREFIX + "cmd/crewship": 880}


def partition(packages, count, costs, default):
    if count < 1:
        raise ValueError("shard count must be positive")
    if len(packages) < MIN_PACKAGES or len(packages) != len(set(packages)):
        raise ValueError(f"go list returned {len(packages)} packages (need >= {MIN_PACKAGES}, unique)")
    if any(not p.startswith(PREFIX) or any(c.isspace() for c in p) for p in packages):
        raise ValueError("unexpected package in go list inventory")
    if not isinstance(default, (int, float)) or not math.isfinite(default) or default <= 0:
        raise ValueError("default cost must be finite and positive")
    weight = lambda p: max(1, costs.get(p, default))
    groups, totals = [[] for _ in range(count)], [0.0] * count
    for package in sorted(packages, key=lambda p: (-weight(p), p)):
        index = min(range(count), key=lambda i: (totals[i], i))
        groups[index].append(package)
        totals[index] += weight(package)
    flat = [p for g in groups for p in g]
    if sorted(flat) != sorted(packages) or not all(groups):
        raise ValueError("partition does not cover every package exactly once")
    return groups, totals


def run(index, count, extra):
    if not 0 <= index < count:
        raise ValueError("require 0 <= index < count")
    baseline = json.loads((ROOT / "scripts/ci/general-race-costs.json").read_text())
    costs = dict(baseline["packages"], **EXTRA_COSTS)
    listed = subprocess.run(["go", "list", "./..."], cwd=ROOT, text=True, stdout=subprocess.PIPE, check=True)
    packages = listed.stdout.split()
    groups, totals = partition(packages, count, costs, baseline["default_seconds"])
    print(f"Go package shard {index + 1}/{count}: {len(groups[index])} of {len(packages)} packages "
          f"(estimated weight {totals[index]:.0f}).", flush=True)
    return subprocess.run(["bash", "scripts/ci/go-test.sh", *groups[index], *extra], cwd=ROOT).returncode


if __name__ == "__main__":
    try:
        if len(sys.argv) < 4 or sys.argv[1] != "run":
            raise ValueError("usage: go-package-shard.py run INDEX COUNT [go test args...]")
        sys.exit(run(int(sys.argv[2]), int(sys.argv[3]), sys.argv[4:]))
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f"::error::{error}", file=sys.stderr)
        sys.exit(1)
