#!/usr/bin/env bash
# check-release-artifacts.sh — assert the DISTRIBUTED artifacts contain the
# dependency license texts listed in the shipped manifests, with matching
# SHA-256 — not just that the YAML says so.
#
# Channels are chosen explicitly and independently — an image check never
# requires local dist files, a dist check never requires a local image:
#
#   scripts/check-release-artifacts.sh --dist dist          # archives+deb+rpm
#   scripts/check-release-artifacts.sh --image ghcr.io/…@sha256:…
#
# Every channel reads the license manifests FROM the artifact itself
# (archives: LICENSES/{go,frontend}/manifest.tsv; deb/rpm: the flat
# go-manifest.tsv/npm-manifest.tsv; image: licenses/{go,frontend}/manifest.tsv)
# and verifies each listed text exists and hashes correctly. A missing or
# altered text fails the channel; an empty manifest fails too.
#
# --dist DIR   checks the full archive matrix (tar.gz AND zip), every deb
#              and every rpm in DIR. In --strict mode a missing channel is
#              a failure; without it, it is reported as UNVERIFIED.
# --image REF  verifies an immutable image reference (prefer name@digest)
#              per distributed platform (linux/amd64, linux/arm64).
#
# VERIFIED is printed only for a channel (or platform) whose every check
# passed. No grep -q on long listings (pipefail/SIGPIPE-safe).
set -euo pipefail
shopt -s nullglob

STRICT=0
DIST=""
IMAGE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --strict) STRICT=1; shift ;;
    --dist)
      if [[ $# -lt 2 || -z "${2:-}" || "${2:-}" == --* ]]; then echo "usage: --dist needs a directory" >&2; exit 2; fi
      DIST="$2"; shift 2 ;;
    --dist=*) DIST="${1#*=}"; [ -n "$DIST" ] || exit 2; shift ;;
    --image)
      if [[ $# -lt 2 || -z "${2:-}" || "${2:-}" == --* ]]; then echo "usage: --image needs a reference" >&2; exit 2; fi
      IMAGE="$2"; shift 2 ;;
    --image=*) IMAGE="${1#*=}"; [ -n "$IMAGE" ] || exit 2; shift ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "usage: $0 [--strict] (--dist DIR | --image REF)" >&2; exit 2 ;;
  esac
done

if [ -z "$DIST" ] && [ -z "$IMAGE" ]; then
  echo "check-release-artifacts: no channel selected — pass --dist DIR and/or --image REF" >&2
  exit 2
fi

FAIL=0
fail() { echo "FAIL  $*" >&2; FAIL=1; }
note_unverified() {
  echo "UNVERIFIED  $1: $2" >&2
  if [ "$STRICT" -eq 1 ]; then
    echo "  (--strict: unverified channel fails)" >&2
    FAIL=1
  fi
  return 0
}

sanitize() { printf '%s' "$1" | tr '/@' '__'; }

# verify_manifest_dir <dir> <go-manifest> <npm-manifest> <label>
#   go-manifest rows: module \t version \t file \t provenance \t sha256
#   npm-manifest rows: name \t version \t file \t sha256
# Files are expected at <dir>/go/<module>/<file> and
# <dir>/frontend/texts/<name>@<version>/<file>.
verify_manifest_dir() {
  local dir="$1" gom="$2" npmm="$3" label="$4"
  local gorows=0 npmrows=0 bad=0
  if [ ! -s "$gom" ]; then
    fail "$label: go manifest missing/empty"
    bad=1
  else
    while IFS=$'\t' read -r mod ver file _prov sum; do
      [ -n "$mod" ] || continue
      gorows=$((gorows + 1))
      local f="$dir/go/$mod/$file"
      if [ ! -f "$f" ]; then
        fail "$label: missing go text $mod/$file"; bad=1; continue
      fi
      if [ "$(sha256sum "$f" | cut -d' ' -f1)" != "$sum" ]; then
        fail "$label: hash mismatch for $mod/$file"; bad=1
      fi
    done < <(tail -n +2 "$gom")
  fi
  if [ ! -s "$npmm" ]; then
    fail "$label: npm manifest missing/empty"
    bad=1
  else
    while IFS=$'\t' read -r name ver file sum; do
      [ -n "$name" ] || continue
      npmrows=$((npmrows + 1))
      local f="$dir/frontend/texts/$name@$ver/$file"
      if [ ! -f "$f" ]; then
        fail "$label: missing npm text $name@$ver/$file"; bad=1; continue
      fi
      if [ "$(sha256sum "$f" | cut -d' ' -f1)" != "$sum" ]; then
        fail "$label: hash mismatch for $name@$ver/$file"; bad=1
      fi
    done < <(tail -n +2 "$npmm")
  fi
  # A manifest with zero data rows is as bad as none.
  [ "$gorows" -gt 0 ] || { fail "$label: go manifest has no rows"; bad=1; }
  [ "$npmrows" -gt 0 ] || { fail "$label: npm manifest has no rows"; bad=1; }
  if [ "$bad" = 0 ]; then
    echo "    $label: $gorows go texts + $npmrows npm texts match their manifests (sha256)"
  else
    return 1
  fi
}

# verify_flat_dir <dir> <go-manifest> <npm-manifest> <label>
#   nfpm flat layout: go_<sanitized module>.<file>, npm_<sanitized name@ver>.<file>
verify_flat_dir() {
  local dir="$1" gom="$2" npmm="$3" label="$4"
  local bad=0 n=0 npmrows=0
  for manifest in "$gom" "$npmm"; do
    [ -s "$manifest" ] || { fail "$label: manifest missing/empty: $manifest"; return 1; }
  done
  while IFS=$'\t' read -r mod ver file _prov sum; do
    [ -n "$mod" ] || continue
    n=$((n + 1))
    local f
    f="$dir/licenses/go_$(sanitize "$mod").$file"
    if [ ! -f "$f" ]; then
      fail "$label: missing flat go text for $mod ($file)"; bad=1; continue
    fi
    if [ "$(sha256sum "$f" | cut -d' ' -f1)" != "$sum" ]; then
      fail "$label: flat hash mismatch for $mod/$file"; bad=1
    fi
  done < <(tail -n +2 "$gom")
  while IFS=$'\t' read -r name ver file sum; do
    [ -n "$name" ] || continue
    npmrows=$((npmrows + 1))
    local f
    f="$dir/licenses/npm_$(sanitize "$name@$ver").$file"
    if [ ! -f "$f" ]; then
      fail "$label: missing flat npm text for $name@$ver/$file"; bad=1; continue
    fi
    if [ "$(sha256sum "$f" | cut -d' ' -f1)" != "$sum" ]; then
      fail "$label: flat hash mismatch for $name@$ver/$file"; bad=1
    fi
  done < <(tail -n +2 "$npmm")
  [ "$n" -gt 0 ] || { fail "$label: flat go manifest has no rows"; bad=1; }
  [ "$npmrows" -gt 0 ] || { fail "$label: flat npm manifest has no rows"; bad=1; }
  if [ "$bad" = 0 ]; then
    echo "    $label: flat texts match the shipped manifests (sha256)"
  else
    return 1
  fi
}

WORK="$(mktemp -d)"
IMAGE_CIDS=""
cleanup() {
  local c
  for c in $IMAGE_CIDS; do timeout 60 docker rm "$c" >/dev/null 2>&1 || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

# ── dist channel ───────────────────────────────────────────────────────────
check_dist() {
  local DIST="$1"
  if [ ! -d "$DIST" ]; then
    note_unverified dist "directory '$DIST' does not exist"
    return 0
  fi

  DIST="$(cd "$DIST" && pwd)"
  local -a tars=("$DIST"/*.tar.gz) zips=("$DIST"/*.zip)
  if [ "$((${#tars[@]} + ${#zips[@]}))" -eq 0 ]; then
    note_unverified archives "no tar.gz/zip in '$DIST'"
  else
    local a ok=1
    if [ "$STRICT" -eq 1 ] && [ "${#tars[@]}" -eq 0 ]; then ok=0; fail "no .tar.gz archives in '$DIST' (release matrix ships tar.gz)"; fi
    if [ "$STRICT" -eq 1 ] && [ "${#zips[@]}" -eq 0 ]; then ok=0; fail "no .zip archives in '$DIST' (release matrix ships zip for windows)"; fi
    for a in "${tars[@]}" "${zips[@]}"; do
      local x="$WORK/arch"
      rm -rf "$x"; mkdir -p "$x"
      case "$a" in
        *.tar.gz)
          tar xzf "$a" -C "$x" LICENSE NOTICE THIRD-PARTY-NOTICES.md LICENSES 2>/dev/null \
            || { fail "$(basename "$a"): LICENSES/legal files not extractable"; ok=0; continue; } ;;
        *.zip)
          unzip -q "$a" 'LICENSE' 'NOTICE' 'THIRD-PARTY-NOTICES.md' 'LICENSES/*' -d "$x" 2>/dev/null \
            || { fail "$(basename "$a"): LICENSES/legal files not extractable"; ok=0; continue; } ;;
      esac
      local req
      for req in LICENSE NOTICE THIRD-PARTY-NOTICES.md; do
        [ -s "$x/$req" ] || { fail "$(basename "$a") missing $req"; ok=0; }
      done
      verify_manifest_dir "$x/LICENSES" "$x/LICENSES/go/manifest.tsv" "$x/LICENSES/frontend/manifest.tsv" "$(basename "$a")" || ok=0
    done
    [ "$ok" = 1 ] && echo "VERIFIED    archives in $DIST ($((${#tars[@]} + ${#zips[@]})) files): legal files + all manifest texts hash-match"
  fi

  local -a debs=("$DIST"/*.deb)
  if [ "${#debs[@]}" -eq 0 ]; then
    note_unverified deb "no .deb in '$DIST'"
  elif ! command -v dpkg-deb >/dev/null 2>&1; then
    note_unverified deb "dpkg-deb not installed"
  else
    local d ok=1
    for d in "${debs[@]}"; do
      local x="$WORK/deb"
      rm -rf "$x"; mkdir -p "$x"
      dpkg-deb -x "$d" "$x" 2>/dev/null || { fail "$(basename "$d") not extractable"; ok=0; continue; }
      local req
      for req in usr/share/doc/crewship/LICENSE usr/share/doc/crewship/NOTICE \
                 usr/share/doc/crewship/THIRD-PARTY-NOTICES.md; do
        [ -s "$x/$req" ] || { fail "$(basename "$d") missing /$req"; ok=0; }
      done
      verify_flat_dir "$x/usr/share/doc/crewship" "$x/usr/share/doc/crewship/licenses/go-manifest.tsv" \
        "$x/usr/share/doc/crewship/licenses/npm-manifest.tsv" "$(basename "$d")" || ok=0
    done
    [ "$ok" = 1 ] && echo "VERIFIED    deb in $DIST (${#debs[@]} files): legal files + flat texts hash-match"
  fi

  local -a rpms=("$DIST"/*.rpm)
  if [ "${#rpms[@]}" -eq 0 ]; then
    note_unverified rpm "no .rpm in '$DIST'"
  elif command -v rpm2cpio >/dev/null 2>&1 && command -v cpio >/dev/null 2>&1; then
    local r ok=1
    for r in "${rpms[@]}"; do
      local x="$WORK/rpm"
      rm -rf "$x"; mkdir -p "$x"
      # --no-absolute-filenames: nfpm payloads carry absolute paths;
      # without it cpio writes to the REAL /usr (and fails on permissions).
      # rpm2cpio exits 1 even on success, so extraction success is judged by
      # the outcome (the payload tree landing in $x), not the pipeline status.
      ( cd "$x" && rpm2cpio "$r" 2>/dev/null | cpio -id --quiet --no-absolute-filenames ) || true
      [ -d "$x/usr/share/doc/crewship/licenses" ] \
        || { fail "$(basename "$r") not extractable"; ok=0; continue; }
      local req
      for req in usr/share/doc/crewship/LICENSE usr/share/doc/crewship/NOTICE \
                 usr/share/doc/crewship/THIRD-PARTY-NOTICES.md; do
        [ -s "$x/$req" ] || { fail "$(basename "$r") missing /$req"; ok=0; }
      done
      verify_flat_dir "$x/usr/share/doc/crewship" "$x/usr/share/doc/crewship/licenses/go-manifest.tsv" \
        "$x/usr/share/doc/crewship/licenses/npm-manifest.tsv" "$(basename "$r")" || ok=0
    done
    [ "$ok" = 1 ] && echo "VERIFIED    rpm in $DIST (${#rpms[@]} files): legal files + flat texts hash-match (payload extracted)"
  else
    note_unverified rpm "rpm2cpio/cpio not installed; CI installs rpm"
  fi
}

# ── image channel ──────────────────────────────────────────────────────────
check_image() {
  local IMAGE="$1"
  if ! command -v docker >/dev/null 2>&1; then
    note_unverified image "docker not installed"
    return 0
  fi
  # Classic Docker stores cannot import both architectures under one index
  # digest. Resolve children from the supplied immutable index, retaining
  # IMAGE as the publication/signature identity. Local tags remain supported.
  local manifest="$WORK/image-index.json"
  if [[ "$IMAGE" == *@* ]]; then
    if ! timeout 120 docker manifest inspect "$IMAGE" > "$manifest"; then
      note_unverified image "cannot inspect immutable image $IMAGE"
      return 0
    fi
  fi
  local platforms="linux/amd64 linux/arm64"
  local p verified=""
  for p in $platforms; do
    local platform_image="$IMAGE"
    if [[ "$IMAGE" == *@* ]]; then
      if ! platform_image="$(python3 "$(dirname "$0")/ci/image-platform.py" "$IMAGE" "$p" < "$manifest")"; then
        note_unverified "image $p" "cannot resolve platform from $IMAGE"
        continue
      fi
      echo "    image $p $platform_image (index $IMAGE)"
    fi
    local cid="" errout
    errout="$WORK/create-err-$RANDOM"
    if ! cid="$(timeout 120 docker create --platform "$p" "$platform_image" /bin/sh 2>"$errout")" || [ -z "$cid" ]; then
      # docker create only resolves the platform manifest; failure usually
      # means the reference has no such platform (e.g. a local single-arch
      # tag) or the pull failed — the stderr above says which.
      echo "UNVERIFIED  image: $p unavailable for $platform_image (source $IMAGE):" >&2
      sed 's/^/    /' "$errout" >&2 || true
      if [ "$STRICT" -eq 1 ]; then FAIL=1; fi
      continue
    fi
    IMAGE_CIDS="$IMAGE_CIDS $cid"
    local x="$WORK/img-$RANDOM"
    mkdir -p "$x"
    local pok=1 req
    for req in LICENSE NOTICE; do
      if ! timeout 60 docker cp "$cid:/usr/share/doc/crewship/$req" "$x/$req" >/dev/null 2>&1 || [ ! -s "$x/$req" ]; then
        fail "image $IMAGE ($p): missing/empty project $req"; pok=0
      fi
    done
    timeout 300 docker cp "$cid:/usr/share/doc/crewship/licenses/go" "$x/go" >/dev/null 2>&1 \
      || { fail "image $IMAGE ($p): cannot read /usr/share/doc/crewship/licenses/go"; pok=0; }
    timeout 300 docker cp "$cid:/usr/share/doc/crewship/licenses/frontend" "$x/frontend" >/dev/null 2>&1 \
      || { fail "image $IMAGE ($p): cannot read /usr/share/doc/crewship/licenses/frontend"; pok=0; }
    if [ "$pok" = 1 ]; then
      if [ ! -s "$x/go/manifest.tsv" ] || [ ! -s "$x/frontend/manifest.tsv" ]; then
        fail "image $IMAGE ($p): manifests missing under licenses/"; pok=0
      fi
      verify_manifest_dir "$x" "$x/go/manifest.tsv" "$x/frontend/manifest.tsv" "image $p" || pok=0
    fi
    [ "$pok" = 1 ] && verified="$verified $p"
  done
  if [ -n "$verified" ]; then
    echo "VERIFIED    image $IMAGE: legal files + manifest texts hash-match on:$verified"
  fi
}

[ -n "$DIST" ] && check_dist "$DIST"
[ -n "$IMAGE" ] && check_image "$IMAGE"

if [ "$FAIL" -eq 1 ]; then
  echo "check-release-artifacts: FAILURES present" >&2
  exit 1
fi
echo "check-release-artifacts: done (channels above; any UNVERIFIED is explicit)"
