# PRD: Agent Continuity — durable context, interaction & the learning loop (2026)

| Field | Value |
|---|---|
| Owner | Pavel |
| Status | in progress (PR #1 — conversation compaction — in flight) |
| Scope | internal design notes for long-running "persistent colleague" agents |

## 1. Context

Crewship's positioning is **persistent colleagues**, not one-shot tools — agents
that work across long sessions and many days. That promise leans on three
capabilities that today are either missing or only partially built:

1. **Durable context.** A conversation that outgrows the model's context window
   currently loses its oldest turns by *truncation* — the orchestrator drops
   them and stitches a `...(truncated)` marker onto the boundary message
   (`internal/orchestrator/orchestrator_run_conv.go`). Early decisions,
   established facts, and still-open threads silently disappear. A long-lived
   colleague that forgets the start of the conversation isn't a colleague.

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
structured so **each item is one shippable PR**.

## 2. Goals

- Replace lossy conversation truncation with **compaction** (summarize the
  overflow instead of dropping it), with a deterministic fallback so behavior
  is unchanged when no auxiliary model is configured.
- Make compaction summaries **temporally anchored** so a resumed conversation
  never re-issues already-completed work.
- Close the memory learning loop: real **skill-usage telemetry**, **load-time
  injection scanning** of on-disk memory, and **off-thread** memory writes.
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
  prompt cache keeps hitting. See Anthropic prompt-caching docs.
- **SQLite + FTS5** for storage and search; no separate vector DB.
- **File-first markdown** for human-readable agent memory — every value the
  agent reads is something the operator can read and edit too.

## 5. Roadmap — one PR per item

| PR | Title | Effort | Status |
|---|---|---|---|
| **#1** | Conversation compaction (summarize overflow, deterministic fallback) | M | in flight |
| #2 | Temporal anchoring in compaction summaries | S | planned |
| #3 | Compaction robustness (tool-call pair preservation, cross-session guard, cache stability) | M | planned |
| #4 | Memory load-time injection scan at prompt assembly | S-M | planned |
| #5 | Off-thread agent-initiated memory write/reindex | S-M | planned |
| #6 | In-turn consolidation guidance on memory overflow | S | planned |
| #7 | Skill-invocation telemetry (close the usage→lifecycle loop) | M | planned |
| #8 | Mid-turn steering / interrupt-and-redirect | L | planned |
| #9 | Cross-session conversation search | L | planned |
| #10 | Evolving user model | M-L | planned |
| #11 | Model discovery + live `/model` switch | M | planned |

---

## 6. PR #1 — Conversation compaction

### Problem
`buildConversationContext` (`internal/orchestrator/orchestrator_run_conv.go`)
walks messages newest-first until the char budget is exhausted, then drops the
remaining older turns. On long sessions this is silent signal loss.

### Approach (MVP)
Split the history into a verbatim **recent** window and an **overflow** (older)
slice. When overflow exists and an auxiliary summarizer is wired, compact the
overflow into a short summary and prepend it as:

```
[EARLIER CONVERSATION — SUMMARY of older messages no longer shown in full]
<summary>
[END EARLIER CONVERSATION]
[CONVERSATION HISTORY - previous messages in this session]
...
```

A fixed slice of the char budget (`conversationSummaryBudgetPct = 15%`, floored
at `minConversationSummaryChars = 200`) funds the summary; the recent window is
re-selected against the reduced budget so both fit. The summary is clamped to
its budget.

### Deterministic fallback
When no summarizer is wired (no aux model configured) **or** the summarize call
errors / returns blank, the function falls back to the historical newest-first
truncation, byte-for-byte. This keeps dev/CI and Ollama-less deployments
unchanged and means a wedged aux model can never fail a run.

### Reuse (no new infrastructure)
- `consolidate.SummarizerClient` shape (single `Summarize(ctx, prompt)` method).
  The orchestrator declares a structurally-identical local interface
  `ConversationSummarizer` to avoid a dependency cycle; the existing
  `llmSummarizer` adapter (`internal/server/summarizer.go`) satisfies it.
- `tokenutil.CharsForTokens` for the budget; existing `[CONVERSATION HISTORY]`
  delimiter convention.

### Files
- `internal/orchestrator/orchestrator.go` — `ConversationSummarizer` interface,
  `convSummarizer` field, `SetConversationSummarizer` / `getConvSummarizer`.
- `internal/orchestrator/orchestrator_run_conv.go` — `selectRecentMessages`
  helper + compaction path + `summarizeOverflow`.
- `internal/server/server.go` — wire the existing aux summarizer into the
  orchestrator via `SetConversationSummarizer` (guarded on non-nil).

### Verification
- `internal/orchestrator/orchestrator_run_conv_test.go` — fake summarizer test
  double; cases: summarizes overflow into the block; summarize error → falls
  back to truncation; nil summarizer → no block (default path); under-budget →
  no aux call; verbose summary clamped to budget. All 12 pre-existing tests stay
  green (the regression guard — they construct the orchestrator with no
  summarizer).
- Runtime: long conversation over budget on the dev VM → confirm the summary
  block appears in the assembled prompt and the agent retains an early decision.

---

## 7. PR #2 — Temporal anchoring

The summarizer is handed the current date and instructed to rewrite open
instructions as dated, past-tense facts (e.g. *"email the report"* →
*"Sent the report on 2026-06-09"*), so a resumed conversation never re-issues
already-completed work. Date resolution is best-effort: if it fails, the rule is
omitted and compaction proceeds. The date is injected only into the
mid-conversation summary prompt — never the cached system-prompt prefix.

## 8. PR #3 — Compaction robustness

- Keep `tool_call` / `tool_response` pairs intact when an overflow boundary
  falls between them, so a summarized transcript never references a tool result
  whose call was dropped.
- Guard against cross-session summary contamination (a summary from session A
  must never leak into session B's window).
- Assert system-prompt prefix byte-stability under repeated compaction.

## 9. PR #4 — Memory load-time injection scan

Today the Lookout scanner runs on episodic recall hits
(`internal/episodic/recall.go`) but **not** on the markdown memory tiers at
prompt-assembly time (`internal/orchestrator/memory.go` `assembleSections`). An
on-disk poisoned `AGENT.md` / `CREW.md` (supply-chain, external edit, or a
sister-session write) therefore reaches the model. Scan every tier's content at
assembly; replace a flagged entry with a `[BLOCKED: …]` placeholder in the
*prompt snapshot* while leaving the live file untouched so the operator can
still inspect and remove it. Scan is deterministic from disk bytes → no
prompt-cache drift.

## 10. PR #5 — Off-thread memory write/reindex

`internal/sidecar/memory_write.go` writes and re-indexes synchronously on the
agent-initiated path; a slow index/provider holds the agent in "running" after
it has already produced its final response. Dispatch the write+reindex to a
single-worker background executor (serializes so turn N lands before N+1), with
a `flush` barrier for session boundaries and a bounded drain on teardown.

## 11. PR #6 — In-turn consolidation guidance on overflow

When a bounded memory write overflows, the error returned to the model should
instruct it to consolidate (merge/remove/shorten) using the echoed current
entries and retry within the same turn, instead of returning a passive
dead-end. `internal/memory/tools.go`. The store stays a pure bounded store; only
the guidance text and the `replace`/`add` parity change.

## 12. PR #7 — Skill-invocation telemetry

The `skill_invocations` table and the F4.1 lifecycle sweep that *reads* it
(`internal/server/keeper_routines.go`) already exist, but nothing *writes*
invocations, so usage-based lifecycle transitions run on empty data. Capture a
skill-use signal (via the sidecar slash-route surface and/or a
`skill.invoked` journal event), insert into `skill_invocations`, and denormalize
`skills.usage_count` / `last_used_at`. This is the producer for an already-built
consumer.

## 13. PR #8 — Mid-turn steering

Allow an operator message to be injected into a running turn without killing it
(unblocks F4.2). Extend the WS chat path (`internal/ws`) to deliver a trusted
steering message into the in-flight run; CLI-specific stdin/stream handling per
adapter. Larger, separate design.

## 14. PR #9 — Cross-session conversation search

An agent-callable search over its own prior conversation transcripts (FTS5 +
optional summarization recall), distinct from curated memory — raw-transcript
retrieval shaped by the agent's query at the moment it needs it.

## 15. PR #10 — Evolving user model

A structured, deepening model of the operator across sessions (preferences,
working style, recurring context), maintained alongside file-first memory.
Reference: Honcho (github.com/plastic-labs/honcho) for dialectic user modeling.

## 16. PR #11 — Model discovery + live switch

Provider model discovery and a live `/model` switch so the operator can change
an agent's model without editing config and restarting.

## 17. Decision log

- **Compaction over truncation, with deterministic fallback.** Summarizing
  preserves signal; the nil-summarizer fallback guarantees zero behavior change
  where no aux model exists, so the feature ships safely "off by default."
- **Reuse the consolidator's summarizer slot.** No new LLM client, no new
  provider config; the same aux model already used for consolidation/curation.
- **Mid-conversation summary, never the cached prefix.** Preserves the
  prompt-cache invariant that keeps long sessions cheap.
- **One PR per roadmap item.** Each lands with its own tests and docs.

## 18. References

- Anthropic — context engineering / building effective agents / prompt caching
  documentation (context-window management, cache stability).
- EU AI Act, Article 14 (human oversight & audit trail) — memory versioning
  alignment, already referenced in `MEMORY-ROADMAP-2026.md`.
- Promptware Kill Chain (arXiv:2601.09625) and prompt-injection threat-model
  literature — load-time scanning rationale (PR #4).
- Honcho — github.com/plastic-labs/honcho (dialectic user modeling, PR #10).
- agentskills.io — open skills standard (skills interop).

## 19. Sources (internal)

- `internal/orchestrator/orchestrator_run_conv.go` — conversation assembly
- `internal/orchestrator/memory.go` — memory tier assembly + delimiters
- `internal/consolidate/` — summarizer slot, consolidation, scoring
- `internal/episodic/recall.go` — recall + Lookout scan on hits
- `internal/sidecar/memory_write.go` — agent-initiated write path
- `internal/server/keeper_routines.go` — F4.1 skill lifecycle sweep (consumer)
- `internal/database/migrate_consts_v102_keeper_phase2.go` — `skill_invocations`
- `MEMORY-ROADMAP-2026.md`, `PRD-AGENT-EVOLUTION-2026.md` — prior memory work
