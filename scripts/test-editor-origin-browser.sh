#!/usr/bin/env bash
# Chromium contract for the sandboxed editor origin. Missing prerequisites fail.
set -euo pipefail
cd "$(dirname "$0")/.."
out="${EDITOR_TEST_EVIDENCE_DIR:-/tmp/crewship-editor-origin}"
mkdir -p "$out"
go test -c -o "$out/editororigin.test" ./internal/editororigin
export CREWSHIP_TEST_EDITOR_BINARY="$out/editororigin.test"
timeout 90s node e2e/editor-origin-sandbox.mjs | tee "$out/browser.log"
HOST_COEP=1 timeout 90s node e2e/editor-origin-sandbox.mjs | tee "$out/browser-coep.log"
HOST_SANDBOX=1 timeout 90s node e2e/editor-origin-sandbox.mjs | tee "$out/browser-host-sandbox.log"
