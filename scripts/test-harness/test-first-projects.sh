#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Executes the commands printed in docs/guides/first-projects.mdx.
#
# The point is not that these commands exist — it is that the page cannot drift
# away from what actually runs. Two directions are checked:
#
#   harness → doc   a command wired here but no longer printed on the page
#   doc → harness   a command printed on the page that nothing here executes
#
# The second direction is the one that lets a page grow uncovered examples, so
# it is not optional (#1771 review).
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

if [[ ! -f "$DOC" ]]; then
  _fail "first-projects.mdx exists" "no file at $DOC"
  finish
fi

# harness → doc
for command in "${commands[@]}"; do
  if grep -Fq "$command" "$DOC"; then
    _pass "documented command is wired into the harness: $command"
  else
    _fail "documented command changed without updating this harness: $command"
  fi
done

# doc → harness. Every `crewship …` line inside a fenced block on the page must
# appear in the array above; otherwise the page has grown an example that
# nothing executes and the suite would still be green.
documented=()
while IFS= read -r line; do
  documented+=("$line")
done < <(grep -E '^crewship ' "$DOC" || true)

if (( ${#documented[@]} == 0 )); then
  _fail "the guide prints at least one crewship command" "found none in $DOC — did the fence format change?"
fi

for line in ${documented[@]+"${documented[@]}"}; do
  found=0
  for command in "${commands[@]}"; do
    [[ "$line" == "$command" ]] && { found=1; break; }
  done
  if (( found )); then
    _pass "documented command has execution coverage: $line"
  else
    _fail "the guide prints a command this harness never runs: $line" \
          "add it to the commands array and execute it in section 2"
  fi
done

section "2. Execute the documented rungs"
# Every documented command is asserted on its exit status. Discarding it would
# let a rung fail while a later assertion passed off state an earlier run left
# behind — which is exactly what a first-run reader would not experience.
assert_ok "crew create"    cs crew create --name "Docs Lab" --slug docs-lab
crews="$(cs crew list)"
assert_contains "crew created and listed" "$crews" "docs-lab"

assert_ok "agent create"   cs agent create --name "Writer" --slug writer --crew docs-lab --role AGENT --memory
assert_ok "agent writes the shared file" \
  cs ask --agent writer "Create /crew/shared/hello.txt containing exactly: hello from Crewship"
files="$(cs crew files list docs-lab)"
assert_contains "agent wrote into crew shared storage" "$files" "hello.txt"

assert_ok "credential create" cs credential create --name demo-token --type API_KEY --value local-demo-value
assert_ok "credential assign" cs credential assign demo-token writer --env-var-name DEMO_TOKEN

assert_ok "agent stores a crew-shared fact" \
  cs ask --agent writer "Remember in crew-shared memory that the demo project codename is BLUE-BOAT. Do not print the credential."
reply="$(cs ask --agent writer "What is the crew-shared demo project codename? Reply with only the codename.")"
assert_contains "agent recalls the crew-shared fact" "$reply" "BLUE-BOAT"

assert_ok "routine init"     cs routine init --script --output "$ROUTINE_FILE"
assert_ok "generated routine validates" cs routine validate "$ROUTINE_FILE"
assert_ok "routine save" \
  cs routine save --name "docs-lab-routine" --description "Write the shared hello file" \
     --definition "$ROUTINE_FILE" --author-crew docs-lab --author-agent writer
schedule="$(cs routine schedules create --slug docs-lab-routine --cron "0 9 * * 1-5" --timezone UTC --yes)"
assert_contains "routine is scheduled on weekdays at 09:00 UTC" "$schedule" "0 9 * * 1-5"

assert_ok "routine list is readable"        cs routine list
assert_ok "journal returns the crew events" cs journal --crew docs-lab --lines 20
assert_ok "routine run history is readable" cs routine runs docs-lab-routine

finish
