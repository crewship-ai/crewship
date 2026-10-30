#!/usr/bin/env bash
# Source after lib.sh. Each suite owns unique routine slugs and removes only
# successful saves from this invocation; existing workspace routines are untouched.
if ! have jq; then
  _fail "routine fixture setup requires jq"
  finish
fi
_HARNESS_FIXTURE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/crewship-harness.XXXXXX")" || exit 2
_HARNESS_FIXTURE_SLUGS=()
_HARNESS_FIXTURE_IDS=()
_HARNESS_FIXTURE_SCHEDULES=()
harness_fixture_cleanup() {
  local rc=$? slug failed=0 index=0 actual_id
  trap - EXIT
  for slug in ${_HARNESS_FIXTURE_SCHEDULES[@]+"${_HARNESS_FIXTURE_SCHEDULES[@]}"}; do
    if ! cs routine schedules delete "$slug" --yes >/dev/null 2>&1; then
      printf 'Fixture cleanup failed for owned schedule %s\n' "$slug" >&2
      failed=1
    fi
  done
  for slug in ${_HARNESS_FIXTURE_SLUGS[@]+"${_HARNESS_FIXTURE_SLUGS[@]}"}; do
    actual_id="$(cs routine get "$slug" --format json 2>/dev/null | jq -er '.id')"
    if [[ "$actual_id" != "${_HARNESS_FIXTURE_IDS[$index]}" ]]; then
      printf 'Fixture cleanup refused changed identity for %s\n' "$slug" >&2
      failed=1
      index=$((index + 1))
      continue
    fi
    index=$((index + 1))
    if ! cs routine delete "$slug" --yes >/dev/null 2>&1; then
      printf 'Fixture cleanup failed for owned routine %s\n' "$slug" >&2
      failed=1
    fi
  done
  rm -rf -- "$_HARNESS_FIXTURE_DIR"
  (( failed == 0 )) || rc=1
  exit "$rc"
}
trap harness_fixture_cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
harness_fixture() {
  local kind="$1" crew="$2" variable="$3" slug file id random
  random="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
  [[ "$random" =~ ^[0-9a-f]{32}$ ]] || { _fail "allocate fixture name"; finish; }
  slug="harness-$random-$kind"
  # Save is an upsert. Only an exact not-found permits this unguessable name.
  if cs routine get "$slug" --format json >"$_HARNESS_FIXTURE_DIR/preflight.log" 2>&1; then
    _fail "fixture name must be absent"
    finish
  elif ! grep -qF 'API error (404): pipeline not found' "$_HARNESS_FIXTURE_DIR/preflight.log"; then
    _fail "verify fixture name absence"
    finish
  fi
  file="$_HARNESS_FIXTURE_DIR/$kind.json"
  if ! jq --arg slug "$slug" '.name = $slug | if .name | endswith("-approval") then .steps[0].wait.approval_prompt = $slug else . end' "$_HARNESS_DIR/fixtures/$kind.json" >"$file"; then
    _fail "render $kind fixture"
    finish
  fi
  # No upsert of a shared seed slug: every name is unique to this suite.
  if ! cs routine save --name "$slug" --definition "$file" --author-crew "$crew" --format json >"$_HARNESS_FIXTURE_DIR/save.log" 2>&1; then
    _fail "save $kind fixture" "$(head -c 200 "$_HARNESS_FIXTURE_DIR/save.log")"
    finish
  fi
  id="$(jq -er --arg slug "$slug" 'select(.slug == $slug) | .id | select(type == "string" and length > 0)' "$_HARNESS_FIXTURE_DIR/save.log")" || { _fail "saved fixture reports its identity"; finish; }
  _HARNESS_FIXTURE_SLUGS+=("$slug")
  _HARNESS_FIXTURE_IDS+=("$id")
  printf -v "$variable" '%s' "$slug"
}
