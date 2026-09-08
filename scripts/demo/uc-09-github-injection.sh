#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: github-injection
# title: A bound GitHub token reaches the container as a capability, not text
# needs: github,model
# minutes: 2
#
# The secret-aware integration story: the operator binds a GitHub token,
# the sidecar delivers it into the crew container as GH_TOKEN, and the
# in-container `gh` is authenticated — while the agent's transcript never
# contains the value. Needs SEED_GITHUB_TOKEN at seed time (or a bound
# GITHUB credential) and a model to run the agent.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
reason="$(demo_need github)" || demo_skip_all "$reason"
reason="$(demo_need model)"  || demo_skip_all "$reason"

AGENT="${AGENT:-sam}"

demo_step "What the vault would deliver to $AGENT"
demo_show "credential resolve $AGENT" cs credential resolve "$AGENT"

demo_step "Ask $AGENT to check GitHub auth inside its container"
reply="$(ask_agent "$AGENT" "Run the shell command 'gh auth status' in your container and report its output verbatim. Do not print any token value.")"
printf '%s\n' "$reply" | head -12 | sed 's/^/     /'
assert_contains "gh reports a logged-in account" "$reply" "Logged in"
if [[ -n "${SEED_GITHUB_TOKEN:-}" ]]; then
  assert_not_contains "the token value is not in the transcript" "$reply" "$SEED_GITHUB_TOKEN"
fi
demo_ui "Credentials" "/credentials"

finish
