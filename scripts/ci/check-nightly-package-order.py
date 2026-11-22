#!/usr/bin/env python3
"""Use each native comparator on the actual GoReleaser output before signing the manifest."""
import os
from pathlib import Path
import re
import subprocess
import sys

def output(*args, **options):
    return subprocess.check_output(args, text=True, **options).strip()

def greater(fmt, newer, older):
    if fmt == "deb":
        return subprocess.run(["dpkg", "--compare-versions", newer, "gt", older]).returncode == 0
    comparison = output("rpm", "--eval",
                        '%{lua:print(rpm.vercmp(os.getenv("NEW_EVR"),os.getenv("OLD_EVR")))}',
                        env=dict(os.environ, NEW_EVR=newer, OLD_EVR=older))
    return comparison == "1"

def check(directory, tag):
    date, counter = re.fullmatch(r"nightly-([0-9]{8})-r([1-9][0-9]*)", tag).groups()
    for fmt in ("deb", "rpm"):
        package, = directory.glob(f"crewship_*_linux_amd64.{fmt}")
        actual = (output("dpkg-deb", "-f", package, "Version") if fmt == "deb"
                  else output("rpm", "-qp", "--qf", "%{VERSION}", package))
        # nFPM normalizes internal hyphens in RPM versions to underscores.
        pattern = rf"(0[.]0[.]1~nightly[-_]{date}[-_]r){counter}([-_]g)[0-9a-f]+"
        match = re.fullmatch(pattern, actual)
        if not match:
            raise ValueError(f"{fmt}: unexpected version {actual} for {tag}")
        prefix, suffix = match.groups()
        older = f"{prefix}{int(counter)-1}{suffix}fffffffff"
        newer = f"{prefix}{int(counter)+1}{suffix}000000000"
        if not all((greater(fmt, actual, "0.0.0~snapshot-9ffffffff"),
                    greater(fmt, actual, older), greater(fmt, newer, actual))):
            raise ValueError(f"{fmt}: {actual} breaks nightly upgrade ordering")
        print(f"{fmt}: {older} < {actual} < {newer}")

if __name__ == "__main__":
    check(Path(sys.argv[1]), sys.argv[2])
