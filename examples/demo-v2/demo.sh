#!/usr/bin/env bash
# Demo v2 — small manifest steps, applied one at a time, checked in the UI.
#
#   ./demo.sh list                  the steps, in order
#   ./demo.sh plan  <step>          dry-run: what apply would create/update/delete
#   ./demo.sh apply <step>          create or update the step's resources (never deletes)
#   ./demo.sh reset <step>          delete + recreate them — "run it again from scratch"
#   ./demo.sh bind-model            bind the workspace's model credential to the Lab agents
#
# apply/reset of 00-crew also binds the model credential and builds the
# crew's container (crewship crew provision lab) — a few minutes the first
# time, then cached.
#
# <step> is a directory name (01-issues), its number (1) or a file path.
# A directory applies every *.yaml in it, in name order.
#
# Target: CREWSHIP_PROFILE / CREWSHIP_SERVER as for any crewship command.
# Binary: CREWSHIP (default: ./crewship at the repo root, else PATH).
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
if [[ -z "${CREWSHIP:-}" ]]; then
  if [[ -x "$ROOT/crewship" ]]; then CREWSHIP="$ROOT/crewship"; else CREWSHIP="$(command -v crewship || echo "$ROOT/crewship")"; fi
fi
CREW_SLUG="lab"

cs() { "$CREWSHIP" "$@"; }

steps() { find "$HERE" -mindepth 1 -maxdepth 1 -type d -name '[0-9][0-9]-*' | sort; }

list() {
  printf '%-4s %-14s %s\n' '#' 'STEP' 'FILES'
  local d
  for d in $(steps); do
    local n="${d##*/}"
    printf '%-4s %-14s %s\n' "$((10#${n%%-*}))" "$n" "$(find "$d" -maxdepth 1 -name '*.yaml' -printf '%f ' | sort)"
  done
  printf '\napply one:  %s apply <step>     re-run:  %s reset <step>\n' "${0##*/}" "${0##*/}"
}

# resolve <step> — the yaml files to apply, one per line.
resolve() {
  local want="$1" d
  if [[ -f "$want" ]]; then printf '%s\n' "$want"; return; fi
  if [[ -f "$HERE/$want" ]]; then printf '%s\n' "$HERE/$want"; return; fi
  for d in $(steps); do
    local n="${d##*/}"
    if [[ "$n" == "$want" || "$((10#${n%%-*}))" == "$want" || "$n" == "$want"-* ]]; then
      find "$d" -maxdepth 1 -name '*.yaml' | sort
      return
    fi
  done
}

# bind_model — the crew manifest declares no credential slot (see
# 00-crew/lab.crew.yaml). Find the ACTIVE Anthropic credential the seed made
# and assign it to every Lab agent that does not have it yet.
bind_model() {
  local cred agents a
  cred="$(cs credential list --format json 2>/dev/null \
    | jq -r '[.[]? | select(.provider == "ANTHROPIC" and .status == "ACTIVE")] | first | .name // empty' 2>/dev/null)"
  if [[ -z "$cred" ]]; then
    echo "bind-model: no ACTIVE ANTHROPIC credential in this workspace — run crewship seed, or create one" >&2
    return 1
  fi
  agents="$(cs agent list --format json 2>/dev/null | jq -r --arg c "$CREW_SLUG" '.[]? | select((.crew.slug // .crew_slug // "") == $c) | .slug' 2>/dev/null)"
  [[ -z "$agents" ]] && agents="$(cs agent list 2>/dev/null | awk -v c="$CREW_SLUG" '$4 == c {print $2}')"
  for a in $agents; do
    if cs credential assign "$cred" "$a" --env-var-name "$cred" >/dev/null 2>&1; then
      echo "  + $cred → $a"
    else
      echo "  = $cred → $a (already bound, or refused — see: crewship credential resolve $a)"
    fi
  done
}

apply_files() { # <mode> <files...>
  local mode="$1"; shift
  local f rc=0
  for f in "$@"; do
    printf '\n== %s %s ==\n' "$mode" "${f#"$HERE"/}"
    case "$mode" in
      plan)  cs apply --file "$f" --dry-run ;;
      apply) cs apply --file "$f" --no-delete --yes ;;
      reset) cs apply --file "$f" --replace --yes ;;
    esac || rc=1
  done
  return $rc
}

cmd="${1:-list}"; shift || true
case "$cmd" in
  list|-h|--help) list ;;
  plan|apply|reset)
    [[ -z "${1:-}" ]] && { echo "usage: ${0##*/} $cmd <step>" >&2; list >&2; exit 2; }
    mapfile -t files < <(resolve "$1")
    (( ${#files[@]} == 0 )) && { echo "unknown step '$1'" >&2; list >&2; exit 2; }
    apply_files "$cmd" "${files[@]}"; rc=$?
    # A fresh or recreated crew has agents without a model and no container:
    # bind the credential, then build (provision streams until done).
    if [[ "$cmd" != plan && "${files[0]}" == *"/00-crew/"* ]]; then
      printf '\n== bind-model ==\n'; bind_model || rc=1
      printf '\n== provision %s ==\n' "$CREW_SLUG"; cs crew provision "$CREW_SLUG" || rc=1
    fi
    exit $rc ;;
  bind-model) bind_model ;;
  *) echo "unknown command '$cmd'" >&2; list >&2; exit 2 ;;
esac
