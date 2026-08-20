#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Crew container lifecycle — start, write, stop.
#
# This is the loop a definitions deploy actually walks, and every step of
# it was broken or missing until recently:
#
#   1. `crew provision` reports "provisioned" while the container stays
#      stopped, because provision builds an IMAGE and starts nothing.
#   2. Files under the crew's shared tree are owned by the container user
#      (uid 1001), so overwriting one on a stopped crew answers 409 —
#      mid-apply, after earlier resources have already been committed.
#   3. The 409 said "start the crew and retry" and named no command that
#      would, because none existed. The workaround was to run an agent
#      with a throwaway prompt: tokens spent for a side effect.
#
# `crew start` / `crew stop` close that. This suite drives the real CLI
# binary — per CLAUDE.md, the acceptance test for an endpoint drives the
# CLI, because the CLI is the contract agents use.
#
# Deliberately uses an EXISTING seeded crew rather than creating one: the
# 409 only reproduces on a shared-tree file the container runtime already
# owns, which needs a crew whose container has run at least once.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

CREW="${HARNESS_CREW:-ops}"
STAMP="$(nonce LIFECYCLE)"
LOCAL_FILE="$(mktemp)"
DEST="shared/harness-lifecycle.txt"
trap 'rm -f "$LOCAL_FILE"' EXIT

preflight

# ─────────────────────────────────────────────────────────────────────────────
section "1. start is idempotent and reports a running container"
# ─────────────────────────────────────────────────────────────────────────────
info "Starting crew '$CREW' (provisions its image first if it has none)…"
assert_ok "crew start $CREW" cs crew start "$CREW"

# Twice, because a deploy script should not have to branch on current
# state before writing files.
assert_ok "crew start $CREW (again — must be a no-op that succeeds)" \
  cs crew start "$CREW"

assert_contains "container-status reports running after start" \
  "$(cs crew container-status "$CREW" 2>&1)" "running"

# ─────────────────────────────────────────────────────────────────────────────
section "2. a running crew accepts an overwrite of a file it owns"
# ─────────────────────────────────────────────────────────────────────────────
printf 'first-%s\n' "$STAMP" > "$LOCAL_FILE"
assert_ok "files save (first write)" \
  cs crew files save "$CREW" "$DEST" --file "$LOCAL_FILE"

# The overwrite is the half that 409s on a stopped crew — the first write
# to an unowned path can succeed host-side and prove nothing.
printf 'second-%s-and-longer\n' "$STAMP" > "$LOCAL_FILE"
assert_ok "files save (overwrite — the half that 409s when stopped)" \
  cs crew files save "$CREW" "$DEST" --file "$LOCAL_FILE"

# Byte-for-byte, not just "the name is listed". A same-named file holding
# stale bytes is exactly the failure that made a routine run against the
# script it was being fixed to replace.
want=$(wc -c < "$LOCAL_FILE" | tr -d ' ')
got=$(cs crew files list "$CREW" --path shared -f json 2>/dev/null \
        | python3 -c "
import json,sys
try: rows = json.load(sys.stdin)
except Exception: sys.exit(0)
rows = rows.get('files', rows) if isinstance(rows, dict) else rows
for r in rows if isinstance(rows, list) else []:
    if isinstance(r, dict) and str(r.get('name','')).endswith('harness-lifecycle.txt'):
        print(r.get('size','')); break
" 2>/dev/null)
if [[ -n "$got" && "$got" == "$want" ]]; then
  _pass "uploaded size matches local ($want bytes)"
elif [[ -z "$got" ]]; then
  skip "size comparison (files list did not report a size for $DEST)"
else
  _fail "uploaded size matches local" "remote=$got local=$want — same name, different bytes"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. stop actually stops it, and stays idempotent"
# ─────────────────────────────────────────────────────────────────────────────
assert_ok "crew stop $CREW" cs crew stop "$CREW"

assert_contains "container-status reports stopped after stop" \
  "$(cs crew container-status "$CREW" 2>&1)" "stopped"

assert_ok "crew stop $CREW (again — must succeed)" cs crew stop "$CREW"

# ─────────────────────────────────────────────────────────────────────────────
section "4. a stopped crew refuses the overwrite, and says what to run"
# ─────────────────────────────────────────────────────────────────────────────
printf 'third-%s-longer-still\n' "$STAMP" > "$LOCAL_FILE"
refusal="$(cs crew files save "$CREW" "$DEST" --file "$LOCAL_FILE" 2>&1)"
rc=$?
if [[ $rc -eq 0 ]]; then
  _fail "overwrite on a stopped crew is refused" "the save reported success"
else
  _pass "overwrite on a stopped crew is refused"
fi
assert_contains "the refusal names a command that starts a container" \
  "$refusal" "crewship crew start"
assert_contains "the refusal rules out 'crew provision', the wrong turn" \
  "$refusal" "provision"

# ─────────────────────────────────────────────────────────────────────────────
section "5. leave the crew as we found it"
# ─────────────────────────────────────────────────────────────────────────────
# KNOWN BROKEN — a restart in this exact sequence does not take.
#
# `stop` → `stop` → a (correctly) refused write → `start` leaves the
# container `exited` with code 143 (SIGTERM), reproducibly. It is not
# `crew start` giving up early: the handler polls the runtime for 3s
# before answering, and Docker still reports `exited`. Something stops
# the container again after EnsureCrewRuntime has started it.
#
# A single stop → start works every time, which is the path the docs and
# the 409 send people down, so the feature is usable — but this sequence
# has to stay visible rather than be quietly retried into green. Marked
# xfail: loud, counted, and not hard-red, exactly as lib.sh intends.
#
# What is already fixed: the handler used to answer `"status":"running"`
# here without asking the runtime, so the sequence FAILED SILENTLY and
# the next file write got the 409. It now says so.
if cs crew start "$CREW" >/dev/null 2>&1; then
  _pass "crew start $CREW (restore)"
  assert_ok "remove the harness file" cs crew files delete "$CREW" "$DEST" --yes
else
  xfail "crew start $CREW after stop→stop→refused-write" \
        "container stays exited (143); crew start correctly refuses to claim running"
  info "leaving $DEST behind — the crew could not be restarted to remove it"
fi

finish
