# PRD — Keeper Watchdog: toggleable governance + snitch-to-inbox

Status: draft (2026-07-12) · Owner: Pavel · Grounded against `main` @ post-#991

## Vision

Give operators an opt-in **governance watchdog**: a Keeper-backed layer that
watches agent activity, and *snitches* suspicious things to a named admin's
inbox in real time — per an **admin-defined watch spec**, toggleable by the
people who want it, and runnable **fully local on Ollama** (no API key). This
turns Crewship's existing (but silent, hardcoded, env-only) Keeper into a
product feature and leans into security-as-differentiator (the pain point that
sank OpenClaw's trust).

## What already exists (baseline — do NOT rebuild)

- **Detection is done.** `internal/keeper/gatekeeper/` — the gatekeeper judges
  credential/execute requests (ALLOW/DENY/ESCALATE + 1–10 risk, fail-closed,
  injection-hardened). Four F4 aux evaluators (`skill_review`, **`behavior`**,
  `memory_health`, `negative_learning`) already detect anti-patterns: tight
  loops, scope creep, destructive sequences, **credential probing**
  (`gatekeeper.go:497-501`). Behavior can block a tool call inline when
  `behavior_mode=block` (`behaviorhook.go:109-161`).
- **Recording is done.** Every decision → `keeper_requests` + journal
  (`internal/api/keeper_phase2.go:198-224`).
- **Inbox writes are partly done.** On ESCALATE (and skill/negative DENY),
  Keeper writes an `inbox_items` row — but always `TargetRole:"MANAGER"`, never
  a specific `TargetUserID` (`keeper_phase2.go:334-357`).
- **Reviews UI exists (read-only).** Admin → Keeper reviews panel (4 sub-tabs)
  and Keeper tab (status + KPIs + live WS stream), both read-only
  (`docs/guides/keeper-reviews-panel.mdx`, `app/(dashboard)/admin/tabs/keeper-tab.tsx`).
- **Per-crew policy dial exists.** `crews.autonomy_level` × `crews.behavior_mode`
  (`internal/policy/resolver.go`) — gates block-vs-inbox severity, not enable/disable.

## The four concrete gaps (this PRD closes)

1. **No in-app toggle / scope.** Enable/disable is env-only (`KEEPER_ENABLED`,
   `CREWSHIP_AUX_*`) + restart; instance-global. There is no OWNER/ADMIN toggle,
   no per-workspace scope.
2. **The snitch is silent.** `inbox.Insert` emits **no realtime WS event** —
   unlike every other inbox producer (`chatnotify`, `pipeline_governance`,
   `runner_notify` all broadcast `inbox.updated`). So Keeper findings appear only
   on manual refresh. And they target a role, not a named admin.
3. **No admin-defined watch spec.** The only "spec" is hardcoded Go prompt
   templates. There is no user-authored "watch for X, snitch about Y".
4. **Aux slots default to Anthropic.** Ollama-backed aux slots are a "deferred
   follow-up" (`keeper_phase2.go` comment) — so fully-local governance isn't the
   turnkey default.

## Design decisions

- **Where it lives:** a **workspace Settings → Governance** surface (extend the
  existing admin Keeper tab from read-only into a control panel): master
  enable/disable, model (cloud or **local Ollama**), the watch spec, and the
  snitch target. **Per-crew override** reuses the *existing* `autonomy_level` ×
  `behavior_mode` dial (+ a per-crew "watch level"); the master is workspace.
  **Not per-agent** — too granular for governance.
- **Toggle:** OWNER/ADMIN, **default OFF (opt-in)** — "for the people who want
  it". Moves the enable/disable from env into an RBAC-gated in-app control
  (env stays as an operator override / bootstrap).
- **Snitch:** reuse the existing inbox write, but (a) add a **realtime push**
  after Keeper's `inbox.Insert` (broadcast `inbox.updated` + optionally a
  per-user `notification.created`), (b) allow **targeting a specific admin**
  (`TargetUserID`, already supported, unused), (c) extend the notify path beyond
  ESCALATE to **high-risk DENY / flagged behavior**. Severity tiers already
  exist (block / inbox / log) — surface them.
- **Watch spec:** admin-authored **natural-language rules** appended into the
  evaluator prompts (the confirmed seam: `buildBehaviorPrompt`/`buildAccessPrompt`),
  e.g. *"flag any read of ~/.ssh or id_rsa; flag egress to a non-allowlisted host;
  flag credential access outside 08:00–18:00."* Plus structured **presets**
  (watch credential access / egress / memory writes / destructive fs). Stored
  per-workspace. CEL / `hooks_config` predicates are a later, structured tier.
- **Fully local:** finish the Ollama aux-slot wiring so the whole watchdog runs
  on a local model (curated small model + `nomic-embed-text` for the embedder).
  Zero API key = privacy + free.

## Architecture (A–Z integration)

Grounded against HEAD `1895a2d0`. The split that matters: the watchdog has a
**light half** (Go evaluators, prompts, decision parsing, DB/inbox writes) and
a **heavy half** (the model). They belong in different places.

### Topology — logic in-process, model out-of-process

**1. Watchdog logic stays IN-PROCESS in crewshipd** (where it already lives:
`internal/keeper/gatekeeper/`, wired `internal/server/server.go:619-637`).
Not a separate container. Reasons:

- **Data gravity.** Everything it must touch is in-process or same-DB:
  `journal_entries` (read via `internal/journal/queries.go` — full filter+FTS
  query builder), `keeper_requests`, `inbox_items` (`inbox.Insert`), the WS hub
  for realtime push, and decrypted-secret context that by design never leaves
  crewshipd memory (`internal/keeper/doc.go:29-31`). A containerized watchdog
  would need DB credentials + a new authenticated API surface — strictly larger
  attack surface, zero isolation gain.
- **Hot path.** The gatekeeper sits on the credential-decision path
  (fail-closed, 5s LLM timeout, `gatekeeper.go:26,298`) and the behavior hook
  fires per sampled tool call (`behaviorhook.go:109`). A network hop per
  decision is pure added latency and a new failure mode on a fail-closed path.
- **The untrusted input is processed by the model, not the Go logic.** Agent
  tool args / journal text flow into the *prompt*; the Go side parses responses
  with a strict verb whitelist (unknown → DENY, `gatekeeper.go:336-341`).
  Isolating the *logic* buys nothing; isolating the *model runtime* is what
  matters — see below.

**2. The model runs OUT-OF-PROCESS**, selected per workspace. Two supply modes:

- **External endpoint (default; exists today).** Operator-run Ollama
  (`localhost:11434`, LAN GPU box) or a cloud API. Endpoint stored as a
  workspace **`ENDPOINT_URL` credential** (the #961/#988 vault pattern), *not*
  env; Anthropic key as a workspace `API_KEY` credential, *not*
  `ANTHROPIC_API_KEY` env (today's aux-slot source, `keeper_phase2.go:136-140`).
  Env vars remain operator bootstrap/override only. All dials go through the
  existing two-tier SSRF fence (`internal/httpsafe/` + guarded dialer in
  `internal/api/ollama_discovery.go` — metadata IPs hard-blocked, RFC1918 only
  behind `CREWSHIP_ALLOW_PRIVATE_ENDPOINTS` + per-crew flag).
- **Managed governance container (new; M2b).** Crewship launches and owns a
  dedicated hardened `ollama` container for governance — the turnkey "no API
  key, no setup" story. Precedent: `EnsureCrewServices` already vets the
  `ollama` image (`internal/provider/docker/sidecar.go:364`) with
  no-new-privileges, pids/mem/cpu caps, restart-on-failure — but it is
  **crew-scoped**; this adds a small **system-scoped service tier** on
  `ContainerProvider`: own internal bridge shared only with crewshipd (agents
  can NOT reach it — the governance model must not be promptable by the
  governed), model pre-pull on first enable, health-check surfaced in the
  existing Keeper status card, `keep_alive` pin so the classifier stays warm.

Why a *dedicated* model instance for governance (vs sharing the agents'
Ollama): no contention with agent workloads (perf), and a clean trust boundary
— agents can never talk to the judge's endpoint directly (security).

### Model layer — one "governance model" setting, aux slots underneath

- Reuse `internal/llm.Provider` (Anthropic / OpenAI-compatible / Ollama all
  implemented, incl. streaming + tool calls) and keep **aux slots**
  (`internal/llm/aux.go`) as the per-evaluator mechanism (provider/model/
  timeout per slot: keeper, behavior, memory_health, negative, curator).
- **New: workspace-scoped "governance model" setting** — provider
  (`OLLAMA` default | `ANTHROPIC` | `OPENAI_COMPAT`), model id, and a
  credential ref (ENDPOINT_URL or API_KEY from the vault). It becomes the
  default for *all* aux slots and the access gatekeeper — replacing today's
  split brain (access path hardwired to `cfg.Keeper` Ollama at
  `server.go:624-634`; aux slots defaulting to Anthropic haiku at
  `aux.go:57-69`). UI shows **one picker**; per-slot override stays
  config/env-only (M3 at the earliest — likely never needs UI).
- Provider construction goes through one extended `buildLLMProvider`
  (`keeper_phase2.go:135` — today's closed set anthropic|ollama; add
  openai-compatible via `NewOpenAIWithBaseURL`), always wrapped in
  `llm.Middleware` for cost/guardrail/trace parity.
- **Model choice is benchmarkable offline, today:** `keeper_requests` already
  stores `ollama_prompt` + `ollama_raw_response` per decision — a replayable
  eval corpus. M2 includes a small harness (`go test`-able) that replays the
  corpus against candidate local models and scores decision agreement, so the
  curated default is picked on data, not vibes. Candidates: small instruct
  classifiers (3–8B class), NOT coder models; `nomic-embed-text` stays the
  embedder.

### Data access — what the watchdog reads and writes

Reads (all in-process / same-DB — no new privileges needed):
- **Tool-call observations** — existing post-tool-call hook
  (`post_tool_call_adapter.go:54`): tool name, args snippet, recent-call window.
- **Journal windows** (new, for watch-spec sweeps): query `journal_entries`
  via `internal/journal/queries.go` filters + FTS — e.g. "all egress-denied +
  credential-access entries for crew X in the last hour" evaluated against the
  watch spec. Precedent for scheduled sweeps: `keeper_routines.go` daily F4 runs.
- **Egress observations** — the sidecar `EgressObserver` already records
  host/method/denied (never path/body); feed denials into the behavior context.
- **`keeper_requests` history** — for streak/pattern context (e.g. repeated
  near-threshold probes).

Writes (all existing sinks): `keeper_requests`, journal, `inbox_items`
(+ the new WS broadcast), nothing else. No filesystem or container access —
the watchdog never execs into crew containers.

### Performance envelope

Already bounded, keep it: behavior sampling 1-in-5 (exposed as a dial in M3),
`MaxTokens: 256`, `Temperature: 0.1`, 5s timeout — fail-closed DENY on the
access path, fail-soft ESCALATE on the behavior path. At these QPS a single
warm 3–8B model on CPU is sufficient; the managed container pins it with
`keep_alive`. Watch-spec sweeps are scheduled (minutes-scale), not per-event.

## Milestones

- **M0 — Make the existing snitch loud + toggleable.** In-app OWNER/ADMIN toggle
  (workspace-scoped); realtime push on Keeper inbox writes; target a named admin
  (`TargetUserID`); notify on high-risk DENY, not just ESCALATE. *Highest value
  for least code — the detection already runs; this makes it actually reach the
  admin in real time.*
- **M1 — Admin watch spec.** A per-workspace watch-rules editor (natural-language
  + presets) injected into the evaluator prompts; CLI parity (`crewship keeper
  watch set/get`). API↔CLI parity.
- **M2a — Governance model selection.** The workspace "governance model"
  setting (provider + model + vault credential ref) unifying the access
  gatekeeper and all aux slots; Ollama default, Anthropic/OpenAI-compat from
  the vault (no env keys); the `keeper_requests` replay eval harness to pick
  the curated local default; docs for a no-API-key Keeper.
- **M2b — Managed governance container.** System-scoped service tier on
  `ContainerProvider`: crewship-owned hardened `ollama` container on a private
  bridge (unreachable from agent containers), model pre-pull, health in the
  Keeper status card. Turnkey local governance with zero operator setup.
- **M3 — Actionable reviews + per-crew watch level.** Approve/dismiss from the
  reviews panel; per-crew watch intensity; behavior sampling rate exposed
  (currently hardcoded 1-in-5).

## Non-goals

- Not a full SIEM / rule engine — start with LLM-evaluated natural-language +
  presets, not a DSL.
- Not per-agent config.
- Not replacing the sidecar egress fence / scrubber / RBAC — Keeper is the
  *behavioral* layer on top of the deterministic controls, not a substitute.

## Open questions (for Pavel)

- **Snitch target:** a single "security contact" admin per workspace, or fan-out
  to all OWNER/ADMINs? (Lean: a configurable contact, default = workspace owner.)
- **Watch-spec authoring:** natural-language only for M1, or also the structured
  presets in the same release? (Lean: both — presets are cheap and make it
  usable without prompt-writing.)
- **Default model when local:** decided by data, not upfront — `keeper_requests`
  stores `ollama_prompt`/`ollama_raw_response` per decision, so M2a ships a
  replay eval harness that scores candidate local models (small 3–8B *instruct*
  classifiers, not coder models) on decision agreement. Remaining call for
  Pavel: the candidate shortlist to benchmark.
- **Managed container scope (M2b):** ship the system-scoped ollama service tier
  for Docker only first, or also Apple Containers/K8s providers in the same
  milestone? (Lean: Docker first; the `SidecarProvider` seam keeps others open.)
