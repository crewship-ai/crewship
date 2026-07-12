#!/bin/bash
# Crewship Sandbox: L3/L4 Network Firewall
# Based on Claude Code devcontainer init-firewall.sh
# https://github.com/anthropics/claude-code/blob/main/.devcontainer/init-firewall.sh

set -euo pipefail
IFS=$'\n\t'

# 1. Extract Docker DNS info BEFORE flushing
DOCKER_DNS_RULES=$(iptables-save -t nat 2>/dev/null | grep "127\.0\.0\.11" || true)

# Flush existing rules and sets
iptables -F
iptables -X
iptables -t nat -F
iptables -t nat -X
iptables -t mangle -F
iptables -t mangle -X
ipset destroy allowed-domains 2>/dev/null || true

# 2. Restore Docker DNS rules via iptables-restore (safer than xargs for
# rules containing shell metacharacters; format is already iptables-save).
if [ -n "$DOCKER_DNS_RULES" ]; then
    echo "Restoring Docker DNS rules..."
    iptables -t nat -N DOCKER_OUTPUT 2>/dev/null || true
    iptables -t nat -N DOCKER_POSTROUTING 2>/dev/null || true
    printf "*nat\n%s\nCOMMIT\n" "$DOCKER_DNS_RULES" | iptables-restore --noflush 2>/dev/null || {
        echo "WARN: failed to restore Docker DNS rules" >&2
    }
fi

# 3. Allow DNS only to the resolvers listed in /etc/resolv.conf (typically
# Docker's embedded 127.0.0.11). A broad "allow all :53" rule would let any
# process inside the sandbox smuggle traffic over DNS; scoping to the known
# resolver IPs closes that. Inbound replies are gated on ESTABLISHED state.
NAMESERVERS=$(awk '/^nameserver/ {print $2}' /etc/resolv.conf 2>/dev/null || true)
if [ -z "$NAMESERVERS" ]; then
    echo "ERROR: no nameservers in /etc/resolv.conf" >&2
    exit 1
fi
for ns in $NAMESERVERS; do
    # Skip IPv6 nameservers — we DROP v6 entirely below.
    [[ "$ns" =~ : ]] && continue
    echo "Allowing DNS to $ns"
    iptables -A OUTPUT -p udp -d "$ns" --dport 53 -j ACCEPT
    iptables -A INPUT  -p udp -s "$ns" --sport 53 -m state --state ESTABLISHED -j ACCEPT
    iptables -A OUTPUT -p tcp -d "$ns" --dport 53 -j ACCEPT
    iptables -A INPUT  -p tcp -s "$ns" --sport 53 -m state --state ESTABLISHED -j ACCEPT
done
iptables -A INPUT -i lo -j ACCEPT
iptables -A OUTPUT -o lo -j ACCEPT

# 4. Create ipset with CIDR support
ipset create allowed-domains hash:net

# 5. Add GitHub IP ranges (web + api + git)
echo "Fetching GitHub IP ranges..."
gh_ranges=$(curl -sf --max-time 10 https://api.github.com/meta) || {
    echo "ERROR: Failed to fetch GitHub IP ranges" >&2
    exit 1
}

echo "$gh_ranges" | jq -e '.web and .api and .git' >/dev/null || {
    echo "ERROR: GitHub API response malformed" >&2
    exit 1
}

while read -r cidr; do
    [[ "$cidr" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$ ]] && ipset add allowed-domains "$cidr"
done < <(echo "$gh_ranges" | jq -r '(.web + .api + .git)[]' 2>/dev/null | aggregate -q 2>/dev/null || echo "$gh_ranges" | jq -r '(.web + .api + .git)[]')

# 6. Resolve and allow specific domains
ALLOWED_DOMAINS=(
    # AI APIs
    "api.anthropic.com"
    "api.openai.com"
    "generativelanguage.googleapis.com"
    # OpenCode BYOK providers (#944) — keep in sync with
    # internal/sidecar/allowlist.go DefaultAllowedDomains
    "openrouter.ai"
    "api.x.ai"
    "api.groq.com"
    "api.deepseek.com"
    "api.moonshot.ai"
    "api.z.ai"
    "api.minimax.io"
    # Package registries
    "registry.npmjs.org"
    "registry.yarnpkg.com"
    "pypi.org"
    "pypi.python.org"
    "files.pythonhosted.org"
    # Linux package mirrors (Debian)
    "deb.debian.org"
    "security.debian.org"
    # Container registries
    "ghcr.io"
    "pkg-containers.githubusercontent.com"
    "registry-1.docker.io"
    "auth.docker.io"
    "production.cloudflare.docker.com"
    # Telemetry (Anthropic/Claude Code)
    "statsig.anthropic.com"
    "statsig.com"
    "sentry.io"
)

# #974-S6 (defense-in-depth): filter resolved IPs before seeding the L3
# allowset. A domain that resolves to an internal IP at startup would
# otherwise be silently permitted at L3 even though the sidecar's dial-time
# SSRF guard blocks it — so a misconfigured/rebinding allowed_domain can't
# open a hole to the host's private network. Hard ranges (link-local/metadata,
# loopback, this-net, multicast, reserved) are ALWAYS skipped; RFC1918/CGNAT
# are skipped unless private-endpoint egress is enabled at the instance level.
# Default-safe: with the flag unset, only genuinely-public IPs are added, which
# is exactly what the public ALLOWED_DOMAINS above should resolve to anyway.
case "$(printf '%s' "${CREWSHIP_ALLOW_PRIVATE_ENDPOINTS:-}" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes|on|y|t) ALLOW_PRIVATE=1 ;;
    *)                  ALLOW_PRIVATE=0 ;;
esac

# is_blocked_ip <ipv4> — prints a reason and returns 0 if the IP must NOT be
# added to the allowset; returns 1 (add it) otherwise.
is_blocked_ip() {
    local ip="$1" o1 o2 o3 o4
    IFS='.' read -r o1 o2 o3 o4 <<< "$ip"
    # Hard tier — never overridable.
    if [ "$o1" -eq 0 ] || [ "$o1" -eq 127 ] \
       || { [ "$o1" -eq 169 ] && [ "$o2" -eq 254 ]; } \
       || [ "$o1" -ge 224 ]; then
        echo "hard-blocked (link-local/metadata/loopback/reserved)"
        return 0
    fi
    # Private tier — RFC1918 + CGNAT (100.64.0.0/10), relaxable via instance opt-in.
    if [ "$o1" -eq 10 ] \
       || { [ "$o1" -eq 172 ] && [ "$o2" -ge 16 ] && [ "$o2" -le 31 ]; } \
       || { [ "$o1" -eq 192 ] && [ "$o2" -eq 168 ]; } \
       || { [ "$o1" -eq 100 ] && [ "$o2" -ge 64 ] && [ "$o2" -le 127 ]; }; then
        if [ "$ALLOW_PRIVATE" != "1" ]; then
            echo "private (RFC1918/CGNAT) — set CREWSHIP_ALLOW_PRIVATE_ENDPOINTS to allow"
            return 0
        fi
    fi
    return 1
}

for domain in "${ALLOWED_DOMAINS[@]}"; do
    echo "Resolving $domain..."
    ips=$(dig +noall +answer +short A "$domain" 2>/dev/null | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' || true)
    if [ -z "$ips" ]; then
        echo "WARN: Failed to resolve $domain, skipping" >&2
        continue
    fi
    while read -r ip; do
        if reason=$(is_blocked_ip "$ip"); then
            echo "WARN: skipping $domain -> $ip: $reason" >&2
            continue
        fi
        ipset add allowed-domains "$ip" 2>/dev/null || true
    done <<< "$ips"
done

# 7. Allow outbound to crewshipd on the host gateway only.
# Sandbox needs to reach crewshipd's IPC port (for sidecar-injected credential
# lookups, keeper requests, etc.). Scope to HOST_IP/32 + specific port rather
# than opening the whole gateway — Docker bridges are typically /16 and the
# gateway is reachable from any container on it.
# Reply traffic is covered by the ESTABLISHED,RELATED rules below, so no
# blanket INPUT accept is needed.
CREWSHIPD_PORT="${CREWSHIPD_PORT:-8080}"
HOST_IP=$(ip route | awk '/default/ {print $3; exit}')
if [ -n "$HOST_IP" ]; then
    echo "Allowing crewshipd at $HOST_IP:$CREWSHIPD_PORT"
    iptables -A OUTPUT -p tcp -d "$HOST_IP" --dport "$CREWSHIPD_PORT" -j ACCEPT
fi

# 8. Default DROP policies
iptables -P INPUT DROP
iptables -P FORWARD DROP
iptables -P OUTPUT DROP

# 8b. Block all IPv6 traffic — no v6 allowlist implemented.
# Containers should not have outbound IPv6 by default; this is belt+suspenders.
# Use `2>/dev/null || true` so the script doesn't fail on hosts without IPv6
# kernel support (Alpine / minimal Debian may ship without the v6 module loaded).
ip6tables -P INPUT DROP 2>/dev/null || true
ip6tables -P FORWARD DROP 2>/dev/null || true
ip6tables -P OUTPUT DROP 2>/dev/null || true
ip6tables -A INPUT -i lo -j ACCEPT 2>/dev/null || true
ip6tables -A OUTPUT -o lo -j ACCEPT 2>/dev/null || true

# 9. Allow established connections
iptables -A INPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT

# 10. Allow outbound to allowed destinations
iptables -A OUTPUT -m set --match-set allowed-domains dst -j ACCEPT

# 11. REJECT all other outbound with clear error
iptables -A OUTPUT -j REJECT --reject-with icmp-admin-prohibited

# 12. Self-verify
echo "Verifying firewall..."
if curl -sf --connect-timeout 5 https://example.com >/dev/null 2>&1; then
    echo "ERROR: Firewall LEAKING - can reach blocked example.com" >&2
    exit 1
fi
if ! curl -sf --connect-timeout 5 https://api.github.com/zen >/dev/null 2>&1; then
    echo "ERROR: Firewall BROKEN - cannot reach api.github.com (allowed)" >&2
    exit 1
fi

echo "Crewship Sandbox firewall: ACTIVE"
echo "  Allowed: Anthropic/OpenAI/Google AI, GitHub, npm/PyPI, Debian, OCI registries"
echo "  Default: DROP (icmp-admin-prohibited)"
