#!/usr/bin/env python3
"""Bound Go module fetches separately from compilation and test execution."""

import argparse
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time


def download(timeout, *, cwd=None, external=False):
    command = ["go", "mod", "download"]
    env = None
    if external:
        command.append("all")
        env = dict(os.environ, GOWORK="off")
    for attempt in range(1, 4):
        try:
            subprocess.run(command, cwd=cwd, env=env, timeout=timeout, check=True)
            return 0
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
            print(f"Go module download attempt {attempt}/3 failed: {error}", flush=True)
            if attempt == 3:
                return 1
            time.sleep(2 * attempt)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--attempt-timeout", type=int, default=60, metavar="SECONDS")
    parser.add_argument("--tool", metavar="MODULE@VERSION", help="download a pinned tool and its module graph")
    args = parser.parse_args()
    if not 1 <= args.attempt_timeout <= 60:
        parser.error("each attempt must be between 1 and 60 seconds")
    if args.tool is None:
        return download(args.attempt_timeout)
    module, _, version = args.tool.partition("@")
    if not re.fullmatch(r"[A-Za-z0-9._/-]+", module) or not re.fullmatch(r"v[0-9][A-Za-z0-9.+-]*", version):
        parser.error("the tool must have a module path and an explicit version")
    # Go resolves the external tool graph independently of the repository.
    with tempfile.TemporaryDirectory(prefix="ci-go-modules-") as directory:
        (Path(directory) / "go.mod").write_text(
            f"module ci-tools\n\ngo 1.16\n\nrequire {module} {version}\n",
            encoding="utf-8",
        )
        return download(args.attempt_timeout, cwd=directory, external=True)


if __name__ == "__main__":
    raise SystemExit(main())
