#!/usr/bin/env bash
# Regression test for the #974 S6 private-IP filter in init-firewall.sh.
# No bats harness exists in-repo, so this is a self-contained assert script:
# it extracts the is_private_ip function from init-firewall.sh (the rest of
# that script runs iptables and can't be sourced) and checks the CIDR matrix.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# Extract just the is_private_ip function definition.
eval "$(awk '/^is_private_ip\(\) \{/,/^\}/' "$here/init-firewall.sh")"

fail=0
check() { # ip expected(0=private,1=public)
    if is_private_ip "$1"; then got=0; else got=1; fi
    if [ "$got" != "$2" ]; then
        echo "FAIL: is_private_ip($1) = $got, want $2" >&2
        fail=1
    fi
}

# Private / loopback / link-local / metadata → must be filtered (0).
check 10.0.0.5 0
check 10.255.255.255 0
check 127.0.0.1 0
check 169.254.169.254 0   # cloud IMDS
check 169.254.1.1 0       # link-local
check 192.168.1.222 0
check 172.16.0.1 0
check 172.20.5.5 0
check 172.31.255.255 0
# Public → must be allowed (1).
check 8.8.8.8 1
check 1.1.1.1 1
check 140.82.112.3 1      # github
check 172.15.0.1 1        # just below the 172.16/12 block
check 172.32.0.1 1        # just above the 172.16/12 block

if [ "$fail" = 0 ]; then echo "OK: is_private_ip matrix passed"; fi
exit "$fail"
