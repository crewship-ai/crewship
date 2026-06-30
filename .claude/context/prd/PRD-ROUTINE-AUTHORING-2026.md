# PRD — Routines as Agent-Authored Capabilities (Manifest · Authoring · Governance · Prepare) 2026

**Status:** Living draft · **Date:** 2026-06-30 · **Owner:** Pavel + Claude
**Extends** [PRD-ROUTINES-MAX-2026](./PRD-ROUTINES-MAX-2026.md) (engine done). §0 is the north-star vision (the breakthrough); §1+ is the onboarding/governance layer already in flight (W0–W2). Read §0 first.

---

## 0. North star — routines as agent-authored capabilities

### 0.0 The thesis (why this is the breakthrough)
A routine is **not** "fetch X → post Y." A routine is an **agent-authored, durable, re-runnable CAPABILITY** that uses the *full devcontainer*: writes + runs scripts, runs CLIs (Ansible, kubectl, terraform), touches datastores (Redis, Postgres), uses integrations + credentials, and can delegate to other agents — **authored conversationally, like Claude Code.** You iterate 5–10 min in chat with a Lead; it prepares the routine *and* the scripts; you save it; it runs.

**The moat is the container.** trigger.dev / n8n / Zapier run workflows in constrained sandboxes with predefined nodes/SDKs. Crewship gives each crew a **full, persistent devcontainer** — so a routine can do anything a developer's machine can, and cloned repos / installed tools **persist between runs**. They structurally cannot copy this. So: **chat-first authoring, skip the visual builder, vytěžit the container maximum.** The JSON DSL stays the durable compiled artifact; legibility comes from a stable **capability manifest**.

### 0.1 The capability manifest (the stable contract — "doesn't change, supports maximum")
One stable vocabulary of capability **kinds**: `integration · datastore · tool/script · agent · routine · egress · credential · http`. Two faces of the same vocabulary:

- **Capability catalog (Crewship → agent):** *"here's what you CAN do in this container, and how."* Connected integrations, **crew services** (Redis at host `redis`, Postgres at `postgres:5432`), installed **tools** (devcontainer features + mise), delegatable peers, available routines, and **actions** (save_routine, query_peer, assign, expose_port…). Injected **structurally** — actions as MCP tools (the `memory.*` pattern), context as a `[CAPABILITIES]` block — so the agent **never probes** ("let me curl and see").
- **Routine manifest (agent → Crewship):** *"what THIS routine touches."* Derived from the step graph (agents from `agent_run`, routines from `call_pipeline`, egress from `http`, plus `integrations_required`/`egress_targets`/`credentials_required`) **+ a declared `resources` block** (datastores, tools/scripts — these can't be statically inferred since scripts run via `agent_run`+shell).

The manifest has **two readings**: **descriptive** (drives the legibility flow-diagram) and **prescriptive** (the preconditions — what must be ready). One source → three uses: **legibility + governance + provisioning.**

### 0.2 Requires vs has → prepare
- **Manifest** = what the routine **REQUIRES** (ansible, postgres, a cloned repo).
- **Catalog** = what the crew **HAS** (installed tools, running services).
- **Diff** = what a **prepare** phase must satisfy → and a checkable **gate** (generalise W0's integration gate to tools/services/repos: "needs ansible, crew lacks it → block / offer to provision").

**Prepare is OPTIONAL** (usually present for hard use-cases). Two distinct layers — must not be conflated:
1. **Env scaffold (one-time, infrastructural):** the **crew/workspace YAML manifest** (`internal/manifest/kinds/crew.go` — Services + devcontainer features). **Already solved.** A library of **use-case scaffolds** ("Ansible Ops crew" = features+services+agents+base routines) is the new bit.
2. **Per-run prepare ("pre-routine", agent-driven, may be slow):** clone repo, install deps, write run config — via a `before_all` hook or a `setup` phase. **MUST be idempotent + cached** — the persistent crew container keeps cloned repos/installed tools between runs (the moat vs trigger.dev's ephemeral runs). Otherwise complex routines are unusably slow.

### 0.3 Legibility — how to present this power clearly (the hard part)
Every routine, however complex, shows the **same legible face** (3 layers), so power stays clear:
1. **Flow preview** — a **read-only, auto-generated data-flow diagram** from the manifest (`Trigger → Fetch → Agent → Redis → Ansible → Postgres → Discord`). **Static, not live** (we will not trace live writes). Reconciles with "no builder canvas": chat **authors**, the diagram only **interprets**.
2. **"What it touches"** manifest panel — chips per kind, **risky highlighted** (→ governance).
3. **Plain-language spec** with **deterministic vs AI** tags per step (`skript` vs `AI rozhoduje`) — the trust signal ("what's fixed vs what an agent decides").
Plus: **run + watch** (progress in the run timeline / Activity Bar). **Observability demoted** (the 4 big stat cards → a thin strip). **Advanced grouped** (JSON/Editor, Versions, Webhooks, Wait points). **Future:** embedded container-app preview via `expose-port` (the agent builds a web UI with start/stop; Crewship previews it in-app so users don't chase ports) — **off by default**. Mockup: `.claude/context/wireframes/routine-detail-redesign.html`. **Icons: `lucide-react` everywhere.**

### 0.4 Authoring (chat-first, no probing)
- Lead authors via chat (the bundled **routine-author skill** playbook); the agent writes the DSL **and declares the manifest**.
- **`save_routine` becomes an MCP tool** (not curl-prose) — kills the shell-probing; the **capability catalog** tells the agent what's available so it never tries-and-sees.
- **Fast:** agent-authoring validates via **dry-run** (no nested agent execution), not a full test_run.

### 0.5 Readiness (~70% — what EXISTS vs the gaps)
| Capability | Status | Notes |
|---|---|---|
| Datastores (Redis/Postgres) | ✅ exists | crew **services** (`manifest/kinds/crew.go` + `EnsureCrewServices` + `crews.services_json`) |
| Container tools (ansible/kubectl/…) | ✅ declarable | devcontainer features catalog + mise (Crewship knows them) |
| Expose-port (embedded app) | ✅ exists | reverse-proxy to in-container app |
| Integrations/creds/skills/peers/routines | ✅ resolvable | scattered, no unified catalog |
| Routine "touches" | ✅ partial | risk classifier extracts http/code/egress/creds (W0/W1) |
| **Manifest foundation** | ✅ **built** | Slice 1: `resources` block + `ExtractManifest()` + API `manifest` (branch `feat/routine-manifest`) |
| `code` step (scripts) | ❌ unwired | scripts run via `agent_run`+shell → tools must be **declared** |
| Unified capability catalog → agent | ❌ missing | curl-prose → agent probes (Slice 2) |
| Preconditions/prepare gate | ❌ missing | the requires-vs-has diff (future slice) |
| Flow-diagram + manifest panel (UI) | ❌ missing | frontend, lucide (future slice) |

### 0.6 Roadmap (slices — build incrementally ON the manifest, never a parallel system)
**Foundation (merged/in-flight):** #739 W0 integration gate (**merged**), #736 approval-UX + C1 (**merged**), #740 W1 governance (open), #741 W2 describe-first + routine-author skill + dry-run gate + internal test_run (open), **Slice 1 manifest foundation** (`feat/routine-manifest`).
**Next:** **Slice 2 — capability catalog → agent** (`ResolveCapabilityCatalog`: consolidate services/tools/integrations/peers/routines; inject structurally so the agent knows "I have Postgres at `postgres:5432`, I have ansible" — no probing). **Slice 3 — `save_routine` MCP tool** (definitive probing fix).
**Then:** flow-diagram + manifest panel + **detail-page redesign** (frontend, lucide). **Preconditions gate + prepare phase** (`before_all`/`setup`, idempotent, persistent-container-cached). **Use-case scaffolds** (crew/workspace YAML templates).
**Bugs gating the vision (fix first):** `smartmania-top5-to-discord` doesn't run + isn't visible in the Activity Bar (the "watchable" foundation); seeded routines landing `proposed` after reseed (W1 should bypass the gate for trusted seed data).

---

## 1. Problem

Today a routine is authored by hand-writing **raw JSON DSL** in a CodeMirror box with 3 thin skeletons (`routine-create-dialog.tsx`). That's the worst quadrant — neither a friendly visual/form surface nor real code. It's error-prone and nobody outside engineering will do it. Three structural gaps make it worse:

1. **No integration awareness.** A routine that needs GitHub/Slack (via Composio) has no way to *declare* that need, and nothing checks at author- or run-time that the executing agent actually has it. Routines have **no `credential_requirements`** field (skills do). → silent run-time failures.
2. **No governance.** Agent-authored routines (`POST /pipelines/save` via sidecar) go **live immediately** — any agent, no role gate, no human review (skills go to a `.proposed` review queue; routines don't).
3. **No reuse on-ramp.** ~12 production recipes exist in seed data but the create dialog surfaces none of them; there's no "fork existing" or guided template.

## 2. Goals / Non-goals

**Goals**
- Make routine creation something a non-engineer can do safely, via **two surfaces over one DSL**: a Lead-agent **chat** (NL → agent authors + asks clarifying Qs) and a structured **form-builder**. JSON becomes "Advanced/source".
- Let **Lead (and any) agents propose routines from experience**; route the **risky** ones through a **human review gate** (maker-checker). "Agent proposes / human controls."
- Make **integration/credential needs a first-class, declarative contract** on the routine, validated at author-time and **blocked at run-time** if the running crew can't satisfy it.
- Rich **template gallery** + fork-existing + guided setup.
- Full **RBAC + admin airbag** (disable/kill, spend, egress, audit).

**Non-goals**
- A drag-and-drop node-graph builder à la n8n/Make (we don't win that race; visual is *read-only* comprehension at most).
- Replacing the DSL — JSON stays the canonical, versioned, diffable, replayable compile target.
- Per-end-user OAuth fan-out (multi-tenant external auth) — out of scope for v1.

## 3. Core thesis

**Routines are the crew's durable, reusable procedural memory** — "we worked this out, here's the repeatable recipe." Authored by humans (template/form/chat-assisted) **and** by Lead agents (crystallized from experience), shared workspace-wide, runnable by anyone whose crew can satisfy the integration needs. The governance spine is the same one that already powers **skills** and **credentials**: *the agent does the work and proposes; the human approves the risky parts.* This makes the Lead agent the operator of the crewship while humans keep full control.

## 4. Competitive grounding (what to steal / avoid)

Full cited research in session transcript; the load-bearing conclusions:

| Pattern | Steal from | Avoid (anti-pattern) |
|---|---|---|
| **"requires connection X" as a typed, declarative contract**, validated at author/deploy time | **Windmill Resource Types**; Gumloop per-node credential dropdown | Trigger.dev/Inngest/Temporal — fail only at *run* time on missing `process.env` (bad when an autonomous agent authored it and isn't watching) |
| **Per-node credential resolution: Personal-default / Team-default / Specific** | **Gumloop** (explicit, configurable) | **Make** — team-shared connection embedded by ID runs as the *builder* regardless of trigger; had to bolt on "Dynamic Connections" later |
| **Block activation on missing/invalid connection** (table stakes) | Make (`Missing __IMTCONN__`), Zapier ("can't turn on") | n8n's "publish fails" is fine too |
| **Bind credentials by reference; secrets never travel with the flow** | n8n / Make blueprints / Pipedream frozen links / Zapier "share a copy" | — (universal; maps to Crewship sidecar/stdin isolation) |
| **Pre-publish maker-checker approval gate** (genuine differentiator — the field barely has one) | **Zapier Enterprise "publishing restrictions"**; our own skills `.proposed` flow | n8n/Pipedream outsource to Git PRs; Make/Gumloop/Lindy have none |
| **AI/NL authoring = bounded draft generator → validate → human completes** | n8n (excludes creds from LLM context), Pipedream (schema-grounded), Gumloop "Gummie", Zapier Copilot | One-shot "AI builds the whole thing live" — every leader treats it as a draft only |
| **Separate AUTHOR/DEPLOY right from RUN right** | Temporal (namespace Read/Write/Admin), Windmill (Developer vs Operator) | — |
| **Split control-plane audit (who created/changed) from data-plane (who ran)** | Temporal | Make's audit even omits scenario lifecycle events |
| **Templates need guided setup + immutable-published + live-checkpoint versioning** | Zapier guided templates, Gumloop org templates, Make immutable publish, Gumloop "Make This Checkpoint Live" | — |

**Key insight:** Crewship is *already ahead* on the two things the field is weakest at — a review gate (skills `.proposed`) and agent authoring. We just haven't applied them to routines. Windmill (typed requirements + Developer/Operator + PR-promotion) is the closest reference; Gumloop is the model for credential binding.

## 5. Current state & gaps (ground truth, with citations)

| Capability | Today | Gap to close |
|---|---|---|
| RBAC create/save | MANAGER+ (`pipelines_crud.go:523`) | Allow anyone to **propose**; keep MANAGER+ for **publish/approve** |
| RBAC delete/rollback/cancel | OWNER/ADMIN (`pipelines_crud.go:108,425`, `pipeline_runs.go:38`) | Keep |
| RBAC **run** | **none** — any authed user (`router_pipelines.go:27`) | Keep open; add **integration gate** |
| Workspace visibility | `workspace_visible` bool, default 1 (`migrate_v78`) | Keep workspace-shared default + attribution |
| `agent_run` agent resolution | bound to author crew `(crew_id, agent_slug)` (`runner_orchestrator.go:299`) | This is the integration-binding hook |
| **Credential requirements on routines** | **ABSENT** (skills have it via `credential_requirements`, read at `agent_config.go:244`) | **ADD — mirror skills** |
| Composio attachment | agent/crew-level MCP (`agent_config.go:243`) | Add routine-level *requirement* + run check |
| Agent-authored routines | live immediately, no review (`/internal/pipelines/save`) | **ADD review gate** (skills `.proposed` is the model, `skills_author_handler.go:95`) |
| Chat infra | exists: `/internal/chats` create/resolve (`internal_chat.go:12`), `/chat/[agentSlug]` UI | Add **task-scoped "author a routine" chat mode** |
| Airbag | test-run gate, `max_cost_usd`, concurrency, run-cancel, dry-run, prompt-injection guardrails | **ADD per-routine disable/kill**, per-routine egress allowlist; **split audit** |

## 6. Design

### 6A. Routine requirements contract *(foundation)*
Add to the DSL a declarative `requirements` block, mirroring skills' `credential_requirements`:
```jsonc
"requirements": {
  "integrations": ["github", "slack"],     // Composio connector slugs
  "credentials":  ["STRIPE_API_KEY"]        // env-var names
}
```
- **Author-time validation** (`store.Save`): parse + record requirements; warn if the author crew can't satisfy them.
- **Run-time gate** (`pipelines_exec.Run`): before executing, resolve the running crew/agent's connected integrations + credentials; if a requirement is unmet → **block with a clear Problem Details error** ("routine needs `github`, not connected for crew Quality") + a **"request integration"** affordance. (Make/Zapier "can't turn on" pattern.)
- Surface requirements in the UI (create dialog, Overview, review card) as chips.

### 6B. Credential / runner binding
Steal Gumloop's explicit per-step resolution. For each `agent_run` step the credential resolves from the **running** crew's agent (per-runner default) — never a baked-in builder credential (avoid Make's footgun). Optional future: a `binding: specific|crew_default` knob per step. Secrets stay reference-only; export/share/version carry the requirement, never the value (maps to existing sidecar/stdin isolation).

### 6C. Smart review gate (maker-checker)
Anyone (human or agent) may **propose**. A **risk classifier** decides the path:
- **Auto-live (low risk):** only `transform` / `agent_run` steps, no new integration/credential, no egress, no `code` runtime.
- **Review-required (risky):** needs a **new integration/credential**, has **`http`/egress**, **`code`** runtime, or **creates a credential**.
Risky proposals land as **`status=proposed`** + an **inbox item** (`TargetRole=MANAGER`, blocking), reusing the skills `.proposed` flow and the **inbox/approval UI we just shipped** (PR #736). MANAGER+ approves → live; reject → discarded. Decision is audited.

### 6D. RBAC matrix
| Action | Who |
|---|---|
| Propose (chat/form/agent) | Any member; any agent (incl. LEAD) |
| Publish low-risk | Auto (after test-run gate) |
| Publish risky | MANAGER+ approval (inbox) |
| Run | Any workspace member, **subject to integration gate** |
| Edit / rollback | MANAGER+ (edit), OWNER/ADMIN (rollback) |
| Delete | OWNER/ADMIN |
| **Disable / kill (airbag)** | OWNER/ADMIN |

### 6E. Authoring surfaces (two, over one DSL)
1. **Chat authoring (Lead agent)** — "New routine" → pick **crew + Lead** + type the **goal**. Opens a **task-scoped chat** (reuse `/internal/chats` + `/chat/[agentSlug]`) seeded with an authoring system prompt; the Lead asks clarifying questions, then emits a **draft** via `/pipelines/save` → which now lands as `proposed` (6C), shown to the human as **readable text** (6F) to confirm. The Lead is the most capable agent — let it interrogate the requirement. *Bounded draft-generator, grounded in the DSL schema + auto `test_run`* — better determinism than n8n/Make's bar.
2. **Form-builder** — structured per-step-type forms (`agent_run` / `http` / `transform` / `wait` / `code`). No raw JSON. JSON = "Advanced/source" tab.
3. **DSL → readable text** — a pure renderer ("Input `url`. Step 1 Fetch GET `{url}`. Step 2 Agent summarizes…") used in create dialog, Overview, and the **review card** (so approvers never read JSON).

### 6F. Templates & reuse
Replace the 3 skeletons with a **gallery**: curated starters **+ the ~12 seed recipes** (`summarize-text`, `fetch-and-extract`, `extract-contacts`, `parse-log-line`, `classify-ticket`, `csv-to-json`, `normalize-dates`, `redact-secrets`, `json-schema-validate`, `pr-review-structured`, `invoice-extract`, `routing-decision`) categorized (Extract/Summarize/Classify/Transform/Fetch/Review) **+ "fork any workspace routine"**. Guided setup (Zapier/Gumloop) walks the user through filling editable fields + connecting required integrations. The more agents crystallize, the richer the library — a flywheel.

### 6G. Run model & cross-crew
Workspace-shared + discoverable (keep `workspace_visible` default 1, add author-crew attribution). Run is open (no RBAC, as today) but **gated on integration availability** (6A). Cross-crew run blocked only when the running crew lacks a required integration → "request access".

### 6H. Airbag / guardrails
- **Per-routine disable/kill switch** (ADD) — OWNER/ADMIN can deactivate a routine (and kill in-flight runs) while keeping history.
- **Per-routine egress allowlist** (ADD) — today only workspace/agent level.
- Reuse existing: test-run gate, `max_cost_usd`, concurrency keys, run-cancel, dry-run, prompt-injection guardrails.
- **Audit split (Temporal model):** control-plane (who proposed/approved/changed/disabled — low volume, durable) vs data-plane (who ran — high volume, export/journal).

## 7. Implementation waves

- **Wave 0 — Requirements contract (foundation, backend).** Add `requirements` to DSL + `store.Save` validation + `pipelines_exec.Run` integration gate + block-with-request error + UI chips. *Smallest, highest-leverage; directly fixes the "integration forgotten" hole; mirrors skills.*
- **Wave 1 — Governance gate (backend + reuse UI).** `proposed` status + risk classifier + inbox review (reuse PR #736 approval/inbox) + per-routine disable/kill + audit split. *Closes the live-without-review hole.*
- **Wave 2 — Chat authoring (backend + frontend).** Task-scoped authoring chat (pick crew+Lead+goal → draft → proposed) + DSL→readable-text renderer. *The path the user wants to start from.*
- **Wave 3 — Form-builder + template gallery (frontend).** Per-step forms; gallery with seed recipes + fork-existing + guided setup.
- **Wave 4 — Polish.** Live-checkpoint versioning, immutable-published templates, richer audit/observability.

> Sequencing note: chat authoring (W2) is only *safe* once the requirements contract (W0) and review gate (W1) exist — otherwise a chatted agent would push live, unvalidated routines. So W0→W1 precede W2 even though chat is the most exciting surface.

## 8. Open questions
- Per-step credential `binding` knob (crew-default vs specific) — defer to v2 or include in W0?
- "Request integration" — does it open a credential escalation (existing `PENDING_APPROVAL` flow) or just notify a MANAGER?
- Should LOW-risk agent proposals still drop a non-blocking inbox notice (visibility without friction)?
