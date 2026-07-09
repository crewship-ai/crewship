#!/bin/sh
# Pre-remove scriptlet for the crewship .deb / .rpm packages.
#
# Stops and disables the service ONLY on a real uninstall, never on the
# remove-half of an upgrade (the postinstall try-restart handles upgrades).
# Argument conventions differ by distro:
#   deb: $1 = "remove" | "purge" | "upgrade" | "deconfigure" ...
#   rpm: $1 = count of versions that will remain — "0" = final erase, >=1 = upgrade
set -e

is_final_removal=0
case "$1" in
    remove|purge|0)
        is_final_removal=1
        ;;
esac

if [ "${is_final_removal}" = "1" ] && command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now crewship.service >/dev/null 2>&1 || true
fi

exit 0
