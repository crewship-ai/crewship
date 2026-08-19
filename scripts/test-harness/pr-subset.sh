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
  # The composition depth cap. It belongs on the per-PR gate rather than the
  # nightly for the reason its own header gives: the property it asserts —
  # that a cycle of rules and routines terminates — has already been broken
  # twice by changes that no unit test could see, once because the unit test
  # modelled a world where every emitter stamps the causing run id and
  # production only did so from one. A safety property proven nightly is a
  # safety property that ships broken for a day.
  #
  # Deterministic and provider-free like the rest of the list: the routines it
  # drives are `crewship` and `transform` steps, so it calls no model.
  test-automation-loop.sh
  # Pages (#1937). On the per-PR gate rather than the nightly because the
  # properties it proves are the ones only a running server can show — that
  # provenance is ATTACHED rather than echoed, that the 64 KiB cap answers 422
  # and not 500, and that a panel goes stale on its own — and each of them is a
  # silent failure: a page that lies about its freshness looks exactly like one
  # that does not. Deterministic and provider-free: no model is called, the
  # only wait is the 5s SLA it is measuring, and it deletes its page on exit.
  test-pages.sh
)

# Every suite runs, and EVERY failure counts. A bare `for … done` exits with the
# status of the last command it ran, so the first draft of this runner gated on
# whichever suite happened to be last in the list and swallowed the rest — a
# green job that had a red suite in it, the exact failure mode the per-PR gate
# was added to remove. No `set -e` either: one red suite must not cost us the
# results of the ones after it.
failed=()
for suite in "${tests[@]}"; do
  printf '\n############ PR harness: %s ############\n' "$suite"
  timeout "${HARNESS_SUITE_TIMEOUT:-600}" bash "$HERE/$suite"
  rc=$?
  if (( rc == 124 )); then
    failed+=("$suite (timed out after ${HARNESS_SUITE_TIMEOUT:-600}s)")
  elif (( rc != 0 )); then
    failed+=("$suite (exit $rc)")
  fi
done

printf '\n############ PR harness summary ############\n'
if (( ${#failed[@]} > 0 )); then
  printf 'FAILED %d of %d suite(s):\n' "${#failed[@]}" "${#tests[@]}"
  printf '  - %s\n' "${failed[@]}"
  exit 1
fi
printf 'all %d suite(s) passed\n' "${#tests[@]}"
