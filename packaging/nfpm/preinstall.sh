#!/bin/sh
# RPM cannot unpack a regular file over the directory in older Crewship RPMs.
# Its pre-install argument is 2 on upgrade.
set -eu
case ${1:-} in
    2) cli=/usr/libexec/crewship/crewship-agent-cli ;;
    install|upgrade) cli=${DPKG_ROOT:-}/usr/libexec/crewship/crewship-agent-cli ;;
    *) exit 0 ;;
esac
[ -d "$cli" ] && [ ! -L "$cli" ] || exit 0
old="$cli/crewship-agent-cli"
if [ "$1" != 2 ]; then
    # dpkg replaces the legacy directory during unpack. Refuse before it
    # can displace local additions, including dotfiles and dangling links.
    extra=$(find "$cli" -mindepth 1 -maxdepth 1 ! -name crewship-agent-cli -print -quit)
    if [ -n "$extra" ] || [ ! -f "$old" ] || [ -L "$old" ]; then
        echo "Move local files out of $cli before upgrading Crewship" >&2
        exit 1
    fi
    exit 0
fi
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
