#!/usr/bin/env bash
# gen-license-bundle.sh — build build/licenses/ for distribution artifacts.
#
# Produces, under build/licenses/ (gitignored; generated on demand):
#   go/<module>/…   license/NOTICE texts of every Go module linked into ANY
#                   binary of the distributed GOOS/tags matrix
#                   (tools/gen-licenses — fails on a missing text)
#   frontend/…      the npm dependency texts of the embedded Next.js build
#                   plus npm-licenses.json inventory
#                   (scripts/gen-frontend-licenses.mjs — fails on a missing
#                   text; reads the lockfile-installed node_modules)
#   project/        copies of LICENSE, NOTICE, THIRD-PARTY-NOTICES.md
#   manifest.tsv    module → copied files (written by tools/gen-licenses)
#
# `go` and `frontend` subcommands generate only their part; the Docker build
# uses them per stage. Archives/packages get the whole tree via goreleaser
# (LICENSES/) and nfpm (build/licenses-flat/). Regenerate after dependency
# changes; `make licenses` wraps this.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

MODE="${1:-all}"
OUT_ROOT="build/licenses"

case "$MODE" in
  go)
    go run ./tools/gen-licenses -out "$OUT_ROOT/go"
    exit 0
    ;;
  frontend)
    node scripts/gen-frontend-licenses.mjs "$OUT_ROOT/frontend"
    exit 0
    ;;
  all) ;;
  *) echo "usage: $0 [all|go|frontend]" >&2; exit 2 ;;
esac

command -v go >/dev/null 2>&1 || { echo "go toolchain required" >&2; exit 1; }
command -v node >/dev/null 2>&1 || { echo "node required for frontend texts" >&2; exit 1; }
[ -d node_modules ] || { echo "node_modules missing — run pnpm install first" >&2; exit 1; }

rm -rf "$OUT_ROOT" "build/licenses-flat"
mkdir -p "$OUT_ROOT"

go run ./tools/gen-licenses -out "$OUT_ROOT/go"
node scripts/gen-frontend-licenses.mjs "$OUT_ROOT/frontend"

mkdir -p "$OUT_ROOT/project"
cp LICENSE NOTICE THIRD-PARTY-NOTICES.md "$OUT_ROOT/project/"

# Flat copy for nfpm (deb/rpm): package contents globs cannot nest directory
# trees without basename collisions, so flatten EVERYTHING into uniquely
# named files (module/package path / and @ → _). Files only — a directory
# in the glob root would be flattened by nfpm and collide again.
# Flat copy for nfpm (deb/rpm), NAMED FROM THE MANIFESTS so that
# check-release-artifacts.sh can verify every distributed file against them:
#   go_<module sanitized>.<file>          (from go/manifest.tsv)
#   npm_<name@version sanitized>.<file>   (from frontend/manifest.tsv)
# Sanitization: '/' and '@' in the identifier become '_' (nfpm glob roots
# must be plain files with unique names).
FLAT="build/licenses-flat"
mkdir -p "$FLAT"
tail -n +2 "$OUT_ROOT/go/manifest.tsv" | while IFS=$'\t' read -r mod _ver file _prov _sum; do
  [ -f "$OUT_ROOT/go/$mod/$file" ] || { echo "flat: missing $mod/$file" >&2; exit 1; }
  cp "$OUT_ROOT/go/$mod/$file" "$FLAT/go_$(printf '%s' "$mod" | tr '/@' '__').$file"
done
tail -n +2 "$OUT_ROOT/frontend/manifest.tsv" | while IFS=$'\t' read -r name ver file _sum; do
  src="$OUT_ROOT/frontend/texts/$name@$ver/$file"
  [ -f "$src" ] || { echo "flat: missing $name@$ver/$file" >&2; exit 1; }
  cp "$src" "$FLAT/npm_$(printf '%s' "$name@$ver" | tr '/@' '__').$file"
done
cp "$OUT_ROOT/go/manifest.tsv" "$FLAT/go-manifest.tsv"
cp "$OUT_ROOT/frontend/manifest.tsv" "$FLAT/npm-manifest.tsv"
cp "$OUT_ROOT/frontend/npm-licenses.json" "$FLAT/"
cp LICENSE NOTICE THIRD-PARTY-NOTICES.md "$FLAT/"

echo "── license bundle ──"
echo "go modules:      $(tail -n +2 "$OUT_ROOT/go/manifest.tsv" | cut -f1 | sort -u | wc -l)"
echo "frontend pkgs:   $(node -e 'const j=require("./'$OUT_ROOT'/frontend/npm-licenses.json");console.log(j.length)')"
echo "frontend texts:  $(find "$OUT_ROOT/frontend/texts" -type f | wc -l)"
echo "flat files:      $(find "$FLAT" -type f | wc -l)"
echo "project files:   LICENSE NOTICE THIRD-PARTY-NOTICES.md"
