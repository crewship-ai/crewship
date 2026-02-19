# Crewship Agent Runtime
# Non-root, hardened container for running AI agents.
# 1 container = 1 crew. Agents share the container via Docker exec.
# Rootfs is readonly at runtime; /workspace, /output, /tmp, /home/agent are writable mounts.

# Stage 1: Build crewship-sidecar binary
FROM golang:1.26-alpine AS sidecar-builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/crewship-sidecar/ cmd/crewship-sidecar/
COPY internal/sidecar/ internal/sidecar/
COPY internal/scrubber/ internal/scrubber/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /crewship-sidecar ./cmd/crewship-sidecar/

# Stage 2: Agent runtime
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl git jq openssh-client python3 python3-pip ca-certificates ripgrep \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    rm -rf /var/lib/apt/lists/*

# Install Claude Code CLI globally
RUN npm install -g @anthropic-ai/claude-code@latest && \
    npm cache clean --force

# Non-root user (UID 1001, matches Docker provider security config)
RUN groupadd -g 1001 agent && useradd -u 1001 -g agent -m agent

# SECURITY: Sidecar runs as separate UID (1002) so the agent (UID 1001)
# cannot read /proc/<sidecar_pid>/mem to extract credentials from memory.
RUN groupadd -g 1002 sidecar && useradd -u 1002 -g sidecar -s /usr/sbin/nologin sidecar

RUN mkdir -p /workspace /output && \
    chown agent:agent /workspace /output

# Sidecar proxy binary (credential injection, domain allowlist)
COPY --from=sidecar-builder /crewship-sidecar /usr/local/bin/crewship-sidecar

COPY docker/agent-runtime/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh /usr/local/bin/crewship-sidecar

# Disable auto-update (rootfs is readonly at runtime)
ENV CLAUDE_CODE_DISABLE_AUTOUPDATE=1
ENV HOME=/home/agent

USER agent
WORKDIR /workspace

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD test -f /workspace/.ready || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
