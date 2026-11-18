#!/bin/sh
# GoReleaser post-build hook: reuse the Linux clionly build as a named runtime
# companion. Non-Linux clients are never copied into Linux crew containers.
set -eu
[ "$#" -eq 2 ] || { echo 'usage: stage-agent-cli.sh TARGET BINARY' >&2; exit 2; }
case "$1" in
  linux_amd64*|linux_arm64*) ;;
  darwin_*|windows_*) exit 0 ;;
  *) echo 'unsupported agent CLI build target' >&2; exit 2 ;;
esac
[ -f "$2" ] && [ ! -L "$2" ] || { echo 'regular built client required' >&2; exit 1; }
cp -- "$2" "${2%/*}/crewship-agent-cli"
chmod 0755 "${2%/*}/crewship-agent-cli"
