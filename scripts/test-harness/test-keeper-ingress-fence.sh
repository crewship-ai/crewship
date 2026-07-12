#!/usr/bin/env bash
# shellcheck shell=bash source=lib.sh
# Keeper ingress fence — the internal keeper HTTP surface is reachable only by
# the sidecar, and only with a valid X-Internal-Token.
#
# WHY THIS SUITE USES raw curl (the ONE documented exception to the
# "everything through the CLI" rule): the target here is the *internal* channel
# `POST /api/v1/internal/keeper/{request,execute,skill-review,...}` — the
# sidecar→crewshipd hop. It has NO CLI command *by design* (agents must never be
# able to drive it directly). You cannot dogfood an attack against a tokenless
# internal endpoint through the sanctioned CLI, so an ingress-fence test is
# necessarily a raw HTTP probe. Every OTHER suite in this dir stays CLI-only.
#
# What it asserts:
#   1. Baseline: an unregistered route returns 404 (so 404 = "gate/not-here").
#   2. The fence HOLDS: internal keeper routes never return 2xx to a caller with
#      no token, a forged token, or a spoofed X-Forwarded-For.
#   3. FINDING probe: whether the network-origin gate (internal.go: non-internal
#      IP → 404) actually fires for an off-host caller, or is defeated by the
#      reverse proxy (Caddy) presenting all traffic as local. A 403 (token
#      reject) instead of 404 (network reject) from the public internet means the
#      ONLY guard on the internal surface is the static shared X-Internal-Token.
#      Recorded as a WARN finding, not a hard fail — it is a deployment property,
#      and the token fence itself still holding (assertions in #2) is what keeps
#      it safe today. See .claude/context/notes/keeper-adversarial-test-suite-*.

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

_WARN=0
warn() { _WARN=$((_WARN+1)); printf '%s  ⚠ WARN%s %s\n' "$_C_YEL" "$_C_OFF" "$*"; }

# http_code <method> <path> [curl-args...] — echo the HTTP status of a request
# to $SERVER<path>. Body is a harmless empty JSON object. 8s cap.
http_code() {
  local method="$1" path="$2"; shift 2
  curl -s -o /dev/null -w '%{http_code}' -X "$method" "$SERVER$path" \
    -H 'Content-Type: application/json' -d '{}' --max-time 8 "$@" 2>/dev/null || echo "000"
}

# assert_not_2xx <name> <code> — the fence must never let a request through.
assert_not_2xx() {
  local name="$1" code="$2"
  if [[ "$code" =~ ^2 ]]; then
    _fail "$name" "internal endpoint returned $code — FENCE BREACH (request was accepted without valid internal auth)"
  else
    _pass "$name (rejected with $code)"
  fi
}

section "Preflight: server reachable"
if [[ "$(http_code GET /healthz)" != "200" ]]; then
  printf '%s  ✗ %s/healthz is not 200 — is the server up?%s\n' "$_C_RED" "$SERVER" "$_C_OFF"
  exit 2
fi
info "healthz OK · target: $SERVER"

INTERNAL_ROUTES=(
  "POST /api/v1/internal/keeper/execute"
  "POST /api/v1/internal/keeper/request/probe-id"
  "POST /api/v1/internal/keeper/skill-review"
  "POST /api/v1/internal/keeper/behavior"
  "POST /api/v1/internal/keeper/memory-health"
  "POST /api/v1/internal/keeper/negative-learning"
)

# ─────────────────────────────────────────────────────────────────────────────
section "1. Baseline — an unregistered route returns 404"
# ─────────────────────────────────────────────────────────────────────────────
bogus_path="/api/v1/internal/does-not-exist-$(nonce X)"
base_code="$(http_code POST "$bogus_path")"
assert_eq "unregistered internal route → 404" "404" "$base_code"

# ─────────────────────────────────────────────────────────────────────────────
section "2. The fence holds — no valid internal auth is ever accepted"
# ─────────────────────────────────────────────────────────────────────────────
for route in "${INTERNAL_ROUTES[@]}"; do
  m="${route%% *}"; p="${route#* }"

  code="$(http_code "$m" "$p")"
  assert_not_2xx "no token: $route" "$code"

  code="$(http_code "$m" "$p" -H 'X-Internal-Token: forged-deadbeef-not-a-real-token')"
  assert_not_2xx "forged token: $route" "$code"

  code="$(http_code "$m" "$p" -H 'X-Internal-Token: 00000000000000000000000000000000')"
  assert_not_2xx "32-hex zero token: $route" "$code"

  code="$(http_code "$m" "$p" -H 'X-Forwarded-For: 203.0.113.7' -H 'X-Real-IP: 203.0.113.7')"
  assert_not_2xx "XFF/Real-IP spoof: $route" "$code"
done

# Method-confusion: GET on a POST-only route must not execute anything.
gm_code="$(http_code GET /api/v1/internal/keeper/execute)"
if [[ "$gm_code" =~ ^2 ]]; then
  _fail "method confusion: GET /keeper/execute" "returned $gm_code"
else
  _pass "method confusion: GET /keeper/execute rejected ($gm_code)"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "3. FINDING probe — is the network-origin gate live behind the proxy?"
# ─────────────────────────────────────────────────────────────────────────────
# internal.go: a non-internal source IP is meant to get 404 (route hidden). From
# an off-host public caller we should therefore see 404 if the gate fires. If we
# instead see 403 (the token-reject code), our public request reached the token
# check — i.e. the network gate did NOT fire and the reverse proxy is presenting
# us as local. The static X-Internal-Token is then the sole guard.
probe="$(http_code POST /api/v1/internal/keeper/execute)"
if [[ "$probe" == "404" ]]; then
  _pass "network-origin gate appears active (off-host caller → 404, route hidden)"
elif [[ "$probe" == "403" ]]; then
  warn "network-origin gate is DEFEATED for this deployment: off-host caller → 403 (token reject), not 404."
  warn "  → The internal keeper surface is internet-reachable; only the static shared X-Internal-Token guards it."
  warn "  → Anyone who reads that token from a container env can replay it off-host and (per keeper_execute.go"
  warn "     trusting body requesting_agent_id/container_id) impersonate any agent + exec in any container."
  warn "  → Tracked as finding C / test T1. Fence still holds (section 2), so this is a hardening gap, not a live breach."
else
  info "network-gate probe returned $probe (neither 404 nor 403) — inspect manually."
fi

printf '\n%s   warnings (findings, non-fatal): %d%s\n' "$_C_YEL" "$_WARN" "$_C_OFF"
finish
