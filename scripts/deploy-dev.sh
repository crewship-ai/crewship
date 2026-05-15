#!/usr/bin/env bash
# Deploy current branch to a remote dev server over SSH.
#
# Usage: ./scripts/deploy-dev.sh [branch]
#   - No args: deploys current local branch
#   - With arg: deploys specified branch
#   - Pushes to origin, pulls on server, rebuilds, restarts
#
# Required env (no defaults — must export):
#   CREWSHIP_DEPLOY_HOST   SSH alias or user@host for the target dev server.
#   CREWSHIP_DEPLOY_PATH   Absolute path to the crewship checkout on that host.
set -euo pipefail

if [ -z "${CREWSHIP_DEPLOY_HOST:-}" ]; then
  echo "error: CREWSHIP_DEPLOY_HOST is required (export CREWSHIP_DEPLOY_HOST=<ssh-alias-or-user@host>)" >&2
  exit 2
fi
if [ -z "${CREWSHIP_DEPLOY_PATH:-}" ]; then
  echo "error: CREWSHIP_DEPLOY_PATH is required (export CREWSHIP_DEPLOY_PATH=<absolute-path-on-server>)" >&2
  exit 2
fi
SERVER_HOST="${CREWSHIP_DEPLOY_HOST}"
SERVER_PATH="${CREWSHIP_DEPLOY_PATH}"
GO_PATH_EXPORT='export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin'

BRANCH="${1:-$(git rev-parse --abbrev-ref HEAD)}"
echo ">>> Deploying branch '$BRANCH' to $SERVER_HOST..."

# 1. Push local branch to origin
echo ">>> Pushing to origin..."
git push origin "$BRANCH" 2>&1

# 2. On server: checkout branch, pull, rebuild, restart
echo ">>> Deploying on server..."
ssh "$SERVER_HOST" bash <<REMOTE
  set -euo pipefail
  export PATH=\$PATH:/usr/local/go/bin:\$HOME/go/bin
  cd "$SERVER_PATH"

  # Stop containers to avoid stale processes
  docker rm -f \$(docker ps -q --filter "name=crewship-team-") 2>/dev/null || true

  # Fetch and checkout
  git fetch origin "$BRANCH" 2>&1
  CURRENT=\$(git branch --show-current)
  if [ "\$CURRENT" != "$BRANCH" ]; then
    git checkout -- . 2>/dev/null || true
    git checkout "$BRANCH" 2>&1 || git checkout -b "$BRANCH" "origin/$BRANCH" 2>&1
  fi
  git reset --hard "origin/$BRANCH"
  echo "  Branch: $BRANCH (\$(git log --oneline -1))"

  # Rebuild
  echo "  Building Go..."
  go build -o crewship ./cmd/crewship 2>&1

  echo "  Installing npm deps..."
  pnpm install 2>&1 | tail -2

  # Restart
  echo "  Restarting services..."
  ./dev.sh stop 2>&1 | grep -E "Stopped|stopped" || true
  ./dev.sh start 2>&1

  echo ""
  echo ">>> Deploy complete: $BRANCH on \$(hostname)"
REMOTE
