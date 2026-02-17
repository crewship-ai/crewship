#!/bin/bash
set -e

export HOME=/home/agent

# Pre-create Claude CLI config directory for credential injection
mkdir -p /home/agent/.claude

# Create ready marker for healthcheck
touch /workspace/.ready

# PID 1: keep container alive for Docker exec pattern
exec sleep infinity
