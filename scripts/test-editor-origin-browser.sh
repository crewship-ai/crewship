#!/usr/bin/env bash
# Chromium contract for the sandboxed editor: dedicated origin (three host
# variants) and the zero-config same-host mode (direct, behind a proxy on
# another host:port, and with a configured origin the browser does not use).
# Missing prerequisites fail.
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
# Zero-config behind a reverse proxy on another host:port: no public origin is
# configured anywhere, the editor must still load.
EDITOR_MODE=same-host EDITOR_PROXY=1 timeout 180s node e2e/editor-origin-sandbox.mjs | tee "$out/browser-same-host-proxy.log"
# A configured public origin that the browser does not use: fail closed.
EDITOR_MODE=same-host EDITOR_PROXY=1 EDITOR_PINNED=1 timeout 120s node e2e/editor-origin-sandbox.mjs | tee "$out/browser-same-host-pinned.log"
