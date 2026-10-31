#!/bin/bash
# Answers PRD_RESTART_HANDSHAKE requests from the operator's own shell, where
# `crew restart` works (it refuses to run under crewship-heavy):
#
#   mkdir -p /tmp/prd-restart && e2e/acceptance/prd/restart-watcher.sh /tmp/prd-restart dev2 &
#   PRD_RESTART_HANDSHAKE=/tmp/prd-restart node e2e/acceptance/prd/run.mjs ...
#
# Usage: restart-watcher.sh <handshake dir> <crew environment>
dir="${1:?handshake directory}"
env="${2:?crew environment, e.g. dev2}"
mkdir -p "$dir"
while true; do
  if [ -f "$dir/restart.request" ]; then
    rm -f "$dir/restart.request"
    if out=$(crew restart "$env" 2>&1); then echo "ok" > "$dir/restart.done"; else printf 'failed: %s\n' "$(echo "$out" | tr '\n' ' ' | cut -c1-400)" > "$dir/restart.done"; fi
  fi
  sleep 1
done
