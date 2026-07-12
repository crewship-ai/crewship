#!/usr/bin/env bash
# shellcheck shell=bash
# Run the whole Crewship CLI integration harness and print one combined summary.
#
#   ./run-all.sh                      # memory, notifications, credentials, determinism
#   WITH_GITHUB=1 ./run-all.sh        # also the GitHub real-world scenario
#   WITH_KEEPER_SECURITY=1 ./run-all.sh  # also the keeper adversarial suite
#   ./run-all.sh --quick              # skip the slower determinism sweep
#
# Each test-*.sh is self-contained and exits non-zero if any assertion failed.
# We run them all (continuing past failures) and aggregate the exit codes.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

QUICK=0
[[ "${1:-}" == "--quick" ]] && QUICK=1

tests=(test-memory.sh test-delegation.sh test-notifications.sh test-orchestration.sh test-credentials.sh)
(( QUICK == 0 )) && tests+=(test-determinism.sh)
[[ "${WITH_GITHUB:-0}" == "1" ]] && tests+=(test-realworld-github.sh)
# Keeper adversarial suite — opt-in (creates HARNESS_ credentials + probes the
# internal keeper HTTP surface). Ingress-fence is read-only; toctou/audit clean
# up after themselves.
[[ "${WITH_KEEPER_SECURITY:-0}" == "1" ]] && tests+=(test-keeper-ingress-fence.sh test-keeper-toctou.sh test-keeper-audit-integrity.sh)
# Local-Ollama scenario is macOS-only and self-skips when Ollama isn't reachable,
# so it's safe to always include; opt out with WITH_OLLAMA=0.
[[ "${WITH_OLLAMA:-1}" == "1" ]] && tests+=(test-ollama-local.sh)

declare -a results=()
overall=0
for t in "${tests[@]}"; do
  printf '\n\033[1m############ %s ############\033[0m\n' "$t"
  if bash "$HERE/$t"; then
    results+=("✓ $t")
  else
    results+=("✗ $t")
    overall=1
  fi
done

printf '\n\033[1m################ HARNESS SUMMARY ################\033[0m\n'
printf '  %s\n' "${results[@]}"
printf '################################################\n'
exit "$overall"
