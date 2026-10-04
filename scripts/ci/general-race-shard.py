#!/usr/bin/env python3
"""Balance complete Go packages; the measured costs never select coverage."""
import argparse
import json
import math
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PREFIX = "github.com/crewship-ai/crewship/"
# Structural migration-fixture exclusion; API and CLI are required separate jobs.
DENIED = {PREFIX + "internal/database"}
DEDICATED = {PREFIX + "internal/api", PREFIX + "cmd/crewship"}


def partition(packages, count, costs, default):
    if count < 1:
        raise ValueError("shard count must be positive")
    if not packages or len(packages) != len(set(packages)):
        raise ValueError("go list inventory must be nonempty and unique")
    if any(not p.startswith(PREFIX) or any(c.isspace() for c in p) for p in packages):
        raise ValueError("unexpected package in go list inventory")
    missing = (DENIED | DEDICATED) - set(packages)
    if missing:
        raise ValueError(f"stale race exclusions/dedicated packages: {sorted(missing)}")
    if not isinstance(default, (int, float)) or not math.isfinite(default) or default <= 0:
        raise ValueError("default cost must be finite and positive")
    if any(not isinstance(v, (int, float)) or not math.isfinite(v) or v < 0 for v in costs.values()):
        raise ValueError("measured costs must be finite and nonnegative")
    eligible = set(packages) - DENIED - DEDICATED
    if count > len(eligible):
        raise ValueError("shard count would leave an empty shard")
    weight = lambda p: max(1, costs.get(p, default))
    groups, totals = [[] for _ in range(count)], [0.0] * count
    # Largest first, with stable path/index tie breaks. Unknown packages get a
    # conservative default; renamed/removed cost entries cannot hide inventory.
    for package in sorted(eligible, key=lambda p: (-weight(p), p)):
        index = min(range(count), key=lambda i: (totals[i], i))
        groups[index].append(package)
        totals[index] += weight(package)
    flattened = [p for group in groups for p in group]
    if set(flattened) != eligible or len(flattened) != len(eligible) or not all(groups):
        raise ValueError("race partition does not cover every eligible package exactly once")
    return groups, totals


def run(index, count, timeout):
    if not 0 <= index < count or timeout <= 0:
        raise ValueError("require 0 <= index < count and positive timeout seconds")
    baseline = json.loads((ROOT / "scripts/ci/general-race-costs.json").read_text())
    listed = subprocess.run(["go", "list", "./..."], cwd=ROOT, text=True,
                            stdout=subprocess.PIPE, check=True)
    packages = listed.stdout.splitlines()
    groups, totals = partition(packages, count, baseline["packages"], baseline["default_seconds"])
    results = ROOT / ".ci-results"
    results.mkdir(exist_ok=True)
    (results / "general-race-shard.json").write_text(json.dumps({
        "index": index, "count": count, "inventory": sorted(packages),
        "denied": sorted(DENIED), "dedicated": sorted(DEDICATED),
        "partitions": groups, "selected": groups[index],
        "estimated_package_seconds": totals, "source_run": baseline["source_run"],
        "default_seconds": baseline["default_seconds"],
    }, indent=2) + "\n")
    print(f"Race shard {index + 1}/{count}: {len(groups[index])} of {sum(map(len, groups))} "
          f"eligible packages; {len(DENIED)} excluded, {len(DEDICATED)} in dedicated jobs. "
          f"Estimated sum of package durations {totals[index]:.1f}s (not wall clock).", flush=True)
    return subprocess.run(["bash", "scripts/ci/go-test.sh", *groups[index],
                           "-race", "-count=1", "-timeout", f"{timeout}s"], cwd=ROOT).returncode


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["run"])
    parser.add_argument("index", type=int)
    parser.add_argument("count", type=int)
    parser.add_argument("timeout_seconds", type=int)
    args = parser.parse_args()
    try:
        return run(args.index, args.count, args.timeout_seconds)
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        print(f"::error::{error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
