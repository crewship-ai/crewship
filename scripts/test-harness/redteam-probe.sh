#!/usr/bin/env bash
# Red-team insider probe — runs inside a crew container via a routine `script` step.
# Authorized self-attack: reports what a COMPROMISED agent can reach from inside.
# Emits one JSON line to stdout (the step output). Token-zero, deterministic.
set -uo pipefail

esc() { printf '%s' "${1:-}" | tr '\n' ';' | sed 's/\\/\\\\/g; s/"/\\"/g'; }
have() { command -v "$1" >/dev/null 2>&1; }

ID="$(id 2>&1 || true)"
PROXY="HTTP=${HTTP_PROXY:-none} HTTPS=${HTTPS_PROXY:-none}"

# #1364 — can the agent read cleartext secret files on disk?
SECRETS_LS="$(ls -la /secrets 2>&1 | head -15 || true)"
SECRETS_FILES="$(find /secrets -maxdepth 4 -type f 2>/dev/null | head -25 || true)"

# HTTP prober (curl or wget)
httpcode() { # url [extra curl args...]
  local url="$1"; shift || true
  if have curl; then curl -s -o /dev/null -w '%{http_code}' --max-time 8 "$@" "$url" 2>/dev/null || echo ERR
  elif have wget; then wget -q -O /dev/null -T 8 "$url" 2>/dev/null && echo 200 || echo ERR
  else echo NO-HTTP-CLIENT; fi
}

# Tier-B premise: is the internal API reachable from inside the container?
INTERNAL="$(httpcode http://host.docker.internal:8080/api/v1/internal/agents)"
# #1368: raw egress bypassing HTTP_PROXY (deny at L3 would make this fail)
RAW_EGRESS="$(httpcode https://1.1.1.1 --noproxy '*')"
# egress via proxy to a NON-allowlisted host (restricted crew should 403)
PROXIED="$(httpcode https://example.org)"
# raw TCP without any HTTP client — /dev/tcp to a public IP:443
RAW_TCP="closed"; (exec 3<>/dev/tcp/1.1.1.1/443) 2>/dev/null && { RAW_TCP="open"; exec 3>&- 2>/dev/null; }

# tokens visible in the environment (redacted)
TOKENS="$(env | grep -iE 'TOKEN|SECRET|KEY|CRED' | sed -E 's/=(.{4}).*/=\1<REDACTED>/' | head -20 || true)"

printf '{"id":"%s","proxy":"%s","internal_api_code":"%s","raw_egress_code":"%s","proxied_blocked_code":"%s","raw_tcp_443":"%s","secrets_ls":"%s","secrets_files":"%s","token_env":"%s"}\n' \
  "$(esc "$ID")" "$(esc "$PROXY")" "$INTERNAL" "$RAW_EGRESS" "$PROXIED" "$RAW_TCP" \
  "$(esc "$SECRETS_LS")" "$(esc "$SECRETS_FILES")" "$(esc "$TOKENS")"
