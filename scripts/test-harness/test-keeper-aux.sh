#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper evaluator models — "can an operator change what the sweeps cost?"
#
# Drives the REAL `crewship keeper aux` CLI against a live server. What it
# proves, which no unit test can:
#   - `aux list` reports every slot with per-field provenance over HTTP
#   - `aux set` is a partial update — a field you don't pass is not touched
#   - an override reads back as source=instance, so it is really in force
#   - clearing a field returns it to the server's CREWSHIP_AUX_* value
#   - `aux use-judge` moves every slot onto the local judge in one call — the
#     "stop paying per token for governance" action
#   - a provider this build cannot construct is refused with the reason, not
#     saved into a slot that fails at first use
#   - `aux reset` drops one override, `--all` drops them all
#
# The other half — whether the model a slot points at actually answers — is
# `keeper judge test` and the aux-status card; this is the control plane.
#
# Runs against a shared dev instance, so the original configuration is captured
# up front and restored on exit, including provenance: a slot that was
# INHERITED goes back to inheriting rather than to a pinned copy of the value.

# Private scratch. The fixed /tmp/cs-*.out names this file used are guessable, so
# on a shared host a local user can pre-create or symlink them and redirect what
# the harness writes.
_TMP="$(mktemp -d "${TMPDIR:-/tmp}/cs-keeper-aux.XXXXXX")"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

# An older installed binary against a newer server is the classic skew — skip
# loudly rather than fail confusingly.
if ! cs keeper aux --help >/dev/null 2>&1; then
  skip "keeper aux CLI present" "installed crewship has no 'keeper aux' command — rebuild from this branch"
  finish
fi
if ! have jq; then
  skip "keeper aux assertions" "jq is required to read --format json"
  finish
fi

ORIG_JSON="$(cs keeper aux list --format json 2>/dev/null || echo '{}')"
if [[ "$(printf '%s' "$ORIG_JSON" | jq -r '.slots // empty')" == "" ]]; then
  skip "keeper aux readable" "aux list returned nothing — needs OWNER/ADMIN in the target workspace"
  finish
fi

# The slot the assertions run against. `behavior` is the live-applying one an
# operator is most likely to actually retune, and it is not the fallback.
SLOT="behavior"

slot_json() { cs keeper aux list --format json 2>/dev/null | jq -r --arg s "$1" '.slots[] | select(.slot == $s)'; }
slot_field() { slot_json "$1" | jq -r ".${2}.value  // empty"; }
slot_source() { slot_json "$1" | jq -r ".${2}.source // empty"; }

ORIG_SLOT="$(printf '%s' "$ORIG_JSON" | jq -r --arg s "$SLOT" '.slots[] | select(.slot == $s)')"
ORIG_PROVIDER="$(printf '%s' "$ORIG_SLOT" | jq -r '.provider.value  // empty')"
ORIG_MODEL="$(printf '%s' "$ORIG_SLOT" | jq -r '.model.value  // empty')"
ORIG_MODEL_SRC="$(printf '%s' "$ORIG_SLOT" | jq -r '.model.source // empty')"
ORIG_ANY="$(printf '%s' "$ORIG_JSON" | jq -r '.any_overridden')"
JUDGE_MODEL="$(printf '%s' "$ORIG_JSON" | jq -r '.judge_model // empty')"

info "starting state: $SLOT=$ORIG_PROVIDER/$ORIG_MODEL ($ORIG_MODEL_SRC) any_overridden=$ORIG_ANY judge=$JUDGE_MODEL"

# Restore to how it was FOUND. use-judge writes every slot, so a full reset is
# the only honest baseline when nothing was overridden to begin with; when
# something was, the probed slot is put back as an explicit override and the rest
# are left as the run found them.
restore_aux() {
  cs keeper aux reset --all >/dev/null 2>&1 || true
  # Replay the FULL snapshot taken at startup, slot by slot. Restoring only the
  # probed slot left every other operator override cleared — on a shared dev
  # instance that is data loss, and announcing it in a log line is not a restore.
  [[ -z "${ORIG_JSON:-}" ]] && return 0
  printf '%s' "$ORIG_JSON" | jq -r '
      .slots[]
      | select(.provider.source == "instance" or .model.source == "instance" or .timeout_ms.source == "instance")
      | [.slot, .provider.value, .model.value, (.timeout_ms.value|tostring)] | @tsv' 2>/dev/null |
  while IFS=$'\t' read -r slot provider model timeout; do
    [[ -z "$slot" ]] && continue
    local args=("$slot")
    [[ -n "$provider" ]] && args+=(--provider "$provider")
    [[ -n "$model" ]] && args+=(--model "$model")
    [[ -n "$timeout" && "$timeout" != "0" && "$timeout" != "null" ]] && args+=(--timeout "${timeout}ms")
    cs keeper aux set "${args[@]}" >/dev/null 2>&1 || true
  done
}
trap 'restore_aux; rm -rf "$_TMP"' EXIT

# ─────────────────────────────────────────────────────────────────────────────
section "1. aux list reports every slot with provenance"
# ─────────────────────────────────────────────────────────────────────────────
if cs keeper aux list >"$_TMP/aux.out" 2>&1; then
  _pass "keeper aux list exits 0"
else
  _fail "keeper aux list exits 0" "$(head -c 200 "$_TMP/aux.out" | tr '\n' ' ')"
fi
for s in curator behavior memory_health negative run_summary fallback; do
  assert_nonempty "list reports a model source for $s" "$(slot_source "$s" model)"
done
# The keeper slot is deliberately absent — nothing resolves it, so offering it
# would be a knob wired to nothing.
assert_eq "the unused keeper slot is not offered" "" "$(slot_source keeper model)"
# An operator who changes run_summary and sees nothing happen would call the
# feature broken; the row has to say a restart is needed.
assert_eq "run_summary declares it needs a restart" "restart" "$(slot_json run_summary | jq -r '.applies_at')"
assert_eq "$SLOT applies on the next evaluation"    "immediately" "$(slot_json "$SLOT" | jq -r '.applies_at')"

# ─────────────────────────────────────────────────────────────────────────────
section "2. an override takes effect and reads back as an instance value"
# ─────────────────────────────────────────────────────────────────────────────
# Opus 5 rather than a nonce: the picker used to jump from Fable 5 to 4.8, so
# "the current Opus is selectable" is the regression worth holding.
if cs keeper aux set "$SLOT" --provider anthropic --model claude-opus-5 >/dev/null 2>&1; then
  _pass "aux set --provider --model exits 0"
else
  _fail "aux set --provider --model exits 0" "set errored"
fi
assert_eq "the model we set is what is in force" "claude-opus-5" "$(slot_field "$SLOT" model)"
assert_eq "and it reports as an instance override" "instance"    "$(slot_source "$SLOT" model)"
assert_eq "any_overridden follows"                 "true"        "$(cs keeper aux list --format json 2>/dev/null | jq -r '.any_overridden')"

# ─────────────────────────────────────────────────────────────────────────────
section "3. a partial update leaves fields it did not mention alone"
# ─────────────────────────────────────────────────────────────────────────────
BEFORE_TIMEOUT="$(slot_field "$SLOT" timeout_ms)"
if ! cs keeper aux set "$SLOT" --model claude-haiku-4-5 >/dev/null 2>&1; then
  _fail "a model-only update succeeds" "the partial update errored"
fi
assert_eq "provider survived a model-only update" "anthropic"       "$(slot_field "$SLOT" provider)"
assert_eq "timeout survived it too"               "$BEFORE_TIMEOUT" "$(slot_field "$SLOT" timeout_ms)"

# A timeout is its own field with its own provenance.
if ! cs keeper aux set "$SLOT" --timeout 21s >/dev/null 2>&1; then
  _fail "a timeout-only update succeeds" "the partial update errored"
fi
assert_eq "the timeout we set is in force"      "21000"    "$(slot_field "$SLOT" timeout_ms)"
assert_eq "and reports as an instance override" "instance" "$(slot_source "$SLOT" timeout_ms)"

# ─────────────────────────────────────────────────────────────────────────────
section "4. clearing a field returns it to the server configuration"
# ─────────────────────────────────────────────────────────────────────────────
if ! cs keeper aux set "$SLOT" --timeout "" >/dev/null 2>&1; then
  _fail "clearing the timeout succeeds" "the clear errored"
fi
CLEARED_SRC="$(slot_source "$SLOT" timeout_ms)"
if [[ "$CLEARED_SRC" == "instance" ]]; then
  _fail "clearing the timeout drops the override" "still reports source=instance"
else
  _pass "clearing the timeout drops the override (now $CLEARED_SRC)"
fi
# And back to a real deadline, not to "no deadline".
CLEARED_TIMEOUT="$(slot_field "$SLOT" timeout_ms)"
if [[ -n "$CLEARED_TIMEOUT" && "$CLEARED_TIMEOUT" -gt 0 ]]; then
  _pass "the inherited deadline is back ($CLEARED_TIMEOUT ms)"
else
  _fail "the inherited deadline is back" "got '$CLEARED_TIMEOUT' — the slot has no deadline"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. a provider this build cannot construct is refused with the reason"
# ─────────────────────────────────────────────────────────────────────────────
if cs keeper aux set "$SLOT" --provider google --model gemini-2.0-flash >"$_TMP/bad.out" 2>&1; then
  _fail "an unbuildable provider is refused" "the server accepted google — the slot would fail at first use"
else
  _pass "an unbuildable provider is refused"
  assert_contains "the refusal says why, not just 'invalid'" \
    "$(tr '[:upper:]' '[:lower:]' <"$_TMP/bad.out")" "gemini"
fi
# A provider with no model cannot resolve — the builder needs both.
if cs keeper aux set curator --provider anthropic --model "" >"$_TMP/half.out" 2>&1; then
  _fail "a provider with no model is refused" "the server accepted a half-configured slot"
else
  _pass "a provider with no model is refused"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "6. use-judge moves every slot onto the local judge"
# ─────────────────────────────────────────────────────────────────────────────
if [[ -z "$JUDGE_MODEL" ]]; then
  skip "use-judge points every slot at the judge" "this target has no judge model configured"
else
  if cs keeper aux use-judge >/dev/null 2>&1; then
    _pass "keeper aux use-judge exits 0"
  else
    _fail "keeper aux use-judge exits 0" "use-judge errored"
  fi
  for s in curator behavior memory_health negative run_summary fallback; do
    assert_eq "$s runs on the local judge" "ollama" "$(slot_field "$s" provider)"
  done
  assert_eq "and on the judge's model" "$JUDGE_MODEL" "$(slot_field "$SLOT" model)"
  # Explicit per-slot rows, not a mode flag: one slot can still be moved back.
  assert_eq "each slot is an explicit override" "instance" "$(slot_source curator model)"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "7. reset drops one override, --all drops them all"
# ─────────────────────────────────────────────────────────────────────────────
cs keeper aux set "$SLOT" --provider anthropic --model claude-haiku-4-5 >/dev/null 2>&1 || true
cs keeper aux set curator --provider anthropic --model claude-opus-5 >/dev/null 2>&1 || true
if cs keeper aux reset "$SLOT" >/dev/null 2>&1; then
  _pass "keeper aux reset <slot> exits 0"
else
  _fail "keeper aux reset <slot> exits 0" "reset errored"
fi
RESET_SRC="$(slot_source "$SLOT" model)"
if [[ "$RESET_SRC" == "instance" ]]; then
  _fail "the reset slot stops being an override" "still reports source=instance"
else
  _pass "the reset slot stops being an override (now $RESET_SRC)"
fi
assert_eq "resetting one slot left another alone" "instance" "$(slot_source curator model)"

# A bare `reset` with no slot must not silently clear everything.
if cs keeper aux reset >/dev/null 2>&1; then
  _fail "a bare reset is refused" "it ran without naming a slot or --all"
else
  _pass "a bare reset is refused (name a slot, or --all)"
fi

if cs keeper aux reset --all >/dev/null 2>&1; then
  _pass "keeper aux reset --all exits 0"
else
  _fail "keeper aux reset --all exits 0" "reset --all errored"
fi
assert_eq "nothing is overridden after a full reset" "false" \
  "$(cs keeper aux list --format json 2>/dev/null | jq -r '.any_overridden')"

finish
