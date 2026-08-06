#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Real-forge proof for the link-first Git integration — internal/gitlink,
# internal/api/issue_code_links.go, cmd/crewship/cmd_issue_links.go.
#
# WHY THIS EXISTS
# ---------------
# Every provider payload in the Go suite is a FIXTURE. A fixture pins the shape
# we *believe* GitHub and GitLab return; by construction it cannot notice a
# field that was renamed upstream, an auth error whose body moved, a pagination
# envelope we did not expect, or a rate-limit reply we mishandle. Until
# something drives the fetcher against a real forge, "the parser works" is a
# statement about our own test data and nothing else.
#
# So this suite attaches REAL, PUBLIC, long-lived pull/merge requests to a
# throwaway issue through the real `crewship` CLI — the contract an agent
# actually drives — and asserts the FIELDS the fetcher extracts, not merely
# that the call returned 200:
#
#   * a MERGED object reads as MERGED. On GitHub that is the one case a
#     naive parser gets wrong: a merged PR reports `"state": "closed"` and is
#     only distinguishable by a non-null `merged_at` (gitlink.githubState).
#   * a CLOSED-not-merged object reads as CLOSED, from the same repository, so
#     the two are told apart by real data rather than by two fixtures.
#     The GitLab default here is deliberately an MR whose `closed_at` is NULL
#     upstream (gitlab-org/gitlab!8) — proof the state comes from `state` and
#     not from the presence of a timestamp.
#   * author, source branch and target branch carry through. These are
#     `user.login` / `head.ref` / `base.ref` on GitHub and
#     `author.username` / `source_branch` / `target_branch` on GitLab — the
#     exact keys a rename would break, and the ones the handler stores.
#   * the failure paths fixtures cannot honestly simulate: a repository that
#     does not exist (real 404) and a credential the provider rejects (real
#     401/403). Both are asserted on Crewship's OWN error surface — the RFC
#     7807 `type` URI and `code` member — because issue_code_links.go says in
#     as many words that the status codes collide across three different
#     failures and the type URI is what a client keys off.
#
# WHAT IT DOES *NOT* TOUCH
# ------------------------
# The forge is read-only: `GET` on public objects, nothing is created, edited
# or commented anywhere upstream. Everything this suite writes is in Crewship
# — one issue and up to two credentials per provider, all named with a nonce
# and removed on exit (including on Ctrl-C).
#
# ENABLE IT
# ---------
#   GITLINK_GITHUB_TOKEN   GitHub PAT, read-only. `public_repo` (classic) or a
#                          fine-grained token with no repo access at all is
#                          enough — the default objects are public.
#   GITLINK_GITLAB_TOKEN   gitlab.com PAT with `read_api`.
#
# With NEITHER set the whole file SKIPs and exits 0 without contacting the
# server or any forge — that is the CI-safe path, and CI has no forge secret.
# Each provider block skips independently on its own missing token.
#
# PRECONDITION THE CLI CANNOT CHECK (finding, see README)
# -------------------------------------------------------
# Credential resolution matches on `account_label` first (oldest ACTIVE row
# wins), so this suite labels the credential it creates with the forge host.
# If the workspace ALREADY holds an older ACTIVE credential for that provider
# labelled with the same host, that one wins and the good-path block will fail
# with `credential-rejected` through no fault of the fetcher. `crewship
# credential list/get` do not expose `account_label`, so this cannot be
# detected from the CLI — the good-path failure message says so instead.
#
# Overrides: GITLINK_CREW, GITLINK_GITHUB_MERGED_URL, GITLINK_GITHUB_CLOSED_URL,
# GITLINK_GITHUB_MISSING_URL and the GITLINK_GITLAB_* equivalents. Pointing a
# URL somewhere else RELAXES its field assertions to "non-empty": the pinned
# author/branch values below belong to the built-in objects.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

# ── The objects under test ──────────────────────────────────────────────────
#
# PROVENANCE — every value below was read from the live provider API on
# 2026-08-06, unauthenticated, and is immutable for a merged/closed object:
#
#   GET https://api.github.com/repos/cli/cli/pulls/1     merged 2019-10-04
#   GET https://api.github.com/repos/cli/cli/pulls/400   closed 2020-02-17, merged_at null
#   GET https://gitlab.com/api/v4/projects/gitlab-org%2Fgitlab/merge_requests/200000   merged
#   GET https://gitlab.com/api/v4/projects/gitlab-org%2Fgitlab/merge_requests/8        closed, closed_at null
#
# Titles are NOT pinned: a maintainer may edit one, and a red suite for that
# would be a false alarm. Emptiness is what a renamed field produces, so the
# title is asserted non-empty and printed.
GH_MERGED_DEFAULT="https://github.com/cli/cli/pull/1"
GH_CLOSED_DEFAULT="https://github.com/cli/cli/pull/400"
GL_MERGED_DEFAULT="https://gitlab.com/gitlab-org/gitlab/-/merge_requests/200000"
GL_CLOSED_DEFAULT="https://gitlab.com/gitlab-org/gitlab/-/merge_requests/8"

GH_MERGED_URL="${GITLINK_GITHUB_MERGED_URL:-$GH_MERGED_DEFAULT}"
GH_CLOSED_URL="${GITLINK_GITHUB_CLOSED_URL:-$GH_CLOSED_DEFAULT}"
GL_MERGED_URL="${GITLINK_GITLAB_MERGED_URL:-$GL_MERGED_DEFAULT}"
GL_CLOSED_URL="${GITLINK_GITLAB_CLOSED_URL:-$GL_CLOSED_DEFAULT}"

# A repository that does not exist, under an owner that does. GitHub and
# GitLab both answer 404 here whether or not the credential is valid.
GH_MISSING_URL="${GITLINK_GITHUB_MISSING_URL:-https://github.com/cli/gitlink-harness-no-such-repo/pull/1}"
GL_MISSING_URL="${GITLINK_GITLAB_MISSING_URL:-https://gitlab.com/gitlab-org/gitlink-harness-no-such-project/-/merge_requests/1}"

# The RFC 7807 namespace from issue_code_links.go. Hard-coded on purpose: this
# is the contract a client keys off, so a change to it must break a test.
PROBLEM_BASE="https://crewship.ai/problems/code-link/"

# A token that is syntactically plausible and certainly invalid. Never a real
# secret, and never written anywhere but this workspace's own vault, from
# which it is deleted at the end of the block.
BAD_TOKEN="gitlink-harness-invalid-$(nonce T)"

NONCE="$(nonce GL)"
ISSUE=""
CREATED_CREDS=""

# ── Cleanup ─────────────────────────────────────────────────────────────────
# Runs on every exit path including finish() and Ctrl-C. Failures here are
# reported, not fatal: a leftover row must never mask the suite's verdict.
# Idempotent: an interrupt runs it and then the EXIT trap runs it again, so it
# empties what it has cleaned rather than trying twice and reporting the second
# (correct) failure as a problem.
# shellcheck disable=SC2329  # invoked from the EXIT/INT/TERM traps installed below
cleanup() {
  local rc=$? c
  # shellcheck disable=SC2086  # word splitting is the point: a space-separated list
  for c in $CREATED_CREDS; do
    cs credential delete "$c" --yes >/dev/null 2>&1 \
      || info "cleanup: could not delete credential $c — remove it by hand"
  done
  CREATED_CREDS=""
  if [[ -n "$ISSUE" ]]; then
    cs issue delete "$ISSUE" --yes >/dev/null 2>&1 \
      || info "cleanup: could not delete issue $ISSUE — remove it by hand"
    ISSUE=""
  fi
  return "$rc"
}

# ── Helpers ─────────────────────────────────────────────────────────────────

# problem_json <file> — the structured error envelope the CLI writes to stderr
# under `--format json`, with any preceding warning lines stripped. Emitting
# from the first `{` is what keeps a version-skew notice from breaking the
# parse.
problem_json() { awk '/^\{/{seen=1} seen' "$1"; }

# problem_detail <file> — the one-line human reason out of that envelope, for
# a failure message. Falls back to the raw first bytes when it is not JSON.
problem_detail() {
  local body detail
  body="$(problem_json "$1")"
  if [[ -n "$body" ]]; then
    detail="$(printf '%s' "$body" | jq -r '"\(.error.extensions.code // "?"): \(.error.detail // .error.message // "")"' 2>/dev/null)"
    [[ -n "$detail" ]] && { printf '%s' "$detail" | head -c 240; return; }
  fi
  head -c 240 "$1" | tr '\n' ' '
}

# assert_problem <name> <stderr-file> <expected-codes> [<expected-status>]
# Asserts Crewship's own error surface: that `code` is one of the pipe-
# separated expectations, that `type` is the namespaced URI for THAT code (not
# merely present), and — when given — the HTTP status. The status alone is
# deliberately not sufficient: three different remedies share 502.
assert_problem() {
  local name="$1" errf="$2" want_codes="$3" want_status="${4:-}"
  local body code type status
  body="$(problem_json "$errf")"
  if [[ -z "$body" ]]; then
    _fail "$name" "no JSON error envelope on stderr: $(head -c 200 "$errf" | tr '\n' ' ')"
    return 1
  fi
  code="$(printf '%s' "$body" | jq -r '.error.extensions.code // empty')"
  type="$(printf '%s' "$body" | jq -r '.error.extensions.type // empty')"
  status="$(printf '%s' "$body" | jq -r '.error.status // empty')"

  case "|$want_codes|" in
    *"|$code|"*)
      _pass "$name — code=$code"
      ;;
    *)
      _fail "$name" "code=«$code» status=«$status», expected one of «$want_codes»; detail: $(printf '%s' "$body" | jq -r '.error.detail // empty' | head -c 200)"
      return 1
      ;;
  esac
  assert_eq "$name · RFC 7807 type URI is namespaced" "${PROBLEM_BASE}${code}" "$type"
  if [[ -n "$want_status" ]]; then
    assert_eq "$name · HTTP status" "$want_status" "$status"
  fi
  return 0
}

# link_json <url> — the stored link whose canonical URL is <url>, as one JSON
# object; empty when the issue carries no such link. Goes through
# `crewship issue links`, i.e. the same read path an agent uses.
link_json() {
  cs issue links "$ISSUE" --format json 2>/dev/null \
    | jq -c --arg u "$1" '(if type=="array" then . else [] end) | map(select(.url == $u)) | .[0] // empty'
}

# field <json> <key> — a scalar field, with JSON null flattened to "".
field() { printf '%s' "$1" | jq -r --arg k "$2" '.[$k] // empty'; }

# assert_parsed <label> <json> <state> <author> <source> <target> <pinned>
# The core assertion: the values gitlink.decode pulled out of a REAL payload.
# <pinned> = 1 asserts the exact upstream values; 0 (a caller-supplied URL)
# only asserts they are non-empty, which is still what a renamed field breaks.
assert_parsed() {
  local label="$1" json="$2" state="$3" author="$4" src="$5" tgt="$6" pinned="$7"
  assert_eq "$label · state" "$state" "$(field "$json" state)"
  if [[ "$pinned" == "1" ]]; then
    assert_eq "$label · author (user.login / author.username)" "$author" "$(field "$json" author)"
    assert_eq "$label · source branch (head.ref / source_branch)" "$src" "$(field "$json" source_branch)"
    assert_eq "$label · target branch (base.ref / target_branch)" "$tgt" "$(field "$json" target_branch)"
  else
    assert_nonempty "$label · author (user.login / author.username)" "$(field "$json" author)"
    assert_nonempty "$label · source branch (head.ref / source_branch)" "$(field "$json" source_branch)"
    assert_nonempty "$label · target branch (base.ref / target_branch)" "$(field "$json" target_branch)"
  fi
  assert_nonempty "$label · title" "$(field "$json" title)"
  info "title: $(field "$json" title)"
  assert_nonempty "$label · last_synced_at" "$(field "$json" last_synced_at)"
  assert_eq "$label · no last_sync_error" "" "$(field "$json" last_sync_error)"
}

# ── 0. Configuration ────────────────────────────────────────────────────────

section "0. Configuration"

if [[ -z "${GITLINK_GITHUB_TOKEN:-}" && -z "${GITLINK_GITLAB_TOKEN:-}" ]]; then
  skip "the whole real-forge gitlink suite" "no forge credential configured"
  info "Set GITLINK_GITHUB_TOKEN (a read-only GitHub PAT) and/or GITLINK_GITLAB_TOKEN"
  info "(a gitlab.com PAT with read_api) to enable it. Each provider is independent."
  info "Nothing was contacted: no Crewship call, no forge call. This is the path CI takes."
  finish
fi

if ! have jq; then
  skip "the whole real-forge gitlink suite" "jq is required to assert on parsed fields"
  finish
fi

# The resolved CLI has to be new enough to have the surface under test. Both
# gaps are silent otherwise: an old `credential create` rejects
# --account-label with "unknown flag" (which reads as a broken harness), and an
# old `issue` has no `link` subcommand at all. Checked against --help rather
# than a version string, because what matters is the flag being there.
if ! "$CREWSHIP" credential create --help 2>&1 | grep -q -- '--account-label'; then
  skip "the whole real-forge gitlink suite" \
    "$CREWSHIP has no 'credential create --account-label' — build the CLI from a tree that has #1758"
  finish
fi
if ! "$CREWSHIP" issue --help 2>&1 | grep -qE '^[[:space:]]+link[[:space:]]'; then
  skip "the whole real-forge gitlink suite" \
    "$CREWSHIP has no 'issue link' command — build the CLI from a tree that has #1758"
  finish
fi

preflight
trap cleanup EXIT
trap 'cleanup; exit 130' INT TERM

CREW="${GITLINK_CREW:-$(cs crew list --format json 2>/dev/null | jq -r '(if type=="array" then . else [] end) | .[0].slug // empty')}"
if [[ -z "$CREW" ]]; then
  skip "the whole real-forge gitlink suite" "no crew in this workspace to hang an issue off"
  finish
fi
info "crew: $CREW"

ISSUE="$(cs issue create --crew "$CREW" --title "gitlink real-forge harness $NONCE" 2>&1 \
  | grep -oE '[A-Z][A-Z0-9]*-[0-9]+' | head -n1)"
if [[ -z "$ISSUE" ]]; then
  _fail "create the throwaway issue" "crewship issue create --crew $CREW produced no identifier"
  finish
fi
_pass "created throwaway issue $ISSUE on crew $CREW"

# ── The per-provider block ──────────────────────────────────────────────────
#
# One implementation, two configurations — because GitHub and GitLab are the
# same contract here (parse the URL, resolve a credential by host, fetch,
# store). Inputs are globals rather than a dozen positional parameters:
#
#   P_NAME P_PROVIDER P_HOST P_TOKEN
#   P_MERGED_URL P_MERGED_PINNED P_MERGED_AUTHOR P_MERGED_SRC P_MERGED_TGT
#   P_CLOSED_URL P_CLOSED_PINNED P_CLOSED_AUTHOR P_CLOSED_SRC P_CLOSED_TGT
#   P_MISSING_URL P_OWNER P_REPO P_NUMBER
run_provider() {
  local bad_cred="gitlink-harness-bad-${P_NAME}-${NONCE}"
  local good_cred="gitlink-harness-${P_NAME}-${NONCE}"
  local errf link id synced1 synced2

  errf="$(mktemp -t gitlink-err.XXXXXX)"

  # ── 1. A credential the provider REJECTS ────────────────────────────────
  # The one failure a fixture cannot honestly simulate: the real forge
  # deciding our token is no good. Asserted on the error surface, because the
  # user-visible remedy ("rotate the credential") is carried by the code, not
  # by the 502.
  section "$P_NAME · 1. a credential the real provider rejects"
  if printf '%s' "$BAD_TOKEN" | cs credential create --name "$bad_cred" --type API_KEY \
      --provider "$P_PROVIDER" --account-label "$P_HOST" --value-stdin >/dev/null 2>&1; then
    CREATED_CREDS="$CREATED_CREDS $bad_cred"
    if cs --format json issue link "$ISSUE" "$P_MERGED_URL" >/dev/null 2>"$errf"; then
      _fail "$P_NAME attach with an invalid credential is refused" \
        "the attach SUCCEEDED — an older credential labelled $P_HOST probably won resolution"
      # It stored a link this block did not intend to create; drop it so
      # section 2 attaches cleanly rather than hitting `already-linked`.
      id="$(field "$(link_json "$P_MERGED_URL")" id)"
      [[ -n "$id" ]] && cs issue unlink "$ISSUE" "$id" >/dev/null 2>&1
    else
      # 401 → credential-rejected; 403 (wrong scope, or a fine-grained token
      # with no access) → credential-forbidden. Both are correct answers to
      # "this credential cannot read that repository"; which one you get is
      # the provider's choice, not ours.
      assert_problem "$P_NAME invalid credential → provider rejection" "$errf" \
        "credential-rejected|credential-forbidden" "502"
    fi
    cs credential delete "$bad_cred" --yes >/dev/null 2>&1 \
      && CREATED_CREDS="${CREATED_CREDS/ $bad_cred/}"
  else
    skip "$P_NAME invalid credential → provider rejection" "could not create the throwaway credential"
  fi

  # ── 2. The real credential, the real merged object ──────────────────────
  section "$P_NAME · 2. a MERGED object parses"
  if ! printf '%s' "$P_TOKEN" | cs credential create --name "$good_cred" --type API_KEY \
      --provider "$P_PROVIDER" --account-label "$P_HOST" --value-stdin >/dev/null 2>&1; then
    _fail "$P_NAME credential create" "could not store the supplied token"
    rm -f "$errf"
    return 1
  fi
  CREATED_CREDS="$CREATED_CREDS $good_cred"

  if cs --format json issue link "$ISSUE" "$P_MERGED_URL" >/dev/null 2>"$errf"; then
    _pass "$P_NAME attach $P_MERGED_URL"
  else
    local why
    why="$(problem_detail "$errf")"
    _fail "$P_NAME attach $P_MERGED_URL" \
      "$why — if that is credential-rejected, check whether an OLDER ACTIVE $P_PROVIDER credential labelled $P_HOST already exists; the CLI cannot read account_label back"
    rm -f "$errf"
    return 1
  fi

  link="$(link_json "$P_MERGED_URL")"
  if [[ -z "$link" ]]; then
    _fail "$P_NAME the attached link is readable back" "no link with url=$P_MERGED_URL"
    rm -f "$errf"
    return 1
  fi
  assert_eq "$P_NAME · provider recognised from the URL grammar" "$P_PROVIDER" "$(field "$link" provider)"
  assert_eq "$P_NAME · host"   "$P_HOST"   "$(field "$link" host)"
  assert_eq "$P_NAME · owner"  "$P_OWNER"  "$(field "$link" owner)"
  assert_eq "$P_NAME · repo"   "$P_REPO"   "$(field "$link" repo)"
  assert_eq "$P_NAME · number" "$P_NUMBER" "$(field "$link" number)"
  assert_parsed "$P_NAME merged" "$link" "MERGED" \
    "$P_MERGED_AUTHOR" "$P_MERGED_SRC" "$P_MERGED_TGT" "$P_MERGED_PINNED"

  # ── 3. Merged is not the same as closed ─────────────────────────────────
  # Same repository, an object that was closed WITHOUT being merged. On
  # GitHub both report `"state": "closed"` and only `merged_at` separates
  # them; on GitLab the default object's `closed_at` is null, so CLOSED here
  # can only have come from the `state` field.
  section "$P_NAME · 3. a CLOSED-not-merged object is CLOSED, not MERGED"
  if cs --format json issue link "$ISSUE" "$P_CLOSED_URL" >/dev/null 2>"$errf"; then
    link="$(link_json "$P_CLOSED_URL")"
    if [[ -z "$link" ]]; then
      _fail "$P_NAME the closed link is readable back" "no link with url=$P_CLOSED_URL"
    else
      assert_parsed "$P_NAME closed" "$link" "CLOSED" \
        "$P_CLOSED_AUTHOR" "$P_CLOSED_SRC" "$P_CLOSED_TGT" "$P_CLOSED_PINNED"
    fi
  else
    _fail "$P_NAME attach $P_CLOSED_URL" "$(problem_detail "$errf")"
  fi

  # ── 4. Refresh re-reads the provider ────────────────────────────────────
  section "$P_NAME · 4. refresh re-reads the provider"
  link="$(link_json "$P_MERGED_URL")"
  id="$(field "$link" id)"
  synced1="$(field "$link" last_synced_at)"
  if [[ -z "$id" ]]; then
    skip "$P_NAME relink" "no link id to refresh"
  elif cs --format json issue relink "$ISSUE" "$id" >/dev/null 2>"$errf"; then
    link="$(link_json "$P_MERGED_URL")"
    synced2="$(field "$link" last_synced_at)"
    assert_eq "$P_NAME · state survives a refresh" "MERGED" "$(field "$link" state)"
    assert_eq "$P_NAME · refresh clears last_sync_error" "" "$(field "$link" last_sync_error)"
    assert_nonempty "$P_NAME · refresh stamps last_synced_at" "$synced2"
    if [[ "$synced1" == "$synced2" ]]; then
      # Second-precision timestamps: two refreshes inside one second are
      # equal. Report it rather than failing on a clock granularity.
      info "last_synced_at unchanged ($synced2) — same-second refresh, not a defect"
    else
      _pass "$P_NAME · refresh moved last_synced_at ($synced1 → $synced2)"
    fi
  else
    _fail "$P_NAME relink" "$(problem_detail "$errf")"
  fi

  # ── 5. A repository that does not exist ─────────────────────────────────
  # A real 404 from the real provider, with a VALID credential — the case
  # that separates "no such pull request" from "your token was rejected",
  # which is exactly what a fixture cannot prove.
  section "$P_NAME · 5. a repository that does not exist → 404"
  if cs --format json issue link "$ISSUE" "$P_MISSING_URL" >/dev/null 2>"$errf"; then
    _fail "$P_NAME missing repository is a 404" "the attach SUCCEEDED against $P_MISSING_URL"
  else
    assert_problem "$P_NAME missing repository → not-found" "$errf" "pull-request-not-found" "404"
  fi

  rm -f "$errf"
}

# ── GitHub ──────────────────────────────────────────────────────────────────

section "GitHub"
if [[ -z "${GITLINK_GITHUB_TOKEN:-}" ]]; then
  skip "the GitHub block" "set GITLINK_GITHUB_TOKEN (a read-only GitHub PAT) to enable it"
else
  P_NAME="github"
  P_PROVIDER="GITHUB"
  P_HOST="github.com"
  P_TOKEN="$GITLINK_GITHUB_TOKEN"
  P_MERGED_URL="$GH_MERGED_URL"
  P_CLOSED_URL="$GH_CLOSED_URL"
  P_MISSING_URL="$GH_MISSING_URL"
  P_OWNER="cli"; P_REPO="cli"; P_NUMBER="1"
  P_MERGED_AUTHOR="vilmibm"; P_MERGED_SRC="gh-pr"; P_MERGED_TGT="prototype"
  P_CLOSED_AUTHOR="issyl0"; P_CLOSED_SRC="readme-homebrew-core-tap"; P_CLOSED_TGT="master"
  P_MERGED_PINNED=0; P_CLOSED_PINNED=0
  [[ "$P_MERGED_URL" == "$GH_MERGED_DEFAULT" ]] && P_MERGED_PINNED=1
  [[ "$P_CLOSED_URL" == "$GH_CLOSED_DEFAULT" ]] && P_CLOSED_PINNED=1
  if [[ "$P_MERGED_PINNED" != "1" ]]; then
    # owner/repo/number come from the built-in object; a caller-supplied URL
    # invalidates them, so read them back from the parse instead of pinning.
    P_OWNER="$(printf '%s' "$P_MERGED_URL" | awk -F/ '{print $4}')"
    P_REPO="$(printf '%s' "$P_MERGED_URL" | awk -F/ '{print $5}')"
    P_NUMBER="$(printf '%s' "$P_MERGED_URL" | awk -F/ '{print $7}')"
    P_HOST="$(printf '%s' "$P_MERGED_URL" | awk -F/ '{print $3}')"
  fi
  run_provider
fi

# ── GitLab ──────────────────────────────────────────────────────────────────

section "GitLab"
if [[ -z "${GITLINK_GITLAB_TOKEN:-}" ]]; then
  skip "the GitLab block" "set GITLINK_GITLAB_TOKEN (a gitlab.com PAT with read_api) to enable it"
else
  P_NAME="gitlab"
  P_PROVIDER="GITLAB"
  P_HOST="gitlab.com"
  P_TOKEN="$GITLINK_GITLAB_TOKEN"
  P_MERGED_URL="$GL_MERGED_URL"
  P_CLOSED_URL="$GL_CLOSED_URL"
  P_MISSING_URL="$GL_MISSING_URL"
  P_OWNER="gitlab-org"; P_REPO="gitlab"; P_NUMBER="200000"
  P_MERGED_AUTHOR="julie_huang"
  P_MERGED_SRC="jh/add-top-margin-to-exclusion-settings"
  P_MERGED_TGT="master"
  P_CLOSED_AUTHOR="DouweM"; P_CLOSED_SRC="max-file-size-git-hook"; P_CLOSED_TGT="master"
  P_MERGED_PINNED=0; P_CLOSED_PINNED=0
  [[ "$P_MERGED_URL" == "$GL_MERGED_DEFAULT" ]] && P_MERGED_PINNED=1
  [[ "$P_CLOSED_URL" == "$GL_CLOSED_DEFAULT" ]] && P_CLOSED_PINNED=1
  if [[ "$P_MERGED_PINNED" != "1" ]]; then
    P_HOST="$(printf '%s' "$P_MERGED_URL" | awk -F/ '{print $3}')"
    P_OWNER="$(printf '%s' "$P_MERGED_URL" | awk -F/ '{print $4}')"
    P_REPO="$(printf '%s' "$P_MERGED_URL" | awk -F/ '{print $5}')"
    P_NUMBER="$(printf '%s' "$P_MERGED_URL" | awk -F/ '{print $NF}')"
  fi
  run_provider
fi

finish
