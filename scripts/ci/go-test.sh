#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
# pipefail preserves the test process exit status even when the reporter succeeds.
go test -json "$@" | python3 "$root/scripts/ci/go-events.py"
