#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: eval-tiers
# title: The same recipe on the fast and the smart tier gives the same answer
# needs: model
# minutes: 6
#
# `crewship eval scenarios` runs the workspace's eval-* routines across
# tiers. The claim: a recipe authored on the smart tier runs on the fast
# tier with identical, gradable output. Two cheap scenarios, one run each.
# Seeded by `crewship seed --with-evals`; skipped when they are absent.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

preflight
reason="$(demo_need model)" || demo_skip_all "$reason"

SCENARIOS="${SCENARIOS:-eval-extract-emails,eval-classify-sentiment}"
present="$(cs routine list --format json 2>/dev/null | grep -oE '"slug": *"eval-[a-z0-9-]+"' | wc -l | tr -d ' ')"
(( present == 0 )) && demo_skip_all "no eval-* routines in this workspace — seed with --with-evals"

demo_step "Run $SCENARIOS on fast and smart"
demo_say "Each cell is one routine run; the table is what a CI job would diff."
demo_show "eval scenarios" cs eval scenarios --scenarios "$SCENARIOS" --tiers fast,smart --runs 1 -f markdown
assert_contains "the sweep reports a table" "$DEMO_OUT" "|"
if printf '%s' "$DEMO_OUT" | grep -qiE '\bfail'; then
  _fail "every cell passed" "a cell reports a non-pass — read the table above"
else
  _pass "every cell passed"
fi
demo_ui "Routine runs" "/routines"

finish
