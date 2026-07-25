# Crewship Sandbox Runtime Image
# Inspired by Claude Code's devcontainer (https://github.com/anthropics/claude-code)
#
# Provides L3/L4 network isolation via iptables + ipset, allowing only specific
# domains/IPs. Stronger than HTTP-proxy filtering - cannot be bypassed from inside.

FROM debian:bookworm-slim

ARG TZ="UTC"
ENV TZ="$TZ"
ENV DEBIAN_FRONTEND=noninteractive

# Install network security tools + basic dev tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    # Core
    ca-certificates curl wget gnupg \
    # Network security (iptables firewall)
    iptables ipset iproute2 dnsutils aggregate jq \
    # Development
    git less procps sudo nano vim \
    # Build essentials for some devcontainer features
    build-essential \
    # Shell
    bash zsh \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

# Create non-root user matching Crewship's UID 1001
# Use "agent" to match crewship convention (sidecar uses UID 1002)
RUN groupadd -g 1001 agent && useradd -u 1001 -g agent -m -s /bin/bash agent

# Ensure agent has access to /usr/local/share for global installs
RUN mkdir -p /usr/local/share/npm-global && \
    chown -R agent:agent /usr/local/share /home/agent

# Set DEVCONTAINER env var for orientation
ENV DEVCONTAINER=true

# Create workspace and standard Crewship mount points
RUN mkdir -p /workspace /output /crew /secrets && \
    chown -R agent:agent /workspace /output /crew

WORKDIR /workspace

# Copy firewall initialization script
COPY init-firewall.sh /usr/local/bin/init-firewall.sh
RUN chmod +x /usr/local/bin/init-firewall.sh && \
    echo "agent ALL=(root) NOPASSWD: /usr/local/bin/init-firewall.sh" > /etc/sudoers.d/agent-firewall && \
    chmod 0440 /etc/sudoers.d/agent-firewall

# Defense-in-depth: strip the SUID bit from every setuid binary except sudo
# (#1400 part 2). The base image ships su, mount, umount, passwd, chsh, chfn,
# newgrp, gpasswd, ssh-keysign, … as SUID-root — gratuitous within-container
# attack surface. They are inert TODAY because the runtime sets NoNewPrivs=1
# (the kernel ignores setuid), but a single regression that drops NoNewPrivs or
# grants a capability would re-arm all of them at once.
#
# sudo is the one SUID binary kept: it is the elevation path for the NOPASSWD
# firewall-init sudoers rule above, and package-install postCreate flows on
# devcontainer bases rely on `sudo apt-get`. It stays inert under NoNewPrivs=1
# like the rest, but removing its SUID would break those paths.
#
# The trailing assertion FAILS THE BUILD if any SUID binary other than
# /usr/bin/sudo survives, so a future base-image bump that reintroduces one is
# caught at build time (the #1400 "find / -perm -4000" acceptance check, minus
# the load-bearing sudo).
RUN find / -xdev -perm -4000 -type f ! -path /usr/bin/sudo -exec chmod u-s {} + 2>/dev/null || true; \
    remaining="$(find / -xdev -perm -4000 -type f 2>/dev/null | sort | tr '\n' ' ' | sed 's/ $//')"; \
    if [ "$remaining" != "/usr/bin/sudo" ]; then \
        echo "SUID strip assertion failed (#1400): expected only /usr/bin/sudo, found: [$remaining]" >&2; \
        exit 1; \
    fi

# Switch to non-root user
USER agent
ENV HOME=/home/agent
ENV SHELL=/bin/bash
ENV EDITOR=nano

# Healthcheck
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=10s \
    CMD test -f /workspace/.ready || exit 1

# Default: keep container alive for docker exec
CMD ["sleep", "infinity"]
