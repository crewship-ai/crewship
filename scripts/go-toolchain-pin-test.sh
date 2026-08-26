#!/usr/bin/env bash
# go-toolchain-pin-test.sh — negative tests for scripts/go-toolchain-pin.sh.
#
# The guard's whole value is that it goes RED on a disagreement no PR check
# would otherwise notice. A consistency check that only ever runs against a
# consistent tree proves nothing about whether it can fail, and this repo has
# shipped that mistake before: scripts/security-yml-test.sh exists because two
# YAML blocks regressed while their tests were green, and the shape both times
# was a check that matched nothing and therefore passed everything.
#
# So each case here builds a small fixture tree, breaks exactly one pin, and
# asserts both the exit status and that the message names the file at fault.
# The guard takes a root directory argument for precisely this reason.
#
# Usage: bash scripts/go-toolchain-pin-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="$SCRIPT_DIR/go-toolchain-pin.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

TMPROOT="$(mktemp -d)"
trap 'rm -rf "$TMPROOT"' EXIT

# Build a fixture tree. Every pin defaults to agreeing on 1.27.0; each argument
# overrides one file so a case can state its mutation in one line.
#
#   $1 dir  $2 go.mod toolchain  $3 FROM tag  $4 GOTOOLCHAIN  $5 GO_VERSION
#   $6 codeql literal
#
# A value of "-" omits the line entirely, which is a distinct failure from
# setting it wrong and is tested separately.
make_tree() {
  local dir="$1" toolchain="$2" from="$3" gotc="$4" gover="$5" literal="$6"
  mkdir -p "$dir/.github/workflows"

  {
    echo "module github.com/crewship-ai/crewship"
    echo
    echo "go 1.26"
    [ "$toolchain" = "-" ] || echo "toolchain go$toolchain"
  } > "$dir/go.mod"

  {
    echo "FROM node:22-alpine AS frontend"
    echo "FROM golang:$from-alpine AS backend"
    [ "$gotc" = "-" ] || echo "ENV GOTOOLCHAIN=$gotc"
    echo "WORKDIR /app"
  } > "$dir/Dockerfile"

  {
    echo "name: CI"
    echo "env:"
    [ "$gover" = "-" ] || echo "  GO_VERSION: \"$gover\""
    echo "jobs:"
    echo "  build:"
    echo "    steps:"
    echo "      - uses: ./.github/actions/setup-go-embed"
    echo "        with:"
    echo "          go-version: \${{ env.GO_VERSION }}"
  } > "$dir/.github/workflows/ci.yml"

  {
    echo "name: CodeQL"
    echo "jobs:"
    echo "  analyze:"
    echo "    steps:"
    echo "      - uses: ./.github/actions/setup-go-embed"
    echo "        with:"
    [ "$literal" = "-" ] || echo "          go-version: \"$literal\""
  } > "$dir/.github/workflows/codeql.yml"
}

# Run the guard against a tree; assert exit status and (when red) that the
# output names the file the mutation was made in. want_match is an ERE, so the
# assertions can pin `<file>:<line> names <version>,` without hard-coding a
# fixture line number — and the trailing comma is load-bearing: without it
# `names 1.27` would also match `names 1.27.0` and the floating-tag case would
# pass against a guard that never noticed the tag was floating.
#
# Asserting the named file is not decoration. Every one of these mutations
# makes SOME pin disagree with some other, so a guard that reported the wrong
# file — or a blanket "pins disagree" with no location — would pass a
# status-only assertion while sending the next maintainer to the wrong line.
expect() {
  local label="$1" want_rc="$2" want_match="$3" dir="$4" out rc
  out="$(bash "$GUARD" "$dir" 2>&1)"
  rc=$?
  if [ "$rc" -ne "$want_rc" ]; then
    fail "$label (want exit $want_rc, got $rc)"
    printf '%s\n' "$out" | sed 's/^/         | /'
    return
  fi
  if [ -n "$want_match" ] && ! printf '%s\n' "$out" | grep -qE "$want_match"; then
    fail "$label (exit $rc as expected, but output never mentions '$want_match')"
    printf '%s\n' "$out" | sed 's/^/         | /'
    return
  fi
  pass "$label"
}

# Each case gets its own directory so a fixture can never leak into the next.
tree() { local d="$TMPROOT/$1"; shift; make_tree "$d" "$@"; printf '%s' "$d"; }

echo "consistent tree:"

expect "every pin agrees -> green" 0 "all Go toolchain pins agree on 1.27.0" \
  "$(tree agree 1.27.0 1.27.0 local 1.27.0 1.27.0)"

echo
echo "one pin broken at a time (each must go red and name the file):"

# The #2060 shape verbatim: Dependabot bumps the Dockerfile alone, and nothing
# that runs on the PR builds that file.
expect "Dockerfile FROM bumped alone -> red" 1 "Dockerfile:[0-9]+ names 1\.28\.0," \
  "$(tree from-ahead 1.27.0 1.28.0 local 1.27.0 1.27.0)"

# The mirror: go.mod moves and the image is left behind. Under GOTOOLCHAIN=
# local this is a hard build failure at release time; here it is a PR failure.
expect "go.mod toolchain bumped alone -> red" 1 "Dockerfile:[0-9]+ names 1\.27\.0," \
  "$(tree gomod-ahead 1.28.0 1.27.0 local 1.27.0 1.27.0)"

# GO_VERSION is in the check because the analysers are pinned to it: a CI
# toolchain that lags the image means govulncheck and golangci-lint graded a
# compiler that is not the one shipping.
expect "workflow GO_VERSION lags -> red" 1 "ci\.yml:[0-9]+ names 1\.26\.6," \
  "$(tree gover-lags 1.27.0 1.27.0 local 1.26.6 1.27.0)"

# codeql.yml pins go-version as a literal rather than through GO_VERSION, so
# it drifts independently of all ten env pins.
expect "codeql literal go-version lags -> red" 1 "codeql\.yml:[0-9]+ names 1\.26\.6," \
  "$(tree literal-lags 1.27.0 1.27.0 local 1.27.0 1.26.6)"

# A floating minor tag is not a pin. `golang:1.27-alpine` silently follows
# every patch release, which is the state Audit M15 flagged and Dependabot
# exists to end.
expect "floating minor FROM tag -> red" 1 "Dockerfile:[0-9]+ names 1\.27," \
  "$(tree floating 1.27.0 1.27 local 1.27.0 1.27.0)"

echo
echo "the GOTOOLCHAIN pin itself:"

expect "GOTOOLCHAIN removed -> red" 1 "no 'ENV GOTOOLCHAIN=local'" \
  "$(tree gotc-missing 1.27.0 1.27.0 - 1.27.0 1.27.0)"

# The form this pin deliberately is not. A version literal here re-introduces
# the drift the guard exists to catch, and worse, lets a FROM bump be undone
# by a download instead of failing.
expect "GOTOOLCHAIN as a version literal -> red" 1 "want local" \
  "$(tree gotc-literal 1.27.0 1.27.0 go1.27.0 1.27.0 1.27.0)"

expect "GOTOOLCHAIN=auto -> red" 1 "want local" \
  "$(tree gotc-auto 1.27.0 1.27.0 auto 1.27.0 1.27.0)"

echo
echo "the guard must not pass vacuously:"

# These are the failures that matter most, because they are the ones that look
# like success. A guard whose anchor or whose scan has stopped matching reports
# no mismatches — which is indistinguishable from agreement unless it is fatal.
expect "go.mod toolchain directive gone -> fatal, not green" 1 \
  "no 'toolchain goX.Y.Z' directive" \
  "$(tree no-toolchain - 1.27.0 local 1.27.0 1.27.0)"

expect "no Go pin in any workflow -> fatal, not green" 1 \
  "no Go version pin found in any workflow" \
  "$(tree no-workflow-pins 1.27.0 1.27.0 local - -)"

NO_DOCKERFILE="$TMPROOT/no-dockerfile"
make_tree "$NO_DOCKERFILE" 1.27.0 1.27.0 local 1.27.0 1.27.0
rm -f "$NO_DOCKERFILE/Dockerfile"
expect "Dockerfile gone -> fatal, not green" 1 "no Dockerfile at" "$NO_DOCKERFILE"

NO_FROM="$TMPROOT/no-from"
make_tree "$NO_FROM" 1.27.0 1.27.0 local 1.27.0 1.27.0
printf 'FROM node:22-alpine AS frontend\nENV GOTOOLCHAIN=local\n' > "$NO_FROM/Dockerfile"
expect "Go build stage gone from Dockerfile -> fatal, not green" 1 \
  "no 'FROM golang:<version>' line" "$NO_FROM"

echo
echo "against the repo as committed:"

# One case reads the real tree, so this suite keeps saying something about
# production and not only about its own fixtures. If this goes red, the repo's
# pins genuinely disagree — fix the pins, not this line.
expect "the committed tree is consistent" 0 "all Go toolchain pins agree" "$REPO_ROOT"

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all go-toolchain-pin checks passed"
else
  echo "$FAILURES check(s) FAILED"
fi
exit $(( FAILURES > 0 ? 1 : 0 ))
