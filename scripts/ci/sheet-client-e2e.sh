#!/usr/bin/env bash
# The client's path through a Sheet (e2e/sheet-client) on a Linux host with Docker,
# against a throwaway Crewship server in the same container: nothing reaches a
# shared environment, and only synthetic workbooks are used.
#
#   scripts/ci/sheet-client-e2e.sh <crewship binary with embedded UI> <package dir> <output dir> [playwright args…]
#
# <package dir> holds catalog.txt and crewship-craft-{gridcraft,wordcraft}.craftpkg
# of scripts/build-craft-components.py for the pins of this commit. The container
# is the Playwright image the lockfile pins; inside it scripts/ci/sheet-client-e2e-inner.sh
# starts the server (own data dir, HOME and port, random secrets), seeds the
# offline demo, installs and enables the packages, and runs
# playwright.sheet-client.config.ts under xvfb. Exit status is the test result;
# report.json, the HTML report, screenshots, timings and server.log land in <output dir>.
# Run from a clean checkout of the commit under test (git archive is what runs).
set -euo pipefail

binary="$(realpath "$1")"; packages="$(realpath "$2")"; mkdir -p "$3"; output="$(realpath "$3")"; shift 3
root="$(cd "$(dirname "$0")/../.." && pwd)"
[ -x "$binary" ] || { echo "no server binary at $binary" >&2; exit 2; }
for app in wordcraft gridcraft; do [ -f "$packages/crewship-craft-$app.craftpkg" ] || { echo "missing $app package in $packages" >&2; exit 2; }; done
want="$(tr -d '\n' < "$root/tools/craft-components/catalog.txt")"; have="$(tr -d '\n' < "$packages/catalog.txt")"
[ "$want" = "$have" ] || { echo "the packages are for other pins: $have (this commit pins $want)" >&2; exit 2; }
if command -v docker >/dev/null && docker info >/dev/null 2>&1; then engine=(docker); elif sudo -n docker info >/dev/null 2>&1; then engine=(sudo -n docker); else echo "no usable docker" >&2; exit 2; fi

version="$(grep -oE '@playwright/test@[0-9]+\.[0-9]+\.[0-9]+' "$root/pnpm-lock.yaml" | head -1 | cut -d@ -f3)"
image="mcr.microsoft.com/playwright:v$version-noble"
pnpm_version="$(sed -n -E 's/^ *"packageManager": *"pnpm@([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' "$root/package.json" | head -1)"

git -C "$root" diff --quiet HEAD -- || { echo "the checkout has local changes; test a clean commit" >&2; exit 2; }
commit="$(git -C "$root" rev-parse HEAD 2>/dev/null || echo unknown)"
work="$(mktemp -d "${TMPDIR:-/tmp}/sheet-client.XXXXXX")"
trap 'rm -rf "$work"' EXIT
git -C "$root" archive "$commit" | tar -x -C "$work"

echo "sheet client e2e: commit $commit, $image, server $("$binary" version 2>/dev/null | head -1)"
"${engine[@]}" image inspect "$image" >/dev/null 2>&1 || "${engine[@]}" pull -q "$image" >/dev/null
set +e
"${engine[@]}" run --rm --ipc=host --init \
  -e CI=1 -e PNPM_VERSION="$pnpm_version" -e SHEET_E2E_ROWS="${SHEET_E2E_ROWS:-}" -e SHEET_E2E_FLAGS="${SHEET_E2E_FLAGS:-}" -e SHEET_E2E_AGENTS="${SHEET_E2E_AGENTS:-}" -e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
  -v "$work:/src" -v "$binary:/opt/crewship:ro" -v "$packages:/pkg:ro" -v "$output:/out" \
  -v sheet-client-pnpm:/pnpm \
  -w /src "$image" bash scripts/ci/sheet-client-e2e-inner.sh "$@"
status=$?
set -e
echo "sheet client e2e: exit $status, results in $output"
exit "$status"
