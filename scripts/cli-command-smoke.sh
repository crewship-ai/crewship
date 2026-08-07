#!/usr/bin/env bash
# C1 — breadth smoke over the shipped CLI command tree.
#
# This deliberately exercises only parser/help/error paths. It does not invent
# positional arguments or call mutating commands against a server. The manifest
# comes from the same binary under test, so adding a command automatically adds
# it to this gate.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLI="${CREWSHIP:-$ROOT/crewship}"
BAD_FLAG="--__crewship_contract_invalid_flag"
TMP="$(mktemp -d -t crewship-cli-smoke.XXXXXX)"
trap 'rm -rf "$TMP"' EXIT
export HOME="$TMP/home"
mkdir -p "$HOME"

if [[ ! -x "$CLI" ]]; then
  echo "error: CLI binary not found or not executable: $CLI" >&2
  exit 2
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "error: python3 is required to decode the command manifest" >&2
  exit 2
fi

manifest="$TMP/manifest.json"
if ! "$CLI" --no-color commands --format json >"$manifest" 2>"$TMP/manifest.err"; then
  echo "error: crewship commands --format json failed" >&2
  cat "$TMP/manifest.err" >&2
  exit 1
fi

paths="$TMP/paths"
if ! python3 - "$manifest" >"$paths" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as fh:
    doc = json.load(fh)

def walk(nodes):
    for node in nodes:
        path = node.get("path", "").strip()
        if not path:
            raise SystemExit("manifest contains a command without path")
        print(path)
        walk(node.get("commands", []))

walk(doc.get("commands", []))
PY
then
  echo "error: command manifest is not a valid Crewship tree" >&2
  exit 1
fi

count=0
failures=0
pass() { count=$((count + 1)); }
fail() {
  failures=$((failures + 1))
  printf 'FAIL %s: %s\n' "$1" "$2" >&2
}

while IFS= read -r path; do
  [[ -z "$path" ]] && continue
  count=$((count + 1))
  read -r -a words <<< "$path"

  help_out="$TMP/help.out"
  help_err="$TMP/help.err"
  if ! "$CLI" --no-color "${words[@]}" --help >"$help_out" 2>"$help_err"; then
    fail "$path --help" "exit code was non-zero"
  elif [[ ! -s "$help_out" && ! -s "$help_err" ]]; then
    fail "$path --help" "produced no help text"
  fi
  if grep -Eiq '(^|[[:space:]])(goroutine [0-9]+|panic:)' "$help_out" "$help_err"; then
    fail "$path --help" "panic-like output detected"
  fi

  invalid_out="$TMP/invalid.out"
  invalid_err="$TMP/invalid.err"
  if "$CLI" --no-color "${words[@]}" "$BAD_FLAG" >"$invalid_out" 2>"$invalid_err"; then
    fail "$path invalid flag" "unexpected exit code 0"
  fi
  if ! grep -Fqi -- "$BAD_FLAG" "$invalid_out" "$invalid_err"; then
    fail "$path invalid flag" "error did not name $BAD_FLAG"
  fi
  if grep -Eiq '(^|[[:space:]])(goroutine [0-9]+|panic:)' "$invalid_out" "$invalid_err"; then
    fail "$path invalid flag" "panic-like output detected"
  fi

  # The invalid invocation is intentional: it exercises the command's
  # --format=json error path without inventing arguments or performing a
  # mutating operation. Every command inherits the global --format flag.
  json_out="$TMP/json.out"
  json_err="$TMP/json.err"
  # Put the persistent flag before the command path. Cobra accepts persistent
  # flags after a child only when that child declares the flag itself; the
  # manifest must not make that placement assumption.
  "$CLI" --no-color --format json "${words[@]}" "$BAD_FLAG" >"$json_out" 2>"$json_err" || true
  if ! python3 - "$json_out" "$json_err" <<'PY'
import json
import sys

for name in sys.argv[1:]:
    with open(name, encoding="utf-8", errors="replace") as fh:
        text = fh.read().strip()
    if not text:
        continue
    try:
        value = json.loads(text)
    except json.JSONDecodeError:
        continue
    if isinstance(value, dict):
        raise SystemExit(0)
raise SystemExit(1)
PY
  then
    fail "$path --format json" "no parseable JSON response"
  fi
done < "$paths"

if (( count == 0 )); then
  echo "error: command manifest contained no commands" >&2
  exit 1
fi
printf 'CLI breadth smoke: %d command nodes checked, %d failures\n' "$count" "$failures"
(( failures == 0 ))
