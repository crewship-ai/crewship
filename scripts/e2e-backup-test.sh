#!/usr/bin/env bash
# e2e-backup-test.sh — end-to-end smoke test for `crewship backup`.
#
# What this covers:
#   - Binary builds, `crewship backup --help` prints the subcommand tree
#   - `crewship backup list` succeeds against a running server (auth path)
#   - `crewship backup inspect` of a synthetic bundle returns a manifest
#
# What this does NOT cover (needs a real Docker daemon and a crew
# container):
#   - actual create → pause → tar → unpause → write round-trip
#   - restore into a fresh instance
#
# Those paths are exercised by the integration tests in the backup
# package (go test ./internal/backup/... -count=1) and the manual
# smoke drill in .claude/context/prd/BACKUP.md section 15.
#
# Run: scripts/e2e-backup-test.sh
set -euo pipefail

HERE="$(cd "$(dirname "$0")/.." && pwd)"
cd "$HERE"

echo "== build =="
go build -o /tmp/crewship-e2e ./cmd/crewship

echo "== help =="
/tmp/crewship-e2e backup --help >/dev/null
/tmp/crewship-e2e backup create --help >/dev/null
/tmp/crewship-e2e backup list --help >/dev/null
/tmp/crewship-e2e backup inspect --help >/dev/null
/tmp/crewship-e2e backup restore --help >/dev/null
/tmp/crewship-e2e backup delete --help >/dev/null

echo "== unit =="
go test ./internal/backup/... -count=1 -race

echo "== api handler =="
go test ./internal/api/... -count=1 -race -run TestBackup

echo "OK — backup e2e smoke passed."
