#!/usr/bin/env bash
# Run through crewship-heavy with WORDCRAFT_CE_* environment inputs; see e2e/wordcraft-ce/README.md.
# Mutates the explicitly configured disposable document through normal UI only.
set -euo pipefail
exec pnpm exec playwright test --config playwright.wordcraft-ce.config.ts e2e/wordcraft-ce/latency.acceptance.ts "$@"
