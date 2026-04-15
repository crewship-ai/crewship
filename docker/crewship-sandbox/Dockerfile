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
