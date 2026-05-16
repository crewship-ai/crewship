#!/bin/sh
# Wrapper entrypoint for the single-binary production image.
#
# Two responsibilities:
#
#   1. Pre-flight required env vars when (and ONLY when) the requested
#      subcommand is `start`. Without this, a fresh `docker run` from a
#      beta tester missing NEXTAUTH_SECRET or ENCRYPTION_KEY produces a
#      blank port :8080 and a cryptic panic the user has to dig out of
#      `docker logs`. Exit 78 (EX_CONFIG) with copy-pasteable openssl
#      one-liners surfaces immediately on `docker run` exit.
#
#   2. Pass through unchanged for every other subcommand (`version`,
#      `doctor`, `--help`, `setup`, etc). Those don't need a running
#      server, must not depend on app secrets, and are exactly what
#      CI smoke tests and humans-debugging-an-install want to be able
#      to run inside the container without setting up the world.
#
# Production binary still has its own server.New() panic guards as
# defence in depth — this script is the UX layer.

set -e

# Decide whether this invocation is a `start` (or its default form, no
# args = same as start because the image's documented default is server
# mode). Anything else flows straight through.
needs_env=0
if [ $# -eq 0 ]; then
  needs_env=1
else
  case "$1" in
    start)
      needs_env=1
      ;;
  esac
fi

if [ "$needs_env" = "1" ]; then
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
fi

# No args = default to `start`; explicit subcommand passes through.
if [ $# -eq 0 ]; then
  exec crewship start
fi
exec crewship "$@"
