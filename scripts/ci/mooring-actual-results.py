#!/usr/bin/env python3
"""Fail closed when an explicitly selected actual Go test did not execute.

Consumes go test -json output after the test process exits. Package success,
cached output, zero matches and skipped integration tests are not evidence of
actual execution. The caller must also preserve the go process exit status.
"""

import argparse
import json
from pathlib import Path


def verify(lines, package, expected):
    if not expected or len(expected) != len(set(expected)):
        raise ValueError("expected tests must be nonempty and unique")
    runs = {name: 0 for name in expected}
    passes = {name: 0 for name in expected}
    package_passes = 0
    for line in lines:
        event = json.loads(line)
        if not isinstance(event, dict) or event.get("Package") != package:
            raise ValueError("unexpected Go event package")
        action = event.get("Action")
        name = event.get("Test")
        if action in {"fail", "skip"}:
            raise ValueError(f"non-execution or failure: {name or package}: {action}")
        if not name:
            if action == "pass":
                package_passes += 1
            if event.get("Cached"):
                raise ValueError("cached integration result")
            continue
        top = name.split("/", 1)[0]
        if top not in runs:
            raise ValueError(f"unexpected test: {name}")
        if name == top and action == "run":
            runs[top] += 1
        if name == top and action == "pass":
            passes[top] += 1
    if package_passes != 1:
        raise ValueError("missing or duplicate package completion")
    if any(runs[name] != 1 or passes[name] != 1 for name in expected):
        raise ValueError("missing, duplicated or incomplete actual test execution")
    return {"package": package, "executed": sorted(expected), "skipped": 0}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package", required=True)
    parser.add_argument("--test", action="append", required=True)
    parser.add_argument("events", type=Path)
    args = parser.parse_args()
    try:
        with args.events.open(encoding="utf-8") as events:
            result = verify(events, args.package, args.test)
    except (OSError, ValueError, TypeError) as error:
        parser.exit(1, f"actual test qualification failed: {error}\n")
    print(json.dumps(result, sort_keys=True))


if __name__ == "__main__":
    main()
