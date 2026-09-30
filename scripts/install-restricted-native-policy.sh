#!/usr/bin/env bash
# Explicit operator action; installs only Crewship's uniquely named native policy.
set -euo pipefail
[[ $# == 0 && $(id -u) == 0 ]]
repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
source_policy="$repo_dir/internal/restrictedruntime/nativepolicy/apparmor.profile"
/usr/sbin/apparmor_parser --skip-kernel-load --skip-read-cache "$source_policy"
install -m 0644 -- "$source_policy" /etc/apparmor.d/crewship-native-codex-v159
/usr/sbin/apparmor_parser --replace --skip-read-cache /etc/apparmor.d/crewship-native-codex-v159
