#!/usr/bin/env bash
# pr-image-build-paths.sh — keeps the PR image build's trigger honest against
# what the Dockerfile actually needs.
#
# Why this exists (#2064 half (b)): .github/workflows/pr-image-build.yml runs
# `docker build` on a pull request, but only when the diff touches a path in
# its `paths:` filter — that is what keeps it off the vast majority of PRs
# instead of every one. A filter that goes stale is silent: add a new `COPY`
# source to the Dockerfile (exactly the #849/#886 shape — a root-level package
# the backend stage now needs) without adding it here, and the image build
# keeps not running on the PR that most needs it, while every other check
# stays green.
#
# So this reads both files as committed and fails on disagreement, the same
# idiom as scripts/go-toolchain-pin.sh: the Dockerfile is the source of truth
# for what a build needs, and every non `--from=` `COPY` source it names must
# appear in the workflow's `paths:` list (a directory source `foo/` is
# satisfied by a `foo/**` entry). A handful of paths the Dockerfile pulls in
# without a literal `COPY` — the Dockerfile itself, `.dockerignore`, and
# `prisma/**` (read only via the frontend stage's `COPY . .` for `pnpm prisma
# generate`, see #2064's second comment) — are required separately, since
# nothing about parsing `COPY` lines could ever discover them.
#
# It does NOT build anything — it is pure bash over the source tree, cheap
# enough to run unconditionally on every PR (wired into ci.yml's `Shell` job),
# same placement rationale as go-toolchain-pin.sh: the drift has to be
# catchable on the PR that introduces it, which means the check itself cannot
# be behind the same path filter it is guarding.
#
# Usage: bash scripts/pr-image-build-paths.sh [root-dir]
#
# The optional root-dir is how scripts/pr-image-build-paths-test.sh points the
# same parser at fixture trees that disagree on purpose.

set -uo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

FAILURES=0
fail() { printf '  MISSING  %s\n' "$1"; FAILURES=$((FAILURES + 1)); }
ok() { printf '  ok       %s\n' "$1"; }

fatal() {
  printf 'FATAL: %s\n' "$1" >&2
  shift
  for line in "$@"; do printf '       %s\n' "$line" >&2; done
  exit 1
}

DOCKERFILE="$ROOT/Dockerfile"
WORKFLOW="$ROOT/.github/workflows/pr-image-build.yml"

[ -f "$DOCKERFILE" ] || fatal "no Dockerfile at $DOCKERFILE"
[ -f "$WORKFLOW" ] || fatal "no .github/workflows/pr-image-build.yml at $WORKFLOW" \
  "That is the PR-triggered image build itself (#2064) — without it this" \
  "guard has nothing to check its paths against."

# ---------------------------------------------------------------------------
# Required triggers
# ---------------------------------------------------------------------------
if ! grep -qE '^[[:space:]]*pull_request:[[:space:]]*$' "$WORKFLOW"; then
  fail "$WORKFLOW has no 'pull_request:' trigger — the image build must run on PRs, that is the entire point of #2064"
else
  ok "pull_request: trigger present"
fi

if ! grep -qE '^[[:space:]]*workflow_dispatch:' "$WORKFLOW"; then
  fail "$WORKFLOW has no 'workflow_dispatch:' trigger — needed for a manual re-run (see release.yml / nightly.yml, both of which have one)"
else
  ok "workflow_dispatch: trigger present"
fi

# ---------------------------------------------------------------------------
# The image must not be pushed from a PR
# ---------------------------------------------------------------------------
if grep -qE '^[[:space:]]*push:[[:space:]]*true[[:space:]]*(#.*)?$' "$WORKFLOW"; then
  fail "$WORKFLOW sets 'push: true' — a PR build must build, never push (the issue is explicit: 'builds but does not push')"
else
  ok "image is not pushed (no 'push: true')"
fi

# ---------------------------------------------------------------------------
# Extract the workflow's paths: list
# ---------------------------------------------------------------------------
# One state machine, not a `sed -n '/paths:/,/^[a-z]/p'` range: the latter has
# no way to stop at the FIRST closing line without also swallowing blank lines
# and comments inside the list, which is exactly the shape that goes silently
# wrong the day a path entry is commented out for debugging and the range
# keeps reading past it.
mapfile -t PATH_LINES < <(awk '
  /^[[:space:]]*paths:[[:space:]]*$/ { inlist = 1; next }
  inlist && /^[[:space:]]*-[[:space:]]*/ { print; next }
  inlist && /^[[:space:]]*#/ { next }
  inlist && /^[[:space:]]*$/ { next }
  inlist { inlist = 0 }
' "$WORKFLOW")

if [ "${#PATH_LINES[@]}" -eq 0 ]; then
  fatal "no 'paths:' list found under any trigger in $WORKFLOW" \
    "Without one the workflow runs on every PR, which is the cost #2064's" \
    "own suggested shape says path-filtering exists to avoid."
fi

declare -A PATHS=()
for line in "${PATH_LINES[@]}"; do
  v="$(printf '%s' "$line" | sed -E "s/^[[:space:]]*-[[:space:]]*//; s/^['\"]//; s/['\"][[:space:]]*(#.*)?\$//")"
  [ -n "$v" ] && PATHS["$v"]=1
done

echo "paths: list (${#PATHS[@]} entries) in ${WORKFLOW#"$ROOT"/}:"
for k in "${!PATHS[@]}"; do printf '    %s\n' "$k"; done | sort
echo

# ---------------------------------------------------------------------------
# Collect what the Dockerfile actually COPYs (excluding multi-stage
# `--from=` copies, which read from a previous stage, not the build context)
# ---------------------------------------------------------------------------
declare -A NEEDED=()
while IFS= read -r line; do
  rest="${line#COPY}"
  rest="$(printf '%s' "$rest" | sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//')"
  # shellcheck disable=SC2206
  toks=($rest)
  n=${#toks[@]}
  [ "$n" -ge 2 ] || continue
  # Last token is the destination; everything before it is a source.
  for ((i = 0; i < n - 1; i++)); do
    src="${toks[$i]}"
    [ "$src" = "." ] && continue
    NEEDED["$src"]=1
  done
done < <(grep -E '^COPY[[:space:]]' "$DOCKERFILE" | grep -v -- '--from=')

if [ "${#NEEDED[@]}" -eq 0 ]; then
  fatal "found no non --from= COPY source in $DOCKERFILE" \
    "Either the Dockerfile no longer copies anything from the build context" \
    "(unlikely) or this parser stopped matching its COPY lines — either way" \
    "this guard has nothing to check and would pass everything."
fi

# Named separately: not literal COPY sources, so nothing above can discover
# them, but the image build depends on all three (see header).
NEEDED["Dockerfile"]=1
NEEDED[".dockerignore"]=1
NEEDED["prisma/**"]=1

# ---------------------------------------------------------------------------
# Every needed source must be covered by the paths: list
# ---------------------------------------------------------------------------
covered() {
  local src="$1"
  # Exact match (files, and glob entries like prisma/**).
  [ -n "${PATHS[$src]:-}" ] && return 0
  # A directory source ("cmd/") is covered by its "cmd/**" glob.
  if [[ "$src" == */ ]]; then
    [ -n "${PATHS[${src}**]:-}" ] && return 0
  fi
  return 1
}

for src in "${!NEEDED[@]}"; do
  if covered "$src"; then
    ok "$src"
  else
    fail "$src — Dockerfile needs it but it is not in ${WORKFLOW#"$ROOT"/}'s paths: list"
  fi
done

# The workflow's own file must trigger it, or an edit to the filter itself
# (including a regression this guard would have caught) never runs the check.
if [ -n "${PATHS[.github/workflows/pr-image-build.yml]:-}" ]; then
  ok ".github/workflows/pr-image-build.yml (self-trigger)"
else
  fail ".github/workflows/pr-image-build.yml is not in its own paths: list — an edit to the filter itself would never re-run the build that proves the edit right"
fi

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "pr-image-build.yml's paths: list covers everything the Dockerfile needs."
else
  echo "$FAILURES path(s) missing from pr-image-build.yml's paths: list (#2064)."
fi
exit $(( FAILURES > 0 ? 1 : 0 ))
