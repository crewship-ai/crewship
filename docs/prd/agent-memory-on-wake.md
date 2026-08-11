# Design — What an agent knows when it wakes

Status: draft · 2026-08-01 · Companion to `crew-runtime-capacity.md`

> **The requirement being validated.** *"An agent woken after a week must still hold
> context — its own memory, the crew's memory, the workspace's memory, and knowledge of the
> human user. It must be able to pick up a routine or an issue with that context."*
>
> All `file:line` references verified against `fix/aux-reach-probes-the-slots-endpoint` on
> 2026-08-01. Live behaviour probed against dev1 the same day. Re-verify before
> implementing.

---

## 0. Verdict

**Storage is solid. Delivery is broken.**

Every memory file lives on a host bind mount and survives container stop, removal,
re-provision and image rebuild. The agent's DB row, its `/crew/agents/<slug>` home and its
bearer token all come back byte-identical after container recreation — the token is a
deterministic HMAC (`internaltoken.go:145`), not an issued record. The FTS index rebuilds
cheaply at sidecar boot. Nothing meaningful expires by age except journal rows past 30 days
and version history.

What fails is what the agent is *shown* at wake, and what it is *told* to go looking for.

---

## 1. Live evidence

Asked an agent on dev1 to report its own prompt contents. Agent has `memory: on`.

> **(a)** `NONE` — no `OPERATOR MODEL` or `PEER CONTEXT` block.
> **(b)** `NOTHING` — no information about the human user: no name, no preferences.
> **(c)** No `AGENT MEMORY` block. Session context does carry
> `[MEMORY NUDGE] You have 177 new journal entries since your last memory update`.

The nudge fires; the memory does not arrive.

**Honest limit of this test:** the missing `[AGENT MEMORY]` block cannot be distinguished
from "this agent never wrote any memory" — `[PERSONA]` *was* present, and it travels the
same assembly path, so assembly ran and had nothing to show. That is itself the finding:
**memory appears only if the agent previously wrote some, and nothing in the wake path
ensures it ever does.** (a) and (b) are unambiguous.

---

## 2. What is actually assembled

Two assemblers. The API builds a base `system_prompt`; the orchestrator appends to it and
puts everything volatile into the **user** message for prompt-cache stability
(`orchestrator_run.go:688-694`).

**Static preamble** (const `crewshipSystemPreamble`, `orchestrator/exec.go:22-152`),
prepended per adapter.

**API-built** (`loadAgentSystemPrompt`, `api/agent_config.go:504-583`): ethos, language,
identity, custom prompt, skills, routines, Keeper block, integrations, container resources.

**Orchestrator-appended** (`assembleSystemPrompt`, `orchestrator_run.go:687-844`): crew
context (LEAD) or peer communication (non-LEAD), the memory context, language.

**Memory context** (`orchestrator/memory.go:137-186`), in emission order:

| Block | Read from | Budget |
|---|---|---|
| `[AGENT MEMORY]` — `pins.md`, `BRIEF.md`, `AGENT.md`, `daily/<yesterday>.md`, `daily/<today>.md` | `cat` exec, `/crew/agents/<slug>/.memory/` | remainder |
| `[CREW SHARED MEMORY]` — `CREW.md`, `daily/<today>.md`, lessons digest (LEAD) | `/crew/shared/.memory/` | 40 % |
| `[WORKSPACE MEMORY]` | host `{DataDir.Root}/memory/<ws_id>/` — never mounted into a container | 15 % |
| `[PINS]` | `/crew/shared/.memory/<crew_slug>/topics/pins.md` | 10 % |
| `[PERSONA]`, `[OPERATOR MODEL]`, `[PEER CONTEXT]` | `memory_persona.go:92-218` | unbudgeted |

**Volatile, in the user message** (`session_context.go:22`): conversation history, episodic
recall, memory nudge, cost awareness.

The consolidator's `.proposed` staging directory is created one component at a time and
refuses symlinks. Both consolidation proposals and memory-derived skill candidates share
this boundary, preventing the agent-writable tree from redirecting host-side staging.
Proposal mode requires the configured topics output directory to exist before staging;
it does not create an unanchored output path.

The host consolidator treats `pins.md` and daily `learned-*.md` files as
agent-writable trust boundaries. Reads and appends are anchored to the resolved topics
directory and explicitly refuse a final-component symlink, so a link planted in the
shared bind mount cannot redirect a privileged host write outside that directory.

Canonical memory versioning reads host files without following a final-component
symlink. A canonical file can live in an agent-writable bind mount, so the version
recorder must refuse a link rather than snapshot bytes from an unrelated host path.

---

## 3. The gaps

### 3.1 A week is exactly the wrong window

The agent block reads only `daily/<yesterday>.md` and `daily/<today>.md`
(`memory.go:252, 261-262, 282-283`); the crew block only `daily/<today>.md` (`:307`).

After seven idle days all three are empty. The last working day's notes — the single most
task-relevant artifact — sit on disk, indexed, and are never injected. **Nothing computes
"last active day" and nothing scans backwards.** Only `memory.read tier=daily
key=<explicit date>` retrieves them, and the agent is never told which date to ask for.

### 3.2 The prompt tells the agent not to look

`[MEMORY INSTRUCTIONS]` (`memory.go:536-537`) says:

> *"the boot snapshot above already includes the relevant crew memory tier; mid-session
> recall via native memory tools lands in PR-A (F1)."*

That text is stale — the tools shipped — and it instructs the model that the snapshot is
sufficient. The only line gesturing at recall is `memory.go:528`, *"Before starting complex
tasks, check your memory for relevant past context,"* which names no tool. **`memory.search`
and `conversation.search` are never named anywhere in prompt text**; the model can only
learn they exist from the MCP tool listing.

### 3.3 A cold container withholds the memory tools entirely

`memorySinkReady` (`mcp_memory_inject.go:68-70`) probes the sidecar on `:9119`; on failure
the **whole** `crewship-memory` MCP server is dropped from the config (`:85-98`,
`mcp_writers.go:245/288/350/425`). Assignment- and mission-dispatched runs use
`SkipSidecar` (`assignments_run.go:601`) and never start one.

So a crew container that was TTL-stopped during the idle week and restarted for an
assignment gets **no `memory.*` and no `conversation.search` at all** — the lazy path is
unavailable in precisely the scenario the requirement describes.

### 3.4 On a routine or issue wake, human knowledge is structurally unreachable

Routines mint a **fresh chat per step** (`pipeline/runner_orchestrator.go:226-238`) and
resolve via `ResolveAgent`, so `OpenedByUserID` is `""` (`api/agent_config.go:415-421`:
*"opener is `""` for agent-only resolves"*). Both `[OPERATOR MODEL]` and `[PEER CONTEXT]`
are hard-gated on that field.

The fresh `ChatID` also means an empty conversation JSONL, so no `[CONVERSATION HISTORY]`.
Assignments set `SkipConvHistory = true` unconditionally (`assignments_run.go:602`).

**This is exactly the trigger the requirement names, and today it cannot carry user context
even in principle.**

### 3.5 And the user-model files are empty anyway

Production wires both sweeps with no extractor — `cmd_start.go:1055-1058` and `:1076-1080`
default to `NoopExtractor` / `NoopUserModelExtractor`, described in code as *"the MVP
placeholder"*; they return `""` and `SyncUserModel` treats that as `skip_empty_content`.

The only place a human's real name or email reaches an agent is the mission/issue comment
digest (`mission_tasks.go:282-302`).

### 3.6 Memory is off by default at the schema level

`memory_enabled INTEGER NOT NULL DEFAULT 0` (`migrate_consts_v01_init.go:145`), and
`agents_create.go:220-223` sets 1 only when the caller sends it. `req.MemoryEnabled` gates
the **entire** memory context (`orchestrator_run.go:797`). The manifest path defaults it
true (`manifest/kinds/agent.go:548-551`) — so an agent created via API/UI has no memory
tier at all while a manifest-created one does. Inconsistent, and silent.

### 3.7 Episodic recall silently requires Keeper + Ollama

The embedder is constructed only when `cfg.Keeper.Enabled && cfg.Keeper.OllamaURL != ""`
(`server.go:475-477`); a nil embedder returns empty recall (`orchestrator_adapters.go:251`).
It is additionally gated on `req.UserMessage != ""` (`orchestrator_run.go:751`). On an
install without Ollama the only *content-addressed* route back to a week-old event does not
exist — leaving the fixed-path reads, which per §3.1 are empty.

### 3.8 Learned rules are written to the host filesystem root

`CrewMemoryRoot` defaults to the container-absolute literal `"/crew/shared/.memory"`
(`consolidate/runner.go:179-180`, `server.go:777`) while the consolidator runs in the
**host** process. `/crew` inside a container is a bind of host
`{Storage.BasePath}/crews/{crewID}`. So `learned-*.md`, `.proposed/` and `pins.md` are
`MkdirAll`-ed at the host's filesystem root.

Even discounting that, `buildMemoryContext` reads a closed path list containing
`topics/pins.md` but not `topics/learned-*.md`, and the tier enum (`memory/tools.go:111`)
has no `learned` member. This is a known, **test-pinned** four-way gap — see the sentinel
header at `orchestrator/learned_rules_not_delivered_test.go:22-79`.

Consequence: the `[PINS]` block is also empty in a host-run `crewshipd`, so the
"operator-pinned = always in context" guarantee at `memory.go:379-388` does not hold.

---

## 4. What genuinely degrades with time

**Deleted:**
- Journal compaction, daily 03:00 UTC, **30-day** cutoff (`consolidate/compact.go:78`).
  Originals are deleted (`:507`) after a lossy archive — summary cut to 200 chars, payload
  to 400 (`:573-575`) — and **deletion proceeds even if archiving failed** (`:140-147`).
- `memory_versions` per-workspace retention is a pure age DELETE with **no keep-N floor**
  and can remove the last surviving version row (`memory/retention.go:112-116`); hazard
  documented in-code at `consolidate/runner.go:346-359`.
- `lessons.md` caps at 500 entries, evicting oldest, kind-blind
  (`consolidate/lesson_writer.go:74`).

**Degraded:** episodic importance decays `× max(0.1, 1 − days/180)`
(`episodic/importance.go:100`) — ~×0.96 over a week; consolidation recency has a 14-day
half-life; recall considers only the newest 5000 journal rows.

**Not aged out:** `AGENT.md`, `CREW.md`, `pins.md`, `learned-*.md`, session JSONL,
`journal_embeddings`. `journal_entries.expires_at` exists but is a dead column — no
producer, no filter, no sweep.

---

## 5. Durability, for the record

| Tier | Location | Survives stop / rm / re-provision / rebuild |
|---|---|---|
| per-agent `.memory/*` | `/crew/agents/<slug>/.memory/` (host bind) | ✓ ✓ ✓ ✓ |
| daily notes, peer memory | same | ✓ ✓ ✓ ✓ |
| crew-shared, user model | `/crew/shared/.memory/` | ✓ ✓ ✓ ✓ |
| workspace tier | host `{Root}/memory/<ws_id>/`, never mounted | ✓ ✓ ✓ ✓ |
| episodic | DB `journal_entries` + `journal_embeddings` | ✓ ✓ ✓ ✓ |
| FTS index | inside each `.memory/`, rebuilt at sidecar boot | ✓ ✓ ✓ ✓ |
| `/secrets/<slug>` | tmpfs | ✗ (rewritten every run — correct) |

Agent identity survives recreation intact: `agents` has no `container_id` column; the
bearer token is re-derived deterministically from `ENCRYPTION_KEY`.

> **Security note, out of scope here but worth an issue:** that token has **no expiry and
> no revocation list**. Once derived it is valid forever unless `ENCRYPTION_KEY` changes.
> See `agent-identity-signing.md`, which replaces it with kernel-attested identity.

---

## 6. Work items

Ordered by value. §6.1 and §6.2 are independent of the container-per-agent decision.

### 6.1 Make the wake carry the right window

1. **Scan back to the last active day** instead of reading only yesterday/today. Emit it
   labelled with its actual date so the agent knows there was a gap.
2. **Tell the agent a gap happened** — last-run timestamp and elapsed time. The prompt
   carries today's date (`memory.go:520`) and nothing else temporal.
3. **Consider a since-digest on wake.** `/standup?since=` exists on the sidecar but is
   documented only in the LEAD block (`lead.go:72-74`); the non-LEAD `[PEER COMMUNICATION]`
   block never mentions it (`peer.go:15-32`).

### 6.2 Fix the instructions

1. Delete the stale "lands in PR-A (F1)" sentence (`memory.go:536-537`).
2. **Name `memory.search` and `conversation.search` in the prompt**, with a wake-specific
   instruction to use them when the last activity is not today.

### 6.3 Make user context reachable from routines and issues

1. Populate `OpenedByUserID` (or an equivalent) on agent-only resolves so `[OPERATOR MODEL]`
   and `[PEER CONTEXT]` can be emitted for routine and issue wakes.
2. Ship a real extractor for the user model and peer cards, or remove the blocks and the
   schema until one exists — shipping a `NoopExtractor` behind a documented feature is worse
   than not shipping it.

### 6.4 Close the silent-empty paths

1. Make `memory_enabled` consistent between the API and manifest defaults.
2. Fix the learned-rules output root (§3.8) — it is already test-pinned.
3. Make the memory MCP server available on a cold container, or make the eager snapshot
   sufficient when it is not (§3.3).
4. Add a keep-N floor to per-workspace version retention (§4).
5. Surface, rather than swallow, the "episodic recall is unavailable because no embedder is
   configured" state (§3.7).

---

## 7. Open questions

1. **Is eager-snapshot or lazy-search the intended primary path?** Today it is eager with a
   fixed path list, and the prompt actively discourages the lazy one. Both halves fail at a
   one-week gap for different reasons. Picking one and committing to it would simplify §6.1
   and §6.2.
2. **Should there be per-user memory at all,** given cross-operator gossip is deliberately
   forbidden (`memory_persona.go:26-32`)? If yes, it needs an extractor; if no, the schema
   and the two prompt blocks should go.
3. **Does container-per-agent change any of this?** The memory tiers are host-backed and
   agent-scoped already, so durability is unaffected. §3.3 gets *worse*: more containers
   means more cold sidecars.
