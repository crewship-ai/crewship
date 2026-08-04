#!/usr/bin/env python3
"""Emit the small, secret-free result contract for the live API harness."""

import argparse
import json
import re
import xml.etree.ElementTree as ET
from pathlib import Path


SCHEMA_MARKERS = (
    "response schema",
    "schema conformance",
    "invalid schema",
    "schema error",
    "openapi",
)


def classify_failure(text: str, exit_code: int) -> str:
    lowered = text.lower()
    if any(marker in lowered for marker in SCHEMA_MARKERS):
        return "schema"
    # Schemathesis uses 2 for schema/configuration aborts and 1 for executed
    # checks. An executed but unrecognised failure is conservatively runtime.
    return "schema" if exit_code == 2 else "runtime"


def junit_failures(path: str, exit_code: int) -> list[dict[str, str]]:
    if not path or not Path(path).is_file():
        return []
    try:
        root = ET.parse(path).getroot()
    except (ET.ParseError, OSError):
        return [{"name": "junit", "class": "schema"}]
    failures = []
    for case in root.iter("testcase"):
        failure = case.find("failure")
        error = case.find("error")
        item = failure if failure is not None else error
        if item is None:
            continue
        text = " ".join(filter(None, [item.get("message"), item.text]))
        failures.append(
            {
                "name": case.get("name", "unknown"),
                "class": classify_failure(text, exit_code),
            }
        )
    return failures[:20]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--phase", required=True)
    parser.add_argument("--exit-code", type=int, required=True)
    parser.add_argument("--failure-class", default="")
    parser.add_argument("--failure-message", default="")
    parser.add_argument("--schema-file", required=True)
    parser.add_argument("--junit-file", required=True)
    parser.add_argument("--run-log", required=True)
    for name in ("catalog", "selected", "excluded-auth", "excluded-non-json", "excluded-method"):
        parser.add_argument(f"--{name}-count", type=int, required=True)
    args = parser.parse_args()

    failures = junit_failures(args.junit_file, args.exit_code)
    failure_class = args.failure_class or ""
    if not failure_class and failures:
        classes = {item["class"] for item in failures}
        failure_class = "schema" if classes == {"schema"} else "runtime"
    if args.exit_code == 0:
        status = "passed"
        failure_class = "none"
    elif args.exit_code == 2:
        failure_class = failure_class or "schema"
        status = "blocked"
    else:
        status = "failed"
    summary = {
        "tool": "crewship-api-contract",
        "phase": args.phase,
        "status": status,
        "failure_class": failure_class or "runtime",
        "operations": {
            "catalog": args.catalog_count,
            "selected": args.selected_count,
            "excluded": {
                "methods": args.excluded_method_count,
                "auth_ui": args.excluded_auth_count,
                "non_json": args.excluded_non_json_count,
            },
        },
        "limits": {"max_examples_per_operation": 10, "workers": 1, "request_timeout_seconds": 10},
    }
    if failures:
        summary["failures"] = failures
    if args.failure_message:
        summary["message"] = re.sub(r"(?i)(bearer\s+)[^\s]+", r"\1<redacted>", args.failure_message)
    print(json.dumps(summary, separators=(",", ":"), sort_keys=True))


if __name__ == "__main__":
    main()
