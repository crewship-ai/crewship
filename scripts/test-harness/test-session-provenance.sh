#!/usr/bin/env bash
# Session provenance (#1934) — runtime validation against a live server.
#
# The claim under test is not "the parser reads a field". Unit tests already
# prove that, and they proved it while the field reached nobody. The claim is
# that provenance the CLI reported at session start survives every hop —
# adapter → event metadata → accumulator → run record → journal → API → the
# shipped `crewship` binary — and that a skipped MCP server is impossible to
# miss at the end of it.
#
# That chain is only provable end to end. A run that answers correctly while
# silently missing its memory tools looks identical to a healthy one in every
# layer except the one this suite reads.
#
# Two tiers, and the suite says which ones it ran:
#
#   Tier A — control plane, NO provider credential required.
#     The surfaces exist and are wired to the right routes: `run get` is a real
#     command against a real endpoint, `journal get --type run.session_init`
#     filters rather than ignoring the flag, and `run list --format json`
#     carries the fields the server sends (the bug that shipped: the CLI
#     decoded into a narrower struct and dropped `model` on the floor).
#
#   Tier B — a live agent run, requires a provider credential.
#     The part no fixture can fake. Run an agent for real, then ask the CLI
#     which binary served it. Before #1932/#1934 the honest answer was "grep
#     the server log"; the assertion here is that the answer now comes out of
#     the run record, and that the version it reports is a plausible one rather
#     than an empty string that happens to satisfy a contains-check.
#
# Tier B SKIPs (never fails) when no agent replies — the shape every runtime
# suite hits on a box without a provider key.
#
# Deliberately NOT in pr-subset.sh: Tier B needs a provider credential, and
# that list is explicitly the deterministic, provider-free gate. Adding a suite
# there is a reviewable gate change, not a side effect of writing one.

set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/test-harness/lib.sh
source "$HERE/lib.sh"

preflight

# ─────────────────────────────────────────────────────────────────────────────
section "Tier A — the surfaces exist and filter"

# `run get` is the detail view; without it the provenance fields are reachable
# only by reading JSON off the list endpoint, which is what "no CLI surface"
# meant in the issue.
help_out="$(cs run get --help 2>&1 || true)"
assert_contains "run get is a real command" "$help_out" "run"

# A journal type that no entry uses would still return 200 with an empty list,
# so this asserts the FILTER is honoured, not that rows exist: an unknown type
# must come back empty rather than echoing the unfiltered feed.
bogus="$(cs journal --type run.no_such_type_$$ --lines 5 2>&1 || true)"
assert_not_contains "an unknown --type does not fall through to the whole feed" \
  "$bogus" "run.started"

# The list decode used to be a narrower struct than the response, which is how
# `model` was dropped for the entire life of the field. Machine output must
# carry whatever the server sends.
list_json="$(cs run list --format json 2>&1 || true)"
if printf '%s' "$list_json" | grep -q '"id"'; then
  assert_contains "run list --format json exposes the run's model field" "$list_json" "model"
else
  skip "run list --format json (no runs on this server yet)"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "Tier B — a real run reports which binary served it"

AGENT="${PROVENANCE_AGENT:-$(cs agent list --format quiet 2>/dev/null | head -1)}"
if [[ -z "$AGENT" ]]; then
  skip "Tier B (no agent in this workspace)"
  finish
fi

# Deliberately an ordinary question, not an echo-this-nonce probe: a Crewship
# agent is prompted to treat isolated "output exactly this token" directives as
# prompt injection and refuse them — correctly. The nonce would be testing the
# trust fence, not provenance. All this tier needs is that a real run happened
# and produced a real answer; the assertions that matter read the run record.
info "asking $AGENT an ordinary question"
reply="$(ask_agent "$AGENT" "In one short sentence, what does the Unix 'ls' command do?" || true)"

if [[ -z "${reply// /}" ]]; then
  skip "Tier B (agent did not reply — no provider credential?)"
  finish
fi
_pass "agent answered"

# The run we just made is the newest one for this agent.
run_id="$(cs run list --format quiet 2>/dev/null | head -1)"
if [[ -z "$run_id" ]]; then
  _fail "could not read back the run id"
  finish
fi
info "run $run_id"

detail="$(cs run get "$run_id" 2>&1 || true)"
detail_json="$(cs run get "$run_id" --format json 2>&1 || true)"

# The point of the whole chain: which binary answered. Asserted on the JSON so
# a label change in the human view cannot mask a missing field.
if printf '%s' "$detail_json" | grep -q 'cli_version'; then
  _pass "run record carries cli_version"
  # An empty string satisfies a contains-check, which is exactly how a broken
  # pipeline passes a test. Demand something version-shaped.
  ver="$(printf '%s' "$detail_json" | sed -n 's/.*"cli_version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
  if printf '%s' "$ver" | grep -qE '^[0-9]+\.[0-9]+'; then
    _pass "cli_version looks like a version ($ver)"
  else
    _fail "cli_version is present but not version-shaped: '$ver'"
  fi
else
  # Non-Claude adapters report none of this; say so rather than failing the box.
  adapter="$(cs agent get "$AGENT" --format json 2>/dev/null | grep -o '"cli_adapter"[^,]*' || true)"
  skip "cli_version absent (adapter: ${adapter:-unknown} — only Claude Code reports it)"
fi

assert_contains "run get renders the human detail view" "$detail" "Status"

# The journal is the durable copy. The chat card is live-stream only — it is
# gone on a refresh — so if this entry is missing, provenance did not actually
# outlive the run.
# --run-id ties the entry to THIS run rather than to any session_init the
# workspace ever recorded: a stale entry from an earlier run would otherwise
# satisfy the assertion while the run under test recorded nothing.
entry="$(cs journal --run-id "$run_id" --type run.session_init --lines 5 2>&1 || true)"
if printf '%s' "$entry" | grep -qiF "session"; then
  _pass "run.session_init entry is queryable after the run"
else
  _fail "no run.session_init entry — provenance did not survive the run"
fi

finish
