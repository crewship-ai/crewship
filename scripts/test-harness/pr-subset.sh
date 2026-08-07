#!/usr/bin/env bash
# The deterministic, no-provider subset run on every pull request.
# Keep this list explicit: adding a suite here is a reviewable gate change.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tests=(
  test-keeper.sh
  test-keeper-config.sh
  test-keeper-aux.sh
  test-inbox.sh
  test-orphan-token-reap.sh
)

for suite in "${tests[@]}"; do
  printf '\n############ PR harness: %s ############\n' "$suite"
  timeout "${HARNESS_SUITE_TIMEOUT:-600}" bash "$HERE/$suite"
done
