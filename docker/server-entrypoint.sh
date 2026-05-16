#!/bin/sh
# Pre-flight check for the single-binary production image.
#
# Runs BEFORE `crewship start` so missing required env vars surface as a
# clear actionable error in `docker logs <container>` instead of the
# binary panicking deep inside server.New() with a stack trace. Without
# this, a fresh `docker run ghcr.io/crewship-ai/crewship` from a beta
# tester without env vars produces a blank port :8080 and a cryptic
# "NEXTAUTH_SECRET not set" panic the user has to dig out of the logs.
#
# Exits non-zero with a human-readable bullet list when anything is
# missing. Docker / docker-compose surface that exit code immediately,
# so the user gets the feedback at the moment they ran the wrong
# command. Production binary still has its own server.New() panic
# guards as a defence in depth — this script is the UX layer.

set -e

missing=""

require() {
  var=$1
  hint=$2
  # shellcheck disable=SC2154
  eval "val=\${$var:-}"
  if [ -z "$val" ]; then
    missing="${missing}  - ${var}\n    ${hint}\n"
  fi
}

require NEXTAUTH_SECRET    "Random 32+ byte hex string. Generate with: openssl rand -hex 32"
require ENCRYPTION_KEY     "Random 32-byte key encoded as hex (64 chars). Generate with: openssl rand -hex 32"

if [ -n "$missing" ]; then
  printf '\n'
  printf 'crewship cannot start — required environment variables are not set:\n\n'
  # shellcheck disable=SC2059
  printf "$missing"
  printf '\nPass them on the docker command line, e.g.\n\n'
  printf '  docker run -e NEXTAUTH_SECRET=$(openssl rand -hex 32) \\\n'
  printf '             -e ENCRYPTION_KEY=$(openssl rand -hex 32) \\\n'
  printf '             -p 8080:8080 ghcr.io/crewship-ai/crewship:latest\n\n'
  printf 'Or use a docker-compose .env file. See docs/quickstart.mdx.\n'
  exit 78  # EX_CONFIG — sysexits.h "configuration error"
fi

exec crewship start "$@"
