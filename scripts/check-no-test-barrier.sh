#!/bin/sh
# check-no-test-barrier.sh — fail if any Go binary under the given paths was
# built with the crewship_testbarrier tag. That tag compiles a test-only hook
# that can park workbook operations; it must never reach a release.
#
#   scripts/check-no-test-barrier.sh dist            # every Go binary in dist
#   scripts/check-no-test-barrier.sh /crewship       # one binary (image build)
#
# Reads build settings with `go version -m`. Symlinks are followed. Readable
# non-Go files found inside a directory are skipped, but a file named
# explicitly must be a readable Go binary, an unreadable file anywhere fails,
# and at least one Go binary must be found, so a wrong path cannot pass
# silently.
# POSIX sh on purpose: the Docker build stage (Alpine) runs it too.
set -eu

if [ $# -eq 0 ]; then
  echo "usage: $0 PATH..." >&2
  exit 2
fi
command -v go >/dev/null 2>&1 || { echo "FAIL  go not found" >&2; exit 1; }

# A file named explicitly is meant to be checked: it must be a readable Go
# binary. Only files found by walking a directory may be skipped.
explicit_bad=0
for arg in "$@"; do
  if [ ! -d "$arg" ] && ! go version -m "$arg" >/dev/null 2>&1; then
    echo "FAIL  $arg is not a readable Go binary" >&2
    explicit_bad=1
  fi
done
[ "$explicit_bad" -eq 0 ] || exit 1

list="$(mktemp)"
trap 'rm -f "$list"' EXIT
find -L "$@" -type f > "$list"

checked=0
bad=0
while IFS= read -r file; do
  if [ ! -r "$file" ]; then
    echo "FAIL  $file is not readable, cannot prove it is clean" >&2
    bad=1
    continue
  fi
  info="$(go version -m "$file" 2>/dev/null)" || continue
  checked=$((checked + 1))
  tags="$(printf '%s\n' "$info" | awk '$1 == "build" && $2 ~ /^-tags=/ { sub(/^-tags=/, "", $2); print $2 }')"
  case ",$tags," in
    *,crewship_testbarrier,*)
      echo "FAIL  $file was built with -tags=$tags (test barrier compiled in)" >&2
      bad=1 ;;
  esac
done < "$list"

if [ "$checked" -eq 0 ]; then
  echo "FAIL  no Go binaries found under: $*" >&2
  exit 1
fi
if [ "$bad" -ne 0 ]; then
  exit 1
fi
echo "ok  $checked Go binaries built without crewship_testbarrier"
