#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# uc: pack-docs-drift
# title: Pack: docs drift — deterministic scan, agent fact-check, verify
# needs: github
# minutes: 3
#
# One of the three demo packs `crewship seed` installs (docs/guides/
# demo-packs.mdx). The contract is `crewship seed verify --pack docs-drift`:
# scripts byte-identical to the seed, the probe against an independent read
# of the source, the agent's COUNTS line against the probe, the notification
# in the inbox, the Page panels written by that run.
#
# Default is the cheap half (files + probe, --skip-report). WITH_REPORT=1
# also runs the agent report routine and reconciles it — minutes and tokens.
# Needs SEED_GITHUB_TOKEN at seed time. With a local checkout (this repo),
# verify also checks every cited path exists at the scanned commit.
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

PACK="docs-drift"
CREW="quality"
PROBE=""
REPORT="docs-drift-audit"
PAGE="docs-drift"
FILES="docs_drift.py docs_audit.sh"
WITH_REPORT="${WITH_REPORT:-0}"

preflight
# The pack's own requirement gate — the same one `seed verify` applies, so a
# missing token is SKIP here and SKIP there, never a green.
if [[ "$PACK" != "site-replica" ]]; then
  reason="$(demo_need github)" || demo_skip_all "$reason"
fi
if (( WITH_REPORT == 1 )); then
  reason="$(demo_need model)" || demo_skip_all "$reason (WITH_REPORT=1 runs the agent report)"
fi

demo_step "The scripts the seed delivered to crew $CREW"
demo_show "crew files list $CREW --path shared/scripts" cs crew files list "$CREW" --path shared/scripts
for f in $FILES; do
  assert_contains "$f is on the crew volume" "$DEMO_OUT" "$f"
done

if [[ -n "$PROBE" ]]; then
  demo_step "The token-zero probe: $PROBE"
  demo_say "Agentless. This is the wake gate — on a green night the report routine never runs."
  run_routine "$PROBE"
  assert_eq "probe completed" "COMPLETED" "$RUN_STATUS"
fi

demo_step "crewship seed verify --pack $PACK"
verify_args=(--pack "$PACK")
(( WITH_REPORT == 1 )) || verify_args+=(--skip-report)
[[ "$PACK" == "docs-drift" && -d "$HERE/../../.git" ]] && verify_args+=(--repo-dir "$HERE/../..")
demo_show "seed verify ${verify_args[*]}" cs seed verify "${verify_args[@]}" --timeout "$RUN_WAIT"
if printf '%s' "$DEMO_OUT" | grep -qE '^[a-z-]+ +env +SKIP'; then
  demo_skip_all "$(printf '%s' "$DEMO_OUT" | sed -nE 's/^[a-z-]+ +env +SKIP +(.*)$/\1/p' | head -1)"
fi
assert_contains "no failed check" "$DEMO_OUT" " 0 failed"

demo_step "What the audience sees: the Page and the inbox"
demo_show "page get $PAGE" cs page get "$PAGE"
demo_ui "Page" "/pages/$PAGE"
if (( WITH_REPORT == 1 )); then
  demo_show "inbox" cs inbox list --kind message --state all --limit 5
  demo_ui "Inbox" "/inbox"
else
  demo_say "WITH_REPORT=1 $0 runs $REPORT too: the agent's report, its COUNTS line reconciled with the probe, the notification in the inbox."
fi

finish
