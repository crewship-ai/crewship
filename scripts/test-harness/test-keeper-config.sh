#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper instance judge configuration — "can an operator set the judge without a restart?"
#
# Drives the REAL `crewship keeper config` CLI against a live server. What it
# proves, which no unit test can:
#   - `config get` reports the effective judge AND where each value came from
#   - `config set` is a partial update — a field you don't pass is not touched
#   - an override reads back as source=instance, so the change is really in force
#   - clearing a field returns it to the server's KEEPER_* value (source=env)
#   - the fail-closed guard holds over HTTP: enabling Keeper with no judge is a
#     400, not an instance that DENYs every credential request
#   - `config reset` drops every override
#
# This is the control-plane half. Whether the configured judge can actually be
# reached and returns a parseable verdict is `keeper judge test` — a separate
# script, because it needs a real model on the target.
#
# Runs against a shared dev instance, so the original configuration is captured
# up front and restored on exit, including its provenance: a field that was
# INHERITED goes back to inheriting rather than to a pinned copy of the same
# value.

# Private scratch: the fixed /tmp names this file used are guessable, so on a
# shared host a local user can pre-create or symlink them.
_TMP="$(mktemp -d "${TMPDIR:-/tmp}/cs-keeper-config.XXXXXX")"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight

# An older installed binary against a newer server is the classic skew — skip
# loudly rather than fail confusingly.
if ! cs keeper config --help >/dev/null 2>&1; then
  skip "keeper config CLI present" "installed crewship has no 'keeper config' command — rebuild from this branch"
  finish
fi
if ! have jq; then
  skip "keeper config assertions" "jq is required to read --format json"
  finish
fi

ORIG_JSON="$(cs keeper config get --format json 2>/dev/null || echo '{}')"
if [[ "$ORIG_JSON" == '{}' ]]; then
  skip "keeper config readable" "config get returned nothing — needs OWNER/ADMIN in the target workspace"
  finish
fi

field()  { printf '%s' "$ORIG_JSON" | jq -r ".${1}.value  // empty"; }
source_of() { printf '%s' "$ORIG_JSON" | jq -r ".${1}.source // empty"; }

# NOT via field(): jq's `//` treats `false` as absent, so a disabled engine would
# read back as an empty string and the state line would say "enabled=".
ORIG_ENABLED="$(printf '%s' "$ORIG_JSON" | jq -r '.enabled.value')"
ORIG_ENABLED_SRC="$(source_of enabled)"
ORIG_MODEL="$(field judge_model)"
ORIG_MODEL_SRC="$(source_of judge_model)"
ORIG_ENDPOINT="$(field judge_endpoint_url)"
ORIG_ENDPOINT_SRC="$(source_of judge_endpoint_url)"

info "starting state: enabled=$ORIG_ENABLED ($ORIG_ENABLED_SRC) model=$ORIG_MODEL ($ORIG_MODEL_SRC) endpoint=$ORIG_ENDPOINT ($ORIG_ENDPOINT_SRC)"

# Restore each field to how it was FOUND, not to a copy of its value: putting an
# inherited field back as an instance override would leave the instance pinned to
# a value it was only borrowing.
restore_config() {
  local args=()
  case "$ORIG_ENABLED_SRC" in
    instance) [[ "$ORIG_ENABLED" == "true" ]] && args+=(--enabled on) || args+=(--enabled off) ;;
    *)        args+=(--enabled inherit) ;;
  esac
  if [[ "$ORIG_MODEL_SRC" == "instance" ]]; then args+=(--model "$ORIG_MODEL"); else args+=(--model ""); fi
  if [[ "$ORIG_ENDPOINT_SRC" == "instance" ]]; then args+=(--endpoint "$ORIG_ENDPOINT"); else args+=(--endpoint ""); fi
  cs keeper config set "${args[@]}" >/dev/null 2>&1 || true
}
trap 'restore_config; rm -rf "$_TMP"' EXIT

cfg_field() { cs keeper config get --format json 2>/dev/null | jq -r ".${1}.value  // empty"; }
cfg_source() { cs keeper config get --format json 2>/dev/null | jq -r ".${1}.source // empty"; }

# ─────────────────────────────────────────────────────────────────────────────
section "1. config get reports the effective judge with provenance"
# ─────────────────────────────────────────────────────────────────────────────
if cs keeper config get >"$_TMP/config.out" 2>&1; then
  _pass "keeper config get exits 0"
else
  _fail "keeper config get exits 0" "$(head -c 200 "$_TMP/config.out" | tr '\n' ' ')"
fi
for f in enabled judge_provider judge_endpoint_url judge_wire judge_model; do
  assert_nonempty "get reports a source for $f" "$(cfg_source "$f")"
done

# ─────────────────────────────────────────────────────────────────────────────
section "2. an override takes effect and reads back as an instance value"
# ─────────────────────────────────────────────────────────────────────────────
PROBE_MODEL="harness-probe-model:$(nonce)"
if cs keeper config set --model "$PROBE_MODEL" >/dev/null 2>&1; then
  _pass "config set --model exits 0"
else
  _fail "config set --model exits 0" "set errored"
fi
assert_eq "the model we set is what is in force" "$PROBE_MODEL" "$(cfg_field judge_model)"
assert_eq "and it reports as an instance override"  "instance"    "$(cfg_source judge_model)"

# ─────────────────────────────────────────────────────────────────────────────
section "3. a partial update leaves fields it did not mention alone"
# ─────────────────────────────────────────────────────────────────────────────
BEFORE_ENDPOINT="$(cfg_field judge_endpoint_url)"
BEFORE_ENDPOINT_SRC="$(cfg_source judge_endpoint_url)"
cs keeper config set --model "${PROBE_MODEL}-2" >/dev/null 2>&1 || true
assert_eq "endpoint value survived a model-only update"    "$BEFORE_ENDPOINT"     "$(cfg_field judge_endpoint_url)"
assert_eq "endpoint provenance survived it too"            "$BEFORE_ENDPOINT_SRC" "$(cfg_source judge_endpoint_url)"

# ─────────────────────────────────────────────────────────────────────────────
section "4. clearing a field returns it to the server configuration"
# ─────────────────────────────────────────────────────────────────────────────
# Two legal outcomes, and swallowing the exit code could not tell them apart —
# which is how this section started reporting a product regression that was the
# fail-closed guard working. With the engine ON and nothing to inherit, clearing
# the model is REFUSED: an enabled Keeper with no judge denies every credential
# request, so the server will not let you configure that state.
# What is in force immediately BEFORE the attempt — section 3 has already moved
# the model, so the starting value is the wrong thing to compare a refusal
# against.
BEFORE_CLEAR="$(cfg_field judge_model)"
if cs keeper config set --model "" >"$_TMP/clear.out" 2>&1; then
  CLEARED_SRC="$(cfg_source judge_model)"
  if [[ "$CLEARED_SRC" == "instance" ]]; then
    _fail "clearing the model drops the override" "still reports source=instance"
  else
    _pass "clearing the model drops the override (now $CLEARED_SRC)"
  fi
  assert_eq "the inherited model is back" "$ORIG_MODEL" "$(cfg_field judge_model)"
else
  # Refused. That is correct only when there is genuinely nothing to fall back
  # to; a refusal with an inherited model waiting would be a real bug.
  if [[ "$ORIG_ENABLED" == "true" && "$ORIG_MODEL_SRC" == "instance" ]]; then
    _pass "clearing the only judge model is refused while the engine is on"
    assert_contains "the refusal names the fail-closed reason" \
      "$(tr '[:upper:]' '[:lower:]' <"$_TMP/clear.out")" "fail-closed"
    assert_eq "and the model is untouched" "$BEFORE_CLEAR" "$(cfg_field judge_model)"
  else
    _fail "clearing the model drops the override" \
      "refused even though there is an inherited value to fall back to: $(head -c 160 "$_TMP/clear.out" | tr '\n' ' ')"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. Keeper cannot be enabled without a judge (fail-closed guard)"
# ─────────────────────────────────────────────────────────────────────────────
# Only meaningful on a target with no judge configured; where one exists, this is
# a legal request and there is nothing to assert.
if [[ -z "$(cfg_field judge_endpoint_url)" || -z "$(cfg_field judge_model)" ]]; then
  if cs keeper config set --enabled on >"$_TMP/enable.out" 2>&1; then
    _fail "enabling with no judge is refused" "the server accepted it — every credential request would DENY"
  else
    _pass "enabling with no judge is refused"
    assert_contains "the refusal names what is missing" \
      "$(tr '[:upper:]' '[:lower:]' <"$_TMP/enable.out")" "model"
  fi
else
  skip "enabling with no judge is refused" "this target has a judge configured — nothing to provoke"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "6. reset drops every override"
# ─────────────────────────────────────────────────────────────────────────────
cs keeper config set --model "harness-reset-probe:$(nonce)" >/dev/null 2>&1 || true
if cs keeper config reset >/dev/null 2>&1; then
  _pass "keeper config reset exits 0"
else
  _fail "keeper config reset exits 0" "reset errored"
fi
assert_eq "nothing is overridden after a reset" "false" \
  "$(cs keeper config get --format json 2>/dev/null | jq -r '.overridden')"
# After a full reset the model is whatever the SERVER config says, which is the
# starting value only when the starting value was inherited. When it was an
# instance override, reset drops it — and on an instance with no KEEPER_MODEL that
# legitimately leaves it empty.
if [[ "$ORIG_MODEL_SRC" == "instance" ]]; then
  assert_eq "the instance override is gone after a reset" "" "$(cfg_source judge_model | grep -x instance || true)"
else
  assert_eq "the server's model is in force again" "$ORIG_MODEL" "$(cfg_field judge_model)"
fi

finish
