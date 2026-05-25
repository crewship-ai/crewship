# PRD-BACKUP-V3-POLISH

**Status**: post-merge follow-up
**Owner**: backup subsystem
**Parent PR**: #594 (`feat/backup-v3-rewrite`)
**Date**: 2026-05-25

## Background

PR #594 delivered the disaster-recovery rewrite (`--replace` mode, FK
discovery, IntentMap, user-email reconciliation, 70+ table
BackupTables). It was end-to-end validated on dev2 against the real
96 MB UO-Outlands bundle: nuke → fresh start → restore → admin login
flow. **DR works.**

What follows is the honest backlog of items deferred from the original
"atomic big-bang" scope plus issues surfaced during real-bundle
validation. Each item has a concrete fix shape, an effort estimate,
and a priority. None of these are blockers for the PR #594 merge —
they're the next sprint of polish work to take the system from
"DR-works" to "production-grade".

---

## P0 — Real DR fail-modes still exposed

### 1. Skills FK reconciliation (mirror of users)

**Problem.** Bundle has skill `sk_custom_X` with `UNIQUE name="my-skill"`.
Target has `sk_target_Y` with the same name. INSERT OR IGNORE drops
the bundle row → `agent_skills.skill_id = sk_custom_X` orphans → FK
violation → restore aborts.

This is the exact same dynamic `ReconcileUsersByEmail` solved for
users, but the existing `nonRemappablePKTables` mechanism in
`remap.go` only triggers under `--as-workspace` (not `--replace`).

**Fix.** New `ReconcileSkillsByName` in `reconcile.go`. Same shape as
`ReconcileUsersByEmail`: pre-pass that finds target skill rows
matching by (name, slug), rewrites bundle skill IDs and propagates
through `agent_skills.skill_id` (and any other FK that references
`skills.id`).

**Effort**: 1.5 h (mirror existing implementation + e2e test)

### 2. Memory filesystem restore — never validated end-to-end

**Problem.** The DR validation on dev2 confirmed DB-side restore.
Memory tar (`memory/<crew>/.memory/`) extraction was a no-op because
dev2 has no provisioned containers. Real DR includes container
provisioning + filesystem restore, and that code path has zero
integration coverage.

A subtle failure here would mean: admin restores, sees the crew/agents
in the UI, but the agent boots with empty memory and starts learning
from scratch. Silent data loss in the worst place — the agent's
working notes.

**Fix.** New integration test gated by `INTEGRATION=docker` env. Spins
up a real crew container, populates `/output/<crew>/.memory/`, takes
a backup, nukes, provisions a NEW container, restores, asserts the
sidecar's `POST /memory/search` returns the expected wiki chunks.

**Effort**: 3 h (real Docker integration, fixture management)

### 3. Drift detection NOT in CI

**Problem.** `CategoriseScopedTables` raises `ErrDiscoveryDrift` if a
workspace-scoped table is missing from `BackupTableIntent`. But this
check runs ONLY during `--replace` at runtime. A new migration that
adds a table without updating IntentMap will ship and **silently
omit that table from bundles** — exactly the bug class IntentMap was
meant to prevent.

**Fix.** Standalone test `TestIntentMap_NoDriftAgainstMigratedSchema`
that:

1. Opens `openMigratedDB(t)` (full HEAD schema)
2. Runs `DiscoverScopedTables`
3. Calls `CategoriseScopedTables` against `BackupTableIntent`
4. Fails the test if `ErrDiscoveryDrift` fires

Runs on every PR via the existing CI. Catches the omission at code
review time, not at production restore time.

**Effort**: 30 min

---

## P1 — UX / safety improvements

### 4. `--replace --dry-run` preview

**Problem.** `--replace` is destructive. Today `--dry-run` validates
the bundle but doesn't show what `--replace` would wipe. An admin
running `--replace` on the wrong target has no warning.

**Fix.** When both `--dry-run` and `--replace` are set, call
`resolveTargetWorkspaceIDs` and `resolveDeletionOrder`, then run the
DELETE queries with `EXPLAIN QUERY PLAN` (or just count via SELECT
COUNT) to report:

```
WOULD WIPE under --replace:
  workspaces.id IN (ws_old_xyz)    → 1 row
  crews                            → 2 rows
  agents                           → 4 rows
  chats                            → 46 rows
  credentials                      → 7 rows
  journal_entries                  → 2991 rows
  [...]
WOULD RESTORE: 3048 rows from bundle
```

**Effort**: 1 h

### 5. Credentials decrypt validation post-restore

**Problem.** Bundle carries `credentials.encrypted_value` verbatim
(cipher preserved across hosts). If target instance has a different
`ENCRYPTION_KEY` env, every credential decrypt at runtime fails
silently — agents start failing API calls with no clear cause.

**Fix.** After restore, probe-decrypt ONE credential per workspace
using the target's keyring. On failure, log a `WARN` with a
prominent message:

```
WARNING: restored credentials cannot be decrypted with this
instance's ENCRYPTION_KEY. The bundle's source ENCRYPTION_KEY
must be set on this host for credentials to work. See
docs/dr-runbook.md#cross-instance-credentials.
```

**Effort**: 1 h

### 6. DR runbook documentation

**Problem.** No `.claude/context/` or `docs/` page covers post-nuke
DR. A new operator running into the user's original bug scenario has
no playbook. The CLI flag help is the only signal.

**Fix.** New `docs/dr-runbook.md`:

- The three restore modes (`--replace`, `--as-workspace`, default)
  with concrete use cases for each
- The ENCRYPTION_KEY portability requirement
- Container re-provisioning sequence for full filesystem restore
- Smoke checks the operator should run after restore

Cross-link from `CLAUDE.md` and `README.md`.

**Effort**: 1 h

### 7. Bundle manifest carries dumped_tables list

**Problem.** Bundle created with `IntentMap v1` (50 tables) restored
on a binary with `IntentMap v2` (60 tables): `--replace` wipes the
10 newer tables, but the bundle has nothing for them → target ends
up with empty rows in tables that should have had data.

**Fix.** Add `manifest.contents.dumped_tables: ["users","workspaces",
...]` recording the exact set the dump produced. Replace pass
intersects against this list so we only wipe what the bundle can
restore.

**Effort**: 2 h (manifest schema change, restore-side logic)

---

## P2 — Architectural debt (user-requested deferrals)

### 8. Drop AGE create-side

**Problem.** User explicitly requested in #594 discussion: "drop
encryption create-side, admin to stejně dělá. MVP it nepotřebuje."
Deferred to keep PR scope manageable. CreateBackup still requires
one of {Passphrase, Recipients, NoEncrypt}; CLI still prompts for
passphrase.

**Fix.**

- Default CreateBackup to NoEncrypt (no flag needed)
- Optional `--password foo` adds passphrase encryption
- Remove X25519 recipient flow entirely (it was barely used)
- Keep DecryptStream + DecryptStreamPassphrase for reading v1 bundles
- Update CLI to mirror

**Effort**: 4 h (decryption read path stays; create path simplifies;
existing tests need rewriting)

### 9. Drop crew + instance scope

**Problem.** Instance scope restore is gated with "not supported yet
(V1.5)" and has been since CRE-129. Dead code. Crew scope works but
has subtle interactions with the new IntentMap (nonRemappablePKTables
overlap) that aren't fully verified.

**Fix.**

- Remove `ScopeInstance` from manifest enum + delete instance.go
  bootstrap helpers
- Either remove ScopeCrew or fully audit it under the new BackupTables
- Simplify CLI to single workspace scope

**Effort**: 4 h (touch ~500 LOC across runner, CLI, tests)

### 10. CLI flag simplification

**Problem.** `--use-keyring`, `--identity`, `--recipient`,
`--passphrase-file` exist across `crewship backup create` and
`restore` commands. After dropping AGE create-side many become dead
code or no-op.

**Fix.** Mark deprecated, add `--password` / `--password-file` as
the canonical replacement, remove keyring helper in
`internal/backup/keyring.go`.

**Effort**: 2 h

### 11. `nonRemappablePKTables` removal

**Problem.** `remap.go:44-54` hardcodes `{skills, users}` as ID-stable
during `--as-workspace`. Once `ReconcileUsersByEmail` (#594) and
`ReconcileSkillsByName` (#1 above) land, this map is redundant.

**Fix.** Delete the map; trust reconciliation. Verify via
`TestE2E_AsWorkspace_*` suite.

**Effort**: 1 h (delete + run tests)

---

## P3 — Lower priority

### 12. Composite PK support

`discovery.go` assumes every table has an `id` column. All current
INTENT_INCLUDE tables do. Future schemas with composite keys would
need an audit.

**Effort**: 2 h (only if a real composite-PK table appears)

### 13. Webhook event annotation

`backup.restored` event doesn't indicate whether `--replace` was
used. Observability distinguishes "this admin restored fresh" from
"this admin wiped + restored" only via audit log.

**Fix.** Add `replace_mode: bool` to webhook payload + restore audit
log entry.

**Effort**: 30 min

### 14. Metrics: tables-wiped count under `--replace`

No metric tracks how much state `--replace` deleted. For DR drill
audits, knowing "wipe deleted 3 048 rows across 12 tables" matters.

**Fix.** New gauge `backup_replace_rows_wiped` with table-name label.

**Effort**: 30 min

### 15. CLI bootstrap on dev instances is fragile (orthogonal)

**Problem (not in scope but observed).** During dev2 validation,
`dev.sh seed` produced repeated SQLITE_BUSY on
`bootstrap: begin tx`. Background loops (port_expose_registry purge,
RateLimiter cleanup, ProvisioningHandler) grab the DB lock and
bootstrap can't compete. Real admin path (POST /api/v1/bootstrap)
fails non-deterministically on dev2.

**Suggested fix**: bootstrap should acquire connection with
`PRAGMA busy_timeout = 30000` so it waits out background contention
rather than failing on first lock. Or refactor background loops to
share a single read-only connection that doesn't compete with
writers.

Tracked separately from this PRD.

**Effort**: 1 h

---

## Sequencing

**Sprint 1 (~6 h)**: P0 items — #1 skills reconciliation, #2 memory
e2e, #3 drift in CI. These close the real DR fail-modes.

**Sprint 2 (~5 h)**: P1 UX — #4 dry-run preview, #5 decrypt
validation, #6 DR runbook, #7 dumped_tables manifest.

**Sprint 3 (~10 h)**: P2 debt cleanup — #8 drop AGE create-side, #9
drop crew/instance, #10 CLI simplification, #11 remove
nonRemappablePKTables.

**Backlog**: P3 polish.

## Acceptance criteria for declaring "backup system production-grade"

- [ ] Same-email + same-skill-name DR scenarios both pass without
      manual intervention
- [ ] Memory filesystem restoration validated end-to-end with real
      Docker
- [ ] Drift detection runs in CI on every PR
- [ ] DR runbook published; new-operator can recover from a nuke in
      under 10 minutes following the doc
- [ ] Webhook + metrics distinguish `--replace` from normal restore
- [ ] No SQLITE_BUSY race in bootstrap path
