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
#   reviewed   a review was submitted (with its actionable-comment count)
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
  sed -n '3,42p' "$0" | sed 's/^# \{0,1\}//'
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
    or test("reached your PR review limit"; "i");

  # "Review failed — PR is closed" is the one this repo trips by merging
  # early; the rest are the generic CodeRabbit error shapes.
  def isFailure:
    test("review failed"; "i")
    or test("review (has )?(encountered|hit) an error"; "i")
    or test("could ?n.t (complete|finish) (the|this) review"; "i");

  def isWalkthrough: test("summarize by coderabbit\\.ai"; "i");

  # `**Actionable comments posted: 1**`
  def actionable:
    ( [ scan("actionable comments posted:[^0-9]{0,4}([0-9]+)"; "i") ]
      | if length == 0 then null else (.[0][0] | tonumber) end );

  def secs: if . == null or . == "" then null else (try fromdateiso8601 catch null) end;

  . as $in
  | ($in.now | secs) as $now
  | ( [ $in.comments[]? | {
          at: (.createdAt // ""),
          t:  ((.createdAt // "") | secs),
          kind: ( (.body // "")
                  | if isThrottle then "throttle"
                    elif isFailure then "failure"
                    elif isWalkthrough then "walkthrough"
                    else "other" end ),
          wait: ((.body // "") | waitMinutes)
        } ]
      + [ $in.reviews[]? | {
          at: (.submittedAt // ""),
          t:  ((.submittedAt // "") | secs),
          kind: "review",
          rstate: (.state // "?"),
          commitId: (.commitId // ""),
          actionable: ((.body // "") | actionable)
        } ] ) as $ev

  | ($ev | map(select(.kind == "review"))      | last) as $rev
  | ($ev | map(select(.kind == "throttle"))    | last) as $thr
  | ($ev | map(select(.kind == "failure"))     | last) as $fail
  | ($ev | map(select(.kind == "walkthrough")) | last) as $walk
  | (if $now == null or (($in.createdAt // "") | secs) == null then null
     else $now - (($in.createdAt // "") | secs) end) as $age
  | (($in.windowMin // 5) * 60) as $window

  # Newest wins. A review followed by a throttle means the latest push went
  # unread, however thorough the earlier review was.
  | (if   ($rev != null) and (($thr == null) or ($rev.t >= $thr.t))
          and (($fail == null) or ($rev.t >= $fail.t))              then "reviewed"
     elif ($thr != null) and (($fail == null) or ($thr.t >= $fail.t)) then "throttled"
     elif ($fail != null)                                            then "failed"
     elif ($age != null) and ($age < $window)                        then "pending"
     else "absent" end) as $state

  | (if $state == "reviewed" then
        "review " + ($rev.rstate | ascii_downcase)
        + (if $rev.actionable != null
           then ", " + ($rev.actionable | tostring) + " actionable comment(s)" else "" end)
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
      + (if $state == "throttled" and $rev != null
         then ["an earlier review exists (" + $rev.at + ") but does not cover the current head"]
         else [] end)
      + (if $state == "absent" and $walk != null
         then ["a walkthrough was posted, a review never followed"] else [] end)
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
  local n="$1" pr comments reviews status sha
  pr="$(gh api "repos/$REPO/pulls/$n" 2>/dev/null)" || return 1
  [ -n "$pr" ] || return 1
  sha="$(printf '%s' "$pr" | jq -r '.head.sha // ""' 2>/dev/null)" || return 1
  [ -n "$sha" ] || return 1
  comments="$(gh api "repos/$REPO/issues/$n/comments" --paginate 2>/dev/null)" || return 1
  reviews="$(gh api "repos/$REPO/pulls/$n/reviews" --paginate 2>/dev/null)" || return 1
  # --paginate concatenates one JSON array per page; slurp them back into one.
  comments="$(printf '%s' "$comments" | jq -s 'add // []' 2>/dev/null)" || return 1
  reviews="$(printf '%s' "$reviews" | jq -s 'add // []' 2>/dev/null)" || return 1
  status="$(gh api "repos/$REPO/commits/$sha/status" 2>/dev/null)" || status=""
  [ -n "$status" ] || status='{"statuses":[]}'

  jq -n \
    --argjson pr "$pr" \
    --argjson comments "$comments" \
    --argjson reviews "$reviews" \
    --argjson status "$status" \
    --arg bot "$BOT" \
    --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --argjson window "$WINDOW_MIN" '
    ( [ $status.statuses[]? | select((.context // "") | test("coderabbit"; "i")) ] | last ) as $s
    | { number: $pr.number, title: ($pr.title // ""), branch: ($pr.head.ref // ""),
        headSha: ($pr.head.sha // ""), createdAt: ($pr.created_at // ""),
        now: $now, windowMin: $window,
        statusState: (($s.state) // ""), statusDesc: (($s.description) // ""),
        comments: [ $comments[] | select(.user.login == $bot)
                    | {createdAt: .created_at, body: (.body // "")} ],
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
      printf '%s\n' "$notes" | tr '|' '\n' | sed 's/^ *//; /^$/d; s/^/             ⚠ /'
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
