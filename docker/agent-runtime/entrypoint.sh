#!/bin/bash
set -e

export HOME=/home/agent

# Create ready marker for healthcheck
touch /workspace/.ready

# Phase 2: start crewship-sidecar here
# if [ -x /usr/local/bin/crewship-sidecar ]; then
#     /usr/local/bin/crewship-sidecar --team-id="${CREWSHIP_TEAM_ID}" &
# fi

# PID 1: keep container alive for Docker exec pattern
exec sleep infinity
