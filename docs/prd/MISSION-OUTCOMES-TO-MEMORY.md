# PRD: Mission outcomes → crew memory (F4.5)

| Field  | Value                                          |
| ------ | ---------------------------------------------- |
| Owner  | Pavel                                          |
| Status | draft / in-flight                              |
| Scope  | internal design — wires mission lifecycle into the existing memory subsystem |
| Track  | Agent Evolution F4 (Keeper Phase 2 — extends the negative-learning surface from per-agent run failures to per-mission outcomes) |

## 1. Context

The May 2026 audit on `main` (`f299cdd1`) found that issues/missions
have no path back into agent memory. Boot-context recall works
(AGENT.md, CREW.md, daily logs are injected at session start), but
when a mission COMPLETES or FAILS, no signal flows into any memory
tier. LEAD agents see a static CREW.md snapshot, never an
operational digest of "what did my crew solve / fail at recently."

Pipelines already track full authorship (`author_agent_id`,
`author_chat_id`, `author_run_id`, `authored_via`). Missions don't —
all we know is the LEAD that owns it. Two missions in the same crew
solved by two different agent runs in two different chats are
indistinguishable in the table.

This PRD closes both gaps in one PR.

## 2. Goals

- Mission completion (status → COMPLETED, FAILED, CANCELLED, DONE)
  emits a structured `LessonEntry` to `/crew/shared/.memory/lessons.md`
- LEAD agents see a fresh "[CREW OUTCOMES — last N entries]" section
  in their boot prompt
- Missions get the same provenance trio as pipelines: chat_id, run_id,
  authored_via
- Idempotent — a mission re-transitioned through the same terminal
  state (or re-fetched by the hook) lands the same lesson ID, not a
  duplicate
- Read-only via the existing memory.read tier=`lessons` surface so
  agents can ask "what's our crew's pattern with X?" mid-session

## 3. Non-goals (explicit cuts)

- Auto-promotion to skills (skill_promote source stays gated on F4.1)
- LLM-graded summaries — the rule body is mechanically derived from
  mission title + outcome; aux-model rewrite is a future PR
- Cross-workspace lesson sharing (each crew's lessons.md stays scoped
  to that crew dir)
- Cancel reasons or human-written retros — those write through
  manual lessons.md or `memory.write tier=lessons`, not this hook
- Topic-tier promotion (3+ similar lessons → `topics/<keyword>.md`) —
  separate follow-up PR; primitives need to land first
- Backfill of historical missions — only NEW transitions from this
  PR's deploy forward emit lessons

## 4. Constraints

- File-first markdown; no new tables
- Reuses existing `consolidate.WriteLesson` shape — new function is a
  thin wrapper, not a parallel implementation
- Stays inside the existing memory budget (CREW tier 40%); the new
  outcomes section is part of CREW.md/lessons.md content, not a
  fresh tier
- Migration is additive ADD COLUMN only — no rewrites, no SQLite
  recreate dance

## 5. Design

### 5.1 New `LessonSource` enum value

```go
LessonSourceMissionOutcome LessonSource = "mission_outcome"
```

Lands in `internal/consolidate/lesson_writer.go` alongside the existing
five sources. Added to `validLessonSources` map.

### 5.2 New `WriteCrewLesson` function

Mirror of `WriteLesson` but operates on the crew-shared dir:

```go
func WriteCrewLesson(ctx context.Context, crewSharedMemoryDir string, entry LessonEntry) error
```

Same idempotent-by-ID semantics, same flock serialization, same
boundary validation. Writes to `<crewSharedMemoryDir>/lessons.md`.
The agent-only path (`WriteLesson`) is unchanged.

Reuses the lower-level `loadLessonsLocked` / `saveLessonsLocked`
helpers so the two writers share format, header, and atomic-rename
behavior. Only the path differs.

### 5.3 Schema migration v108 (`mission_provenance`)

```sql
ALTER TABLE missions ADD COLUMN author_chat_id TEXT;
ALTER TABLE missions ADD COLUMN author_run_id TEXT;
ALTER TABLE missions ADD COLUMN authored_via TEXT
    CHECK (authored_via IN ('agent_tool_call','user_api','imported','seed','routine','recurring'));
CREATE INDEX IF NOT EXISTS idx_mission_chat ON missions(author_chat_id)
    WHERE author_chat_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_mission_run ON missions(author_run_id)
    WHERE author_run_id IS NOT NULL;
```

All three columns nullable — pre-existing rows retain NULL and
behave exactly as before. `authored_via` mirrors the closed enum
`pipelines.authored_via` uses, extended with `routine` and `recurring`
(both already implicit in mission origin code paths).

No `RestoreBackfillFunc` needed — pure additive ADD COLUMN with
nullable defaults.

### 5.4 Mission completion hook

In `internal/api/mission_handler_mutate.go` and
`internal/api/issue_handler_workflow.go`, after the transaction commits
on a terminal status transition (`COMPLETED`, `FAILED`, `CANCELLED`,
`DONE`), call:

```go
consolidate.EmitMissionOutcomeLesson(ctx, db, missionID, newStatus, logger)
```

The helper:
- Reads the mission row + the crew shared memory dir
- Maps status → LessonKind (COMPLETED|DONE → Positive, FAILED → Negative, CANCELLED → Neutral)
- Builds the rule + context from mission identifier/title/lead/agent
- Calls `WriteCrewLesson` with `LessonSourceMissionOutcome`
- Logs but does not return the error to the caller — outcome
  recording is best-effort; a write failure must not break the
  status transition

The hook is invoked **after** the SQL tx commits so a failed write
can't roll back the operator's intentional status change. Errors
are logged at `WARN` level.

### 5.5 LEAD boot-context outcomes section

In `internal/orchestrator/memory.go`, the `buildCrewMemoryBlock`
function already reads `CREW.md` and crew daily logs. Extend it to
also read `lessons.md` (the new shared one), filter to the most
recent N entries (default 10), and append a `[CREW OUTCOMES]` block
within the existing CREW SHARED MEMORY budget.

```text
[CREW SHARED MEMORY]
--- CREW.md (crew-wide knowledge) ---
<existing content>

--- Crew daily: 2026-05-22 ---
<existing content>

--- Crew outcomes (last 10) ---
✓ ENG-12: chose PostgreSQL over SQLite for analytics
  (2026-05-20, LEAD=eva, mission completed)
✗ DEV-4: network probe required sudo we don't have
  (2026-05-21, LEAD=ondrej, mission failed)
[END CREW SHARED MEMORY]
```

Only LEAD agents see the outcomes section — AGENT-role members
already get CREW.md + daily and don't need the operational digest
that's about delegation/coordination decisions. AGENT-role still
sees lessons.md via the `memory.read tier=lessons` mid-session tool
if they ask.

### 5.6 Hook call sites

Exhaustive list of status mutations that trigger the lesson write:

1. `mission_handler_mutate.go:186` — generic `PATCH /missions/:id`
   transition to COMPLETED/FAILED/CANCELLED
2. `issue_handler_workflow.go:62` — review approve → DONE
3. `issue_handler_workflow.go:335` — cancel → CANCELLED

Status transitions that don't go to a terminal state (BACKLOG → TODO,
TODO → IN_PROGRESS, REVIEW → TODO etc.) do NOT emit lessons — only
terminal transitions matter for institutional knowledge.

## 6. Verification

### 6.1 Unit tests

- `lesson_writer_crew_test.go` — WriteCrewLesson: creates file, appends,
  idempotent-by-ID, flock-serialized (mirror of existing
  lesson_writer_test.go shape)
- `lesson_source_test.go` — mission_outcome accepted, typos rejected
- `mission_outcome_lesson_test.go` — status→kind mapping, rule body
  shape, error swallowing on write fail
- `migrate_v108_test.go` — migration applies cleanly on fresh DB and
  on a DB at v107; idempotent

### 6.2 Integration tests

- `mission_handler_mutate_test.go` — after PATCH COMPLETED, lessons.md
  contains the entry; after a re-PATCH to same status, no duplicate
- `memory_outcomes_test.go` — when crew lessons.md has 12 entries,
  buildCrewMemoryBlock for LEAD includes 10 most recent; for AGENT
  excludes the outcomes section

### 6.3 Live verification (dev3, post-deploy)

```bash
# 1. Bootstrap + seed (after #527 fixes land, or use manual bootstrap)
crewship seed

# 2. Take a mission to COMPLETED
crewship mission update <id> --status COMPLETED

# 3. Inspect the lessons file (UID 1001 ownership assumed)
docker exec <crew-container> cat /crew/shared/.memory/lessons.md
# expect a YAML entry with source: mission_outcome

# 4. Spawn a LEAD agent and ask
crewship ask --agent eva 'What did your crew complete recently?'
# expect summary that names the mission identifier
```

## 7. Risk analysis

| Risk | Mitigation |
|---|---|
| Write failure on lessons.md breaks status transition | Hook runs post-commit, errors logged not returned |
| Lessons.md grows unbounded | Existing per-tier cap protocol applies; F4 keeper memory-health route can prune via F4.3 |
| Concurrent transitions race the file | Existing flock serialization in `WriteCrewLesson` |
| Backfill of old missions floods lessons.md | Explicit non-goal; only NEW transitions emit |
| LEAD prompt grows past budget | Outcomes lives inside CREW tier's existing 40% slice; no extra budget |
| Crew shared dir doesn't exist yet | EmitMissionOutcomeLesson `MkdirAll`s the path; same pattern as WriteLesson |
| Migration on already-deployed DBs | Pure ADD COLUMN, nullable, no backfill — safe to roll out hot |
| Mission deleted before hook fires | Hook reads row before write; missing row is a no-op + log |

## 8. Curator pattern framing

A common pattern in long-running agent memory systems is a "curator"
loop that periodically reads completed sessions and distils durable
learnings into shared memory. Crewship's mission-outcome hook lands
the same load-bearing primitive — durable, shared, lesson-shaped
knowledge — but via two pattern choices that diverge from the typical
scheduled-curator shape:

1. **Event-driven, not time-triggered.** Mission completion is a
   structured event; we don't need to wait for a nightly sweep to
   surface it. The lesson lands at the moment of transition, so the
   LEAD's next boot prompt already includes the freshest signal.
2. **Operator-validated outcomes, not model-graded.** Scheduled
   curators typically rely on the model itself to grade "did this
   session go well." Crewship's mission status is already
   operator-affirmed (or routine-confirmed) before the hook fires —
   the status IS the signal. No grader prompt, no aux-model call.

The non-goal "no LLM-graded summaries" keeps this PR small and the
lesson body deterministic. A future PR can layer an aux-model
rewrite on top without changing the event-driven pipeline.

## 9. Open questions deferred to follow-up

- Should `memory.read tier=lessons` surface crew lessons in addition
  to agent lessons? (Currently the tier resolves to the agent's own
  file. Cross-resolving would be a separate dispatcher change.)
- Should COMPLETED missions referenced by a routine emit a lesson on
  BOTH the mission's crew AND the routine's authoring crew? Today
  the hook resolves to mission.crew_id only.
- Should the outcomes section in the boot prompt rotate based on
  what the LEAD has already seen, or is "last N entries" simple enough
  forever?
- Memory-health hook (F4.3) future enhancement: when lessons.md
  passes a threshold, propose collapsing similar entries into a
  topic file under `topics/<keyword>.md`.

## 10. Done definition

- v108 migration lands and applies cleanly on v107 DBs
- `LessonSourceMissionOutcome` const + map entry land in
  `lesson_writer.go`
- `WriteCrewLesson` lands with full test coverage matching
  `WriteLesson`'s
- `EmitMissionOutcomeLesson` is invoked from all three terminal-state
  call sites, no extra ones, no missing ones
- `buildCrewMemoryBlock` includes the outcomes section for LEAD
  agents and skips it for AGENT-role
- `go test ./...` + `go vet ./...` green
- PR description includes the live verification recipe so reviewer
  can reproduce the end-to-end signal
