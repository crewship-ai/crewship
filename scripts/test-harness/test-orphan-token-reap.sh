#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Orphaned-container reap — the #1385 stable-master remediation lever.
#
# Background: when crewshipd restarts with a DIFFERENT internal-token master
# while a per-crew agent container survives, that container keeps the crew-bound
# token it was minted with and the new process rejects it forever
# ("WARN internal API auth failed: invalid crew-bound token"), spamming the log
# every reap interval. PR #1387 makes the master STABLE across restarts (derived
# from ENCRYPTION_KEY) so a normal restart no longer orphans anything, and adds
# `crewship admin reap-orphan-containers` as the operator lever to clear any
# container that outlived the one deploy that first rotates the master.
#
# A real restart-then-assert-no-spam loop isn't scriptable from the CLI harness
# (it can't bounce the server), and the restart-invalidation property is locked
# by the Go unit tests (TestCrewTokenSurvivesRestart, the token-fingerprint
# orphan classifier, and the reap-handler suite). What the harness CAN prove
# end-to-end through the REAL CLI is the durable-fix property that matters
# operationally: against a normally-running, stable-master server every live
# container's token validates, so the reap sweep finds ZERO orphans — no false
# positives that would reap a healthy container — and the dry-run never mutates.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

# ─────────────────────────────────────────────────────────────────────────────
section "1. The reap-orphan-containers command exists in the CLI"
# ─────────────────────────────────────────────────────────────────────────────
help_out="$(cs admin reap-orphan-containers --help 2>&1)"
if printf '%s' "$help_out" | grep -qi "stale internal token"; then
  _pass "admin reap-orphan-containers is wired (API↔CLI parity)"
else
  _fail "admin reap-orphan-containers --help" "$(printf '%s' "$help_out" | head -c 200 | tr '\n' ' ')"
  finish
fi

# ─────────────────────────────────────────────────────────────────────────────
section "2. Dry-run against the stable-master server finds no orphans"
# ─────────────────────────────────────────────────────────────────────────────
info "Running the dry-run sweep (report only — no --apply)…"
dry_out="$(cs admin reap-orphan-containers 2>&1)"
dry_rc=$?

if printf '%s' "$dry_out" | grep -qi "docker not configured\|unavailable"; then
  skip "orphan reap dry-run" "server container provider is not docker (503)"
  finish
fi

if (( dry_rc != 0 )); then
  _fail "orphan reap dry-run exits cleanly" "rc=$dry_rc: $(printf '%s' "$dry_out" | head -c 200 | tr '\n' ' ')"
else
  _pass "orphan reap dry-run exits cleanly"
fi

# On a normally-running server the master that minted every live container's
# token is the SAME one validating now, so nothing is orphaned. A non-empty
# "Found N orphaned" here would be a false positive (the detector reaping a
# healthy container) — exactly what the fail-safe classifier must never do.
assert_contains "no orphaned containers on a stable-master server" "$dry_out" "No orphaned crew containers found"

# ─────────────────────────────────────────────────────────────────────────────
section "3. Dry-run is non-mutating and idempotent"
# ─────────────────────────────────────────────────────────────────────────────
info "Re-running the dry-run — it must stay clean (no state was changed)…"
dry_out2="$(cs admin reap-orphan-containers 2>&1)"
assert_contains "dry-run is stable on a second call" "$dry_out2" "No orphaned crew containers found"

finish
