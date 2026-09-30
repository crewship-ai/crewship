# PRD: Agent Continuity — durable context, interaction & the learning loop (2026)

| Field | Value |
|---|---|
| Owner | Pavel |
| Status | in progress — PR #1 (#630) open, awaiting review |
| Scope | internal design notes for long-running "persistent colleague" agents |

## 1. Context

Crewship's positioning is **persistent colleagues**, not one-shot tools — agents
that work across long sessions and many days. That promise leans on three
capabilities that today are either missing or only partially built:

1. **Durable context.** A conversation that outgrows the model's context window
   currently loses its oldest turns by *truncation* — the orchestrator drops
   them and stitches a `...(truncated)` marker onto the boundary message
   (`internal/orchestrator/orchestrator_run_conv.go`). Early decisions,
   established facts, and still-open threads silently disappear.

2. **Interaction model.** While an agent is mid-turn there is no supported way
   to steer or redirect it without killing the run (F4.2 is deferred in
   `PRD-AGENT-EVOLUTION-2026.md`). Working *with* a colleague means being able
   to course-correct without starting over.

3. **The learning loop.** Memory consolidation, episodic recall, and the
   memory→skill promotion path already exist. The loop is not yet closed:
   skill-usage telemetry has a consumer but no producer, on-disk memory files
   aren't injection-scanned at prompt-assembly time, and several reliability
   gaps remain on the agent-initiated write path.

This PRD captures the roadmap that closes these gaps. It is deliberately
structured so **each item is one shippable PR**, each with its own tests and
docs. Every claim below is grounded in the current code; file:line citations
are inline.

## 2. Goals

- Replace lossy conversation truncation with **compaction** (summarize the
  overflow instead of dropping it), with a deterministic fallback so behavior
  is unchanged when no auxiliary model is configured.
- Make compaction summaries **temporally anchored** and **structurally robust**
  (tool context intact, no cross-session bleed, cache-stable).
- Close the memory learning loop: real **skill-usage telemetry**, **load-time
  injection scanning** of on-disk memory, **off-thread** memory writes, and
  **in-turn consolidation guidance**.
- Lay the groundwork for **mid-turn steering**, **cross-session conversation
  search**, an **evolving user model**, and **model discovery / live switch**.

## 3. Non-goals

- Multi-platform chat delivery (Telegram/Discord/Slack/etc.) and voice — out of
  scope for a self-hosted dev-agent runtime; deliverable later on the existing
  hooks substrate (`internal/hooks`) if demand appears.
- Training-data trajectory generation/compression.
- A hosted multi-tenant offering.

## 4. Constraints

- **Self-host, no external services.** Summarization rides the existing
  auxiliary-model slot (`internal/consolidate` `SummarizerClient`, wired from
  Ollama / aux model in `server.New`); no new provider dependency.
- **Prompt-cache invariant.** Any compaction summary is a *mid-conversation*
  message, never part of the cached system-prompt prefix. The system-prompt
  prefix must stay byte-stable for the lifetime of a session so the provider's
  prompt cache keeps hitting.
- **SQLite + FTS5** for storage and search; no separate vector DB.
- **File-first markdown** for human-readable agent memory — every value the
  agent reads is something the operator can read and edit too.

## 5. Roadmap — one PR per item

| PR | Title | Effort | Status |
|---|---|---|---|
| **#1** | Conversation compaction (summarize overflow, deterministic fallback) | M | **PR #630 open** |
| #2 | Temporal anchoring in compaction summaries | S | planned |
| #3 | Compaction robustness (tool context, cross-session guard, cache stability) | M | planned |
| #4 | Memory load-time injection scan at prompt assembly | S-M | planned |
| #5 | Off-thread agent-initiated memory write/reindex | S-M | planned |
| #6 | In-turn consolidation guidance on memory overflow | S | planned |
| #7 | Skill-invocation telemetry (close the usage→lifecycle loop) | M | planned |
| #8 | Mid-turn steering / interrupt-and-redirect | L | planned |
| #9 | Cross-session conversation search | L | planned |
| #10 | Evolving user model | M-L | planned |
| #11 | Model discovery + live `/model` switch | M | planned |

---

## PR #1 — Conversation compaction *(shipped in PR #630)*

### Problem
`buildConversationContext` (`internal/orchestrator/orchestrator_run_conv.go`)
walks messages newest-first until the char budget is exhausted, then drops the
remaining older turns with a `...(truncated)` boundary marker. On long sessions
this is silent signal loss.

### Approach (MVP)
Split history into a verbatim **recent** window and an **overflow** (older)
slice (`selectRecentMessages`). When overflow exists and an auxiliary
summarizer is wired, compact the overflow into a short block prepended to the
recent window:

```
[EARLIER CONVERSATION — SUMMARY of older messages no longer shown in full]
<summary>
[END EARLIER CONVERSATION]
[CONVERSATION HISTORY - previous messages in this session]
...
```

A fixed slice of the char budget (`conversationSummaryBudgetPct = 15%`, floored
at `minConversationSummaryChars = 200`) funds the summary; the recent window is
re-selected against the reduced budget so both fit; the summary is clamped.

### Deterministic fallback
No summarizer wired (no aux model) **or** a summarize error/blank → historical
newest-first truncation, byte-for-byte. Dev/CI and Ollama-less deployments are
unchanged; a wedged aux model can never fail a run.

### Reuse / Files / Verification
Reuses `consolidate.SummarizerClient` shape via a structurally-identical local
`ConversationSummarizer` interface (no dependency cycle); the existing
`llmSummarizer` adapter (`internal/server/summarizer.go`) satisfies it, wired in
`server.New` via `SetConversationSummarizer`. Tests:
`internal/orchestrator/orchestrator_run_conv_test.go` (summarize / error-fallback
/ nil / under-budget / clamp); the 12 pre-existing truncation tests stay green as
the regression guard. Docs: `chat-sessions.mdx` compaction section.

---

## PR #2 — Temporal anchoring in compaction summaries

### Problem
When the overflow slice is compacted (`summarizeOverflow`,
`orchestrator_run_conv.go:181`), `conversationSummaryInstruction`
(`orchestrator_run_conv.go:34-45`) tells the aux model to preserve completed
tasks but says nothing about *tense* or *dates*. The summarizer can echo
imperative phrasing ("email the report") into the `[EARLIER CONVERSATION —
SUMMARY]` block; on a resumed session the agent reads it as a still-open
instruction and re-issues completed work. Fix: rewrite completed actions into
dated past-tense facts ("Sent the report on 2026-06-09").

**Time-source investigation.** There is no global clock abstraction in the
orchestrator — ~60 direct `time.Now()` calls, and `memory.go:73` already does
`time.Now().UTC().Format("2006-01-02")` (the exact "today" pattern). The
idiomatic injectable-clock shape to copy is `consolidate.Consolidator.Now func()
time.Time` (`internal/consolidate/consolidator.go:32-35`) with a `now()` helper
that falls back to `time.Now().UTC()` when nil (`consolidator.go:277-282`).
Configurable timezone exists only in the pipeline/routine subsystem
(`pipeline.Schedule.Timezone`, `internal/pipeline/schedules.go:33,82`); there is
no per-agent timezone the conversation builder can read. Conclusion: resolve
"today" via an injectable `Now func()` on the `Orchestrator`, UTC `2006-01-02`,
consistent with `memory.go:73`. Per-agent timezone is out of scope.

### Approach
1. Add `now func() time.Time` to `Orchestrator` (`orchestrator.go`, near
   `convSummarizer`) + a `nowUTC()` helper (nil → `time.Now().UTC()`).
2. In `summarizeOverflow`, resolve `today := o.nowUTC().Format("2006-01-02")` and
   prepend a **TEMPORAL ANCHORING** directive (new
   `conversationTemporalAnchorInstruction` const) before the existing
   instruction + transcript: rewrite completed/imperative actions as dated
   past-tense facts; prefer a date recoverable from the transcript, else "around
   {today}"; never restate a finished action as an open instruction.
3. **Best-effort:** if the date string is empty, omit the directive so the prompt
   is byte-identical to PR #1.

**Prompt-cache invariant:** unaffected — the directive lives only inside the
throwaway aux-LLM prompt (`summarizeOverflow`'s builder), never the cached
system-prompt prefix; the summary block is injected after `req.SystemPrompt`
(`orchestrator_run.go:236`).

### Files
- `internal/orchestrator/orchestrator.go` — `now` field + `nowUTC()`.
- `internal/orchestrator/orchestrator_run_conv.go` — `conversationTemporalAnchorInstruction`; extend `summarizeOverflow` (`:181`).
- `internal/orchestrator/orchestrator_run_conv_test.go` — tests.

### Reuse
Clock pattern from `consolidate.Consolidator.Now` (`consolidator.go:34,277-282`);
date idiom from `memory.go:73`; existing `strings.Builder` in `summarizeOverflow`;
extend the `fakeConvSummarizer` test double (`orchestrator_run_conv_test.go:308`)
to capture the prompt string.

### Verification
- Pin `o.now` to `2026-06-09`, capture the aux prompt, assert it contains the
  date + TEMPORAL ANCHORING header.
- Empty-date path → prompt equals PR #1 baseline byte-for-byte (directive omitted).
- A past-tense summary still flows into the block unchanged.
- All existing compaction tests stay green; truncation-fallback path untouched.

### Risks
- **Wrong-date anchoring** — transcripts rarely carry dates; phrase as "around
  {date}" and prefer transcript-recoverable dates. `conversation.Message.Timestamp`
  (`internal/conversation/store.go:36`) could later render real per-message dates
  (follow-up, not in scope).
- **UTC vs operator-local skew** near midnight — accepted; consistent with
  `memory.go:73`; no plumbed per-agent timezone.
- **Prompt-size creep** — a few hundred chars on the aux prompt only; output still
  clamped (`orchestrator_run_conv.go:208`). Negligible.

---

## PR #3 — Compaction robustness

### Problem
Three correctness gaps sit on top of PR #1.

**(a) Tool context at the overflow boundary.** Investigation of the persisted
type changes the shape of this risk. `conversation.Message`
(`internal/conversation/store.go:28-37`) has `ID`, `ChatID` (JSON `session_id`),
`Role`, `Content`, `ToolName`, `ToolSummary`, `Metadata`, `Timestamp` — there is
a `RoleTool` constant (`store.go:23`) **but no `tool_call_id` linkage field**,
and **no caller ever persists a `RoleTool` message**: every `convStore.Append`
writes only `RoleUser`/`RoleAssistant` (`chatbridge/bridge.go:371,652,707`;
`scheduler/scheduler.go:335,440`; `pipeline/runner_orchestrator.go:182`). Tool
activity is folded into the assistant message's `ToolSummary` text field
(`bridge.go:698-709`). So a tool call and its result are already co-located in
one message and cannot be split by an overflow cut. The real residual risk is
narrower: `selectRecentMessages` truncates the boundary message's `Content` and
**zeroes `ToolSummary`** when budget is partial (`orchestrator_run_conv.go:147-150`),
so the recent window can show a half-sentence referencing a tool whose summary
was dropped.

**(b) Cross-session contamination.** Input is correctly session-scoped:
`buildConversationContext` is keyed on `sessionID` (`:62`) from `req.ChatID`
(`orchestrator_run.go:233-234`); the store reads strictly from
`conversations/{sessionID}.jsonl` (`store.go:110`). The residual risk is in the
summarizer round-trip — a shared stateless aux provider; the guard is
**preventive**: make session-purity an asserted invariant before any future
caching change can break it.

**(c) System-prompt prefix byte-stability.** The final prompt starts with
`req.SystemPrompt` then appends history (`orchestrator_run.go:236,249-251`). The
invariant — repeated compaction never perturbs the leading `req.SystemPrompt`
bytes — is documented (`orchestrator_run_conv.go:59-61`) but not tested.

### Approach
- **(a)** In `selectRecentMessages` (`:132`), when the boundary message would be
  truncated and lose its `ToolSummary` (`:147-150`), prefer to drop it into
  overflow whole rather than emit a tool-less fragment — *unless* it is the only
  message that fits (never produce an empty window; preserve the `remaining > 200`
  last-resort truncation at `:145`). Same rule symmetrically so the overflow
  slice never starts mid-message.
- **(b)** Assert `summarizeOverflow` is a pure function of its `overflow` arg
  (it already builds a fresh builder per call, `:187`); add a defensive check
  that all overflow messages share one `ChatID`, else log + skip compaction.
- **(c)** Add a test that builds the final prompt twice over the same overflowing
  session with the summarizer returning two different strings and asserts the
  leading `len(req.SystemPrompt)` bytes are identical and equal to the original.

### Files
- `internal/orchestrator/orchestrator_run_conv.go` — boundary rule
  (`:132-174`); ChatID-consistency guard (`:181`).
- `internal/orchestrator/orchestrator_run_conv_test.go` — tests for (a)(b)(c).
- *(optional)* `internal/orchestrator/orchestrator_run.go` — a small pure
  `assembleSystemPrompt` helper only if needed to test (c) without full `RunAgent`.

### Reuse
Extend existing `cut`/`remaining` accounting (`:142-159`);
`conversation.Message.ChatID` + per-session file path already enforce input
isolation; `seedOverflowSession`/`appendMsg`/`fakeConvSummarizer`/`newConvOrchestrator`
fixtures cover the needed cases (add a `ToolSummary`-bearing variant + a second
session id).

### Verification
- **(a)** Boundary message with a `ToolSummary` → kept window never shows a
  truncated `Content` whose summary was dropped; overflow slice begins at a
  message boundary.
- **(b)** Two sessions with distinct sentinels → each prompt contains only its
  own; mixed-ChatID overflow → `summarizeOverflow` returns `""` (skip + log).
- **(c)** Two runs, different summaries → first `len(req.SystemPrompt)` bytes
  byte-identical.
- All PR #1 compaction tests stay green; no-summarizer path byte-for-byte unchanged.

### Risks
- **(a)** Slightly smaller recent window in tool-heavy turns — mitigated by the
  "only drop when another message fits" rule.
- **(b)** Over-strict guard safe-degrades to truncation (not a crash) and is
  visible via the existing debug log (`:200`).
- **(c)** Helper-extraction churn on a hot path — keep it a pure string fn; if
  risky, test the invariant at the `buildConversationContext` + concat seam instead.

---

## PR #4 — Memory load-time injection scan at prompt assembly

### Problem
Crewship scans memory for prompt-injection on two of three paths but **not** at
boot-snapshot assembly — the largest, highest-trust surface.

- Episodic recall scans every hit: `RenderInjection` calls `lookout.ScanInput(summary)`
  and redacts on `VerdictBlock` (`internal/episodic/recall.go:224`). Signature
  `func ScanInput(text string) ScanResult` (`internal/lookout/injection.go:77`);
  `ScanResult{Findings, Verdict}` (`internal/lookout/types.go:100`), `Verdict` is
  `"allow"|"block"` flipping to block only on High/Critical (`injection.go:157-158`).
- The native memory dispatcher scans on read/write/search via the stronger
  `func ScanContent(body string) *ScanHit` (`internal/memory/quarantine.go:191`,
  returns `*ScanHit{Category,Pattern}`) which adds invisible-unicode, homoglyph,
  and base64 passes (`quarantine.go:179-219`) — `tools.go:262,416,570`.

The gap: `buildMemoryContext` (`memory.go:69`) reads each tier file via
`readContainerFile` (`memory.go:511`) and hands raw bytes to `assembleSections`
(`memory.go:391`), which only wraps them in an "UNTRUSTED HINTS" header
(`memory.go:410-412`). **No `ScanContent`/`ScanInput` call exists in `memory.go`.**
A poisoned on-disk `AGENT.md`/`CREW.md`/`pins.md` (supply-chain template import,
external/PR edit, sister-session write bypassing the dispatcher) reaches the
model verbatim. Framing alone is probabilistic, not deterministic (the recall
code concedes this at `recall.go:188-194`; cf. arXiv:2601.09625).

### Approach
1. In `assembleSections` (`:391`), before a `memorySection`'s content is written,
   run it through `memory.ScanContent` (the stronger scanner). On a non-nil
   `*ScanHit`, substitute the body with `[BLOCKED: possible prompt injection in
   <label> — category=<hit.Category> pattern=<hit.Pattern>; operator can inspect
   the file directly]`, keeping the section label. Marker frame, header, and
   budget math unchanged.
2. Per-section scan so one poisoned `daily/2026-06-08.md` doesn't blank a clean
   `AGENT.md` in the same block (mirrors `handleSearch`'s per-file fail-closed,
   `tools.go:570-588`).
3. **Live file never modified** — assembly is a read-only render (unlike the
   dispatcher's `Quarantine`, `quarantine.go:408`); operator can still `cat`/remove.
4. Deterministic from disk bytes → no prompt-cache drift (matters for the per-day
   instruction cache, `memory.go:466-473`).

### Files
- `internal/orchestrator/memory.go` — scan+placeholder in `assembleSections`
  (`:432-451` loop); import `internal/memory`. All four tier builders
  (`buildAgentMemoryBlock:242`, `buildCrewMemoryBlock:282`,
  `buildWorkspaceMemoryBlock:330`, `buildPinsBlock:371`) funnel through it.
- `internal/orchestrator/memory_test.go` — tests.

### Reuse
`memory.ScanContent`/`ScanHit` (`quarantine.go:191,22`); existing `memorySection`
struct (`memory.go:60-63`) and budget/truncation machinery; the "UNTRUSTED HINTS"
header stays as the outer layer.

### Verification (failing-first)
- Poisoned `AGENT.md` (`ignore previous instructions`) → block contains
  `[BLOCKED:` and not the payload (fails on current main).
- Homoglyph (Cyrillic i) + base64 variant blocked → proves `ScanContent` over
  the weaker `ScanInput`.
- Clean tier renders byte-for-byte identical (cache stability + no false positives).
- Per-section isolation; live file bytes unchanged after assembly.

### Risks
- **False positives** blank a legit entry — placeholder names file+category+pattern;
  nothing deleted; boot-snapshot memory is higher-trust → fail-closed is right.
- **Latency** — one scan per non-empty section under `memoryReadTimeout`
  (`memory.go:29`); bounded by per-tier budgets; negligible.
- **Cache** — placeholders must be deterministic (covered by clean-passthrough test).

---

## PR #5 — Off-thread agent-initiated memory write/reindex

### Problem
`handleMemoryWrite` (`internal/sidecar/memory_write.go:97`) calls `memory.WriteFile`
(`:170`) then `engine.ReindexContext(r.Context())` (`:200`) **synchronously**
before returning `201` (`:219`); `ReindexContext` (`internal/memory/index.go:23`)
does FTS5/embedding work inline. A slow provider holds the agent in "running"
*after* it produced its final response. The handler comment concedes the immediate
reindex is an optimization ("so search hits the new content without debounce lag",
`:198-199`); a 60s periodic crew reindex is the backstop (`sidecar/server.go:578-599`).

### Approach
Dispatch write+reindex to a **single-worker background executor**:
1. Buffered job channel drained by **one** goroutine → strict FIFO, so turn N's
   reindex completes before turn N+1's (preserves read-your-writes within a session).
2. Keep response-critical semantics inline: `WriteFile` + the scrubber/cap check
   + the `422` rejection envelope (`:177-222`) stay synchronous; only
   `ReindexContext` (`:200`) and the `memory.updated` journal emit (`:211`) move
   to the background job. Return `201` as soon as bytes are durable.
3. **Flush barrier** — `Flush()` enqueues a sentinel and blocks until drained,
   called at session boundaries before a read that must see the index.
4. **Bounded drain on teardown** — close+drain with a deadline alongside the
   existing `crewReindexDone` join (`server.go:625-627,643-645`) and
   `memoryEngine.Close()` (`:622-623`).

### Reuse
Mirror the orchestrator's bounded-semaphore-as-token-bucket pattern
(`postToolCallSem chan struct{}`, `orchestrator.go:239`, cap 64 at `:634`, init in
`New()` `:653`, consumed non-blocking at `orchestrator_run.go:815-831`) — but the
memory executor must **not drop and must stay ordered**, so use `cap=1` worker +
bounded queue + flush sentinel (same channel mechanics, ordering-preserving
variant). Teardown join mirrors `crewReindexDone` (`server.go:625-627`).

### Files
- `internal/sidecar/memory_write.go` — move `ReindexContext` (`:200`) + `memory.updated`
  emit (`:211`) off the response path into an enqueue; keep `WriteFile` + rejection inline.
- `internal/sidecar/server.go` — executor field on `Server` (beside `memoryEngine` `:104`),
  start worker where crew-reindex starts (`:582`), drain in both shutdown arms (`:615,636`).
- new `internal/sidecar/memory_executor.go` (single-worker queue + `Flush` + `Close(ctx)`),
  or inline on `Server` if <~40 LOC.
- `internal/sidecar/memory_write_test.go` / `memory_executor_test.go` — tests.

### Verification (failing-first)
- Slow mock reindex (2s) → `handleMemoryWrite` returns `201` in well under 2s.
- Ordering: writes A then B → reindex observes A before B (single-worker FIFO).
- Flush barrier: post-flush `memory.search` sees new content.
- Bounded drain on teardown completes within deadline; no silent job loss (logged).

### Risks
- **Read-your-writes within a turn** — `memory.read` reads the file directly
  (`tools.go:248`), not the index, so unaffected; only `memory.search` could
  briefly miss the newest write — bounded by flush + the 60s periodic reindex.
- **Lost reindex on crash** between `201` and the job — recovered by periodic +
  next-write reindex (matches today's watcher-tick model, `memory_write.go:198`).
- **Queue saturation** — bound the queue and back-pressure briefly (writes are rare
  per turn); do **not** copy the drop policy from `postToolCallSem`.

---

## PR #6 — In-turn consolidation guidance on memory overflow

### Problem
When a `memory.write` (or `append`/`append_daily`) would exceed a tier cap,
`handleWrite` returns a passive dead-end (`internal/memory/tools.go:445-459`):
it says the tier is full and vaguely "drop older entries", but does **not** hand
the model the current entries to consolidate. The model must issue a separate
`memory.read` round-trip — which it often skips, so the write is abandoned and
the new fact lost. The soft-cap warning (`:474-479`) has the same shape.
`append_daily` inherits this via `handleWrite` (`:632-649`).

### Approach
Turn the overflow result into a self-contained consolidation prompt:
1. On overflow, attach the current file body as `current_entries` + usage
   (`current_bytes`/`cap_bytes`/`projected_bytes`) to the error result. (`append`
   already has `old` in hand at `:383`.)
2. Rewrite `Content` to an explicit in-turn directive: "The tier is full. Below
   are the current entries. Consolidate them (merge duplicates, remove the
   stalest, shorten verbose lines) and re-issue this write with `mode='replace'`
   and the consolidated body in this same turn — do not abandon the write."
3. Bring `append`/`replace` to parity; the soft-cap warning adopts the same
   `current_entries` payload so the model can consolidate pre-emptively.
4. **Store stays a pure bounded store** — no auto-consolidation, no LLM call;
   trimming is the model's decision (preserves the agent-curated model,
   `memory.go:191-197`). Overflow is checked *after* `ScanContent` (`:416` before
   `:445`), so poisoned content is never echoed back.

### Files
- `internal/memory/tools.go` — `handleWrite` overflow branch (`:445-459`) + soft-cap
  branch (`:474-479`); reuse `old`/`existing` (`:383-387`) for `append`, add a read
  for `replace`; `handleAppendDaily` (`:632`) inherits. Optionally extend the
  `memory.write` tool description (`:114-116`) — the tool contract is the agent-facing doc.
- `internal/memory/tools_test.go` — tests.

### Reuse
Existing `ToolResult{IsError,Content,Metadata}` (`:40-44`) — `current_entries` +
usage go in `Metadata`; `capForTier`/`capPct` (`:688,709`) compute the usage block.

### Verification
- Overflow returns current entries + retry directive (red first; today only the
  bare string).
- add/replace parity.
- Round-trip: model consolidates returned body, re-issues `replace` → succeeds.
- Soft-cap warning carries entries.
- Store purity: no write performed when over cap.

### Risks
- **Context cost** — echoing the body is bounded by the cap (≤4 KB AGENT/CREW,
  ≤8 KB pins, `:63-67`), one-shot.
- **Model ignores directive** — wording-only; store behavior unchanged → strictly
  better than today. Measure abandonment in eval.

---

## PR #7 — Skill-invocation telemetry (close the usage→lifecycle loop)

### Problem
The Keeper Phase-2 skill-review sweep is **a consumer with no producer**.
`runSkillReviewSweep` (`internal/server/keeper_routines.go:105`) →
`loadSkillSweepInputs` (`:143`) aggregates `COUNT(*)`, error `SUM`, `MAX(invoked_at)`
over `skill_invocations` (`:196-202`) and reads `skills.last_used_at` (`:153-154`)
into `gatekeeper.SkillStats` (`:204-211`), which drives `active→stale→archived`
via `SetLifecycle` (`:260`). The schema shipped in v102: `skill_invocations`
(`internal/database/migrate_consts_v102_keeper_phase2.go:126-138`) + denormalized
`skills.lifecycle_state/last_used_at/usage_count/error_count` (`:110-114`). But a
repo-wide grep finds **no `INSERT INTO skill_invocations`** and **no `skill.invoked`
journal type** (`internal/journal/types.go:230-233` has only import/delete/assign/
unassign). So `invCount` is always 0 and the lifecycle machine runs on a false
"never used" signal. **This PR builds the missing producer.**

### Signal options (with feasibility)
Skills are materialized as files into the container (`.claude/skills/<slug>/SKILL.md`
+ 5 sibling paths via `writeAgentSkills`, `internal/orchestrator/skills_writer.go:103-131`)
and invoked inside the agent's CLI — Crewship never sees activation directly.

- **(a) New sidecar `/skill` self-report** — follows the existing slash-route proxy
  pattern (`sidecar/server.go:441`, `skill_generate.go:31`) but relies on the model
  voluntarily curling on every activation (honor system it'll skip) + adds an
  endpoint with API↔CLI-parity obligation. **Low value, high cost — defer.**
- **(b) Journal events** — import/assign only; a sink, not a source. We add the
  missing `skill.invoked` type, but it still needs a detector.
- **(c) Orchestrator tool-call stream tap** — **the reliable signal.** Adapters
  decode `tool_use` into `AgentEvent{Type:"tool_call"}` with `Metadata["tool_name"]`
  + `["input"]` (`adapter_claude.go:186-200`; all parsers normalize the same). There
  is **already a hot-path tap**: `orchestrator_run.go:788` extracts `toolName`/`input`
  and dispatches to a `PostToolCallObserver` through a bounded semaphore
  (`:815-831`); `ToolCallObservation{WorkspaceID,CrewID,AgentID,MissionID,ToolName,Payload}`
  (`orchestrator.go:302-309`) carries every field a row needs. **High feasibility.**
  Matching: a call is a skill invocation when `tool_name=="Skill"` and the input
  slug matches `skills.slug` (the folder name `writeAgentSkills` writes, and a
  `NOT NULL UNIQUE` column, `migrate_consts_v01_init.go:205`); for CLIs without a
  first-class Skill tool, match `tool_name` against the agent's assigned slugs
  (`req.Skills[].Slug`, `orchestrator.go:52,72-76`).

### Approach (producer for an already-built consumer)
1. Add `EntrySkillInvoked EntryType = "skill.invoked"` (`journal/types.go:230-233`).
2. Add a `SkillInvocationObserver` interface + `SetSkillInvocationObserver`/getter on
   the orchestrator (sibling to `PostToolCallObserver`, `orchestrator.go:289-309,514-522`),
   invoked from the same `tool_call` branch (`orchestrator_run.go:788`), reusing the
   extracted `toolName`/`payload` and the same bounded-semaphore discipline.
3. Implement the observer in `server/` (new `skill_invocation_observer.go`, the
   trusted tier with `*sql.DB` + journal writer, wired beside `newPostToolCallObserver`
   at `server.go:709`): resolve the agent's assigned slugs once per run (cache by
   `agent_id`), match, then `INSERT INTO skill_invocations(...)` + **denormalize in
   the same txn** `UPDATE skills SET usage_count=usage_count+1, last_used_at=?
   [, error_count=error_count+1]`, and emit `skill.invoked`. `exit_code` defaults 0
   (activation, not the skill's own result — see Risks).

One coherent PR: rides the **existing** tool-call tap + observer plumbing — no new
stream parser, no new sidecar route, no new HTTP endpoint (no API↔CLI obligation).
Accurate skill *exit codes* require tool-result correlation → follow-up.

### Files
- `internal/journal/types.go` — `EntrySkillInvoked`.
- `internal/orchestrator/orchestrator.go` — `SkillInvocationObserver` + `SkillInvocation`
  struct, `skillInvObs` field, setter/getter (siblings to `:289-309,514-522`).
- `internal/orchestrator/orchestrator_run.go` — dispatch in the `tool_call` block (`:788-834`).
- new `internal/server/skill_invocation_observer.go` (model on `post_tool_call_adapter.go`).
- `internal/server/server.go` — `orch.SetSkillInvocationObserver(...)` beside `:703-709`.

### Reuse
The entire detection path (`tool_call` event, extraction, bounded semaphore) at
`orchestrator_run.go:788-834`; `PostToolCallObserver`/`ToolCallObservation` template;
`skills.slug` ↔ folder-name join key; consumer + `gatekeeper.SkillStats` untouched.

### Verification
- Orchestrator unit (red-first): a `Skill` `tool_use` line fires the observer with the
  slug; a Read/Bash call does not (extend `exec_test.go`).
- Producer→consumer integration on a migrated SQLite (v102 harness
  `migrate_v102_keeper_phase2_test.go`): one matched observation → one
  `skill_invocations` row, `usage_count==1`, `last_used_at` non-NULL, a `skill.invoked`
  entry, and `loadSkillSweepInputs` now returns non-zero `InvocationCount` — loop closes.
- Lifecycle: a freshly-invoked skill is **not** flipped to `stale`; an unused one is.

### Risks
- **Activation ≠ success** — `exit_code=0`; document so F4.1 isn't read as "never errors".
- **Slug-matching FP/FN** — gate on the per-run assigned-slug set; unit-test each adapter's
  `tool_name` shape; an unmatched call records nothing (fail-safe).
- **Hot-path cost** — bounded semaphore + drop-on-overflow + per-run slug-set cache (map
  lookup) + single indexed insert; rare vs raw tool calls.

---

## PR #8 — Mid-turn steering / interrupt-and-redirect

### Problem
Once a turn runs, the operator can't course-correct short of killing it. The path
is one-shot and synchronous: `claudeCodeAdapter.BuildCommand` bakes the user
message into argv (`adapter_claude.go:21-86`, line 84) — no stdin; `RunAgent`
launches via `o.container.Exec` with a `provider.ExecConfig` that has **no Stdin**
(`orchestrator_run.go:722`, `provider/container.go:108-115`); the Docker impl
attaches stdout/stderr only (`provider/docker/docker.go:481-515`); `streamOutput`
(`exec_stream.go:94-226`) never writes back. `Bridge.HandleChatMessage`
(`chatbridge/bridge.go:258`) blocks on `RunAgent` (`:634`) with **no in-flight
guard** — a second WS message spawns a *separate* `Exec`. Only cancellation exists
(`execCtx`, `orchestrator_run.go:671`). `PRD-AGENT-EVOLUTION-2026.md:369-370` lists
"F4.2 synchronous BLOCK CLI interrupt — DEFERRED". The bidirectional primitive
already exists for the web terminal: `ExecInteractive` sets `AttachStdin:true`
and returns a `ReadWriteCloser` (`docker.go:528-565`, `container.go:214-218`) — the
run path just doesn't use it.

### Approach (minimal first slice — Claude + Docker)
1. Add `Stdin io.Reader` to `provider.ExecConfig` (`container.go:108`) and wire it
   in `docker.Provider.Exec` (`AttachStdin:true`, copy into the conn, no TTY so
   `stdcopy` framing on output is preserved — mirror `ExecInteractive`). Gate behind
   adapter capability so every other adapter's one-shot path is untouched.
2. Add `SupportsStreamingInput() bool` to `CLIAdapter` (`cli_adapter.go`), true only
   for Claude; when true `BuildCommand` switches to `--input-format stream-json` and
   emits the initial user message as the first stream-json line; add a steer-encoder.
3. Add a buffered steering channel to run state keyed by `chatID`
   (`orchestrator_run_status.go`), a method `Steer(chatID,msg)`, and an input-pump
   goroutine in the exec path that writes encoded lines to stdin.
4. In `HandleChatMessage`, add the missing in-flight guard (active-runs
   `map[chatID]` under a mutex, like `b.containerMu` `bridge.go:154`): live run +
   streaming-input adapter → route to `Steer` + emit `steering_injected`; else
   fall back to the deferred behavior (`convStore.Append` tagged as queued steer +
   a "next turn" status).
5. **Trust boundary** — the steering line enters as a `user` turn; scan it with
   `memory.ScanContent` (already used at `exec_stream.go:258`) before it reaches the
   model; block on a hit.

### Files
`provider/container.go` (Stdin); `provider/docker/docker.go` (wire stdin; Apple
provider returns unsupported in this slice); `orchestrator/cli_adapter.go`
(capability + encoder); `orchestrator/adapter_claude.go` (`--input-format stream-json`
branch); `orchestrator/orchestrator_run.go` (steering channel + pump, register/
unregister run); `orchestrator/orchestrator_run_status.go` (`Steer`); `chatbridge/bridge.go`
(in-flight guard + routing + queued fallback); `internal/api/` + `cmd/crewship/cmd_chat.go`
(`POST /api/v1/chats/{id}/steer` + CLI — parity rule); `docs/guides/*.mdx` (operator
doc + per-adapter support matrix).

### Reuse
`ExecInteractive`/`InteractiveExecResult` (stdin-attach template); run/status tracking
(`orchestrator_run_status.go`, `updateRunStatus` `:725`); inbound scan
(`memory.ScanContent`, `exec_stream.go:258`); WS fan-out pipeline; container-mutex
pattern (`bridge.go:154`).

### Verification
- `adapter_claude` emits `--input-format stream-json` + a first user line when steering
  enabled; one-shot shape unchanged when not (pin `exec_test.go`).
- Docker SDK test (per no-`exec.Command` rule): `Exec` with `Stdin` delivers bytes and
  still returns framed stdout.
- `Steer` mid-run produces an extra stdin line; a steer tripping `ScanContent` is blocked.
- Bridge: second message during live run on streaming adapter → `Steer` (no second
  `Exec`); non-supporting adapter → queued `Append` + "next turn".
- Acceptance: `crewship chat steer ...` via the binary.

### Risks
- **stdin framing vs `stdcopy`** — output is multiplexed; the stdin writer must stay
  independent of the `io.Pipe`+`StdCopy` output path (`docker.go:504-509`).
- **CLI input-format drift** across `claude` releases — capability flag must fail closed
  to the queued fallback.
- **Race on the in-flight map** — single CAS-style transition under the mutex.
- **Trust** — steering is a privileged mid-loop injection; `ScanContent` gate is load-bearing.
- **Non-supporting adapters** (Codex/Gemini/Cursor/Droid/OpenCode) get async-only —
  the doc matrix must say so.

---

## PR #9 — Cross-session conversation search

### Problem
An agent can recall *curated* memory but cannot search its own *raw past
transcripts*. Curated memory is FTS-indexed and agent-callable
(`memory.Engine` FTS5 `memory_chunks`, `internal/memory/engine.go:79-83`; tools at
`tools.go:89-217`). Raw transcripts are **not** queryable: `conversation.Store`
persists each session as JSONL (`internal/conversation/store.go:82-87`), and the
only read API is `Read(ctx,sessionID,offset,limit)` (`:101`) — one named file,
linear scan, no table, no index, no cross-session query (write/read-by-id only at
`bridge.go:369,650,705`; `runner_orchestrator.go:179-182`; `scheduler.go:38`). So
"what did I tell this user about their deploy pipeline three sessions ago?" has no
answer surface. The infra is proven adjacently: external-content FTS5 over a base
table with sync triggers (`journal_entries_fts`, `migrate.go:430-452`; `fts5Phrase`
`journal/queries.go:51-55`) and dense recall (`episodic.Recall`/`HybridRecall`,
`recall.go:28-138`, `hybrid.go:13-14`).

### Approach (minimal first slice)
Messages live only in JSONL, so this PR must first land them in a queryable store.
1. Add a `conversation_messages` table (id, session_id, agent_id, workspace_id,
   crew_id, role, content, ts) + external-content FTS5 shadow
   `conversation_messages_fts(content, content='conversation_messages',
   content_rowid='rowid', tokenize='porter ascii')` with the **exact** trigger form
   from journal (`migrate.go:437-452` — the contentless `'delete'` trigger is
   mandatory to avoid index corruption). `Store.Append` (`store.go:57`) **dual-writes**:
   keep JSONL (replay/debug) + insert a row.
2. `Store.Search(ctx, agentID, query, limit)` runs `MATCH` joined to the base table,
   **always filtered by `agent_id`** (an agent only searches its own transcripts);
   neutralize FTS operators with `fts5Phrase`/`sanitizeFTSQuery` (`memory/search.go:77-98`).
3. New MCP tool `conversation.search` in `ToolSchemas()`/`Dispatch` (`tools.go:89,204`),
   *next to* `memory.search`, description making the distinction explicit; thread the
   conversation store into the dispatcher.
4. Optional semantic re-rank when the episodic embedder is available (cosine over the
   FTS candidate set, `recall.go:61-138` / `HybridRecall`); BM25-only otherwise.
5. CLI parity: `POST /api/v1/conversations/search` + `cmd/crewship/cmd_conversation.go`.

### Files
`internal/database/migrate.go` (new migration after the latest version — table + FTS +
triggers, copy `:430-452`); `internal/conversation/store.go` (accept `*sql.DB`, dual-write
in `Append` `:57`, add `Search`); `internal/server/server.go` (pass DB into
`conversation.NewStore` `:206`, store handle into the dispatcher);
`internal/memory/tools.go` (`conversation.search` schema `:89` + dispatch `:204`);
`internal/episodic/hybrid.go` (reuse fusion if enabled); `internal/api/` +
`cmd/crewship/cmd_conversation.go`; `docs/guides/*.mdx`.

### Reuse
`journal_entries_fts` triggers template (`migrate.go:430-452`); `fts5Phrase`
(`journal/queries.go:51`) + `sanitizeFTSQuery` (`memory/search.go:77`); `memory.search`
schema/dispatch shape; `episodic.Recall`/`HybridRecall`/`OllamaEmbedder`; existing
`Store.Append` call sites already feed all message traffic (no producer changes).

### Verification
- Migration-lint test (`migrate_lint_test.go`): version unique/sequential, FTS triggers present.
- `Store`: `Append` writes JSONL + row; `Search` returns the hit and **excludes another
  agent's** transcript; FTS-operator query (`NEAR(...)`, `*`, `col:`) neutralized.
- MCP dispatch: `conversation.search` returns hits; `memory.search` unchanged.
- Acceptance: `crewship conversation search ...` via the binary.
- Optional: stub embedder reorders FTS candidates; no-embedder → BM25-only still returns.

### Risks
- **FTS5 trigger correctness** — copy the contentless `'delete'` trigger verbatim
  (`migrate.go:440-445`) or risk index corruption (SQLite 267).
- **Backfill** — existing transcripts are JSONL-only; first slice is search-from-now-on
  or a one-time importer walking `conversations/*.jsonl`.
- **Dual-write divergence** — define ordering + failure policy so JSONL and row don't drift.
- **Cross-tenant leakage** — the `agent_id`/workspace filter on `Search` is load-bearing.
- **Index growth / cost** — conversations are higher-volume than journal; needs a
  retention story (cf. `journal_entries_archived`). Default BM25-only; semantic re-rank opt-in.

---

## PR #10 — Evolving user model

### Problem
Crewship distills *per-(agent,user)* peer cards but never deepens into a structured,
durable operator model. A peer card is a ≤1500-byte markdown blob per `(agent,user)`
(`internal/memory/peers.go:14-58`, `UserSlug` hash `:75`), written by `SyncPeerCard`
(`internal/consolidate/peer_card_writer.go:120`; threshold `:38-57`, consent
`IsOptedOut:79`, audit `:246`, write `memory.WritePeerCard` `peers.go:146`), with the
LLM prompt in the worker (`peer_card_worker.go:32-50,73`), injected as `[PEER CONTEXT]`
after PERSONA (`orchestrator/memory_persona.go:111-137`, wired `memory.go:146-160`,
framed "hint not fact" `:133`). Limits: **(1)** per-agent, not per-operator — a fresh
agent can't read a shared operator model on turn one; **(2)** flat/unstructured,
overwritten each sweep, no field deepening/correction; **(3)** no `user`/`operator`
memory category in the file-first tiers (`internal/memory/doc.go:1-11`), so operator
facts leak into `AGENT.md`.

### Approach
A **workspace-scoped, structured user model** written by the consolidator, mirroring
(not replacing) the peer-card pattern.
- **One file per operator per workspace**: `rootDir/{workspaceID}/users/{user_slug}.md`
  (registry root `internal/memory/registry.go:35`), `user_slug` via the existing hash.
- **Structured, append-and-revise** layout with stable headings (`## Preferences`,
  `## Working style`, `## Recurring context / projects`, `## Constraints (do/don't)`,
  `## Open questions`); each bullet carries a confidence/last-seen marker so a sweep can
  *deepen* or *correct* rather than clobber. This dialectic deepening is the concept
  explored by Honcho (github.com/plastic-labs/honcho); kept file-first/transparent so the
  operator can hand-edit (human-edit watcher, `internal/memory/watcher.go`).
- **Consolidator-driven**, mirroring `SyncPeerCard`: same threshold/consent/audit
  scaffolding, same "prompt in worker, plumbing in package" split; differs in granularity
  (`(user,workspace)` not `(agent,user)`) and a **merge** extraction prompt (prior model +
  new transcript → revised model).
- **Inject a new `[OPERATOR MODEL]` block** right before `[PEER CONTEXT]` in
  `buildAgentMemoryBlock` (`memory.go:146-160`) — operator model is the durable
  cross-agent layer, peer card the agent's private relationship colour; both stay
  "hints, not facts".

### Files
new `internal/memory/users.go` (mirror `peers.go:96-228`; reuse `UserSlug` + flock +
byte cap); new `internal/consolidate/user_model_writer.go` (mirror
`peer_card_writer.go:120-275`); new `internal/consolidate/user_model_worker.go` (mirror
`peer_card_worker.go:73-221`, merge prompt); `internal/orchestrator/memory_persona.go`
(`buildUserModelBlock` next to `:111`); `internal/orchestrator/memory.go` (wire the block
into `:122-129` and `:146-160`, ahead of peer); DB `user_model_audit` + reuse
`user_peer_consent` (queried `peer_card_writer.go:79`) so one opt-out covers both;
`docs/guides/*.mdx`.

### Reuse
The peer-card subsystem is a near-complete template (threshold, consent, audit,
persistence-split-from-extraction, FTS-indexed file-first storage, prompt-block
injection). PR #10 is largely a re-parameterisation to a coarser key + merge-not-replace
prompt + typed layout.

### Verification (test-first)
- `users_test.go` — round-trip, slug isolation across workspaces, byte cap, path-traversal
  rejection (mirror `peers_test.go`).
- `user_model_writer_test.go` — threshold gating, consent opt-out skip+purge, audit row,
  **merge preserves prior high-confidence fields when the new transcript is silent** (the
  behaviour distinguishing this from peer cards).
- `memory_persona_test.go` — `buildUserModelBlock` returns "" with no opener/workspace/file;
  renders ahead of `[PEER CONTEXT]`. Failing-first: an `[OPERATOR MODEL]` block appears for a
  fresh second agent in a workspace where the operator already has a model.

### Risks
- **PII surface** — keep the slug hash, consent gate, "never inject other operators' models".
- **Prompt-injection / poisoning** — wrap transcript in delimiters, keep "hint not fact" so a
  malicious operator can't write durable instructions cross-session.
- **Overlap with peer cards** — scope: operator model = stable cross-agent facts; peer card =
  relationship tone. Document the boundary to avoid double extraction.
- **Budget** — small/unbudgeted like PERSONA/peer (`memory.go:147-149`); keep the byte cap tight.

---

## PR #11 — Model discovery + live switch

### Problem
An agent's model is a free-text column with no discovery and no validation. The agents
table has `cli_adapter`/`llm_provider`/`llm_model` (`migrate_consts_v01_init.go:137-139`),
`llm_model` nullable free text. At run time it's read per turn and passed straight through:
`AgentRunRequest.LLMModel` (`orchestrator.go:32`) from `target.LLMModel`
(`api/assignments_run.go:398`, `api/query_handler.go:276`) → `"model": req.LLMModel`
(`orchestrator_run.go:700`); the `llm.Provider` interface is model-agnostic
(`internal/llm/provider.go:82-94`) and providers forward `req.Model` to the wire
(`anthropic.go:102`, `openai.go:166`, `ollama.go:105-119`). Validation is asymmetric:
the PATCH handler validates `llm_provider` against an enum (`api/agents_update.go:155-164`,
`agents.go:190-197`) and `cli_adapter` against the six adapters (`cli_adapter.go:97-103`),
but `llm_model` is an **unchecked passthrough** (`agents_update.go:56-61`). **No discovery
exists anywhere** — no `/models` route, no `ListModels`, no curated constant, no CLI model
command. The live switch is half-built: `crewship agent update --llm-model`
(`cmd_agent_lifecycle.go:121,168-172`) PATCHes the agent and, because the model is read
fresh per turn, already takes effect next turn. Missing: **(a)** discovery of what to set,
**(b)** validation so the switch can't set a model the provider rejects.

### Approach
**(a) Provider discovery** — add an optional capability without breaking the model-agnostic
core:
```go
type ModelLister interface { ListModels(ctx context.Context) ([]ModelInfo, error) }
type ModelInfo struct { ID, DisplayName, Provider string }
```
Ollama → `GET {base}/api/tags` (live local models); OpenAI → `GET {base}/v1/models`;
Anthropic → `GET /v1/models` if reachable else a **curated list** in one file (source the
Claude model ids from the `claude-api` skill, not memory). Providers without `ModelLister`
fall back to the curated list keyed by `llm_provider`.

**(b) API + validation + live switch** — `GET /api/v1/models?provider=…` builds the provider
via the existing credential-resolution path (`runner_llm.go:297-340`), type-asserts
`ModelLister`, returns live list or curated fallback. Tighten `agents_update.go`: validate
`llm_model` against the discovered/curated set for the resolved provider (turn the silent
passthrough `:56-61` into a real check beside `:155-164`). The switch needs no new write
path.

**(c) CLI parity** (rule #3) — `crewship model list [--provider]` via the shared
`client.Get` helpers; `crewship agent update --llm-model` (`cmd_agent_lifecycle.go:168-172`)
is the switch; acceptance test drives the endpoint via the binary.

### Files
`internal/llm/provider.go` (`ModelLister`+`ModelInfo`); `internal/llm/{ollama,openai,anthropic}.go`
(`ListModels`); new `internal/llm/models_curated.go`; new `internal/api/models.go` + route in
`router_crews.go` (beside `:195-210`); `internal/api/agents_update.go` (`llm_model` validation;
mirror in `agents_create.go`); new `cmd/crewship/cmd_model.go`; `docs/guides/*.mdx`.

### Reuse
Credential resolution + middleware-wrapped provider construction (`runner_llm.go:297` /
`crew_ai.go getLLMProvider`); provider enum (`agents.go:190`); CLI client helpers +
`resolveAgentID` (`cmd_agent_lifecycle.go:134`); the live switch needs **no new code** —
`target.LLMModel` is already read per turn.

### Verification
- `{ollama,openai,anthropic}_test.go` — `ListModels` parses the payload against `httptest`;
  non-`ModelLister` path returns curated.
- `models_test.go` — live list when supported, curated fallback otherwise, 400 on unknown provider.
- `agents_update_test.go` — bogus `llm_model` now 400s (red on current main); valid 200s and
  next run reads it.
- Acceptance: `crewship model list --provider ANTHROPIC` then `crewship agent update --llm-model …`.

### Risks
- **Live discovery latency/failures** — short context timeout + curated fallback; optional
  per-workspace cache.
- **Curated-list staleness** — one file; source Anthropic ids from `claude-api` skill.
- **Self-hosted Ollama** — for `OLLAMA`, validate against live `/api/tags` (accept whatever is
  pulled), reject only when reachable and definitively missing.
- **Mid-run switch** — lands next turn, not in-flight (`orchestrator_run.go:700`); document it.

---

## Decision log

- **Compaction over truncation, with deterministic fallback.** Summarizing preserves
  signal; the nil-summarizer fallback guarantees zero behavior change where no aux model
  exists, so the feature ships safely "off by default."
- **Reuse existing infrastructure over new mechanisms.** The summarizer slot (PR #1/#2),
  the tool-call observer plumbing (PR #7), the peer-card subsystem (PR #10), and the
  journal FTS5 pattern (PR #9) are all reused rather than reinvented.
- **Mid-conversation summary, never the cached prefix** (PR #1–#3) — preserves the
  prompt-cache invariant that keeps long sessions cheap.
- **Producers for already-built consumers first** — PR #7 (skill telemetry) fills a table
  whose consumer already runs on empty data; high leverage, low cost.
- **One PR per roadmap item** — each lands with its own tests and docs.

## References

- Anthropic — context engineering / building effective agents / prompt caching docs;
  canonical Claude model ids via the `claude-api` skill (PR #11).
- EU AI Act, Article 14 (human oversight & audit trail) — memory versioning alignment,
  also referenced in `MEMORY-ROADMAP-2026.md`.
- Promptware Kill Chain (arXiv:2601.09625) and prompt-injection threat-model literature —
  load-time scanning rationale (PR #4).
- Honcho — github.com/plastic-labs/honcho (dialectic user modeling, PR #10).
- agentskills.io — open skills standard (skills interop).

## Sources (internal)

- `internal/orchestrator/orchestrator_run_conv.go` — conversation assembly (PR #1–#3)
- `internal/orchestrator/memory.go`, `memory_persona.go` — memory/persona assembly (PR #4,#10)
- `internal/consolidate/` — summarizer slot, peer-card subsystem (PR #2,#10)
- `internal/episodic/recall.go`, `hybrid.go` — recall + Lookout scan + FTS/dense (PR #4,#9)
- `internal/lookout/`, `internal/memory/quarantine.go` — injection scanners (PR #4)
- `internal/sidecar/memory_write.go`, `internal/memory/tools.go` — write path + tools (PR #5,#6)
- `internal/server/keeper_routines.go`, `internal/database/migrate_consts_v102_keeper_phase2.go`
  — skill lifecycle consumer + `skill_invocations` schema (PR #7)
- `internal/orchestrator/orchestrator_run.go`, `exec_stream.go`, `cli_adapter.go`,
  `internal/provider/` — run/exec/adapters (PR #7,#8)
- `internal/conversation/store.go`, `internal/database/migrate.go` — transcript store + FTS (PR #9)
- `internal/llm/`, `internal/api/agents_update.go`, `cmd/crewship/` — providers + agent config (PR #11)
- `MEMORY-ROADMAP-2026.md`, `PRD-AGENT-EVOLUTION-2026.md` — prior memory/evolution work
