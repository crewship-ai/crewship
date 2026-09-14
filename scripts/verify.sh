#!/usr/bin/env bash
# Shared local pre-push entrypoint. Does not start or reset a dev instance.
set -euo pipefail
cd "$(dirname "$0")/.."
mode=${1:-quick}
case "$mode" in quick|go|full) ;; *) echo 'usage: scripts/verify.sh [quick|go|full]' >&2; exit 2 ;; esac
printf 'Verifying %s (dirty=%s)\n' "$(git rev-parse HEAD)" "$(scripts/build-stamp.sh dirty)"
python3 -m unittest discover -s scripts/ci -p 'test_*.py'
bash scripts/go-toolchain-pin.sh
bash scripts/pr-image-build-paths.sh
if command -v actionlint >/dev/null; then
  actionlint -shellcheck='' -pyflakes=''
else
  go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 -shellcheck='' -pyflakes=''
fi
if [ "$mode" != quick ]; then
  bash scripts/ci/go-test.sh ./... -count=1 -timeout 25m
  go vet ./...
fi
if [ "$mode" = full ]; then
  pnpm lint
  pnpm exec tsc --noEmit
  pnpm test:coverage
  NEXTAUTH_SECRET=build-time-placeholder pnpm build
fi
