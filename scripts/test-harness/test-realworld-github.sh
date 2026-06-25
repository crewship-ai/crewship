#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Real-world use case — an agent uses the in-container `gh` CLI against a PUBLIC
# repo, the way a real customer would after wiring a (dummy) GitHub account.
#
# Prereq: the workspace was seeded with SEED_GITHUB_TOKEN, so a CLI_TOKEN named
# GH_TOKEN is mounted into the engineering/quality crew containers and `gh`
# authenticates with zero extra setup. If `gh auth` isn't configured, the whole
# file SKIPs — it's an integration probe, not a core invariant.
#
# Scenario (read-only, safe against any public repo):
#   - sam confirms `gh auth status` works inside its container
#   - sam fetches the latest open issues of a public repo and summarises them
#   - we assert the reply references the repo / issue signal (not a refusal)
#
# Pick any public repo via REPO env (default: a small, stable one).

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

REPO="${REPO:-cli/cli}"   # GitHub's own gh repo — public, always has open issues

preflight

section "0. Is GitHub auth wired in the crew container?"
auth_reply="$(ask_agent sam "Run exactly 'gh auth status' in your container and \
paste the raw output. If gh is not installed or not authenticated, reply with \
exactly: GH_NOT_READY.")"
if printf '%s' "$auth_reply" | grep -qiE 'GH_NOT_READY|not (installed|logged|authenticated)|command not found'; then
  skip "GitHub real-world scenario" "gh not authenticated in container (seed without SEED_GITHUB_TOKEN?)"
  finish
fi
assert_contains "gh is authenticated inside sam's container" "$auth_reply" "github.com"

section "1. Agent reads a public repo's open issues via gh"
issues_reply="$(ask_agent sam "Use the in-container gh CLI to list the 3 most \
recently updated OPEN issues in the public repo ${REPO} (gh issue list -R ${REPO} \
-L 3 --json number,title). Then give me a one-line summary per issue as \
'#<number>: <title>'. Use ONLY real data from gh, do not invent issues.")"
assert_nonempty "agent returned a non-empty issue summary" "$issues_reply"
assert_contains "summary references real issue numbers (#)" "$issues_reply" "#"
assert_not_contains "agent did not refuse / hit an auth wall" "$issues_reply" "GH_NOT_READY"

section "2. Agent self-creates a GitHub credential? (probe)"
# Real-world: an agent that needs broader GitHub scope might try to provision a
# token itself. Probe whether that path exists (documents the gap if not).
self_reply="$(ask_agent sam "If you can create/store a GitHub credential \
yourself for future runs, do so and confirm with the credential name. If you \
cannot, reply exactly: NO_SELF_SERVICE.")"
if printf '%s' "$self_reply" | grep -qi 'NO_SELF_SERVICE'; then
  skip "agent self-provisions a GitHub credential" "not wired — agent reports NO_SELF_SERVICE"
else
  info "agent claims it self-provisioned — verify in: crewship credential list --format json"
  _pass "agent attempted GitHub credential self-provision (verify attribution manually)"
fi

finish
