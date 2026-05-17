# PRD: Memory Roadmap — Post-Reliability Bundle (May 2026)

| Field | Value |
|---|---|
| Status | Draft — pending PR #1 merge |
| Owner | Pavel Srba |
| Created | 2026-05-16 |
| Depends on | `feat/memory-reliability-bundle` (8 commits, awaiting merge) |
| Supersedes | n/a (initial roadmap) |
| Companion files | `internal/memory/`, `internal/consolidate/`, `internal/episodic/`, `internal/sidecar/memory_write.go`, `scripts/verify-memory-bundle.sh` |

## 1. Context

**What just shipped (PR #1):**

The reliability bundle (`feat/memory-reliability-bundle`) closed Crewship's
write-path correctness gaps: atomic-replace + flock on consolidator writes,
scrubber-on-write at the sidecar boundary, per-file byte caps, fsnotify
watcher with mtime-poll fallback for Docker Desktop bind-mounts, FNV-64
pattern dedup against last 7 days of `learned-*.md`, pins.md readback into
the agent system prompt, journal nudge query fixed, and feature-flagged
HITL staging (`CREWSHIP_CONSOLIDATE_HITL=1`) that writes consolidator output
to `.proposed/proposal-*.md` plus `memory_proposals` DB row plus
`inbox_items` row plus distinct journal type. Migration v89 widens
`inbox_items.kind` CHECK, adds `memory_proposals` table, adds
`workspaces.memory_config` column.

Live-validated on dev2 (`/opt/crewship_2`): **16/16 contract assertions
PASS** across schema, journal, and sidecar HTTP surfaces. Verification
scripts at `scripts/verify-memory-bundle.sh` + `scripts/verify-sidecar-memory-write.sh`
make the contract reproducible against any running instance.

**What this roadmap closes:**

Live web research (May 2026) into Letta, Anthropic Memory Tool / Managed
Agents, Mem0, Zep / Graphiti, LangMem, Cognee, Microsoft Agent Framework,
Hermes Agent, OpenClaw Dreaming, and GitHub Copilot Memory showed three
gaps that are **measurably the difference between Crewship being reliable
and Crewship being competitive on memory quality benchmarks** (LoCoMo,
LongMemEval, MemoryArena ICLR 2026):

1. **Memory tools mid-session.** 2026 benchmark winners (Mem0 91.6 LoCoMo,
   Letta 74.0 with GPT-4o-mini, ByteRover) all expose memory ops as
   callable tools the agent invokes during reasoning. Crewship's current
   frozen system-prompt injection is the legacy pattern.

2. **Hybrid retrieval (FTS5 + embeddings via RRF).** `internal/episodic/`
   already has Ollama embeddings + BM25 RRF over `journal_entries`, but
   the sidecar `/memory/search` agent-facing surface uses only FTS5 over
   markdown chunks. The agent gets half the recall.

3. **Write-time verifier.** MINJA memory-poisoning attacks reach 95 %
   injection / 70 % ASR without defenses (arXiv 2601.05504, Jan 2026).
   GitHub Copilot Memory mitigates by verifying cited code locations
   before applying a memory. EU AI Act Art. 14 (Aug 2 2026) makes
   oversight non-optional for high-risk systems. Crewship's scrubber
   blocks credentials but not truth claims.

This PRD scopes how those three gaps land across three follow-up PRs.

## 2. Goals

Each goal has a verifier — the metric we measure to call the work done.

| # | Goal | Verifier |
|---|---|---|
| G1 | Agent calls memory tools mid-session rather than receiving a frozen snapshot | Per-session journal entries of type `tool.call` with `tool=memory_*` > 0 for ≥80 % of multi-turn sessions one week post-deploy |
| G2 | `/memory/search` returns hybrid (FTS + dense) results when an embedder is configured, falls back to FTS-only when not | recall@10 ≥ 1.15× current FTS-only baseline on a held-out journal-eval set |
| G3 | Write-time verifier rejects contradictions against pinned facts and stale citations before they land in canonical memory | 0 contradictions-against-pin in canonical `AGENT.md` / `CREW.md` / `learned-*.md` over 30 days of agent operation |
| G4 | Every memory write is recoverable to any of the last N versions | `crewship memory log <path>` + `crewship memory restore <path> <sha>` work end-to-end; storage growth bounded by configured retention |
| G5 | Consolidator scores rule candidates with the published six-signal rubric, exposes the breakdown via `promote-explain` | `GET /api/v1/consolidate/proposed/{id}/explain` returns per-signal contributions; reject rate on signal-scored proposals < auto-apply-mode reject rate |
| G6 | Long-stable learned rules promote to Anthropic-spec `SKILL.md` with HITL gate | Skills promoted from memory load via progressive disclosure (~30 tokens at startup, full body on trigger); 0 unreviewed auto-promotions |

## 3. Non-goals (explicit cuts)

- **Knowledge graph layer** (Zep / Graphiti / Cognee / Mem0ᵍ pattern).
  Requires an external service (Neo4j / Kuzu / Memgraph) which breaks
  the "single Go binary" thesis. Revisit only if (a) a non-coding-agent
  persona arrives, or (b) LongMemEval scores stall below 80 % after
  G2 lands.
- **Three-LLM-call write pipeline** (Mem0's pre-April-2026 mistake).
  We stay single-pass: ADD-only at write time, conflict resolution
  deferred to retrieval. Verifier is one call, not a chain.
- **Sleep-time compute as a separate goroutine.** We repurpose the
  existing 6h consolidator with scoring + thresholds, not a new
  scheduler primitive.
- **Memory-flush silent turn before compaction.** Dropped in PR #1
  per Plan-agent push-back (race with compactor, token cost, weak
  reliability win). Not re-litigated here.
- **Substring-match edit primitive** (Hermes pattern). Agents already
  have full filesystem write through the container; a second edit
  surface is not worth the API expansion.
- **Per-agent embedder model selection.** Workspace-level `memory_config`
  is the right scope; per-agent override is over-engineering until we
  see real demand.

## 4. Constraints

- **Single Go binary.** No new external services (Neo4j / Redis /
  pgvector). sqlite-vec extension is acceptable because it links into
  the existing modernc.org/sqlite path.
- **File-first transparency.** Every memory artifact stays inspectable
  with `cat`. No opaque binary blobs except the FTS5/vec indexes which
  are derivable from the markdown.
- **Apache-2.0 core.** No copyleft dependencies on the open-source
  path. Enterprise-tier features (if any) live under `/ee` per CRE-79.
- **Multi-CLI neutrality.** Every memory tool must be exposable to all
  six adapter CLIs (Claude Code / Codex / Cursor / Droid / Gemini /
  OpenCode) — no Claude-only patterns.
- **Container-per-agent isolation.** Verifier runs in the sidecar
  process inside the container, not in a host-level co-process.
- **EU AI Act Art. 14 (Aug 2 2026).** Versioned memory + audit trail
  is a compliance must-have, not an optional polish.

## 5. Roadmap overview

| PR | Title | Scope | Effort | Open ordering dependency |
|---|---|---|---|---|
| #2 | Close-the-loop | Phase 6b HITL HTTP API + Workspace tier wiring + Versioned memory | ~2 weeks | none (builds on PR #1) |
| #3 | Smart memory | Memory tools mid-session + Hybrid RRF in `/memory/search` + Write-time verifier | ~2–3 weeks | depends on PR #2 (workspace tier shape) |
| #4 | Research-grade | Sleep-time consolidator (6-signal scoring + thresholds + `promote-explain`) + Memory→Skills bridge | ~2 weeks | depends on PR #2 (`explain` endpoint surface) and PR #3 (verifier hook on skill promotion) |

Total: ~6–7 dev weeks. Each PR ships independently behind feature flags so
none of them blocks the others' merge.

---

## 6. PR #2 — Close-the-loop

**Goal:** finish what PR #1 staged — the HITL inbox flow has rows but no
UI/API to act on them; the workspace memory tier is built but never read;
and EU AI Act compliance needs versioned writes before Aug 2 2026.

### Scope items

#### 6.1 HITL HTTP API (Phase 6b)

Three new endpoints under `/api/v1/consolidate/proposed/{id}/`:

- `POST .../approve` — OWNER/ADMIN only. Atomically:
  1. Read `.proposed/proposal-{runID}.md`.
  2. Acquire flock on canonical `learned-YYYY-MM-DD.md` (date = current).
  3. Append the proposal body to canonical file (atomic-replace via
     temp + rename, same pattern as `memory.WriteFile`).
  4. Update `memory_proposals.status='approved'`, `decided_at`,
     `decided_by_user_id`.
  5. Resolve the inbox row via `inbox.ResolveBySource(kind='memory_consolidation', source_id=proposal_id, action='approved', user_id=...)`.
  6. Emit `journal.EntryMemoryConsolidated` (the canonical type, not
     the proposed one) so downstream readers count this as a live
     consolidation.

- `POST .../reject` — OWNER/ADMIN. `memory_proposals.status='rejected'`,
  inbox resolved with `action='rejected'`. The `.proposed/` file stays
  on disk for audit (deleted by a retention sweep after N days, not
  on-reject). Operators with `reason` query param have it stored in
  `payload.reason`.

- `GET .../explain` — returns per-rule evidence breakdown. For PR #2
  this just returns the raw rules + their evidence IDs from
  `memory_proposals.evidence_json`. For PR #4, the same endpoint
  upgrades to include per-signal scoring.

**Files touched:**
- `internal/api/consolidate_proposed_handler.go` (new) — three handlers
- `internal/api/router.go` — route registration + RBAC middleware
- `internal/consolidate/approve.go` (new) — `ApproveProposal(ctx, db, journal, proposalID, userID) error`
  that wraps steps 1–6 atomically
- `internal/inbox/writer.go` — `ResolveBySource` already exists, reuse

**Acceptance:**
- `POST .../approve` lands canonical file content + flips both DB rows
  + emits `EntryMemoryConsolidated`
- `POST .../reject` only flips DB rows + emits inbox resolution; no
  filesystem write
- Permission check enforced (403 for MEMBER+VIEWER)
- Idempotent on retry (second `/approve` is a no-op, returns 409 with
  current status)

#### 6.2 Workspace tier wiring

The `memory.WorkspaceMemory` primitive already exists at
`internal/memory/workspace.go` and is never called in production
(verified by the Phase 1 exploration agent). Plumb it through:

- `internal/server/server_lifecycle.go` instantiates one
  `*memory.WorkspaceMemory` per workspace at startup, keyed by
  `workspace_id` in a registry held by the orchestrator.
- `internal/orchestrator/orchestrator.go` `NewOrchestrator` signature
  gains a `WorkspaceMemoryProvider` interface (returns the right
  WorkspaceMemory for a given workspace_id).
- `internal/orchestrator/memory.go` `buildMemoryContext` inserts a new
  `[WORKSPACE MEMORY]` block between `[CREW SHARED MEMORY]` and
  `[PINS]`. Budget allocation: workspace 15 % cap of the post-pins
  remainder (pins are 10 %, crew 40 %, workspace 15 %, agent gets the
  rest with dynamic reclaim).
- The block content comes from
  `WorkspaceMemory.GetContext(workspaceMemoryBudget)`.

**Acceptance:**
- Workspace memory file written by hand at
  `~/.crewship/memory/{workspace-id}/notes.md` appears in the next
  agent session's prompt under `[WORKSPACE MEMORY]`
- Existing two-tier tests (`memory_test.go`) updated for the new
  block ordering
- Empty workspace tier reclaims its budget for the agent tier
  (the dynamic-reclaim path already shipped in PR #1)

#### 6.3 Versioned memory + audit (EU AI Act Art. 14)

Every successful write via `memory.WriteFile` records an append-only row
to a new `memory_versions` table (DB migration v90).

Schema:
```sql
CREATE TABLE memory_versions (
    id           TEXT PRIMARY KEY,           -- 'mv_' + content sha truncated
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    path         TEXT NOT NULL,              -- container-relative or workspace-relative
    tier         TEXT NOT NULL CHECK (tier IN ('agent','crew','workspace','pins','learned')),
    sha256       TEXT NOT NULL,              -- content hash
    bytes        INTEGER NOT NULL,
    written_at   TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    written_by   TEXT,                       -- agent_id, system, or user_id
    parent_sha   TEXT,                       -- previous version sha at same path
    payload_ref  TEXT NOT NULL                -- on-disk blob path: ~/.crewship/memory/versions/<sha256[:2]>/<sha256>
);
CREATE INDEX idx_memory_versions_path_ts ON memory_versions (path, written_at DESC);
```

Storage: content blobs live on disk under
`~/.crewship/memory/versions/<sha256[:2]>/<sha256>` (content-addressed,
dedupes automatic). Default retention: 30 days, configurable via
`workspace_settings.memory_config.versions_retention_days`. Daily
sweep runs in the existing `consolidate.runCompactionLoop`.

CLI:
- `crewship memory log <path>` — list versions newest-first
- `crewship memory show <path> <sha>` — print historical content
- `crewship memory restore <path> <sha>` — atomic-replace current
  with historical version (OWNER/ADMIN only via CLI auth check)

API mirrors:
- `GET /api/v1/memory/{tier}/{path:.+}/versions`
- `GET /api/v1/memory/{tier}/{path:.+}/versions/{sha}` (returns content)
- `POST /api/v1/memory/{tier}/{path:.+}/versions/{sha}/restore`

**Files touched:**
- `internal/memory/writer.go` — emit version record on every
  successful write (after rename, before return)
- `internal/database/migrate_consts_v90_memory_versions.go` (new)
- `internal/memory/versions.go` (new) — Log, Show, Restore helpers
- `cmd/crewship/cmd_memory.go` (new) — CLI subcommand
- `internal/api/memory_versions_handler.go` (new)

**Acceptance:**
- Every `memory.WriteFile` adds a `memory_versions` row + blob on disk
- Two writes of identical content (same sha) reuse the blob (storage dedupe)
- Restore atomically replaces canonical with historical content, emits
  a new version row pointing at the restored sha as the new latest
- 30-day retention sweep removes versions older than the cutoff,
  except the most recent 3 per path (per the Anthropic Managed Agents
  pattern of "always keep last N regardless of age")

### PR #2 verification

Extend `scripts/verify-memory-bundle.sh`:
- Assert `memory_versions` table exists with the 9 columns
- Insert a row, retrieve via API, restore via API, assert canonical
  content matches restored sha
- Approve a proposal end-to-end (with a real `.proposed/` file)
- List workspace memory file content appears in a fresh agent's
  system prompt (this needs a sidecar smoke test extension)

---

## 7. PR #3 — Smart memory

**Goal:** lift Crewship from "reliable + governed" to "measurably
competitive on the benchmarks the field actually uses". This is the
biggest accuracy delta in the whole roadmap.

### Scope items

#### 7.1 Memory tools mid-session (biggest single win)

Today: `buildMemoryContext` produces frozen `[AGENT MEMORY] / [CREW
SHARED MEMORY] / [WORKSPACE MEMORY] / [PINS]` blocks injected once at
session start. Mid-session, the agent has no way to look up a fact
that's not in the budget-truncated snapshot.

Target: the agent receives a **short boot snapshot** (top-K most relevant
entries from each tier, ~1 500 chars total) plus a **set of callable
tools** for on-demand lookup:

- `memory_search(query: string, scope: 'agent'|'crew'|'workspace'|'all', limit?: int)` → ranked hits (hybrid in PR #3.2)
- `memory_read(path: string)` → full content of a memory file (path
  validated under the agent's accessible tier base)
- `memory_write(file: string, content: string)` → the existing sidecar
  `/memory/write` endpoint, surfaced as a tool

Tool schemas mounted per CLI adapter:
- **Claude Code** (`adapter_claude.go`) — native tools via `tool_use`
  blocks
- **Codex** (`adapter_codex.go`) — function-calling JSON schema
- **Cursor** (`adapter_cursor.go`) — MCP-style tool definitions
- **Droid** (`adapter_droid.go`) — TBD per Droid's tool API
- **Gemini** (`adapter_gemini.go`) — function declarations
- **OpenCode** (`adapter_opencode.go`) — TBD per OpenCode's tool API

The HTTP surface lives in the sidecar (already there for `memory_write`,
extends to `memory_search` + `memory_read`). Each CLI adapter has a
thin tool-call → HTTP shim.

**Guardrails:**
- Per-session circuit breaker: ≤ N tool calls per minute (default 20)
- Budget tracking: tool-call response counted against the session token
  budget; if a single response would exceed remaining budget, the
  search returns truncated results with a `truncated=true` flag
- `MemoryEnabled=false` continues to suppress the tools entirely

**Files touched:**
- `internal/sidecar/memory_read.go` (new) — `GET /memory/read?path=...&scope=...`
- `internal/sidecar/memory_search.go` — extend existing handler to
  accept `scope='all'` and return per-tier results
- `internal/orchestrator/adapter_*.go` (each adapter) — register the
  three tools in the adapter's tool list when `MemoryEnabled=true`
- `internal/orchestrator/memory.go` `buildMemoryContext` — switch
  from full-tier dumps to top-K boot snapshot + memory-tool
  instructions block
- `internal/orchestrator/orchestrator_run.go` — rate-limit middleware
  for the tool-call shim

**Acceptance:**
- Agent in a Claude Code session calls `memory_search("Outlands custom thievery")` mid-response and the result is in the conversation context
- Per-session tool-call count + p95 latency surface in metrics
- Boot snapshot ≤ 2 KB; full tier content reachable only via tool
- Disabled cleanly when `MemoryEnabled=false`

**Risk + measurement:**
- Token-cost regression from tool-call overhead → measure
  avg-tokens-per-session before / after on a held-out task set
- Agents over-calling memory → measure avg tool calls / session, alert
  if > 95th percentile of baseline
- CLI adapter regressions → existing `parser_*_test.go` files exercise
  tool-call parsing; extend with memory-tool fixtures

#### 7.2 Hybrid retrieval in sidecar `/memory/search` (RRF fusion)

Today: `internal/memory/Engine.Search()` is FTS5 BM25 over markdown
chunks. `internal/episodic/HybridRecall()` is BM25 + dense (Ollama
embeddings via nomic-embed-text) + RRF over `journal_entries`. Sidecar
search hits only the first.

Target: a single `SidecarSearch(query, scope, limit)` that fans out to
both engines, returns RRF-fused results with source tags so the agent
can distinguish "markdown memory hit" from "journal entry hit".

**Files touched:**
- `internal/memory/hybrid.go` (new) — `Search(ctx, engine, embedder,
  episodicDB, query, scope, limit)` that fans out + RRF-merges
- `internal/sidecar/memory.go` — `handleMemorySearch` calls the new
  hybrid surface when `embedder != nil`, falls back to FTS-only when
  episodic dependencies are absent
- Sidecar `ServerConfig` gains an optional `EpisodicDB *sql.DB` +
  `Embedder episodic.Embedder` so the sidecar can call `episodic.HybridRecall`
  for the journal half
- RRF constant: `k=60` (literature standard)

**Acceptance:**
- Agent search for a paraphrased concept (e.g. "thieving in
  Outlands") that doesn't lexically match the indexed markdown still
  retrieves the journal entry that contains the dense-vector neighbor
- recall@10 ≥ 1.15× FTS-only baseline on a held-out evaluation set
  (need to assemble this set — see §10 open questions)
- Latency budget: p95 < 100 ms hybrid (FTS ~5 ms + dense ~50 ms + RRF
  ~5 ms + transport overhead)

**Trap to avoid:** below ~30 K entries the embedding lift is < 5 % and
adds setup cost. Default `embedder != nil` only when `episodic`
package is wired with > N entries AND embedder is configured. Below
that, the legacy FTS-only path is correct.

#### 7.3 Write-time verifier

Before `memory.WriteFile` returns success, run a verifier pass:

1. **Pin-contradiction check** — load all pins from the workspace's
   `pins.md`; for each pin, compute its key facts (LLM extraction or
   regex over markdown structure) and check the candidate write
   against them. If the write contradicts a pin (e.g. write says
   "economy resets daily" while pin says "economy NEVER resets") →
   reject with `verifier_contradiction` kind.

2. **Citation-staleness check** (only when content contains markdown
   citations like `file:line` or `[ref:agent_id/journal_entry_id]`):
   - For `file:line` citations, verify the file exists in the
     accessible filesystem path (container or workspace).
   - For journal-entry refs, verify the entry exists and was not
     deleted.
   - On stale citation → reject with `verifier_stale_citation` kind.

3. **Optional LLM verifier** (controlled by
   `workspace_settings.memory_config.verifier_mode = 'off|cheap|llm'`):
   - `off` (default) — skip the LLM pass entirely
   - `cheap` — only run regex / structural checks above
   - `llm` — additionally call a cheap model (default Ollama-backed
     phi3:mini or claude-haiku) with the candidate write + the top-K
     hybrid-search results and ask "does this contradict prior
     stable memory?" Receive a yes/no + reason.

**Verifier rejections share the existing 422 envelope shape:**
```json
{
  "rejected": true,
  "kind": "verifier_contradiction" | "verifier_stale_citation",
  "detail": {
    "conflicting_pin_id": "j_42",
    "conflicting_text": "economy NEVER resets",
    "stale_reference": "src/outlands/economy.go:142",
    "verifier_reason": "..."
  }
}
```

A new journal type `EntryMemoryWriteVerifierBlocked` (sibling of the
existing `EntryMemoryWriteRejected`) makes the failure trail
distinguishable from scrubber / cap rejections.

**Files touched:**
- `internal/memory/verifier.go` (new) — `Verify(ctx, candidate,
  workspaceCtx, mode) VerifierResult`
- `internal/memory/writer.go` — call `Verify` between scrubber and
  flock; on `Decision=Reject` return a structured `WriteResult` with
  the new envelope
- `internal/journal/types.go` — add
  `EntryMemoryWriteVerifierBlocked = "memory.write_verifier_blocked"`
- `internal/sidecar/memory_write.go` — surface 422 with the verifier
  envelope when present
- `internal/database/migrate_consts_v91_verifier_audit.go` (new) —
  add `verifier_mode` to the `workspace_settings.memory_config`
  schema (JSON column, no DDL change required, but document the
  shape)

**Acceptance:**
- A write that contradicts a pinned fact is rejected with the new
  envelope + journal entry
- A write with a stale `file:line` citation is rejected
- Verifier mode `off` skips all checks (no regression baseline)
- LLM mode adds latency budget < 500 ms per write (measure separately
  from the regex pass)

**Trap to avoid:** Mem0's pre-April-2026 mistake was three sequential
LLM calls per write — single-pass with deferred conflict resolution
at retrieval is documented as 3-4× cheaper. We follow that pattern:
one verifier call per write, not three.

### PR #3 verification

Extend `scripts/verify-sidecar-memory-write.sh`:
- Insert a pinned fact, write a contradicting candidate, assert 422
  with `kind=verifier_contradiction`
- Write a citation to a non-existent file, assert 422 with
  `kind=verifier_stale_citation`
- Search for a paraphrase that misses FTS exactly but hits via
  dense, assert hit in the result set
- Tool-call shim: spawn an in-process Claude Code adapter, send a
  conversation that should trigger `memory_search` tool, verify it
  fires

---

## 8. PR #4 — Research-grade (deferred but tracked)

**Goal:** measurable benchmark gains via sleep-time-compute pattern;
close the memory → skills loop.

**Implementation status (2026-05-17):** §8.1 and §8.2 below are landed
on branch `feat/memory-pr4-research-grade` (seven commits, full repo
build green, race-clean). Commit map:

| Step | Commit | Surface |
|------|--------|---------|
| 1 | `6e8a3187` | `internal/consolidate/scoring.go` — six-signal computer (OpenClaw weights) |
| 2 | `584138c7` | Migration v91 `score_json` column + `writeProposal` population |
| 3 | `3a48f8c1` | `GET /api/v1/consolidate/proposed/{id}/explain` surfaces `score_json` |
| 4 | `55137823` | `internal/consolidate/post_run_trigger.go` — debounced sleep-time trigger |
| 5 | `f3f82c23` | `internal/consolidate/skill_promote.go` — memory→Skills bridge primitive |
| 6 | `e2aff97c` | Bridge wired into `writeProposal` (non-fatal hook) |
| 7 | `b0b35d3c` | `GET/POST /api/v1/skills/proposed[/approve|/reject]` HITL HTTP surface |

The four-step roadmap (PR #1 reliability → PR #2 close-the-loop → PR #3
smart memory → PR #4 research-grade) is therefore feature-complete in
the working tree; remaining gaps are wiring of the post-run trigger
into `internal/api/internal_runs.go` where `run.completed` is emitted,
and the journal-side population of `RecallCount` / `UniqueQueries`
counters so the Skill-promotion gate fires in the steady state rather
than only on hand-supplied score maps. Both are tracked as follow-ups
in this commit's tail comment.

### 8.1 Sleep-time consolidator (six-signal scoring + thresholds)

OpenClaw's publicly-specified algorithm (the only non-marketing one in
this space):

| Signal | Weight |
|---|---|
| Relevance | 0.30 |
| Frequency | 0.24 |
| Query diversity | 0.15 |
| Recency | 0.15 |
| Consolidation | 0.10 |
| Conceptual richness | 0.06 |

Promotion thresholds:
- minScore ≥ 0.80
- minRecallCount ≥ 3 (rule recalled in ≥ 3 distinct retrievals)
- minUniqueQueries ≥ 3 (matched ≥ 3 distinct user queries)

**Score storage:** new column `memory_proposals.score_json` (JSON blob
with per-signal contributions) so the `explain` endpoint can return
the breakdown. Computed at proposal-creation time.

**Trigger:** in addition to the current 6 h cron, fire after each
agent-run completion (debounced: at most one consolidation pass per
crew per 30 min). This is the "sleep-time" trigger — agent finishes,
consolidator wakes, scores candidates, stages the survivors. The
6 h cron remains as a safety net.

**Files touched (as landed):**
- `internal/consolidate/scoring.go` (new) — `ComputeScore` + `DefaultSignalWeights` + `DefaultThresholds`
- `internal/consolidate/proposed.go` — `computeProposalScores` populates `score_json` in `writeProposal`
- `internal/consolidate/post_run_trigger.go` (new) — `PostRunTrigger.OnRunCompleted` with per-(workspace, crew) debounce (default 30 min). **Follow-up:** wiring into `internal/api/internal_runs.go` where `EntryRunCompleted` is emitted.
- `internal/api/consolidate_proposed_handler.go` `GET .../explain` — surfaces `score_json` via `ProposalExplanation.Scores`
- `internal/database/migrate_consts_v91_proposal_scoring.go` (renumbered from v92) — `ALTER TABLE memory_proposals ADD COLUMN score_json TEXT NOT NULL DEFAULT '{}'`

**Acceptance status:**
- ✅ Proposal with score 0.65 + recall_count 2 stays out of canonical memory — the gate is `Composite ≥ 0.80 ∧ RecallCount ≥ 3 ∧ UniqueQueries ≥ 3` (`PromotionThresholds` in `scoring.go`). First-time proposals always have `RecallCount = 0` and therefore can't promote regardless of LLM confidence; covered by `TestProposalMode_PopulatesScoreJSON`.
- ✅ `explain` endpoint returns per-signal contributions — `ProposalExplanation.Scores` carries the raw JSON; tested by `TestExplainProposal_ReturnsScores`.
- ⏳ Post-run trigger fires within 30 s of agent run completion — the standalone primitive ships in commit `55137823` with six tests (fakeClock + countingSummarizer). The caller-side wire into `internal_runs.go` is the next follow-up commit; the trigger contract is locked so the wire is purely "look up the trigger, call `OnRunCompleted`".

### 8.2 Memory → Skills bridge

When a rule in `learned-*.md` accumulates recall_count ≥ 10 across
distinct sessions, the consolidator proposes a `SKILL.md` (Anthropic
open-standard YAML frontmatter + markdown body) under `.proposed/`.

Progressive disclosure semantics:
- The skill's `description` field (≤ 200 chars) loads at agent
  startup as part of the skill index (~30 tokens)
- The skill body loads only when triggered by a tool call or by the
  agent invoking `skill_view(name)`

Reuses `internal/skills/parser.go` (already in the codebase) for
the YAML parse and structure validation.

**Promotion gate (landed thresholds):** stricter than the
learned-rule promotion gate by design — a learned-rule mistake is a
chat-history footnote, a Skill mistake is a behaviour the LLM may
pattern-match against for weeks.

| Gate | Learned rules (`learned-*.md`) | Skills (`SKILL.md`) |
|------|--------------------------------|---------------------|
| `Composite` | ≥ 0.80 | ≥ 0.85 |
| `RecallCount` | ≥ 3 | ≥ 10 |
| `UniqueQueries` | ≥ 3 | — (covered by recall) |
| Source | LLM extraction | Already-promoted learned rule that re-cleared recall floor |

**Files touched (as landed):**
- `internal/consolidate/skill_promote.go` (new) — `PromoteRuleToSkill`, `PromoteEligibleRules`, `renderSkillMarkdown`, `safeStagingFileName`-style slug disambiguation. Frontmatter is engineered to pass `skills.ParseSKILLMD` + `skills.LintDescription` with zero warnings (`category: CUSTOM`, `runtime: INSTRUCTIONS`, `maturity: EXPERIMENTAL`, `author: crewship-consolidator`, tags `[auto-promoted, memory-derived]`).
- `internal/consolidate/proposed.go` — `promoteProposalSkills` hook runs after the DB insert; logged-not-fatal on partial failures so a Skill that fails to stage never invalidates the underlying proposal.
- `internal/journal/types.go` — three new entry types: `memory.skill_proposed`, `memory.skill_approved`, `memory.skill_rejected`.
- `internal/api/skills_proposed_handler.go` (new) — HITL HTTP surface (`List`/`Approve`/`Reject`). Stateless against the DB (disk is truth); audit via journal entries. OWNER/ADMIN/MANAGER gating matches `canRole("create")`.

**Acceptance status:**
- ✅ A rule that crosses recall ≥ 10 surfaces a `.proposed/skill-{slug}.md` proposal in the next consolidator run — wired in `writeProposal` (`e2aff97c`); the post-run trigger (`55137823`) is the steady-state caller.
- ✅ Skill description ≤ 400 chars; body parses cleanly under `skills.ParseSKILLMD` and the description passes `skills.LintDescription` — tested by `TestPromoteRuleToSkill_WritesParseableSKILLMD` and `TestPromoteRuleToSkill_DescriptionPassesLinter`.
- ✅ HITL approval works the same way as memory proposals — endpoints `GET /api/v1/skills/proposed`, `POST .../approve`, `POST .../reject` (`b0b35d3c`). Approve runs through `skills.Importer.Import` so the same SPDX/scrubber gates apply as for URL-imported skills.

### PR #4 verification

Extend the integration scripts with:
- A scored proposal that fails the threshold is auto-rejected
  (not even staged)
- `explain` endpoint returns per-signal numbers
- 10 simulated recalls of the same pattern trigger a skill proposal

---

## 9. Decision log

Decisions made up-front to avoid re-litigation:

| Decision | Why | Source |
|---|---|---|
| Skip knowledge graph layer | Breaks "single Go binary" thesis; KG buys 15 pts on LongMemEval temporal recall but Crewship's coding-agent audience derives temporal truth from the code itself, not from memory | atlan.com/know/best-ai-agent-memory-frameworks-2026, Cognee vectors+graphs playbook |
| Single-pass verifier, not three | Mem0's pre-April-2026 three-LLM-call pattern was 3-4× more expensive than necessary | Mem0 token-optimization playbook 2026 |
| Six-signal scoring with OpenClaw weights | The only publicly-specified scoring algorithm in this space — adopt with attribution rather than invent | docs.openclaw.ai/concepts/dreaming |
| File-first markdown, not typed blocks | Convergent with Anthropic Memory Tool, Hermes, GitHub Copilot Memory; transparency + self-host advantage | platform.claude.com docs, github.blog/agentic-memory |
| Memory tools mid-session (not just prompt injection) | "Agents that call memory beat agents fed by it" — Mem0 91.6 LoCoMo, Letta 74.0, ByteRover all use tool-call retrieval | mem0.ai/blog/state-of-ai-agent-memory-2026 |
| Versioned memory by PR #2 | EU AI Act Art. 14 enforcement begins Aug 2 2026 — non-optional for "high-risk" AI systems | strata.io HITL guide; Anthropic Managed Agents docs |
| Defer Phase 6b memory-flush silent turn | Race vs. compactor + token cost > weak reliability win | Plan-agent review of PR #1 |

## 10. Open questions

Tracked here for decision before the relevant PR starts:

1. **Hybrid search evaluation set.** PR #3 G2 needs recall@10 baselines.
   We don't have a held-out journal-eval set yet — what's the synthetic
   corpus we use? Suggest: replay last 30 days of dev2 journal entries +
   hand-authored 50 paraphrased queries from real agent transcripts.
2. **Tool-call rate limits per CLI adapter.** Default 20/min seems
   reasonable for Claude Code but may be wrong for an agent in a
   tight loop (e.g. Codex doing 50 file edits in a session). Open
   for measurement.
3. **Workspace memory write path.** PR #2 wires the read; who writes
   to workspace memory? Lead/Coordinator agents only? Operator-only?
   Need decision before PR #2 design freeze.
4. **Verifier LLM model selection.** Cheap model = phi3:mini via
   Ollama (already wired) or claude-haiku via the existing
   /llm middleware? Per-workspace selectable?
5. **Skill promotion threshold (10 recalls).** OpenClaw uses 3 unique
   queries; Hermes uses task complexity. 10 distinct sessions is a
   guess — needs measurement on real usage.

## 11. Sources

All claims grounded in live web research (May 2026). Items marked
`[older]` are >12 months old. Items marked `[unverified]` rely on a
single source.

- Letta architecture + sleep-time agents — docs.letta.com/guides/agents/architectures/sleeptime/, docs.letta.com/guides/core-concepts/memory/shared-memory, letta.com/blog/sleep-time-compute, letta.com/blog/benchmarking-ai-agent-memory
- Anthropic Memory Tool + Managed Agents memory + Context Management — platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool, platform.claude.com/docs/en/managed-agents/memory, anthropic.com/news/context-management
- Anthropic Agent Skills (open standard, Dec 18 2025) — platform.claude.com/docs/en/agents-and-tools/agent-skills/overview, anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills
- Mem0 research + token-efficient algorithm — mem0.ai/research-3, mem0.ai/blog/state-of-ai-agent-memory-2026, arxiv.org/abs/2504.19413
- Zep / Graphiti — github.com/getzep/graphiti, arxiv.org/abs/2501.13956 `[older]`, neo4j.com/blog/developer/graphiti-knowledge-graph-memory/
- LangGraph BaseStore + LangMem — docs.langchain.com/oss/python/langgraph/memory, langchain-ai.github.io/langmem/
- Cognee architecture — cognee.ai/blog/fundamentals/how-cognee-builds-ai-memory
- Microsoft Agent Framework memory + checkpointing — learn.microsoft.com/en-us/agent-framework/get-started/memory, learn.microsoft.com/en-us/agent-framework/tutorials/workflows/checkpointing-and-resuming
- Hermes Agent — github.com/nousresearch/hermes-agent, github.com/NousResearch/hermes-agent-self-evolution
- OpenClaw Dreaming six-signal scoring — docs.openclaw.ai/concepts/dreaming, clawtask.app/news/dreaming-in-openclaw-latest-version-2026-4-5
- GitHub Copilot Memory — github.blog/ai-and-ml/github-copilot/building-an-agentic-memory-system-for-github-copilot/, docs.github.com/en/copilot/concepts/agents/copilot-memory
- Sleep-time Compute paper — arxiv.org/abs/2504.13171 `[older]`
- A-MEM (NeurIPS 2025) — arxiv.org/abs/2502.12110 `[older]`
- MINJA memory-poisoning attack — arxiv.org/abs/2601.05504
- A-MEMGUARD defense — arxiv.org/pdf/2510.02373 `[older]`
- Survey: Memory Security in LLM Agents — arxiv.org/html/2604.16548v1
- EU AI Act Art. 14 HITL — strata.io/blog/agentic-identity/practicing-the-human-in-the-loop/
- RRF k=60 hybrid retrieval — glaforge.dev/posts/2026/02/10/advanced-rag-understanding-reciprocal-rank-fusion-in-hybrid-search/

## 12. Out-of-scope items kept for visibility

Items considered but NOT scheduled — re-evaluate quarterly:

- **MemPalace / ByteRover / Supermemory benchmark claims.** Vendor-only
  numbers, not independently reproduced. Watch for replication studies.
- **Substring-match edit primitive** (Hermes pattern). Agents have
  filesystem write already; second edit surface is over-engineering.
- **Per-agent embedder model selection.** Workspace-level config is
  the right scope until demand exceeds.
- **MCP server for memory.** Exposing Crewship memory as MCP to other
  agents is interesting but the strategic call belongs in the MCP
  Gateway PRD, not this one.
- **`§` entry delimiter** in markdown (Hermes). Cosmetic; no
  functional win.
- **Active Memory blocking pre-turn sub-agent** (OpenClaw). For
  coding agents this adds latency without measurable accuracy gain.
