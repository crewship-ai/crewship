#!/usr/bin/env bash
# Prepare verified public inputs. Builds run through the host build queue.
set -euo pipefail
cd "$(dirname "$0")/../.."
archive=$(mktemp)
trap 'rm -f -- "$archive"' EXIT
curl --fail --location --retry 3 --connect-timeout 15 --max-time 180 \
  https://github.com/storytold/gridcraft/releases/download/v0.10.0/gridcraft-0.10.0-linux-x86_64.tar.gz --output "$archive"
printf '%s  %s\n' b78a28605cd34594fc35dfc1ad36a924054571a1382625fb7506fd70cdfb9197 "$archive" | sha256sum --check --strict
for member in bin/gridcraft-cli share/doc/gridcraft/LICENSE-APACHE share/doc/gridcraft/LICENSE-MIT; do
 tar -xOf "$archive" "gridcraft-0.10.0-linux-x86_64/$member" > "docker/gridcraft/${member##*/}"
done
printf '%s  %s\n' 000100860eb14b641da48e5e0d2dcd6dcc5ddcd65a4b267a0ef2fcdfadcdd984 docker/gridcraft/gridcraft-cli | sha256sum --check --strict
chmod 0555 docker/gridcraft/gridcraft-cli
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o docker/gridcraft/crewship-gridcraft-worker ./cmd/crewship-gridcraft-worker
