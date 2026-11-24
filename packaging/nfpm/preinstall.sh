#!/bin/sh
# RPM cannot unpack a regular file over the directory in older Crewship RPMs.
# Its pre-install argument is 2 on upgrade. dpkg handles this transition itself.
set -eu
[ "${1:-}" = 2 ] || exit 0
cli=/usr/libexec/crewship/crewship-agent-cli
[ -d "$cli" ] && [ ! -L "$cli" ] || exit 0
old="$cli/crewship-agent-cli"
if [ ! -f "$old" ] || [ -L "$old" ] ||
    [ "$(rpm -qf --qf '%{NAME}' "$old")" != crewship ]; then
    echo "Cannot migrate unrecognized directory $cli" >&2
    exit 1
fi
# Preserve local additions and the previous executable if unpacking fails.
# A private sibling on the same filesystem makes the rename atomic.
saved=$(mktemp -d "${cli}.rpmsave.XXXXXX")
mv -- "$cli" "$saved/"
echo "Previous agent CLI directory saved in $saved"
