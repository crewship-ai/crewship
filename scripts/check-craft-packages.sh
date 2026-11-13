#!/usr/bin/env bash
# check-craft-packages.sh <catalog> <directory>
#
# The release gate between the Word and Sheet packages and the binaries that
# trust them (#5315). <catalog> is "<application>=<version>=<manifest sha256>"
# comma separated, exactly what the binaries are built with. It passes only
# when every pin names a known application once, and the directory holds
# exactly one package per pin whose manifest.json has that digest, so a binary
# never trusts a package that is not published and nothing unpinned is
# published. The rules match internal/craftassets.ParsePins.
set -euo pipefail

catalog="${1:?catalog}"
dir="${2:?directory}"
fail() { echo "::error::$*" >&2; exit 1; }

IFS=, read -ra pins <<<"$catalog"
[ "${#pins[@]}" -ge 1 ] || fail "empty catalog"
declare -A seen=()
for pin in "${pins[@]}"; do
  [[ "$pin" =~ ^(wordcraft|gridcraft)=([A-Za-z0-9.+_-]{1,64})=([0-9a-f]{64})$ ]] || fail "malformed pin: $pin"
  app="${BASH_REMATCH[1]}"; sha="${BASH_REMATCH[3]}"
  [ -z "${seen[$app]:-}" ] || fail "$app is pinned twice"
  seen[$app]=1
  pkg="$dir/crewship-craft-${app}.craftpkg"
  [ -f "$pkg" ] || fail "no package for pin $app"
  [ "$(tar -xOf "$pkg" manifest.json | sha256sum | cut -d' ' -f1)" = "$sha" ] || fail "$pkg is not the pinned package"
done
shopt -s nullglob
packages=("$dir"/*)
[ "${#packages[@]}" -eq "${#pins[@]}" ] || fail "${#pins[@]} pins but ${#packages[@]} files in $dir"
echo "Craft packages match the catalog: $catalog"
