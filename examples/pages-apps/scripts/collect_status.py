#!/usr/bin/env python3
"""Bounded, token-free pilot collectors. Connection configuration stays in the crew."""
import argparse
import json
import os
import subprocess
import sys


def collect(kind):
    if kind == "mysql":
        config = os.environ.get("MYSQL_DEFAULTS_FILE")
        if not config:
            return {"items": [{"name": "MySQL", "state": "warning", "label": "Collector connection is not configured"}]}, True
        # SELECT 1 tests an authenticated query. mysqladmin ping alone can report
        # a living server even when authentication fails. Never put passwords in argv.
        command = ["mysql", "--defaults-extra-file=" + config, "--connect-timeout=5", "--batch", "--skip-column-names", "--execute=SELECT 1"]
        name, label, timeout = "MySQL", "Authenticated SELECT 1 completed", 10
    else:
        inventory = os.environ.get("ANSIBLE_INVENTORY_FILE")
        playbook = os.environ.get("ANSIBLE_PLAYBOOK_FILE")
        if not inventory or not playbook:
            return {"items": [{"name": "Ansible", "state": "warning", "label": "Collector inventory or playbook is not configured"}]}, True
        command = ["ansible-playbook", "--check", "--inventory", inventory, playbook]
        name, label, timeout = "Ansible", "Check mode completed; no deployment was requested", 120
    try:
        # Discard tool output: SQL/Ansible diagnostics may contain credentials or
        # customer data. The authenticated query's exit status is the only signal.
        result = subprocess.run(command, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, timeout=timeout, check=False)
        if result.returncode != 0:
            return {"items": [{"name": name, "state": "critical", "label": "Check failed; inspect the routine in its authorized environment"}]}, True
        return {"items": [{"name": name, "state": "ok", "label": label}]}, False
    except (OSError, subprocess.TimeoutExpired):
        return {"items": [{"name": name, "state": "warning", "label": "Collector unavailable or timed out; outcome is unknown"}]}, True


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("kind", choices=["mysql", "ansible"])
    parser.add_argument("--routine-output", action="store_true", help="Emit scalar fields for a following page.write step; the check verdict is data")
    parser.add_argument("--publish", metavar="PAGE/PANEL", help="Publish through the existing authorized Crewship CLI session")
    args = parser.parse_args()
    payload, failed = collect(args.kind)
    if args.routine_output:
        if args.publish:
            parser.error("--routine-output cannot be combined with --publish")
        print(json.dumps({**payload["items"][0], "producer_state": "failed" if failed else "ok"}))
        return 0
    encoded = json.dumps(payload)
    if args.publish:
        command = ["crewship", "page", "set", args.publish, "--data", "-"]
        if failed:
            command += ["--state", "failed"]
        # A write failure fails the routine. Never report a snapshot as delivered
        # based only on the collector succeeding.
        subprocess.run(command, input=encoded, text=True, check=True, timeout=20)
    else:
        print(encoded)
    return int(failed)


if __name__ == "__main__":
    sys.exit(main())
