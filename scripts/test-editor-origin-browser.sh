#!/usr/bin/env bash
# Chromium contract for the sandboxed editor: dedicated origin (three host
# variants) and the zero-config same-host mode. Missing prerequisites fail.
#
#   crewship-heavy scripts/test-editor-origin-browser.sh
#
# crewship-heavy runs with a clean environment and presets a shared
# PLAYWRIGHT_BROWSERS_PATH that may hold another Chromium revision. The
# contract picks the cache (that one first, then ~/.cache/ms-playwright) that
# holds the exact revision of the installed Playwright, and fails clearly if
# none does. A
# checkout without node_modules (a worktree) passes PLAYWRIGHT_MODULE, e.g.
#   crewship-heavy env PLAYWRIGHT_MODULE=/path/to/repo/node_modules/playwright/index.mjs \
#     scripts/test-editor-origin-browser.sh
set -euo pipefail
cd "$(dirname "$0")/.."
out="${EDITOR_TEST_EVIDENCE_DIR:-/tmp/crewship-editor-origin}"
mkdir -p "$out"
go test -c -o "$out/editororigin.test" ./internal/editororigin
export CREWSHIP_TEST_EDITOR_BINARY="$out/editororigin.test"
timeout 120s node e2e/editor-origin-sandbox.mjs | tee "$out/browser.log"
HOST_COEP=1 timeout 120s node e2e/editor-origin-sandbox.mjs | tee "$out/browser-coep.log"
HOST_SANDBOX=1 timeout 120s node e2e/editor-origin-sandbox.mjs | tee "$out/browser-host-sandbox.log"
EDITOR_MODE=same-host timeout 180s node e2e/editor-origin-sandbox.mjs | tee "$out/browser-same-host.log"
