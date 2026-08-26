#!/usr/bin/env bash
# go-toolchain-pin.sh — one Go toolchain, named the same in every file that
# names it.
#
# Why this exists (#2060, #2064): the root Dockerfile is built by release.yml
# and nightly.yml and by nothing that runs on a pull request. Its `FROM
# golang:<ver>-alpine` is therefore the one Go version no PR check can reach,
# and it is a version Dependabot bumps on its own — the `docker-images` group
# targets that line. On #2060 that bump would have shipped release binaries
# compiled by 1.27.0 while all eleven CI pins, `go.mod`'s `toolchain` directive
# and the Go Vuln Scan gate stayed on 1.26.6: the published artefact would have
# been the only thing built by a toolchain nothing had verified. That was
# caught by hand. This script is what catches the next one.
#
# It is a source-consistency check in the idiom of scripts/lint-migrations and
# scripts/security-yml-test.sh: it parses the files as committed and fails on
# disagreement. It does NOT build anything — a PR-triggered image build is the
# other, larger half of #2064 and is still open.
#
# WHAT IS CHECKED, and why each one is in the set
#
#   go.mod `toolchain go<ver>`   The anchor. This is the line the go command
#                               itself reads, so every other pin is compared
#                               against it rather than against a constant here.
#   Dockerfile `FROM golang:`   The compiler for the shipped binary.
#   Dockerfile `ENV GOTOOLCHAIN` Must be `local`. See the note below.
#   workflow `GO_VERSION:`      Ten workflows set it; every Go job consumes it.
#                               It is in this check on purpose: a toolchain
#                               that CI never compiles with is a toolchain
#                               whose analysers never ran. golangci-lint and
#                               govulncheck are both pinned *to* GO_VERSION
#                               (see the comments at their pins) because an
#                               x/tools older than the standard library it is
#                               asked to analyse dies on syntax it does not
#                               know — so GO_VERSION drifting from the image is
#                               not a cosmetic mismatch, it is the vuln gate
#                               and the linter grading a different compiler
#                               than the one that builds the release.
#   workflow `go-version: "x"`  Literal pins that bypass GO_VERSION. codeql.yml
#                               has one; it sets up Go for the CodeQL analyse
#                               phase, so it is an analyser pin like the rest
#                               and drifts exactly as silently.
#
# WHAT IS NOT CHECKED
#
#   go.mod's `go` directive. It stays at 1.26 deliberately (#2060): the
#   language floor is a separate promise to consumers, and nothing in the tree
#   needs 1.27 semantics. Folding it in here would force the two to move
#   together and quietly raise the floor on every toolchain bump.
#
# WHY `GOTOOLCHAIN=local` AND NOT `GOTOOLCHAIN=go1.27.0`
#
#   `local` means "use the toolchain this image ships and never download
#   another", which makes the `FROM` tag the single authority for what compiles
#   the release binary. The default is `auto`, which honours go.mod's
#   `toolchain` directive by fetching that compiler — so under `auto` a
#   `toolchain` bump silently builds the release with a Go that no line in this
#   repo pins and no vuln scan ever graded.
#
#   A literal `GOTOOLCHAIN=go1.27.0` would be one more copy of the version
#   string to keep in sync, and it inverts the failure in the direction that
#   matters: bump `FROM golang:1.28.0-alpine` and leave the literal behind, and
#   the go command would *download* 1.27.0 and build with it — neutering the
#   base-image bump while every check stayed green. `local` cannot do that.
#
#   The official `golang:<ver>-alpine` images already default GOTOOLCHAIN to
#   `local`, so this line does not change today's behaviour. That is the point
#   of writing it down: an inherited upstream default is not an invariant, it
#   is a fact that happens to hold. Stated in our own Dockerfile it survives a
#   base-image swap, and this script can check it.
#
# WHY A STATIC CHECK AND NOT JUST THE PIN
#
#   Because `local` is silent about exactly the disagreement this script
#   exists to find. Verified against golang:1.27.0-alpine rather than assumed:
#
#     toolchain go1.27.1 + GOTOOLCHAIN=local  -> builds with 1.27.0, exit 0
#     toolchain go1.27.1 + GOTOOLCHAIN=auto   -> downloads 1.27.1
#     go 1.28 directive  + GOTOOLCHAIN=local  -> "go.mod requires go >= 1.28
#                                                 (running go 1.27.0)", exit 1
#
#   Under `local` the `toolchain` directive is ignored outright. Only the `go`
#   directive can fail a build, and that one deliberately stays at 1.26. So no
#   image build — not even the nightly one — will ever report that go.mod and
#   the Dockerfile name different toolchains. Nothing at run time can catch
#   this; it has to be read off the source, which is what follows.
#
# Usage: bash scripts/go-toolchain-pin.sh [root-dir]
#
# The optional root-dir is how scripts/go-toolchain-pin-test.sh points the same
# parser at fixture trees that disagree on purpose. Nothing else should pass it.

set -uo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"

FAILURES=0
fail() { printf '  MISMATCH %s\n' "$1"; FAILURES=$((FAILURES + 1)); }

fatal() {
  printf 'FATAL: %s\n' "$1" >&2
  shift
  for line in "$@"; do printf '       %s\n' "$line" >&2; done
  exit 1
}

# Emit `<file>:<line>\t<version>` for every match of an sed -E capture program.
# Kept in one place so every pin is extracted the same way and a file that does
# not exist is an empty result rather than a shell error.
#
# The line number is prepended by awk before sed sees the line, which is why
# the caller's own capture group lands at \2: a pin reported without a line
# number sends the reader hunting through ci.yml for one of eleven pins.
scan() {
  local prog="$1"; shift
  local f
  for f in "$@"; do
    [ -f "$f" ] || continue
    awk '{ printf "%d\t%s\n", FNR, $0 }' "$f" \
      | sed -nE "s|^([0-9]+)\t${prog}\$|\1\t\2|p" \
      | while IFS=$'\t' read -r ln v; do
          printf '%s:%s\t%s\n' "${f#"$ROOT"/}" "$ln" "$v"
        done
  done
}

# ---------------------------------------------------------------------------
# The anchor: go.mod's toolchain directive
# ---------------------------------------------------------------------------
GOMOD="$ROOT/go.mod"
[ -f "$GOMOD" ] || fatal "no go.mod at $GOMOD"

# `toolchain go1.27.0` -> `1.27.0`
WANT="$(sed -nE 's|^[[:space:]]*toolchain[[:space:]]+go([0-9][0-9.]*)[[:space:]]*$|\1|p' "$GOMOD" | head -1)"
if [ -z "$WANT" ]; then
  fatal "go.mod has no 'toolchain goX.Y.Z' directive." \
    "Every other Go version pin in this repo is compared against that line," \
    "so without it this guard has nothing to check and would pass silently." \
    "If the directive was dropped by 'go mod tidy', put it back explicitly —" \
    "the release binary's compiler should be named in the file, not inferred." \
    "See #2060."
fi

echo "anchor: go.mod toolchain go$WANT"
echo

# ---------------------------------------------------------------------------
# Collect every other pin
# ---------------------------------------------------------------------------
DOCKERFILE="$ROOT/Dockerfile"
[ -f "$DOCKERFILE" ] || fatal "no Dockerfile at $DOCKERFILE"

WORKFLOWS=()
while IFS= read -r f; do WORKFLOWS+=("$f"); done < <(
  find "$ROOT/.github/workflows" "$ROOT/.github/actions" \
    -maxdepth 2 \( -name '*.yml' -o -name '*.yaml' \) 2>/dev/null | sort
)

# `FROM golang:1.27.0-alpine AS backend` -> `1.27.0`. The tag is captured up to
# the first `-` so a two-component tag such as `golang:1.27-alpine` is reported
# as `1.27` and mismatches `1.27.0` — which is correct and deliberate: a
# floating minor tag means the image's compiler is not pinned at all.
FROM_PINS="$(scan 'FROM[[:space:]]+golang:([0-9][0-9.]*)[^[:space:]]*.*' "$DOCKERFILE")"
[ -n "$FROM_PINS" ] || fatal "no 'FROM golang:<version>' line in Dockerfile." \
  "Either the Go build stage is gone or its base image is no longer pinned" \
  "to an explicit version. Both make this guard vacuous."

# `GO_VERSION: "1.27.0"` at workflow env level (any indent, quoted or not).
ENV_PINS=""
LITERAL_PINS=""
if [ "${#WORKFLOWS[@]}" -gt 0 ]; then
  ENV_PINS="$(scan '[[:space:]]*GO_VERSION:[[:space:]]*"?([0-9][0-9.]*)"?[[:space:]]*(#.*)?' "${WORKFLOWS[@]}")"
  # `go-version: "1.27.0"` written out instead of `${{ env.GO_VERSION }}`.
  # The value must start with a digit, so input declarations (`go-version:`
  # with nothing after it) and expressions are not matched.
  LITERAL_PINS="$(scan '[[:space:]]*go-version:[[:space:]]*"?([0-9][0-9.]*)"?[[:space:]]*(#.*)?' "${WORKFLOWS[@]}")"
fi

if [ -z "$ENV_PINS" ] && [ -z "$LITERAL_PINS" ]; then
  fatal "no Go version pin found in any workflow under .github/." \
    "Ten workflows set GO_VERSION and codeql.yml pins go-version directly;" \
    "finding none of them means this scan stopped matching, not that the" \
    "pins are gone. A guard that matches nothing passes everything."
fi

# ---------------------------------------------------------------------------
# Compare
# ---------------------------------------------------------------------------
check_pins() {
  local label="$1" pins="$2" loc version
  [ -n "$pins" ] || return 0
  while IFS=$'\t' read -r loc version; do
    [ -n "$loc" ] || continue
    if [ "$version" = "$WANT" ]; then
      printf '  ok       %-46s %s\n' "$loc" "$version"
    else
      fail "$loc names $version, go.mod names $WANT ($label)"
    fi
  done <<< "$pins"
}

check_pins "Dockerfile Go build stage"      "$FROM_PINS"
check_pins "workflow GO_VERSION"            "$ENV_PINS"
check_pins "workflow literal go-version"    "$LITERAL_PINS"

# ---------------------------------------------------------------------------
# The GOTOOLCHAIN pin itself
# ---------------------------------------------------------------------------
# Checked by value, not against $WANT: the whole argument for `local` is that
# it does not name a version. See the header.
GOTOOLCHAIN_VAL="$(sed -nE 's|^[[:space:]]*ENV[[:space:]]+GOTOOLCHAIN=([^[:space:]]+).*$|\1|p' "$DOCKERFILE" | head -1)"
if [ -z "$GOTOOLCHAIN_VAL" ]; then
  fail "Dockerfile has no 'ENV GOTOOLCHAIN=local' in the Go build stage (see this script's header for why)"
elif [ "$GOTOOLCHAIN_VAL" != "local" ]; then
  fail "Dockerfile sets GOTOOLCHAIN=$GOTOOLCHAIN_VAL, want local — a version literal here lets a base-image bump be silently undone by a download"
else
  printf '  ok       %-46s %s\n' "Dockerfile ENV GOTOOLCHAIN" "local"
fi

echo
if [ "$FAILURES" -eq 0 ]; then
  echo "all Go toolchain pins agree on $WANT"
else
  echo "$FAILURES pin(s) disagree with go.mod's toolchain go$WANT."
  echo
  echo "A Go toolchain bump is never a one-line change. Move them together:"
  echo "  go.mod          toolchain goX.Y.Z"
  echo "  Dockerfile      FROM golang:X.Y.Z-alpine"
  echo "  .github/**      GO_VERSION / go-version"
  echo "and re-check the golangci-lint and govulncheck version pins, which are"
  echo "coupled to the toolchain they analyse (#2060)."
fi
exit $(( FAILURES > 0 ? 1 : 0 ))
