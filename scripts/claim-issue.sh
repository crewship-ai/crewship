#!/usr/bin/env bash
#
# claim-issue.sh — claim / release / check an issue, so two agent sessions
# don't silently work the same one.
#
# Why (#1488): every session pushes as the same GitHub account, so the
# assignee field cannot tell "taken by another session" from "that's me".
# #1481 duplicated #1471's fix and both sides lost the work. The lock that
# does work is a comment naming clone + branch + UTC time, released in the
# same thread when the session stops. This fills all three in for you and,
# more importantly, looks before you leap.
#
#   scripts/claim-issue.sh 1488                    # check, then claim
#   scripts/claim-issue.sh 1488 --check            # just look (read-only)
#   scripts/claim-issue.sh 1488 --release "hypothesis unconfirmed, see above"
#   scripts/claim-issue.sh --list                  # every open claim in the repo
#   scripts/claim-issue.sh 1488 --dry-run          # print the comment, post nothing
#
# Exit codes: 0 clear / claimed  ·  2 usage or tooling error
#             3 someone else holds an active claim (nothing was posted)
#
# Full convention, including the failure modes: CONTRIBUTING.md → "Claiming an
# issue before you work it".
#
# Requires: gh CLI logged in, jq.

set -uo pipefail

REPO="${REPO:-crewship-ai/crewship}"
# A claim older than this is reported as STALE — the session probably died
# without releasing.
#
# Stale still BLOCKS (exit 3). That is deliberate and it is the whole reason
# the threshold is only a label: "old" and "abandoned" are not the same thing,
# a long-running job that outlived the window is exactly the work you least
# want to duplicate, and the script cannot tell the two apart. Say so in the
# thread and take it over with --force. CONTRIBUTING.md → "When it goes wrong".
STALE_HOURS="${CLAIM_STALE_HOURS:-24}"

usage() {
  cat <<'EOF'
Usage: scripts/claim-issue.sh <issue> [--check | --release "<reason>"] [--dry-run] [--force]
       scripts/claim-issue.sh --list [--limit N]

EOF
  sed -n '3,26p' "$0" | sed 's/^# \{0,1\}//'
}

die() { printf 'claim-issue: %s\n' "$1" >&2; exit 2; }

# ── clone name ────────────────────────────────────────────────────────────
# A path like .../crewship_3/.claude/worktrees/agent-abc belongs to clone
# crewship_3 — the worktree name is noise, the numbered checkout is the
# identity that maps to a dev slot. Fall back to the last path component when
# there is no crewship_N in the path at all.
clone_of_path() { # <path>
  local path="$1" hit
  hit="$(printf '%s\n' "$path" | grep -oE 'crewship_[0-9]+' | head -1)"
  if [ -n "$hit" ]; then printf '%s\n' "$hit"; return 0; fi
  path="${path%/}"; path="${path%/.git}"
  printf '%s\n' "${path##*/}"
}

# CLAIM_CLONE / CLAIM_BRANCH override detection. Two real uses beyond the
# tests: a detached-HEAD worktree, where `rev-parse --abbrev-ref HEAD` reports
# the literal "HEAD" and every such session would claim under the same
# meaningless branch name; and a container or CI checkout whose path carries no
# crewship_N at all. An identity you can state is better than one guessed wrong.
detect_clone() {
  if [ -n "${CLAIM_CLONE:-}" ]; then printf '%s\n' "$CLAIM_CLONE"; return 0; fi
  local root
  root="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" ||
    root="$(git rev-parse --show-toplevel 2>/dev/null)" ||
    root="$PWD"
  [ -n "$root" ] || root="$PWD"
  clone_of_path "$root"
}

detect_branch() {
  if [ -n "${CLAIM_BRANCH:-}" ]; then printf '%s\n' "$CLAIM_BRANCH"; return 0; fi
  local branch upstream root
  branch="$(git rev-parse --abbrev-ref HEAD 2>/dev/null)" || branch=""
  case "$branch" in
    worktree-agent-*)
      # `git worktree add` without an explicit -b mints this branch name
      # automatically, and CONTRIBUTING says to claim *before* the feature
      # branch exists — so this is the ordinary, documented sequence, not an
      # edge case. The hash means nothing to a reader and it is guaranteed to
      # differ from whatever branch is checked out by the time --release
      # runs, which is what makes a claim unreleasable (#2107). Prefer the
      # upstream/tracking branch if one is already configured (rare this
      # early, but free when it exists); otherwise fall back to the worktree
      # path — unlike the branch, it does not change between CLAIM and the
      # matching RELEASE, and unlike a bare "?" it still tells two sessions
      # on the same clone apart.
      upstream="$(git rev-parse --abbrev-ref --symbolic-full-name '@{u}' 2>/dev/null)" || upstream=""
      if [ -n "$upstream" ]; then
        printf '%s\n' "$upstream"
        return 0
      fi
      root="$(git rev-parse --path-format=absolute --show-toplevel 2>/dev/null)" || root="$PWD"
      printf '%s\n' "$root"
      return 0
      ;;
    "")
      printf 'unknown\n'
      return 0
      ;;
    *)
      printf '%s\n' "$branch"
      return 0
      ;;
  esac
}

now_stamp() { date -u +'%Y-%m-%dT%H:%MZ'; }

# Seconds since an RFC 3339 timestamp, or "" if it cannot be parsed. GNU and
# BSD date disagree about -d, so pick by capability rather than by uname.
age_seconds() { # <rfc3339>
  local at now
  if date --version >/dev/null 2>&1; then
    at="$(date -u -d "$1" +%s 2>/dev/null)" || return 0
  else
    at="$(date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$1" +%s 2>/dev/null)" || return 0
  fi
  [ -n "$at" ] || return 0
  now="$(date -u +%s)"
  printf '%s\n' "$((now - at))"
}

human_age() { # <seconds>
  local s="$1"
  [ -n "$s" ] || { printf 'age unknown\n'; return 0; }
  if [ "$s" -lt 3600 ]; then printf '%dm ago\n' "$((s / 60))"
  elif [ "$s" -lt 172800 ]; then printf '%dh ago\n' "$((s / 3600))"
  else printf '%dd ago\n' "$((s / 86400))"; fi
}

# ── the parser ────────────────────────────────────────────────────────────
# Reads the comment array `gh issue view --json comments -q .comments` returns
# and prints one TSV row per claim that has not been released:
#   clone <TAB> branch <TAB> createdAt <TAB> author <TAB> url
#
# Tolerant on input by design: threads already contain hand-written claims
# ("Claimed — clone `x`, branch `y`, …") from before this script existed, and a
# claim the parser cannot see is reported as no claim at all. Strict on
# position, though — the marker word has to open the comment, so prose that
# happens to use "claim" mid-sentence is not a lock.
#
# A release cancels the claims it names by clone identity alone (#2107): a
# clone can hold only one live claim on an issue in practice, so matching on
# clone is enough, and it is the direction that fails safe. Branch is not part
# of the predicate — a worktree claims before its feature branch exists
# (CONTRIBUTING says to claim before the first commit), so branch is
# guaranteed to differ between CLAIM and the matching RELEASE for every
# well-behaved session; requiring it to match turned most releases into
# permanent phantom locks. The alternative — cancel only the most recent
# unreleased claim by that clone — is more precise when one clone legitimately
# holds two live claims on the same issue (it can, across worktrees), but
# picking the wrong one of two reintroduces this exact bug. Clone-only
# matching over-cancels in that rare case, and in the safe direction: a
# session that still holds the issue just re-claims, cheaply and visibly. A
# release that names neither clone nor branch cancels every open claim on the
# issue — the manual escape hatch, kept as-is.
parse_claims() {
  jq -r '
    def line0: ((.body // "") | split("\n")[0]);
    # `clone crewship_3` and `clone `crewship_3`` both count: the helper
    # writes backticks, humans do not, and both shapes are already in the
    # threads. Value stops at the first character a clone or branch name
    # cannot contain (comma, backtick, space, ·).
    def fld($k): ([ (.body // "")
                    | scan($k + "[[:space:]:=]+`?([A-Za-z0-9._/-]+)`?"; "i") ]
                  | (.[0][0] // ""));
    def shown: if . == "" then "?" else . end;
    [ .[] | {
        kind: ( line0
                | if   test("^[[:space:]]*[*_~]*[[:space:]]*(claim|claimed|claiming)\\b"; "i") then "claim"
                  elif test("^[[:space:]]*[*_~]*[[:space:]]*(release|released|releasing|unclaim|unclaimed)\\b"; "i") then "release"
                  else "skip" end ),
        clone:  fld("clone"),
        branch: fld("branch"),
        at:     (.createdAt // ""),
        author: (.author.login // "?"),
        url:    (.url // "")
      } ]
    | map(select(.kind != "skip"))
    | reduce .[] as $c ([];
        if $c.kind == "claim" then . + [$c]
        else map(select(($c.clone != "") and ($c.clone != .clone)))
        end)
    # Placeholders, not empty fields: `read -r a b c` with a tab IFS drops a
    # leading empty field and shifts every column left.
    | .[] | [(.clone|shown), (.branch|shown), (.at|shown), (.author|shown), (.url|shown)] | @tsv
  '
}

# ── read side ─────────────────────────────────────────────────────────────
fetch_claims() { # <issue>
  gh issue view "$1" --repo "$REPO" --json comments -q '.comments' 2>/dev/null | parse_claims
}

# Branches and PRs naming the issue number are a claim signal too — someone got
# far enough to push, whether or not they remembered to comment.
print_related() { # <issue>
  local branches prs
  branches="$(git for-each-ref --format='%(refname:short)' refs/heads refs/remotes 2>/dev/null |
    grep -E "(^|[^0-9])$1([^0-9]|$)" | head -5)"
  # GitHub's search is fuzzy enough to return #1530 for "1550", so the number
  # is re-checked here as a whole word before anything is reported.
  prs="$(gh pr list --repo "$REPO" --state open --search "$1" \
    --json number,headRefName,title,body 2>/dev/null |
    jq -r --arg n "$1" '.[]
      | select(((.title // "") + " " + (.body // "")) | test("(^|[^0-9])#?" + $n + "([^0-9]|$)"))
      | "    PR #\(.number)  \(.headRefName)  \(.title)"' 2>/dev/null | head -5)"
  if [ -n "$branches" ] || [ -n "$prs" ]; then
    printf '\n  Other signals that someone is on it:\n'
    [ -n "$prs" ] && printf '%s\n' "$prs"
    [ -n "$branches" ] && printf '%s\n' "$branches" | sed 's/^/    branch  /'
  fi
}

# Prints the claim table; returns 3 if a live claim belongs to another
# clone/branch, 0 otherwise. MINE is set when this clone+branch already holds it.
MINE=0
report_claims() { # <issue> <claims-tsv>
  local issue="$1" claims="$2" clone branch foreign=0
  clone="$(detect_clone)"; branch="$(detect_branch)"
  if [ -z "$claims" ]; then
    printf '  no active claim\n'
    return 0
  fi
  local c b at _who url secs age tag
  while IFS=$'\t' read -r c b at _who url; do
    [ -n "$c$b" ] || continue
    secs="$(age_seconds "$at")"
    age="$(human_age "$secs")"
    if [ "$c" = "$clone" ] && [ "$b" = "$branch" ]; then
      tag='← this clone + branch (yours)'; MINE=1
    elif [ -n "$secs" ] && [ "$secs" -gt "$((STALE_HOURS * 3600))" ]; then
      tag="← STALE (>${STALE_HOURS}h, session likely died — see CONTRIBUTING.md)"; foreign=1
    else
      tag='← HELD BY ANOTHER SESSION'; foreign=1
    fi
    printf '  CLAIM  %s · %s · %s  %s\n' "$c" "$b" "$age" "$tag"
    [ "$url" != "?" ] && printf '         %s\n' "$url"
  done <<< "$claims"
  [ "$foreign" -eq 1 ] && return 3
  return 0
}

cmd_check() { # <issue>
  local issue="$1" claims rc title state
  read -r state title <<< "$(gh issue view "$issue" --repo "$REPO" --json state,title \
    -q '"\(.state) \(.title)"' 2>/dev/null)" || die "cannot read issue #$issue (gh logged in?)"
  [ -n "$state" ] || die "cannot read issue #$issue (gh logged in?)"
  printf '\nIssue #%s — %s (%s)\n\n' "$issue" "$title" "$state"
  claims="$(fetch_claims "$issue")"
  report_claims "$issue" "$claims"; rc=$?
  print_related "$issue"
  printf '\n'
  return $rc
}

cmd_list() {
  local limit="${1:-100}" any=0 row decoded num title claims c b at _who _url
  # Process substitution, not a pipe: a pipeline would run the loop in a
  # subshell and `any` would come back 0 no matter what was printed.
  while read -r row; do
    decoded="$(printf '%s' "$row" | base64 --decode)"
    num="$(printf '%s' "$decoded" | jq -r '.number')"
    title="$(printf '%s' "$decoded" | jq -r '.title')"
    claims="$(printf '%s' "$decoded" | jq -c '.comments' | parse_claims)"
    [ -n "$claims" ] || continue
    any=1
    printf '#%-6s %s\n' "$num" "$title"
    while IFS=$'\t' read -r c b at _who _url; do
      [ -n "$c$b" ] || continue
      printf '        %-14s %-45s %s\n' "${c:-?}" "${b:-?}" "$(human_age "$(age_seconds "$at")")"
    done <<< "$claims"
  done < <(gh issue list --repo "$REPO" --state open --limit "$limit" \
             --json number,title,comments -q '.[] | @base64' 2>/dev/null)
  [ "$any" -eq 0 ] && printf '(no unreleased claims on the %s most recent open issues)\n' "$limit"
  return 0
}

claim_body() { # <clone> <branch> <stamp>
  printf '**CLAIM** — clone `%s` · branch `%s` · %s\n\nWorking this now. Will post a **RELEASE** comment in this thread when I stop, whether it ships or not.\n' "$1" "$2" "$3"
}

release_body() { # <clone> <branch> <stamp> <reason>
  printf '**RELEASE** — clone `%s` · branch `%s` · %s\n\n%s\n' "$1" "$2" "$3" "$4"
}

post() { # <issue> <body>
  if [ "$DRY_RUN" -eq 1 ]; then
    printf -- '--- dry run, nothing posted to #%s ---\n%s\n--------------------------------------\n' "$1" "$2"
    return 0
  fi
  printf '%s' "$2" | gh issue comment "$1" --repo "$REPO" --body-file -
}

# ── argument parsing ──────────────────────────────────────────────────────
ISSUE=""; MODE="claim"; REASON=""; DRY_RUN=0; FORCE=0; LIMIT=100

case "${1:-}" in
  --parse)     shift; parse_claims; exit 0 ;;   # internal: JSON on stdin (tests)
  # internal: claims TSV on stdin, prints the table and exits with the gate's
  # own verdict (3 = held by another session). This is the decision that makes
  # the convention a lock rather than a note, so it is reachable on its own.
  --report)    shift; report_claims "" "$(cat)"; exit $? ;;
  --clone-of)  shift; [ $# -eq 1 ] || die "--clone-of takes one path"; clone_of_path "$1"; exit 0 ;;
  # internal: prints what detect_branch() resolves for the current checkout
  # (tests use this to drive real worktrees without touching gh/network).
  --detect-branch) shift; detect_branch; exit 0 ;;
  -h|--help)   usage; exit 0 ;;
  "")          usage >&2; exit 2 ;;
esac

while [ $# -gt 0 ]; do
  case "$1" in
    --check)   MODE="check" ;;
    --list)    MODE="list" ;;
    --release) MODE="release"; REASON="${2:-}"; shift ;;
    --dry-run) DRY_RUN=1 ;;
    --force)   FORCE=1 ;;
    --limit)   LIMIT="${2:-100}"; shift ;;
    --repo)    REPO="${2:-$REPO}"; shift ;;
    -h|--help) usage; exit 0 ;;
    -*)        die "unknown flag: $1" ;;
    *)         [ -z "$ISSUE" ] || die "unexpected argument: $1"; ISSUE="$1" ;;
  esac
  shift
done

command -v gh >/dev/null 2>&1 || die "gh CLI not found — https://cli.github.com/"
command -v jq >/dev/null 2>&1 || die "jq not found"

if [ "$MODE" = "list" ]; then
  cmd_list "$LIMIT"
  exit 0
fi

[ -n "$ISSUE" ] || die "which issue? e.g. scripts/claim-issue.sh 1488"
case "$ISSUE" in ''|*[!0-9]*) die "issue must be a number, got «${ISSUE}»";; esac

CLONE="$(detect_clone)"
BRANCH="$(detect_branch)"
STAMP="$(now_stamp)"

case "$MODE" in
  check)
    cmd_check "$ISSUE"
    exit $?
    ;;

  release)
    [ -n "$REASON" ] || die '--release needs a reason: --release "why you stopped"'
    post "$ISSUE" "$(release_body "$CLONE" "$BRANCH" "$STAMP" "$REASON")"
    [ "$DRY_RUN" -eq 1 ] && exit 0
    printf 'released #%s (clone %s, branch %s)\n' "$ISSUE" "$CLONE" "$BRANCH"
    exit 0
    ;;

  claim)
    # Look before you leap — this is the whole point. cmd_check returns 3 when
    # another clone/branch holds it, and sets MINE when this one already does.
    cmd_check "$ISSUE"; rc=$?
    if [ "$MINE" -eq 1 ] && [ "$FORCE" -eq 0 ]; then
      printf 'already claimed by this clone + branch — nothing to do.\n'
      exit 0
    fi
    if [ "$rc" -eq 3 ] && [ "$FORCE" -eq 0 ]; then
      cat >&2 <<EOF
Not claiming #$ISSUE — another session holds it (above).

  Talk to it in the thread, pick another issue, or, if the claim is stale
  and you are taking it over, say so and re-run with --force:

      scripts/claim-issue.sh $ISSUE --force

EOF
      exit 3
    fi
    post "$ISSUE" "$(claim_body "$CLONE" "$BRANCH" "$STAMP")"
    [ "$DRY_RUN" -eq 1 ] && exit 0
    printf 'claimed #%s (clone %s, branch %s, %s)\n' "$ISSUE" "$CLONE" "$BRANCH" "$STAMP"
    exit 0
    ;;
esac
