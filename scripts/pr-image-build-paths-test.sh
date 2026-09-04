#!/usr/bin/env bash
# pr-image-build-paths-test.sh — negative tests for scripts/pr-image-build-paths.sh.
#
# Same rationale as scripts/go-toolchain-pin-test.sh: a consistency guard that
# has only ever run against an already-consistent tree proves nothing about
# whether it can go red. Each case here builds a small fixture tree, breaks
# exactly one thing, and asserts both the exit status and that the message
# names the thing at fault.
#
# Usage: bash scripts/pr-image-build-paths-test.sh

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="$SCRIPT_DIR/pr-image-build-paths.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

TMPROOT="$(mktemp -d)"
trap 'rm -rf "$TMPROOT"' EXIT

# A Dockerfile with the same shape as the real one: an explicit COPY for each
# of the source trees the backend stage needs, plus the frontend's blanket
# `COPY . .`. $1 is a directory to name in COPY that the caller can omit
# entirely, to simulate a source the Dockerfile grew that the paths list never
# heard about.
make_dockerfile() {
  local dir="$1" extra_copy_dir="${2:-}"
  cat > "$dir/Dockerfile" <<EOF
FROM node:22-alpine AS frontend
WORKDIR /app
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY . .
RUN pnpm prisma generate
RUN pnpm build

FROM golang:1.27.0-alpine AS backend
WORKDIR /app
COPY go.mod go.sum ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY schemas/ ./schemas/
COPY web/ ./web/
EOF
  if [ -n "$extra_copy_dir" ]; then
    echo "COPY $extra_copy_dir ./$extra_copy_dir" >> "$dir/Dockerfile"
  fi
  cat >> "$dir/Dockerfile" <<'EOF'
COPY --from=frontend /app/out ./web/out

FROM alpine:3.24
COPY --from=backend /crewship /usr/local/bin/crewship
COPY docker/server-entrypoint.sh /usr/local/bin/crewship-entrypoint
EOF
}

# The paths: list that agrees with make_dockerfile's default (no extra COPY
# dir). Overrides let a case drop one line, flip push, or drop a trigger.
make_workflow() {
  local dir="$1"
  local drop_path="${2:-}"      # a paths: entry to omit
  local include_push="${3:-0}"  # 1 = add `push: true`
  local include_dispatch="${4:-1}"  # 0 = omit workflow_dispatch
  local include_pr_trigger="${5:-1}" # 0 = omit pull_request:

  mkdir -p "$dir/.github/workflows"
  {
    echo "name: PR Image Build"
    echo "on:"
    if [ "$include_pr_trigger" = "1" ]; then
      echo "  pull_request:"
      echo "    branches: [main]"
      echo "    paths:"
      for p in "Dockerfile" ".dockerignore" "go.mod" "go.sum" "package.json" \
               "pnpm-lock.yaml" "pnpm-workspace.yaml" "prisma/**" "cmd/**" \
               "internal/**" "schemas/**" "web/**" \
               "docker/server-entrypoint.sh" \
               ".github/workflows/pr-image-build.yml"; do
        [ "$p" = "$drop_path" ] && continue
        echo "      - \"$p\""
      done
    fi
    if [ "$include_dispatch" = "1" ]; then
      echo "  workflow_dispatch: {}"
    fi
    echo "jobs:"
    echo "  build:"
    echo "    runs-on: ubuntu-latest"
    echo "    steps:"
    echo "      - name: Build"
    echo "        uses: docker/build-push-action@abc"
    echo "        with:"
    echo "          context: ."
    if [ "$include_push" = "1" ]; then
      echo "          push: true"
    else
      echo "          load: true"
    fi
  } > "$dir/.github/workflows/pr-image-build.yml"
}

run_guard() {
  bash "$GUARD" "$1"
}

expect_pass() {
  local name="$1" dir="$2"
  local out status
  out="$(run_guard "$dir" 2>&1)"; status=$?
  if [ "$status" -eq 0 ]; then
    pass "$name"
  else
    fail "$name (expected pass, got exit $status)"
    echo "$out" | awk '{ print "    " $0 }'
  fi
}

expect_fail() {
  local name="$1" dir="$2" want_substr="$3"
  local out status
  out="$(run_guard "$dir" 2>&1)"; status=$?
  if [ "$status" -eq 0 ]; then
    fail "$name (expected failure, guard exited 0)"
    return
  fi
  if echo "$out" | grep -qF "$want_substr"; then
    pass "$name"
  else
    fail "$name (exit $status, but output did not contain expected text: '$want_substr')"
    echo "$out" | awk '{ print "    " $0 }'
  fi
}

# ---------------------------------------------------------------------------
# Case: a fully consistent tree passes.
# ---------------------------------------------------------------------------
d="$TMPROOT/consistent"
mkdir -p "$d"
make_dockerfile "$d"
make_workflow "$d"
expect_pass "consistent tree: everything covered" "$d"

# ---------------------------------------------------------------------------
# Case: no workflow file at all — the #2064 starting state.
# ---------------------------------------------------------------------------
d="$TMPROOT/no-workflow"
mkdir -p "$d"
make_dockerfile "$d"
expect_fail "missing pr-image-build.yml is fatal" "$d" "no .github/workflows/pr-image-build.yml"

# ---------------------------------------------------------------------------
# Case: a new top-level COPY the paths: list was never told about.
# ---------------------------------------------------------------------------
d="$TMPROOT/missing-copy-dir"
mkdir -p "$d"
make_dockerfile "$d" "widgets/"
make_workflow "$d"
expect_fail "a COPY'd directory missing from paths: is reported" "$d" "widgets/"

# ---------------------------------------------------------------------------
# Case: an existing entry silently dropped from paths:.
# ---------------------------------------------------------------------------
d="$TMPROOT/dropped-internal"
mkdir -p "$d"
make_dockerfile "$d"
make_workflow "$d" "internal/**"
expect_fail "internal/** dropped from paths: is reported" "$d" "internal/"

d="$TMPROOT/dropped-prisma"
mkdir -p "$d"
make_dockerfile "$d"
make_workflow "$d" "prisma/**"
expect_fail "prisma/** dropped (not a literal COPY source) is still reported" "$d" "prisma/**"

# ---------------------------------------------------------------------------
# Case: the workflow forgets to trigger on itself.
# ---------------------------------------------------------------------------
d="$TMPROOT/no-self-trigger"
mkdir -p "$d"
make_dockerfile "$d"
make_workflow "$d" ".github/workflows/pr-image-build.yml"
expect_fail "missing self-trigger path is reported" "$d" "is not in its own paths: list"

# ---------------------------------------------------------------------------
# Case: pushes from a PR.
# ---------------------------------------------------------------------------
d="$TMPROOT/pushes"
mkdir -p "$d"
make_dockerfile "$d"
make_workflow "$d" "" 1
expect_fail "'push: true' is reported" "$d" "push: true"

# ---------------------------------------------------------------------------
# Case: no workflow_dispatch trigger.
# ---------------------------------------------------------------------------
d="$TMPROOT/no-dispatch"
mkdir -p "$d"
make_dockerfile "$d"
make_workflow "$d" "" 0 0
expect_fail "missing workflow_dispatch: is reported" "$d" "workflow_dispatch"

# ---------------------------------------------------------------------------
# Case: no pull_request trigger (and so no paths: list at all).
# ---------------------------------------------------------------------------
d="$TMPROOT/no-pr-trigger"
mkdir -p "$d"
make_dockerfile "$d"
make_workflow "$d" "" 0 1 0
expect_fail "missing paths: list entirely is fatal" "$d" "no 'paths:' list"

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all pr-image-build-paths.sh guard tests passed."
else
  echo "$FAILURES test(s) FAILED."
fi
exit $(( FAILURES > 0 ? 1 : 0 ))
