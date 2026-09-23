# AWX/Omarchy diagnostics: implementation handoff (2026-09-23)

Base: `origin/main` at `ce7710274`. Issue: #2656. This report describes the branch `feat/run-evidence-diagnostics`; merge and deployment are separate states, not implied here.

## Delivered

- `journal.WithRunID` now enters the routine execution context once the run ID exists. New agent/tool emissions from that context use the run ID as `trace_id` unless an entry explicitly names another trace. Nested routines overwrite the context with their own ID. A migrated-database test executes two concurrent runs of the same agent, writes through the real journal Writer, and queries each run by `run_id` to prove their tool entries do not mix.
- `lib/run-evidence.ts` is a pure, versioned, allowlisted projection. The JSON fixture in `lib/__fixtures__/run-evidence.json` is intended as the shared contract for a later server-side O3 builder. It exports typed references, times, status/outcome, recognized failure kind, trigger, recipe version/hash and selected static event facts. The entire final UTF-8 JSON is bounded to 16 KiB, with at most 20 events and a truncation marker. A terminal event is retained when the event count is capped. Raw `summary`, `payload`, inputs, output, metadata, prompts, error text and span/tool I/O are never exported.
- The routine detail now previews and copies that exact evidence text, copies its Activity link, shows the latest exported event and admits unavailable or incomplete journal reads. No click in this panel starts a run, chat or repair.
- `/activity?run=<id>` checks the existing `GET /runs/{id}` kind. Agent runs render a light process detail with agent/issue links, exit code, bounded evidence and the run-filtered activity timeline. Routine runs keep their existing detail. A 404 probes only the workspace-scoped routine record; 403/500 stop with an error and retry, with no broader-data fallback.
- The issue run card displays the assignment ID separately from the run ID. Missing `run_id` now says "run not recorded"; it does not assert that the agent never started.
- The previously local AWX/Omarchy PRD, implementation plan, Omarchy research, original diagnostics handoff and opponent reports are added to the tracked `docs/prd/` tree, making their links reviewable in the repository.

## Verification

- `go test ./internal/pipeline ./internal/journal -count=1`: pass. The new migrated-database correlation test passes.
- Targeted `internal/api` tests for run GET/list, routine run and journal run-id parameters: pass.
- `go vet ./...`: pass.
- Full frontend Vitest: 778 files, 9,275 tests passed. `pnpm exec tsc --noEmit`, `pnpm build` and `pnpm lint`: pass (lint reports existing warnings, no errors).
- A full `internal/api` package run reached its 10-minute timeout amid a very large parallel test/goroutine load; its targeted run/journal tests passed. This is a verification limit, not a claim that every API test passed.

## Remaining work and limits

- This is an evidence **projection**, not an API permission boundary. It uses the existing workspace-authorized run/journal endpoints; a scoped agent read tool and server builder (O3) need separate authorization, parity fixtures and review. Existing VIEWER access to raw run data is not changed.
- Old journal entries without per-run context are not retroactively correlated. The writer still treats an emit failure as best effort; absence of a record is not proof that no work happened. Sidecar/LLM events without reliable run ID remain outside the export. A fresh read is available through **Refresh evidence**; the panel does not claim to be a live process monitor.
- The agent detail reads the run aggregate, while the issue card reads assignment outcome. A deep link without an authoritative assignment cannot claim that outcome; the issue link is the path back to it. No new read endpoint or retry engine was added.
- Iterations 6–10 remain open: richer trusted provenance and historical credential declaration, compare-and-set version choice for Run again, backend access/me verdicts, credential dependents, and Page-to-chat draft handoff. The product decisions listed in the PRD remain unresolved. Final two-account read/write/revoke and integrated Page→run→result→evidence acceptance have not been performed.
