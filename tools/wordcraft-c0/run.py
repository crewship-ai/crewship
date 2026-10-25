#!/usr/bin/env python3
"""Opt-in runtime probes; every actual RED remains a nonzero test result.
Run through crewship-heavy. No servers, environments or application file/network
commands are started. Rust dependencies use only the committed public pins.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

CASES = [
    "c0_1_block_identity_survives_split_join_undo_restart",
    "c0_2_utf8_contract_rejects_inside_grapheme_without_snapping",
    "c0_3_author_and_authoritative_time_reach_engine_before_command",
    "c0_4_allowlist_rejects_unknown_fields_and_process_globals",
    "c0_5_bounded_history_pending_metrics_and_m2_rollback_retention",
    "c0_6_participant_undo_must_preserve_other_authors_work",
]
PIN = "7ef2633a7e84956522d1ca47b99fe6feef16c1db"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    manifest = Path(__file__).resolve().with_name("Cargo.toml")
    common = ["cargo", "test", "--manifest-path", str(manifest), "--release", "--locked", "--test", "conditions"]
    with (output / "build.log").open("w") as log:
        build = subprocess.run([*common, "--no-run"], stdout=log, stderr=subprocess.STDOUT)
    if build.returncode:
        print("C0 build failed; no runtime evidence. See build.log", file=sys.stderr)
        return 2
    results = []
    for case in CASES:
        with (output / (case + ".log")).open("w") as log:
            run = subprocess.run([*common, case, "--", "--exact", "--nocapture", "--test-threads=1"], stdout=log, stderr=subprocess.STDOUT, env={**os.environ, "C0_OUTPUT_DIR": str(output)})
        raw = (output / (case + ".log")).read_text()
        evidence = [json.loads(line.split("C0_EVIDENCE ", 1)[1]) for line in raw.splitlines() if "C0_EVIDENCE " in line]
        executed = "running 1 test" in raw and any(row["engine_pin"] == PIN for row in evidence)
        outcome = "PASS" if run.returncode == 0 and executed else "RED" if run.returncode == 101 and executed else "HARNESS_ERROR"
        row = {"test": case, "exit": run.returncode, "outcome": outcome, "actual_evidence": evidence}
        results.append(row)
        print(json.dumps(row), flush=True)
    report = {"pin": PIN, "actual_cases": results, "scope": "Standalone unpatched native engine only; no C1 host, Mooring transport/SQL, deploy or user acceptance."}
    (output / "results.json").write_text(json.dumps(report, indent=2) + "\n")
    # RED is never silently converted to green. Infrastructure failures are
    # distinct from actual assertions; the current unmodified baseline is RED.
    return 2 if any(r["outcome"] == "HARNESS_ERROR" for r in results) else int(any(r["outcome"] == "RED" for r in results))


if __name__ == "__main__":
    sys.exit(main())
