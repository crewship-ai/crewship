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
# in_json_rc — same, plus the bot's inline review comments. Kept separate so
# the dozens of existing cases stay readable; they pass none.
# in_json_rc <now> <createdAt> <headSha> <statusState> <statusDesc> <commentsJSON> <reviewsJSON> <reviewCommentsJSON>
in_json_rc() {
  in_json "$1" "$2" "$3" "$4" "$5" "$6" "$7" \
    | jq -c --argjson rc "$8" '. + {reviewComments:$rc}'
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

# Three phrasings each match a rate-limit notice, and the real fixture above
# happens to carry all three — so a test built only on it cannot tell whether
# two of the three were deleted. Pin each shape on its own, or the redundancy
# quietly decays to one and the next wording change reads as "absent".
throttled_by() { # <body>
  state_of "$(in_json "$NOW" "$OPENED" "$SHA" success "" \
    "$(cmt 2026-07-30T21:20:00Z "$1")" "$NONE")"
}
expect_eq "the rate-limited marker alone is throttled" "throttled" \
  "$(throttled_by '<!-- This is an auto-generated comment: rate limited by coderabbit.ai -->')"
expect_eq "the «Review limit reached» heading alone is throttled" "throttled" \
  "$(throttled_by '> ## Review limit reached')"
expect_eq "the «reached your PR review limit» prose alone is throttled" "throttled" \
  "$(throttled_by "you've reached your PR review limit, so we couldn't start this review.")"
expect_contains "reviewed carries the actionable count" \
  "$(classify "$THROTTLED_IN" >/dev/null; classify "$REVIEWED_IN")" "2 actionable"

# The trap: the throttle notice opens with the same `summarize by
# coderabbit.ai` marker as a real walkthrough. Matching that first would read
# every throttled PR as "review started".
expect_not_contains "the summarize marker inside a throttle notice is not a walkthrough" \
  "$(state_of "$THROTTLED_IN")" "pending"

echo "== an approval with no content is not a review (#1729) =="

# Seen on PR #1722, 2026-08-03: CodeRabbit posted the rate-limit notice and
# then submitted an APPROVED review whose body was zero-length, with no inline
# comments. "Newest event wins" made that the winning event and the script
# reported `reviewed / review approved` on a PR nothing had read — the exact
# substitution this tool exists to refuse, arriving through a different field.
EMPTY_APPROVAL_AFTER_THROTTLE="$(in_json "$NOW" "$OPENED" "$SHA" success "Review rate limited" \
  "$(cmt 2026-07-30T21:15:08Z "$THROTTLE_BODY")" \
  "$(rev 2026-07-30T21:35:00Z APPROVED "" "$SHA")")"
expect_eq "an empty approval does not outrank the throttle it followed" \
  "throttled" "$(state_of "$EMPTY_APPROVAL_AFTER_THROTTLE")"
expect_contains "the empty approval is called out, not silently ignored" \
  "$(notes_of "$EMPTY_APPROVAL_AFTER_THROTTLE")" "approved with no content"

# On its own, with nothing else posted, an empty approval leaves the PR in the
# same position as one CodeRabbit never touched.
EMPTY_APPROVAL_ALONE="$(in_json "$NOW" "$OPENED" "$SHA" success "" \
  "$NONE" "$(rev 2026-07-30T21:35:00Z APPROVED "" "$SHA")")"
expect_eq "an empty approval alone is not a review" \
  "absent" "$(state_of "$EMPTY_APPROVAL_ALONE")"

# The inverse must keep working: a review that says nothing in its BODY but
# left inline comments really did read the diff.
EMPTY_BODY_WITH_INLINE="$(in_json_rc "$NOW" "$OPENED" "$SHA" success "" \
  "$NONE" "$(rev 2026-07-30T21:35:00Z APPROVED "" "$SHA")" \
  '[{"createdAt":"2026-07-30T21:35:04Z"}]')"
expect_eq "an empty body with inline comments IS a review" \
  "reviewed" "$(state_of "$EMPTY_BODY_WITH_INLINE")"

# Inline comments from an EARLIER review must not launder a later empty
# approval into looking like a fresh read.
STALE_INLINE="$(in_json_rc "$NOW" "$OPENED" "$SHA" success "Review rate limited" \
  "$(cmt 2026-07-30T21:30:00Z "$THROTTLE_BODY")" \
  "$(rev 2026-07-30T21:35:00Z APPROVED "" "$SHA")" \
  '[{"createdAt":"2026-07-30T21:00:00Z"}]')"
expect_eq "inline comments predating the approval do not count for it" \
  "throttled" "$(state_of "$STALE_INLINE")"

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

echo "== the API failing is its own state, never a benign one =="

# Everything above drives the pure classifier, which is the part that can be
# subtly wrong. But the part that can be *catastrophically* wrong lives in
# fetch_pr/examine: turning "the API did not answer" into "nothing was posted"
# is the exact substitution this whole script exists to catch, and no
# pure-stdin test can reach that branch. So drive the real script with a `gh`
# that fails. Still network-free — the stub is the only `gh` on PATH.
STUB_DIR="$(mktemp -d)"
trap 'rm -rf "$STUB_DIR"' EXIT
stub_gh() { printf '%s\n' '#!/bin/sh' "$@" > "$STUB_DIR/gh"; chmod +x "$STUB_DIR/gh"; }
with_stub() { PATH="$STUB_DIR:$PATH" "$RS" "$@" 2>&1; }

stub_gh 'exit 1'
out="$(with_stub 1234)"; rc=$?
expect_contains "a failed API call reports 'unknown'" "$out" "? unknown"
expect_contains "…and refuses to call it 'no review'" "$out" 'NOT "no review"'
expect_eq "…and exits 3, never 0" "3" "$rc"
expect_not_contains "…and never claims the PR was reviewed" "$out" "✓ reviewed"

# `gh` exiting 0 with a body jq cannot read is the same hazard wearing a
# success code, and it is the shape an auth or proxy interstitial arrives in.
stub_gh 'echo not-json'
expect_contains "a malformed API body is 'unknown', not 'absent'" \
  "$(with_stub 1234)" "? unknown"

stub_gh 'echo "{}"'
expect_contains "valid JSON with no head SHA is 'unknown'" \
  "$(with_stub 1234)" "? unknown"

stub_gh 'exit 1'
expect_contains "--json reports the unknown state too" \
  "$(with_stub --json 1234)" '"state":"unknown"'

# The realistic failure is partial, not total: the PR call succeeds and one of
# the paginated calls behind it trips GitHub's secondary rate limit. Losing the
# comments is losing exactly the throttle notice, so a half-read PR must stay
# `unknown` rather than settle into whatever the surviving half implies.
stub_gh \
  'case "$*" in' \
  '  *"/issues/"*"/comments"*) exit 1 ;;' \
  '  *"/pulls/"*"/reviews"*)   echo "[]" ;;' \
  '  *"/pulls/"*) echo "{\"number\":1234,\"title\":\"t\",\"head\":{\"sha\":\"abc1234\",\"ref\":\"b\"},\"created_at\":\"2020-01-01T00:00:00Z\"}" ;;' \
  '  *) echo "{}" ;;' \
  'esac'
out="$(with_stub 1234)"; rc=$?
expect_contains "a half-fetched PR is 'unknown', not 'absent'" "$out" "? unknown"
expect_eq "…and still exits 3" "3" "$rc"

# Same again with the other half missing, so neither call can be quietly
# defaulted to an empty list without a test noticing.
stub_gh \
  'case "$*" in' \
  '  *"/pulls/"*"/reviews"*)   exit 1 ;;' \
  '  *"/issues/"*"/comments"*) echo "[]" ;;' \
  '  *"/pulls/"*) echo "{\"number\":1234,\"title\":\"t\",\"head\":{\"sha\":\"abc1234\",\"ref\":\"b\"},\"created_at\":\"2020-01-01T00:00:00Z\"}" ;;' \
  '  *) echo "{}" ;;' \
  'esac'
expect_contains "a PR whose reviews call failed is 'unknown' too" \
  "$(with_stub 1234)" "? unknown"

# With no PR arguments the listing is the first call. A failed listing must
# not degrade into examining zero PRs and printing the all-clear.
stub_gh 'exit 1'
out="$(with_stub)"; rc=$?
expect_eq "a failed PR listing exits 2" "2" "$rc"
expect_not_contains "…and does not print an all-clear" "$out" "carry a real review"

# Missing tooling is a usage error, never a clean run over nothing. Invoked
# through "$BASH" rather than the shebang, because with gh gone the PATH is
# empty and `/usr/bin/env bash` would fail to resolve bash itself.
out="$(PATH="$STUB_DIR/nonexistent" "$BASH" "$RS" 1234 2>&1)"; rc=$?
expect_eq "gh missing exits 2" "2" "$rc"
expect_contains "…and says gh is missing" "$out" "gh CLI not found"

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
