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
# Sections:
#   1. Baseline — an unregistered route returns 404 (so 404 = "gate/not-here").
#   2. The fence HOLDS — no token / forged / zero / spoofed XFF is ever 2xx.
#   3. HTTP method matrix — no verb (GET/PUT/DELETE/PATCH/OPTIONS) executes.
#   4. Malformed-token fuzz — empty, whitespace, 8 KB, newline/unicode, SQL-ish.
#   5. Oversized body — a giant payload is rejected, not accepted.
#   6. No info leak — the rejection body carries no stack trace / token echo.
#   7. The fence isn't keeper-specific — other /internal/* routes reject too.
#   8. Public API still needs auth (regression: not everything is open).
#   9. Constant-time probe — token compare shouldn't leak length via timing.
#  10. FINDING probe — is the network-origin gate live behind the proxy?

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

_WARN=0
warn() { _WARN=$((_WARN+1)); printf '%s  ⚠ WARN%s %s\n' "$_C_YEL" "$_C_OFF" "$*"; }

# http_code <method> <path> [curl-args...] — HTTP status of a request to
# $SERVER<path>. Empty JSON body. 8s cap. 000 on transport failure.
http_code() {
  local method="$1" path="$2"; shift 2
  curl -s -o /dev/null -w '%{http_code}' -X "$method" "$SERVER$path" \
    -H 'Content-Type: application/json' -d '{}' --max-time 8 "$@" 2>/dev/null || echo "000"
}

# http_body <method> <path> [curl-args...] — response body (first 2 KB).
http_body() {
  local method="$1" path="$2"; shift 2
  curl -s "$SERVER$path" -X "$method" -H 'Content-Type: application/json' \
    -d '{}' --max-time 8 "$@" 2>/dev/null | head -c 2048
}

# assert_not_2xx <name> <code> — the fence must never let a request through.
assert_not_2xx() {
  local name="$1" code="$2"
  if [[ "$code" =~ ^2 ]]; then
    _fail "$name" "internal endpoint returned $code — FENCE BREACH (accepted without valid internal auth)"
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
assert_eq "unregistered internal route → 404" "404" "$(http_code POST "$bogus_path")"

# ─────────────────────────────────────────────────────────────────────────────
section "2. The fence holds — no valid internal auth is ever accepted"
# ─────────────────────────────────────────────────────────────────────────────
for route in "${INTERNAL_ROUTES[@]}"; do
  m="${route%% *}"; p="${route#* }"
  assert_not_2xx "no token: $route"        "$(http_code "$m" "$p")"
  assert_not_2xx "forged token: $route"    "$(http_code "$m" "$p" -H 'X-Internal-Token: forged-deadbeef-not-a-real-token')"
  assert_not_2xx "32-hex zero token: $route" "$(http_code "$m" "$p" -H 'X-Internal-Token: 00000000000000000000000000000000')"
  assert_not_2xx "XFF/Real-IP spoof: $route" "$(http_code "$m" "$p" -H 'X-Forwarded-For: 203.0.113.7' -H 'X-Real-IP: 203.0.113.7')"
done

# ─────────────────────────────────────────────────────────────────────────────
section "3. HTTP method matrix — no verb executes on the internal surface"
# ─────────────────────────────────────────────────────────────────────────────
for m in GET PUT DELETE PATCH OPTIONS; do
  assert_not_2xx "method $m /keeper/execute" "$(http_code "$m" /api/v1/internal/keeper/execute)"
done

# ─────────────────────────────────────────────────────────────────────────────
section "4. Malformed-token fuzz — every shape is rejected, never 2xx"
# ─────────────────────────────────────────────────────────────────────────────
long_tok="$(printf 'A%.0s' $(seq 1 8192))"
declare -a FUZZ_TOKENS=(
  ""                                             # empty header value
  "   "                                          # whitespace only
  "$long_tok"                                    # 8 KB token (buffer/DoS probe)
  "tok'; DROP TABLE keeper_requests;--"          # SQL-ish
  "tok\$(id)"                                     # shell-ish
  "../../etc/passwd"                             # path-ish
)
i=0
for tk in "${FUZZ_TOKENS[@]}"; do
  i=$((i+1))
  assert_not_2xx "malformed token #$i: /keeper/execute" \
    "$(http_code POST /api/v1/internal/keeper/execute -H "X-Internal-Token: $tk")"
done
# A header with an embedded newline is rejected by curl itself (good) or by the
# server; either way it must not yield a 2xx.
nl_code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$SERVER/api/v1/internal/keeper/execute" \
  -H "X-Internal-Token: a$(printf '\r\n')X-Injected: 1" -d '{}' --max-time 8 2>/dev/null || echo "000")"
if [[ "$nl_code" =~ ^2 ]]; then _fail "CRLF-injected token header" "returned $nl_code"; else _pass "CRLF-injected token header rejected ($nl_code)"; fi

# ─────────────────────────────────────────────────────────────────────────────
section "5. Oversized body — a giant payload is rejected, not accepted"
# ─────────────────────────────────────────────────────────────────────────────
big="$(mktemp -t cs-big.XXXXXX)"; { printf '{"intent":"'; head -c 2000000 /dev/zero | tr '\0' 'A'; printf '"}'; } > "$big"
big_code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$SERVER/api/v1/internal/keeper/execute" \
  -H 'Content-Type: application/json' --data-binary "@$big" --max-time 15 2>/dev/null || echo "000")"
rm -f "$big"
assert_not_2xx "2 MB body: /keeper/execute" "$big_code"

# ─────────────────────────────────────────────────────────────────────────────
section "6. No info leak — the rejection body carries no internals"
# ─────────────────────────────────────────────────────────────────────────────
body="$(http_body POST /api/v1/internal/keeper/execute)"
assert_not_contains "rejection body has no goroutine stack trace" "$body" "goroutine"
assert_not_contains "rejection body has no Go source path"        "$body" ".go:"
assert_not_contains "rejection body does not echo an internal token" "$body" "X-Internal-Token"
assert_not_contains "rejection body has no SQL"                    "$body" "SELECT "

# ─────────────────────────────────────────────────────────────────────────────
section "7. The fence isn't keeper-specific — other /internal/* reject too"
# ─────────────────────────────────────────────────────────────────────────────
for p in /api/v1/internal/dispatch /api/v1/internal/journal /api/v1/internal/cost; do
  assert_not_2xx "no token: POST $p" "$(http_code POST "$p")"
done

# ─────────────────────────────────────────────────────────────────────────────
section "8. Public API still requires auth (regression: not everything is open)"
# ─────────────────────────────────────────────────────────────────────────────
# A normal authenticated route hit WITHOUT credentials must be 401/403 — proves
# the auth middleware is live and section 2's rejections aren't a blanket 403.
for p in /api/v1/agents /api/v1/credentials /api/v1/crews; do
  c="$(http_code GET "$p")"
  if [[ "$c" == "401" || "$c" == "403" ]]; then
    _pass "unauthenticated GET $p → $c (auth enforced)"
  else
    _fail "unauthenticated GET $p" "expected 401/403, got $c"
  fi
done

# ─────────────────────────────────────────────────────────────────────────────
section "9. Constant-time probe — token compare shouldn't leak via timing"
# ─────────────────────────────────────────────────────────────────────────────
# Compare median latency of a plausible-length token vs a 1-char token. A large,
# consistent delta would hint at a non-constant-time compare (length/prefix
# oracle). Best-effort + noisy over the internet → WARN only, never fails.
timed() { # <token> → ms for one request
  local tk="$1" t0 t1
  t0=$(perl -MTime::HiRes=time -e 'printf "%d", time()*1000' 2>/dev/null || echo 0)
  http_code POST /api/v1/internal/keeper/execute -H "X-Internal-Token: $tk" >/dev/null
  t1=$(perl -MTime::HiRes=time -e 'printf "%d", time()*1000' 2>/dev/null || echo 0)
  echo $(( t1 - t0 ))
}
if perl -MTime::HiRes -e 1 >/dev/null 2>&1; then
  sumA=0; sumB=0; N=6
  for _ in $(seq 1 $N); do
    sumA=$(( sumA + $(timed "00000000000000000000000000000000") ))
    sumB=$(( sumB + $(timed "0") ))
  done
  avgA=$(( sumA / N )); avgB=$(( sumB / N )); delta=$(( avgA > avgB ? avgA-avgB : avgB-avgA ))
  info "median-ish latency: 32-char=${avgA}ms 1-char=${avgB}ms Δ=${delta}ms"
  if (( delta > 40 )); then
    warn "token-compare timing delta ${delta}ms is large — review for constant-time compare (subtle.ConstantTimeCompare)"
  else
    _pass "no obvious token-compare timing oracle (Δ=${delta}ms ≤ 40ms)"
  fi
else
  skip "constant-time timing probe" "perl Time::HiRes not available"
fi

# ─────────────────────────────────────────────────────────────────────────────
section "10. FINDING probe — is the network-origin gate live behind the proxy?"
# ─────────────────────────────────────────────────────────────────────────────
# internal.go: a non-internal source IP is meant to get 404 (route hidden). From
# an off-host public caller we should see 404 if the gate fires. A 403 (token
# reject) instead means our public request reached the token check — the network
# gate did NOT fire (reverse proxy presents us as local) and the static
# X-Internal-Token is the sole guard.
probe="$(http_code POST /api/v1/internal/keeper/execute)"
if [[ "$probe" == "404" ]]; then
  _pass "network-origin gate appears active (off-host caller → 404, route hidden)"
elif [[ "$probe" == "403" ]]; then
  warn "network-origin gate is DEFEATED for this deployment: off-host caller → 403 (token reject), not 404."
  warn "  → The internal keeper surface is internet-reachable; only the static shared X-Internal-Token guards it."
  warn "  → Fix (#1020): deny /api/v1/internal/* at the proxy, OR set CREWSHIP_INTERNAL_TRUSTED_PROXIES=<proxy-IP>"
  warn "     on the server so the real client is resolved from X-Forwarded-For and this off-host caller gets 404."
  warn "  → Fence still holds (sections 2–7), so this is a hardening gap, not a live breach."
else
  info "network-gate probe returned $probe (neither 404 nor 403) — inspect manually."
fi

# ─────────────────────────────────────────────────────────────────────────────
section "11. #1020 — a spoofed X-Forwarded-For from an untrusted peer changes nothing"
# ─────────────────────────────────────────────────────────────────────────────
# Config-independent invariant: this harness is an untrusted client (direct, or
# behind the public proxy). A spoofed XFF claiming a loopback/private origin
# must NOT alter the origin-gate outcome — XFF is honored only for a peer in
# CREWSHIP_INTERNAL_TRUSTED_PROXIES, and even then only the rightmost UNtrusted
# hop counts (the proxy appends our real IP to the right; our injected leftmost
# value is ignored). So the response with a spoofed header must equal the one
# without, and must never be 2xx.
base_code="$(http_code POST /api/v1/internal/keeper/execute)"
for hdr in "X-Forwarded-For: 127.0.0.1" "X-Forwarded-For: 10.0.0.1" "X-Forwarded-For: 127.0.0.1, ::1" "X-Real-IP: 127.0.0.1"; do
  spoof_code="$(http_code POST /api/v1/internal/keeper/execute -H "$hdr")"
  if [[ "$spoof_code" =~ ^2 ]]; then
    _fail "spoofed origin ($hdr)" "internal route returned $spoof_code — origin gate bypassed via a spoofed X-Forwarded-For"
  elif [[ "$spoof_code" == "$base_code" ]]; then
    _pass "spoofed origin ($hdr) ignored — same outcome ($spoof_code) as no header"
  else
    _fail "spoofed origin ($hdr)" "changed outcome $base_code → $spoof_code (an untrusted peer's XFF must have NO effect)"
  fi
done

# ─────────────────────────────────────────────────────────────────────────────
section "12. #1020 — legit trusted-proxy client resolution (needs server config + private origin)"
# ─────────────────────────────────────────────────────────────────────────────
skip "trusted-proxy XFF resolution end-to-end (T1b)" \
  "set CREWSHIP_INTERNAL_TRUSTED_PROXIES=<this-proxy's-IP> on the server, then: (a) from an origin INSIDE a trusted/private range, a request the proxy tags with X-Forwarded-For=<that private IP> must resolve to the private client and pass the origin gate (reaching the token check); (b) a public rightmost hop must be 404'd. Can't be faked from a public harness host — the reverse proxy appends the REAL client IP as the rightmost XFF entry, so we can only ever present as a public client here. Unit-proven in internal_ingress_trusted_proxy_sec_test.go."

printf '\n%s   warnings (findings, non-fatal): %d%s\n' "$_C_YEL" "$_WARN" "$_C_OFF"
finish
