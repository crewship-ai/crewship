# PRD — Agent Evolution: Learning, Governance, Identity & Lifecycle

**Status:** v3 + decision log §10 (the v4 retro-fit revert).
This PRD remains the **statement of intent**, not a status report.
The earlier "v4 — all 7 stack PRs merged" revision (commit `425cadf6`,
2026-05-21) was reverted because it bent the spec to match what the
code actually shipped — F1 search, F5 schema, F4 routing, and the
CLI grammar were all silently aligned with shipped behavior in the
v4 pass, which obscured drift instead of recording it. The §10
decision log enumerates every drift between the originally stated
behavior and what shipped, marked DRIFT / DEFERRED / PLACEHOLDER so a
future reader (or auditor) can see exactly where intent and code
diverged and why. Ship state of each PR is tracked in the PR
descriptions on GitHub (PRs #458, #466, #461, #468, #470, #469, #471
plus the in-flight PR-G/PR-F roll-up at #472), not here.
**Owner:** Pavel
**Scope:** internal design notes — see implementation PRs PR-Z / PR-A / PR-B / PR-C / PR-D / PR-E for the shipping artifacts

## 1. Strategic positioning

Crewship runs AI coding agents on the operator's own hardware with the
operator's own keys. Today the runtime is solid (multi-CLI adapters,
sidecar credential isolation, RBAC, manifest-driven declarative deploys)
but four ergonomic axes lag the substrate they sit on:

1. Agent memory ergonomics — agents read memory at boot, can't refresh mid-session
2. Skill self-authorship — agents can't propose new skills under operator review
3. Behavior governance — Keeper handles `read` / `execute` decisions but not skill review, behavior trimming, or memory health
4. Dynamic team scaling — agent roster is static; ephemeral / contractor agents need a lifecycle

This PRD unifies the response into a layered stack so the six features
ship as one coherent product story, not seven scattered patches.

## 2. Goals & non-goals

### Goals

- Native function-calling memory tools on every supported CLI adapter (F1)
- Per-crew autonomy slider that maps to concrete behavioral knobs (F2)
- Auxiliary model slot for memory-related work that doesn't need the lead model (F3)
- Keeper Phase 2: skill review, behavior trimming, memory health, negative learning (F4)
- Ephemeral agent lifecycle (hire / rehire / ghost state) (F5)
- PERSONA.md identity surface + per-user peer cards + GDPR primitives (F6)

### Non-goals (Phase 1)

- Public skill marketplace — bundled + signed catalog only
- Multi-tenant SaaS hosted offering — self-host only
- Auto-promotion of agent-proposed memory to crew-shared without review
- Cross-workspace memory sharing
- Real-time memory tools mid-session for adapters that don't support function calling

## 3. Architecture overview

Six layers, each independently mergeable. Earlier layers do not depend
on later ones; later layers consume earlier primitives.

```text
F6  PERSONA.md + peer cards + GDPR        ← identity layer
F5  Ephemeral agent lifecycle              ← roster layer
F4  Keeper Phase 2 (4 new request types)   ← governance layer
F3  Auxiliary model slot                   ← economics layer
F2  Autonomy slider                        ← behavior layer
F1  Native memory tools (6 adapters)       ← memory layer
─── PR-Z hard reset (cleanup) ─────────────
```

## 4. PR-Z — hard reset (pre-launch cleanup)

Originally drafted as eight items; Z.6 was voided during implementation
as an audit error (see Z.6 entry below), so seven items actually land.
PR-Z merges before PR-A so the new feature work sits on a clean
foundation instead of layering over old workarounds.

### Z.1 — Delete curl-based memory instructions

22-line `[MEMORY TOOLS]` block in the system prompt that taught agents
to curl the memory HTTP API. PR-A replaces it with native function-
calling tools. Removing the block early avoids parallel paths.

### Z.2 — Remove `phi3:mini` Keeper fallback

`gatekeeper.go` silently fell back to `phi3:mini` when `keeper.model`
was empty. Loud config validation now rejects `keeper.enabled=true`
with no model. No silent defaults.

### Z.3 — Deprecate `agents.system_prompt` column (lighter scope)

Mark deprecated via Go doc + DB schema comment. Full rename deferred
to PR-E where the PERSONA migration will touch the same call sites.

### Z.4 — Fix ESCALATE → Inbox silent gap

Keeper `ESCALATE` decisions previously logged to journal only. Now
persist to `inbox_items` as blocking, high-priority rows so operators
get an actionable surface.

### Z.5 — Remove dead `TaskContext` field

`EvalRequest.TaskContext` had no callers. Dropped along with the
prompt assembly that read it. Keeper evaluates the requesting agent's
conversation history only.

### Z.6 — *(voided)*

Original audit assumed Crewship had a `_pinned_guard` config flag.
Verification: that flag belongs to a different system; Crewship has
13 bundled skills protected via DB columns + RBAC instead. Item voided
during implementation with a lesson captured about verifying audit
citations against actual paths.

### Z.7 — Unified `lessons.md` writer

New `internal/consolidate/lesson_writer.go` ships now (so PR-C can
import a stable primitive). Consumer wiring deferred to PR-C where
F4.4 lands as the first real consumer.

### Z.8 — Remove Anthropic `memory_20250818` shim placeholder

Doc-only tombstone in `internal/memory/doc.go` locks in the no-shim
decision for the upstream Anthropic memory tool.

## 5. Feature specifications

### F1 — Native memory tools (Layer 1)

#### Problem
Agents read memory only at boot. Mid-session updates require the
operator to manually restart the agent or re-issue a system-prompt
refresh.

#### Solution
Adapter-agnostic core in `internal/memory/` exposing four tools as
function-calling schemas:

- `memory.read(scope)` — read AGENT / CREW / PERSONA / pins / daily / peers / lessons
- `memory.write(scope, content, mode)` — write or replace
- `memory.search(query, scope)` — substring-only (FTS5 deferred; see §7 open question)
- `memory.append_daily(content)` — daily journal append

Per-CLI wiring forwards the schemas — one MCP descriptor for CLAUDE_CODE,
one `--functions` JSON for CODEX_CLI, function declarations for GEMINI,
provider pass-through for OPENCODE, cursor adapter API for CURSOR_CLI,
droid manifest for FACTORY_DROID.

#### Cap protocol

Single source of truth in `internal/memory/tools.go`:

| Scope    | Cap (B) | File |
|----------|---------|------|
| AGENT    | 4 000   | AGENT.md |
| CREW     | 4 000   | CREW.md (under crew shared dir) |
| PERSONA  | 1 500   | PERSONA.md (F6) |
| pins     | 8 000   | pins.md |
| daily    | 30 000  | daily/YYYY-MM-DD.md |
| peers    | 1 500   | peers/{slug}.md (F6) |
| lessons  | —       | lessons.md (writer manages caps) |

Behavior:

- **Soft warning at 80%**: write succeeds; result appends "warning:
  approaching cap (N of M bytes, P%)". Lets the model preemptively
  consolidate.
- **Hard error at 100%**: append rejected with structured error
  including current size, projected size, cap. Model self-prunes
  (drop entries, summarize, use `mode='replace'`) and retries.
- **`mode='replace'`** is always permitted up to cap so the agent can
  reorganize freely without tripping cap on a shrinking edit.

#### Inbound prompt injection scan

Memory files are normally agent-authored, but external ingestion paths
(operator manual edits, crew-shared CREW.md via PR, future skill
imports, peer card content from past sessions) can land poisoned
content. The READ path runs a scanner: nine regex rules across
prompt-injection / exfiltration / persistence categories plus fourteen
invisible-unicode codepoints.

Hit → quarantine to `.quarantine/{sha256}.md` + placeholder substitution.
Original content never returned to the model. Idempotent (same content
reuses the sha-keyed file). Fail-closed (quarantine write failure
returns `IsError=true` rather than poisoned content).

#### Daily cap 100k → 30k breaking change

The legacy sidecar `dailyCap` is lowered to match the dispatcher's
`capDailyBytes`. Single source of truth lives in `tools.go`; sidecar
mirrors until the legacy HTTP path is retired in a follow-up.

### F2 — Per-crew autonomy slider (Layer 2)

#### Problem
Today every crew operates with the same Keeper gating. Some crews
should run lightly (research / discovery); others should require
review on every write (production / customer-facing).

#### Solution
`crews.autonomy_level` integer 0..3:

- 0 = full Keeper review on read + execute + memory write
- 1 = Keeper review on execute + memory write (default)
- 2 = Keeper review on execute only
- 3 = full autonomy (Keeper still logs decisions but doesn't block)

Stored on `crews`; read at request time via the existing
`resolveAgentConfig` path. CLI: `crewship crew update <slug>
--autonomy 2`. UI: slider in the crew detail panel.

### F3 — Auxiliary model slot (Layer 2)

#### Problem
Memory consolidation, skill review, and behavior trimming don't need
the lead model. Running Claude Sonnet 4.6 for "should this lesson get
promoted to crew-shared?" is wasteful both in cost and latency.

#### Solution — MVP: Haiku only

`crews.auxiliary_model_provider` + `crews.auxiliary_model` fields.
When unset, jobs route to the lead model. When set, memory health /
skill review / negative learning jobs route to the aux model.

Phase 1 only allows Claude Haiku 4.5 as the aux slot — keeps the
provider matrix simple. Phase 2 opens it to any provider/model pair
that supports tool calling.

#### Relationship to existing lead-delegate pattern

The lead agent in a crew already delegates tasks via `dispatchByID`.
Aux model is a NEW orthogonal axis: lead delegates work; aux runs
maintenance jobs. They never conflict at the routing layer.

### F4 — Keeper Phase 2 (Layer 2)

Four new endpoints, all routed through the existing Routines surface
so dispatch and audit reuse what's already wired.

#### F4.1 — `POST /api/v1/keeper/skill-review`

Agent proposes a new skill (markdown body + intent). Keeper reads the
catalog, the proposed body, and any related operator instructions,
returns `APPROVE` / `REJECT` / `ESCALATE` with rationale. Approved
proposals land in `skills_proposed` with a UI button for operator
final review.

#### F4.2 — `POST /api/v1/keeper/behavior` (DUAL MODE)

Two modes share the endpoint:

- **periodic** (cron): trim AGENT.md sections that haven't been
  referenced in N days; write trimmed version with a journal entry
- **ad-hoc** (agent-initiated): "should I keep this line?" Keeper
  returns keep / drop with reasoning

#### F4.3 — `POST /api/v1/keeper/memory-health`

Daily job. Reads AGENT.md and PERSONA.md sizes, age distribution,
recall-vs-write ratio. Returns `HEALTHY` / `BLOATED` / `STALE` plus
suggested actions (consolidate, archive, trim). Routes through the
inbox when action is required.

#### F4.4 — Negative learning (lessons.md writer)

When an agent run fails in a structured way (validation error, tool
refusal, escalation), `lessons.md` gets a YAML-backed entry via the
`internal/consolidate/lesson_writer.go` primitive Z.7 ships. The
writer enforces:

- Per-agent caps
- Replace-by-ID semantics
- Atomic temp+rename saves
- Enum validation on kind / severity

The lesson then appears in the agent's next system prompt assembly
as guard rails: "you've previously failed at X — try Y first".

### F5 — Ephemeral agent lifecycle (Layer 3)

#### Problem
Agent roster today is permanent: once you `crewship crew add-agent`,
the agent persists forever. Contractor scenarios (hire for one
mission, ghost when done) and burn-after-use agents have no surface.

#### Solution

PR-D ships the ephemeral lifecycle as two columns on `agents`,
not the originally-drafted `lifecycle` enum:

- `ephemeral` (BOOLEAN, default 0). 1 means the row participates in
  the hire/rehire/sweep mechanics; 0 means the row is permanent and
  the sweeper ignores it.
- `expires_at` (TEXT RFC3339, nullable). When `ephemeral=1`, the
  sweeper compares this to `now` to decide if the row should ghost.
- `expired_at` (TEXT RFC3339, nullable). Stamped by the sweeper when
  the row actually ghosts; the canonical "this agent is a ghost"
  signal. Rehire clears it back to NULL.

So the three lifecycle states map to column shapes, not an enum:

- **permanent**: `ephemeral=0` (default; existing behavior).
- **ephemeral live**: `ephemeral=1`, `expired_at IS NULL`.
- **ghosted**: `ephemeral=1`, `expired_at IS NOT NULL`. Auto-set by
  the EphemeralExpiry sweeper when `expires_at < now` and the agent
  is not RUNNING (mid-mission grace). Operator-killed ghosts share
  the same shape; the audit log distinguishes the two.

Plus a `hire`/`rehire` CLI pair that re-activates a ghosted agent
without losing its conversation history. `rehire` clears
`expired_at` and pushes `expires_at` forward.

### F6 — PERSONA.md + per-user peer cards + GDPR (Layer 3)

#### Problem
Same agent reacts identically to every operator. Pavel (technical,
terse, Czech), Ivana (warm, formal, English), Pepa (external,
super-formal, English) — all see the same response register. The
agent has no surface for per-user calibration.

GDPR compliance also lacks primitives: there's no documented "delete
all data for user X" path beyond the existing user-deletion cascade.

#### Solution

`PERSONA.md` is operator-edited (agent can suggest via inbox).
Lives next to AGENT.md / CREW.md, 1.5KB cap, read on every run.

Per-user peer cards under `/secrets/<agent>/peers/<user-slug>.md` —
small (1.5KB) profile cards the agent reads when interacting with a
specific user. Agent never writes them; agent suggests via inbox →
operator approves.

GDPR primitives:

- `DELETE /api/v1/admin/users/{id}/data` — cascades to memory,
  peer cards, audit log entries authored by the user
- Per-row `data_subject_id` on memory tables so the delete is exact
- Audit-log export endpoint (read-only history of what was stored)

## 6. References

Internal:

- `internal/memory/doc.go` — module-level architecture notes
- `internal/keeper/gatekeeper/gatekeeper.go` — Keeper decision pipeline
- `internal/consolidate/lesson_writer.go` — Z.7 lessons writer
- `internal/orchestrator/memory.go` — boot-time snapshot assembly
- `internal/secrets/bootstrap.go` — zero-friction secrets generation

Crewship PRs that shape this work — final landed state on main:

- PR #454 — declarative manifest layer (CrewTemplate / Recipe / Connector kinds)
- PR #456 — auto-managed sidecar credentials (T1 tier)
- PR #458 (PR-Z) — ✅ MERGED 2026-05-21 — hard reset (7 cleanup items, Z.6 voided)
- PR #466 (PR-A foundation) — ✅ MERGED 2026-05-21 — F1 native memory dispatcher + scanner + cap mirror
- PR #461 (PR-B) — ✅ MERGED 2026-05-21 — F2 autonomy slider + F3 aux model slot — migration v101
- PR #468 (PR-A.4/5) — ✅ MERGED 2026-05-21 — F1.4/F1.5 adapter MCP wiring across 5 adapters (CURSOR deferred — upstream `--print` MCP bug)
- PR #470 (PR-C) — ✅ MERGED 2026-05-21 — F4 Keeper Phase 2 (4 evaluators + bootstrap + scheduler + behaviorhook) — migration v102
- PR #469 (PR-D) — ✅ MERGED 2026-05-21 — F5 ephemeral hire/rehire + ghost UI + pending-review gate + sweeper grace — migration v103
- PR #471 (PR-E) — ✅ MERGED 2026-05-21 — F6 PERSONA two-layer + peer cards + GDPR + 4-subtab Memory UI — migrations v104 + v105

## 6.1. Tier-2 follow-ups (PR-F roll-up — partial ship, partial deferred)

PR-G/PR-F #472 (in-flight) addresses items 1, 4, 5, 6, 7 below. Items
still deferred after PR-F merge are marked DEFERRED with a target PR.

1. **F3** `cfg.Auxiliary` YAML override — DEFERRED (PR-F11). Bootstrap
   uses `llm.DefaultAuxiliaryModels()`; per-workspace override path
   isn't wired. Aux-status UI is read-only diagnostic only.
2. **F4.2** synchronous BLOCK CLI interrupt — DEFERRED (PR-F12). Today
   logs + journals + inbox; in-flight CLI runs to next tool call.
3. **F5** `last_activity_at` column for finer sweeper grace — DEFERRED
   (PR-F13). Current `status != RUNNING` guard works for the common
   case but doesn't catch RUNNING-then-idle inside the 5-min window.
4. **F5** Inbox approve-hire UI button — ✅ SHIPPED in PR-F #472
   (`components/features/inbox/inbox-list.tsx` waitpoint branch).
5. **F6** aux-LLM peer card extractor — DEFERRED (PR-F14). Today
   `NoopExtractor`; peer cards have empty bodies until aux LLM
   extractor wires through the PR-B F3 aux slot.
6. **F6** operator-editable AGENT.md / CREW.md — PLACEHOLDER. The
   codemirror markdown editor shipped in PR-F (Task C) renders
   AGENT.md / CREW.md but they remain read-only because
   `internal/memory/writer_caps.go` restricts writes to the agent
   runtime. Operator-edit needs a new gated endpoint.
7. **F1.6** CURSOR_CLI adapter MCP wiring — BLOCKED-UPSTREAM.
   `cursor-agent --print` ignores `mcpServers`; adapter declares
   `SupportsMCP() returns false`. Tracking the upstream fix.
8. **F1** FTS5 plumb-through for `memory.search` — DEFERRED-WITH-
   TOMBSTONE (PR-F5 marked the dead-code path; PR-F15 will wire it
   end-to-end). See `internal/memory/hybrid.go` file header +
   `internal/memory/hybrid_dead_code_test.go` for the AST sentinel.

## 6.2. PR-F #472 ship state (post-audit roll-up)

This PR consolidates the post-audit remediation. Verified against
the auditor's 2026-05-21 findings list:

**SHIPPED** in PR-F #472:
- ✅ Migration v107 — `data_subject_id` columns + `gdpr_actions` audit table
- ✅ Admin GDPR cascade: `DELETE /api/v1/admin/users/{userId}/data` + EXPORT
- ✅ Scanner v2: base64 deobfuscation + Cyrillic/Greek homoglyph fold +
     URL exfil patterns + tool-return scan path (MINJA defense)
- ✅ MemoryProvider interface + LocalDispatcher reference impl
- ✅ AgentBrief sub-agent briefing primitive + BRIEF.md wire-up
- ✅ Lessons tier security tombstone — agent-author write path rejected
- ✅ Hybrid search tombstone + AST sentinel test
- ✅ F4.1-4 keeper queue UIs in admin panel (4 sub-tabs)
- ✅ GDPR admin UI panel (export + cascade delete)
- ✅ Codemirror markdown editor in memory tab (PERSONA writable;
     AGENT.md / CREW.md highlighted but read-only)
- ✅ Self_learning gate consumed by F4.4 negative_learning + F6
     persona_suggest paths (per-agent override of crew autonomy)
- ✅ Paymaster scope attachment fix in F4 evaluator handlers
- ✅ 10 CodeRabbit round-N fixes (a11y labels, RBAC mirror, error
     handling, log scrubbing)

**STILL DEFERRED** after PR-F #472 merge:
- ⏳ Lessons.md content-scan in GDPR cascade (warning logged; manual
     review required per data_subject_id mention)
- ⏳ memory_versions blob orphan GC (rows deleted; blobs left on
     disk — needs separate sweep; PR-F16 target)
- ⏳ Hybrid search dispatcher wire-up (engine reachable from HTTP
     surface today, not from agent tools; PR-F15)
- ⏳ Per-adapter tool-return scan paths beyond Claude `tool_result`
     blocks (PR-F4 ships "scan path 1"; scan path 2 enumerates the
     other adapters)
- ⏳ Provider interface swap at production call sites (interface
     shipped + LocalDispatcher ref impl + tests; existing call sites
     still use `*Dispatcher` directly; PR-F17 lifts them)
- ⏳ Provider reference impl for Mem0 / Letta shape (PR-F18)
- ⏳ LoCoMo / LongMemEval reproducer + published baseline (PR-F19)
- ⏳ Skill→Memory bridge (MEMORY-ROADMAP §8.2; PR-F20)
- ⏳ Bitemporal `valid_from` / `valid_to` (PR-F21)

**Round-2 / Round-3 post-audit hardening (added 2026-05-21):**
- ✅ Lessons memory tier security tombstone — dispatcher write path
   rejects `tier="lessons"`; consolidate.WriteLesson stays single
   writer with schema + flock + idempotency. Closes the auditor's
   #4 finding ("`capForTier("lessons")` returns 0 = persistence
   attack vector").
- ✅ Cross-tenant `loadSelfLearningEnabled` scoping — workspace_id
   added to WHERE clause + `assertBodyWorkspaceMatchesCtx` on all
   four F4 handlers rejects asymmetric body/ctx mismatch with 400.
   Closes the asymmetric-bypass case; symmetric remains for PR-F24.
- ✅ Frontend RBAC mirrors backend — `AgentLearningToggle` uses
   `abilities.can("manage", "Agent")` (matching server PATCH
   permission); update-only users see toggle disabled instead of
   403 at save.
- ✅ `Promise.allSettled` for independent fetches in
   `CrewPolicyControls.load` so a quota-fetch network error doesn't
   poison the required policy-fetch render.
- ✅ Inbox `fetch()` try/catch on BOTH the approve-hire (wrap
   "approved") and routine-retry (wrap "retried") paths. Network
   failure now surfaces a toast; pre-fix it cleared busy state with
   no user feedback (silent success). Class-of-bugs sweep
   (grep `await fetch` confirmed both call sites wrapped).

**Tier-3 deferred (open as GitHub issues for tracking):**
- ⏳ **PR-F24** — bind `X-Internal-Token` to a specific workspace in
   `internalWsCtx` middleware (closes symmetric cross-tenant case).
- ⏳ **PR-F25** — drop `body.workspace_id` from F4 handlers; derive
   exclusively from ctx (restores the invariant
   `docs/api-reference/internal.mdx:23` originally claimed).
- ⏳ **PR-F26** — replace native `<input>` / `<button>` in
   `agent-learning-toggle.tsx` with shadcn `Input` / `Button`
   primitives (consistency pass across all governance toggle
   surfaces).
- ⏳ **PR-F27** — assert `memory.LoadPersona` succeeds before
   asserting content in `agent_persona_test.go:287` (catches
   silent storage regression in the OFF-path test).
- ⏳ **PR-F28** — boot-time SQL prepare validation across all raw-
   string queries in `internal/api/`. Origin (2026-05-21 incident):
   a leaked git merge-conflict marker inside a Go raw-string SQL
   query in `loadAgentData` (PR #475 hot-fix) shipped to main
   because Go raw strings don't parse their content (so go vet /
   golangci-lint / gosec all stayed green) and the bug only
   surfaced as a runtime SQLite "syntax error" on first chat
   resolve. A central `prepareAll()` at server startup would have
   caught it before the first request. Estimated scope: ~30
   queries enumerated, single registry, ~150 LOC + tests.
- ⏳ **PR-F29** — one-time admin audit on `main` branch protection:
   require the new `merge-conflict-markers` CI job in the protected
   status checks, disable force-push, disable allow-merge-with-
   conflicts (GitHub default permits merges with markers in the
   diff if the protection rules don't explicitly forbid).
- ⏳ **PR-F30** — chat-resolve end-to-end smoke test in
   `.github/workflows/nightly-smoke.yml`: bootstrap fresh
   instance + `crewship apply` minimal crew/agent + `crewship ask`
   and assert 200. Would have caught the PR #475 bug in CI even
   without the marker sentinel — defense in depth for the next
   shape of bug that the marker grep wouldn't catch.

## 7. Open questions

- F1: should `memory.search` use FTS5 from day one or substring-only
  with FTS5 deferred? Current plan: substring MVP, FTS5 swap is a
  contained later change.
- F4.2: behavior endpoint dual-mode (periodic vs ad-hoc) — is the
  shared endpoint surface worth the conditional, or should they
  split into two endpoints? Keep shared for now; reassess after
  first month of telemetry.
- F5: do ephemeral agents share workspace memory or get isolated
  scratch space? Probably scratch + opt-in promote-to-crew. Decided
  in PR-D scoping.
- F6: GDPR export format — JSON or markdown bundle? Pavel preference
  is markdown; revisit if a customer needs JSON for processing.

## 10. Decision log — drift between stated intent and shipped code

Added 2026-05-21 as part of the v4 retro-fit revert. Every drift the
implementation introduced versus the v3 PRD is recorded here so a
future reader (or external auditor) can see exactly where intent and
shipped behavior diverged, when, and why. Do NOT silently re-align
this PRD to the code — record the drift instead.

### 10.1 F1 `memory.search` — substring-only, FTS5 deferred

**v3 PRD said:** "substring + FTS5 search."

**Code shipped:** substring-only via `tools.go::handleSearch`. The
FTS5 engine (`internal/memory/hybrid.go` + `engine.go` + `index.go`)
shipped alongside but is unwired from the agent tool surface.

**Why:** Wiring the engine through the dispatcher constructor +
sidecar handoff was estimated at ~150 LOC across signatures + test
fixtures, beyond the PR-A foundation scope. The engine is reachable
from `internal/api/memory_hybrid_search_handler.go` (operator API
path), just not from the agent's `memory.search` tool.

**Tombstone:** `internal/memory/hybrid.go` file header +
`internal/memory/hybrid_dead_code_test.go` (AST sentinel that fails
if `HybridSearch` enters the `handleSearch` call graph — flipping the
test green is the wire-up signal). Target: PR-F15.

### 10.2 F2 CLI — `crewship policy` vs original `crewship crew update`

**v3 PRD said:** `crewship crew update <slug> --autonomy 2 --reason X`.

**Code shipped:** `crewship policy get/set/list` as a sub-noun, with
`autonomy_level` accepting the string enum `strict|guided|trusted|full`
(not integer 0..3).

**Why:** The crew-update surface already carries many flags; adding
governance to it was bundling concerns. The policy sub-noun is also
easier to extend (PR-B added `behavior_mode`, PR-G added
`max_ephemeral_agents` to the same panel). String enum was chosen
over integer to make raw `crews` table inspection self-documenting.

**Cost:** Operators reading the v3 PRD literally get `command not
found`. The v4 retro-fit silently rewrote the PRD to match; this
decision log restores the original intent and explains the trade.

### 10.3 F2 schema — `autonomy_level TEXT enum` vs `integer 0..3`

**v3 PRD said:** `crews.autonomy_level INTEGER 0..3`.

**Code shipped:** `TEXT NOT NULL DEFAULT 'guided' CHECK(autonomy_level
IN ('strict','guided','trusted','full'))`.

**Why:** Same string-vs-integer reasoning as 10.2. Integer ordering
also implies the levels are linearly more-permissive, which isn't
quite true (`full` + `block` is forbidden — not on the integer scale).
String enum forces consumers to use closed-set switch statements.

### 10.4 F4 endpoint URLs — `/api/v1/internal/keeper/*` vs public `/keeper/*`

**v3 PRD said:** `POST /api/v1/keeper/skill-review` (etc, four
endpoints).

**Code shipped:** All four under `/api/v1/internal/keeper/*` with
`internalAuth` middleware (X-Internal-Token).

**Why:** F4 endpoints are platform-triggered, not operator-triggered:
the scheduler routines + behavior hook are the callers. A public path
would invite operators to call them ad-hoc, which is OK but creates
the wrong default — the routine cron has the right context (workspace
sweep, full skill catalog) that an ad-hoc operator call misses.
Internal-token auth also keeps the surface off the operator's mental
model.

**Cost:** Operator can't curl `/api/v1/keeper/skill-review` per v3
PRD's literal URL. The admin UI panels in PR-F2 surface the
`keeper_requests` rows the internal endpoint produces, which is the
operator-facing path.

### 10.5 F5 schema — two columns vs three-state enum

**v3 PRD said:** "Three states on `agents.lifecycle`."

**Code shipped:** Two columns — `ephemeral BOOLEAN` (yes/no) +
`expired_at TEXT` (null/timestamp). The state machine derives:
`(ephemeral=0)` = permanent; `(ephemeral=1, expired_at IS NULL)` =
live; `(ephemeral=1, expired_at NOT NULL)` = ghost.

**Why:** The third state (archived) was conflated with deleted_at
soft-delete in the schema review; carrying it as a separate column
would have created two ways to express the same "agent isn't usable"
intent. Today: archived = `deleted_at NOT NULL` (existing pattern);
ghost = derived from `expired_at`. The lifecycle enum is reconstructed
in the query layer.

**Cost:** Any consumer reading the v3 PRD literal "lifecycle column"
gets `column not found`. Migration v103 carries the doc comment
explaining the two-column choice.

### 10.6 F4.4 lessons routing — WriteLesson only, not direct dispatcher

**v3 PRD said implicitly:** generic write path includes lessons.

**Code shipped:** lessons tier is **rejected** in `tools.go::
handleWrite` (2026-05-21 hardening). The only path to write a lesson
is through `consolidate.WriteLesson` via the F4.4 negative-learning
endpoint, which enforces schema + idempotency + flock that the raw
dispatcher does not.

**Why:** Auditor flagged `capForTier("lessons")` returning 0 as a
persistence attack vector. The agent could call
`memory.write(tier="lessons", content="<freeform>", mode="replace")`
and corrupt the schema, accumulate duplicate entries, or persist
attack content past the next agent restart.

**Cost:** None — no production caller writes lessons through the
dispatcher path. The consolidator is the only writer.

### 10.7 F6 admin GDPR cascade — shipped in PR-F #472 (was missing pre-audit)

**v3 PRD said:** `DELETE /api/v1/admin/users/{id}/data` cascades to
memory + peer cards + audit.

**Pre-audit code:** Did not exist. Only self-service
`DELETE /api/v1/users/me/peer-cards` was shipped. Auditor flagged
this as the #1 EU release blocker.

**PR-F #472 ships:** Migration v107 (`data_subject_id` columns +
`gdpr_actions` audit table), `DELETE /api/v1/admin/users/{userId}/
data` (cascade) + `GET /api/v1/admin/users/{userId}/data` (Art. 15
export). Admin UI panel in `components/features/admin/gdpr-actions-
panel.tsx`. Idempotent; each invocation creates a `gdpr_actions` row.

**Still deferred:** lessons.md content-scan (warning logged, manual
review required); memory_versions blob orphan GC on disk.

### 10.8 F6 PERSONA suggest CLI — placeholder, not shipped

**v3 PRD said:** `crewship persona suggest-from-inbox` operator-facing
command.

**Code shipped:** Command exists but errors gracefully ("not
implemented in this build"). Per-agent `self_learning` gate (PR-G)
provides the operator-approval flow at the API + UI level; CLI
parity is PR-F22 scope.

### 10.9 F6 memory editor — codemirror highlighting, write still backend-gated

**v3 PRD said:** "Memory tab UI editor for AGENT.md / CREW.md."

**Code shipped:** PR-F (Task C) replaces the textarea with the
shared `components/shared/markdown-editor.tsx` codemirror wrapper for
syntax highlighting. PERSONA tier is writable through this editor;
AGENT.md / CREW.md render with highlighting but are read-only because
`internal/memory/writer_caps.go` restricts writes to the agent
runtime (operator-edit needs a new gated endpoint — PR-F23).

**Cost:** Operator sees the AGENT.md / CREW.md content nicely
rendered but can't edit. Compromise between "expose the data
transparently" and "don't let operator edits drift from what the
agent thinks is true."

### 10.10 PRD process drift — v4 retro-fit was wrong (this revert)

**What happened:** Commit `425cadf6` rewrote the PRD header to say
"v4 — all 7 stack PRs merged" and quietly aligned several feature
descriptions to match what shipped (F1 search wording, F5 schema
wording, CLI naming notes). The retro-fit produced a document that
looked complete but recorded zero drift — exactly the opposite of
what a PRD is for.

**Why it was wrong:** A PRD is the **statement of intent**. When code
diverges from intent, the right move is to log the divergence (so a
future reader can audit it) — not to revise the intent to match the
code (which makes the audit impossible).

**The fix:** This decision log (§10) reverts the v4 retro-fit by
restoring the v3 phrasing throughout the spec body and recording
every drift here, with the WHY. Future PRD revisions append to this
log; they do not silently overwrite earlier statements.

**Cost recorded:** PR-G #472 commit `425cadf6` is the cardinal sin.
The fix is procedural, not technical — discipline carried by this
log file.
