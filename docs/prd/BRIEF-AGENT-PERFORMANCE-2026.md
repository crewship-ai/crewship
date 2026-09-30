# Brief / Zadání — Agent + Crewship Performance ("je to pomalé")

**Status:** Topic brief for research · **Date:** 2026-06-30 · **Owner:** Pavel (analysis) + Claude (brief)
**Type:** New, large topic — this document is the *problem statement + what to improve*, NOT an implementation plan. Pavel will do the full analysis/research on top.

---

## 1. Problem (observed, concrete)

Watching `morgan` (Opus 4.8) author + save a self-contained Ansible routine end-to-end in a CLI chat session, the whole loop felt **slow**:

- The agent makes **many MCP / tool calls** (memory operations, `ToolSearch`, file Write/Edit, Bash) — Pavel's words: *"zahoval tisíce MCP příkazů"*.
- The step where it **appends to the Ansible playbook** took disproportionately long.
- The agent spends turns **writing its own memory** mid-task.

**Pavel's thesis (the north star for this topic):**
> *Crewship should KNOW and REMEMBER what the agent does — Crewship should do all this bookkeeping itself. The agent shouldn't waste turns discovering tools and writing memory. The whole experience of working with Crewship + an agent must be **multiples faster**.*

This blocks the *feel* of the product. It is a foundational quality bar, not a nice-to-have.

---

## 2. Current architecture (where time plausibly goes)

- **Agent runtime:** the `claude` CLI runs inside a per-crew Docker container, exec'd by the orchestrator (`--print --output-format stream-json`), streaming events back. Model = per-agent `llm_model` (currently `claude-opus-4-8`; thorough but slow).
- **MCP tools are DEFERRED.** Built-in tools are curated via `--tools`; MCP tools (`crewship-memory`, `crewship-routines`, Composio) are **deferred-by-default**, so the agent must call **`ToolSearch`** to load a tool's schema *before* it can call the tool. → a discovery round-trip per tool, and the agent **doesn't inherently know what it has** (see memory `feedback_claude_tools_flag_mcp`: ToolSearch must stay or MCP breaks).
- **Every MCP call = a round-trip** to the sidecar at `127.0.0.1:9119` (memory read/write/search/append_daily; save_routine/list_routines; Composio).
- **Memory:** the agent can write memory (`AGENT.md`, `daily/*`) via MCP during a task. Consolidation is **post-run, debounced (~30 min), background** (verified in logs: "consolidate: skipping, below threshold" / "post-run consolidator debounced"). So *consolidation* is already off the hot path, but *inline agent-driven memory writes* are not.
- **Complexity/tier routing exists** (fast vs smart tier) but the whole authoring task ran on Opus.
- **The session + journal already capture everything the agent does** — a durable, queryable record already exists.

> ⚠️ We currently lack hard data: the big session had already rolled off the log tail. **Measurement is prerequisite #1.**

---

## 3. What to improve (the levers — the actual task)

### Lever 1 — Pre-load tool schemas (kill the `ToolSearch` tax) — *highest leverage*
The agent should NOT have to search for the tools it obviously needs. Pre-inject the **callable schemas** of its primary tools (`save_routine`, `run_routine`, memory ops, the crew's *connected* Composio integrations) so they're immediately invocable — no `ToolSearch` round-trips, and the agent *knows* its capabilities. Extend the existing `[CONTAINER RESOURCES]` / capability catalog from "what you have" (prose) to "what you can call" (schemas).
- **Tension to resolve:** why were they deferred? (context-size / `--tools` budget). Quantify the context cost of pre-loading vs the round-trip savings; pick a hybrid (pre-load the top-N likely tools, defer the long tail).

### Lever 2 — Crewship auto-remembers (no manual memory on the hot path) — *Pavel's explicit ask*
The session/journal **is** the record. Move ALL memory capture to background/post-session consolidation; stop prompting/enabling inline memory writes during a task. The agent focuses on the task; Crewship summarizes afterward (it already journals everything).
- **Risk to resolve:** does fully-background memory lose mid-task working memory or cross-session recall? Define what *must* be inline (likely nothing) vs background.

### Lever 3 — Model routing (fast tier for mechanical work)
Opus for design/decisions; **Sonnet/Haiku for the mechanical steps** (writing a playbook, calling `save_routine`, formatting a report). Use the existing complexity routing per step/phase. Quantify the speed gain vs any quality loss for authoring.

### Lever 4 — Crewship orchestrates more, agent free-forms less — *deeper redesign*
A **structured authoring flow** (Crewship drives: gather requirements → draft DSL → validate → save) instead of the agent taking dozens of free-form turns. The agent fills in the creative parts; Crewship owns the scaffolding + the deterministic steps.

### Lever 5 — Instrument + measure first — *prerequisite*
Build the observability to make this data-driven: **per-turn timing, MCP-call counts + latency by type, tokens/turn, total wall-clock per task**, surfaced per session/run. Without it we're guessing. (We already journal richly — extend it with timing/cost-per-step rollups.)

---

## 4. Goals / success criteria (to nail down in the analysis)

- A target speedup (e.g. **routine authoring N× faster wall-clock**).
- A cap on round-trips/turns per typical task.
- A latency budget per phase (provisioning is separate — see the provisioning-observability work).
- No regression in correctness/quality of authored artifacts.

---

## 5. Open questions for research (Pavel)

1. **Measure first:** how many turns / MCP round-trips / tokens does a typical authoring task actually take, and what's the per-MCP-round-trip latency to the sidecar?
2. **Pre-load vs defer:** what's the real context cost of pre-loading the top tools, and where's the crossover vs `ToolSearch` round-trips?
3. **Memory:** can it be fully background without losing anything? What (if anything) genuinely needs to be inline?
4. **Model:** does Opus over-deliberate for mechanical steps? Speed/quality curve of fast-tier authoring?
5. **Orchestration:** what does "Crewship orchestrates more" look like concretely — a routine-authoring state machine? How much creativity must stay with the agent?
6. **Generality:** these levers apply to ALL agent work, not just routine authoring — does the analysis target the whole agent loop or the authoring path first?

---

## 6. Adjacent (separate topics, parked here so they're not lost)

- **Session typing** — tag each session with type (`GUI` / `CLI` / `routine`) + a link to the routine it authored/ran, so *search sessions* filters by what the agent did. Ties to the routine↔chat linkage below.
- **Routine ↔ chat linkage + Files/Context tab** — the routine already stores `author_chat_id` / `author_run_id`; surface it (Files tab + "authored in [session]" link). Separate UX topic, already scoped in conversation.
- **Provisioning performance/visibility** — handled separately (cache-key + observability + lingering card already shipped on `feat/routines-vision`).
