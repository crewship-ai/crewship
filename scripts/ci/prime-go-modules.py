#!/usr/bin/env python3
"""Bound Go module fetches separately from compilation and test execution."""

import argparse
import subprocess
import time


def download(modules, timeout):
    for attempt in range(1, 4):
        try:
            subprocess.run(["go", "mod", "download", *modules], timeout=timeout, check=True)
            return 0
        except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
            print(f"Go module download attempt {attempt}/3 failed: {error}", flush=True)
            if attempt == 3:
                return 1
            time.sleep(2 * attempt)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--attempt-timeout", type=int, default=60, metavar="SECONDS")
    parser.add_argument("modules", nargs="*", help="specific module@version arguments")
    args = parser.parse_args()
    if not 1 <= args.attempt_timeout <= 60:
        parser.error("each attempt must be between 1 and 60 seconds")
    return download(args.modules, args.attempt_timeout)


if __name__ == "__main__":
    raise SystemExit(main())
