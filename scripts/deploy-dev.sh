#!/usr/bin/env bash
# Deploy current branch to a remote dev server over SSH.
#
# Usage: ./scripts/deploy-dev.sh [branch]
#   - No args: deploys current local branch
#   - With arg: deploys specified branch
#   - Publishes the branch to origin (skipped when origin already has every
#     local commit), then pulls on server, rebuilds, restarts
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

# Publish $1 to origin, unless origin already has every local commit.
#
# The remote half of this script deploys `origin/$BRANCH` (fetch + reset
# --hard), never the local working copy, so a push that would only re-send
# commits origin already has buys nothing. It does, however, *fail* when the
# local branch is merely BEHIND origin (non-fast-forward), and under
# `set -euo pipefail` that aborted the whole deploy — the common case of
# deploying `main` from a clone that hadn't pulled in a while.
push_branch() {
  local branch="$1"
  # Push moves refs/heads/$branch, so ancestry has to be measured from that
  # ref too; HEAD only coincides with it when $branch is checked out.
  local local_ref="refs/heads/$branch"
  git rev-parse --verify --quiet "$local_ref^{commit}" >/dev/null || local_ref="HEAD"

  # Fetch first: the ancestry check must compare against origin's real tip,
  # not a remote-tracking ref that is itself stale.
  if ! git fetch origin "$branch" >/dev/null 2>&1; then
    # Nothing on origin to compare against (brand-new branch), or the fetch
    # failed for its own reasons — either way, let the push decide.
    echo ">>> Pushing to origin (no origin/$branch yet)..."
  elif git merge-base --is-ancestor "$local_ref" "refs/remotes/origin/$branch" 2>/dev/null; then
    echo ">>> Skipping push: origin/$branch already contains every local commit"
    echo "    (the server deploys origin/$branch, not this working copy)."
    return 0
  else
    echo ">>> Pushing to origin..."
  fi

  if ! git push origin "$branch" 2>&1; then
    echo "error: local '$branch' has diverged from origin; nothing was deployed." >&2
    echo "       Pull or force-push before retrying." >&2
    return 1
  fi
}

BRANCH="${1:-$(git rev-parse --abbrev-ref HEAD)}"
echo ">>> Deploying branch '$BRANCH' to $SERVER_HOST..."

# 1. Make sure origin has the commits the server is about to check out.
push_branch "$BRANCH"

# 2. On server: checkout branch, pull, rebuild, restart.
# SERVER_PATH and BRANCH are passed as positional args via `bash -s --`
# so the remote heredoc is fully single-quoted and can't be tampered
# with by crafted env values (CodeRabbit security catch). The remote
# reads them as $1, $2.
echo ">>> Deploying on server..."
# Forward SENTRY_DSN / NEXT_PUBLIC_SENTRY_DSN through to the remote when set
# locally. Empty default keeps existing dev-VM deploys telemetry-silent (matches
# Makefile + .goreleaser.yml + Dockerfile contract). Passed as positional args
# rather than `ssh -o SendEnv` so we don't depend on AcceptEnv being configured
# on the dev server's sshd_config.
SENTRY_DSN_VAL="${SENTRY_DSN:-}"
NEXT_PUBLIC_SENTRY_DSN_VAL="${NEXT_PUBLIC_SENTRY_DSN:-}"
ssh "$SERVER_HOST" bash -s -- "$SERVER_PATH" "$BRANCH" "$SENTRY_DSN_VAL" "$NEXT_PUBLIC_SENTRY_DSN_VAL" <<'REMOTE'
  set -euo pipefail
  SERVER_PATH="$1"
  BRANCH="$2"
  export SENTRY_DSN="${3:-}"
  export NEXT_PUBLIC_SENTRY_DSN="${4:-}"
  export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
  cd "$SERVER_PATH"

  # Recreate this instance's agent containers so each one's sidecar mints a
  # fresh workspace-bound IPC token under the current server. The containers are
  # named crewship-<N>-team-<crew> (N = instance, from the checkout dir), but the
  # old "crewship-team-" filter matched NONE of them — so stale sidecars survived
  # every deploy and their tokens went invalid after an internal-token / restart
  # change, making agent escalations fail with 403 "invalid workspace-bound
  # token". Scope the kill to THIS instance so sibling instances on the same host
  # are untouched.
  INSTANCE="$(basename "$SERVER_PATH" | grep -oE '[0-9]+$' || true)"
  if [ -n "$INSTANCE" ]; then
    docker rm -f $(docker ps -q --filter "name=crewship-${INSTANCE}-team-") 2>/dev/null || true
  else
    # No instance suffix on the checkout dir — skip the kill rather than fall back
    # to a host-wide "team-" filter, which would stop sibling instances' agent
    # containers on a shared host.
    echo "  WARN: could not derive instance from '$SERVER_PATH'; skipping agent-container recycle (sidecar tokens may be stale)" >&2
  fi

  # Fetch and checkout
  git fetch origin "$BRANCH" 2>&1
  CURRENT=$(git branch --show-current)
  if [ "$CURRENT" != "$BRANCH" ]; then
    git checkout -- . 2>/dev/null || true
    git checkout "$BRANCH" 2>&1 || git checkout -b "$BRANCH" "origin/$BRANCH" 2>&1
  fi
  git reset --hard "origin/$BRANCH"
  echo "  Branch: $BRANCH ($(git log --oneline -1))"

  # Rebuild via Make so LDFLAGS (incl. -X crashreport.DSN=$SENTRY_DSN) are
  # applied consistently with CI / goreleaser. Empty SENTRY_DSN leaves the
  # binary telemetry-silent — same shape as a local `make build:go`.
  echo "  Building Go..."
  make build:go 2>&1

  # --frozen-lockfile: install EXACTLY what pnpm-lock.yaml pins, same as CI
  # (setup-node-pnpm action), release.yml, nightly.yml and the Dockerfile. An
  # unfrozen `pnpm install` here would silently bump transitive deps on the
  # slot, so the binary it embeds diverges from what CI built — type errors and
  # runtime behaviour that don't reproduce anywhere else. If the lockfile is
  # genuinely out of sync with package.json on origin/$BRANCH, fail LOUDLY with
  # the full pnpm error rather than masking it through `| tail` and drifting.
  echo "  Installing npm deps (frozen lockfile)..."
  # Unique temp log (mktemp) + cleanup trap so concurrent slot deploys on the
  # same host don't clobber each other's output.
  PNPM_INSTALL_LOG="$(mktemp /tmp/crewship-pnpm-install.XXXXXX)"
  trap 'rm -f "$PNPM_INSTALL_LOG"' EXIT
  if ! pnpm install --frozen-lockfile >"$PNPM_INSTALL_LOG" 2>&1; then
    echo "  ERROR: 'pnpm install --frozen-lockfile' failed — pnpm-lock.yaml is" >&2
    echo "  out of sync with package.json on origin/$BRANCH. Commit an updated" >&2
    echo "  lockfile; do NOT run an unfrozen install on the slot." >&2
    tail -20 "$PNPM_INSTALL_LOG" >&2
    exit 1
  fi
  tail -2 "$PNPM_INSTALL_LOG"

  # Restart
  echo "  Restarting services..."
  ./dev.sh stop 2>&1 | grep -E "Stopped|stopped" || true
  ./dev.sh start 2>&1

  echo ""
  echo ">>> Deploy complete: $BRANCH on $(hostname)"
REMOTE
