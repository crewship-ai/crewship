#!/usr/bin/env bash
# Real compiler + MCP + browser contract. Missing prerequisites fail, never skip.
set -euo pipefail
cd "$(dirname "$0")/.."
evidence_dir="${PAGES_TEST_EVIDENCE_DIR:-/tmp/crewship-pages-ci}"
mkdir -p "$evidence_dir"
docker version >/dev/null
docker build --iidfile "$evidence_dir/image-id" tools/pages-build
export CREWSHIP_TEST_PAGE_BUILD_IMAGE
CREWSHIP_TEST_PAGE_BUILD_IMAGE="$(cat "$evidence_dir/image-id")"
export PAGES_TEST_BUILD_IMAGE="$CREWSHIP_TEST_PAGE_BUILD_IMAGE"
export CREWSHIP_TEST_PAGE_ARTIFACT_OUT="$evidence_dir/artifact.json"
go test -p 1 -json -count=1 -timeout=10m \
  ./internal/pagebuild ./internal/api ./internal/sidecar ./cmd/crewship ./examples/pages-apps \
  -run '^(TestDockerPreviewBuildIntegration|TestPageBuildDockerRoundTrip|TestPageProjectMCPDockerIntegration|TestSeedPageAppLifecycle|TestOperationsCollectorPayloads)$' \
  | tee "$evidence_dir/go.jsonl"
python3 - "$evidence_dir/go.jsonl" <<'PY'
import json, sys
events = [json.loads(line) for line in open(sys.argv[1])]
required = {
    "TestDockerPreviewBuildIntegration",
    "TestDockerPreviewBuildIntegration/typecheck",
    "TestDockerPreviewBuildIntegration/utf8-chunks",
    "TestDockerPreviewBuildIntegration/lockfile",
    "TestPageBuildDockerRoundTrip",
    "TestPageProjectMCPDockerIntegration",
    "TestSeedPageAppLifecycle/publish",
    "TestOperationsCollectorPayloads",
}
passed = {event.get("Test") for event in events if event["Action"] == "pass"}
skipped = [event.get("Test") for event in events if event["Action"] == "skip"]
if required - passed or skipped:
    raise SystemExit(f"Pages integration coverage missing: {required - passed}; skipped: {skipped}")
PY
go test -c -o "$evidence_dir/runtime.test" ./internal/pagebuild
export CREWSHIP_TEST_RUNTIME_BINARY="$evidence_dir/runtime.test"
timeout 90s node e2e/pages-preview-smoke.mjs "$evidence_dir/artifact.json" \
  | tee "$evidence_dir/browser.log"

# The CLI example exercises action/status/history over the real compiled SDK.
export CREWSHIP_TEST_PAGE_MAIN="$PWD/examples/pages-apps/main.tsx.tmpl"
export CREWSHIP_TEST_PAGE_ACTIONS=1
go test -p 1 -count=1 -timeout=5m ./internal/pagebuild -run '^TestDockerPreviewBuildIntegration$'
timeout 90s node e2e/pages-preview-smoke.mjs "$evidence_dir/artifact.json" \
  | tee "$evidence_dir/sdk-browser.log"
