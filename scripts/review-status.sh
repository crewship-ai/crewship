#!/usr/bin/env bash
#
# review-status.sh — did CodeRabbit actually review this PR, or did it only
# say `pass`?
#
# Why (#1580): CodeRabbit reports through a commit *status*, and when it hits
# the per-developer review limit that status is:
#
#     $ gh pr checks 1568
#     CodeRabbit    pass    0    Review rate limited
#
# `pass`. Identical in colour, wording and position to a PR that really was
# reviewed, where the description reads `Review completed`. On 2026-07-30
# eleven of twelve open PRs carried that green and not one of them had been
# read. The rule "wait for CodeRabbit before merging" was being satisfied by a
# check that never ran.
#
# So this script never asks the check what happened. It reads the comment and
# review bodies CodeRabbit actually posted, and reports one of:
#
#   reviewed   a review was submitted (with its actionable-comment count).
#              An APPROVED review with an empty body counts only when a
#              walkthrough names the same commit as reviewed — a clean CHILL
#              review and the #1729 non-event are the same empty approval,
#              and that range is the only thing that separates them.
#   throttled  a rate-limit notice was posted instead — NOT reviewed
#   failed     CodeRabbit reported a failure (e.g. the PR was closed mid-run)
#   pending    nothing yet, still inside the review window
#   absent     the window elapsed and nothing arrived
#   unknown    the API call failed — its own state, never folded into
#              `absent`, because "we could not look" is not "nothing there"
#
#   scripts/review-status.sh                        # every open PR
#   scripts/review-status.sh 1568 1571              # named PRs
#   scripts/review-status.sh --checks               # + skipped-but-green CI checks
#   scripts/review-status.sh --retrigger --dry-run  # the re-review queue
#   scripts/review-status.sh --json                 # one JSON object per PR
#
# Exit codes: 0 everything examined was reviewed  ·  2 usage or tooling error
#             3 at least one PR is not reviewed (throttled / absent / failed /
#               unknown) — i.e. do not merge that one on its green check
#
# The rule this verifies lives in CONTRIBUTING.md → "Wait for CodeRabbit — and
# check that it actually reviewed".
#
# Requires: gh CLI logged in, jq.

set -uo pipefail

REPO="${REPO:-crewship-ai/crewship}"
# The repo rule says a review lands 2–5 minutes after `gh pr create`. Younger
# than that and silence means "still coming", not "never came".
WINDOW_MIN="${REVIEW_WINDOW_MIN:-5}"
# Seconds between two `@coderabbitai review` posts. The limit replenishes one
# review at a time, so a burst just re-throttles every PR in the burst.
RETRIGGER_DELAY="${RETRIGGER_DELAY:-900}"
# Never block longer than this waiting for one PR's own limit to lift.
RETRIGGER_MAX_WAIT="${RETRIGGER_MAX_WAIT:-3600}"
BOT="coderabbitai[bot]"

usage() {
  cat <<'EOF'
Usage: scripts/review-status.sh [PR...] [--checks] [--json] [--window-min N]
       scripts/review-status.sh --retrigger [--dry-run] [--max N] [--delay S]

EOF
  sed -n '3,46p' "$0" | sed 's/^# \{0,1\}//'
}

die() { printf 'review-status: %s\n' "$1" >&2; exit 2; }

# GNU and BSD date disagree about -d; pick by capability, not by uname.
iso_to_epoch() { # <rfc3339>
  [ -n "${1:-}" ] || return 0
  if date --version >/dev/null 2>&1; then
    date -u -d "$1" +%s 2>/dev/null || true
  else
    date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$1" +%s 2>/dev/null || true
  fi
}

# ── shared jq: how long until the limit lifts ─────────────────────────────
# The notice carries its own answer — `**Next review available in:** **37
# minutes**` — so the re-trigger queue does not have to guess a backoff.
WAIT_JQ='
  def waitMinutes:
    ( [ scan("next review available in:?[^0-9]{0,20}([0-9]+)[^A-Za-z]{0,4}(minute|min|hour|hr)"; "i") ]
      | if length == 0 then null
        else ( .[0] as $m
               | ($m[0] | tonumber) as $n
               | if ($m[1] | ascii_downcase | startswith("h")) then $n * 60 else $n end )
        end );
'

# ── the classifier ────────────────────────────────────────────────────────
# Reads one JSON object on stdin, prints one TSV row:
#
#   state <TAB> headline <TAB> notes <TAB> throttledAt <TAB> waitMinutes
#
# Pure: no network, no `gh`, and no clock of its own — `now` is an input.
# That is what makes it testable, and the classification is the one part of
# this script that can be wrong in a way nobody notices.
#
# Input shape (see fetch_pr):
#   { now, createdAt, windowMin, headSha, statusState, statusDesc,
#     comments: [{createdAt, body, url}],
#     reviews:  [{submittedAt, state, body, commitId}] }
# Both arrays are already filtered to the CodeRabbit bot.
#
# Test order matters more than it looks: the rate-limit notice ALSO carries
# the `summarize by coderabbit.ai` marker a real walkthrough carries, so
# throttle must be tested before walkthrough or every throttled PR reads as
# "review started".
CLASSIFY_JQ="$WAIT_JQ"'
  def short: if . == null or . == "" then "?" else .[0:7] end;

  def isThrottle:
    test("rate limited by coderabbit\\.ai"; "i")
    or test("review limit reached"; "i")
    or test("reached your PR review limit"; "i")
    # Seen on #2022, 2026-08-20: a re-trigger with no slot left is refused
    # with "⚠️ Action not completed / Review rate limited." — none of the
    # three phrases above, and every marker of the "✅ Action performed"
    # acknowledgement below. Matched on the pair so a walkthrough that merely
    # mentions a limit cannot be mistaken for the refusal.
    or (test("action not completed"; "i") and test("rate.?limit"; "i"));

  # "Review failed — PR is closed" is the one this repo trips by merging
  # early; the rest are the generic CodeRabbit error shapes.
  def isFailure:
    test("review failed"; "i")
    or test("review (has )?(encountered|hit) an error"; "i")
    or test("could ?n.t (complete|finish) (the|this) review"; "i");

  def isWalkthrough: test("summarize by coderabbit\\.ai"; "i");

  # A walkthrough that reports a FINISHED review, as opposed to one that only
  # announces the change. #2038: on the CHILL profile a review that finds
  # nothing is delivered as this comment plus a zero-body APPROVED with no
  # inline comments — byte-identical, through the review object alone, to the
  # #1729 non-event. This comment is the difference, and it is only trusted
  # because it names the commit range it read (see reviewedSha).
  def isCompletedWalkthrough:
    test("<!-- recent_review_start -->"; "i")
    or test("no actionable comments were generated"; "i");

  # `Reviewing files that changed from the base of the PR and between
  #  39a4ea4b…388 and b709c11b…f1e.` — the END of the range is the commit
  # CodeRabbit actually read to.
  def reviewedSha:
    ( [ scan("between\\s+([0-9a-f]{7,40})\\s+and\\s+([0-9a-f]{7,40})"; "i") ]
      | if length == 0 then null else (.[0][1] | ascii_downcase) end );

  # Abbreviated SHAs appear in some of these bodies, so compare by prefix —
  # but never on a stub short enough to collide.
  def shaCovers($a; $b):
    ( ($a // "") | ascii_downcase ) as $x
    | ( ($b // "") | ascii_downcase ) as $y
    | ($x | length) >= 7 and ($y | length) >= 7
      and (($x | startswith($y)) or ($y | startswith($x)));

  # The most misleading artifact of the lot, found by running this script on
  # its own PR: answering `@coderabbitai review` while still rate-limited
  # gets back "✅ Action performed — Review finished." and no review at all.
  # It is an acknowledgement of the command, not a review of the code.
  def isAck:
    test("auto-generated reply by CodeRabbit"; "i")
    and (test("action performed"; "i") or test("review finished"; "i"));

  # `**Actionable comments posted: 1**`
  def actionable:
    ( [ scan("actionable comments posted:[^0-9]{0,4}([0-9]+)"; "i") ]
      | if length == 0 then null else (.[0][0] | tonumber) end );

  def secs: if . == null or . == "" then null else (try fromdateiso8601 catch null) end;

  . as $in
  | ($in.now | secs) as $now
  | ( [ $in.comments[]?
        | (.body // "") as $b
        | {
          at: (.createdAt // ""),
          t:  ((.createdAt // "") | secs),
          kind: ( $b
                  | if isThrottle then "throttle"
                    elif isFailure then "failure"
                    elif isAck then "ack"
                    elif isCompletedWalkthrough then "completed-walkthrough"
                    elif isWalkthrough then "walkthrough"
                    else "other" end ),
          # Only meaningful on a completed walkthrough; null everywhere else,
          # and a null never matches a commit id.
          reviewedSha: ($b | reviewedSha),
          found: ($b | if test("no actionable comments were generated"; "i")
                       then 0 else actionable end),
          wait: ($b | waitMinutes)
        } ] ) as $cev
  | ( [ $cev[] | select(.kind == "completed-walkthrough")
                | select(.reviewedSha != null) ] ) as $done

  | ( $cev
      + [ $in.reviews[]?
          | ((((.submittedAt // "") | secs) // 0) - 60) as $since
          | (.commitId // "") as $cid
          # The walkthrough that says a review of the commit THIS review
          # points at finished. Nothing else promotes an empty approval: not a
          # walkthrough for another commit, not one with no range at all.
          | ([ $done[] | select(shaCovers(.reviewedSha; $cid)) ] | last) as $cw
          | {
          at: (.submittedAt // ""),
          t:  ((.submittedAt // "") | secs),
          # A review event only counts when it carries CONTENT: a body, or
          # at least one inline comment posted with it. #1729: CodeRabbit
          # answered a rate-limited PR with an APPROVED review whose body was
          # empty and which touched no line, and "newest event wins" made
          # that outrank the throttle notice — the script reported `reviewed`
          # on a PR nothing had read. An approval that says nothing is the
          # same non-event the green check was.
          #
          # #2038 is the mirror image, and the reason "empty ⇒ not a review"
          # cannot be the whole rule: a CHILL-profile review that finds
          # nothing posts the same empty approval. So a third way to earn
          # `review` — a completed walkthrough naming this exact commit. The
          # evidence is the range, never the emptiness.
          kind: (if ((.body // "") | length) > 0
                    or ([ $in.reviewComments[]? | select((((.createdAt // "") | secs) // 0) >= $since) ] | length) > 0
                    or ($cw != null)
                 then "review" else "empty-review" end),
          # True only when the walkthrough is what carried it, so the
          # headline can say where the verdict came from.
          promoted: (((.body // "") | length) == 0
                     and ([ $in.reviewComments[]? | select((((.createdAt // "") | secs) // 0) >= $since) ] | length) == 0
                     and ($cw != null)),
          rstate: (.state // "?"),
          commitId: (.commitId // ""),
          actionable: (((.body // "") | actionable)
                       // (if $cw != null then $cw.found else null end))
        } ] ) as $ev

  | ($ev | map(select(.kind == "review"))      | last) as $rev
  | ($ev | map(select(.kind == "empty-review")) | last) as $emptyRev
  | ($ev | map(select(.kind == "throttle"))    | last) as $thr
  | ($ev | map(select(.kind == "failure"))     | last) as $fail
  | ($ev | map(select(.kind == "walkthrough" or .kind == "completed-walkthrough")) | last) as $walk
  | ($ev | map(select(.kind == "ack"))         | last) as $ack
  | (if $now == null or (($in.createdAt // "") | secs) == null then null
     else $now - (($in.createdAt // "") | secs) end) as $age
  | (($in.windowMin // 5) * 60) as $window

  # Does the winning review name the commit that is about to merge?
  | (($rev != null) and (($in.headSha // "") != "")
     and shaCovers($rev.commitId; $in.headSha)) as $revCoversHead

  # Newest wins. A review followed by a throttle means the latest push went
  # unread, however thorough the earlier review was — unless the review names
  # the head commit itself, in which case there was no later push and the
  # throttle is a refused RE-request, not an unread diff. Without that
  # exception every refused `@coderabbitai review` would demote a
  # fully-reviewed head, and `--retrigger` would then re-request it, collect
  # another refusal, and loop.
  | (if   ($rev != null) and (($thr == null) or ($rev.t >= $thr.t) or $revCoversHead)
          and (($fail == null) or ($rev.t >= $fail.t))              then "reviewed"
     elif ($thr != null) and (($fail == null) or ($thr.t >= $fail.t)) then "throttled"
     elif ($fail != null)                                            then "failed"
     elif ($age != null) and ($age < $window)                        then "pending"
     else "absent" end) as $state

  | (if $state == "reviewed" then
        "review " + ($rev.rstate | ascii_downcase)
        + (if $rev.actionable != null
           then ", " + ($rev.actionable | tostring) + " actionable comment(s)" else "" end)
        + (if ($rev.promoted // false)
           then " (empty body; the walkthrough records a completed review of "
                + ($rev.commitId | short) + ")" else "" end)
     elif $state == "throttled" then
        "rate-limited, no review"
        + (if $thr.wait != null
           then " (notice said next review in " + ($thr.wait | tostring) + "m)" else "" end)
     elif $state == "failed"  then "CodeRabbit reported a failure"
     elif $state == "pending" then
        "inside the " + (($in.windowMin // 5) | tostring) + "-minute window, nothing yet"
     else "nothing after the " + (($in.windowMin // 5) | tostring) + "-minute window"
     end) as $headline

  | ( []
      # The head SHA is what merges. A review of an earlier commit is a real
      # review of code that is no longer the code landing.
      + (if $state == "reviewed" and (($rev.commitId // "") != "")
              and ((($in.headSha) // "") != "") and ($rev.commitId != $in.headSha)
         then ["reviewed " + ($rev.commitId | short) + ", head is "
               + ($in.headSha | short) + " — the newest push is unreviewed"]
         else [] end)
      + (if $emptyRev != null
         then ["CodeRabbit " + ($emptyRev.rstate | ascii_downcase)
               + " with no content — no body, no inline comments; it did not read the diff"]
         else [] end)
      + (if $state == "throttled" and $rev != null and ($revCoversHead | not)
         then ["an earlier review exists (" + $rev.at + ") but does not cover the current head"]
         else [] end)
      + (if $state == "absent" and $walk != null
         then ["a walkthrough was posted, a review never followed"] else [] end)
      # THE CHECK THAT IS NOT THERE. `gh pr checks` can only render rows
      # that exist: a workflow which never produced a run for this head
      # contributes no row at all, so the list reads green with the main
      # test suite simply missing. Observed on #1833 on 2026-08-08 — two
      # pushes, no CI run either time, while the labeler
      # (pull_request_target) and the CodeQL default setup ran and
      # reported green. Cause not established: a conflicting PR was the
      # first guess and #1837 disproves it (conflicting, and its head does
      # have a CI run). So this asserts the OBSERVABLE — no CI run exists
      # for the head SHA — rather than a theory about why.
      + (if ($in.ciRunForHead == false)
         then ["no CI workflow run exists for head " + ($in.headSha | short)
               + " — `gh pr checks` cannot show a row that was never created, so its green means CI is ABSENT, not passing. Re-run it: gh workflow run ci.yml --ref <branch>"]
         else [] end)
      # Reading the thread, "✅ Action performed — Review finished." looks like
      # the answer. It is not: it acknowledges the command, and CodeRabbit
      # will not re-review a commit it has already seen, so a re-trigger fired
      # too early gets this and nothing else.
      + (if $state != "reviewed" and $ack != null
              and (($rev == null) or ($ack.t > $rev.t))
         then ["CodeRabbit replied «Review finished» but submitted no review — that reply acknowledges the command, it is not a review"]
         else [] end)
      # Cross-check against the status the merge button shows. Disagreement in
      # either direction is worth printing: this script and that status line
      # read different evidence, and only one of them is evidence.
      + (if $state == "reviewed" and ((($in.statusDesc) // "") | test("rate.?limit"; "i"))
         then ["status says «" + $in.statusDesc + "» — disagrees with the posted review"]
         else [] end)
      + (if $state != "reviewed" and ((($in.statusDesc) // "") | test("completed"; "i"))
         then ["status says «" + $in.statusDesc + "» — but no review was posted"]
         else [] end)
      + (if $state != "reviewed" and $state != "pending"
              and ((($in.statusState) // "") == "success")
         then ["CodeRabbit status is green — merging on it would be merging unreviewed"]
         else [] end)
    ) as $notes

  # Placeholders, never empty fields. `read -r a b c` with a tab IFS collapses
  # runs of tabs — tab is IFS whitespace — so an empty column silently shifts
  # every later column left, and a wait-minutes value lands in the notes.
  | def shown: if . == null or . == "" then "-" else . end;
    [ $state, $headline, (($notes | join(" | ")) | shown),
      ((if $thr != null then ($thr.at // "") else "" end) | shown),
      (if $thr != null then (($thr.wait // 0) | tostring) else "0" end) ]
  | @tsv
'

classify() { jq -r "$CLASSIFY_JQ"; }

# ── check-run reading ─────────────────────────────────────────────────────
# The sibling failure, same shape, different producer: `gh pr checks` prints
# `skipping` for a job that never ran and the aggregate stays green — exactly
# as `go test` prints `ok` for a package whose every test called t.Skip (see
# scripts/skip-budget.sh). CodeQL concludes `neutral` while its findings live
# in the run's annotations, which no check status surfaces, so the annotation
# count is printed whenever it is non-zero on a green run.
CHECKS_JQ='
  [ .check_runs[]?
    | { name, status, conclusion, ann: (.output.annotations_count // 0) } ] as $c
  | { skipped:   [ $c[] | select(.conclusion == "skipped") | .name ],
      neutral:   [ $c[] | select(.conclusion == "neutral") | .name ],
      annotated: [ $c[] | select((.conclusion == "success" or .conclusion == "neutral")
                                 and .ann > 0)
                        | .name + " (" + (.ann | tostring) + " annotation(s))" ],
      failed:    [ $c[] | select(.conclusion == "failure" or .conclusion == "timed_out"
                                 or .conclusion == "cancelled") | .name ] }
'

# ── fetch ─────────────────────────────────────────────────────────────────
# Every call is checked. A failed fetch becomes state `unknown` upstream. The
# one thing this script must never do is turn "the API did not answer" into
# "nothing was posted" — that is the exact substitution it exists to catch.
fetch_pr() { # <number> -> classifier input on stdout, rc 1 if anything failed
  local n="$1" pr comments reviews revcomments status sha
  pr="$(gh api "repos/$REPO/pulls/$n" 2>/dev/null)" || return 1
  [ -n "$pr" ] || return 1
  sha="$(printf '%s' "$pr" | jq -r '.head.sha // ""' 2>/dev/null)" || return 1
  [ -n "$sha" ] || return 1
  comments="$(gh api "repos/$REPO/issues/$n/comments" --paginate 2>/dev/null)" || return 1
  reviews="$(gh api "repos/$REPO/pulls/$n/reviews" --paginate 2>/dev/null)" || return 1
  # Inline review comments. Needed because a review can carry its findings
  # entirely on the diff with an empty body — and, the other way round (#1729),
  # because an APPROVED review with neither body nor inline comments read
  # nothing and must not be counted as a review.
  revcomments="$(gh api "repos/$REPO/pulls/$n/comments" --paginate 2>/dev/null)" || return 1
  revcomments="$(printf '%s' "$revcomments" | jq -s 'add // []' 2>/dev/null)" || return 1
  # --paginate concatenates one JSON array per page; slurp them back into one.
  comments="$(printf '%s' "$comments" | jq -s 'add // []' 2>/dev/null)" || return 1
  reviews="$(printf '%s' "$reviews" | jq -s 'add // []' 2>/dev/null)" || return 1
  status="$(gh api "repos/$REPO/commits/$sha/status" 2>/dev/null)" || status=""
  [ -n "$status" ] || status='{"statuses":[]}'
  # Did the CI workflow produce a run for THIS commit at all? A workflow
  # that never ran contributes no check row, so its absence is invisible in
  # `gh pr checks`. Any event counts, including a manual workflow_dispatch:
  # the question is whether CI was exercised, not how it was triggered.
  local ciruns ciRun=false
  ciruns="$(gh api "repos/$REPO/actions/runs?head_sha=$sha&per_page=100" 2>/dev/null)" || ciruns=""
  if [ -n "$ciruns" ] &&
     printf '%s' "$ciruns" | jq -e '[.workflow_runs[]? | select(.name == "CI")] | length > 0' >/dev/null 2>&1; then
    ciRun=true
  fi

  jq -n \
    --argjson pr "$pr" \
    --argjson comments "$comments" \
    --argjson reviews "$reviews" \
    --argjson revcomments "$revcomments" \
    --argjson status "$status" \
    --arg bot "$BOT" \
    --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson window "$WINDOW_MIN" \
    --argjson ciRun "$ciRun" '
    ( [ $status.statuses[]? | select((.context // "") | test("coderabbit"; "i")) ] | last ) as $s
    | { number: $pr.number, title: ($pr.title // ""), branch: ($pr.head.ref // ""),
        headSha: ($pr.head.sha // ""), createdAt: ($pr.created_at // ""),
        now: $now, windowMin: $window,
        # Whether ANY run of the CI workflow exists for the head commit.
        ciRunForHead: $ciRun,
        statusState: (($s.state) // ""), statusDesc: (($s.description) // ""),
        comments: [ $comments[] | select(.user.login == $bot)
                    | {createdAt: .created_at, body: (.body // "")} ],
        reviewComments: [ $revcomments[] | select(.user.login == $bot)
                    | {createdAt: .created_at} ],
        reviews:  [ $reviews[]  | select(.user.login == $bot)
                    | select((.state // "") != "PENDING")
                    | {submittedAt: .submitted_at, state: .state,
                       body: (.body // ""), commitId: (.commit_id // "")} ] }' 2>/dev/null
}

# ── rendering ─────────────────────────────────────────────────────────────
badge() { # <state>
  case "$1" in
    reviewed)  printf '✓ reviewed ' ;;
    throttled) printf '✗ THROTTLED' ;;
    failed)    printf '✗ FAILED   ' ;;
    pending)   printf '· pending  ' ;;
    absent)    printf '✗ ABSENT   ' ;;
    *)         printf '? unknown  ' ;;
  esac
}

# Reads the raw `--paginate` stream (one object per page) on stdin and prints
# the summary object. Pure, so the tests can drive it without a network.
classify_checks() {
  jq -s '{check_runs: (map(.check_runs // []) | add // [])}' | jq "$CHECKS_JQ"
}

report_checks() { # <sha>
  local runs j skipped neutral annotated failed
  runs="$(gh api "repos/$REPO/commits/$1/check-runs" --paginate 2>/dev/null)" || {
    printf '             checks: unreadable (API error)\n'; return 0; }
  j="$(printf '%s' "$runs" | classify_checks 2>/dev/null)" || return 0
  skipped="$(printf '%s' "$j" | jq -r '.skipped | length')"
  neutral="$(printf '%s' "$j" | jq -r '.neutral | join(", ")')"
  annotated="$(printf '%s' "$j" | jq -r '.annotated | join(", ")')"
  failed="$(printf '%s' "$j" | jq -r '.failed | join(", ")')"
  # Skipped jobs are normal and numerous here (path filters), so the count is
  # the signal and the names are noise. `neutral` is rarer and worth naming:
  # that is what CodeQL reports, and `gh pr checks` renders it as "skipping".
  [ "$skipped" != "0" ] && printf '             %s check(s) skipped — green without running\n' "$skipped"
  [ -n "$neutral" ] && printf '             neutral, renders as "skipping": %s\n' "$neutral"
  [ -n "$annotated" ] && printf '             green but carrying annotations: %s\n' "$annotated"
  [ -n "$failed" ] && printf '             failing: %s\n' "$failed"
  return 0
}

# ── main loop ─────────────────────────────────────────────────────────────
WORST=0
THROTTLED_LIST=""

examine() { # <number>
  local n="$1" input row state headline notes thr_at thr_wait title branch sha meta
  input="$(fetch_pr "$n")"
  if [ -z "$input" ]; then
    if [ "$JSON" -eq 1 ]; then
      printf '{"number":%s,"state":"unknown","headline":"GitHub API call failed"}\n' "$n"
    else
      printf '%s  #%-5s %s\n\n' "$(badge unknown)" "$n" \
        '(GitHub API call failed — state genuinely unknown, NOT "no review")'
    fi
    WORST=3
    return 0
  fi
  row="$(printf '%s' "$input" | classify)"
  IFS=$'\t' read -r state headline notes thr_at thr_wait <<< "$row"
  [ "$notes" = "-" ] && notes=""
  [ "$thr_at" = "-" ] && thr_at=""
  meta="$(printf '%s' "$input" | jq -r '[.title, .branch, .headSha] | @tsv')"
  IFS=$'\t' read -r title branch sha <<< "$meta"

  if [ "$JSON" -eq 1 ]; then
    jq -nc --argjson n "$n" --arg s "$state" --arg h "$headline" --arg no "$notes" \
           --arg t "$title" --arg b "$branch" \
      '{number:$n, state:$s, headline:$h,
        notes:($no | split(" | ") | map(select(. != ""))), title:$t, branch:$b}'
  else
    printf '%s  #%-5s %s\n' "$(badge "$state")" "$n" "$title"
    printf '             %s\n' "$headline"
    if [ -n "$notes" ]; then
      printf '%s\n' "$notes" | tr '|' '\n' | sed 's/^ *//; s/ *$//; /^$/d; s/^/             ⚠ /'
    fi
    [ "$SHOW_CHECKS" -eq 1 ] && report_checks "$sha"
    printf '\n'
  fi

  case "$state" in
    reviewed) ;;
    throttled)
      THROTTLED_LIST="${THROTTLED_LIST}${n}	${thr_at}	${thr_wait}
"
      WORST=3 ;;
    *) WORST=3 ;;
  esac
  return 0
}

# ── re-review queue ───────────────────────────────────────────────────────
# Not a burst. Each `@coderabbitai review` consumes the one slot the limit
# just released, so firing at eleven PRs at once re-throttles ten of them —
# the exact hole this script exists to report. The queue waits out each PR's
# own "next review available in N minutes" before its turn, then spaces the
# rest by --delay.
retrigger() {
  if [ -z "$THROTTLED_LIST" ]; then
    printf 'Nothing throttled — nothing to re-trigger.\n'
    return 0
  fi
  local n at wait_m ready_at now sleep_for posted=0 planned=0
  printf '\n── re-review queue ──────────────────────────────────────────\n'
  [ "$DRY_RUN" -eq 1 ] && printf '(dry run: the schedule below is computed, nothing is posted)\n'
  printf '\n'
  while IFS='	' read -r n at wait_m; do
    [ -n "$n" ] || continue
    if [ "$RETRIGGER_MAX" -gt 0 ] && [ "$posted" -ge "$RETRIGGER_MAX" ]; then
      printf '  … stopping at --max %s; re-run later for the rest.\n' "$RETRIGGER_MAX"
      break
    fi
    case "$wait_m" in ''|*[!0-9]*) wait_m=0 ;; esac

    ready_at=""
    if [ -n "$at" ] && [ "$wait_m" -gt 0 ]; then
      ready_at="$(iso_to_epoch "$at")"
      [ -n "$ready_at" ] && ready_at=$((ready_at + wait_m * 60))
    fi
    now="$(date -u +%s)"
    sleep_for=0
    if [ -n "$ready_at" ] && [ "$ready_at" -gt "$now" ]; then
      sleep_for=$((ready_at - now))
    fi
    # After the first post the binding constraint is our own spacing, not the
    # notice's estimate — that estimate was written before we spent a slot.
    if [ "$posted" -gt 0 ] && [ "$sleep_for" -lt "$RETRIGGER_DELAY" ]; then
      sleep_for="$RETRIGGER_DELAY"
    fi
    [ "$sleep_for" -gt "$RETRIGGER_MAX_WAIT" ] && sleep_for="$RETRIGGER_MAX_WAIT"

    if [ "$DRY_RUN" -eq 1 ]; then
      printf '  #%-5s  wait %5ds  then post: @coderabbitai review\n' "$n" "$sleep_for"
      planned=$((planned + sleep_for))
      posted=$((posted + 1))
      continue
    fi
    if [ "$sleep_for" -gt 0 ]; then
      printf '  #%-5s  waiting %ds for its limit to lift…\n' "$n" "$sleep_for"
      sleep "$sleep_for"
    fi
    if printf '@coderabbitai review\n' | gh pr comment "$n" --repo "$REPO" --body-file - >/dev/null 2>&1; then
      printf '  #%-5s  re-review requested\n' "$n"
    else
      printf '  #%-5s  FAILED to post — check `gh auth status`\n' "$n"
    fi
    posted=$((posted + 1))
  done <<< "$THROTTLED_LIST"
  if [ "$DRY_RUN" -eq 1 ]; then
    printf '\n  %s PR(s), ~%dm of wall time if run for real. Run it in a background\n' \
      "$posted" "$((planned / 60))"
    printf '  shell, or in batches with --max, rather than blocking a session on it.\n'
  fi
  printf '\n'
}

# ── argument parsing ──────────────────────────────────────────────────────
SHOW_CHECKS=0; JSON=0; DRY_RUN=0; RETRIGGER=0; RETRIGGER_MAX=0; PRS=""

case "${1:-}" in
  # Internal, for the tests: pure stdin→stdout, no network.
  --classify)        shift; classify; exit 0 ;;
  --classify-checks) shift; classify_checks; exit 0 ;;
  --parse-wait)      shift; jq -Rrs "$WAIT_JQ"' waitMinutes // "" '; exit 0 ;;
  -h|--help)         usage; exit 0 ;;
esac

while [ $# -gt 0 ]; do
  case "$1" in
    --checks)     SHOW_CHECKS=1 ;;
    --json)       JSON=1 ;;
    --retrigger)  RETRIGGER=1 ;;
    --dry-run)    DRY_RUN=1 ;;
    --max)        RETRIGGER_MAX="${2:-0}"; shift ;;
    --delay)      RETRIGGER_DELAY="${2:-$RETRIGGER_DELAY}"; shift ;;
    --window-min) WINDOW_MIN="${2:-$WINDOW_MIN}"; shift ;;
    --repo)       REPO="${2:-$REPO}"; shift ;;
    -h|--help)    usage; exit 0 ;;
    -*)           die "unknown flag: $1" ;;
    *)            case "$1" in ''|*[!0-9]*) die "PR must be a number, got «$1»";; esac
                  PRS="$PRS $1" ;;
  esac
  shift
done

command -v gh >/dev/null 2>&1 || die "gh CLI not found — https://cli.github.com/"
command -v jq >/dev/null 2>&1 || die "jq not found"
for v in "$WINDOW_MIN" "$RETRIGGER_MAX" "$RETRIGGER_DELAY"; do
  case "$v" in ''|*[!0-9]*) die "expected a number, got «$v»";; esac
done

if [ -z "${PRS// /}" ]; then
  PRS="$(gh pr list --repo "$REPO" --state open --limit 100 --json number \
           -q '.[].number' 2>/dev/null | tr '\n' ' ')" \
    || die "cannot list open PRs (gh logged in?)"
  [ -n "${PRS// /}" ] || die "no open PRs found (or the listing failed)"
fi

COUNT=0
[ "$JSON" -eq 1 ] || printf '\nCodeRabbit review state — %s (window %sm, %s)\n\n' \
  "$REPO" "$WINDOW_MIN" "$(date -u +%Y-%m-%dT%H:%MZ)"

for n in $PRS; do
  examine "$n"
  COUNT=$((COUNT + 1))
done

if [ "$JSON" -eq 0 ]; then
  if [ "$WORST" -eq 0 ]; then
    printf 'All %s PR(s) examined carry a real review.\n\n' "$COUNT"
  else
    cat <<'EOF'
At least one PR above is NOT reviewed, and its CodeRabbit check still says
`pass`. That green is the bug, not the verdict — do not merge on it. Ask for
the review again, as a queue rather than a burst:

    scripts/review-status.sh --retrigger --dry-run   # see the schedule
    scripts/review-status.sh --retrigger             # run it

EOF
  fi
fi

[ "$RETRIGGER" -eq 1 ] && retrigger

exit "$WORST"
