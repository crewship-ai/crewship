# Crewship Agent Runtime
# Non-root, hardened container for running AI agents.
# 1 container = 1 team. Agents share the container via Docker exec.

FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl git jq openssh-client python3 python3-pip ca-certificates ripgrep \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install CLI adapters globally
RUN npm install -g @anthropic-ai/claude-code@latest && \
    npm cache clean --force

# Non-root user (UID 1001)
RUN groupadd -g 1001 agent && useradd -u 1001 -g agent -m agent

RUN mkdir -p /workspace /output && \
    chown agent:agent /workspace /output

COPY entrypoint.sh /usr/local/bin/entrypoint.sh

USER agent
WORKDIR /workspace

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD test -f /workspace/.ready || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
