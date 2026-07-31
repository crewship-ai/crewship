#!/usr/bin/env bash
#
# Stage the Next.js static export into web/out/ for `//go:embed all:out`
# (web/embed.go), and verify that what got staged is a real export.
#
# Single source of truth for a step that used to be an inline
# `rm -rf web/out && cp -r out web/out` copy-pasted into the Makefile,
# dev.sh, and four workflows. Two things it does that the inline copy
# does not:
#
#   sync    Preserves the TRACKED placeholder web/out/.placeholder.html.
#           `rm -rf web/out` deletes it, which leaves the working tree
#           showing a deleted tracked file after every build — and a
#           stray `git add -A` would then re-break `go build` for every
#           fresh clone and worktree (#1567). Then runs `verify`.
#
#   verify  Fails loudly when web/out/ holds the placeholder instead of a
#           real build. THIS IS THE RELEASE GATE. Making `go build` work
#           everywhere is only safe if a release that skipped `pnpm build`
#           cannot ship a UI-less binary quietly, so the release and image
#           paths call this before the binary is compiled.
#
# Usage:
#   scripts/embed-web-out.sh sync     # after `pnpm build`
#   scripts/embed-web-out.sh verify   # assert a real export is staged
#
# SRC overrides the export directory (default: <repo>/out).

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${SRC:-$ROOT/out}"
DST="$ROOT/web/out"
PLACEHOLDER=".placeholder.html"

usage() {
  sed -n '3,28p' "${BASH_SOURCE[0]}" | sed 's/^#\{1,2\} \{0,1\}//'
  exit "${1:-2}"
}

verify() {
  local failed=0

  if [ ! -f "$DST/index.html" ]; then
    echo "ERROR: $DST/index.html is missing — no web UI would be embedded." >&2
    failed=1
  fi

  # A real Next.js export always emits the hashed-asset tree. The
  # placeholder never does, and neither does a hand-rolled
  # `echo '<!doctype html>' > web/out/index.html` stub — which is exactly
  # the shape that used to sneak past a "does index.html exist" check.
  if [ ! -d "$DST/_next" ]; then
    echo "ERROR: $DST/_next is missing — web/out/ holds a placeholder or a stub, not a Next.js static export." >&2
    failed=1
  fi

  if [ "$failed" -ne 0 ]; then
    echo "" >&2
    echo "Refusing to build a binary with no embedded web UI." >&2
    echo "Run:  pnpm build && $0 sync" >&2
    exit 1
  fi

  echo "web/out/ holds a real Next.js export ($(find "$DST" -type f | wc -l | tr -d ' ') files)."
}

sync_export() {
  if [ ! -f "$SRC/index.html" ]; then
    echo "ERROR: no Next.js static export at $SRC — run 'pnpm build' first." >&2
    exit 1
  fi

  mkdir -p "$DST"
  # Clear the previous export but keep the tracked placeholder. Stale
  # files must go: Next.js chunk names are content-hashed, so a merge
  # copy would leave every superseded chunk behind and the directory
  # would grow without bound.
  find "$DST" -mindepth 1 -maxdepth 1 ! -name "$PLACEHOLDER" -exec rm -rf {} +
  cp -R "$SRC"/. "$DST"/

  verify
}

case "${1:-}" in
  sync) sync_export ;;
  verify) verify ;;
  -h | --help | help) usage 0 ;;
  *)
    echo "ERROR: expected 'sync' or 'verify', got '${1:-}'" >&2
    usage 2
    ;;
esac
