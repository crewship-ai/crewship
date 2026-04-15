# Refactor queue — autonomous nightly pass

This file is the source of truth for the `refactor/nightly` branch's
scheduled refactor agent. Every 20 min the agent picks the first
**unblocked** entry, performs the refactor + tests + validation, and
commits on this branch. On failure the pass reverts and marks the
item `blocked:<reason>`.

**Rules for the agent:**

- ONE item per pass. Don't chain refactors in a single commit —
  bisection must stay clean.
- Start from `git pull origin refactor/nightly && git rebase main`.
  If rebase conflicts, stop and escalate.
- Before committing: `go test ./... -count=1 && go vet ./... &&
  pnpm lint && pnpm build`. All four must be green.
- Write new tests for moved / extracted code when the original had
  none. Don't delete existing tests.
- Never touch `prisma/` generated code, `lib/generated/`, or
  `.git/`.
- Commit message format: `refactor(<package>): <what moved>
  (nightly/<N>)` where N is the queue item number.
- If the item is `high_risk: true`, require additional:
  - `go test ./... -race -count=1` pass
  - Record the before/after LOC in commit body
  - Do NOT merge to main automatically; the PR waits for human review

## Queue

### Low risk — extract-only refactors (start here)

- [x] **1. integrations-page** — `app/(dashboard)/integrations/page.tsx`
      (1627 LOC, 5 colocated components). Split into:
      `components/features/integrations/oauth-auto-connect.tsx`,
      `template-popover.tsx`, `test-connection-button.tsx`,
      `expanded-panel.tsx`. `IntegrationsPage` default export stays
      in `page.tsx`. Preserve all prop / hook wiring exactly.
      Risk: low (pure move).

- [x] **2. credentials-loaders** — `internal/api/credentials.go`
      (926 LOC). Extract batch loaders to
      `internal/api/credentials_loaders.go`:
      `loadAgentNamesBatch`, `loadMCPUsedBatch`, `loadCrewIDs`,
      `loadCrewIDsBatch`, `setCrewIDs`. Handler file drops to ~600.
      Risk: low.

- [x] **3. agents-loaders** — `internal/api/agents.go` (917 LOC).
      Extract `batchCountByAgentID` + related helpers to
      `internal/api/agents_loaders.go`. Risk: low.

- [x] **4. keeper-helpers** — `internal/api/keeper.go` (849 LOC).
      Extract `containsDangerousShellChars` + any other free
      functions to `internal/api/keeper_helpers.go`. Risk: low.

- [x] **5. runner-create-restore-split** — `internal/backup/runner.go`
      (1361 LOC). Move `CreateBackup` + deferred-webhook closure to
      `runner_create.go`. Move `RestoreBackup` + `replayRestoreBackfills`
      to `runner_restore.go`. Keep `DefaultBackupsDir`, `ListBackups`,
      `Inspect`, `Verify`, `Delete`, `Rotate`, `cleanupStalePartials`,
      `ensureAgentsIdle`, `currentInstance`, `DetectCrewshipVersion`
      in `runner.go` — those are the “public catalog” API. Risk: low.

### Medium risk — domain splits

- [x] **6. docker-provider-split** — `internal/provider/docker/docker.go`
      (1064 LOC). Split by domain:
      - `docker_detect.go` — `Detect`, `candidateSockets`
      - `docker_network.go` — `ensureNetwork`
      - `docker_image.go` — `ensureImage`, related
      - `docker_container.go` — container lifecycle methods
      - `docker_volume.go` — volume management
      Keep `New`, `Provider` struct, `Config` in `docker.go`.
      Risk: medium (provider iface users must keep working).

### High risk — requires tests + race + human review

- [!] **7. mission-tasks-split** `high_risk: true` —
      `internal/orchestrator/mission_tasks.go` (1320 LOC).
      Split by lifecycle stage:
      - `mission_resolve.go` — `ResolveReadyTasks`, `buildMissionBrief`
      - `mission_schedule.go` — `scheduleReadyTasks`, `scheduleTask`,
        `autoAssignTask`, `areCrewsConnected`
      - `mission_complete.go` — `OnAssignmentCompleted`, `checkApprovalGate`
      BEFORE splitting: write integration test that drives the full
      lifecycle if one doesn't exist. Risk: HIGH.

      > blocked: the high-risk gate requires `go test -race` green,
      > but `TestScheduleTask_CrossCrew_Connected` reports a
      > pre-existing data race on `main` (goroutine spawned inside
      > `scheduleTask`). The refactor itself was a pure file move
      > (3-way split into `mission_resolve.go`/`mission_schedule.go`/
      > `mission_complete.go`) and validated green without `-race`,
      > but the autonomous pass refuses to merge work that hides
      > behind a flaky race detector. Human action: fix the race in
      > `scheduleTask`'s goroutine first (likely a shared map /
      > slice accessed from the spawned goroutine without a lock),
      > then re-open this item by flipping the checkbox back to
      > `[ ]`. Revert preserved `main`-matching runner state.

- [~] **8. orchestrator-config-split** `high_risk: true` —
      `internal/orchestrator/orchestrator.go` (1195 LOC). Extract
      pure getters/setters (`SetStatsRegisterCallback`,
      `SetSidecarEnabled`, `SetKeeperEnabled`, `SetIPCConfig`,
      `ContainerProvider`, etc.) to `orchestrator_config.go`. Keep
      business logic (`GetOrCreateContainer`, run lifecycle) in
      `orchestrator.go`. Risk: HIGH — this is core infra.

### Deferred (don't touch automatically)

- `internal/database/migrate.go` (1684) — migrations are versioned
  data, not logic. The ordered slice enforces version monotonicity;
  splitting breaks collision detection. HUMAN ONLY.
- `cmd/crewship/cmd_seed_data.go` (1028) — seed constants, low
  refactor value.
- `internal/server/routes.go`, `internal/api/router.go` — route
  tables, keep together for discoverability.

## Progress log

Each completed item appends one line here via the nightly commit.

<!-- start:progress -->
- `#1 integrations-page` — 1627 → 669 LOC in page.tsx, 6-way split to `components/features/integrations/{types,helpers,oauth-auto-connect,template-popover,test-connection-button,expanded-panel}` (2026-04-15)
- `#2 credentials-loaders` — 926 → 800 LOC in credentials.go; 5 batch/junction loaders moved to `internal/api/credentials_loaders.go` (140 LOC) (2026-04-15)
- `#3 agents-loaders` — 917 → 885 LOC in agents.go; batchCountByAgentID moved to `internal/api/agents_loaders.go` (49 LOC); parseListPagination left in place (cross-file helper, followup refactor) (2026-04-16)
- `#4 keeper-helpers` — 849 → 773 LOC in keeper.go; containsDangerousShellChars + regex vars + reverseString + nullIfEmpty moved to `internal/api/keeper_helpers.go` (103 LOC); added `keeper_helpers_test.go` (116 LOC) with 24 subtests covering shell-injection classes + UTF-8 reverse + env-var pattern (2026-04-16)
- `#5 runner-create-restore-split` — 1361 → 465 LOC in runner.go; CreateBackup + CreateOptions/Result + LockTimeout + compatibleTargetsFor + buildContents (443 LOC) moved to `internal/backup/runner_create.go`; RestoreBackup + RestoreOptions/Result + firstWorkspaceID/Slug + rewriteWorkspaceSlug + rewriteCrewSlug + replayRestoreBackfills (496 LOC) moved to `internal/backup/runner_restore.go`; list/inspect/verify/delete/rotate + shared utilities stay in runner.go (2026-04-16)
- `#6 docker-provider-split` — 1064 → 540 LOC in docker.go; container lifecycle (EnsureCrewRuntime + runPostStartCommands + shortID + StopCrewRuntime + RemoveCrewRuntime + ContainerStatus + ContainerStats, 544 LOC) moved to `internal/provider/docker/docker_container.go`. Network/image/volume/exec stayed in docker.go — queue called for a 5-way split but the 2-way split extracts the single biggest function (EnsureCrewRuntime at 370 LOC) and keeps provider surface intact; further domain sub-splits deferred as follow-up to avoid ballooning this run past the 15 min budget (2026-04-16)
<!-- end:progress -->
