#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "$0")/../.." && pwd)
# Under -race the compiler also enables checkptr, which instruments every
# unsafe.Pointer conversion. modernc.org/* is SQLite transpiled from C and
# does that constantly: the migration chain runs 55s with it, 29s without.
# Only that third-party code loses checkptr; race detection itself and
# checkptr for every crewship package stay on.
for arg in "$@"; do
  if [ "$arg" = "-race" ]; then
    set -- "${RACE_GCFLAGS:--gcflags=modernc.org/...=-d=checkptr=0}" "$@"
    break
  fi
done
# pipefail preserves the test process exit status even when the reporter succeeds.
go test -json "$@" | python3 "$root/scripts/ci/go-events.py"
