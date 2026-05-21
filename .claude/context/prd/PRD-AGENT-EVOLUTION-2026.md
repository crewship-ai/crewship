# PRD — Agent Evolution: Learning, Governance, Identity & Lifecycle

**Status:** draft v3 (post-PR-Z hard reset, pre-PR-A feature work)
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

Eight items, lands before PR-A so the new feature work sits on a clean
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
- `memory.search(query, scope)` — substring + FTS5 search
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

Three states on `agents.lifecycle`:

- `permanent` (default; existing behavior)
- `ephemeral` (auto-archive after N days idle or M missions complete)
- `ghosted` (operator-killed; preserved for audit, can't dispatch)

Plus a `hire`/`rehire` CLI pair that re-activates a ghosted agent
without losing its conversation history.

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

Crewship PRs that shape this work:

- PR #454 — declarative manifest layer (CrewTemplate / Recipe / Connector kinds)
- PR #456 — auto-managed sidecar credentials (T1 tier)
- PR-Z — hard reset (this PR)
- PR-A — F1 native memory tools
- PR-B — F2 autonomy slider + F3 aux model
- PR-C — F4 Keeper Phase 2 (consumes Z.7 lesson writer)
- PR-D — F5 ephemeral lifecycle
- PR-E — F6 PERSONA + peer cards + GDPR

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
