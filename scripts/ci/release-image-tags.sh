#!/usr/bin/env bash
# release-image-tags.sh <release tag> <image base>
#
# Prints the image tags a release promotes, one per line:
#   <base>:<tag>             always (v1.2.3, v1.2.3-rc.1)
#   <base>:vX.Y, <base>:latest  only for a stable vX.Y.Z
# Moving a stable alias onto a beta/rc image would silently downgrade
# everyone pinned to vX.Y or `latest`, so prereleases get only their own tag.
#
# release.yml computes this list in the job that uses it. It must not be a
# job output: GitHub drops a job output that contains a secret's value, and a
# tag such as ghcr.io/crewship-ai/... was dropped that way (v0.1.0-rc.2).
# Refuses an empty or malformed tag or base and prints nothing then.
set -euo pipefail

[ "$#" -eq 2 ] || { echo "usage: $0 <release tag> <image base>" >&2; exit 2; }
tag="$1"
base="$2"

# The same rule as the verified job's release tag check.
if ! [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || [ "${#tag}" -gt 128 ]; then
  echo "::error::malformed release tag: '$tag'" >&2
  exit 1
fi
if ! [[ "$base" =~ ^[a-z0-9][a-z0-9._/-]*$ ]]; then
  echo "::error::malformed image base: '$base'" >&2
  exit 1
fi

echo "${base}:${tag}"
if [[ "$tag" =~ ^(v[0-9]+\.[0-9]+)\.[0-9]+$ ]]; then
  echo "${base}:${BASH_REMATCH[1]}"
  echo "${base}:latest"
fi
