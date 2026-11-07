#!/usr/bin/env bash
# Verify the text worker image's runner checksum and legal files from the host.
# The image is built from scratch and has no shell, so nothing runs inside it.
set -euo pipefail
if [[ $# != 1 || -z $1 ]]; then
  echo 'usage: scripts/ci/verify-text-image-contents.sh IMAGE' >&2
  exit 2
fi
work=$(mktemp -d)
container=
cleanup() {
  if [[ -n $container ]]; then docker rm -f "$container" >/dev/null 2>&1 || true; fi
  rm -rf -- "$work"
}
trap cleanup EXIT
container=$(docker create --network none "$1")
for path in /usr/share/licenses/crewship-text/go/manifest.tsv \
  /usr/share/licenses/crewship-text/project/THIRD-PARTY-NOTICES.md \
  /usr/share/crewship-text/release-source.json \
  /usr/share/crewship-text/runner.sha256; do
  docker cp -q "$container:$path" "$work/$(basename "$path")"
  test -s "$work/$(basename "$path")" || { echo "empty $path" >&2; exit 1; }
done
docker cp -q "$container:/opt/crewship-runner" "$work/crewship-runner"
(cd "$work" && sha256sum -c runner.sha256)
if docker cp -q "$container:/bin/sh" "$work/sh" 2>/dev/null; then
  echo 'text worker image unexpectedly contains /bin/sh' >&2
  exit 1
fi
