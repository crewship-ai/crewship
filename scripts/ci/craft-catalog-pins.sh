#!/usr/bin/env bash
# craft-catalog-pins.sh <catalog.txt>
#
# Prints "pins=<catalog>" for $GITHUB_OUTPUT from the committed Word and Sheet
# catalog. release.yml reads it in each job that needs the pins instead of
# passing them as a job output, which GitHub drops when the value contains a
# secret's value (a version may carry a hyphen). craft-components proves the
# build produced exactly this file at the same commit.
set -euo pipefail
pins="$(tr -d '\n' < "${1:?catalog.txt}")"
[[ "$pins" =~ ^[a-z]+=[A-Za-z0-9.+_-]+=[0-9a-f]{64}(,[a-z]+=[A-Za-z0-9.+_-]+=[0-9a-f]{64})*$ ]] || {
  echo "::error::malformed craft catalog: $1" >&2; exit 1; }
echo "pins=$pins"
