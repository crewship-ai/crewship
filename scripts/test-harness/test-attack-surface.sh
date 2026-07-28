#!/usr/bin/env bash
# shellcheck shell=bash
# Adversarial harness — drives the REAL server as an attacker and asserts the
# security boundaries hold. Companion to the architecture-review issues
# #1364–#1380. Two tiers:
#
#   Tier A — EXTERNAL attacker (no privileged position). Runnable from any
#            machine with a normal user token. Validates the perimeter:
#            auth fence, internal-surface unreachability, cross-workspace
#            isolation.
#
#   Tier B — INSIDER / compromised agent (holds a sidecar/internal token or runs
#            inside an agent container). SKIPPED here, because they need an
#            in-container position; each skip carries the exact command to run
#            FROM AGENT CONTEXT. test-redteam-insider.sh actually executes the
#            #1368 subset via a routine `script` step; see ATTACK-SCENARIOS.md
#            for the rest.
#
# Usage (Tier A live):
#   CREWSHIP_SERVER=https://crewship-dev3.unifylab.cz \
#   CREWSHIP=/path/to/crewship  bash test-attack-surface.sh
#
# Token: $CREWSHIP_ATTACK_TOKEN, else the token of the cli-config profile whose
# `server:` matches $SERVER (NOT blindly `current:` — that profile may point at
# a different slot, which would make A3 fail for the wrong reason). Never
# hard-code a token here.
#
# Why raw curl instead of the CLI: CLAUDE.md requires ops to go through the
# `crewship` CLI, and every other suite here does. This one cannot. Its whole
# job is to send requests the CLI would never construct — a garbage bearer
# token, a spoofed X-Forwarded-For, an unauthenticated POST at
# /api/v1/internal/*. A CLI that could express those would itself be the
# vulnerability. The probes stay read-only and are all expected to be REJECTED.
#
# Read-only / reversible: Tier A only performs probes that are meant to be
# rejected. It creates nothing. Safe against a shared dev slot.

set -uo pipefail
_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$_DIR/lib.sh"

# ── resolve token + workspace ────────────────────────────────────────────────
# Pick the profile whose server matches the target, so the token belongs to the
# instance under attack. Falls back to `current:`; prints nothing on any error
# (the caller then reports "no token" rather than leaking a parse trace).
_profile_token() { # <server-url>
  python3 - "$HOME/.crewship/cli-config.yaml" "$1" <<'PY' 2>/dev/null || true
import sys
try:
    import yaml
except ImportError:
    sys.exit(0)
try:
    with open(sys.argv[1]) as fh:
        cfg = yaml.safe_load(fh) or {}
except OSError:
    sys.exit(0)
want = sys.argv[2].rstrip("/")
servers = cfg.get("servers") or {}
if not isinstance(servers, dict):
    sys.exit(0)
for prof in servers.values():
    if isinstance(prof, dict) and str(prof.get("server", "")).rstrip("/") == want:
        if prof.get("token"):
            print(prof["token"])
            sys.exit(0)
cur = servers.get(cfg.get("current"))
if isinstance(cur, dict) and cur.get("token"):
    print(cur["token"])
PY
}
# CREWSHIP_ATTACK_TOKEN wins; then CREWSHIP_TOKEN, which is what an automated
# runner already has (stage provisions it in its EnvironmentFile); then the
# cli-config profile matching $SERVER. The profile lookup is a developer
# convenience and cannot work unattended: it string-matches the server URL, and
# a CI runner targets a loopback address (http://127.0.0.1:8084) that no profile
# is ever written for.
TOKEN="${CREWSHIP_ATTACK_TOKEN:-${CREWSHIP_TOKEN:-$(_profile_token "$SERVER")}}"
WS="${CREWSHIP_ATTACK_WS:-$(cs workspace list --format json 2>/dev/null | jq -r '.[0].id' 2>/dev/null)}"
# A workspace that EXISTS and the token holder is NOT a member of. There is no
# safe way to discover one (a non-member cannot list it), and guessing is worse
# than skipping: a made-up id answers 404, which is indistinguishable from a
# real isolation failure and would either false-pass or cry wolf. So the
# cross-workspace checks run only when the operator names one.
OTHER_WS="${CREWSHIP_ATTACK_OTHER_WS:-}"
# The PUBLIC entrypoint — the reverse proxy, not the app port. The perimeter
# probes below are meaningless without it; see the B section for why.
#
# Trailing slash stripped once, here. The gauntlet forwards its positional
# public-url arg verbatim, so `https://edge/` is a realistic spelling of the
# same edge, and unnormalised it breaks two things:
#
#   - the "$EDGE == $SERVER" skip below stops matching, so a run pointed at the
#     app port would probe it as if it were the edge — the exact confusion this
#     whole section exists to prevent;
#   - "$base$p" builds `https://edge//api/v1/internal/...`, which curl does send
#     on the wire (verified: `GET //api/v1/internal/credentials`). Our Caddy
#     merges slashes before matching, so its deny still fires — but that is an
#     edge-specific mercy, not a property to depend on. Any edge whose matcher
#     is literal answers 404 for having no such route, and 404 is exactly what
#     B1–B6 assert: a green perimeter while the deny rule was gone.
EDGE="${CREWSHIP_ATTACK_EDGE_URL:-}"
EDGE="${EDGE%/}"

# _is_public_url <url> — true when the host is one requireInternal would treat
# as OUTSIDE. That gate hides /api/v1/internal/* from a non-private source, so
# it is the only definition that matters here: a probe sent from a host the gate
# calls private tells us nothing about the perimeter, whatever the url looks
# like. Loopback is not the question — RFC1918 is. `http://192.168.1.201:8082`
# is not loopback and is still inside, which is why this checks the whole
# private space rather than just 127.0.0.1/localhost.
#
# The ranges mirror internal/api/internal.go's privateNetCIDRs (10/8, 172.16/12,
# 192.168/16, 169.254/16, fc00::/7, fe80::/10) plus the loopback it handles
# separately via IP.IsLoopback. Keep the two in step: if that list grows, a host
# in the new range would be classified public here and B1–B6 would assert 404
# against a surface that answers 403.
#
# This inspects the URL STRING, not a resolved address — it is a "did the
# operator point us at the right layer" check, not an SSRF guard. A public name
# with a private A record still gets probed, and would fail loudly rather than
# silently pass.
#
# Bash 3.2 only (macOS ships it and this harness is run from developer laptops):
# no ${var,,}, no associative arrays.
_is_public_url() {
  local host="${1#*://}"
  host="${host%%/*}"; host="${host%%\?*}"; host="${host##*@}"
  case "$host" in
    \[*\]*) host="${host#\[}"; host="${host%%\]*}" ;;   # [::1]:8080
    *:*)    host="${host%%:*}" ;;
  esac
  host="$(printf '%s' "$host" | /usr/bin/tr '[:upper:]' '[:lower:]')"
  # One address has many spellings, and the literal patterns below only know a
  # couple of them. Fold the rest in first, or `[::ffff:192.168.1.201]` — dev2
  # reached through a v4-mapped literal — reads as public and B1–B6 go at an
  # internal endpoint.
  case "$host" in
    *:*.*.*.*) host="${host##*:}" ;;      # ::ffff:192.168.1.201 → 192.168.1.201
    *:*)
      # ::1 and 0:0:0:0:0:0:0:1 are one address, as are :: and 0:0:…:0. Drop
      # every zero and separator: loopback leaves "1", unspecified leaves
      # nothing, and anything else keeps hex for the prefix cases below. `1::`
      # also strips to "1" and would be called private — a false SKIP, which is
      # the harmless direction: we decline to test rather than test the wrong
      # layer, and it is loud in the summary either way.
      local z="${host//:/}"; z="${z//0/}"
      [[ -z "$z" || "$z" == "1" ]] && return 1
      ;;
  esac
  case "$host" in
    ''|localhost|*.localhost|*.local|*.internal) return 1 ;;
    127.*|0.0.0.0|::1|::|169.254.*) return 1 ;;
    f[cd]??:*|fe[89ab]?:*) return 1 ;;                 # fc00::/7, fe80::/10
    10.*|192.168.*) return 1 ;;
    172.1[6-9].*|172.2[0-9].*|172.3[01].*) return 1 ;;
  esac
  return 0
}

# No edge given, but $SERVER may already BE the edge. The usage example at the
# top of this file is exactly that — `CREWSHIP_SERVER=https://crewship-dev3…`,
# a public url with Caddy in front — and skipping B1–B6 for someone who ran the
# documented invocation means the perimeter goes untested for the audience most
# likely to be reading the output. The gauntlet is the other shape: it drives
# loopback on purpose and names the edge separately.
if [[ -z "$EDGE" ]] && _is_public_url "$SERVER"; then
  EDGE="${SERVER%/}"
fi

# ── http helper: prints the status code, stashes body in $_ATK_BODY ───────────
# Per-run temp file: a fixed /tmp path is both a symlink target on a shared box
# and a collision between two concurrent runs.
_ATK_BODY=""
_ATK_BODY_FILE="$(mktemp -t atk_body.XXXXXX)"
trap 'rm -f "$_ATK_BODY_FILE"' EXIT
# _ATK_BASE lets a probe target something other than the app port — namely the
# public edge. Defaults to $SERVER so every existing call site is unchanged.
_ATK_BASE=""
code() { # method path [curl args...]
  local m="$1" p="$2"; shift 2
  local base="${_ATK_BASE:-$SERVER}"; base="${base%/}"
  # Bounded, because one of these targets is now a remote host over TLS. An edge
  # that accepts the connection and then stalls would hang B1 forever and the
  # suite would never reach its summary — which the gauntlet reports as
  # `blocked`, i.e. no verdict at all, after burning its whole 30-minute cap.
  # A probe that cannot answer in 10s has failed; say so and keep going.
  local c; c=$(/usr/bin/curl -sS --connect-timeout 5 --max-time 10 \
    -o "$_ATK_BODY_FILE" -w '%{http_code}' -X "$m" "$base$p" "$@" 2>/dev/null)
  _ATK_BODY="$(/usr/bin/head -c200 "$_ATK_BODY_FILE" 2>/dev/null | /usr/bin/tr '\n' ' ')"
  printf '%s' "$c"
}
# assert_code <name> <expected-code> <method> <path> [curl args...]
assert_code() {
  local name="$1" want="$2" m="$3" p="$4"; shift 4
  local got; got=$(code "$m" "$p" "$@")
  if [[ "$got" == "$want" ]]; then _pass "$name (HTTP $got)"
  else _fail "$name" "expected HTTP $want, got $got — body: ${_ATK_BODY:0:120}"; fi
}
# assert_code_edge — same as assert_code, but through the PUBLIC edge.
assert_code_edge() {
  local name="$1"; shift
  _ATK_BASE="$EDGE"
  assert_code "$name" "$@"
  _ATK_BASE=""
}
# assert_code_in <name> <regex> <method> <path> ... — accept any of several codes
assert_code_in() {
  local name="$1" re="$2" m="$3" p="$4"; shift 4
  local got; got=$(code "$m" "$p" "$@")
  if [[ "$got" =~ $re ]]; then _pass "$name (HTTP $got)"
  else _fail "$name" "expected HTTP ~$re, got $got — body: ${_ATK_BODY:0:120}"; fi
}

section "Preflight"
# SKIP, not exit 2, when the environment cannot supply a token or workspace.
#
# run-all.sh treats any non-zero exit as a failed suite, and the stage gauntlet
# turns a failed suite into `promotable: false` for the whole e2e leg. A missing
# env var is an environment gap, not a security finding, and it must not be able
# to hold a release. The skip is loud in the summary and names what to set — an
# operator who wanted this suite to run will see that it didn't.
if [[ -z "$TOKEN" || -z "$WS" ]]; then
  missing=""
  [[ -z "$TOKEN" ]] && missing="no token (set CREWSHIP_ATTACK_TOKEN or CREWSHIP_TOKEN)"
  [[ -z "$WS" ]] && missing="${missing:+$missing; }no workspace (set CREWSHIP_ATTACK_WS)"
  skip "Tier A perimeter probes" \
    "$missing — the cli-config profile lookup only resolves for a server URL some profile was written for, so an unattended run against a loopback address must be given both explicitly"
  finish
fi
info "server=$SERVER ws=$WS"
AUTH=(-H "Authorization: Bearer $TOKEN")

# ─────────────────────────────────────────────────────────────────────────────
section "Tier A · Auth fence (external attacker)"
assert_code    "A1 protected route rejects no-auth"       401 GET "/api/v1/crews?workspace_id=$WS"
assert_code    "A2 protected route rejects garbage token" 401 GET "/api/v1/crews?workspace_id=$WS" -H "Authorization: Bearer deadbeef-not-real"
assert_code    "A3 valid token is accepted"               200 GET "/api/v1/crews?workspace_id=$WS" "${AUTH[@]}"
assert_code    "A4 admin route rejects no-auth"           401 GET "/api/v1/admin/stats?workspace_id=$WS"

section "Tier A · Internal surface must be UNREACHABLE from the edge (#audit L0.1)"
# THESE MUST GO THROUGH THE EDGE, and the original version did not — it probed
# $SERVER, the app port on loopback, and asserted 404. It could never pass:
#
#   via Caddy (the edge)          404   ← the deny rule on /api/v1/internal/*
#   app port, from the LAN        403   ← requireInternal sees an RFC1918 source
#   app port, from loopback       403   ← same
#
# requireInternal hides the route from a NON-PRIVATE source. A harness running
# on the box — or anywhere that can reach the box — is on a private LAN by
# definition, so it is inside that gate and will always see 403/405. There is no
# vantage point on this network from which the app port answers 404, which makes
# "assert 404 against $SERVER" unsatisfiable rather than strict.
#
# What actually faces the world is the reverse proxy, and its deny is the
# property worth gating on: delete that Caddy block and the internal keeper and
# credential surface becomes reachable by anything that can reach Caddy. So the
# probes go to $EDGE. Any 2xx here would mean exactly that regression.
#
# The gate on running them is one question, asked once: is the target a host
# requireInternal would treat as OUTSIDE? That is stricter than the "$EDGE ==
# $SERVER" check it replaces, which only caught the operator naming the app url
# verbatim and let `CREWSHIP_ATTACK_EDGE_URL=http://192.168.1.201:8082` — a
# different string, the same wrong side of the fence — through to assert 404
# against a surface that answers 403.
if [[ -z "$EDGE" ]]; then
  skip "B1–B6 internal surface unreachable from the edge" \
    "no public target: \$SERVER ($SERVER) is loopback or RFC1918, and CREWSHIP_ATTACK_EDGE_URL is unset. Set it to the PUBLIC url (e.g. https://crewship-stage.unifylab.cz). Probing the app port directly cannot answer this: every host that can reach it is on a private LAN, which requireInternal treats as internal"
elif ! _is_public_url "$EDGE"; then
  skip "B1–B6 internal surface unreachable from the edge" \
    "CREWSHIP_ATTACK_EDGE_URL=$EDGE is a loopback or RFC1918 address — requireInternal treats every caller from there as internal, so these probes could not tell a working fence from a deleted one"
else
  info "edge=$EDGE"
  assert_code_edge "B1 /internal/credentials unreachable no-token"  404 GET  "/api/v1/internal/credentials?workspace_id=$WS"
  assert_code_edge "B2 /internal/credentials unreachable via userJWT" 404 GET "/api/v1/internal/credentials?workspace_id=$WS" "${AUTH[@]}"
  assert_code_edge "B3 /internal/keeper/request unreachable no-token" 404 POST "/api/v1/internal/keeper/request" -H "Content-Type: application/json" -d '{"agent_slug":"x","credential_id":"y","intent":"probe"}'
  assert_code_edge "B4 /internal/keeper/request unreachable w/ guessed static token" 404 POST "/api/v1/internal/keeper/request" -H "X-Internal-Token: internal-dev-token" -H "Content-Type: application/json" -d '{"agent_slug":"x","credential_id":"y","intent":"probe"}'
  assert_code_edge "B5 /internal/agents unreachable no-token"        404 GET  "/api/v1/internal/agents?workspace_id=$WS"
  # The network-origin gate must not be spoofable by a header an edge proxy would
  # otherwise be trusted to set (#1020). A 2xx/4xx-other here means XFF is honoured.
  assert_code_edge "B6 spoofed X-Forwarded-For does not fake a private origin" 404 GET "/api/v1/internal/agents?workspace_id=$WS" -H "X-Forwarded-For: 127.0.0.1"
fi

section "Tier A · Cross-workspace isolation"
if [[ -z "$OTHER_WS" ]]; then
  skip "C1/C2 cross-workspace isolation" "set CREWSHIP_ATTACK_OTHER_WS to a workspace that EXISTS and this token is NOT a member of — a guessed id answers 404 and would prove nothing"
else
  assert_code  "C1 crews of a non-member workspace → 403" 403 GET "/api/v1/crews?workspace_id=$OTHER_WS" "${AUTH[@]}"
  assert_code  "C2 admin/users of a non-member workspace → 403" 403 GET "/api/v1/admin/users?workspace_id=$OTHER_WS" "${AUTH[@]}"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "Tier B · Insider / compromised-agent attacks (run from AGENT CONTEXT)"
info "These require a sidecar/internal token or in-container position. The ones"
info "marked FIXED are regression checks — they must stay contained. See"
info "ATTACK-SCENARIOS.md; test-redteam-insider.sh runs the #1368 subset live."

skip "T-1364 SECRET file-mount leak [FIXED #1364]" \
  "with Keeper ON and a SECRET cred assigned, /secrets/<slug>/<VAR> must NOT exist inside the container. Check: test -e /secrets/<slug>/<VAR> && echo LEAK"
skip "T-1365 cross-crew issue mutation [FIXED #1365]" \
  "with a crew-A crwv1 sidecar token, PATCH /api/v1/internal/issues/<crewB-issue>/status (and POST .../comment) must be 403"
skip "T-1367 egress bypass via notify/MCP/hook [FIXED #1367]" \
  "from a RESTRICTED crew, trigger a notify webhook / MCP tool / hook at a NON-allowlisted host (https://example.org) — must be blocked"
skip "T-1371 agent-authored routine skips test-gate [FIXED #1371]" \
  "via run_routine/InternalSave, save a routine with forged LastTestRunPassed=true and no dry-run — must land 'proposed'/inactive"
skip "T-1373 credential lease TTL [FIXED #1373]" \
  "capture an L3/L4 credential lease, wait past TTL, reuse — must be refused"
skip "T-1369 journal tamper-evidence [PARTIAL — #1369 open]" \
  "hash-chain landed (#1401, #1450): mutate/delete a journal row then 'crewship admin journal verify' — must report the break. Signed compaction checkpoints still open."
skip "T-1368 raw-socket egress (proxy bypass) [OPEN — #1368]" \
  "from inside an agent container, curl --noproxy '*' https://1.1.1.1 — must be refused at L3. Today app-layer only, so it succeeds. test-redteam-insider.sh asserts this live."
skip "T-1370 fleet swarm (correlation + breaker) [OPEN — #1370]" \
  "N agents each take one benign-looking privileged step — the fleet must correlate and halt"

finish
