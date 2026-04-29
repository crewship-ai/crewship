#!/bin/bash
set -e

export HOME=/home/agent

# Make new files group-writable so the host (crewshipd, uid 1000)
# can edit / save files through the Files UI. Without this, files
# the agent creates (uid 1001) come out as 0644 and host fs writes
# fail with EACCES on os.Create. Combined with setgid on the bind-
# mount root, this gives both processes rw on the same files.
umask 0002

# First-boot skeleton: initialize home directory on empty volume.
if [ ! -f /home/agent/.bashrc ]; then
    cp /etc/skel/.bashrc /home/agent/.bashrc 2>/dev/null || true
    cp /etc/skel/.profile /home/agent/.profile 2>/dev/null || true
fi
mkdir -p /home/agent/.claude /home/agent/.local/bin /home/agent/.ssh
chmod 700 /home/agent/.ssh 2>/dev/null || true

# Ensure crew tools directory is usable.
mkdir -p /opt/crew-tools/bin 2>/dev/null || true

# Prepend persistent tool directories to PATH.
export PATH="/opt/crew-tools/bin:/home/agent/.local/bin:$PATH"

# Create ready marker for healthcheck.
touch /workspace/.ready

# PID 1: keep container alive for Docker exec pattern.
exec sleep infinity

# NOTE: The sidecar binary is started via Docker exec (by the orchestrator)
# as UID 1002 (sidecar group), NOT as the agent user (UID 1001).
# This prevents the agent from reading sidecar memory via /proc/<pid>/mem.
