#!/usr/bin/env bash
# acceptance-artifact-unattended-runs-live.sh — live end-to-end check that a
# scheduled routine reaches its agent's standing artifacts only through the
# agent's own access and only while the schedule's person creator is in good
# standing (#5317 step 5, #5339). Driven by the crewship CLI binary only: no
# database shell, no docker exec.
#
#   scripts/acceptance-artifact-unattended-runs-live.sh [--suspend] [--evidence DIR]
#
# Environment:
#   CREWSHIP_BIN      CLI binary (default: the dev1 binary from
#                     /etc/crewship/environments.json)
#   CREWSHIP_SERVER   server URL (default http://localhost:8081, dev1)
#   CREWSHIP_CONFIG   the operator's CLI config: an instance admin who may
#                     create workspaces, add members and suspend accounts
#                     (default: ~/.config)
#   QA_SETUP_HOOK     command run (as the operator, CREWSHIP_WORKSPACE and
#                     QA_CREW=qa-crew, QA_AGENT=qa-reader set) after the agent
#                     exists, to give it a model provider; a fresh workspace
#                     has none. On dev1, with an Anthropic API key in a file:
#                       QA_SETUP_HOOK='crewship credential create --name qa-anthropic
#                         --type API_KEY --provider ANTHROPIC --crews qa-crew
#                         --value-stdin < "$HOME/.qa-anthropic-key" &&
#                         crewship credential assign qa-anthropic qa-reader
#                         --env-var-name ANTHROPIC_API_KEY'
#                     (a Claude setup-token instead: --type PROVIDER_LOGIN
#                     --provider ANTHROPIC --value-stdin, see
#                     docs/guides/credentials.mdx "Provider logins")
#   FIRE_TIMEOUT      seconds to wait for each fire to finish (default 900)
#
# The schedule's creator is a throwaway account made for this run
# (qa-5356-<stamp>@example.test, created with `workspace member invite
# --create-only`, which refuses any existing account). Its setup link is
# redeemed with one anonymous POST /api/v1/auth/reset, the call the setup page
# makes; there is no CLI command for it. Everything else is the CLI. No shared
# or pre-existing account is ever demoted or suspended (MANAGER_EMAIL is
# refused). At exit the script undoes only what it did: it logs the throwaway
# account out and deletes its CLI config, removes it from the QA workspace,
# suspends it (the API cannot delete accounts) and deletes the QA workspace.
# Evidence logs stay. A failed cleanup step is logged and makes the exit code
# non-zero.
#
# What it shows, from server evidence (audit, journal, run records):
#   A. a MANAGER's schedule fire: the agent reads the artifact it holds its own
#      read grant on (audited as artifact.agent_run_accessed with no user,
#      trigger SCHEDULED and the schedule_id) and not the second artifact
#      (refused like a missing one; nothing audited for it);
#   B. after the agent's grant is revoked, the next fire reads nothing;
#   C. after the creator is demoted to MEMBER, the next fire gets no capability
#      and the journal says artifact.run_capability_withheld with the generic
#      reason schedule_creator_not_in_good_standing and the schedule_id;
#   D. (--suspend) the same after the throwaway creator is suspended.
# The schedule is fired with "run now" by the operator, not the creator: the
# creator decides, the person pressing lends nothing.
#
# The agent's step is a real model turn: it is asked to read both artifacts
# with its artifact tool. If phase A shows neither an audit row nor the
# marker, the agent did not call the tool and the run is INCONCLUSIVE, not a
# pass. run_capability_lost (a sidecar restart mid-run) is not exercised: no
# CLI command restarts only a sidecar during a run.
set -euo pipefail

suspend=0
evidence=""
while [ $# -gt 0 ]; do
  case "$1" in
    --suspend) suspend=1 ;;
    --evidence) evidence="$2"; shift ;;
    -h|--help) sed -n '2,70p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

stamp="$(date -u +%Y%m%d%H%M%S)"
evidence="${evidence:-/mnt/dev-workspace/tmp/builds/live-5356/$stamp}"
mkdir -p "$evidence"
log="$evidence/run.log"
: >"$log"

BIN="${CREWSHIP_BIN:-$(jq -r '.dev1.binary' /etc/crewship/environments.json)}"
export CREWSHIP_SERVER="${CREWSHIP_SERVER:-http://localhost:8081}"
if [ -n "${MANAGER_EMAIL:-}" ]; then
  echo "MANAGER_EMAIL is refused: this script demotes and may suspend its creator, so it only ever uses a throwaway account it creates" >&2
  exit 2
fi
MANAGER_EMAIL="qa-5356-$stamp@example.test"
MANAGER_PASSWORD="$(head -c 24 /dev/urandom | base64 | tr -d '/+=')Aa9!"
FIRE_TIMEOUT="${FIRE_TIMEOUT:-900}"
manager_config="$evidence/manager-cli.json"
results=()

say() { printf '\n== %s\n' "$*" | tee -a "$log"; }
# run LABEL -- CMD...: print the command and its output, keep stdout in $out.
run() {
  printf '\n$ %s\n' "$*" | tee -a "$log"
  set +e
  out="$("$@" 2>"$evidence/stderr")"
  rc=$?
  set -e
  printf '%s\n' "$out" | tee -a "$log"
  if [ -s "$evidence/stderr" ]; then sed 's/^/  stderr: /' "$evidence/stderr" | tee -a "$log"; fi
  return $rc
}
owner() { "$BIN" "$@"; }
manager() { CREWSHIP_CONFIG="$manager_config" "$BIN" "$@"; }
check() { # check NAME OK|FAIL|INCONCLUSIVE DETAIL
  results+=("$2  $1 — $3")
  printf '   -> %s: %s (%s)\n' "$2" "$1" "$3" | tee -a "$log"
}

# What this run created, so the exit trap undoes exactly that.
manager_user_id=""
made_workspace=0
made_account=0
manager_logged_in=0
cleanup_step() { # cleanup_step LABEL CMD...: log it; a failure fails the run.
  printf '\n$ %s\n' "${*:2}" | tee -a "$log"
  if "${@:2}" >>"$log" 2>&1; then return 0; fi
  printf '   -> CLEANUP FAILED: %s\n' "$1" | tee -a "$log"
  cleanup_failed=1
}
cleanup() {
  local rc=$?
  cleanup_failed=0
  set +e
  say "Cleanup (undoes only what this run created)"
  if [ "$manager_logged_in" = 1 ]; then
    cleanup_step "log the throwaway account out" env CREWSHIP_CONFIG="$manager_config" "$BIN" logout
  fi
  if [ -e "$manager_config" ]; then cleanup_step "delete its CLI config" rm -f -- "$manager_config"; fi
  if [ "$made_account" = 1 ]; then
    if [ "$made_workspace" = 1 ] && [ -n "$manager_user_id" ]; then
      cleanup_step "remove it from the QA workspace" "$BIN" workspace member remove "$manager_user_id" --yes
    fi
    # Accounts cannot be deleted through the API; a throwaway one is left
    # suspended (it may already be, after phase D).
    if [ "$suspended" != 1 ]; then
      cleanup_step "suspend the throwaway account" "$BIN" admin instance suspend "$MANAGER_EMAIL" --reason "QA #5356 acceptance finished"
    fi
  fi
  if [ "$made_workspace" = 1 ]; then
    cleanup_step "delete the QA workspace" "$BIN" workspace delete "$ws_slug" --confirm "$ws_slug" --yes
  fi
  if [ "$cleanup_failed" = 1 ]; then
    echo "cleanup failed; see $log" >&2
    [ "$rc" = 0 ] && rc=4
  fi
  exit "$rc"
}
suspended=0
trap cleanup EXIT

say "Preflight"
run owner version
run owner whoami

ws_slug="qa-5356-$stamp"
say "Fresh QA workspace with the flag on"
run owner workspace create --name "QA 5356 $stamp" --slug "$ws_slug" -f json
made_workspace=1
export CREWSHIP_WORKSPACE="$ws_slug"
run owner feature-flag enable artifact_agent_access_modes

say "A throwaway MANAGER account, created for this run only"
printf '\n$ crewship workspace member invite %s --role MANAGER --create-only -f json  (setup link withheld from the log)\n' "$MANAGER_EMAIL" | tee -a "$log"
invite="$(owner workspace member invite "$MANAGER_EMAIL" --role MANAGER --create-only -f json)"
if [ "$(printf '%s' "$invite" | jq -r '.created_user')" != true ]; then
  echo "the server did not create a new account for $MANAGER_EMAIL; refusing to continue" | tee -a "$log"
  exit 2
fi
made_account=1
printf '%s' "$invite" | jq '{email, role, created_user, expires_at}' | tee -a "$log"
setup_token="$(printf '%s' "$invite" | jq -r '.setup_url' | sed -n 's/.*[?&]token=\([^&]*\).*/\1/p')"
[ -n "$setup_token" ] || { echo "the invite carries no setup token" | tee -a "$log"; exit 2; }
printf '\n$ POST %s/api/v1/auth/reset  (anonymous, as the setup page does; no CLI command exists)\n' "$CREWSHIP_SERVER" | tee -a "$log"
jq -n --arg t "$setup_token" --arg p "$MANAGER_PASSWORD" '{token:$t,new_password:$p}' |
  curl -fsS -o /dev/null -w '%{http_code}\n' -H 'Content-Type: application/json' --data-binary @- "$CREWSHIP_SERVER/api/v1/auth/reset" | tee -a "$log"
unset setup_token invite

say "The MANAGER logs in with their own credential"
printf '\n$ crewship login --email %s --password-stdin  (separate CLI config)\n' "$MANAGER_EMAIL" | tee -a "$log"
printf '%s' "$MANAGER_PASSWORD" | CREWSHIP_CONFIG="$manager_config" "$BIN" login --server "$CREWSHIP_SERVER" --email "$MANAGER_EMAIL" --password-stdin 2>&1 | tee -a "$log"
manager_logged_in=1
run manager whoami -f json
run owner workspace member list -f json
manager_user_id="$(printf '%s' "$out" | jq -r --arg e "$MANAGER_EMAIL" '[.[] | select((.email // .user_email // .user.email) == $e)][0].user_id // empty')"
[ -n "$manager_user_id" ] || { echo "cannot find $MANAGER_EMAIL among the workspace members" | tee -a "$log"; exit 2; }

say "Crew and agent"
run manager crew create --name "QA Crew" --slug qa-crew -f json
run manager agent create --name "QA Reader" --slug qa-reader --crew qa-crew --timeout 600 -f json
if [ -n "${QA_SETUP_HOOK:-}" ]; then
  say "Model provider setup hook"
  run env QA_CREW=qa-crew QA_AGENT=qa-reader PATH="$(dirname "$BIN"):$PATH" bash -c "$QA_SETUP_HOOK"
fi

say "Two shared artifacts; the agent holds its own read grant on the first only"
for name in granted denied; do
  marker="MARKER-${name^^}-$stamp"
  jq -n --arg m "$marker" '{entrypoint:"index.html",message:"qa",files:[{path:"index.html",media_type:"text/html",content:("<h1>"+$m+"</h1>")}]}' >"$evidence/$name.json"
  run manager page artifact create "qa-$name-$stamp" --name "QA $name" --file "$evidence/$name.json" -f json
  version="$(printf '%s' "$out" | jq -r '.version // 1')"
  page_id="$(printf '%s' "$out" | jq -r '.page_id // .id')"
  if [ "$name" = granted ]; then page_granted="$page_id"; else page_denied="$page_id"; fi
  run manager page artifact share "qa-$name-$stamp" --base-version "$version" --version "$version"
done
run manager page grant "qa-granted-$stamp" --agent qa-reader --level read
run manager page grants "qa-granted-$stamp" -f json
run manager page grants "qa-denied-$stamp" -f json

say "Routine and schedule, created by the MANAGER"
cat >"$evidence/routine.json" <<EOF
{"dsl_version":"1.0","name":"qa-5356-read","steps":[{"id":"read","type":"agent_run","agent_slug":"qa-reader","prompt":"Use your artifact tool (artifact_versions, or the command: crewship page artifact get <slug>) to read the artifact qa-granted-$stamp and then the artifact qa-denied-$stamp. Do nothing else. For each, reply with one line: the slug, then OK and the text of its heading, or REFUSED and the exact error."}]}
EOF
run manager routine save --definition "$evidence/routine.json" --name "qa-5356-read" --author-crew qa-crew -f json
routine_slug="$(printf '%s' "$out" | jq -r '.slug')"
run manager routine schedules create --slug "$routine_slug" --name qa-nightly --cron "0 3 * * *" --timezone UTC -f json
schedule_id="$(printf '%s' "$out" | jq -r '.id')"
run owner routine schedules list --slug "$routine_slug"
creator="$(owner routine schedules list --slug "$routine_slug" -f json | jq -r --arg id "$schedule_id" '.[] | select(.id==$id) | "\(.creator_kind):\(.creator_user_id)"')"
if [ "$creator" = "human:$manager_user_id" ]; then check "schedule creator recorded" OK "$creator"; else check "schedule creator recorded" FAIL "got $creator, want human:$manager_user_id"; fi

# fire: run now as the operator, wait for the fire's run to finish, print it.
fire() {
  local before
  before="$(owner routine runs-all --limit 200 -f json | jq -r --arg s "$routine_slug" '[.[] | select(.pipeline_slug==$s) | .id] | join(" ")')"
  fire_since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  run owner routine schedules now "$schedule_id" -f json
  local deadline=$((SECONDS + FIRE_TIMEOUT)) id="" status=""
  while [ $SECONDS -lt $deadline ]; do
    sleep 10
    id="$(owner routine runs-all --limit 200 -f json | jq -r --arg s "$routine_slug" --arg b " $before " '[.[] | select(.pipeline_slug==$s and .triggered_via=="schedule" and ($b | contains(" " + .id + " ") | not))][0].id // empty')"
    [ -n "$id" ] || continue
    status="$(owner routine result "$id" -f json | jq -r '.status')"
    case "${status^^}" in RUNNING|PENDING|QUEUED|WAITING|STARTING|"") ;; *) break ;; esac
  done
  if [ -z "$id" ]; then echo "no run for the fire within ${FIRE_TIMEOUT}s" | tee -a "$log"; return 1; fi
  echo "fire run: $id" | tee -a "$log"
  run owner routine result "$id" -f json
  fire_output="$out"
}
# reads_since: audit rows of this schedule's reads since the fire started.
reads_since() {
  owner audit --action artifact.agent_run_accessed --since "$fire_since" -f json |
    jq -c --arg s "$schedule_id" '[.[] | (.metadata | if type=="string" then fromjson else . end) as $m | select($m.schedule_id==$s) | {page:.entity_id, user:.user_email, trigger:$m.trigger, schedule_id:$m.schedule_id, operation:$m.operation, reach:$m.reach, run_id:$m.run_id}]'
}
withheld_since() {
  owner journal --type artifact.run_capability_withheld --since "$fire_since" -f json |
    jq -c --arg s "$schedule_id" '[.[] | (.payload | if type=="string" then fromjson else . end) as $p | select($p.schedule_id==$s) | {reason:$p.reason, schedule_id:$p.schedule_id, pipeline_run_id:$p.pipeline_run_id}]'
}

say "A. The fire reads the granted artifact and not the other"
fire
reads="$(reads_since)"; printf '\n$ crewship audit --action artifact.agent_run_accessed --since %s -f json  (this schedule)\n%s\n' "$fire_since" "$reads" | tee -a "$log"
granted_reads="$(printf '%s' "$reads" | jq --arg p "$page_granted" '[.[] | select(.page==$p and .trigger=="SCHEDULED" and .operation=="read")] | length')"
denied_reads="$(printf '%s' "$reads" | jq --arg p "$page_denied" '[.[] | select(.page==$p)] | length')"
if [ "$granted_reads" -ge 1 ] && [ "$denied_reads" = 0 ]; then
  check "A granted read audited with schedule_id, denied not reached" OK "granted=$granted_reads denied=$denied_reads"
elif [ "$granted_reads" = 0 ] && ! printf '%s' "$fire_output" | grep -q "MARKER-GRANTED-$stamp"; then
  check "A granted read audited with schedule_id, denied not reached" INCONCLUSIVE "no audit row and no marker: the agent did not call its artifact tool"
else
  check "A granted read audited with schedule_id, denied not reached" FAIL "granted=$granted_reads denied=$denied_reads"
fi
if printf '%s' "$fire_output" | grep -q "MARKER-DENIED-$stamp"; then check "A denied artifact content absent from the run" FAIL "marker found"; else check "A denied artifact content absent from the run" OK "no marker"; fi

say "B. The agent's grant is revoked; the next fire reads nothing"
run manager page revoke "qa-granted-$stamp" --agent qa-reader --level read
fire
reads="$(reads_since)"; printf '\n$ crewship audit … (this schedule, since %s)\n%s\n' "$fire_since" "$reads" | tee -a "$log"
if [ "$(printf '%s' "$reads" | jq length)" = 0 ] && ! printf '%s' "$fire_output" | grep -q "MARKER-GRANTED-$stamp"; then check "B revoked grant reads nothing" OK "no audit row, no marker"; else check "B revoked grant reads nothing" FAIL "$reads"; fi
run manager page grant "qa-granted-$stamp" --agent qa-reader --level read

say "C. The creator is demoted to MEMBER; the next fire gets no capability"
run owner workspace member role "$manager_user_id" MEMBER
fire
reads="$(reads_since)"; withheld="$(withheld_since)"
printf '\n$ crewship journal --type artifact.run_capability_withheld --since %s -f json  (this schedule)\n%s\n' "$fire_since" "$withheld" | tee -a "$log"
if [ "$(printf '%s' "$reads" | jq length)" = 0 ] && [ "$(printf '%s' "$withheld" | jq '[.[] | select(.reason=="schedule_creator_not_in_good_standing")] | length')" = 1 ]; then
  check "C demoted creator: withheld once with the generic reason" OK "$withheld"
else
  check "C demoted creator: withheld once with the generic reason" FAIL "reads=$reads withheld=$withheld"
fi
run owner workspace member role "$manager_user_id" MANAGER

if [ "$suspend" = 1 ]; then
  say "D. The creator is suspended; the next fire gets no capability"
  run owner admin instance suspend "$MANAGER_EMAIL" --reason "QA #5356 acceptance"
  suspended=1
  fire
  reads="$(reads_since)"; withheld="$(withheld_since)"
  printf '\n$ crewship journal --type artifact.run_capability_withheld … (this schedule)\n%s\n' "$withheld" | tee -a "$log"
  if [ "$(printf '%s' "$reads" | jq length)" = 0 ] && [ "$(printf '%s' "$withheld" | jq '[.[] | select(.reason=="schedule_creator_not_in_good_standing")] | length')" = 1 ]; then
    check "D suspended creator: withheld with the generic reason" OK "$withheld"
  else
    check "D suspended creator: withheld with the generic reason" FAIL "reads=$reads withheld=$withheld"
  fi
fi

say "Summary (evidence: $evidence)"
printf '%s\n' "${results[@]}" | tee -a "$log" "$evidence/summary.txt"
if printf '%s\n' "${results[@]}" | grep -q '^FAIL'; then exit 1; fi
if printf '%s\n' "${results[@]}" | grep -q '^INCONCLUSIVE'; then exit 3; fi
