# Design — Keeper configuration: making the judge settable, verifiable, and tool-independent

Status: draft · 2026-07-29 · Follows the M2a gov-model slice (#1001)

Keeper's architecture is sound and its enforcement works. Its **configuration surface does
not**: an operator who wants to run Keeper against their own Ollama — on the same host or on
a box across the LAN — cannot do it from the dashboard, and the way it fails does not tell
them why. Worse, three separate URL conventions share one credential type, so a
configuration that **passes our own test** can DENY every request in production.

This document specifies the endpoint contract, the configuration model, the CLI-adapter
independence story, and the test strategy that makes all three verifiable.

---

## 1. The problem, walked end to end

An admin opens **Admin → Keeper**, wants to point the judge at `http://192.168.1.40:11434`
and pick a model.

| Step | Expected | Actual |
|---|---|---|
| Enter an endpoint URL | A text field | **No field exists.** The only path is: leave the panel, create an `ENDPOINT_URL` credential in the vault, come back, pick it in a dropdown. Nothing says so. |
| Pick a model | A list of what the endpoint has | Free-text input (`keeper-governance-panel.tsx:622`). A typo is accepted and stored. |
| Verify it works | A Test button | **None.** The probe exists (`local_model_probe.go:36`) but only from a credential's detail sheet. |
| See it took effect | Status shows the judge | Shows the *server* URL/model only (`keeper-tab.tsx:113-123`). The API returns `gov_model_provider`, `gov_model_degraded`; the UI type omits them (`admin/types.ts:54`). |
| Requests flow | ALLOW/DENY per policy | **Every request DENYs.** Two independent causes, §2 and §3. |

The last row is the whole story. A correct configuration and a broken one are
indistinguishable from the dashboard, and both fixes need shell access to the server. For a
self-hosted product whose pitch is *"runs fully local, no API key"*, the local case is the
one that cannot be configured.

## 2. Root cause #1 — one credential type, three incompatible URL conventions

`ENDPOINT_URL` is consumed by three code paths that each expect a **different** URL shape,
and by a probe that tolerates all of them.

| Consumer | Builds request as | Correct stored value |
|---|---|---|
| OpenCode agent (`exec_env.go:460`, `OPENCODE_CONFIG_CONTENT`) | value used as OpenAI `baseURL` | `http://host:11434/v1` |
| `llm.Ollama` — gov provider `ollama` (`ollama.go:118`) | `value + "/api/chat"` | `http://host:11434` (**bare root**) |
| `llm.OpenAI` — gov provider `openai_compat` (`openai.go:172`) | `value` used verbatim as the POST target | `http://host:11434/v1/chat/completions` (**full path**) |
| `probeLocalModelEndpoint` (the Test button) | tries `{v}/models`, then `{v minus /v1}/api/tags` | **accepts all three** |

Our own documentation tells operators to create exactly the first shape
(`docs/guides/cli-adapters.mdx`, local-models section):

```bash
crewship credential create --name ollama-local --provider OLLAMA \
    --type ENDPOINT_URL --value http://host.docker.internal:11434/v1
```

Select that existing credential as the Keeper governance model with provider `ollama` and
the judge POSTs to `http://host.docker.internal:11434/v1/api/chat` → **404** → LLM call fails
→ fail-closed → **DENY on every credential request**. The credential's Test button reports
green throughout, because the probe strips `/v1` before trying `/api/tags`
(`local_model_probe.go:69`).

There is a second, sharper edge in that same value. `host.docker.internal` is correct for the
**agent**, which dials from inside a crew container. The **judge dials from the daemon**. Same
credential, two different network vantage points, and only one of them can be right. Nothing
in the UI, the CLI, or the docs distinguishes them.

**This is the single highest-value fix in this document**, and it is not a UI problem.

## 3. Root cause #2 — private addresses are blocked behind a server env var

`gov_model_resolver.go:242-268` builds the judge's dialer with
`httpsafe.IsBlockedIPForEndpoint(ip, allowPrivate)`, where `allowPrivate` reads the
server-wide `CREWSHIP_ALLOW_PRIVATE_ENDPOINTS`. Unset (the default), `192.168.1.40` and
`127.0.0.1` are refused at connect time. Keeper is fail-closed, so **a network policy
presents as a security verdict**.

The server-global `KEEPER_OLLAMA_URL` path deliberately bypasses this fence (it is
operator-configured and typically loopback, `ollama_discovery.go:19-21`). So loopback works
for the server default and not for a workspace override — the exact inversion of what an
operator would guess.

## 4. Root cause #3 — local models are OpenCode-only

`exec_env.go:437`:

```go
if req.CLIAdapter != "OPENCODE" || req.LocalModelBaseURL == "" {
    return "", false
}
```

An agent on any other adapter never receives the local-model provider block. `agents_update.go`
validates the `cli_adapter` **enum** but not the model↔adapter *combination*, so
`crewship agent update <slug> --llm-model ollama/qwen2.5-coder:7b` on a `CLAUDE_CODE` agent
is accepted, stored, and then **silently ignored at exec** — no error, no journal entry. The
UI hides the trap by only offering `ollama/…` in `OPENCODE_MODELS` (`lib/cli-adapters.ts:191`),
so the API and the dashboard disagree about what is configurable.

`localModelExtraDomains` gates on the same condition (`exec_env.go:523`), so in restricted
network mode the endpoint host is not allowlisted either.

## 5. Current state — two configuration layers wearing one coat

| | Server engine | Per-workspace governance |
|---|---|---|
| **Storage** | `cfg.Keeper` — env / YAML | `keeper_governance_settings` (v137) |
| **Fields** | `KEEPER_ENABLED`, `KEEPER_OLLAMA_URL`, `KEEPER_MODEL` | `enabled`, `security_contact_user_id`, `deny_notify_min_risk`, `require_second_approver`, `auto_lease_seconds`, `watch_spec`, `watch_presets`, `gov_model_provider/id/credential_id` |
| **Changing it** | Restart. Enabled without a model = the server refuses to boot. | Live, `PUT /api/v1/admin/keeper/governance` |
| **Governs** | Whether the gatekeeper exists at all (`server.go:633`) + the degrade judge | Which model decides — access gatekeeper **and** all four F4 evaluators (`internal/server/keeper_phase2.go:buildAuxGatekeeper`) |

Two load-bearing facts, neither obvious:

- **The workspace gov model already governs everything.** Access gatekeeper and all four
  Keeper Reviews evaluators resolve the same per-workspace model at request time. Configuration
  should therefore have *one* judge control, not one per subsystem.
- **`cfg.Keeper.OllamaURL` is not only Keeper's.** It also builds the episodic embedder
  (`server.go:471`) and the chat summarizer (`server.go:680`). Repointing it moves all three;
  moving the embedder silently invalidates stored vectors. Any runtime-tunable server URL must
  be a *new, judge-scoped* value, not an override of this one.

## 6. Gaps

| # | Gap | Evidence |
|---|---|---|
| G1 | Three incompatible URL conventions behind one credential type; probe accepts all | §2 |
| G2 | Judge dials from the daemon, agents from the container — same credential, different vantage | `exec_env.go:460` vs `gov_model_resolver.go:165` |
| G3 | Private/LAN/loopback judge endpoints behind a server env var; failure surfaces as DENY | `gov_model_resolver.go:242` |
| G4 | No endpoint field; a vault credential is the only path and is undocumented in-panel | `keeper-governance-panel.tsx:644-681` |
| G5 | Model is free text although live discovery exists | `models.go:142`, `ollama_discovery.go:89` |
| G6 | No test path for the judge | `local_model_probe.go` reachable only via `POST /credentials/{id}/test` |
| G7 | Resolved judge + degrade state not rendered | `keeper_status.go:52-58` returns it; `admin/types.ts:54` omits it |
| G8 | Local models are OpenCode-only; other adapters accept and silently ignore | `exec_env.go:437`, `agents_update.go:142` |
| G9 | Judge asks for JSON by prompt, not by schema; parse failure = DENY | `keeper.mdx:127-133` |
| G10 | Evaluator aux slots are YAML-only, read-only | `aux-status-section.tsx:113` |
| G11 | Reviews panel: no filter, no override, fixed 1-in-5 sampling, fixed cron times | `keeper-reviews-panel.mdx:186-192` |

**Not a gap:** findings routing. `keeper_request.go:398` writes an `inbox.KindEscalation` row
targeted at `SecurityContactUserID` with MANAGER+ fanout fallback on ESCALATE, and on DENY
when `risk >= deny_notify_min_risk`; the F4 evaluators use the same plumbing
(`keeper_phase2.go:473`), and `BroadcastInboxUpdated` pushes the badge live. This works. What
is missing is any way to *confirm* it works before an incident does it for you (§9.5).

## 7. Decisions taken

1. **Normalize the endpoint once, at the edge** — a wire-format-aware endpoint type, so no
   consumer parses a raw URL again (§8).
2. **Private-network judge endpoints become a per-workspace opt-in in the UI**, OWNER/ADMIN,
   journalled; the env var demotes to a ceiling (§10).
3. **Configuration lives at both levels with visible inheritance** — runtime-tunable instance
   default, workspace override, provenance on every field.
4. **The vault stays the store**; the UI mints the credential on the operator's behalf.
5. **Tool independence is asserted, not assumed** — the judge is daemon-side and must be
   proven adapter-invariant by test; the agent-side local model gets an explicit support
   matrix and a set-time rejection instead of a silent no-op (§11).

## 8. The endpoint contract (fixes §2)

New package `internal/llm/endpoint`:

```go
type Wire string

const (
    WireOllama           Wire = "ollama"             // POST {root}/api/chat
    WireOpenAIChat       Wire = "openai-chat"        // POST {root}/v1/chat/completions
    WireOpenAIResponses  Wire = "openai-responses"   // POST {root}/v1/responses
    WireAnthropicMessages Wire = "anthropic-messages" // POST {root}/v1/messages
)

type Endpoint struct {
    Root    *url.URL // scheme://host[:port] — no API path segments
    Wire    Wire
    APIKey  string
    Headers map[string]string
}

func Normalize(raw string) (Endpoint, error)      // any paste shape -> Root
func (e Endpoint) ChatURL() string                 // Root + the wire's path
func Detect(ctx, root) ([]Wire, error)             // probe which wires the server answers
```

`Normalize` accepts every shape an operator plausibly pastes — `http://h:11434`,
`http://h:11434/`, `http://h:11434/v1`, `http://h:11434/v1/chat/completions`,
`http://h:11434/api/chat` — and reduces all of them to the same `Root`. Every provider then
builds its URL from `Root` via `ChatURL()`, never from the stored string. **The `/v1` class of
bugs stops existing**, and "test green, production DENY" becomes unreachable rather than
merely unlikely.

`Detect` probes `/api/tags` (Ollama native) and `/v1/models` (OpenAI-compat) and returns what
answered, so the UI can say *"Detected: Ollama · native + OpenAI-compatible"* instead of making
the operator pick a provider blind.

Migration: `parseEndpointValue` keeps its current signature and gains a `Normalize` call, so
stored values are re-interpreted at read time. **No data migration** — an existing
`…:11434/v1` credential starts working for the judge on deploy instead of needing an edit.

Vantage-point clarity (G2): the stored value keeps serving the agent path unchanged. The judge
resolves from the same credential but records **which side dialled** in the test result, and
the test refuses `host.docker.internal` from the daemon with a specific message —
*"this hostname only resolves inside containers; the judge dials from the host — use
`localhost` or the host's LAN address"*. That one sentence is the difference between a
five-minute setup and an afternoon.

## 9. Configuration model

Three levels, resolved per request, each field independently:

```
built-in default  ←  instance runtime settings  ←  workspace override
   (nothing)          keeper_runtime_settings      keeper_governance_settings
```

Resolution returns value **and provenance** (`default` | `instance` | `workspace`), so every
control renders "inherited from server" vs "workspace override" with *Reset to inherited*.
This is what makes a two-level model tolerable rather than confusing.

### 9.1 Schema

**v169** — workspace override:

```sql
ALTER TABLE keeper_governance_settings
  ADD COLUMN allow_private_judge_endpoint INTEGER NOT NULL DEFAULT 0;
```

Endpoint and auth stay in the vault behind the existing `gov_model_credential_id` — reusing
the credential row keeps encryption, rotation, revoke-safety (`govmodel.go:119-133`) and the
existing test path, and avoids a second place a URL can rot.

**v170** — instance runtime settings (singleton row, `ratelimitcfg` pattern):

```sql
CREATE TABLE IF NOT EXISTS keeper_runtime_settings (
    id                  TEXT PRIMARY KEY CHECK (id = 'singleton'),
    judge_provider      TEXT NOT NULL DEFAULT '',
    judge_endpoint_url  TEXT NOT NULL DEFAULT '',
    judge_wire          TEXT NOT NULL DEFAULT '',
    judge_model         TEXT NOT NULL DEFAULT '',
    judge_model_digest  TEXT NOT NULL DEFAULT '',
    updated_by          TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at          TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
```

Empty string = fall through to `cfg.Keeper.*`, so every existing deployment keeps its current
behaviour after migration. `judge_endpoint_url` is **judge-scoped**: it does not repoint the
episodic embedder or the chat summarizer (§5).

`KEEPER_ENABLED` stays boot-time in this slice. Flipping the engine at runtime means
constructing the gatekeeper lazily and flipping `orch.SetKeeperEnabled` (`server.go:903`)
under live traffic — separate change, own failure modes, P1.

### 9.2 Resolver changes

`NewGovModelResolver` captures the default judge as a value (`gov_model_resolver.go:62-76`);
it takes a `keepercfg.Store` instead so the default is read per resolve.

- The build cache keys on `govModelFingerprint` (`:232`), which already includes
  provider/model/endpoint — an instance-level edit yields a new fingerprint, so no stale
  provider survives. No invalidation hook needed. Add `wire` to the fingerprint.
- `KeeperStatusHandler` holds `*config.KeeperConfig` directly (`keeper_status.go:17`); it gains
  the same store so the status card reports what is actually in force.

### 9.3 Judge request shape (fixes G9)

Ollama supports **structured outputs**: pass a JSON schema in `format` and the decoder is
constrained to tokens that fit it, which makes malformed JSON mechanically impossible
([Ollama structured outputs](https://ollama.com/blog/structured-outputs),
[docs](https://docs.ollama.com/capabilities/structured-outputs)). Our judge asks for JSON in
prose and DENYs when parsing fails (`keeper.mdx:127-133`) — so a chatty model is a
fail-closed outage.

The judge sends the verdict schema:

```json
{"type":"object",
 "properties":{"decision":{"type":"string","enum":["ALLOW","DENY","ESCALATE"]},
               "reason":{"type":"string"},
               "risk":{"type":"integer","minimum":1,"maximum":10}},
 "required":["decision","reason","risk"]}
```

as `format` (Ollama native) or `response_format: {type: "json_schema"}` (OpenAI-compat), with
a **one-shot retry without the field** when the server 400s on it — some OpenAI-compatible
servers reject unknown keys, and a judge that hard-fails on an older LiteLLM is a worse outcome
than an unconstrained one. The existing brace-scanning parser stays as the fallback path; the
schema removes most of its work rather than replacing it.

Also fixed at the request layer, all classifier hygiene:

| Setting | Value | Why |
|---|---|---|
| `temperature` | `0` | A security verdict should be reproducible; an audit trail of sampled decisions is not defensible. |
| `num_ctx` | explicit, from `/api/show` | Ollama's default context silently truncates. The judge prompt carries conversation history + watch spec + request. |
| prompt budget | ours, history-first eviction | If anything must be dropped it must be background, never the request under evaluation. |
| `keep_alive` | explicit | Default unloads after idle; the gatekeeper sits in the credential path and would pay a cold start on the first request after a quiet hour. |

### 9.4 Model pinning

`/api/show` returns the model's digest and capabilities. We record the digest at configure
time and surface a status warning when it changes under the same tag — the thing making
security decisions changed, which is audit-relevant. `/api/show` also lets us **refuse an
embedding-only model as a judge** at configure time (`nomic-embed-text` is in everyone's
Ollama and will never classify).

### 9.5 Findings verification

`POST /admin/keeper/findings/test` inserts one inbox item marked `test: true` through the
normal `inbox.Insert` path with real target resolution, then returns the recipient list it
resolved. Same code path, same visibility filter, real badge push — the only difference is a
payload flag and a "test" label. An operator can confirm the chain before an escalation does it
for them.

## 10. Private-network endpoints — the security call

Today: instance-wide env var, all-or-nothing, needs shell access.
Proposed: `allow_private_judge_endpoint` per workspace, OWNER/ADMIN, journalled.

**What it widens.** Exactly three dials, all against the *stored, admin-authored* judge
endpoint: the gov-model provider dial, the judge test probe, and model discovery for that same
endpoint. It does **not** touch crew egress — that stays behind the per-crew flag ANDed with
the instance ceiling (`exec_env.go:513`) — and no caller-supplied-URL path.

**What stays hard-blocked regardless.** `IsHardBlockedIP`: cloud metadata and link-local
(169.254/16 + IPv6 forms), multicast, unspecified. The opt-in moves the dial from
`IsBlockedIP` to `IsHardBlockedIP` (`httpsafe.go:160-165`) — the same two-tier shape already
used for crew endpoints, no new primitive.

**Honest risk statement.** This hands a workspace OWNER/ADMIN an internal-reachability oracle:
*"does something answer on 10.0.0.5:11434"*. Mitigations, by weight:

1. **Role.** OWNER/ADMIN is the instance operator in the self-hosted deployment this targets.
   In multi-tenant SaaS it is not — which is why the env var remains a **ceiling**: set
   explicitly to false, the workspace toggle renders disabled ("your instance operator has
   disabled private endpoints"). Unset (today's default) leaves the workspace free, so
   self-hosted works out of the box and hosted operators have one flag to close it.
2. **Rate limit.** New `keeper.judge_test_per_min` key in the `ratelimitcfg` registry
   (default 6/min), same family as the credential-test bucket.
3. **Audit.** Enabling the toggle and every judge-endpoint change emits a journal entry with
   the acting user — same treatment the watch spec gets.
4. **No response reflection.** The test result reports a fixed vocabulary (reachable /
   refused / timeout / no model list / model missing / wrong wire) and never echoes response
   bodies, so it detects services rather than reading them.

Upstream guidance is worth stating in the docs, because operators reach for `0.0.0.0` first:
Ollama has **no native authentication**, so anything that can reach port 11434 can run
prompts, list, and delete models. Recommend binding to a specific LAN IP rather than
`0.0.0.0`, or fronting it with a reverse proxy that adds auth
([securing Ollama](https://localaimaster.com/blog/securing-ollama-guide),
[LAN exposure strategies](https://www.icertglobal.com/community/secure-local-network-access-for-ollama-api)).
Our `ENDPOINT_URL` already carries a bearer token and custom headers and already requires
https for them outside loopback/RFC1918 (`credentials_types.go:281-291`), so the proxy path is
supported today — it just is not documented for the judge.

Alternative considered and rejected: keep it env-only with a better error. Honest, but leaves
the product unconfigurable for the operator who cannot SSH into the box — the case that
motivated this work.

## 11. CLI-tool independence

Two different claims, and only one of them is currently true.

### 11.1 The judge is tool-independent by construction

The gatekeeper and all four evaluators run **in the daemon**. Nothing on the Keeper path reads
`cli_adapter`: the request carries agent/crew/credential/intent, and the evaluator prompt is
assembled from workspace governance. An agent on Claude Code and an agent on Codex get the
same judge, the same model, the same verdict.

This is an *invariant we rely on*, not merely a fact, so §12.3 turns it into a test that fails
if anyone ever threads the adapter into the keeper path.

### 11.2 The agent-side local model is not — support matrix

Each vendor CLI exposes a different lever, and two of them cannot speak to Ollama directly at
all:

| Adapter | Local-model lever | Wire it needs | Status |
|---|---|---|---|
| `OPENCODE` | `OPENCODE_CONFIG_CONTENT` provider block | OpenAI chat | **Works today** (`exec_env.go:436`) |
| `CODEX_CLI` | `~/.codex/config.toml` `[model_providers.*]` + `--oss` / `--local-provider` | OpenAI chat — Codex defaults to the **Responses** API, so `wire_api = "chat"` is required or the call 404s ([Codex provider config](https://www.morphllm.com/codex-provider-configuration), [Ollama × Codex](https://docs.ollama.com/integrations/codex)) | **Addable** — config file write, same mechanism as `SetupSystemPrompt` |
| `FACTORY_DROID` | BYOK custom-model config | OpenAI chat | **Addable** — config file write |
| `CLAUDE_CODE` | `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN` + `ANTHROPIC_MODEL` | **Anthropic Messages** — Claude Code only speaks Anthropic's shape, so raw Ollama 404s; needs a translating proxy (LiteLLM) in front ([guide](https://medium.com/@michael.hannecke/connecting-claude-code-to-local-llms-two-practical-approaches-faa07f474b0f)) | **Proxy-only** |
| `GEMINI_CLI` | none documented in headless mode | — | **Unsupported** |
| `CURSOR_CLI` | none | — | **Unsupported** |

The `Wire` field from §8 is exactly what makes this expressible: an operator who already runs
LiteLLM stores that endpoint with `wire: anthropic-messages` and Claude Code agents work with
**no new code** — we render `ANTHROPIC_BASE_URL` from `Root` instead of an OpenCode provider
block. We do not bundle or install LiteLLM; note in docs that two published releases
(1.82.7, 1.82.8) shipped a credential-stealing payload, so operators should pin and verify
what they run.

Three tiers of work:

- **Tier 1 (P0, cheap, high value).** Refuse at set time. `agents_create`/`agents_update`
  reject an `ollama/…` model on an adapter with no local path, naming the adapters that do
  work and the proxy option. Silent-ignore becomes an explicit error. This is a ~20-line change
  plus a table test and it removes an entire class of "why is my agent not using my model".
- **Tier 2 (P1).** Native injection for `CODEX_CLI` and `FACTORY_DROID` — per-adapter config
  file written into the container before exec, driven by one `Endpoint`. Extend
  `localModelExtraDomains` to the same set so restricted-mode egress is allowlisted.
- **Tier 3 (P2).** `wire: anthropic-messages` rendering for `CLAUDE_CODE`, documented as
  "point us at your LiteLLM". A sidecar-side translation shim is possible later — the sidecar
  already reverse-proxies `/v1` in API-key mode — but it is a real protocol-translation surface
  and should not be the first move.

## 12. Test strategy

The rule is tests before code, and the harness is the universal use case. Concretely, per
layer, with **what each layer is uniquely able to catch**.

### 12.1 Unit — Go

| Suite | Locks |
|---|---|
| `endpoint_normalize_test.go` | Table over every paste shape × every wire: bare root, trailing slash, `/v1`, `/v1/chat/completions`, `/api/chat`, uppercase scheme, IPv6 literal, port-less, query string preserved, userinfo rejected. **This table is the regression test for §2.** |
| `endpoint_wire_test.go` | `ChatURL()` per wire; a wire never produces a doubled or missing path segment. |
| `keepercfg_resolve_test.go` | Three-level precedence and provenance per field; empty instance row falls through to `cfg.Keeper`; workspace override wins; reset restores inheritance. |
| `gov_model_resolver_test.go` (extend) | Private address refused with the toggle off, permitted with it on; hard-blocked ranges refused in **both** states; env ceiling set-false overrides the workspace toggle; degrade still fires with the store in place; `wire` participates in the fingerprint so an edit rebuilds. |
| `gatekeeper_prompt_budget_test.go` | Eviction order under a small `num_ctx`: history drops first, the request under evaluation never does. |
| `gatekeeper_schema_test.go` | Schema sent on the native path; `response_format` on the compat path; a 400 on the schema field triggers exactly one retry without it, and the fallback parser still yields a verdict. |

### 12.2 Contract — a fake Ollama

`internal/llm/endpoint/fakeollama` — an `httptest` server that speaks the real surface
(`/api/tags`, `/api/show`, `/api/chat`, `/v1/models`, `/v1/chat/completions`) **and its
failure modes**, so the whole matrix runs in CI with no GPU:

| Fault | Must produce |
|---|---|
| `/v1/*` 404 but `/api/tags` OK (plain Ollama) | detected as `ollama` wire, judge works |
| `/api/chat` 404 (value stored with `/v1`) | **test stage 3 red with "wrong wire"**, never a silent DENY |
| model absent from `/api/tags` | stage 2 red: "endpoint reachable, model not pulled" |
| model returns prose, not JSON | stage 3 red: "model did not return a verdict" |
| model is embedding-only in `/api/show` | configure-time rejection |
| 5 s delay | stage 3 reports latency; no wedged request |
| connection reset mid-response | error surfaced, judge degrades, no panic |
| digest changed under the same tag | status warning |

This suite is the difference between "we think the test catches it" and knowing.

### 12.3 Adapter-independence matrix

Parameterized over all six `cli_adapter` values:

1. **Judge invariance** — identical gatekeeper input under each adapter yields an identical
   decision, prompt, and model resolution. Plus an architecture assertion that the keeper
   packages never read `cli_adapter` (grep-style test over `internal/keeper/**` and the keeper
   API handlers), so the §11.1 invariant cannot be quietly broken.
2. **Set-time rejection** — `ollama/…` on `GEMINI_CLI` / `CURSOR_CLI` / `CLAUDE_CODE`
   (without an anthropic-messages endpoint) is a 400 naming the working alternatives; on
   `OPENCODE` it is accepted.
3. **Injection shape** — where supported, the rendered config (OpenCode provider block, Codex
   TOML, Droid JSON) contains a URL built from `Root`, never the raw stored string, and the
   endpoint host lands in `localModelExtraDomains`.

### 12.4 Frontend — Vitest

Provenance chips and *Reset to inherited*; private-address form error before save; the
three-stage checklist rendering each failure shape from §12.2; degrade banner from
`gov_model_degraded`; the model picker falling back to free text when discovery fails.

### 12.5 E2E — Playwright

One flow, the one this document exists for: from an empty workspace to a green three-stage
test without leaving the panel. Fixture serves the fake Ollama, so it runs in CI.

### 12.6 Runtime harness — the real CLI

Per project rule, this is where we prove it as an application. New
`scripts/test-harness/test-keeper-judge.sh`, driving the real binary, skipping cleanly when no
Ollama is present:

```
crewship keeper config set --endpoint … --model … --wire ollama
crewship keeper judge test                 # assert 3 stages green, assert latency reported
crewship keeper judge models               # assert the pulled model is listed
crewship keeper config set --model bogus:1b
crewship keeper judge test                 # assert RED at stage 2 — before production finds out
crewship keeper allow-private disable
crewship keeper judge test                 # assert RED with the private-endpoint reason, not a generic failure
```

Then the part that matters most: force a real credential ESCALATE and assert it lands in the
inbox with the right target, and that `keeper requests` shows the decision with the configured
model. Extend `test-ollama-local.sh` with the Tier-1 adapter matrix (accepted on OpenCode,
rejected with a useful message elsewhere). `test-keeper.sh`, `test-credentials.sh`,
`test-notifications.sh` must stay green — the inbox path is untouched by design, and a
regression there means the resolver change leaked.

### 12.7 CI placement

The fake-Ollama contract suite and the adapter matrix run on every PR (no GPU, seconds). The
real-Ollama harness runs nightly on the dev VM, where a model can actually be pulled — and
per the known trap, a scheduled workflow with no alerting is a workflow nobody watches, so it
reports into the same channel as the rest of the nightly suite.

### 12.8 What goes in the PR description

```
## How this was tested

- [ ] Unit: endpoint normalization table (12 paste shapes × 4 wires) — the §2 regression
- [ ] Contract: fake-Ollama fault matrix (8 faults, each mapped to its user-facing message)
- [ ] Adapter matrix: judge decision invariant across all 6 adapters; keeper never reads cli_adapter
- [ ] Vitest: provenance, private-address form error, 3-stage checklist, degrade banner
- [ ] Playwright: empty workspace → green judge test without leaving the panel
- [ ] Harness (dev VM, real Ollama): test-keeper-judge.sh green; test-keeper / credentials /
      notifications still green
- [ ] Manual: stored `…:11434/v1` credential (today's documented value) now judges correctly
      instead of DENYing — the exact bug this PR exists to kill
```

## 13. Surfaces

### 13.1 API

| Method | Path | Role | Purpose |
|---|---|---|---|
| `GET` | `/api/v1/admin/keeper/config` | ADMIN+ | Effective judge config, per-field `{value, source, editable}` |
| `PUT` | `/api/v1/admin/keeper/config` | OWNER/ADMIN | Instance defaults, partial update |
| `PUT` | `/api/v1/admin/keeper/governance` | OWNER/ADMIN | Extended: `allow_private_judge_endpoint`; unchanged partial-update semantics |
| `POST` | `/api/v1/admin/keeper/judge/test` | OWNER/ADMIN | Three-stage verification. Empty body tests what is saved; a body tests unsaved values pre-commit |
| `GET` | `/api/v1/admin/keeper/judge/models` | ADMIN+ | Live model list from the configured (or supplied) endpoint |
| `POST` | `/api/v1/admin/keeper/findings/test` | OWNER/ADMIN | Synthetic finding + resolved recipient list |

`GET /config` redacts the endpoint through `redactUrl`, so a URL carrying auth never
round-trips to the browser in full.

### 13.2 Test connection — three stages

1. **Reach** — `Detect` + `probeLocalModelEndpoint`: which wires answer, how many models.
2. **Model present** — is the configured model in that list, is it a chat model
   (`/api/show`), what is its context length and digest. Separates *"endpoint down"* from
   *"endpoint up, model never pulled"* — the most common real failure, today indistinguishable.
3. **Smoke verdict** — a fixed miniature gatekeeper prompt with the verdict schema attached.
   Require a parseable verdict; report decision, latency (cold vs warm), and tokens.

Stage 3 is not ceremony. A model that answers in prose DENYs everything in production; a 0.5B
model passes stages 1 and 2 and still cannot classify. This is the check that catches it at
configuration time instead of at 3 a.m.

### 13.3 UI — Admin → Keeper

**Judge.** Mode (Local Ollama / Anthropic / OpenAI-compatible / Inherit) · endpoint URL with
`http://localhost:11434` placeholder and detected-wire badge · auth (none / from vault, with a
*save as credential* checkbox so nothing must be pre-created) · model as a discovery-backed
select with *Refresh* and free-text fallback · **Test connection** with the three-stage
checklist · the private-network toggle, shown only when the entered address is private, with
the reason inline. Provenance chip + *Reset to inherited* on every field.

**Policy.** Today's governance panel, behaviour unchanged.

**Findings & routing.** Security contact, resolved *"this will reach: …"* preview,
**Send test finding**, inbox link.

**System status.** Resolved gov model, provenance, degrade banner — data the API already
returns and the UI drops today.

### 13.4 CLI parity

```
crewship keeper config get|set|reset      # --provider --endpoint --wire --model
crewship keeper judge test                 # three stages, exit non-zero on red
crewship keeper judge models
crewship keeper allow-private enable|disable
crewship keeper findings test
```

`crewship keeper status` gains the provenance line and `crewship doctor` gains the judge probe
alongside the existing `ENDPOINT_URL` check.

## 14. Settings inventory

| Setting | Scope | Storage | Default | Editable by | Restart |
|---|---|---|---|---|---|
| Keeper engine enabled | instance | `KEEPER_ENABLED` | off | server operator | **yes** (P1 removes) |
| Judge provider | instance / workspace | `keeper_runtime_settings` / `keeper_governance_settings` | inherit → `cfg.Keeper` | OWNER/ADMIN | no |
| Judge endpoint URL | instance / workspace | as above (workspace via vault credential) | `cfg.Keeper.OllamaURL` | OWNER/ADMIN | no |
| Judge wire format | instance / workspace | as above | detected | OWNER/ADMIN | no |
| Judge model | instance / workspace | as above | `cfg.Keeper.Model` | OWNER/ADMIN | no |
| Judge model digest | instance | `judge_model_digest` | recorded at test | system | no |
| Judge auth (key/headers) | workspace | vault `API_KEY` / `ENDPOINT_URL` | none | OWNER/ADMIN | no |
| Allow private judge endpoint | workspace (env = ceiling) | `allow_private_judge_endpoint` | off | OWNER/ADMIN | no |
| Watchdog enabled | workspace | `enabled` | off | OWNER/ADMIN | no |
| Security contact | workspace | `security_contact_user_id` | MANAGER fanout | OWNER/ADMIN | no |
| DENY notify risk threshold | workspace | `deny_notify_min_risk` | 7 | OWNER/ADMIN | no |
| Require second approver | workspace | `require_second_approver` | off | OWNER/ADMIN | no |
| Auto-issue lease TTL | workspace | `auto_lease_seconds` | 0 (off) | OWNER/ADMIN | no |
| Watch presets | workspace | `watch_presets` | none | OWNER/ADMIN | no |
| Custom watch rules | workspace | `watch_spec` (≤4096) | empty | OWNER/ADMIN | no |
| Evaluator aux slots | instance | YAML `auxiliary.*` | anthropic → local judge | server operator | yes (P1) |
| Behavior sampling rate | instance | `behaviorhook.SetSampleEvery` | 1-in-5 | code | yes (P2) |
| F4 cron times | instance | scheduler | 03:00 / 03:30 UTC | code | yes (P2) |

## 15. Phasing

**P0 — the operator can configure, verify, and trust the judge**
1. `internal/llm/endpoint` — normalize + wire + detect, wired into both providers (§8)
2. `keeper_runtime_settings` (v170) + `keepercfg` store + resolver reads it
3. `allow_private_judge_endpoint` (v169) + fenced dial honours it + env ceiling
4. `POST /admin/keeper/judge/test` — three stages
5. Structured-output schema + `temperature 0` + `num_ctx` + prompt budget (§9.3)
6. `GET /admin/keeper/judge/models` + discovery-backed picker
7. Judge card with provenance, endpoint field, transparent credential minting
8. Status card renders gov model + degrade
9. Tier-1 adapter rejection (§11.2)
10. CLI: `config get/set/reset`, `judge test`, `judge models`, `allow-private`
11. Docs: rewrite the Keeper configuration section; add the per-adapter local-model matrix to
    `cli-adapters.mdx`

**P1** — runtime engine enable without restart · Codex + Droid local-model injection ·
per-workspace aux slots (or collapse them into "all evaluators use the judge") ·
`findings test` + recipient preview.

**P2** — Claude Code via `anthropic-messages` wire · Reviews panel filter/search, per-row
override, configurable sampling rate and cron times.

## 16. Open questions

1. **Per-crew judge override.** Not proposed — workspace granularity matches how governance is
   scoped today, and per-crew adds a fourth resolution level. Revisit on demand.
2. **Ollama auto-discovery on localhost.** Probing `127.0.0.1:11434` on first open and offering
   *"found Ollama with 3 models — use it?"* removes the last typing step. Cheap, but it is a
   server-side dial at page load; deferred pending a call on whether that is acceptable
   unprompted.
3. **Multi-tenant default.** The env ceiling is unset-permissive so self-hosted works out of
   the box. If the hosted offering ships first, that default inverts.
4. **Sidecar translation shim** for Claude Code — real product value (no LiteLLM to run), real
   protocol-translation surface to own. Explicitly out of scope here.

## Sources

- [Ollama — structured outputs](https://ollama.com/blog/structured-outputs) ·
  [docs](https://docs.ollama.com/capabilities/structured-outputs)
- [Securing Ollama: auth, TLS, network isolation](https://localaimaster.com/blog/securing-ollama-guide)
- [Exposing the Ollama API on a LAN](https://www.icertglobal.com/community/secure-local-network-access-for-ollama-api)
- [Codex provider configuration](https://www.morphllm.com/codex-provider-configuration) ·
  [Ollama × Codex CLI](https://docs.ollama.com/integrations/codex)
- [Claude Code with local LLMs via a translating proxy](https://medium.com/@michael.hannecke/connecting-claude-code-to-local-llms-two-practical-approaches-faa07f474b0f)
