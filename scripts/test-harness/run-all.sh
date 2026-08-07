#!/usr/bin/env bash
# shellcheck shell=bash
# Run the whole Crewship CLI integration harness and print one combined summary.
#
#   ./run-all.sh                      # memory, notifications, credentials, determinism
#   WITH_GITHUB=1 ./run-all.sh        # also the GitHub real-world scenario
#   WITH_SECRETLESS=1 ./run-all.sh    # also the secretless-GitHub proof (T-H1…T-H9)
#   WITH_KEEPER_SECURITY=1 ./run-all.sh  # also the keeper adversarial suite
#   KEEPER_ESCALATION=1 ./run-all.sh     # also the real-agent credential tier flow
#   WITH_NOTIFICATIONS_SHOUTRRR=1 ./run-all.sh  # also the #1412 preference-matrix suite
#   ./run-all.sh --quick              # skip the slower determinism sweep
#
# Each test-*.sh is self-contained and exits non-zero if any assertion failed.
# We run them all (continuing past failures) and aggregate the exit codes.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

QUICK=0
[[ "${1:-}" == "--quick" ]] && QUICK=1

tests=(test-memory.sh test-delegation.sh test-crew-links.sh test-notifications.sh test-inbox.sh test-orchestration.sh test-credentials.sh test-keeper.sh test-keeper-config.sh test-keeper-aux.sh)
(( QUICK == 0 )) && tests+=(test-determinism.sh)
[[ "${WITH_GITHUB:-0}" == "1" ]] && tests+=(test-realworld-github.sh)
# Secretless-GitHub proof (PRD-CREDENTIALS-V2 §4.3, T-H1…T-H9) — opt-in for the
# same reasons as the red-team suite: it mints + deletes workspace credentials,
# saves + soft-deletes a probe routine in two crews, and (only when
# SEED_GITHUB_TEST_REPO is set) pushes a uniquely named branch to the throwaway
# test repo and deletes it again. Every section self-SKIPs without its inputs,
# so it is safe to enable on a slot with no GitHub account wired — you just get
# the crew-fanout, zero-disk and cross-crew-isolation legs. Dev slots only.
[[ "${WITH_SECRETLESS:-0}" == "1" ]] && tests+=(test-secretless-github.sh)
# Keeper adversarial suite — opt-in (creates HARNESS_ credentials + probes the
# internal keeper HTTP surface). Ingress-fence is read-only; toctou/audit clean
# up after themselves.
[[ "${WITH_KEEPER_SECURITY:-0}" == "1" ]] && tests+=(test-keeper-ingress-fence.sh test-keeper-toctou.sh test-keeper-audit-integrity.sh test-keeper-load.sh)
# The tier flow drives a REAL agent through /keeper/request — the side of the
# gatekeeper no unit or HTTP test covers, and the side that was completely broken
# until the agent preamble learned the endpoint exists. Opt-in because it costs
# agent tokens, needs a working judge on the target, and takes minutes.
[[ "${KEEPER_ESCALATION:-0}" == "1" ]] && tests+=(test-keeper-escalation.sh)
# Local-Ollama scenario is macOS-only and self-skips when Ollama isn't reachable,
# so it's safe to always include; opt out with WITH_OLLAMA=0.
[[ "${WITH_OLLAMA:-1}" == "1" ]] && tests+=(test-ollama-local.sh)
# Orphaned-container reap (#1385) is a read-only dry-run sweep that self-skips
# when the server's provider isn't docker (503), so it's safe to always include;
# opt out with WITH_ORPHAN_REAP=0.
[[ "${WITH_ORPHAN_REAP:-1}" == "1" ]] && tests+=(test-orphan-token-reap.sh)
# #1412 category preference-matrix suite — opt-in: it creates two PERSONAL
# notification channels + writes real cells into the CURRENT session user's
# preference matrix (cleaned up on exit, but a crash mid-run could leave
# fixture channels behind on a shared dev slot). Also binds a local port for
# the fake webhook receiver — see the script header for the network-
# reachability note when SERVER is a remote devN.
[[ "${WITH_NOTIFICATIONS_SHOUTRRR:-0}" == "1" ]] && tests+=(test-notifications-shoutrrr.sh)
# Real-forge proof for the link-first Git integration (internal/gitlink). Gated
# by its own secrets rather than by a flag: with neither GITLINK_GITHUB_TOKEN
# nor GITLINK_GITLAB_TOKEN set it SKIPs before contacting the server or any
# forge, so including it by default costs a run nothing. With a token set it
# creates + deletes one issue and up to two credentials per provider, and only
# ever GETs public objects upstream. Opt out with WITH_GITLINK=0.
[[ "${WITH_GITLINK:-1}" == "1" ]] && tests+=(test-gitlink-realworld.sh)
# Adversarial suites — opt-in, and deliberately NOT in the default set.
#
# test-attack-surface.sh (Tier A) is read-only and creates nothing, but it is
# raw-curl probing of the auth fence: it belongs to a security sweep, not to
# every harness run.
#
# test-redteam-insider.sh actually attacks the instance from inside a crew
# container. It is opt-in for two reasons: it saves + runs a routine (soft-
# deleted on exit) on a shared dev slot, and it carries xfail assertions for
# open or recently-fixed issues (#1368, #1473) that must stay visible rather
# than become background noise in an unrelated run. Dev slots only — never
# point it at prod.
[[ "${WITH_ATTACK_SURFACE:-0}" == "1" ]] && tests+=(test-attack-surface.sh)
[[ "${WITH_REDTEAM_INSIDER:-0}" == "1" ]] && tests+=(test-redteam-insider.sh)

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
