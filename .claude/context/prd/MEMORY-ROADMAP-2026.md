# PRD: Memory Roadmap — Post-Reliability Bundle (May 2026)

| Field | Value |
|---|---|
| Owner | Pavel |
| Status | partial-merge (#2 landed, #3 in flight, #4 deferred) |
| Scope | internal design notes for the agent-memory subsystem |

## 1. Context

After the reliability bundle landed (HITL gates, parsing safety, FTS5
search, sidecar polish), agent memory was the next ergonomic gap. Agents
read memory at boot, can't refresh mid-session, and the consolidator
ran on cron only — so an agent that learned something in session N
didn't reliably surface it in session N+1.

This roadmap is the three-PR plan that closes the loop, makes memory
agent-callable mid-session, and lays the research groundwork for
durable consolidation.

## 2. Goals

- Close the HITL feedback loop end-to-end (proposal → approve / reject → applied state visible)
- Workspace-tier memory wired into prompt assembly with its own budget slice
- Versioned memory rows with full audit trail (EU AI Act Article 14 alignment)
- Mid-session memory tools so the agent calls memory.read / write / search instead of relying on the boot snapshot
- Hybrid retrieval (BM25 + dense + RRF fusion) so the search surface scales past keyword-only
- Write-time verifier guarding against poisoned content reaching the model

## 3. Non-goals (explicit cuts)

- Public skill marketplace driven by memory promotion
- Multi-tenant SaaS hosted offering
- Cross-workspace memory bleed (each workspace is fully isolated)
- Auto-promote agent-suggested memory to crew-shared without operator review

## 4. Constraints

- Self-host runtime, no external services
- SQLite + FTS5 for storage and search; no separate vector DB
- File-first markdown for human-readable agent memory (operator can edit
  with their editor of choice; no proprietary blob format)
- Transparent storage: every value the agent reads is something the
  operator can read too

## 5. Roadmap overview

```
#4  Research-grade consolidation (six-signal scoring, sleep-time)     [deferred]
#3  Smart memory (mid-session tools + hybrid retrieval + verifier)    [in flight]
#2  Close the loop (HITL + workspace tier + versioning)               [merged]
```

## 6. PR #2 — Close-the-loop

### Scope items

#### 6.1 HITL HTTP API (Phase 6b)

`POST /api/v1/memory/proposals` to surface model-authored proposals,
`POST /api/v1/memory/proposals/{id}/decide` for operator approve/reject.
Decisions write back to the journal so the audit story is one read.

#### 6.2 Workspace tier wiring

Workspace memory gets its own 15% budget slice in `buildMemoryContext`
ordered after crew memory and before the agent tier remainder. When
no workspace provider is wired, the budget reclaims to the agent tier
dynamically so the existing two-tier behavior survives byte-for-byte.

#### 6.3 Versioned memory + audit (EU AI Act Art. 14)

Each memory row gets `version` + `previous_version_id` + `decided_at`
+ `decided_by` columns. Audit log endpoint is read-only; export is
markdown bundle.

### PR #2 verification

- New tests under `internal/memory/*_test.go` for HITL endpoints
- E2E spec for proposal → approve → applied state visible
- Audit log endpoint returns chronological proposal/decide history

## 7. PR #3 — Smart memory

### Scope items

#### 7.1 Memory tools mid-session (biggest single win)

`memory.read(scope)`, `memory.write(scope, content, mode)`,
`memory.search(query, scope)`, `memory.append_daily(content)`.
Dispatcher in `internal/memory/tools.go`; per-CLI wiring forwards the
schemas. See PRD-AGENT-EVOLUTION-2026.md §F1 for the production
implementation under PR-A.

#### 7.2 Hybrid retrieval in sidecar `/memory/search` (RRF fusion)

BM25 from FTS5 + dense embeddings + reciprocal rank fusion. Both
lists ranked, fused with RRF scoring, top-K returned. Operator picks
the K via search query parameter; default 10.

Sidecar caches embeddings per-row so a write-heavy session doesn't
re-embed unchanged content.

#### 7.3 Write-time verifier

Before persisting a model-authored proposal, run the same scanner
the read path uses (nine regex rules + invisible-unicode codepoints).
Hits quarantine the row and surface a journal warning so operator
review sees the alert without having to re-scan.

### PR #3 verification

- Mid-session tools land under `internal/memory/` with end-to-end tests
- Hybrid retrieval tested with known-relevance fixtures
- Verifier exercise with intentionally-poisoned content fixtures

## 8. PR #4 — Research-grade (deferred but tracked)

### 8.1 Sleep-time consolidator (six-signal scoring + thresholds)

Six signals (recency, frequency, recall, reference count, edit
streak, novelty) combine into a single score; threshold gates
promotion from session memory to crew memory. Sleep-time means the
job runs during idle windows rather than mid-session, so latency
impact on agent runs is zero.

Default weights and thresholds set by a small benchmark suite over
the existing journal history; revisit after one month of production
telemetry.

### 8.2 Memory → Skills bridge

When a memory entry has been recalled N+ times across distinct
sessions and includes an action pattern, propose it as a Skill via
the existing Skills handler. Operator approves; skill lands in the
catalog with attribution to the originating memory row.

### PR #4 verification

- Benchmark suite for six-signal scoring with known-relevance fixtures
- Memory → Skills bridge tested with synthetic recall patterns

## 9. Decision log

- **File-first markdown, not typed blocks**: operators can edit memory
  files in their own editor; no proprietary format. Transparency +
  self-host advantage.
- **Memory tools mid-session, not just prompt injection**: agents that
  call memory beat agents fed by it because retrieval can be query-
  shaped rather than pre-shaped at prompt assembly time.
- **HTTP API for HITL**, not Slack/email: keeps the substrate self-
  contained; operator can integrate to their channel of choice with a
  thin adapter.
- **Six-signal scoring with documented weights**: every weight is a
  configurable knob, not a magic number; default values come from
  the benchmark suite and are reproducible.

## 10. Open questions

- PR #3: should `memory.search` use FTS5 from day one or substring-only
  with FTS5 deferred? Current plan: substring MVP, FTS5 swap is a
  contained later change in PR-A.
- PR #4: aux-model slot for the consolidator is in PRD-AGENT-EVOLUTION
  PR-B as F3. Need to confirm the routing point won't conflict.

## 11. Sources

Internal:

- `internal/memory/` — dispatcher + scanner + cap protocol
- `internal/episodic/` — recall + importance scoring
- `internal/consolidate/` — sleep-time consolidator + lessons writer
- `internal/orchestrator/memory.go` — prompt-assembly tier budget
- `internal/sidecar/memory_*.go` — sidecar HTTP surface
- PR #211, #212 — initial memory uplift
- PR #356 — onboarding wizard end-to-end spec
- PRD-AGENT-EVOLUTION-2026.md — Agent Evolution Stack
- CREDENTIALS-VAULT.md — credential vault tier ladder
- QUEUE-MECHANISM-2026.md — queue mechanism design notes

## 12. Out-of-scope items kept for visibility

- **Promotion-to-public-marketplace** of operator-approved memory:
  bundled catalog only in Phase 1, marketplace deferred.
- **Cross-workspace promotion**: each workspace stays isolated; if
  operator wants to share patterns across workspaces, do it via the
  manifest layer (template exports), not memory promotion.
- **Vector-DB integration**: SQLite + FTS5 + small embedding column
  covers all current scale needs; separate vector DB is unnecessary
  complexity until proven otherwise.
