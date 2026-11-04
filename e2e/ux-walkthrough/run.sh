#!/usr/bin/env bash
# Usage (through the build queue, which does not forward the caller's env):
#   crewship-heavy e2e/ux-walkthrough/run.sh <accounts.env> <origin> <target> <out-dir> [playwright args...]
# The accounts file is sourced here so credentials never appear on a command line.
set -euo pipefail
accounts=$1 origin=$2 target=$3 out=$4; shift 4
set -a; . "$accounts"; set +a
export UX_ORIGIN=$origin UX_TARGET=$target UX_OUT=$out
cd "$(dirname "$0")/../.."
exec pnpm exec playwright test -c e2e/ux-walkthrough/playwright.config.ts "$@"
