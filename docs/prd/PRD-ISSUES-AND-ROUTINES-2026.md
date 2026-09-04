# PRD: Issues and Routines — one work loop, proven end to end

| Field | Value |
|---|---|
| Status | **Track A implemented on local branches** `a1` (with `a2` merged), `a3`, `a4`, `a5`, `a6`, `a7`, `a9`, `t1` — nothing merged, nothing pushed, no issues claimed. Track A is 1.0-eligible; Track B is 1.1 and must not start before A merges (§17). |
| Created | 2026-09-01 |
| Baseline | `main` @ `3fa36df5`, live dev1 clone `/srv/crewship/crewship_1/crewship.db` |
| Supersedes | `docs/prd/PRD-AGENT-FIRST-ISSUE-COORDINATION-2026.md` (issues-only, and wrong in five places — see §3) |
| Scope | Issues, comments, mentions, agent sessions, runs, context, Routines, triggers, outcomes, Inbox, realtime, human oversight |
| Implementer | A coding agent, working one claimed issue per PR |
| Revision | **3 (2026-09-01).** Adds F51 (the exclusivity guard the assignment path bypasses), §2.8 (implementation hazards), and a rewritten §9 that cuts six new tables to three. Rev 2 note follows.<br>**2 (2026-09-01).** Rev 1 was reviewed by three further audits — an adversarial pass over its own proposals, a release-scope audit, and a neighbouring-subsystem audit. Rev 2 corrects four errors of its own (§3) and adds §2.7, §17's two-track split, and §26. |
| One-line thesis | **The issue is where work is judged. The routine is how work recurs. A run is one attempt at either. Today none of those three are linked to each other in the schema.** |
| Release position | **This document is mostly not 1.0 scope, and says so.** 1.0 in this project means the surface is proven true, not that new features exist (§17 Track A/B, §24). |

---

## 1. How to read this document

This PRD is written to be executed, not admired. Three rules for the implementer:

1. **Every factual claim about current behaviour carries a `file:line`.** If you find one that no longer holds, the claim is stale — fix the document in the same PR that discovers it, and say so in the PR body. Do not silently build on a claim you could not reproduce.
2. **Nothing here is a licence to skip A0.** A0 re-measures the baseline. Several findings below were true on 2026-09-01 and are exactly the kind of thing another session fixes next week.
3. **Work packages are separately shippable.** Each WP names its own acceptance criteria, its own tests, and the exact commands CI will run. A WP that cannot be reviewed in one sitting has been scoped wrong; split it and say why.

Terminology note: in the database, an issue is a `missions` row. "Issue", "mission" and (in older code) "task" all appear. This PRD says **issue** for the user-facing object and cites the real table names.

---

## 2. Verified baseline — what is actually true today

Nine parallel code audits plus a query of the live dev1 database. This section is the evidence base; every later section refers back to a finding id.

### 2.1 Live data — what has actually been exercised

Queried directly from `/srv/crewship/crewship_1/crewship.db` on 2026-09-01:

| Table | Rows | What it proves |
|---|---:|---|
| `missions` | 17 (8 DONE, 2 REVIEW, 6 BACKLOG, 1 CANCELLED) | Issues are used. |
| `mission_comments` | 30 | Conversation happens on issues. |
| `mission_comment_mentions` | **0** | The mention→dispatch path has never fired on this clone. |
| `assignments` | 17 (16 COMPLETED, 1 FAILED) | Runs happen — but see F1: none is linked to an issue. |
| `checkpoints` | **0** | No continuity artefact has ever been written. |
| `cost_ledger` | **1** | 17 runs, one cost row. |
| `pipelines` / `pipeline_runs` | 2 / 3 | Routines exist; three runs, all manual. |
| `pipeline_schedules` | **0** | No routine has ever run on a schedule. |
| `pipeline_webhooks` | **0** | No routine has ever been triggered by an event. |
| `automations` | **0** | The event-automation path is unexercised. |
| `approvals_queue` / `escalations` / `notification_deliveries` | 0 / 0 / 0 | The human-decision loop is unexercised. |
| `inbox_items` | 4 | 1 waitpoint + 3 "ready for review". |

**Read this honestly.** The engine has depth (§2.4 is genuinely strong). What has *not* happened even once on a working clone is: a scheduled routine firing, an event triggering a routine, an agent being woken by a mention, a human approving something through the queue, or an agent resuming work it had done before. Those are precisely the loops this PRD is about.

The correct claim is therefore **not** "Routines work well." It is: *the executor looks strong and is well tested in isolation; the recurring-work product has never been operated.*

### 2.2 Findings — Issues, sessions, runs

**F1 — A run is not attached to an issue.**
`assignments` has `chat_id`, not `issue_id`/`mission_id` (live schema; no `ALTER TABLE assignments` in history adds one). The only issue↔run link is `mission_comment_mentions.assignment_id` — a table with 0 rows. Consequence: "show me every run for this issue", "did anything actually happen after I commented", and parent/child roll-up are all unanswerable from the schema.

**F2 — A mention of a busy agent starts a second concurrent run.**
`refuseHeldAgent` refuses only `PENDING_REVIEW`; the comment at `internal/api/assignments.go:188-224` deliberately declines to refuse on `RUNNING`. `DispatchMention` (`internal/api/issue_mentions.go:724-881`) inserts a fresh `assignments` row and launches `runAssignment` regardless. The only bound is the per-crew slot budget (`internal/api/assignments_queue.go:108-152`). Two comments 5 seconds apart = two agents editing the same repo.

**F3 — A comment never reaches a running turn.**
`IssueHandler.CreateComment` (`internal/api/issue_handler_comments.go:65-151`) never calls Steer. The steering endpoint exists (`internal/api/chat_steer.go:77-125`) and queues a message for the *next* turn, guarded in `internal/chatbridge/bridge.go:653-677` — but nothing on the issue surface is wired to it.

**F4 — There is no consumption cursor, outbox, or delivery record for agent wake-ups.**
`mission_comment_mentions` is a write-once audit of what the dispatcher did (`dispatch_state ∈ dispatched|refused|skipped|failed`, `issue_mentions.go:66-73`), not a queue an agent drains. `UNIQUE(comment_id, agent_id)` dedupes *within one comment* only; two comments mentioning the same agent produce two independent runs.

**F5 — Sub-agent runs are invisible while they run.**
`RunAgentForAssignment` sets `SuppressSessionStream = true` (`internal/orchestrator/orchestrator_lifecycle.go:81`); output is buffered and surfaced only at completion (`internal/api/assignments_run.go:719-726`). A delegated agent works in the dark.

**F6 — "Stop work" stops nothing.**
`IssueHandler.Stop` (`internal/api/issue_handler_workflow.go:312-362`) only writes `mission_tasks` and `missions` rows to CANCELLED. No context cancel, no `docker kill` (grep for `StopAgent|KillContainer|dockerutil.*Kill` across `internal/api`, `internal/orchestrator`, `internal/dockerutil` returns nothing). `MissionEngine.StopMission` (`internal/orchestrator/mission.go:272`) exists but is called from no HTTP handler. And it could not help if it were: the dispatch goroutine runs on `context.Background()` (`internal/api/assignments_run.go:441-445`), deliberately decoupled from any request context.

**F7 — A cancelled task is resurrected by its own late callback.**
`OnAssignmentCompleted` writes `UPDATE mission_tasks SET status=?...WHERE id=?` with no status guard (`internal/orchestrator/mission_tasks_completion.go:176-178`). The sibling write to `missions` *is* guarded (`...WHERE id=? AND status='IN_PROGRESS'`, `internal/orchestrator/mission.go:497`). So Stop marks a task CANCELLED and the run that ignored the stop marks it COMPLETED again.

**F8 — No lease. Recovery is by process-start timestamp.**
`RecoverInterruptedRunning` fails every RUNNING row older than boot (`internal/api/assignments_running_recovery.go:132-163`), plus a staleness sweeper. This is sound for one process and wrong for two: nothing records *which* process owns a run, so a second replica's boot would fail the first replica's live runs.

**F9 — Terminal-state writes are protected; nothing else is. (Amended in rev 3.)**
`finishAssignment` CASes `WHERE id=? AND status NOT IN ('COMPLETED','FAILED','CANCELLED')` (`internal/api/assignments_run.go:986-989`) and everything downstream (mission comment, activity row) rides on winning that CAS. Good. Rev 2 said `assignments` is never written to `CANCELLED` by anyone. **That is not literally true:** `cancelDeferredAssignment` (`internal/orchestrator/mission_tasks.go:665-673`) writes it, conditionally on `status='PENDING'`, to retire the row a deferred held-agent dispatch left behind. The corrected claim is narrower and still damning — **no Stop path and no general cancellation reaches it**; one unwind case does. Verified directly.

**F10 — Parent issues do not know about their children.**
`sub_issues_count` is a display-only subquery (`internal/api/issue_handler.go:409`); no status write anywhere consults `parent_issue_id`. (Mission *tasks* are different and correct: `checkMissionCompletionWithTasks` waits for all tasks terminal, `internal/orchestrator/mission_tasks_completion.go:389,445-473`.)

**F11 — Two vocabularies for "finished".** `ValidIssueTransitions` (`internal/statuses/transitions.go:14-23`) uses `DONE`; at least six production call sites defensively query `status IN ('DONE','COMPLETED')` — `internal/api/milestone_handler.go:69,264`, `project_handler.go:112,290,556,580`, `metrics_fillers_issues_cost.go:29,54,79`, `mission_outcome_hook.go:198`. This is a refactor, not a doc note.

**F12 — Cost is recorded after the fact and enforced with a known race.**
The sidecar posts cost once the response is parsed and explicitly does not pre-flight budgets (`internal/api/internal_cost.go:59-65`); `paymaster.Middleware` pre-checks only Go-side callers and documents its own race (`internal/paymaster/middleware.go:100-108`); soft budgets warn and continue (`internal/paymaster/budgets.go:141-152`). One `cost_ledger` row for 17 runs (§2.1) is what that looks like in practice.

**F51 — The exclusivity guard already exists, and the assignment path walks around it.**
`chatbridge.tryMarkRunStart` (`internal/chatbridge/steer.go:60-77`) enforces at most one live `RunAgent` exec per chat. Its stated reason is not coordination, it is corruption — `internal/chatbridge/bridge.go:650-655`:

> "Two different users messaging the same group chat concurrently must never race two RunAgent execs into the same agent container/tmux session — interleaved stdout and corrupted tmux state."

It has exactly one caller: `bridge.go:676`, inside `HandleChatMessage`. **`runAssignment` — the path taken by `/assign` and by every @mention — never consults it** (grep across `internal/`: no other call site; `internal/api/assignments_run.go` references `chatbridge` only for `AgentRunOverrides`). The per-crew slot budget (`claimCrewSlot`) bounds how many agents run at once, not how many runs one agent has.

This changes the severity of F2. A second mention of a busy agent is not merely uncoordinated: it takes the exact race the codebase already identified as producing interleaved stdout and corrupted tmux state, and drives it through the one door that has no guard. Under the 1.0 bar's condition #5 — behaviour-level tests on critical orchestration paths — that argues for pulling the *guard* (not the whole session architecture) forward into Track A.

**Rev 3 update — confirmed from code, and worse than stated above.** The exec identity is per-agent-slug: `TmuxSessionName(agentSlug) = "agent-" + agentSlug` (`internal/orchestrator/orchestrator_exec_env.go:69-71`), and every scratch path derives from that one string — `/tmp/agent-<slug>.{args,sh,fifo,exit,env}` (`:156-164`). The wrapper's first act is:

```
tmux kill-session -t 'agent-<slug>' 2>/dev/null; rm -f '<args>' '<exit>'; mkfifo '<fifo>'; ...
```

(`orchestrator_exec_env.go:236-239`). Its comment says the session-scoped kill exists "to avoid disrupting **other agent** sessions in the same crew container" — which is exactly the protection that does not extend to the *same* agent.

So a second concurrent run for one agent does not merely interleave output: **it kills the first run's tmux session and deletes its fifo and exit file.** The chatbridge comment's "interleaved stdout and corrupted tmux state" understates its own defect.

Three further confirmations:
- Every adapter takes the tmux path. `PromptViaStdin` is false for droid, opencode, codex, cursor and gemini; only Claude returns true, and only for E2BIG-sized prompts (`adapter_claude.go:60-68`).
- Two `runAssignment` calls for one agent can trivially be live at once: `claimCrewSlot` budgets per **crew** and is wired only into the mission-engine path; `/assign`'s `Create` and `DispatchMention` spawn goroutines with no exclusivity check at all (`assignments_run.go:434-443`).
- **The existing guard has the wrong key.** `tryMarkRunStart` is keyed on `chatID`, and `ensureMissionChat` mints one synthetic chat *per mission*. The same agent mentioned on two different issues was never protected by it, even in principle.

This moves A9 from "gated on investigation" to a confirmed 1.0 defect. The §27 empirical test is no longer needed to decide it — though running it would still show what the failure looks like from the user's side.

### 2.3 Findings — context and continuity

**F13 — Every sub-agent run starts with no conversation history, always.**
`SkipConvHistory` is set true unconditionally on the assignment path (`internal/orchestrator/orchestrator_run.go:54`), the mission-task path (`internal/api/assignments_run.go:856`, whose comment says "always true for sub-agent runs"), and peer queries (`internal/api/query_handler.go:467`). It gates history injection at `orchestrator_run.go:832`, and `internal/orchestrator/session_context.go:19-21` documents that such runs get no `[SESSION CONTEXT]` block at all. `AgentBrief` was meant to supersede it (`internal/orchestrator/agent_brief.go:16-30`) but is additive: the flag is still live, and a brief is optional.

**F14 — Compaction is real, unpersisted, and silently degrades.**
`buildConversationContextWithStats` summarises overflow when a summarizer is wired (`internal/orchestrator/orchestrator_run_conv.go:157-165`), preserving decisions/facts/open threads with a past-tense temporal anchor (`:56-76`). On a 12s timeout or missing summarizer it falls back to **plain truncation** (`:199-205,264-269`). It is recomputed every turn; no table stores it (grep for `conversation_summary` returns only the implementation and its wiring).

**F15 — The only structured hand-off is three fields, on one path.**
`---HANDOFF--- summary / confidence / artifacts` is instructed at `internal/orchestrator/mission_tasks.go:321-329`, parsed at `internal/orchestrator/mission.go:100-137`, persisted at `internal/orchestrator/mission_tasks_completion.go:88-89`. There is no `done` / `plan` / `facts` / `blockers` / `next_step` schema anywhere. Ordinary agent runs produce no structured artefact at all.

**F16 — Prompt assembly is deliberately cache-shaped, and that constrains this PRD.**
Stable content goes in the system prompt so the Anthropic cache prefix stays byte-stable within a day; everything volatile is prepended to the *user* message as `[SESSION CONTEXT]` (`internal/orchestrator/orchestrator_run.go:813-819`, `session_context.go:23-26`). Memory tiers are budget-truncated, not relevance-ranked (`internal/orchestrator/memory.go:111-163`); only episodic recall is query-dependent (`internal/episodic/hybrid.go:98-247`, 2KB / 2s). Token budget is a 4-chars/token heuristic capped at 32000 (`internal/tokenutil/estimate.go:9`) — never the model's real window. **Any context-pack design must ride the volatile block, not the cached prefix.**

### 2.4 Findings — Routines

The executor is the strongest part of the system. State that plainly before listing gaps.

Real and verified: 11 step types with deterministic and agent kinds (`internal/pipeline/types.go:679-689`), DAG waves (`internal/pipeline/dag.go:16`), `foreach`, `call_pipeline` with cycle detection and depth caps (`internal/pipeline/dsl.go:679-716`, `executor.go:2064,2080`); per-step durable outputs (`internal/database/migrate_consts_v159_run_step_outputs.go:34-40`); boot resume with definition-hash drift checks (`internal/pipeline/resume.go:103,262`); waitpoints with idempotent CAS resume returning 409 (`internal/pipeline/waitpoints.go:610-646`) and a 24h timeout sweeper (`:135-187`); versions with content-hash dedup, diff, rollback and trigger pinning (`internal/pipeline/versions.go`, hard-fail on missing pin at `executor.go:739-754`); idempotency reservations (`internal/pipeline/idempotency.go:73-174`); debounce (`internal/pipeline/pending_runs.go`); retry with full-jitter backoff and a CEL `retry_on` predicate (`internal/pipeline/executor_retry.go:29-135`); schedule-level circuit breaker (`internal/pipeline/schedules.go:1073-1129`); wake gates with fail-open/fail-closed (`:1192-1218`); catch-up policies capped at 20 occurrences (`:785-822`); HMAC + timestamp-bound webhook auth with an idempotency cascade (`internal/pipeline/webhooks.go:633-705`, `internal/api/pipeline_webhooks.go:686-703`); and a genuine eval toolkit — `dry-run`, `doctor`, `bench`, `replay`, `backtest`, plus an online sampler (`cmd/crewship/cmd_routine_*.go`, `internal/quartermaster/online_sampler.go:19-56`).

Against that, the gaps:

**F17 — Authoring a routine does not create a trigger.**
`save_routine` (`internal/sidecar/routine_mcp.go:22-53,331-408`) persists a definition. The DSL has no trigger field. `routineMCPTools` (`:147-213`) exposes no `create_schedule`/`create_webhook`. Schedules and webhooks are separate resources created by separate API calls reachable only from post-creation UI tabs or the CLI. So "make me a routine that runs every morning" can end with a routine that never runs, and the agent has no way to notice.

**F18 — The reliability surface exists in the backend and nowhere else.**
`PipelineSchedule` carries `catchup_policy`, `max_consecutive_failures`, `consecutive_failures`, `disabled_reason`, `last_missed_count`, `wake_*` (7 fields), `target_pipeline_version` (`internal/api/pipeline_schedules.go:20-61`). The create form offers **name, cron, timezone, inputs** (`components/features/routines/routine-schedules-tab.tsx:32-68,218-274`); the wake gate is a read-only chip. A schedule auto-disabled by the circuit breaker shows the user no reason.

**F19 — Event automations accept rules that cannot fire.**
`event_type` is validated by shape regex only — `^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$` (`internal/api/automations.go:71,232`), with a code comment admitting there is no registry to check against. `Matcher.PayloadEquals` keys are never validated (`internal/automation/types.go:98`). A well-shaped nonsense rule saves successfully and never fires. The `POST /api/v1/automations/preview` endpoint (`internal/api/automations_preview.go:39-91`) can detect this by replaying 7 days of journal — but it is opt-in.

**F20 — Trigger failure observability is inconsistent by an order of magnitude.**
Schedule failures raise a journal entry *and* an inbox card (`internal/pipeline/schedules.go:1058-1069,872-906,1140-1189`). Webhook fire failures write a DB row only (`internal/pipeline/webhooks.go:610-620`). Automation enqueue failures — rule matched, run genuinely never created — produce a bare `logger.Error` (`internal/automation/registry.go:734-736`), while the same file emits journal entries for depth and throttle cases (`:218-225,747`). There is no metrics instrumentation on any of the three (grep for `prometheus|promauto` in those files: empty).

**F21 — Webhooks cannot be edited.** No PATCH route, no CLI verb, no UI action anywhere. Changing anything means delete + recreate, which rotates the token and therefore the URL the sender uses.

**F22 — There is no client-meaningful outcome.**
Run status is technical: `queued|running|completed|failed|cancelled|dry_run|interrupted|waiting` (`internal/pipeline/runs.go:30-39`). `runverdict` adds an LLM judgment `goal_met|partial|failed|needs_human` (`internal/runverdict/verdict.go:29-34`), feature-flagged and advisory. Nothing distinguishes *"ran, nothing to do"* from *"ran, created work"* from *"ran, needs a human"* — grep for `no_op|noop|no_change` is empty. Every downstream routing decision (inbox? issue? silence?) is therefore a guess.

**F23 — Concurrency control is per-process.**
`RunRegistry.Acquire` is in-memory with an explicit comment that there is no cross-replica coordination (`internal/pipeline/run_registry.go:52-55,109-173`), and it returns 429 rather than queueing — while `docs/guides/routines.mdx:2047` says such requests "queue".

**F24 — Two independent schedulers.** `internal/scheduler` drives agent-level crons; `internal/pipeline/schedules.go` drives routines on a hand-rolled **30-second poll** of `next_run_at <= now` (`:621-637`), so worst-case fire latency is ~30s. Both are leader-gated (`internal/leader/lease.go`), each with its own idempotency scheme.

**F25 — Monthly budget is decorative.** `MonthlyBudgetUSD` has GET/PATCH handlers and zero references in `executor.go` (`internal/api/pipelines_budget.go`); the enforced gate is the per-run `DSL.MaxCostUSD` (`internal/pipeline/executor.go:1127,1619`). The docs say this correctly; the product name does not.

**F26 — Two documented behaviours are wrong.** `docs/cli/routine.mdx:1097` says rollback creates a new version; `internal/pipeline/versions.go:230-249` only repoints `head_version` (the CLI's own `--help` is right). And the two waitpoint resume routes disagree on the default for an omitted `approved` field — public callback defaults true (`internal/api/pipeline_waitpoint_callback.go:44-47`), authed route defaults false.

### 2.5 Findings — Inbox and human attention

**F27 — Read state is global, not per user.** `inbox_items.read_at` / `read_by_user_id` are single columns on a shared row (`internal/database/migrate_consts_v162_schedule_catchup.go:53-56`); the first user to PATCH clears it for everyone (`internal/api/inbox_handler.go:801-807`). `target_user_id`/`target_role` govern visibility only (`:46-77`).

**F28 — `/inbox-v2` merges three server truths on the client.** It fetches active inbox, resolved inbox, `/api/v1/approvals` (15s poll) and a paginated walk of `/api/v1/missions` (30s), then dedupes via `payload.approval_id`/`payload.inbox_item_id` cross-links (`components/features/inbox-v2/inbox-v2.tsx:47-114`, `inbox-v2-derive.ts:157-169`). Its own PRD names this as the anti-pattern to remove (`docs/prd/inbox-maximum-wireframe.md`). Grep for `attention_class|action_contract|thread_key|decision_receipt` across `internal/` and the frontend: **zero hits**.

**F29 — Decisions record who and what, never over which version.** `approvals_queue` (`internal/harbormaster/store_mutate.go:158-197`) and `pipeline_waitpoints` both CAS correctly and are idempotent — genuinely good — but neither stores a snapshot of the request that was approved. There is no immutable receipt table.

**F30 — There is no success digest, and the digest setting is dead.** `user_notification_prefs.state` allows `'digest'`, but `internal/notifyroute/prefs.go:31,41-43` states the MVP never writes it and no digest scheduler exists. Grouping exists only as a client-side heuristic over untyped escalations (`inbox-v2-derive.ts:192-220`). External delivery, by contrast, is solid: `UNIQUE(channel_id, dedup_key)` (`internal/database/migrate_consts_v161_notification_prefs.go:90-104`) plus a retry/recovery sweep (`internal/notifyroute/recovery.go:17-40,197-218`).

### 2.6 Findings — surfaces and infrastructure

**F31 — The frontend is a static export embedded in the binary.** `output: "export"` in prod (`next.config.ts:15`), embedded at `web/embed.go:19-20`, served by `internal/api/static.go:85`, 503 everywhere if unbuilt (`:88-96`). Every `[param]` route ships a placeholder from `generateStaticParams()` and reads the real parameter client-side. **No server components, no server actions, no request-time env.** Note the trap: the Next.js source lives at the repo root (`app/`, `components/`, `hooks/`); `web/` is embed glue only.

**F32 — One allowlist decides what realtime exists.** `VALID_REALTIME_TYPES` (`hooks/use-realtime.tsx:141-183`) silently drops anything unlisted. `issue.created`, `issue.deleted` and `issue.started` are emitted server-side and dropped client-side today — the head of open issue **#2125**. Several hooks keep polling backstops precisely because of this (`hooks/use-active-runs.ts:57`, `use-crews-status.ts:37`, `use-approvals.ts:42`).

**F33 — The Runs view cannot show routine runs at all. (Corrected in rev 3 — rev 2 got this wrong.)**
`/runs` is a hard redirect to `/journal?tab=runs` (`app/(dashboard)/runs/page.tsx:9`). `GET /api/v1/runs` aggregates journal entries under **two** inner conditions, both required (`internal/journal/runs.go:264-267`):

```
trace_id IS NOT NULL  AND  entry_type LIKE 'run.%'
```

Routine runs fail both. They write `pipeline.run.started` (`internal/journal/types.go:312`), which does not match `run.%`; and the codebase's own comment states it outright (`internal/journal/queries.go:39-41`): *"pipeline/routine runs never set TraceID (internal/pipeline/journal.go stamps ActorID: runID on every emit instead)"*. A pipeline's own `agent_run` step does not rescue it either — that executes through `internal/pipeline/runner_orchestrator.go`, not `internal/api/assignments_run.go`, which is the only path that stamps `TraceID` and writes `run.*`.

**So the view whose header claims to span "ALL runs in the workspace (routine + ad-hoc agent/chat/user)" is structurally incapable of showing an entire class of execution.** Rev 2 described this as a missing websocket subscription and called the endpoint "a superset across trigger types, which is right". That was wrong, and it understated the defect by a wide margin: adding `pipeline.run.*` subscriptions makes the view refetch when a routine fires, but the refetch returns nothing new, because the gap is in the read-side SQL.

Consequence for the tracks: A6 ships the honest half — the subscriptions, plus a header comment that states the real limitation instead of the false claim. **Making routine runs actually reachable is read-side work on `internal/journal/runs.go` and belongs in its own package**, because it means either widening the entry-type filter and the trace-id requirement, or unifying how the two engines stamp runs. That choice has consequences beyond this view and should not be made inside a frontend package.

**F34 — Test infrastructure is good and has specific holes.** `testutil.MigratedSQLDB` (`internal/testutil/migrateddb.go:166`) plus `drainBackgroundWork` (`internal/api/router_test.go:60`) make integration tests cheap. Mentions, assignment-queue recovery, cancellation, scheduler ticks, catch-up, waitpoint resume and the inbox writer all have real suites. Missing: any Playwright spec touching `/inbox` or mentions, any test of duplicate *run* creation (as opposed to duplicate inbox rows), and any golden-file eval corpus. `internal/api` under `-race` takes ~23 minutes and needs `-timeout 40m` (`CONTRIBUTING.md:244`).

**F35 — A feature-flag system already exists.** `internal/featureflags/featureflags.go:19` — `feature_flags` plus `feature_flag_overrides`, per-workspace override beating instance default. Do not build a second gate.

**F36 — The journal has been unstable this month.** Seven journal fixes landed in the 20 commits before this baseline (`51dcd368e`, `ace2ba24f`, `6e716c826`, `7c07e48d7`, `56beafe65`, `0811dddef`, `7356ae211`). The journal is the substrate for run truth (F33) and 30-day compaction deletes low-signal entries (`internal/consolidate/compact.go:79,507`). **Anything this PRD needs to survive 30 days must not live only in `journal_entries`.**


### 2.7 Findings — the neighbours this design must not break

Added in rev 2. These are subsystems the first draft never mentioned.

**F37 — Every new workspace-scoped table must be classified for backup, or CI fails.**
`internal/backup/intent.go:266` holds a `BackupTableIntent` map; `CategoriseScopedTables` fails with `ErrDiscoveryDrift` on an unclassified table (comment at `intent.go:7-11`). Tables with a bare `workspace_id` and no FK also need an entry in `internal/backup/dbdump.go:10-14`. **And the classification is a real design decision, not paperwork:** `notification_deliveries` — the very table this PRD copies its delivery pattern from — is `IntentExcludeOperational` (`intent.go:254`), i.e. it does not ride backups. If `agent_deliveries` is classified the same way, exactly-once (I1) survives a crash but not a restore. That must be a stated choice.

**F38 — There is no GDPR "cascade" to add a table to.**
The erasure path is hand-written, one `DELETE` per table, inside `AdminGDPRHandler.DeleteUserData` (`internal/api/admin_gdpr.go:276`, statements at `:404,424,438,525`), with a mirrored `SELECT` in `ExportUserData` (`:637,704,740`), a `data_subject_id` column added by a dedicated migration (pattern: `internal/database/migrate_consts_v107_gdpr_cascade.go:1-45`) and an entry in `gdprActionScope`. Four hand-edits per table. Rev 1's §16 wording ("added to the cascade") implied a registry that does not exist.

**F39 — Metrics live in `internal/server/metrics_domain.go`, and percentiles do not exist.**
`internal/telemetry` is OpenTelemetry **tracing** only. The `/metrics` endpoint is hand-rolled Prometheus text computed from DB aggregates at scrape time (`writePromMetric`, `metrics_domain.go:94`; fan-out at `:157` into assignment/queue/pipeline/run-event/cost collectors). There is no Prometheus client in `go.mod`, no histograms, and SQLite has no `percentile_cont`. **Every p95 in rev 1's SLO table was a net-new capability, not a wiring job.**

**F40 — Content replayed into a later prompt escapes the injection guard.**
`internal/lookout` (`middleware.go`, `WithScope`/`InputGuard`/`OutputGuard`) guards a request in flight. A comment stored now and re-fed into a fresh agent's context on a later wake (§11) is outside that scope. `internal/scrubber` covers secrets, not injection. Rev 1 cited scrubber and never mentioned lookout.

**F41 — Two independent expiry clocks would disagree.**
`internal/ephemeral/expiry.go` flips `agents.expired_at` and broadcasts `agent.expired`; it never touches `assignments`. A session's lease (§9.4) is a separate clock. An ephemeral agent can expire mid-session, leaving a session rendered `active` over a dead agent until an unrelated sweep fires.

**F42 — `decision_receipts` would sit outside the only tamper-evidence the codebase has.**
`journal_entries` is HMAC-SHA256 hash-chained per workspace (`internal/journal/verify.go:20-41`, `DeriveChainKey`, `VerifyChain`). Keeping receipts out of the journal (D4) dodges 30-day compaction — correct — but "append-only" then means *by convention*, with nothing preventing a direct `UPDATE`. Rev 1 asserted "no UPDATE path" as if it were enforced.

**F43 — The WS hub drops frames silently under load.**
`ws.Hub.dispatch` (`internal/ws/hub.go:494`) sends non-blocking; a full client buffer drops the frame and only force-disconnects after `consecutiveDropsBeforeDisconnect` (`:513-524`), logged at a sampled rate (`recordDrop`, `:798`). The allowlist (F32) guards *registration*, not *delivery*. Seven new frequent event types on the same mission channel therefore require an explicit gap-detection and resync rule on the client, not just registration.

**F44 — Rate limiting exempts exactly the traffic that would storm.**
`ratelimitcfg.KeyHTTPAPIPerMin` is 12000/min per client IP and its own doc says authenticated CLI tokens are exempt (`internal/ratelimitcfg/ratelimitcfg.go:55,123`). Webhook- and automation-driven comment bursts are the exempt path. Duplicate-run protection is therefore **entirely** the unique index (I1/I2) — which is the right design, but it must be stated rather than left to omission.

**F45 — All six provider adapters are stateless today, and that is load-bearing.**
`adapter_claude.go:114` passes `--no-session-persistence` unconditionally; the captured `msg.SessionID` (`:404,456`) is provenance metadata only. Codex, Cursor, Droid and Gemini have no resume in `BuildCommand`. OpenCode documents `--continue`/`--session` in a comment (`adapter_opencode.go:24-25`) and **never appends them** (`:42-64`). This *aligns* with checkpoint-based continuity (§11) rather than conflicting with it. The risk is regression: nothing stops someone wiring native resume later and creating a second, invisible continuity channel.

**F46 — `ee/` is empty and the license gate is unwired.**
`ee/README.md` states nothing shipped depends on it; `internal/license.HasFeature`/`IsEnterprise` (`license.go:189,201`) have zero call sites outside their own package. No parallel enterprise work is needed — and if any of this is ever meant to be enterprise-gated, that decision does not exist yet.

**F47 — Capacity admission is a second queue the SLOs would not see.**
`admission.Controller.Admit` (`internal/admission/admission.go:302`) gates *container start* on host memory/CPU, called from the docker and apple gates. A session can win the run-claim CAS and sit at `RUNNING` while its container waits behind `Admit`. "Session visible as active < 1s" is measurable from DB writes alone and says nothing about whether a process is running.

**F48 — Two live sweeper precedents already exist and should be copied, not reinvented.**
`harbormaster.StartTimeoutSweeper` (`internal/harbormaster/gate.go:238`) and `internal/ephemeral/expiry.go` (`StartExpirySweeper`, whose comment explicitly says "reuse the existing Routines primitive… NOT a new scheduler") are the ticker-plus-DB-sweep shape the lease reaper needs.

**F49 — Feature-flag keys are duplicated per package.**
`featureflags.IsEnabled(ctx, db, workspaceID, key)` (`internal/featureflags/featureflags.go:19`) returns false for an unknown key. But `runVerdictFlagKey` is declared independently in two packages (`internal/api/internal_runs.go:20`, `internal/pipeline/run_verdict.go:25`), and the flag row must be seeded by a migration. Reusing the system (D7) means picking one canonical constant location and shipping the seed migration — not repeating the existing duplication.

**F50 — Issue comments are append-only in Crewship.**
`GET` and `POST` only (`internal/api/router_orchestration.go:106-107`), no `UPDATE mission_comments` anywhere. Worth recording because Linear's published guidance tells agents *not* to reconstruct history from comments precisely because comments are editable there (§26). That argument does not apply to us today — but it becomes true the moment comment editing is added, which is an argument for events being the source of truth regardless.

### 2.8 Findings — implementation hazards

Rev 3. These cost days if discovered at review time.

**F52 — OpenAPI generation is a regex scan over source text.**
`cmd/gen-openapi/main.go:57,60` matches only literal `r.mux.Handle(Func)("METHOD /path"` and `r.authed(Mut|SelfMut|Admin)("METHOD", "/path"`. Three consequences: a path built with `fmt.Sprintf` or a variable is **invisible** to the generator and then fails `docs-inventory -strict`; a **commented-out registration still lands in the spec** (`internal/api/router_auth.go:78-80` says so explicitly); and `// openapi:` annotations attach only from the unbroken comment run **immediately** above the registration line (`router_orchestration.go:396,410`, enforced at `main.go:190-205`). CI runs freshness (`ci.yml:1957-1964`) and then completeness (`:1985`) as two separate gates — passing the first does not imply the second.

**F53 — A binary downgrade after a migration hard-fails startup.**
`guardVersionSkew` (`internal/database/migrate.go:288-311`) refuses to boot a binary whose max known migration is below the DB's applied max. There is no down-migration path; recovery is re-upgrading or restoring the `*.pre-migrate-*.bak` snapshot. **Rolling back a deploy is not safe once any migration here has run** — §20's rollout must say so.

**F54 — Migrations are timestamped SQL files now, and branch age is a trap.**
`internal/database/migrations/<YYYYMMDDHHMMSS>_<snake_case>.sql`, discovered by directory walk (`migrate_registry.go`), with the legacy Go sequence capped at v169 (`legacySequentialCeiling`). Versions must be strictly ascending, so a long-lived branch that stamps its migration at branch-creation time and merges later **violates ordering** — regenerate the stamp at rebase, not at branch start. Rev 1's §9.9 said "Go migrations, append-only", which is only half true.

**F55 — Cascade behaviour is asymmetric and destroys run history.**
`missions` has **no** `ON DELETE` from `workspaces`/`crews`/`lead_agent_id` (`migrate_consts_v02_v15.go:77-79`); the app hand-deletes. Meanwhile `agent_runs`, `pipeline_runs` and `journal_entries` all cascade from `workspace_id` — deleting a workspace destroys its full run and journal history. And `assignments.chat_id`/`assigned_by_id`/`assigned_to_id` have no `ON DELETE` at all (NO ACTION), so an agent delete that cascades through `chats` can hit a live FK violation. **New tables must hang their cascade on their own `workspace_id`, not on the mission chain.**

**F56 — CI has 140 seconds of formal slack.**
The `internal/api` race job computes `-timeout` as 2× `RACE_API_BASELINE_SECONDS=1400` = 2800s, plus 6 min overhead = 3160s against a 55-minute cap = 3300s (`ci.yml:1601-1780`). Green runs on `main` already measure 1156–1708s. A hard timeout prints no `--- FAIL` and no race warning, just a goroutine dump — it reads exactly like a hang in the new code. Re-measuring the baseline after landing new tests is expected maintenance, not optional. (The migrated-template fixture is *not* a risk: built once per binary, copied per test.)

**F57 — The CAS pattern to copy already exists, and it is correct.**
`PendingRunStore.MarkFired` (`internal/pipeline/pending_runs.go:243-251`): `UPDATE ... WHERE id=? AND status='pending'`, then `err != nil` handled **separately** from `RowsAffected()==0`. That distinction is what makes SQLITE_BUSY surface as an error rather than as a false "someone else won". Copy this shape exactly for every new state machine.

**F58 — Monotonic per-scope allocation already exists, and it is race-free.**
`nextIssueIdentifierTx` (`internal/api/issue_create_core.go:116-165`) does `UPDATE issue_counters SET next_number = next_number + 1 WHERE ... RETURNING next_number` **inside the caller's transaction**, with a seeding `INSERT ... ON CONFLICT DO UPDATE ... RETURNING` on first use. Re-key it on `mission_id` and it gives the per-issue `seq` of §9.1. Its own comment records a production bug from choosing too narrow a key — pick the counter key to match the uniqueness constraint the table actually needs.

**F59 — The database configuration is genuinely good, and it makes this design viable.**
`internal/database/database.go:113-119`: `busy_timeout(30000)`, `journal_mode(WAL)`, `foreign_keys(ON)`, and **`_txlock=immediate`** (write lock taken at BEGIN, so no late upgrade deadlock), with `SetMaxOpenConns(5)`/`SetMaxIdleConns(5)`. Concurrent writers **wait, they do not fail** — which is why the CAS-and-unique-index approach works here. Two caveats: writers still serialize, so a long write transaction directly delays the sub-500ms acknowledgement target; and the comment at `:144` claims `busy_timeout(5000ms)` while the DSN sets 30000 (stale comment, worth fixing).

### 2.9 Findings ported from the rev-1 dev1 audit

The superseded draft recorded twelve observations from a live dev1 session (two routine runs, two clone missions, sixteen delegations, a factory reset). Nine map onto findings above; three were not in this document until rev 3 and get their own numbers. All twelve stay as regression cases.

| Rev-1 observation | Maps to | Status |
|---|---|---|
| An issue reached `REVIEW` with four child issues open | F10 | OPEN — B11 |
| Four linked assignments; the Runs endpoint returned one | F1, F33 | FIXED-IN `a1` — `ListRuns` now matches on `mission_id` |
| Seven linked assignments; Runs returned zero | F1, F33 | FIXED-IN `a1` |
| Comments without a mention never reached active work; an analyst repeated a finished screenshot capture | F3, F13, F15 | OPEN — B2, B5 |
| Issue rows carried too little provenance to reconstruct cause | F1 | FIXED-IN `a1` |
| **A temporary agent-token `403` made the issue board unavailable from a live crew, while unauthenticated reads stayed healthy** | **F60** (new) | OPEN — no package; needs its own issue |
| Board and detail did not repaint from every server-emitted lifecycle event | F32 | PARTIAL — `a6` registers the three issue events; ~39 remain for B11 |
| **The Issues board fetched at most 100 rows, exposed no total or pagination, and rendered some fetch failures as an empty board** | **F61** (new) | OPEN — no package; a truth defect in a shipped surface, 1.0-eligible, needs its own issue |
| `DONE` and `COMPLETED` mixed in one column | F11 | OPEN — B13 |
| Stop changed status without proving execution stopped | F6, F7 | FIXED-IN `a1` (Tier 1); hard kill B7 |
| Delegation replaced the polymorphic assignee with the agent, hiding the human owner | I5, scenario 9 | OPEN — **A10** (§9.10, added rev 3) |
| **Start validated that an assignee exists, not that it is an executable agent** | **F62** (new) | OPEN — folds into A10 (`delegate_agent_id` is typed, so the check becomes a FK) |

### 2.10 Finding status at rev 3

**A branch is a claim, not a fix.** `FIXED-IN` means a red-then-green test exists on a local branch that has not been merged, reviewed by CodeRabbit, or pushed. Nothing below is `MERGED`.

| Status | Findings |
|---|---|
| MERGED — `a1` #2295 (Stop, terminal guards, late-failure leak) and `a2` #2279 (`mission_id`, derivation); **live validation found two A1 defects the package tests never saw** — a `QUEUED` run survived Stop (#2312 → #2317) and a mention-started run on a never-started issue was unreachable by Stop (#2315 → #2320); both fixed and re-checked live | F1, F6 (Tier 1), F7, F9 |
| MERGED — `a3` #2271 (registry now scans every ad hoc `journal.EntryType` under `internal/`+`cmd/`, 140 types, not just `types.go`; sparse `PATCH` validates only what it changes) | F19 |
| MERGED — `a4` #2282 | F20 |
| MERGED — `a5` #2289 (docs, labels, stale comment) | F25, F26, F23 (docs half) |
| MERGED — `a7` #2296 (with F37/F38 integration steps applied; read markers of items that do not land on a fork are skipped and reported, #2274) | F27 |
| MERGED — `a9` #2269 (5 of 7 producers guarded; webhook route, direct agent-run route and peer query are a labelled 1.0 limit) | F2 (the kill; deferral semantics stay B2), F51 |
| MERGED (partial by design) — `a6` #2291 | F18 (read-only display; editor B9), F32 (3 of ~42), F33 (header now honest; read-side SQL unassigned) |
| MERGED (partial by design) — `t1` #2293 | F34 (scenarios 11–13 proven; the RunStore blind spot is #2283) |
| OPEN — Track B | F3, F4, F8, F10, F11, F13, F14, F15, F17, F21, F22, F28, F29, F30 |
| OPEN — filed as issues | F5 (sub-agent streaming), F33 read-side (#2284), F60, F61, the RunStore test blind spot (#2283), issues board resilience (#2285, #2286), parallel-agent file ownership (#2287) |
| ACCEPTED as constraints (design must respect, not fix) | F12 (N5), F16, F24, F31, F35, F36, F39, F40, F41, F42, F43, F44, F45, F46, F47, F48, F49, F50, F52–F59 |
| Applied as integration steps in `a7` #2296 | F37, F38 |
| MERGED — `a10` #2297 | F62 |

---

## 3. Corrections — where earlier assessments were wrong

Recorded so the implementer does not inherit them.

| Earlier claim | Verdict | Reality |
|---|---|---|
| "Crewship has budgets and they are enforced" | **Wrong as stated** | `MonthlyBudgetUSD` is report-only (F25). Only per-run `MaxCostUSD` gates. Cost capture itself is post-hoc (F12). |
| "The steering endpoint is a good base; wire issue comments to it" | **Half right** | It exists and is guarded, but it queues for the *next* turn (F3). Genuine mid-turn interruption is not implemented; do not promise it as live insertion. |
| "Compaction is implemented" | **True but incomplete** | It degrades to plain truncation on timeout or missing summarizer and is never persisted (F14). Two unrelated things are called "compaction" — the other is journal row deletion (`internal/consolidate/compact.go`). |
| "Routines work well" | **Unproven** | Nothing has ever run on a schedule, webhook or automation on a working clone (§2.1). The executor is strong; the product is unoperated. |
| "Cross-run state is a good continuity base" | **Overstated** | `pipeline_routine_state` is a string KV keyed `(pipeline_id, schedule_id, key)` — right for watermarks, wrong for work state. |
| "The prior PRD's issue-session dispatcher reuses the existing admission queue" | **Wrong** | `claimCrewSlot` is a single-table CAS on `assignments.status` (F8). No session, delivery or lease concept exists to extend. |
| "A static-export Playwright journey is an established pattern here" | **Wrong** | Playwright runs against `pnpm dev`; the static-export journey was never built (noted in `docs/prd/chat-as-a-primary-surface.md`). Treat it as new infrastructure or drop it. |
| "`issue.created`/`issue.started` already reach the client" | **Wrong** | Emitted server-side, dropped by the allowlist (F32, issue #2125). |
| "Introduce a `checkpoint` table" | **Name collision** | `checkpoints` already exists (backup/restore fork points) and so does `journal_chain_checkpoints`. Use a distinct name. |
| "Introduce an issue event log" | **Needs justification** | `mission_activity` already carries `mission_id`, `actor_type IN ('user','agent','system')`, `actor_id`, `created_at`, fed by `internal/api/issue_events.go`. Resolved in rev 3: extend it (D6, §9.1). |

### 3.1 Errors in revision 1 of *this* document

Found by an adversarial review of rev 1 against the code. Listed with the same standard applied to everyone else.

| Rev 1 claim | Verdict | What the code says |
|---|---|---|
| "Stop terminates the container command within 5s" (§10.3, scenario 5) | **Not achievable as written** | There is no kill primitive. `ContainerProvider` exposes `Exec` and `ExecInspect`, no `Kill`/`Signal` (`internal/provider/container.go:265-280`); the Docker path hijacks a stream and copies it with bare `io.Copy` goroutines (`internal/provider/docker/docker.go:1751-1820`), so cancelling the outer context stops nothing already attached. Worse, **one container is shared by the whole crew** — killing it SIGKILLs every sibling agent's in-flight exec (`internal/provider/docker/crew_resource_drift.go:49`). See §10.3 rev 2 for the corrected two-tier design. |
| "≥60% fewer input tokens on a repeat wake" (§11, WP-6) | **Measuring something that is not there** | `SkipConvHistory` is unconditionally true on exactly this path (F13), and `mentionTaskBrief` (`internal/api/issue_mentions.go:911-948`) sends only the agent name, issue identifier, title, author and the one triggering comment inside an untrusted fence, plus "read the issue yourself". There is no history to remove. The proposed pack's own budget (2900 tokens before memory) would likely be *larger*. The real defect is not bloat — it is that the agent must rediscover state by tool calls every time and has no record of what it already did. §11.4 rev 2 replaces the target. |
| "Pick DONE, migrate rows, delete six defensive clauses" (WP-1) | **Dangerously undersold** | `ValidMissionTransitions` (`internal/statuses/transitions.go:26-36`) legitimately runs `PLANNING→IN_PROGRESS→REVIEW→COMPLETED` for the mission engine *in the same column* as the issue tracker's `BACKLOG→…→DONE`. `COMPLETED` is a live, correct value written by a guarded CAS (`internal/orchestrator/mission.go:497`). The clause count is 8 files not 6; the literal appears in 201 Go files, 60 TS/TSX and 26 docs — most about `assignments.status`/`pipeline_runs.status`, which must not be touched. This is a decision about two lifecycles sharing one column, not a find-and-replace. |
| "The partial unique index enforces one active turn per session" (§9.4) | **True mechanism, missing wiring** | Partial unique indexes are a supported, precedented pattern here (`migrate_consts_v152_journal_hash_chain.go:80`, `v33_v41.go:103`). But `insertCappedAssignment` (`internal/api/delegation_limits.go:537-577`) has no `session_id` in its struct or its `INSERT`, so the index would guard a column nothing sets. Resolving-or-creating the session must happen in the same statement or transaction as the fan-out guard, or the TOCTOU it exists to close is reintroduced. |
| "Delete the rev-1 draft; git history keeps it" (rev-3 analysis, in conversation) | **Wrong on both counts** | The draft was never tracked, so `rm` would have lost it outright — and it still held four sections this document had not absorbed (owner/delegate schema, input routing, twelve dev1 regression cases, the provider matrix). Caught by a second session's review; ported in rev 3 (§2.9, §9.10, §10.5, A0 step 10). |
| Golden scenarios graded by WP-13, which ships last | **Internal contradiction** | §25 requires the scenarios to exist and fail on the pre-WP baseline; §17 rev 1 built them in the final phase. Rev 2 moves the harness to Track A. |

---

## 4. The product model

Seven objects. The whole design is making these real in the schema and honest in the UI.

| Object | Meaning | Today |
|---|---|---|
| **Issue** | The outcome a human cares about, and where work is judged. | `missions` — real. |
| **Event** | An immutable fact: someone said, decided, finished, failed. | Partly `mission_activity`; no ordering guarantee, no delivery semantics. |
| **Delivery** | The record that a specific event was handed to a specific worker, and whether it was consumed. | **Does not exist** (F4). |
| **Session** | One agent's continuing relationship to one issue, across many runs. | **Does not exist** (F1, F13). |
| **Run** | One attempt — one shift. Can fail without the session failing. | `assignments` / `pipeline_runs` — real but unlinked (F1). |
| **Checkpoint** | Structured work state handed from run N to run N+1. | Three fields, one code path (F15). |
| **Outcome** | The client-meaningful result that decides what happens next. | **Does not exist** (F22). |

And two triggers of work: a **Routine** (recurring, defined) and a **Mention/Assignment** (ad hoc, human-initiated). Both must produce the same Run → Outcome → routing behaviour. Today they do not share a single line of that path.

**Non-negotiable framing:** the issue is the control plane for judged work; the comment table is not a message queue. Delivery gets its own row (the generalised mentions table, §9.3) precisely so that a comment stays a comment.

---

## 5. Goals and non-goals

### Goals

G1. A mention or assignment reaches the intended agent exactly once, survives restart, and is visibly acknowledged to the human in under one second.
G2. A follow-up comment while an agent is working does not start a second run; it is delivered to the existing session.
G3. An agent resuming an issue after days knows what it already did before it acts, and does not redo finished work or rediscover state by re-reading everything. (Measured as repeat-work and time-to-first-productive-action, **not** as token reduction — see §3.1 and §11.4.)
G4. Stop means stopped: the process ends, and no late callback can revive the state.
G5. Every run is attributable to an issue and a session, and the Runs surface shows all execution regardless of trigger.
G6. Authoring a routine from chat produces routine **and** trigger atomically, or nothing.
G7. Every routine run ends with an explicit outcome that deterministically decides whether a human is bothered.
G8. The Inbox contains only things a named person can act on, with per-user read state and an immutable record of what was decided.
G9. A trigger that should have fired and did not is visible without reading logs.
G10. Reliability settings that exist in the backend are reachable by the people who need them.

### Non-goals

N1. Mid-token interruption of a model turn. Delivery lands at the next safe checkpoint (F3, F16). Say so in the UI.
N2. Rewriting the pipeline executor. §2.4 is an asset; extend it.
N3. Cross-replica distributed execution. Leases are for correctness under restart and future replicas, not a scale-out project (F8, F23).
N4. A second orchestration path parallel to missions/pipelines.
N5. Solving cost-accounting completeness. F12/F25 are named and bounded here (A5 fixes the misleading label; a session-level ceiling with a named stop reason, §26.2, is separate work).
N6. Replacing the journal. It stays the audit trail; it stops being the only home for state that must outlive 30 days (F36).

---

## 6. Invariants

I1. **Exactly-once consumption.** An event is delivered to a worker at most once successfully; redelivery after crash must not duplicate work. Enforced by a unique key on `(event_id, target)` plus a consumption CAS.
I2. **One active turn per session.** Two runs of the same session cannot be RUNNING simultaneously. Enforced by a partial unique index, not by application politeness.
I3. **A terminal state is terminal.** No write may move a row out of CANCELLED/COMPLETED/FAILED without an explicit, audited override (fixes F7).
I4. **Truth beats label.** A UI state must be derived from the thing it claims. "Running" means a live run row with an unexpired lease, not a status guess (F31's static export makes client-derived state especially tempting — resist).
I5. **Human owner, agent delegate.** Delegating to an agent never changes the human owner of an issue.
I6. **A peer's approval is not a human approval.** Agent-sourced acknowledgement can never satisfy a waitpoint, gate, or policy decision.
I7. **Workspace scoping is absolute.** Every new table carries `workspace_id` and every query filters on it — the existing mention triggers (`trg_mission_comment_mentions_consistency_ins`) are the model to copy.
I8. **Nothing that must persist lives only in `journal_entries`** (F36).
I9. **Every new API endpoint ships with a CLI command and an acceptance test that drives the binary** (project rule; also the three-gate trap in `docs-inventory -strict`).
I10. **Silence is a decision.** Any path that chooses not to notify a human must record that it chose (outcome + reason), so "why didn't I hear about this" is answerable.

---

## 7. A0 — mandatory truth audit before any schema change

Do not skip. Output is a PR that changes only this document plus a report file. Timebox: one day.

1. **Re-verify §2 against current `main`.** Every `file:line` in §2. Mark each CONFIRMED / MOVED / FIXED. If more than three are stale, stop and re-plan.
2. **Re-run the live-data query** on your clone and paste the table. If schedules/webhooks/automations are still all zero, note that the baseline is unchanged; if not, find out who exercised them and how.
3. **Re-verify the event-log decision** (D6, resolved: widen `mission_activity`, §9.1). The decision stands; what A0 checks is that its three costs still hold — no `workspace_id`, no CHECK on `action`, two writers bypassing the emitter — and that no new writer has appeared since `3fa36df5`.
4. **Confirm name availability** for every proposed table and column: `checkpoints`, `sessions`, `deliveries`, `outbox` are all taken or ambiguous. Grep before you name.
5. **Enumerate the realtime allowlist debt.** List which of the events this PRD adds would be dropped by `hooks/use-realtime.tsx`, and confirm whether #2125 has landed. A6 and B11 both depend on the answer.
6. **Confirm the RBAC pattern** for new mutating routes by reading `internal/api/router_orchestration.go:96-97` and naming the role each new route will use. No route ships without one.
7. **Measure the current context payload and the cost of not having one.** Instrument one assignment run: assembled prompt size, and — more importantly — how many context-gathering tool calls the agent makes before its first productive action, and how often it repeats work a previous run finished. Per §3.1 there is no conversation history on this path to remove, so the baseline being established is for the §11.4 metrics, not for a token-reduction target.
8. **Reconcile scope** with open issues #2256, #2257, #2125, #2233, #2234 and with `docs/prd/inbox-maximum-wireframe.md`, `docs/prd/response-shape-contract.md`, `docs/prd/agent-memory-on-wake.md`. Say which WP absorbs each, and which stay independent. If `gh` is unavailable in your sandbox, say so and use the local docs only.
9. **Check the response-shape trap.** `docs/prd/response-shape-contract.md` documents a live PascalCase/snake_case JSON-tag bug on `/api/v1/approvals`. Every new response type here inherits that risk class; state the convention you will follow and the test that enforces it.
10. **Fill the provider capability matrix** (ported from rev 1 §7.5). For Claude, Codex, Gemini, Cursor, Droid and OpenCode record: native continuation/session handle; compaction support; streaming input or interrupt support; cancellation support and observed termination; whether a fresh process can restore from a Crewship checkpoint; which event fields carry resolved model/session ids; behaviour on container or server restart. F45 has the first column; the rest is empty. The product contract works on the lowest common denominator — faster live steering is an enhancement, not a correctness requirement.

---

## 8. Target architecture

One substrate, two entry points.

```
   HUMAN                          TIME / EVENT
   comment, @mention, /assign     schedule, webhook, automation
        |                                   |
        v                                   v
   +----------------------------------------------------+
   |  EVENT  (immutable fact, ordered per issue)         |
   +----------------------------------------------------+
        |
        v
   +----------------------------------------------------+
   |  DELIVERY  (event -> target, exactly-once claim)    |
   |  unique(event_id, agent_id)  (§9.3)                 |
   +----------------------------------------------------+
        |
        +--- session idle -----> wake: start a RUN
        |
        +--- session active ---> enqueue for the next safe
                                 checkpoint of the live RUN
                                 (STOP is the exception: it
                                  cancels immediately)
        |
        v
   +----------------------------------------------------+
   |  RUN  (leased, cancellable, attributable to issue)  |
   |     assignments.*        |     pipeline_runs.*      |
   +----------------------------------------------------+
        |
        v
   +----------------------------------------------------+
   |  CHECKPOINT  (structured state for the next run)    |
   +----------------------------------------------------+
        |
        v
   +----------------------------------------------------+
   |  OUTCOME  (NO_CHANGE | SUCCEEDED | WORK_CREATED |   |
   |            PARTIAL | NEEDS_HUMAN | FAILED |         |
   |            CANCELLED)                               |
   +----------------------------------------------------+
        |                |                |            |
        v                v                v            v
    history only    issue comment /    INBOX      System Health
    (digest)        new issue          (a person   (operator, not
                                        must act)   the client)
```

Why this shape:

- **Delivery is a separate record from the comment** — a row in the generalised mentions table, never a column on `mission_comments` — so the comment table stays a comment table (F4), so redelivery is provable, and so "the agent never saw it" stops being indistinguishable from "the agent ignored it".
- **Session sits between issue and run** so a run may fail without losing the relationship, and so I2 has something to be unique on (F1, F13).
- **Outcome is separate from status** because status answers "did the process end" and outcome answers "should a human care" (F22). Routing off status is why success and infrastructure noise land in the same list today.
- **Both entry points converge before Outcome**, so the Inbox has one contract to honour rather than three producers with three conventions (F20, F30).

---

## 9. Data model — three new tables, not six

**Rev 3 rewrite.** Rev 1 proposed six new tables. A simplification pass against the code found that three of them fold into tables that already exist and already have the right shape. The guarantees are unchanged; the number of things to reason about is halved.

Rules that apply to everything below: migrations are timestamped SQL files with strictly ascending stamps generated at rebase time (F54); every table carries `workspace_id` with its own cascade, never relying on the mission chain (F55, I7); every state machine copies the CAS shape of `PendingRunStore.MarkFired` (F57); every new table passes the §16.1 integration checklist before it is done.

### 9.1 Events → widen `mission_activity`, do not create `issue_events`

`mission_activity` (`internal/database/migrate_consts_v33_v41.go:198-206`) already has `mission_id`, `actor_type` with the exact `CHECK(actor_type IN ('user','agent','system'))` this design wants, `actor_id`, `action`, `details`, `created_at`, and one central writer (`internal/api/issue_events.go:196-206`).

Add four nullable columns: `seq INTEGER`, `payload_json TEXT`, `source_kind TEXT`, `source_id TEXT`. Plus `UNIQUE(mission_id, seq)` and an index on `(source_kind, source_id)`.

**Three things this costs, all of which must be done and none of which are optional:**

1. **`mission_activity` has no `workspace_id`** — verified against the live schema. I7 requires one. Add it nullable, backfill by joining `missions`, then enforce. This is the single biggest cost of the merge and it is still cheaper than a parallel table.
2. **`action` has no CHECK constraint** — it is an open set today, and the emitter's own comment admits it. Constrain it now, while the table is being widened anyway. Live values to preserve: `status_changed`, `task_completed`, `parent_changed`, `assignee_changed`, `created`, `task_failed`, `review_approved`, `priority_changed`. Note what is *absent* from that list: **there is no `commented` action**. Today this table is a status-change audit, not an event log — widening it is a genuine change of purpose and the PR must say so.
3. **Two writers bypass the emitter** and insert directly (named in `internal/api/issue_events.go:60-66`: `assignments_run.go` and `orchestrator/mission_tasks_completion.go`). They must move to the emitter or allocate `seq` themselves. A row without `seq` is invisible to every cursor.

`seq` is allocated with the `nextIssueIdentifierTx` pattern re-keyed on `mission_id` (F58) — `UPDATE ... RETURNING` inside the caller's transaction, race-free because SQLite serializes writers. Wall-clock `created_at` is not orderable enough under concurrent writes.

### 9.2 `issue_agent_sessions` — new, irreducible, thinner than rev 1

Nothing in the schema has `UNIQUE(mission_id, agent_id)`. `mission_comment_mentions` is keyed `UNIQUE(comment_id, agent_id)` — many rows per agent per mission, the wrong cardinality to hold one evolving cursor. `mission_tasks.handoff_context` is one overwritten column scoped to a task. So a durable row is genuinely required.

| Column | Notes |
|---|---|
| `id`, `workspace_id`, `mission_id`, `agent_id` | `UNIQUE(mission_id, agent_id)`. Cascade on `workspace_id` (F55). |
| `state` | `pending` \| `active` \| `awaiting_input` \| `idle` \| `error` \| `stale` \| `closed` |
| `last_consumed_seq` | The cursor. Everything above it is unread for this session. |
| `active_run_id` | NULL unless a run is live. |
| `agent_version` | Stamped from `agent_config_history` at creation (§11.6). |
| `last_activity_at`, `created_at`, `updated_at` | |

Dropped from rev 1: `opened_by_user_id` and `opened_reason` — log them in the event payload instead of carrying columns nothing queries.

### 9.3 Deliveries → generalise `mission_comment_mentions`, do not create `agent_deliveries`

`mission_comment_mentions` already has `dispatch_state`, `assignment_id`, a workspace-consistency trigger set, and a unique index. It is 90% of the delivery table.

Widen it: `comment_id` becomes nullable; add `event_id TEXT` referencing the event row, `state`, `claimed_by_run_id`, `priority`; replace `UNIQUE(comment_id, agent_id)` with `UNIQUE(event_id, agent_id)` — that index is invariant I1 in one line.

**Two costs, both verified:**

1. **`dispatch_state` and `state` are different questions.** The existing column answers "did the dispatcher create an assignment" (`dispatched|refused|skipped|failed`); the new one answers "did a run consume this" (`pending|claimed|consumed|failed|superseded`). Keep both. Conflating them loses the distinction between "never dispatched" and "dispatched but never consumed", which is exactly the F4 blind spot.
2. **A nullable `comment_id` breaks the existing trigger.** `trg_mission_comment_mentions_consistency_ins` does `SELECT mission_id FROM mission_comments WHERE id = NEW.comment_id`; with a NULL comment id that subquery yields NULL, `NULL IS NOT NEW.mission_id` is true, and the insert **aborts**. SQLite has no `ALTER TRIGGER`, so the migration must drop and recreate both the insert and update triggers with a `NEW.comment_id IS NOT NULL AND ...` guard. Verified directly against the live schema — do not discover this in review.

Claim and consume are separate CAS statements (F57). A crash between them leaves a `claimed` row whose run is dead; the lease sweep returns it to `pending` with `attempts+1`. This is the shape `notification_deliveries` already uses in production (`UNIQUE(channel_id, dedup_key)` plus `internal/notifyroute/recovery.go:197-218`) — and note its backup classification is `IntentExcludeOperational`, which is a decision this table must make consciously (F37).

### 9.4 `assignments` — six new columns, not eight

| Column | Why it is required |
|---|---|
| `mission_id` | F1. Track A2. |
| `session_id` | Links a run to its session; only needed because §9.2 exists. |
| `lease_owner`, `lease_expires_at` | No lease mechanism exists at all (F8). |
| `cancel_requested_at` | `status` carries no cooperative-cancel signal (F6). Track A1. |
| `outcome` | §9.6. Routing today can only read `status`, which `internal/consolidate/mission_outcome.go:33-46` shows collapsing to a 3-way squash. |

Dropped from rev 1: `outcome_reason` (reuse the existing `result_summary`/`error_message` rather than adding a third free-text column) and `cancel_requested_by` (attribute it in the journal entry, the way `harbormaster.Decide` already attributes approvals).

Indexes: `(mission_id, created_at DESC)`; `(session_id)`; `(lease_expires_at) WHERE status='RUNNING'`; and

```
CREATE UNIQUE INDEX idx_assignments_one_active_per_session
  ON assignments(session_id)
  WHERE status IN ('PENDING','QUEUED','RUNNING') AND session_id IS NOT NULL;
```

That index is invariant I2. **It guards nothing until `insertCappedAssignment` sets the column** — `cappedAssignment` (`internal/api/delegation_limits.go:509-516`) has no `SessionID` field and its INSERT (`:566-568`) does not list one. Resolve-or-create the session inside the same transaction as the fan-out guard, or the TOCTOU this index exists to close comes straight back.

Prior art: `chatbridge.tryMarkRunStart` (`steer.go:60-77`) was this same guarantee, in memory, keyed on `chat_id`, and it left the assignment door open (F51). A9 has since extracted it as `AgentRunLock`, re-keyed it on the agent, and closed that door. B3 is the next step: the same guarantee in the database, keyed on the session, so it survives a restart and a second replica.

### 9.5 `agent_session_checkpoints` — new, one JSON column

`HANDOFF` (`internal/orchestrator/mission.go:100-137`, enforced at `mission_tasks.go:321-329`) is the right parsing and enforcement machinery and should be reused as-is. Its *storage* cannot serve: `mission_tasks.handoff_context` is a single overwritten column, per task, not per session, with no sequence marker. §9.5's "keep all rows" requirement rules it out.

`id`, `workspace_id`, `session_id`, `run_id`, `seq_at_write`, `checkpoint_json`, `created_at`.

One JSON column, not four — that is this codebase's convention (`payload_json`, `approvals_queue.payload`, `pipeline_waitpoints.decision_payload`). The document schema inside it stays as rev 1 specified: `done`, `plan`, `facts`, `blockers`, `next_step`, `confidence`.

Explicitly not in `journal_entries` (I8, F36).

### 9.6 Outcome — shared enum for both entry points

Unchanged from rev 2. `outcome` on `assignments` and `pipeline_runs`, with a CHECK:

| Value | Meaning | Default routing |
|---|---|---|
| `NO_CHANGE` | Ran, nothing to do. | History only. Digest-eligible. |
| `SUCCEEDED` | Did the work, nothing needed. | History + digest. |
| `WORK_CREATED` | Produced or updated an issue. | Comment on the issue, deduped by `thread_key`. |
| `PARTIAL` | Some done, some failed, no human needed yet. | History + issue comment. |
| `NEEDS_HUMAN` | Blocked on a decision, input or credential. | **Inbox**, with an action contract (§12). |
| `FAILED` | Ran and failed after retries. | Inbox once retries are exhausted. |
| `CANCELLED` | Stopped by a human or superseded. | History. |

Set by the runner, never inferred by a consumer. A run ending without one is `FAILED` with `outcome_reason='no outcome reported'` — an absent outcome is a bug, not a silent success. `status` stays technical; `runverdict` stays an advisory LLM judgment. Three fields is one more than ideal, so the PR that adds `outcome` also documents the difference in `docs/guides/routines.mdx`.

### 9.7 `inbox_item_reads` — new, and already minimal

`(inbox_item_id, user_id)` composite PK, `read_at`. Read state becomes a LEFT JOIN. The existing `read_at`/`read_by_user_id` columns stay and keep answering "someone dealt with it", which is a different question. This is Track A7.

### 9.8 Decision receipts → two columns, not a table

Rev 1 proposed `decision_receipts`. Almost all of it already exists:

- `approvals_queue` has `decided_by`, `decided_at`, `payload` (the verbatim request) and `Decide` already emits a matching journal entry (`internal/harbormaster/store.go:23-38`).
- `pipeline_waitpoints` has `decided_by_user_id`, `decided_at`, `decision_payload`, `status`.
- `inbox_items` has `resolved_by_user_id`, `resolved_at`, `resolved_action`.
- Session control (Stop) gets the same via `cancel_requested_at` plus its journal entry.

The one thing genuinely missing across all four is the question §9.8 was invented to answer: *was it the same version that then ran?* So add **`routine_version` (or `definition_hash`)** to `approvals_queue` and `pipeline_waitpoints`. Nothing else. The journal entry already carries payload, refs and trace id as the effect record.

What is lost: a single cross-kind query target — four call sites instead of one JOIN. That is a real but small cost, and it is smaller than a seventh table whose "append-only" would have been convention-only anyway (F42).

### 9.9 Migration and backfill

- One migration per package; never edit a shipped one (`scripts/lint-migrations`, plus the strictly-ascending scheme).
- **Stamp at rebase, not at branch start** (F54).
- Backfill `assignments.mission_id` from `mission_comment_mentions.assignment_id`; expect ~0 rows and write the non-zero test anyway.
- `mission_activity.workspace_id` backfills by joining `missions`.
- The `mission_comment_mentions` triggers must be dropped and recreated, not altered (§9.3).
- **Net: 3 new tables, 4 widened tables, ~7 migrations.** The migration count barely moves; what halves is the number of tables and joins anyone has to hold in their head.

### 9.10 Ownership fields on `missions` (ported from rev 1 — Track A10)

Invariant I5 says the human owner stays the owner when an agent is delegated to, and scenario 9 tests it. Until rev 3 this document had no schema that could enforce either: `missions` carries a polymorphic `assignee_type`/`assignee_id`, and delegation overwrites it with the agent (rev-1 dev1 observation 11, §2.9). A UI that renders the agent in the owner slot is a truth defect in a shipped surface.

```sql
ALTER TABLE missions ADD COLUMN owner_user_id    TEXT REFERENCES users(id)  ON DELETE SET NULL;
ALTER TABLE missions ADD COLUMN delegate_agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL;
```

Rules:
- backfill `owner_user_id` from rows where `assignee_type='user'`, `delegate_agent_id` from `assignee_type='agent'`;
- `assignee_type`/`assignee_id` stay as a compatibility projection for the migration window; their removal is a separate versioned change;
- delegation writes `delegate_agent_id`, never `owner_user_id`;
- Start uses `delegate_agent_id` or an explicitly chosen executable agent — the typed FK is what closes F62 (Start validated existence, not executability);
- public DTOs expose `owner` and `delegate` independently.

**Why Track A and not B:** it is additive, nullable, backfilled — the same shape as A2's `mission_id` — and it is the schema behind an invariant Track A already claims and a scenario Track A already lists. It is the largest A item and should ship last in A. `ON DELETE SET NULL` for the same reason A2 chose it (F55): deleting a user or agent must not delete the issue.

## 10. State machines

### 10.1 Session

```
            mention / assign / routine
  (none) ─────────────────────────────> pending
                                           │ run claimed
                                           v
                    ┌──────────────────> active ──────────────┐
                    │                      │                  │
   delivery arrives │       run ends       │ run needs human  │ run fails
                    │                      v                  v
                  idle <───────────── awaiting_input        error
                    │                      │                  │
                    │  no activity 14d     │ human answers    │ human retries
                    v                      └──────────────────┘
                  stale ──────── human or agent reopens ──────> pending
                    │
                    v  issue terminal
                 closed
```

Rules: `pending → active` requires winning the run-claim CAS; only a live run may hold `active`; the lease sweeper moves an `active` session whose run lease expired to `error` with a reason; `closed` is set when the issue reaches DONE/CANCELLED/DUPLICATE and is the only state that refuses new deliveries (they are recorded `superseded`, never dropped silently — I10).

### 10.2 Delivery

```
pending ──claim CAS──> claimed ──consume CAS──> consumed
   ^                      │
   └── lease reaped ──────┘ (attempts+1; after 5 → failed, raises NEEDS_HUMAN)
   │
   └── target closed ────> superseded
```

### 10.3 Run cancellation — two tiers, honestly labelled (fixes F6, F7)

Rev 1 claimed a hard kill. The code has no kill primitive and the container is shared by the crew (§3.1). So cancellation ships as **two separate guarantees, delivered in order, each labelled in the UI as what it actually is.**

**Tier 1 — Cooperative stop (achievable now, no new container capability).**

```
RUNNING ──human Stop──> cancel_requested_at set + journal entry (§9.8)
                          │
                          ├─ in-process: the run's context is cancelled, so no
                          │  further model call, tool call or step is started
                          │
                          └─ other replica / after restart: the runner polls
                             cancel_requested_at at every step boundary
                          │
                          v
                 status=CANCELLED, outcome=CANCELLED
                          │
                          v
   every later write for this run is REFUSED by the terminal-state guard
```

This alone fixes F6's worst half (the run keeps *starting new work* today) and all of F7. The honest label is **"Stopping — will finish the current step"**, and the UI must say that rather than implying an instant kill.

**Tier 2 — Hard termination (new capability, separately scoped).**
Requires: persisting `ExecResult.ExecID` (`internal/provider/container.go:194-197`) on the run; discovering the PID inside the container from `ExecInspect`; and signalling that process — Docker has no "kill this exec" call, so this is `kill -TERM <pid>` executed *into* the same container, never `docker kill` on the container itself, which would take out every sibling agent on the crew. Only when Tier 2 lands may the UI promise a bounded stop time.

Terminal-state guards are Tier 1 and non-negotiable: every write to `mission_tasks.status` and `assignments.status` gains `AND status NOT IN ('CANCELLED','COMPLETED','FAILED')`, matching what `missions` already does at `internal/orchestrator/mission.go:497`.

Golden scenario 5 is split accordingly (§18).

### 10.4 Issue status and children (fixes F10)

Add one rule: an issue with non-terminal sub-issues (`parent_issue_id`) or non-terminal `mission_tasks` may not transition to `DONE` or `REVIEW` without `?force=true`, which emits a hash-chained journal entry naming who forced it (§9.8). Everything else in `ValidIssueTransitions` stays.

### 10.5 Input routing (ported from rev 1 — the Track B contract)

Sessions do not exist yet (§9.2 is B1), so this table is the contract B2 implements, not current behaviour. It is here so that "which session does this input wake" is decided once, in writing, rather than per call site.

| Input | Recipient | While a session is active |
|---|---|---|
| Explicit `@agent` mention | The mentioned agent's session(s), within the existing mention fan-out cap | Queue into each existing session; create the missing ones. |
| Reply to an agent's activity | That activity's agent session | Queue for the next safe boundary (N1); live steer only if the adapter supports it — none does today (F45). |
| Plain comment, one delegate | The delegate's session | Queue and acknowledge. |
| Plain comment, several participants | The primary delegate only; others get a human notification per subscription | Ask for an explicit target when it matters. **Never wake everyone by default.** |
| Agent result or hand-off | The parent session waiting on that assignment | Resume only the causally waiting session. |
| Human approval | The exact waitpoint or session that asked | Resume that waitpoint; never mint a generic assignment. |
| `STOP`, `HOLD`, `RESUME`, `VETO` | The server control path (§14.1) | Enforce policy and the state transition; **text alone is never authoritative** (I6). |

---

## 11. Context assembly — the "clean mind" contract

The target is *not* more memory. It is a small, exact packet plus the delta since the agent last looked.

### 11.1 What a woken agent receives

Ordered, and budgeted:

1. **Stable system prompt** — agent persona and permissions. Unchanged; must stay byte-stable for cache reuse (F16).
2. **Issue snapshot** (≤ 800 tokens): identifier, title, goal, acceptance criteria, status, human owner, labels, dependencies, links.
3. **Latest checkpoint** (≤ 600 tokens): `done`, `plan`, `facts`, `blockers`, `next_step`, `confidence` (§9.5).
4. **Unread delta only** (≤ 1200 tokens): `mission_activity` rows where `seq > last_consumed_seq` (§9.1), oldest first, each rendered as `#seq · actor · kind · text`. Over budget → oldest are summarised into one line each, never dropped silently.
5. **Relevant memory** — existing tiered memory and episodic recall, unchanged (`internal/orchestrator/memory.go:111-163`, `internal/episodic/hybrid.go:98-247`).
6. **Artifact manifest** (≤ 300 tokens): files touched, PRs, prior runs, with ids to fetch.

Everything else stays retrievable through tools, never auto-injected. Add a sidecar verb `GET /issue/{id}/comments` — today the sidecar can write a comment but cannot read the thread (`internal/sidecar/issue_verbs.go` is write-only for comments), which is why an agent that wants history has no option but to be handed all of it.

### 11.2 Where it goes

Into the **volatile user-message block** (`[SESSION CONTEXT]`), never the cached system prefix (F16). This is not a style preference: putting per-wake content in the system prompt breaks the prompt cache for every subsequent turn that day.

### 11.3 What the agent must write back

A checkpoint at the end of every run, and at every waitpoint. Enforce it the way HANDOFF is enforced (`internal/orchestrator/mission_tasks.go:321-329`) but for all session runs, and record `Parsed=false` explicitly when the model does not comply (`mission_tasks_completion.go:98` is the existing precedent) so the failure is measurable rather than invisible.

### 11.4 What this is optimising for — and what it is not

Rev 1 set a "≥60% fewer input tokens" target. That was wrong (§3.1): on the mention path there is no conversation history in the prompt to remove, and today's brief is a few hundred tokens. The pack proposed here is *bigger*, deliberately — the defect is not bloat, it is that the agent arrives knowing nothing and must rediscover state by tool calls, every time, with no record of what it already did.

The correct objectives, all measurable:

| Objective | Metric | Direction |
|---|---|---|
| The agent does not redo finished work | repeat-work rate: steps in run N+1 that duplicate a step recorded done in run N's checkpoint | → 0 |
| The agent does not spend its first minutes rediscovering state | context-gathering tool calls before the first productive action | ↓, measured against the A0 baseline |
| Context stays bounded as an issue ages | assembled pack size in tokens | **capped**, not minimised: ≤ 2900 + memory, and it must not grow with thread length |
| A degraded context is visible | share of runs whose context was truncated rather than summarised | reported, alarmed above a threshold |

Note the third row: the property that matters is that a 200-comment issue and a 5-comment issue produce the *same size* wake, because only the delta and the checkpoint are injected. That is the actual win, and it is a bound, not a reduction.

**Compaction must also stop failing silently.** `buildConversationContextWithStats` records which path it took — fit, summarised, or truncated — as a field on the run, not only as a journal entry (F14, F36, I8). A run whose context was truncated is today indistinguishable from one that fit, and it is the likeliest cause of "the agent forgot".

### 11.5 Interruption is an event type, not a side channel

The industry contract (§26) treats interruption as a first-class event on the session — Anthropic's Managed Agents accept a `user.interrupt` event alongside `user.message`. Crewship's delivery priorities (§9.3: `stop` > `correction` > `normal`) must be modelled the same way: an interrupt is an *event with a priority*, consumed at the next safe boundary, not a separate RPC. This keeps one ordered history and makes "the agent was told to stop at seq 41 and stopped at seq 43" reconstructable.

Non-goal N1 stands — we land at the next safe boundary, not mid-token — but the *contract shape* should match the field so it can tighten later without a schema change.

### 11.6 Pin the agent version to the session

`agent_config_history` already exists (`agent_id`, `version`, `changes`, `snapshot`, `UNIQUE(agent_id, version)`) and **nothing reads it** — its only references are the schema, the backup manifest and a test. Meanwhile an agent's system prompt can be edited mid-session and no run records which version it ran under. Managed Agents pin a session to an agent version for exactly this reason (§26).

`issue_agent_sessions` therefore carries `agent_version INTEGER NULL`, stamped at session creation from `agent_config_history`. Cheap, and it makes "why did it behave differently on Thursday" answerable.

---

## 12. Inbox — the attention contract

The Inbox is a queue of things a *named person* can act on. Three lanes, server-decided.

**Requires action.** Only where a specific, permitted action exists: approve/reject, supply missing input, connect a credential, choose between options, review output, resume a disabled routine, take over a blocked issue. Every item carries an **action contract**:

```json
{
  "attention_class": "decision|input|review|repair",
  "thread_key": "routine:daily-triage:2026-09-01",
  "who_can_act": ["role:MANAGER", "user:usr_123"],
  "actions": [
    {"id": "approve", "label": "Approve", "effect": "Resumes run prn_… at step 4", "irreversible": false}
  ],
  "deadline_at": "2026-09-02T09:00:00Z",
  "context": {"issue": "WEB-13", "routine": "daily-triage", "run": "prn_…"}
}
```

`thread_key` is **server-side** (fixes the client heuristic at `inbox-v2-derive.ts:192-220`): the same recurring condition updates one card instead of creating a new one every morning.

**Updates.** Agent finished, routine created an issue, a rule auto-disabled, non-blocking warnings, and the **digest** of successful and no-change runs — the `'digest'` state that already exists in the schema and has no scheduler (F30). B10 gives it one.

**History.** Resolved items plus the decision records of §9.8 — `decided_by`/`decided_at`/`routine_version` on the subject rows and their hash-chained journal entries — searchable.

Two hard rules:
- `NO_CHANGE` and `SUCCEEDED` never create an item (I10 means they are still *recorded*, in the digest).
- Infrastructure failures a client cannot fix (sidecar down, disk, provider outage) go to operator System Health, not the client Inbox. That routing decision belongs to the producer, which is why outcome (§9.6) must be set at the source.

Convergence: `/inbox-v2` stops merging on the client (F28) once the server returns one list with the contract above. Keep it behind the existing feature-flag system (F35) until parity is proven, then promote it to `/inbox`.

---

## 13. Routines — from "engine" to "product"

### 13.1 Atomic authoring (fixes F17)

Extend `save_routine` (`internal/sidecar/routine_mcp.go:22-53`) with an optional `trigger` block:

```json
{
  "definition": {...},
  "trigger": {"kind": "schedule", "cron": "0 9 * * 1-5", "timezone": "Europe/Prague",
              "catchup_policy": "once", "max_consecutive_failures": 5},
  "activation": "draft"
}
```

Server-side, routine + version + trigger are created **in one transaction**; either all exist or none do. `activation: "draft"` creates the trigger disabled and raises a `NEEDS_HUMAN` inbox item — "Routine X is ready. First run would be tomorrow 09:00. Activate?" — which on approval stamps `routine_version` on the approval row and emits the hash-chained journal entry that is the decision record (§9.8). The authoring skill (`internal/skills/bundled/crewship/routine-author/SKILL.md`) must be updated in the same PR to require the trigger and to state, in its final message: what was created, when it first runs, and whether it is active or awaiting approval.

A routine with no trigger and no explicit `"trigger": "manual"` is a warning on the routine page, not a silent success.

### 13.2 The reliability surface, made reachable (fixes F18)

The schedule editor replaces the raw cron box with:

| Control | Backed by |
|---|---|
| **When** — natural language + cron + **preview of the next five fire times in the chosen timezone** | `cron_expr`, `timezone` (`internal/pipeline/schedules.go:735,751`) |
| **If it overlaps** — skip / queue / replace / parallel | `concurrency_key`, `max_concurrent` (F23 — and fix the 429-vs-queue doc lie, `docs/guides/routines.mdx:2047`) |
| **After downtime** — skip / run once / backfill all (cap 20) | `catchup_policy` (`schedules.go:785-822`) |
| **Only run if** — wake gate + fail-open/fail-closed | `wake_pipeline_id`, `wake_fail_closed` (`:1192-1218`) |
| **On repeated failure** — disable after N | `max_consecutive_failures` (`:1073-1129`) |
| **Version** — latest or pinned | `target_pipeline_version` (`executor.go:739-754`) |
| **Health** — last run, next run, consecutive failures, **disabled reason**, missed count, wake stats | the seven fields the UI currently never renders |

Nothing new in the backend. This WP is almost entirely frontend plus CLI parity, which is why it is cheap and high-value.

### 13.3 Triggers that can fire (fixes F19, F20, F21)

- **Closed event registry.** Export the journal's entry types as a real registry and validate `event_type` membership, not shape (`internal/api/automations.go:71`). The existing code comment says the registry does not exist; create it, and generate it from `internal/journal/types.go` so it cannot drift.
- **Payload keys validated** against the registered payload schema for that event type at save time.
- **"Test against recent events"** promoted from the opt-in preview endpoint (`internal/api/automations_preview.go:39-91`) into the create flow: you cannot save a rule that matched nothing in 7 days without acknowledging it.
- **Uniform failure observability.** Webhook fire failures and automation enqueue failures emit a journal entry and, on repetition, an inbox item — matching what schedules already do (`internal/pipeline/schedules.go:1058-1069`). Fixes the `logger.Error`-only path at `internal/automation/registry.go:734-736`.
- **"Should have fired, didn't" detector.** A periodic check comparing expected fire times against created runs, surfacing to System Health. `last_missed_count` already exists; nothing reads it.
- **Webhook update route** (F21): `PATCH /api/v1/crews/{crewId}/pipeline-webhooks/{id}` with explicit, opt-in token rotation. Currently editing means losing the URL.

### 13.4 Continuity between runs (three layers, not one)

1. `pipeline_routine_state` — deterministic cursors and watermarks. Exists, keep as is.
2. **Run checkpoint** — the structured state of §9.5, written by agent steps.
3. **Outcome ledger** — the last N outcomes, changes, failures and human decisions for this routine, rendered into the next run's context.

Do not copy the previous run's chat into the next run. The delta plus the checkpoint is the point.

---

## 14. API, events, CLI

Every endpoint below ships with a CLI command and a binary-driving acceptance test (I9), and each one trips three gates: OpenAPI regeneration, `docs-inventory -strict`, and the read-scope invariant test (`ci.yml:1955,1984`; see also open issue #2144).

### 14.1 HTTP

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/api/v1/crews/{crewId}/issues/{identifier}/sessions` | read | Sessions on an issue, with state and unread count. |
| GET | `/api/v1/crews/{crewId}/issues/{identifier}/events?after_seq=` | read | The ordered event log. |
| POST | `/api/v1/crews/{crewId}/issues/{identifier}/sessions/{sessionId}/control` | mutate, MANAGER | `{"action":"stop"\|"pause"\|"resume"\|"handoff"}`. Stamps `cancel_requested_at` and emits the hash-chained journal entry that is the decision record (§9.8). |
| GET | `/api/v1/crews/{crewId}/issues/{identifier}/checkpoints` | read | Checkpoint history for the issue. |
| PATCH | `/api/v1/crews/{crewId}/pipeline-webhooks/{id}` | mutate | F21. |
| GET | `/api/v1/inbox` | read | Now returns `attention_class`, `thread_key`, `actions[]`, per-user `state`. |

The existing `POST .../issues/{identifier}/stop` is **not** left as a second, weaker stop path. A1 makes it delegate to the real cancellation path; its behaviour changes from "flip rows" to "actually stop", which is a fix, not a break. Say so in the changelog.

CLI parity (naming must follow the existing `issue <verb>` convention — `issue session control` would be the first `issue <noun> <verb>` in 28 subcommands; prefer `crewship issue stop <IDENT> [--session S]`, `crewship issue sessions <IDENT>`, `crewship issue events <IDENT> --after 42`, `crewship routine webhooks update`).

### 14.2 Realtime events

New: `issue.session.created`, `issue.session.state`, `issue.delivery.acked`, `issue.checkpoint.written`, `run.outcome`, `routine.trigger.missed`, `inbox.item.actionable`.

**Every one of them must be added to `VALID_REALTIME_TYPES` (`hooks/use-realtime.tsx:141-183`) in the same PR that emits it, with a test.** This is the single enforcement point (F32), and three existing issue events are already dropped there (#2125). A6 fixes those three first; adding seven more (B11) on top of a broken allowlist is how you ship a board that silently never updates. Registration is not delivery — see F43 for the resync rule B11 must also carry.

---

## 15. Human surfaces

**Issue page.** A session strip: which agent is engaged, its state, unread count, current run with elapsed time and a working stop button, and the last checkpoint's `next_step` rendered as one sentence. Sub-agent output must stream (F5) or the strip must say plainly that it will only appear on completion — a spinner that means nothing is worse than a label that admits the limit (I4).

**Acknowledgement under one second.** On comment, the server writes the event and delivery and pushes `issue.delivery.acked` before any model call. The human sees "Frontend received your message" immediately; "Frontend is working" follows when the run claims the session.

**Routine page.** One page answering: what it does, when it next runs, what it did last time (with outcome, not just status), how healthy it is, who it bothers, and what it costs.

**Runs.** `/journal?tab=runs` cannot show routine runs at all today — the read-side SQL excludes them (F33 rev 3). A6 makes the header say so. Until the read side is fixed in its own package, the honest label is the deliverable; a subscription to `pipeline.run.*` alone changes nothing the user can see.

**Owner vs delegate.** The human owner stays the owner. Agent engagement renders as delegation (I5). Never render an agent in the owner slot.

---

## 16. Security, tenancy, privacy

- **Workspace scoping** on every new table and query (I7), with consistency triggers copied from the mention tables.
- **RBAC named per route** (§14.1). No mutating route ships without a role; A0 step 6 confirms the pattern.
- **Peer ≠ human** (I6): the control endpoint and every waitpoint resume must reject an agent-sourced actor for approval semantics.
- **Scrub before persist.** `mission_activity.payload_json` (§9.1) and checkpoint bodies pass through `internal/scrubber` — issues #2215, #2228, #2229 are all "raw prompt text reached an append-only sink". Do not add a fourth.
- **Erasure.** There is no cascade to join (F38): each new table that can hold user text gets `data_subject_id` and the four hand-edits of §16.1. Note open issue #2233: `approvals_queue` currently has no retention sweep — do not copy that omission.
- **Retention.** Checkpoints and deliveries are excluded from the 30-day journal compaction sweep (I8, F36) and get their own retention policy, stated explicitly.


### 16.1 The integration checklist every new table must pass

Rev 2 addition. Each item is a real gate, not advice.

| Step | Where | Consequence of skipping |
|---|---|---|
| Classify for backup | `internal/backup/intent.go:266` (`BackupTableIntent`), plus `dbdump.go:10-14` if there is no FK | CI fails with `ErrDiscoveryDrift` (F37) |
| **Decide, in writing, whether deliveries ride backups** | same map — note `notification_deliveries` is `IntentExcludeOperational` (`intent.go:254`) | Exactly-once survives a crash but not a restore, silently (F37) |
| Add `data_subject_id` migration | pattern at `migrate_consts_v107_gdpr_cascade.go:1-45` | User text becomes unerasable |
| Add the `DELETE` block | `internal/api/admin_gdpr.go:276` (`:404,424,438,525`) | Erasure request misses the table (F38) |
| Add the mirrored `SELECT` | `admin_gdpr.go:637` (`:704,740`) + `gdprActionScope` | Access request returns an incomplete answer (F38) |
| Add metrics | `internal/server/metrics_domain.go:157` (`collectDomainMetrics` fan-out) | The SLO is unmeasurable (F39) |
| Injection-scan replayed content | `internal/lookout` — currently request-scoped only | Stored text is re-fed to a later agent outside any guard (F40) |
| Seed the feature flag | migration, plus one canonical key constant | `IsEnabled` silently returns false everywhere (F49) |

Two further rules:

- **Decision records need integrity, not just convention.** Rev 3 dropped the `decision_receipts` table (§9.8): the decision columns already exist on `approvals_queue` and `pipeline_waitpoints`, and the journal entry each decision already emits is hash-chained (`internal/journal/verify.go:20-41`). That chain, not a new table, is the tamper evidence (F42). The one addition is `routine_version` on both tables so "which version was approved" is answerable; nothing else is append-only by convention.
- **Sweepers are a solved shape here.** Copy `harbormaster.StartTimeoutSweeper` (`internal/harbormaster/gate.go:238`) or `internal/ephemeral/expiry.go`; do not add a third scheduler (F48). And reconcile the two expiry clocks: an ephemeral agent expiring mid-session must close or error its session, not leave it rendered `active` (F41).

---

## 17. Work packages — two tracks, because 1.0 does not mean this

**Read this before planning anything.** A release-scope audit established what 1.0 means in this repository, and it is not what rev 1 assumed.

`docs/prd/PRD-RELEASE-1-0-QUALITY-AUDIT.md` sets the bar as eight conditions about *proof*, not features — among them: "Every documented API route matches the registered route, HTTP method, parameters, authorization requirements, response shape, and error behavior"; "Critical security, credential, persistence, backup, restore, orchestration, and migration paths have behavior-level tests"; "Documentation distinguishes stable, early, experimental, deprecated, and roadmap behavior consistently". Its non-goals explicitly exclude "Expanding the product surface during this audit unless a missing behavior is required to make an existing documented workflow functional." The GitHub 1.0 milestone delegates its own definition to that document and carries four open issues (#2183, #1785, #1783, #1781). The work order's standard is quoted directly: *"An honest 70% is worth more than a confident 100% that is wrong."*

So the split is:

- **Track A — the 1.0 truth cut.** Defects where the system's behaviour contradicts its own label, documentation or data model. Every item here qualifies under the existing 1.0 bar; none of it is new product surface.
- **Track B — the 1.1 architecture.** Sessions, deliveries, checkpoints, the outcome contract, the attention contract. Real work, real value, **not** 1.0 — and the honest way to ship 1.0 is to label these limits rather than hide them.

If 1.0 is genuinely meant to include Track B, that is a decision to *redefine 1.0*, and it should be taken explicitly by the owner and written into the quality-audit PRD — not smuggled in through this document.

### Track A — the 1.0 truth cut

**A0 · Truth audit** (§7). Docs only. Now also re-measures the release-readiness numbers, since `RELEASE-1-0-READINESS-2026-08-10.md` is 283 commits stale and disclaims its own currency.

**A1 · Stop actually stops (Tier 1), and terminal states hold.** *(merged #2295)*
Cooperative cancellation per §10.3 Tier 1: `cancel_requested_at`, checked before any exec starts and again when the run reports back, and terminal-state guards on `mission_tasks` and `assignments`. **As built there is no mid-execution poll** — a run already inside its exec finishes that exec and is then recorded `CANCELLED`, and the mission engine schedules nothing further (proven for a live RUNNING run, `mission_tasks_stop_midflight_test.go`). That matches the promise exactly; do not describe it as more. `assignments` becomes reachable in `CANCELLED` (F9). The old stop route keeps its path and gains real behaviour. A late *failure* report on a stopped run now also reads as cancelled on every user-facing surface (broadcast, mission comment, activity), not only in `status`. UI label: "Stopping — will finish the current step".
*Status:* merged (#2295). Live validation on dev1 (2026-09-03, `docs/prd/reports/track-a-live-validation-2026-09-03.md`) then broke the accept line twice — a `QUEUED` run was not stamped and ran to `COMPLETED` after the issue read `CANCELLED` (#2312, fixed #2317), and Stop refused a `BACKLOG` issue whose only live run a mention had started (#2315, fixed #2320). Both re-checked live after the fixes.
*Why 1.0:* a control that is documented and does nothing is the definition of the bar's condition #2. Fixes F6 (worst half) and F7 entirely.
*Accept:* a stopped run starts no further step; a late callback changes nothing (regression test must fail on current `main`); the UI label matches the guarantee; no `docker kill` on a shared crew container anywhere in the diff.

> **Merge reconciliation, A1 ↔ A2 — done.** A2 is merged into `a1`. Stop now matches `mission_id` directly **and keeps the `chat_id OR group_id` fallback**, because one path still produces a live run with a NULL `mission_id`: a sub-agent delegating further via `/assign` from inside a mission (the sidecar and the routine dispatcher never send it). The server now derives `mission_id` from `chat_id` on that path (`EXISTS (SELECT 1 FROM missions WHERE id = chat_id)` — `ensureMissionChat` uses the mission id as the chat's primary key, and existence rather than `chats.mode` is the predicate because a hard-deleted mission leaves an orphaned MISSION-mode chat behind). Tests: `TestIssue_Stop_ReachesMentionDispatchedRun`, `TestIssue_Stop_FallsBackForDelegatedRunWithNoMissionID`, `TestAssignmentCreate_DerivesMissionIDFromChatID`.

**A2 · Every run is attributable to its issue.** *(merged #2279)*
`assignments.mission_id` + backfill + index. Nothing else from §9.4.
*Why 1.0:* this is issue #2256 ("nothing records what caused what") and it is a data-model defect, not a feature. Small.
*Accept:* one query returns every run for an issue; migration test covers a populated DB; `scripts/lint-migrations` clean; `internal/backup/intent.go` unchanged (no new table).
*Status:* built on `a2-runs-attributable-to-issues`, merged into `a1`. Found while building it: the existing `GET .../issues/{identifier}/runs` only joined through `mission_tasks`, so every mention-dispatched run was missing from its own issue — now fixed there.

**A3 · Triggers cannot be saved in a state where they can never fire.** *(merged #2271)*
Closed event registry generated from `internal/journal/types.go`, membership validation replacing the shape regex (`internal/api/automations.go:71`), payload-key validation, and the 7-day "matched nothing" acknowledgement promoted from the opt-in preview endpoint into the create flow.
*Why 1.0:* condition #2 again — the API accepts input it cannot honour, and the code comment admits it.
*Accept:* an unregistered `event_type` is rejected naming the valid ones; a nonexistent payload key is rejected; a rule matching nothing in 7 days requires acknowledgement.
*Status:* on `a3-closed-event-registry`. The registry is generated from `types.go` by an AST scan with a drift test (130 entry types, not the 117 the old comment claimed). **Limit, stated honestly in the error and the docs:** no payload-schema registry exists anywhere; nine event types are hand-verified, and a type outside that map gets no payload-key validation. The 7-day acknowledgement is not yet in the create flow.

**A4 · Trigger failure is visible for all three trigger kinds.** *(merged #2282)*
Webhook fire failures and automation enqueue failures emit a journal entry and, on repetition, an inbox item — matching schedules (`internal/pipeline/schedules.go:1058-1069`). Fixes the bare `logger.Error` at `internal/automation/registry.go:734-736`.
*Why 1.0:* condition #5, orchestration paths with behaviour-level evidence.
*Accept:* each of the three kinds produces a durable, queryable record of "should have run, didn't".
*Status:* on `a4-trigger-failure-visible`. Journal entry on every failure; one inbox card at three consecutive failures (lower than schedules' five, because here no run exists at all); a run that started and then failed is deliberately not counted — it already has `EntryPipelineRunFailed`.

**A5 · The docs stop contradicting the code.** *(merged #2289)*
Rollback (`docs/cli/routine.mdx:1097` says a new version is created; `internal/pipeline/versions.go:230-249` only repoints head); concurrency ("queue" vs the actual 429, `docs/guides/routines.mdx:2047`); the waitpoint empty-body asymmetry (`pipeline_waitpoint_callback.go:44-47` defaults true, the authed route false — pick one and document it); and the monthly-budget naming (F25 — either rename it to something that does not read as enforcement, or state on the page that it is reporting-only).
*Why 1.0:* conditions #2, #4 and #7, verbatim.
*Accept:* each of the four has a doc change, a test, or both; `docs-inventory -strict` clean.
*Status:* on `a5-docs-match-code`. No behaviour changed: the waitpoint asymmetry is documented on both handlers with its security reasoning (authed fails closed; the public token callback fails open for `trigger.dev wait.forToken` parity) rather than unified; the budget field keeps its name and gains reporting-only statements in the API reference, the CLI help and every budget command's output.

**A6 · Shipped surfaces tell the truth about what they show.** *(merged #2291)*
`RunsView` subscribes to `pipeline.run.*` as well as `run.*` (F33); the three server-emitted issue events dropped by the allowlist are registered (#2125), with a test that fails when an emitted type is unregistered; schedule health — `disabled_reason`, `consecutive_failures`, `last_missed_count`, wake stats — is *displayed* read-only (the editor is Track B).
*Why 1.0:* a view whose header claims workspace-wide coverage and silently omits a trigger class fails condition #2; a schedule disabled with no visible reason fails #7.
*Accept:* the allowlist test catches a deliberately omitted registration; an auto-disabled schedule shows its reason; and the runs view's header states what it can and cannot show. **Not** "every trigger kind appears in the runs list" — F33 rev 3 shows that is read-side work in a separate package, and an accept criterion the package cannot satisfy is how a truth defect gets re-labelled as done.
*Status:* on `a6-surfaces-tell-truth`. Frontend only; full Vitest (573 files) and a static-export build pass — the build needs `pnpm db:generate` first in any fresh clone or worktree, or it fails on a `TS2307` that looks like a code defect and is not.

**A7 · Inbox read state is per user.** *(merged #2296)*
`inbox_item_reads` (§9.7) with read state computed as a LEFT JOIN; existing columns retained.
*Why 1.0:* on a multi-user workspace the current behaviour is a correctness bug (F27), not a missing feature — one person reading clears it for everyone.
*Accept:* two users have independent read state; the existing columns still answer "someone dealt with it"; the table passes §16.1 in full.
*Status:* on `a7-per-user-inbox-read`. `IntentInclude` for backup, deliberately unlike `notification_deliveries`. Found while building it: `inbox.Upsert` resurrecting an item to unread did not clear per-user markers — fixed, since it is the same class of bug.

**A9 · Close the exclusivity gap the codebase already knows about** (rev 3 — confirmed from the exec wrapper, F51; merged #2269).
`tryMarkRunStart` (`internal/chatbridge/steer.go:60-77`) guards the chat door against two execs racing into one agent container. `runAssignment` — `/assign` and every @mention — did not consult it. Reading the exec wrapper settled the premise: the session name is `agent-<slug>` and the wrapper opens with `tmux kill-session`, so the second run kills the first (§2.2, F51 rev-3 addendum). Not a suspicion any more.
*Why 1.0:* a known-corruption path with one unguarded door; condition #5 covers it.
*As built:* the primitive is extracted into `chatbridge` (renamed `AgentRunLock` to avoid a grep collision with the pipeline integrations gate's `TestRunGate_*`/`ErrTestRunGateFailed`), keyed by agent id, shared by `HandleChatMessage` and `runAssignment`. A run that loses the lock is **requeued** (`QUEUED`, back of the crew FIFO), not failed — the completion pump already drains it. The chat door now also bounces when the agent is busy on an issue; that is correct (they collide) and is a visible behaviour change to call out in the changelog.
*Accept:* two concurrent runs for the same agent cannot both be live; the loser lands `QUEUED` and is drained once the lock frees; the pump cannot force a live collision; tests run under `-race` with deterministic pre-held locks. Full `go test ./...` green on `c874b5463`. **Coverage as merged (#2269):** the lock guards 5 of the 7 producers — `/assign`, @mention, mission task, lead planning and the chat door; the routine webhook route, the direct agent-run route and the peer query are not yet behind it and are written into the user docs as a 1.0 known limit (B0 docs).

**A10 · Owner and delegate are separate columns** (§9.10; ported from rev 1; added rev 3; merged #2297).
Two nullable FK columns on `missions`, backfilled from the polymorphic assignee; delegation never touches the owner; Start requires a typed executable delegate; DTOs expose both.
*Why 1.0:* rev-1 dev1 observation 11 — the UI showed the agent as owner — is a truth defect in a shipped surface (condition #2), and F62 (Start does not check executability) is a correctness gap the typed FK closes for free.
*Accept:* scenario 9 green (owner unchanged after delegation); Start refuses a non-agent delegate with a named error; the legacy assignee projection still reads correctly for old clients; §16.1 applied (the columns hold ids, not user text — no `data_subject_id`, but confirm with the GDPR export path).

**A8 · The golden-scenario harness exists from the start.** *(scenarios 11–13 merged #2293; 5a in #2295)*
The runner, plus the scenarios Track A can actually prove: **5a** (cooperative stop), **11** (a schedule fires on time; a wake gate returning false suppresses and says so), **12** (catch-up honoured across three missed fire times, all three policies), **13** (duplicate webhook with the same idempotency key → one run), and a new **A-scoped** one: an automation rule that can never fire is rejected at save time.
Note what this buys: 11, 12 and 13 exercise engine behaviour that is well built and, per §2.1, **has never once run on a real clone**. Proving them is exactly what 1.0 asks for. Moved here from rev 1's final phase, which contradicted §25.
*Accept:* the harness runs in CI; each included scenario fails on the pre-A baseline (for 11–13, "fails" means the behaviour is unproven, so the first green run is itself the deliverable).

**Explicitly deferred out of Track A, and labelled in the 1.0 docs as known limits:** a mention while an agent is busy is queued behind the live run and only delivered when it ends — it never reaches the running turn (F3; A9 turned the old second-run collision into a queue, and a chat message to an agent busy on an issue now bounces `agent_busy`); routine runs do not appear in the Runs view (F33 rev 3); the 7-day "matched nothing" acknowledgement is not yet in the automation create flow (A3); there is no cross-run continuity (F13, F15); Stop is cooperative, not immediate (§10.3 Tier 2); `DONE`/`COMPLETED` remain two lifecycles in one column (F11, §3.1). Writing these down is condition #7 of the 1.0 bar. Hiding them is what fails it.

### Track B — the 1.1 architecture

Ordered; each is one claimed issue and one PR.

**Precondition, added in rev 3: re-audit before starting.** Track B was designed against `main` at `3fa36df5`. Track A changes the substrate it builds on — `RunGate` now exists where §9.4 assumed no exclusivity primitive did; `cancel_requested_at` exists where §10.3 assumed nothing did; `assignments.mission_id` exists and is derived server-side. The delivery and session designs below are sound in shape but were not written with those in place. Do not open B1 until A has merged and someone has re-read §9 and §10 against the merged code. Building B on rev-3 assumptions after A lands would repeat the exact mistake this document exists to prevent.

**B1 · Event log and session foundations.** Widen `mission_activity` per §9.1 — `workspace_id` (backfilled), `seq` with `UNIQUE(mission_id, seq)`, `payload_json`, `source_kind`/`source_id`, a CHECK on `action`, and the two bypassing writers moved onto the emitter; `issue_agent_sessions` including `agent_version` (§11.6); `assignments.session_id`. §16.1 applies to every table.
*Accept:* a mention reuses an existing session rather than creating a second; `seq` is monotonic under concurrent writes; backup/GDPR/metrics/flag steps all done.

**B2 · Delivery and the wake loop.** Generalise `mission_comment_mentions` per §9.3 — nullable `comment_id`, `event_id`, a separate `state` column, `UNIQUE(event_id, agent_id)`, and the consistency triggers dropped and recreated with a `NEW.comment_id IS NOT NULL` guard; claim/consume CAS in the `MarkFired` shape (F57); the ack event before any model call. **Includes the backup-classification decision of F37, stated in the PR body.**
*Accept:* ten concurrent identical deliveries produce one run; the ack reaches the client without a refresh; a restart between event and consumption loses nothing; a restore-from-backup case is documented either way.

**B3 · One active turn per session.** The partial unique index — **and the insert-path rewrite it depends on**: `cappedAssignment` and `insertCappedAssignment` (`internal/api/delegation_limits.go:509-577`) gain `session_id`, with session resolve-or-create inside the same transaction as the fan-out guard (§3.1). Follow-ups become queued deliveries consumed at the next step boundary via the existing steering queue.
*Accept:* two comments 2s apart produce one run and two consumed deliveries; the index rejects a concurrent second insert at the DB level; no TOCTOU window between session lookup and insert.

**B4 · Leases.** `lease_owner`/`lease_expires_at` with heartbeat, recovery keyed on lease expiry not process start (F8), sweeper copied from `harbormaster.StartTimeoutSweeper` (F48), reconciled with ephemeral expiry (F41).
*Accept:* a killed process's runs recover after lease expiry; an ephemeral agent expiring mid-session closes or errors that session.

**B5 · Checkpoints and the context pack.** `agent_session_checkpoints`; §11.1 assembly; the sidecar comment-read verb; compaction path recorded per run; lookout scanning for replayed content (F40).
*Accept:* the §11.4 metrics table, all four rows; pack size does not grow with thread length; an agent woken after 7 days does not redo completed work.

**B6 · Outcome contract.** `outcome` on both run tables, the §9.6 routing table implemented once.
*Accept:* `NO_CHANGE` creates no inbox item; `NEEDS_HUMAN` creates exactly one with a valid action contract; a run ending without an outcome is `FAILED` with the stated reason.

**B7 · Hard termination (Tier 2).** Persist `ExecResult.ExecID`, discover the PID via `ExecInspect`, signal that process from inside the container. Never `docker kill` on a crew-shared container.
*Accept:* a stop terminates the target process within 5s; sibling agents on the same crew are unaffected — proven by a test that runs two agents in one crew and stops one.

**B8 · Atomic routine authoring.** The transaction lives in the API save handler, not the sidecar (which has no DB access and already makes two sequential internal HTTP calls, `internal/sidecar/pipelines.go:105-195`); schedule validation from `pipeline_schedules.go:190` must become transaction-composable. Realistically 3–5 files plus a migration.
*Accept:* routine+version+trigger commit together or not at all (rollback test); the agent's final message names the first fire time; draft activation raises one approval item with a receipt pinning the version.

**B9 · The reliability editor.** The full §13.2 table, plus the next-five-fire-times preview across a DST boundary, plus webhook update (F21).
*Accept:* every backend field is settable; DST test passes for `Europe/Prague` in March and October; a webhook can be edited without changing its URL.

**B10 · Attention contract.** Server `thread_key`/`attention_class`/`actions[]`; `routine_version` on `approvals_queue` and `pipeline_waitpoints` (§9.8, in place of the dropped receipts table); the digest scheduler; server-side merge replacing the client merge (F28).
*Accept:* one card across five days of the same recurring condition; every decision writes a receipt naming the version; `/inbox-v2` makes one request.

**B11 · The board, and the parent/child rule.** The remaining new event types with allowlist tests and **a client gap-detection and resync rule** (F43 — the hub drops frames silently under load, so registration is not delivery); §10.4's terminal-children rule with `?force=true` and a receipt.
*Accept:* the board moves without refresh for create, status change, comment, session state and outcome (#2257); a forced gap in the frame stream is detected and resynced via `GET .../events?after_seq=`.

**B12 · Instrumentation.** The §19.3 metrics as new collectors in `internal/server/metrics_domain.go:157`, **including the percentile capability that does not exist today** (F39).
*Accept:* each SLO has a real series; percentile computation has its own tests; no metric claims a number it cannot compute.

**B13 · The `DONE`/`COMPLETED` decision.** Not a refactor ticket — a written decision about two lifecycles sharing one column (§3.1), then whatever migration follows from it, scoped to `missions` only and never touching `assignments.status`/`pipeline_runs.status`.
*Accept:* the decision is documented with its reasoning; the mission-engine path still transitions correctly; a test pins that run-status CASes are untouched.

## 18. Golden end-to-end scenarios

These are the acceptance suite. Each must fail on current `main` before it passes — **except 11, 12 and 13**, which test engine behaviour that was correct all along and simply never exercised; there, the first green run is the proof (A8), and they pass on `main`.

1. Mention an idle agent → visible acknowledgement < 1s, exactly one session, exactly one run.
2. Ten duplicate deliveries of the same event → one run.
3. Follow-up comment during an active run → no second run, message consumed exactly once.
4. Correction during an active run → reflected in the next step, not the next unrelated run.
5a. **(Track A)** STOP during a run → no further step is started, run ends `CANCELLED`, a late callback changes nothing.
5b. **(Track B7)** STOP during a container exec → the target process is terminated within 5s and a sibling agent on the same crew keeps running.
6. Server restart between event and consumption → nothing lost, no duplicate run.
7. Agent resumes an issue after 7 simulated days → no repeated completed work, and the assembled pack is the same size as on a 5-comment issue (bounded, not reduced — §11.4).
8. Parent issue with an open child → cannot be marked DONE without force; force writes a receipt.
9. Human owner stays owner after delegating to an agent.
10. A peer agent's "GO" cannot satisfy a waitpoint.
11. Scheduled routine fires on time; a wake gate returning false suppresses the run and says so.
12. Downtime spanning three fire times → `catchup_policy` honoured exactly (all three variants tested).
13. Duplicate webhook with the same idempotency key → one run, second returns the first run id.
14. Routine authored in chat → routine + trigger exist together, first fire time stated; rollback test proves atomicity.
15. `NEEDS_HUMAN` → exactly one inbox item with a valid action contract; acting on it resumes the run, writes a receipt, and updates the same thread rather than creating a new card.

---

## 19. Test matrix, metrics, and the commands CI runs

### 19.1 Layers

| Layer | What it must cover | Where |
|---|---|---|
| Go unit | Delivery CAS, session state machine, outcome routing, event-registry validation, terminal-state guards, catch-up arithmetic, DST fire-time computation | table-driven `*_test.go`, `testutil.MigratedSQLDB` |
| **Persistence, not mocks** | Any test of a scheduled or webhook-triggered run must wire a `RunStore` (`Executor.WithRunStore`, `PipelineHandler.SetRunStore`) and assert a `pipeline_runs` row exists. **Every pre-existing schedule and webhook test omits this**, so none could catch a persistence regression — found by `t1`, whose tests failed with zero rows until the store was wired. Needs its own issue. | `internal/pipeline`, `internal/api` |
| Go concurrency | I1 and I2 under `t.Parallel` + `-race`; ten concurrent claims; two concurrent finishers | `internal/api`, `internal/pipeline` |
| Restart | Kill between event and consumption; between claim and consume; mid-step; lease expiry recovery | pattern from `internal/server/running_recovery_boot_test.go` |
| Migration | Upgrade from a populated old DB; backfill correctness; immutability | `scripts/lint-migrations`, `migrate_upgrade_path_oldest_test.go` |
| Security | Cross-workspace read/write refusal on every new route; RBAC per route; peer-vs-human | existing route contract tests |
| Frontend unit | Allowlist registration test; schedule form ↔ backend field parity test | Vitest |
| E2E | Scenarios 1, 3, 5, 11, 15 in a browser — **new Playwright specs**, none exist for inbox or mentions (F34) | `e2e/` |

### 19.2 Commands (from CI, not from memory)

```bash
go test ./... -count=1 && go vet ./...
go test -race -timeout 40m ./internal/api/        # ~23 min; the default 10m timeout is a false failure
go run ./scripts/lint-migrations                  # if migrations changed
golangci-lint run --timeout=5m ./...              # Go Lint gate 1
go run ./scripts/lint-tsformat origin/main        # gate 2
go run ./scripts/docker-api-surface               # gate 3
# gate 4: OpenAPI spec regenerated and committed
go run ./scripts/docs-inventory -strict           # gate 5 — every new endpoint AND CLI command documented
pnpm lint && pnpm build && pnpm test
pnpm test:e2e
```

Two traps worth stating: "Go Lint" is five gates and the annotation names none of them; and a green `gh pr checks` on a short list means CI did not run, not that it passed.

### 19.3 Service levels — targets, not measured

| Metric | Target | Measured |
|---|---|---|
| Comment persisted → acknowledgement visible | p95 < 500 ms | server timestamp → WS emit |
| Session visible as `pending`/`active` | p95 < 1 s | event → session state change |
| First agent acknowledgement (capacity available) | p95 < 10 s | delivery → run claim |
| Lost deliveries | **0** | `pending` older than 5 min, alarmed |
| Duplicate runs per event | **0** | count runs per `event_id` |
| Scheduled fire punctuality | p95 < 60 s of due time | note the 30 s poll floor (F24) |
| Wake size is bounded, not growing | assembled pack size on a 200-comment issue vs a 5-comment one | equal within tolerance (§11.4) |
| Repeat work after a wake | steps duplicating a checkpointed done step | 0 |
| Inbox items per successful run | **0** | outcome routing |
| Checkpoint compliance | >95% of session runs | `Parsed` flag |

Do not claim any of these before they are instrumented. An uninstrumented SLO is a slogan.

**And instrumentation here is a net-new capability, not wiring (F39).** `/metrics` is hand-rolled Prometheus text computed from DB aggregates at scrape time (`internal/server/metrics_domain.go:94,157`); there is no Prometheus client in `go.mod`, no histograms, and SQLite has no `percentile_cont`. Every p95 above must be built. That is B12, and until it lands the correct statement is "not measured", not "meets target".

Two further honesty notes:

- **Session state is not process state (F47).** `admission.Controller.Admit` (`internal/admission/admission.go:302`) gates container start on host capacity, *after* the run-claim CAS. "Session active within 1s" is a DB fact; under host pressure the process may still be queued. Either report both, or name the metric so it cannot be misread.
- **A published precedent for the ack targets.** Linear's agent contract requires a first activity within **10 seconds** or the agent is shown unresponsive, and treats a session with no activity for **30 minutes** as stale but recoverable (§26). Our 10s first-acknowledgement target and 14-day session staleness are in the same family; the 30-minute figure is a reasonable model for `active → idle`, which §10.1 currently leaves unspecified.

---

## 20. Rollout

Use the existing two-tier flag system (`internal/featureflags/featureflags.go:19` — instance default, per-workspace override). Do not build a second gate (F35).

1. **Shadow.** Write events, deliveries and sessions; do not change dispatch behaviour. Compare: would the new path have produced the same runs? Run for a week on dev1 with real work.
2. **Dogfood.** Enable on dev1/dev2. Our own issues run through the new loop. This is the phase that produces the recurring-work evidence §2.1 shows we have never had.
3. **Canary.** `stage` (CD-owned; never deploy by hand). Watch the §19.3 metrics.
4. **Default on.** Only after the golden scenarios have been green for two weeks.

**Rollback is not safe once a migration has run (F53).** `guardVersionSkew` (`internal/database/migrate.go:288-311`) refuses to boot a binary older than the DB's applied migrations, and there is no down-migration. The recovery path is the `*.pre-migrate-*.bak` snapshot. Every phase above must therefore be treated as a one-way door at the moment its first migration applies — which is another reason Track A's migrations are small and additive, and why they land before anything that depends on them.

Backwards compatibility: old `assignments` rows keep NULL `mission_id`/`session_id` and still render; the old stop route keeps its path and gains real behaviour; `/inbox` stays until `/inbox-v2` reaches parity, then swaps.

---

## 21. Risks

| Risk | Mitigation |
|---|---|
| The scope is large enough to become one unreviewable PR | One work package per claimed issue and PR. A PR touching more than one is rejected in review. The one exception is `a1`, which deliberately carries A2 because they touch the same rows. |
| Journal instability undermines run truth (F36) | A0 gates on journal health; nothing durable lives only in `journal_entries` (I8). |
| The realtime allowlist silently swallows the new board (F32) | A6 fixes the existing three and adds a test that fails on unregistered emissions; B11 adds the rest plus client gap-detection, because the hub also drops frames silently under load (F43). |
| Static export limits the live UI (F31) | Everything realtime rides the existing WS provider; no server components are introduced. |
| "One turn per session" makes agents feel slower | It is correct, not slower: the alternative was the second run killing the first (F51). Surface the queued follow-up in the UI so waiting is visible. |
| Outcome becomes a third confusing status field | One PR documents `status` vs `outcome` vs `runverdict` in `docs/guides/routines.mdx`, or B6 is not done. |
| Cost truth stays broken (F12, F25) | Explicitly out of scope (N5), named here so nobody claims budgets work. |
| Estimates drift because nothing was measured first | Rev 2 replaced the −60% token target with the bounded-context metrics of §11.4; A0 still gates on measuring the baseline. |
| **This PRD gets treated as 1.0 scope and delays the release** | §17's two tracks exist for exactly this. Track B is 1.1. Redefining 1.0 is an owner decision, taken explicitly or not at all. |
| Track A ships and the deferred limits are quietly forgotten | The deferred list at the end of Track A is a documentation deliverable under 1.0 condition #7, not a footnote. |
| Someone wires a provider's native session resume as an "optimization" | F45: all six adapters are stateless today and OpenCode's `--continue`/`--session` sit unused one line from `BuildCommand`. A second, invisible continuity channel would silently diverge from checkpoints. State the prohibition in the adapter package doc. |
| Hard kill is attempted with `docker kill` | It would SIGKILL every sibling agent on the crew (`crew_resource_drift.go:49`). B7's accept criterion tests exactly this. |

---

## 22. Relationship to existing issues and documents

| Item | Relationship |
|---|---|
| #2257 board never moves | A6 (the three dropped events) then B11 (the board). |
| #2256 delegation e2e, nothing records what caused what | A2 gives it `mission_id` — the cheap half, 1.0-eligible. B1/B2 complete it with events and deliveries. |
| #2125 43 realtime events dropped | A6 covers the three issue events; the rest is prerequisite for B11. |
| #2233 `approvals_queue` retention/erasure | Adjacent; §16 must not repeat the omission. |
| #2234 gate auto-tuning fingerprints the prompt | Adjacent; unaffected. |
| #2144 read-scope invariant vs authedMut | Affects every new GET route; check before adding. |
| `docs/prd/PRD-AGENT-FIRST-ISSUE-COORDINATION-2026.md` | **Superseded and, after rev 3, deleted.** Committed once alongside this document so it is in history (`633af3125`); its four unabsorbed sections were ported first (§2.9, §9.10, §10.5, A0 step 10) and its five wrong claims are corrected in §3. |
| `docs/prd/inbox-maximum-wireframe.md` | B10 implements its unbuilt "no-loss technical contract" section; A7 takes only the per-user read-state correctness fix. |
| `docs/prd/response-shape-contract.md` | Binding on every new response type (§7 step 9). |
| `docs/prd/agent-memory-on-wake.md`, `memory-retrieval-layer.md` | Reconcile, do not absorb: §11 consumes memory, it does not redesign it. |
| `docs/guides/routines.mdx`, `docs/cli/routine.mdx` | Two documented behaviours are wrong (F26) — fixed in A5. |
| `docs/prd/PRD-RELEASE-1-0-QUALITY-AUDIT.md` | **Defines 1.0.** Track A is scoped to its eight conditions; Track B is out of scope for 1.0 by its own non-goals. |
| `docs/prd/RELEASE-1-0-READINESS-2026-08-10.md` | 283 commits stale and self-disclaiming. A0 re-measures before any number from it is cited. |
| `docs/prd/CODEX-WORK-ORDER-RELEASE-1-0.md` | Its "blocking" tier (#1781, #1783, #1785, plus the tracker's #2183) is the real 1.0 critical path. Track A must not displace it. |
| `RELEASING.md` | Still documents `v0.1.0-beta.1` while `package.json` says `1.0.0-rc.1`. Not this PRD's defect; worth an issue. |

## 23. Decision log

| # | Decision | Why |
|---|---|---|
| D1 | Delivery is its own record, not a column on comments | Comments are content; delivery is transport. Conflating them is why "did the agent see it" is unanswerable today (F4). Rev 3 keeps the separation and puts the record in the generalised mentions table (D16). |
| D2 | One active turn per session, enforced by a partial unique index | Application-level politeness has already failed here (F2). The database is the only reliable place for I2. |
| D3 | Outcome is separate from status and from `runverdict` | Status is technical, verdict is an LLM's opinion, outcome is a routing decision that must be deterministic (F22). |
| D4 | Checkpoints are a table, not journal entries | The journal is compacted at 30 days and has been unstable this month (F36). |
| D5 | `agent_session_checkpoints`, not `checkpoints` | The name is taken twice already (§3). |
| D6 | **Resolved in rev 3: widen `mission_activity`.** | Two activity logs is worse than one imperfect one. The merge is not free — the table has no `workspace_id`, no CHECK on `action`, records no comment events today, and two writers bypass its emitter (§9.1) — but a parallel table costs all of that plus a second truth. |
| D7 | Reuse `internal/featureflags` | A second gate system is how a rollout becomes unrollbackable (F35). |
| D8 | Mid-turn interruption stays a non-goal | The runtime queues to the next turn (F3); promising live insertion would be a truth-vs-label failure (I4). |
| D9 | Readiness is stated per verified loop, not as a single percentage | §24. |
| D10 | Cancellation ships in two tiers, labelled | There is no kill primitive and the container is crew-shared (§3.1). A cooperative stop that is honestly labelled beats a hard stop that is claimed and absent. |
| D11 | The context goal is a bound, not a reduction | On this path there is no history to remove (§3.1); the win is that a 200-comment issue wakes the same size as a 5-comment one. |
| D12 | Interruption is an event with a priority, not a separate RPC | Matches the field contract (§26) and keeps one ordered history, so a later tightening needs no schema change. |
| D13 | Sessions pin the agent version | `agent_config_history` already exists and nothing reads it (§11.6); without this, "why did it behave differently" is unanswerable. |
| D14 | Provider-native session resume stays unused | All six adapters are stateless today (F45); a second continuity channel would diverge from checkpoints invisibly. |
| D15 | Track A ships for 1.0; Track B is 1.1 | The project's own 1.0 bar is about proof, not surface (§17). Redefining that is an owner decision. |
| D16 | Deliveries generalise `mission_comment_mentions`; receipts become two columns | Six new tables became three (§9). The guarantees are identical; the surface to reason about is halved. The one hard cost is dropping and recreating a consistency trigger, which SQLite forces (§9.3). |
| D17 | A9 was gated on investigation until F51 was confirmed from code | It rested on a code comment until the exec wrapper was read: `TmuxSessionName` is per-slug and the wrapper opens with `kill-session`. Confirmed. The §27 experiment is now recommended for *observation* of the failure from the user's side, not required for the decision. |
| D18 | Owner/delegate schema is Track A, not B | Additive, nullable, backfilled — A2's shape — and it is the only thing that can enforce I5 and make scenario 9 testable (§9.10). |
| D19 | The A9 loser is requeued, not failed | `FAILED` would be a lie in run history; `QUEUED` plus the existing completion pump is the machinery the codebase already has. Deferral *into the live turn* stays B2. |

## 24. Readiness — stated honestly, and split by release

Percentages over surface area were the wrong unit and rev 1 used them anyway. State readiness per loop, with a test that decides it.

| Loop | Ready when | Today | Track |
|---|---|---|---|
| Execution is attributable | every run carries `mission_id` | On `a1`, unmerged (F1) | A2 |
| Stop does what its label says | scenario 5a green | On `a1`, unmerged; 5a proven for PENDING and RUNNING | A1 |
| A trigger cannot be saved unable to fire | the A-scoped scenario in A8 | On `a3`, unmerged (F19); registry covers every declared/used entry type module-wide (140), payload-key checks cover only a curated subset, and PATCH validates only the fields it changes | A3 |
| A trigger that fails is visible | A4 accept | On `a4`, unmerged (F20) | A4 |
| Docs match code | A5 accept | On `a5`, unmerged (F25, F26) | A5 |
| Shipped surfaces show what they claim | A6 accept | On `a6`/`a7`, unmerged; F33 read-side still open | A6, A7 |
| Mention → wake → visible reply | scenarios 1–4 green | **Not implemented** (F2, F3, F4) | B1–B3 |
| Continuity across days | scenario 7 green | **Not implemented** (F13, F15) | B5 |
| Stop is immediate | scenario 5b green | **Not implemented** (§3.1) | B7 |
| Recurring work fires and reports | scenarios 11–13 green (A8), then 14 | **11–13 proven on `t1`** — first evidence ever; 14 is B8 | A3/A4/A8, then B8/B9 |
| Human attention is scarce and provable | scenario 15 green | Partial (F28, F29, F30) | B10 |

### 24.1 Scenario proof status — measured, not assumed (rev 3)

A coverage audit of the eight Track A branches against the 15 scenarios of §18 found, at the time of the audit: **none of the fifteen fully proven.** Since then: **5a** is proven for both a pending and a live RUNNING run (`a1`); **11, 12, 13** and the circuit breaker are proven on `t1` — the first time the scheduler, catch-up and webhook idempotency have been shown to work at all. Four and a half of fifteen. The remainder need Track B (1–4, 6, 7, 15), a rule that does not exist yet (8, B11), or A10 (9).

That is not a failure of the branches. Each built real behavioural coverage **for its own work package** — registry validation, trigger-failure visibility, per-user read state, the per-agent run gate. But a work package is not a scenario: the packages are bounded fixes, the scenarios are cross-cutting behaviour, and §24's table has always listed them separately. The error would be to let package coverage be *read* as scenario coverage.

Two specific traps the audit caught, worth repeating because they are the general shape of the mistake:

- A test asserting that a UI renders the string value of `catchup_policy` is not coverage of scenario 12. It proves a column reached a label; it never touches fire-time arithmetic or the three policy variants.
- A test asserting that a component *subscribes* to `pipeline.run.*` is not proof that a received event repaints anything.

- **The A1 accept line was ticked by package tests and failed live, twice.** "A stopped run starts no further step" held for every row the tests seeded — `PENDING` and `RUNNING`. The first live run on dev1 (2026-09-03) parked a run as `QUEUED` behind a busy agent (#2269's own queue), stopped the issue, and watched the run start and land `COMPLETED` (#2312). The same session mentioned an agent on an issue nobody had started and found Stop refusing the issue by status while the run kept going (#2315). Neither is a subtle race; both are shapes the package tests never constructed. Package coverage is not scenario coverage — the scenario is *a user stops an issue and nothing attributed to it runs on*, and only a live board with a busy agent and a bare mention exercised it.

**Rule going forward:** a scenario counts as covered only when the test observes the behaviour a user would observe. Column-write tests and subscription-argument tests are legitimate unit coverage and are not scenario proof. State which kind each test is.

**What 1.0 requires from this document:** Track A complete, its scenarios green, and the deferred-limits list written into the user-facing docs as known limits. Nothing in Track B. If Track A slips, 1.0 is not blocked by it — the tracker's own 1.0 milestone (#2183, #1785, #1783, #1781) plus the security/hygiene tail is the critical path, and Track A must not displace it.

**What 1.1 requires:** Track B complete, all 15 scenarios green for two weeks, and §19.3 instrumented and inside target.

There is no "100%". For a system like this the measurable properties are delivery, continuation, duplication and human comprehension, and they are measured continuously rather than declared once.

## 25. Definition of done for this PRD

- A0 has been executed and this document updated with its findings, including re-measured release-readiness numbers.
- Every Track A package has a claimed issue with acceptance criteria copied from §17, and each is checked against the 1.0 quality bar it claims to satisfy.
- The Track A deferred-limits list is written into user-facing documentation as known limits (1.0 condition #7).
- Every §2 finding is fixed, assigned to a track, or explicitly accepted with a reason in §22.
- `docs/guides/routines.mdx`, `docs/cli/routine.mdx` and the API reference match the code (F26).
- The golden-scenario harness exists (A8) and every scenario it covers either fails on the pre-A baseline or — for 11–13 — is a first-ever proof of engine behaviour, stated as such.
- Track B is recorded as 1.1 scope, or an owner decision to redefine 1.0 is written into the quality-audit PRD.

---

## 26. Appendix — what the field does now, and what it changes here

Researched 2026-09-01. Included because three of this document's decisions were changed by it, and because "we invented our own semantics" is a bad answer at review time.

### 26.1 Sessions are the industry unit, and they are an append-only event log

Anthropic's Managed Agents make the session the primary object: an agent plus an environment, holding conversation history, sandbox state and outputs server-side, with the harness that calls the model deliberately separated from the sandbox where code runs, so the sandbox can die without losing the work. Sessions are created, then driven by **events** — `user.message`, `user.interrupt`, `user.tool_confirmation`, `user.tool_result` — sent to a running session, with server-assigned ids and all-or-nothing validation. A session can be **pinned to a specific agent version**, or given per-session overrides that never mutate the agent resource.

What this changes here: D12 (interruption becomes an event with a priority, not a side channel) and D13 (sessions pin `agent_version`, using the `agent_config_history` table that already exists and that nothing reads). It also validates §8's shape — event → delivery → session → run — as the mainstream one rather than a local invention.

### 26.2 Budgets are session ceilings with a named stop reason

Managed Agents attach a budget at session creation as a hard ceiling on list cost, expressed as a whole number of cents *as a string* so no float rounding is ever applied, enforced **between model requests**, with the session going idle under the stop reason `budget_reached`.

What this changes here: it is a direct model for fixing F25 later. Crewship's enforced gate (`DSL.MaxCostUSD`) is already the right shape — per-run, checked pre-step, aborting mid-run. The defect is that the thing *called* a budget (`MonthlyBudgetUSD`) is reporting-only. A5 fixes the label; a future session-level ceiling with a named stop reason is the target shape.

### 26.3 Human-in-the-loop is interrupt-and-resume over a checkpointer, and it is used sparingly

LangGraph's pattern is `interrupt()` to pause and persist, `Command(resume=...)` to continue from exactly that point, with a checkpointer as a hard prerequisite — the most common beginner failure is compiling without one. The published guidance is to interrupt only on irreversible, high-blast-radius actions, because every interrupt introduces unbounded latency.

What this confirms here: Crewship's waitpoints already implement this correctly, including idempotent resume returning 409 (`internal/pipeline/waitpoints.go:610-646`). The guidance argues *against* expanding approval gates and *for* §12's rule that success and no-change never create an item.

### 26.4 Exactly-once is the settled expectation, and the mechanism is journal-then-replay

Durable-execution runtimes converged during 2025–2026: automatic state persistence plus exactly-once semantics. Restate journals each step before execution and replays on recovery, skipping already-executed steps, which gives exactly-once *without* idempotency keys in application code; Temporal runs activities exactly once with configurable retries and reached GA integration with the OpenAI Agents SDK on 23 March 2026; DBOS persists execution state in the same Postgres or SQLite as the application data, in-process, with no new infrastructure.

What this confirms here: §9.3's claim/consume CAS plus `UNIQUE(event_id, agent_id)` is the conventional shape, and DBOS's design is the closest analogue to Crewship's — SQLite in-process, no new infrastructure — which is the right precedent to cite when someone proposes adding a queue broker. Crewship's per-step durable outputs and boot resume (`internal/pipeline/resume.go`) already sit in this family.

### 26.5 The agent-facing contract has published numbers

Linear's agent contract requires a first activity **within 10 seconds** of the `created` event or the agent is shown as unresponsive; follow-up activities may continue for **up to 30 minutes** before the session is considered stale, and staleness is recoverable by sending another activity. Activities are typed — `thought` on start, then `response`, `elicitation` or `error` — and agents are told to reconstruct conversation from **Agent Activities (frozen records)** rather than comments, because comments are editable and may have changed since the agent last ran.

What this changes here: §19.3 gains a real precedent for the acknowledgement targets and a model for the `active → idle` timeout §10.1 left unspecified. The typed-activity idea maps onto `mission_activity.action` (§9.1) and is worth adopting for the session strip in §15 — "thinking", "acting", "asking", "answered" is a better human signal than a spinner.

**One thing deliberately not adopted:** the "don't read history from comments" rule. In Crewship comments are append-only — `GET` and `POST` only (`internal/api/router_orchestration.go:106-107`), no `UPDATE mission_comments` anywhere (F50) — so the premise does not hold here today. The event log is still the right source of truth, for ordering and delivery reasons, but this document should not borrow a justification that is not true of our code.

### 26.6 Parallel agents need ownership, and shared state is still the hard part

Anthropic's work on parallel coding agents found that early models coordinated poorly, with pull requests frequently conflicting; later models improved by having each agent maintain **very high ownership of its own files**, and only the most recent model worked effectively on *shared* resources while sustaining throughput. In the C-compiler experiment the agents ran an explicit lock protocol — work, pull, merge, push, release — and merge conflicts were frequent.

What this adds here: a gap this document does not cover. §9.4's one-active-turn-per-session rule prevents two runs of the *same* session, but nothing prevents two different agents from being mentioned on two issues that touch the same files. If Crewship is going to run agents in parallel on one repository, an explicit ownership or lock protocol is required work, and it is not in either track. Record it as the next question after B, not as something this PRD solved.

### Sources

- [Scaling Managed Agents: Decoupling the brain from the hands](https://www.anthropic.com/engineering/managed-agents)
- [Claude Managed Agents — Start a session](https://platform.claude.com/docs/en/managed-agents/sessions)
- [Effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- [Linear — Agent interaction best practices](https://linear.app/developers/agent-best-practices)
- [LangChain — Human-in-the-loop](https://docs.langchain.com/oss/python/langchain/human-in-the-loop)
- [OpenAI — Guardrails and human review](https://developers.openai.com/api/docs/guides/agents/guardrails-approvals)
- [OpenAI — Orchestration and handoffs](https://developers.openai.com/api/docs/guides/agents/orchestration)
- [Durable execution for AI agents in 2026 (Temporal, Inngest, Restate, Prefect)](https://comuvia.ai/articles/durable-execution-for-ai-agents-temporal-vs-inngest-vs-restate-vs-prefect)
- [Inngest — Durable execution, the key to harnessing AI agents in production](https://www.inngest.com/blog/durable-execution-key-to-harnessing-ai-agents)
- [Anthropic — Building a C compiler with a team of parallel Claudes](https://www.anthropic.com/engineering/building-c-compiler)
- [Anthropic — Patterns and problems in multiagent systems](https://www.anthropic.com/research/multiagent-systems)

---

## 27. The empirical gap — what no audit can settle

Twelve audits and this document's entire evidence base are **static reading plus one database query**. Nothing has been run. Per §2.1 the recurring-work loops have never executed even once on a working clone, so "the engine is strong" is an inference from code quality, not an observation.

Four things only a running system can answer, ranked by what they would change:

1. **What does F51 look like from the outside?** It is decided (§2.2 rev-3 addendum; A9 is built). What no audit shows is the *user's* view of the old failure and of the new queue: post a mention, then a second one for the same agent while the first is live, on a build without A9 and on one with it. One pair of runs, on dev1, never stage. *Cost: real agent runs — tokens and a container.* Result goes into the A9 PR body and here.
2. **Does the mention path work end to end?** `mission_comment_mentions` has 0 rows and `mission_activity` has no `mentioned` entry, but both dispatcher doors are wired in production (`router_orchestration.go:796`, `router_internal.go:224`). So the path is almost certainly unused rather than broken — but "almost certainly" is what this document is trying to eliminate.
3. **Do the CAS state machines hold under real concurrency?** `_txlock=immediate` and a 30s busy timeout say writers wait rather than fail (F59), and `MarkFired` shows the correct error handling (F57). Ten concurrent claimants against a five-connection pool has not been measured.
4. **Does a long write transaction blow the sub-500ms acknowledgement target?** Writers serialize. Nobody has measured the tail.

Items 1 and 2 are one experiment, now for observation rather than decision, best run once the A9 PR is open. Items 3 and 4 belong with B12's instrumentation.
