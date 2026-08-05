#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Executes the commands printed in docs/guides/first-projects.mdx.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$HERE/lib.sh"
preflight

DOC="$(cd "$HERE/../.." && pwd)/docs/guides/first-projects.mdx"
ROUTINE_FILE="docs-lab-routine.json"
trap 'rm -f "$ROUTINE_FILE"' EXIT

commands=(
  'crewship crew create --name "Docs Lab" --slug docs-lab'
  'crewship crew list'
  'crewship agent create --name "Writer" --slug writer --crew docs-lab --role AGENT --memory'
  'crewship ask --agent writer "Create /crew/shared/hello.txt containing exactly: hello from Crewship"'
  'crewship crew files list docs-lab'
  'crewship credential create --name demo-token --type API_KEY --value local-demo-value'
  'crewship credential assign demo-token writer --env-var-name DEMO_TOKEN'
  'crewship ask --agent writer "Remember in crew-shared memory that the demo project codename is BLUE-BOAT. Do not print the credential."'
  'crewship ask --agent writer "What is the crew-shared demo project codename? Reply with only the codename."'
  'crewship routine init --script --output docs-lab-routine.json'
  'crewship routine validate docs-lab-routine.json'
  'crewship routine save --name "docs-lab-routine" --description "Write the shared hello file" --definition docs-lab-routine.json --author-crew docs-lab --author-agent writer'
  'crewship routine schedules create --slug docs-lab-routine --cron "0 9 * * 1-5" --timezone UTC --yes'
  'crewship routine list'
  'crewship journal --crew docs-lab --lines 20'
  'crewship routine runs docs-lab-routine'
)

section "1. Documentation and harness command parity"
for command in "${commands[@]}"; do
  if grep -Fq "$command" "$DOC"; then
    pass "documented command is wired into the harness: $command"
  else
    fail "documented command changed without updating this harness: $command"
  fi
done

section "2. Execute the documented rungs"
cs crew create --name "Docs Lab" --slug docs-lab >/dev/null
cs crew list | grep -q 'docs-lab' && pass "crew created and listed" || fail "created crew is listed"
cs agent create --name "Writer" --slug writer --crew docs-lab --role AGENT --memory >/dev/null
cs ask --agent writer "Create /crew/shared/hello.txt containing exactly: hello from Crewship" >/dev/null
files="$(cs crew files list docs-lab)"
assert_contains "agent wrote into crew shared storage" "$files" "hello.txt"
cs credential create --name demo-token --type API_KEY --value local-demo-value >/dev/null
cs credential assign demo-token writer --env-var-name DEMO_TOKEN >/dev/null
cs ask --agent writer "Remember in crew-shared memory that the demo project codename is BLUE-BOAT. Do not print the credential." >/dev/null
reply="$(cs ask --agent writer "What is the crew-shared demo project codename? Reply with only the codename.")"
assert_contains "agent recalls the crew-shared fact" "$reply" "BLUE-BOAT"
cs routine init --script --output "$ROUTINE_FILE" >/dev/null
cs routine validate "$ROUTINE_FILE" >/dev/null && pass "generated routine validates" || fail "generated routine validates"
cs routine save --name "docs-lab-routine" --description "Write the shared hello file" --definition "$ROUTINE_FILE" --author-crew docs-lab --author-agent writer >/dev/null
schedule="$(cs routine schedules create --slug docs-lab-routine --cron "0 9 * * 1-5" --timezone UTC --yes)"
assert_contains "routine is scheduled on weekdays at 09:00 UTC" "$schedule" "0 9 * * 1-5"
cs routine list >/dev/null && pass "routine list is readable" || fail "routine list is readable"
cs journal --crew docs-lab --lines 20 >/dev/null && pass "journal returns the crew events" || fail "journal returns the crew events"
cs routine runs docs-lab-routine >/dev/null && pass "routine run history is readable" || fail "routine run history is readable"

finish
