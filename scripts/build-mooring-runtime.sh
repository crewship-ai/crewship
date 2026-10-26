#!/usr/bin/env bash
# Called inside the existing crewship-heavy build, never installs host files.
set -euo pipefail
exec python3 "$(dirname "$0")/build-mooring-runtime.py" "$@"
