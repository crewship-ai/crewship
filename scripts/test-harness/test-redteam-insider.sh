#!/usr/bin/env bash
# shellcheck shell=bash
# LIVE insider red-team — a routine that attacks its own instance from inside a
# crew container (the "self-attacking routine" pattern). Unlike
# test-attack-surface.sh Tier B (documented skips), this one ACTUALLY RUNS the
# attack via a `script` step and asserts what containment should look like.
#
# It is the regression gate for #1368 (network-layer egress fence, still open)
# and #1473 (routine script steps got no HTTP_PROXY, so the crew allowlist never
# bound to them — found by this suite's first live run, fixed 2026-07-26).
#
# The two #1368 assertions are XFAIL while it stays open: marked xfail rather
# than fail so the suite can run in run-all.sh without turning a known-open gap
# into permanent CI red, while staying loud in the summary. When it lands they
# flip to PASS and the markers come out — that transition is the acceptance test.
#
# The #1473 assertion is xfail for a different and temporary reason: the fix is
# in main, but this suite runs against a LIVE server that may be older. It
# reports PASS the moment the target carries the fix. Convert it to a hard
# _fail once dev and stage are both past 1e498fe4, so a genuine regression is
# caught instead of absorbed.
#
# Mechanism: a token-zero `script` step runs redteam-probe.sh inside the crew
# container and emits a JSON report. No WebSocket (survives flaky edges) — the
# result comes back on the run output.
#
# Setup is idempotent: it (re)delivers the probe to the crew's shared dir and
# (re)saves the routine, then runs it. The routine is soft-deleted on exit. The
# probe file is left at /crew/shared/scripts/redteam-probe.sh — `crew files` has
# no delete verb, and the file is inert (it only executes when this routine
# invokes it) and overwritten on every run.
#
# Usage:
#   CREWSHIP=/path/to/crewship CREWSHIP_SERVER=https://crewship-dev3.unifylab.cz \
#     bash test-redteam-insider.sh
# Env: CREWSHIP_REDTEAM_CREW (default: engineering)
#
# Dev slots only. Never point this at prod — it deliberately attempts egress.

set -uo pipefail
_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$_DIR/lib.sh"

CREW="${CREWSHIP_REDTEAM_CREW:-engineering}"
SLUG="redteam-insider-probe"
PROBE="$_DIR/redteam-probe.sh"      # lives beside this harness (committed asset)
DSL="$(mktemp -t redteam-dsl.XXXXXX).json"

# Tear down whatever we created, on every exit path — a crash mid-run must not
# leave an attack routine sitting in a shared dev workspace.
# --yes is load-bearing: without it the CLI prompts on stdin and, in a trap with
# no tty, silently answers "aborted" and exits 0 — the routine survives and the
# cleanup reports success. Verify with `crewship routine list` after a run.
_cleanup() {
  rm -f "$DSL"
  cs routine delete "$SLUG" --yes >/dev/null 2>&1 || true
}
trap _cleanup EXIT

cat > "$DSL" <<JSON
{ "name":"$SLUG","description":"authorized self-attack probe","dsl_version":"1.0",
  "steps":[{"id":"probe","type":"script","script":{"path":"scripts/redteam-probe.sh","interpreter":"bash"}}] }
JSON

section "Preflight — crew egress posture (crew: $CREW)"
# The allowlist assertion below only means something on a RESTRICTED crew. On a
# free-egress crew reaching example.org is correct behaviour, not a finding.
CREW_JSON="$(cs crew get "$CREW" --format json 2>/dev/null)"
NETMODE="$(printf '%s' "$CREW_JSON" | jq -r '.network_mode // "unknown"' 2>/dev/null)"
# Empty is the interesting case: the CLI errored (mismatched token, stale
# workspace, 502) and jq consumed the error object, so treat it exactly like a
# missing crew instead of falling through with NETMODE="" and skipping the
# allowlist assertion for a reason that has nothing to do with egress.
if [[ -z "$NETMODE" || "$NETMODE" == "unknown" ]]; then
  _fail "resolve crew $CREW" \
    "could not read network_mode from '$(printf '%s' "$CREW_JSON" | head -c160)' — check CREWSHIP_PROFILE / CREWSHIP_WORKSPACE for $SERVER"
  finish
fi
info "crew=$CREW network_mode=$NETMODE allowed=$(printf '%s' "$CREW_JSON" | jq -rc '.allowed_domains // []' 2>/dev/null)"

section "Setup — deliver probe + save self-attacking routine"
if [[ ! -f "$PROBE" ]]; then _fail "probe script present" "missing $PROBE"; finish; fi
cs crew files save "$CREW" shared/scripts/redteam-probe.sh --file "$PROBE" >/dev/null 2>&1 \
  && _pass "probe delivered to $CREW:/crew/shared/scripts" || _fail "probe delivery"
cs routine save --name "$SLUG" --description "authorized self-attack probe" \
  --definition "$DSL" --author-crew "$CREW" >/dev/null 2>&1 \
  && _pass "routine saved ($SLUG)" || _fail "routine save"

section "Attack — run the routine (executes inside the crew container)"
OUT="$(cs routine run "$SLUG" 2>/dev/null)"
# The probe emits one flat JSON object; the run output pretty-prints it. Pull the
# first {...} that parses and carries internal_api_code (flat object → no nesting).
J="$(printf '%s' "$OUT" | python3 -c '
import sys,re,json
t=sys.stdin.read()
for s in re.findall(r"\{[^{}]*\}", t, re.S):
    try: o=json.loads(s)
    except Exception: continue
    if "internal_api_code" in o: print(json.dumps(o)); break
' 2>/dev/null)"
if [[ -z "$J" ]]; then _fail "probe returned a report" "no JSON in run output: $(printf '%s' "$OUT" | head -c200)"; finish; fi
info "probe report: $(printf '%s' "$J" | head -c400)"

get() { printf '%s' "$J" | jq -r ".$1 // \"?\"" 2>/dev/null; }
INTERNAL="$(get internal_api_code)"; RAW="$(get raw_egress_code)"
TCP="$(get raw_tcp_443)"; PROXIED="$(get proxied_blocked_code)"
SECRETS="$(get secrets_files)"

section "Assertions — what containment must look like from inside"

# The internal API is REACHABLE from the container by design: the sidecar lives
# in that same network namespace and has to call crewshipd. Reachability is
# therefore not the boundary — authentication is. An unauthenticated probe from
# agent context must be REJECTED; only a 2xx is a finding.
case "$INTERNAL" in
  2*) _fail "internal API accepts an unauthenticated request from agent context" \
        "got HTTP $INTERNAL on /api/v1/internal/agents with no token — a compromised agent reaches the internal surface" ;;
  ""|000|ERR|NO-HTTP-CLIENT) _pass "internal API not reachable from container ($INTERNAL)" ;;
  *)  _pass "internal API rejects an unauthenticated agent-context request (HTTP $INTERNAL)" ;;
esac

# #1364 regression check — no cleartext SECRET files on disk inside the container.
if [[ -z "$SECRETS" || "$SECRETS" == "?" ]]; then _pass "no cleartext secret files under /secrets (#1364 stays fixed)"
  else _fail "cleartext secret files present under /secrets (#1364 regression)" "found: ${SECRETS:0:200}"; fi

# blocked_code <value> — true when a probe result means "nothing was reached".
#
# `case`, not `[[ =~ ]]`: the original patterns carried a trailing empty
# alternative — ^(000|ERR|BLOCKED|)$ — and in bash 3.2 (what macOS ships, and
# what lib.sh's `${arr[@]+...}` idiom already accommodates) that makes the whole
# expression match NOTHING, not even the empty string. Both egress assertions
# were therefore dead: they could never report PASS, so a correctly blocked
# request read as broken. Verified: with the trailing `|`, "000" -> NOMATCH;
# without it, "000" -> MATCH. A regression gate that cannot go green is not a
# gate.
blocked_code() {
  case "$1" in
    ""|000|ERR|BLOCKED|NO-HTTP-CLIENT|403) return 0 ;;
    *) return 1 ;;
  esac
}

# #1368 — raw egress bypassing HTTP_PROXY must die at L3. Open issue → xfail.
if blocked_code "$RAW"; then _pass "raw egress blocked at L3 ($RAW)"
  else xfail "raw egress reaches the internet" \
        "#1368 open — HTTP $RAW to 1.1.1.1 bypassing HTTP_PROXY; egress is app-layer only, there is no network-layer fence yet"; fi
if [[ "$TCP" == "closed" ]]; then _pass "raw TCP:443 to public IP refused"
  else xfail "raw TCP:443 to public IP OPEN" \
        "#1368 open — a raw socket reaches arbitrary hosts"; fi

# A restricted crew's script step must not reach a non-allowlisted host. Only
# assert it where the allowlist actually applies.
#
# This is what the suite found on its first live run (#1473): script steps got
# NO proxy env at all, so the allowlist never bound to them — a plain curl walked
# out. Not a #1368 proxy *bypass*; there was simply no proxy to bypass. Fixed in
# main; a failure here now means the target server predates the fix, or it
# regressed. The probe reports `proxy` so the two failure modes stay
# distinguishable in the output.
if [[ "$NETMODE" != "restricted" ]]; then
  skip "non-allowlisted host blocked" "crew '$CREW' is network_mode=$NETMODE — the allowlist only binds on 'restricted'"
elif blocked_code "$PROXIED"; then
  _pass "non-allowlisted host blocked ($PROXIED)"
else
  _fail "restricted crew reached a non-allowlisted host (#1473 regression)" \
    "HTTP $PROXIED to example.org with proxy=$(get proxy) — the fix is in main, so this is either a server older than 1e498fe4 or a regression"
fi

finish
