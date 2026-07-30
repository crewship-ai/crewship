#!/usr/bin/env bash
# review-status-test.sh — unit tests for scripts/review-status.sh.
#
# Why this file exists (#1580): the whole value of that script is one
# distinction — a CodeRabbit rate-limit notice is NOT a review, even though
# GitHub renders both as a green `pass`. A classifier that gets that backwards
# is worse than no classifier: it would confirm, in a tool built to catch the
# lie, exactly the lie it was built to catch. So the classifier is pinned here
# on fixtures taken from the real 2026-07-30 comment bodies.
#
# Pure: no network, no gh, no GitHub. `now` is an input to the classifier, so
# the pending/absent boundary is testable without sleeping.
#
# Usage: bash scripts/review-status-test.sh
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RS="$SCRIPT_DIR/review-status.sh"

FAILURES=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1"; printf '       %s\n' "${2:-}"; FAILURES=$((FAILURES + 1)); }

expect_eq() { # <name> <want> <got>
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "want «$2» got «$3»"; fi
}
expect_contains() { # <name> <haystack> <needle>
  case "$2" in *"$3"*) pass "$1";; *) fail "$1" "expected «$3» in: $2";; esac
}
expect_not_contains() { # <name> <haystack> <needle>
  case "$2" in *"$3"*) fail "$1" "did NOT expect «$3» in: $2";; *) pass "$1";; esac
}

if [ ! -x "$RS" ]; then
  fail "review-status.sh is executable" "$RS missing or not +x"
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  printf 'jq not installed — skipping review-status.sh tests\n'
  exit 0
fi

# ── fixtures ──────────────────────────────────────────────────────────────
# Verbatim shapes from the real bodies. The rate-limit notice opens with BOTH
# auto-generated markers, and the second one is the same `summarize by
# coderabbit.ai` marker a genuine walkthrough opens with — which is precisely
# why "CodeRabbit commented, so it reviewed" is the wrong inference.
THROTTLE_BODY='<!-- This is an auto-generated comment: summarize by coderabbit.ai -->
<!-- This is an auto-generated comment: rate limited by coderabbit.ai -->

> [!WARNING]
> ## Review limit reached
>
> `@Srbino`, you'"'"'ve reached your PR review limit, so we couldn'"'"'t start this review.
>
> **Next review available in:** **37 minutes**'

WALKTHROUGH_BODY='<!-- This is an auto-generated comment: summarize by coderabbit.ai -->
## Walkthrough

The change moves the four unchecked apiFetch calls onto the checked helper.'

REVIEW_BODY='**Actionable comments posted: 2**

<details>
<summary>internal/cli/client.go (1)</summary>
`24-44`: _Maintainability_'

FAILED_BODY='> [!CAUTION]
> ## Review failed
>
> The pull request is closed.'

# Verbatim from PR #1587, which is what this script shipped in: answering
# `@coderabbitai review` while still rate-limited gets a ✅ and no review.
ACK_BODY='<!-- This is an auto-generated reply by CodeRabbit -->
<!-- CodeRabbit review command invocation: 27fed4a8 -->
<details>
<summary>✅ Action performed</summary>

Review finished.

> Note: CodeRabbit is an incremental review system and does not re-review already reviewed commits.

</details>'

# in() — build the classifier input. Keeps the tests readable; every field the
# classifier looks at is named here.
# in <now> <createdAt> <headSha> <statusState> <statusDesc> <commentsJSON> <reviewsJSON>
in_json() {
  jq -nc --arg now "$1" --arg created "$2" --arg sha "$3" \
         --arg sstate "$4" --arg sdesc "$5" \
         --argjson comments "$6" --argjson reviews "$7" \
    '{now:$now, createdAt:$created, headSha:$sha, windowMin:5,
      statusState:$sstate, statusDesc:$sdesc,
      comments:$comments, reviews:$reviews}'
}
cmt() { jq -nc --arg at "$1" --arg b "$2" '[{createdAt:$at, body:$b}]'; }
rev() { # <at> <state> <body> <commitId>
  jq -nc --arg at "$1" --arg s "$2" --arg b "$3" --arg c "$4" \
    '[{submittedAt:$at, state:$s, body:$b, commitId:$c}]'
}
merge() { printf '%s\n' "$@" | jq -sc 'add'; }
NONE='[]'

classify() { printf '%s' "$1" | "$RS" --classify; }
state_of() { classify "$1" | cut -f1; }
notes_of() { classify "$1" | cut -f3; }

NOW=2026-07-30T22:00:00Z
OPENED=2026-07-30T21:15:00Z   # 45 minutes before NOW — well past the window
SHA=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa

echo "== the distinction the whole script exists for =="

THROTTLED_IN="$(in_json "$NOW" "$OPENED" "$SHA" success "Review rate limited" \
  "$(cmt 2026-07-30T21:15:08Z "$THROTTLE_BODY")" "$NONE")"
expect_eq "a rate-limit notice is NOT a review" "throttled" "$(state_of "$THROTTLED_IN")"

REVIEWED_IN="$(in_json "$NOW" "$OPENED" "$SHA" success "Review completed" \
  "$(cmt 2026-07-30T21:16:00Z "$WALKTHROUGH_BODY")" \
  "$(rev 2026-07-30T21:20:00Z CHANGES_REQUESTED "$REVIEW_BODY" "$SHA")")"
expect_eq "a submitted review is a review" "reviewed" "$(state_of "$REVIEWED_IN")"

# Both of the above show `CodeRabbit  pass` in `gh pr checks`. If the states
# ever coincide the tool has stopped being able to tell them apart at all.
expect_not_contains "throttled and reviewed do not collapse" \
  "$(state_of "$THROTTLED_IN")" "$(state_of "$REVIEWED_IN")"

expect_contains "throttled carries the wait the notice quoted" \
  "$(classify "$THROTTLED_IN")" "37m"
expect_contains "reviewed carries the actionable count" \
  "$(classify "$THROTTLED_IN" >/dev/null; classify "$REVIEWED_IN")" "2 actionable"

# The trap: the throttle notice opens with the same `summarize by
# coderabbit.ai` marker as a real walkthrough. Matching that first would read
# every throttled PR as "review started".
expect_not_contains "the summarize marker inside a throttle notice is not a walkthrough" \
  "$(state_of "$THROTTLED_IN")" "pending"

echo "== newest event wins =="

expect_eq "throttle after a review means the new head is unreviewed" "throttled" \
  "$(state_of "$(in_json "$NOW" "$OPENED" "$SHA" success "Review rate limited" \
      "$(cmt 2026-07-30T21:40:00Z "$THROTTLE_BODY")" \
      "$(rev 2026-07-30T21:20:00Z COMMENTED "$REVIEW_BODY" bbbbbbb)")")"

expect_contains "…and says so" \
  "$(notes_of "$(in_json "$NOW" "$OPENED" "$SHA" success "Review rate limited" \
      "$(cmt 2026-07-30T21:40:00Z "$THROTTLE_BODY")" \
      "$(rev 2026-07-30T21:20:00Z COMMENTED "$REVIEW_BODY" bbbbbbb)")")" \
  "an earlier review exists"

expect_eq "a review after a throttle clears it" "reviewed" \
  "$(state_of "$(in_json "$NOW" "$OPENED" "$SHA" success "Review completed" \
      "$(cmt 2026-07-30T21:15:00Z "$THROTTLE_BODY")" \
      "$(rev 2026-07-30T21:50:00Z APPROVED "$REVIEW_BODY" "$SHA")")")"

echo "== reviewed, but not of what is merging =="

expect_contains "a review of an older commit is flagged against head" \
  "$(notes_of "$(in_json "$NOW" "$OPENED" "$SHA" success "Review completed" "$NONE" \
      "$(rev 2026-07-30T21:20:00Z APPROVED "$REVIEW_BODY" cccccccccccccccccccccccccccccccccccccccc)")")" \
  "the newest push is unreviewed"

expect_eq "a review of the head commit is not flagged" "" \
  "$(notes_of "$(in_json "$NOW" "$OPENED" "$SHA" success "Review completed" "$NONE" \
      "$(rev 2026-07-30T21:20:00Z APPROVED "$REVIEW_BODY" "$SHA")")" | tr -d '-')"

echo "== silence: pending vs absent =="

expect_eq "silence inside the window is pending, not absent" "pending" \
  "$(state_of "$(in_json 2026-07-30T21:17:00Z 2026-07-30T21:15:00Z "$SHA" pending "" "$NONE" "$NONE")")"

expect_eq "silence after the window is absent" "absent" \
  "$(state_of "$(in_json "$NOW" "$OPENED" "$SHA" success "" "$NONE" "$NONE")")"

expect_contains "a walkthrough with no review that follows is called out" \
  "$(notes_of "$(in_json "$NOW" "$OPENED" "$SHA" success "" \
      "$(cmt 2026-07-30T21:16:00Z "$WALKTHROUGH_BODY")" "$NONE")")" \
  "a walkthrough was posted"

# A young PR is expected to be silent; shouting "green means unreviewed" at it
# would train the reader to ignore the warning that matters.
expect_eq "pending does not get the green-means-unreviewed warning" "" \
  "$(notes_of "$(in_json 2026-07-30T21:17:00Z 2026-07-30T21:15:00Z "$SHA" success "" "$NONE" "$NONE")" | tr -d '-')"

echo "== failure =="

expect_eq "a CodeRabbit failure notice is its own state" "failed" \
  "$(state_of "$(in_json "$NOW" "$OPENED" "$SHA" failure "Review failed" \
      "$(cmt 2026-07-30T21:20:00Z "$FAILED_BODY")" "$NONE")")"

echo '== "✅ Review finished." is not a review =='

# Found by running this script on its own PR. A re-trigger fired while still
# rate-limited comes back with a green tick and the words "Review finished",
# and nothing else — no walkthrough, no submitted review, status back to
# `Review rate limited`. Anyone reading the thread would call that reviewed.
ACK_IN="$(in_json "$NOW" "$OPENED" "$SHA" success "Review rate limited" \
  "$(merge "$(cmt 2026-07-30T21:48:46Z "$THROTTLE_BODY")" \
           "$(cmt 2026-07-30T21:52:53Z "$ACK_BODY")")" "$NONE")"
expect_eq "an Action-performed reply does not make a PR reviewed" "throttled" \
  "$(state_of "$ACK_IN")"
expect_contains "…and the misleading tick is named" "$(notes_of "$ACK_IN")" \
  "acknowledges the command, it is not a review"

# The reverse must stay quiet: an ack that follows a genuine review is just
# CodeRabbit confirming it re-ran, and warning there would be noise.
expect_eq "an ack before a real review raises nothing" "" \
  "$(notes_of "$(in_json "$NOW" "$OPENED" "$SHA" success "Review completed" \
      "$(cmt 2026-07-30T21:52:53Z "$ACK_BODY")" \
      "$(rev 2026-07-30T21:55:00Z APPROVED "$REVIEW_BODY" "$SHA")")" | tr -d '-')"

echo "== the check status is cross-examined, never trusted =="

expect_contains "green status on an unreviewed PR is named as the hazard" \
  "$(notes_of "$THROTTLED_IN")" "merging on it would be merging unreviewed"

expect_contains "a status claiming completion with no review is contradicted" \
  "$(notes_of "$(in_json "$NOW" "$OPENED" "$SHA" success "Review completed" \
      "$(cmt 2026-07-30T21:15:08Z "$THROTTLE_BODY")" "$NONE")")" \
  "but no review was posted"

expect_contains "a stale rate-limited status against a real review is contradicted" \
  "$(notes_of "$(in_json "$NOW" "$OPENED" "$SHA" success "Review rate limited" "$NONE" \
      "$(rev 2026-07-30T21:20:00Z APPROVED "$REVIEW_BODY" "$SHA")")")" \
  "disagrees with the posted review"

echo "== output shape =="

# `read -r a b c` with a tab IFS collapses runs of tabs, because tab is IFS
# whitespace. An empty column would shift every later column left and land the
# wait-minutes value in the notes field — which is exactly what it did before
# the placeholders went in.
row="$(classify "$(in_json "$NOW" "$OPENED" "$SHA" success "Review completed" "$NONE" \
        "$(rev 2026-07-30T21:20:00Z APPROVED "$REVIEW_BODY" "$SHA")")")"
expect_eq "the row has five tab-separated columns" "5" \
  "$(printf '%s' "$row" | awk -F'\t' '{print NF}')"
expect_not_contains "no column is empty (would collapse under read)" "$row" "$(printf '\t\t')"

echo "== waitMinutes parsing =="

wait_of() { printf '%s' "$1" | "$RS" --parse-wait; }
expect_eq "minutes are read off the notice" "37" \
  "$(wait_of '**Next review available in:** **37 minutes**')"
expect_eq "an hours figure is converted to minutes" "120" \
  "$(wait_of 'Next review available in: 2 hours')"
expect_eq "a singular minute parses" "1" \
  "$(wait_of 'next review available in 1 minute')"
expect_eq "a body with no such line yields nothing" "" \
  "$(wait_of 'Actionable comments posted: 0')"

echo "== skipped-but-green checks =="

# Same failure shape, different producer: `gh pr checks` prints `skipping` for
# a job that never ran, and the aggregate stays green — the check-run twin of
# `go test` printing ok for a package whose every test called t.Skip.
CHECKS='{"check_runs":[
  {"name":"Go","status":"completed","conclusion":"success","output":{"annotations_count":0}},
  {"name":"License Check","status":"completed","conclusion":"skipped","output":{"annotations_count":0}},
  {"name":"OSV Scanner","status":"completed","conclusion":"skipped","output":{"annotations_count":0}},
  {"name":"CodeQL","status":"completed","conclusion":"neutral","output":{"annotations_count":0}},
  {"name":"Analyze (go)","status":"completed","conclusion":"success","output":{"annotations_count":2}},
  {"name":"Go Race","status":"completed","conclusion":"failure","output":{"annotations_count":1}}
]}'
sum="$(printf '%s' "$CHECKS" | "$RS" --classify-checks)"

expect_eq "skipped jobs are counted, not read as passes" "2" \
  "$(printf '%s' "$sum" | jq -r '.skipped | length')"
expect_eq "a neutral conclusion is reported distinctly from skipped" "CodeQL" \
  "$(printf '%s' "$sum" | jq -r '.neutral | join(",")')"
expect_contains "a green run carrying annotations is surfaced" \
  "$(printf '%s' "$sum" | jq -r '.annotated | join(",")')" "Analyze (go) (2 annotation(s))"
expect_not_contains "a genuinely green run with no annotations is not surfaced" \
  "$(printf '%s' "$sum" | jq -r '.annotated | join(",")')" "\"Go\" ("
expect_eq "failing checks are listed" "Go Race" \
  "$(printf '%s' "$sum" | jq -r '.failed | join(",")')"

# --paginate emits one object per page; the summary must span all of them
# rather than reporting only the last.
sum2="$(printf '%s\n%s\n' \
  '{"check_runs":[{"name":"A","status":"completed","conclusion":"skipped"}]}' \
  '{"check_runs":[{"name":"B","status":"completed","conclusion":"skipped"}]}' \
  | "$RS" --classify-checks)"
expect_eq "paginated pages are merged, not overwritten" "2" \
  "$(printf '%s' "$sum2" | jq -r '.skipped | length')"

echo "== usage =="

out="$("$RS" --nope 2>&1)"; rc=$?
expect_eq "an unknown flag exits 2" "2" "$rc"
expect_contains "…and says which" "$out" "unknown flag"

out="$("$RS" not-a-number 2>&1)"; rc=$?
expect_eq "a non-numeric PR argument exits 2" "2" "$rc"

out="$("$RS" --help 2>&1)"; rc=$?
expect_eq "--help exits 0" "0" "$rc"
expect_contains "--help prints usage" "$out" "Usage:"
# The help text is the only place the five states are enumerated for a reader
# who has not opened the file.
for s in reviewed throttled failed pending absent unknown; do
  expect_contains "--help documents the state '$s'" "$out" "$s"
done

echo
if [ "$FAILURES" -gt 0 ]; then
  printf '%d failure(s)\n' "$FAILURES"
  exit 1
fi
printf 'all review-status.sh checks passed\n'
