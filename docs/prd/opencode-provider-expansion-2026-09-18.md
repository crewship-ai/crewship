# OpenCode provider expansion — supplemental analysis and implementation brief

Date: 2026-09-18. Follow-up to [Go/Zen PRD](opencode-go-zen-integration-2026-09-16.md) and [acceptance handoff](opencode-go-zen-handoff-2026-09-18.md), attached to PR #2619. **This is proposed follow-up scope, not functionality shipped by that PR.** User priorities: use a newly purchased Z.AI product, expose the broad OpenCode provider ecosystem, retain working Codex and Claude Code workflows.

## Decision

Make OpenCode the broadly compatible model runner in Crewship through a shared, extensible provider registry. Do not equate providers with CLI tools: OpenCode can call model services; this does not make it a wrapper implementing every other CLI's login, subscription or behavior. Keep the six existing runner adapters. Select runner, provider/product, account and model as distinct concepts.

Most providers should be declarative registry entries, not new executable plugins. Add specialized authentication/transport adapters only where necessary. The product target is that every discovered catalog provider is accounted for (connectable, experimental or explicitly unsupported with a reason), not a claim that every catalog model works for every account.

## Evidence and version boundary

Primary sources checked today:

- [OpenCode V1 providers](https://opencode.ai/docs/providers/): catalog-based integration, custom endpoints and cloud-specific setup.
- [OpenCode V2 providers](https://opencode.ai/v2/docs/providers/): materially different configuration and runtime packages.
- [Z.AI's OpenCode instructions](https://docs.z.ai/devpack/tool/opencode): subscribers select **Z.AI Coding Plan**, separately from Z.AI API; optional vision/search/reader MCP services are additional integrations.
- [OpenCode plugins](https://opencode.ai/docs/plugins/): extension hooks and package loading, not a prerequisite for every API provider.
- [models.dev JSON](https://models.dev/api.json): fetched catalog contains **222 provider records**. [Reduced inventory](opencode-provider-inventory-2026-09-18.json) records every ID, name, endpoint, environment metadata and model count with fetch time and source hash. This is an inventory, not 222 verified Crewship integrations.
- [Upstream V1 provider implementation](https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/provider/provider.ts): inspected Bedrock credential-chain setup and native SDK selection. `dev` is mutable; pin the exact deployed release source before implementation.

Host CLI reports **1.18.31**; that does not establish the CLI version inside every crew image. V1 uses `provider` / `options` / `npm`; V2 uses `providers` / `settings` / `package` and different runtime packages. V2 also changes Vertex provider identities. Do not copy V2 examples into today's V1 adapter. Add an explicit version/capability contract; evaluate a V2 migration independently with stream/tool/auth compatibility tests.

## Immediate priority: Z.AI subscription

The user confirmed GLM Coding Plan subscription during this analysis. P1 targets **Z.AI GLM Coding Plan** (`zai-coding-plan`), not metered API credit. The stated vendor is Z.AI; only clarify regional BigModel/Zhipu identity if the actual account console indicates it. Do not ask for the secret in chat; the user enters it through the credential UI. Subscription purchase is not evidence that every catalog model is entitled.

| Product | Native ID | Endpoint from today's catalog |
|---|---|---|
| Z.AI metered API | `zai` | `https://api.z.ai/api/paas/v4` |
| Z.AI Coding Plan | `zai-coding-plan` | `https://api.z.ai/api/coding/paas/v4` |
| Zhipu AI API | `zhipuai` | `https://open.bigmodel.cn/api/paas/v4` |
| Zhipu AI Coding Plan | `zhipuai-coding-plan` | `https://open.bigmodel.cn/api/coding/paas/v4` |

All four catalog entries advertise `ZHIPU_API_KEY`; that shared upstream name is not a safe internal account identity. Keep independent credential IDs and product routes, just as Go/Zen need distinct choices. The Crewship `ZAI_API_KEY` slot currently maps to native `zai` in `opencode_auth_file.go`. It is not Coding Plan support. Never repoint existing ZAI credentials to the subscription endpoint or automatically spill paid API traffic from a subscription failure.

First implementation slice: dedicated Coding Plan connection choice, canonical identity, encrypted key delivery, scoped grant, sidecar route, native prefix, model selection, clear subscription label, invalid-key/quota handling and one real-account acceptance run. Preserve ordinary Z.AI behavior. Region must be explicit if adding Zhipu variants. Coding Plan examples in the current snapshot include `glm-5.3`, `glm-5.3-flash` and `glm-5.3-highspeed`; validate the actual account and deployed CLI inventory instead of promising these models.

Optional Z.AI vision/search/reader MCP belongs in a separate tools step. A model credential must not silently enable extra services, permissions or charges.

## What is already missing in Crewship

Audit baseline: PR implementation `907c57e6f` plus documentation commits.

- Credential cards accept ZAI, Moonshot, MiniMax, OpenRouter and others, but `lib/validations.ts` agent provider enums expose only OPENAI, ANTHROPIC, GOOGLE, CURSOR, FACTORY, OLLAMA, OPENCODE and OPENCODE_GO. Having a credential card is not complete agent UI support.
- `internal/orchestrator/adapter_opencode.go` has a small `openCodeProviderIDs` map. General provider selection and arbitrary namespaced models need explicit semantics; vendor inference must not overwrite a selected reseller/subscription payer.
- `internal/orchestrator/opencode_auth_file.go` has a separate hardcoded slot mapping. It writes real keys for non-routed credentials; only recognized routed credentials get dummy auth. Do not extend the map and describe all providers as sidecar-isolated.
- Provider validation/normalization is duplicated across `internal/providerlogin`, `internal/credprovider`, `internal/api` create/update/onboarding/templates, frontend registries and model lists. Broad expansion needs one generated contract, rather than hundreds of independently maintained enum additions.
- `internal/llmroute` and sidecar codecs are deliberate protocol support, not a generic arbitrary-URL transport. Bedrock signing/event streams and cloud token refresh need explicit designs.
- The curated model list and trimmed price snapshot are not live account discovery. Preserve offline availability, custom model entry and price provenance while adding catalog breadth.

## Proposed registry and runtime design

Design proposal, not existing schema:

`ProviderDefinition`: stable catalog/native ID, display name, aliases, region/product family, auth schema, transport family, vetted endpoint template, nonsecret config schema, native runtime ID/version constraints, model discovery mode and evidence status.

`ProviderConnection`: immutable ID, workspace owner, definition ID, product/region, encrypted secret references, allowed nonsecret settings, account label and existing grant relationships. Do not infer connection identity from one shared environment variable.

`AgentModelBinding`: runner + connection ID + native provider ID + opaque model ID + optional validated variant. Preserve slashes in model IDs; distinguish provider prefix from the rest. Existing `llm_provider` values remain backward-compatible aliases during migration. Determine whether a migration is needed from current schema; do not assume storing arbitrary provider JSON in old fields is sufficient.

Execution resolves the exact granted connection before building config. Emit only that connection and any explicitly approved auxiliary-model bindings. Do not merge all workspace secrets into auth.json. Block unsupported transports at readiness with an actionable reason. A catalog entry is untrusted metadata: endpoint templates, package names and auth behavior require reviewed adapters; catalog refresh must never automatically authorize egress, install code or receive secrets.

Generate API validation, UI choices and runtime mapping from this contract. Add a conformance gate detecting missing projections, duplicate native aliases and inconsistent products. Support states should be visible: catalog only → connection implemented → transport tested → account verified (with version/date). A successful historical test is not proof of every future model.

Catalog refresh: reviewed pinned snapshot with source hash, last successful fallback, version filtering, deprecation handling and diff report. Preserve user custom models; do not auto-change an existing agent's model or billing route. Filter the coding picker by capabilities (text generation/tool support), not merely inclusion in a catalog with embeddings/images/audio. Unknown capabilities remain explicit.

## Authentication and transport workstreams

| Class | Examples | Crewship work required |
|---|---|---|
| Static key, established protocol | Together, DeepInfra, Groq, xAI, Venice, many long-tail entries | Declarative connection + vetted endpoint/header rule + protocol codec; preserve native SDK/model quirks |
| Subscription / regional product | Z.AI/Zhipu plans, Kimi coding plans, MiniMax, Alibaba/Volcengine/Xiaomi regional plans, Umans plan | Distinct product identity, route and account selection; upstream limits remain authoritative; no automatic metered fallback |
| Gateway / reseller | OpenRouter, ZenMux, Vercel AI Gateway, TokenRouter, custom organization gateway | Keep gateway payer and full model ID; tenant headers and optional multiple auth fields; price provenance; never infer original vendor credentials |
| AWS | Amazon Bedrock | Separate bearer-token mode from AWS credentials/roles; region and inference-profile IDs; refresh and endpoint handling |
| Google cloud | Vertex / Vertex Anthropic on V1 | Project/location, scoped ADC or workload identity, token refresh, native protocol; V2 mapping needs migration |
| Azure | Azure / cognitive services | Resource/deployment/API-version configuration; key or identity mode; refresh and tenant isolation |
| OAuth / device / special auth | Copilot, GitLab and supported upstream account plugins | Headless connect/reconnect, scoped encrypted token store, refresh ownership; no sharing host login cache across workspaces |
| Local / custom endpoint | Ollama, LM Studio, vLLM, Other | Container-reachable address, optional auth, validated protocol/model metadata, scoped private-network allowance |

Bedrock deserves two milestones. Bearer mode can be prototyped first with explicit region and a native SDK transport probe. SigV4 must sign the final upstream host/path/body, not a localhost sidecar URL. Design a sidecar credential broker/signing path with scoped temporary credentials and expiry handling; copying a host AWS profile or inheriting instance roles is not a multi-tenant solution. AWS event-stream decoding, model/profile identifiers and denied-region/model errors need fixtures and account tests. Bearer-token support alone must not be labeled full AWS support.

Custom endpoints must pass existing `httpsafe` and egress policies, including redirect and DNS-rebinding checks. Private/local addresses require explicit connection-scoped policy because container localhost is not the user's workstation. Do not accept arbitrary executable provider packages from a connection form. Any necessary package/plugin must be reviewed, version-pinned and installed through the image build process.

## User's models and long provider list

The fetched catalog includes Kimi K3, GLM and Qwen-family models across multiple billing providers. It also contains Muse Spark variants under Go/Zen and other entries; this establishes discoverability only. The transcription “KVAN” is tentatively interpreted as Qwen, not an exact model ID. Do not invent aliases before confirmation. A model can be offered by several services with different costs, context limits, tools and plan entitlements.

The machine-readable inventory covers the entire catalog, not just the pasted tail: Thinking Machines (the pasted name is truncated), Tinfoil, Together, TokenGo/TokenRouter/TrustedRouter, Umans and Coding Plan, UnoRouter, Upstage, v0, Vancine, Venice, Vercel, Vertex variants, Vispark, Vivgrid, Volcengine variants, Vultr, Wafer, Wallaby, watsonx.ai, xAI, Xiaomi regional products, Xpersona, Z.AI, Zeldoc, Zenifra, ZenMux and Zhipu variants should each be matched to exact native IDs before enabling. Labels and spellings are not integration contracts. “Other” is the custom-provider workflow, not a finite catalog entry.

## Rollout backlog and acceptance gates

1. **P0 — finish existing Go/Zen acceptance.** Follow the prior handoff; actual review, real-account success, grants/revocation and exact dev3 backend identity remain required. Keep PR #2619's shipped scope distinct from this expansion.
2. **P1 — Z.AI Coding Plan vertical slice.** Use the confirmed Z.AI GLM Coding Plan subscription, then implement the complete connection → grant → model → run → usage path with independent metered/subscription routes. Exit: paid account-backed stream/tool run, invalid key, wrong product, revoked grant and no secret in agent config/logs. Preserve existing ZAI credential records.
3. **P2 — common registry and model catalog.** Versioned schema, compatibility aliases and generated validators/UI/runtime contract. Include search, connected/favorite providers first, plan/region filters and explicit support status. Exit: every inventoried ID classified; unknown/unsupported cannot falsely appear ready; migration/rollback and offline/custom models tested.
4. **P3 — broad API-key and plan providers.** Enable providers through reviewed metadata in batches; protocol fixtures for each adapter, provider-specific smoke tests and documented live-test coverage. Prioritize the user's actual subscriptions, then Kimi/Moonshot, GLM products, Qwen/Alibaba, MiniMax, gateways and long-tail vendors. Distinguish native API from coding-plan accounts.
5. **P4 — cloud identity.** Bedrock bearer then SigV4/STS, Vertex and Azure as separate reviewable work. Exit: expiry/refresh, tenant isolation, stream usage and least-privilege denial tests plus representative live account evidence.
6. **P5 — custom/local and special plugins.** Reviewed custom endpoint and model workflow; then only necessary OAuth/plugin/MCP integrations. Explicit compatibility table for all existing CLI runners; no promise of a full Cartesian product of runners/providers.
7. **P6 — OpenCode V2 evaluation.** Separate decision with pinned version, config migration, headless behavior, events, tools, auth, network transport, image and rollback tests. Do not silently upgrade the runner to make a new catalog entry work.

Cross-cutting acceptance: exact connection selection with two same-product keys and both products present; concurrent runs and subagent/auxiliary models; no automatic payer failover; cancellation/retries; typed 401/403/429 errors; token/cache/reasoning usage per wire protocol; unknown prices do not become verified zero cost; desktop/mobile onboarding; no regressions for Codex or Claude Code. Reuse the existing Go/Zen proxy and orchestration tests as patterns, not proof that every new provider works.

For each slice create/claim a bounded issue before coding, record requirement → implementation → automated evidence → dev3 evidence, and release the claim on handoff. Effort is smallest for an existing static-key protocol, larger for the registry migration, and highest for refreshing cloud identity and native signed/event-stream transport; estimate concrete slices after the auth design, not by multiplying provider-card count.

## Next-agent starting point

Read this analysis alongside the existing PRD and acceptance handoff. The purchased product is confirmed as Z.AI GLM Coding Plan. First verify the CLI actually installed inside the test crew and use the dedicated `zai-coding-plan` identity. Review `providerlogin.go`, `opencode_auth_file.go`, `adapter_opencode.go`, `exec_env.go`, `llmroute`, agent schemas and billing code together. Propose a minimal P1 contract consistent with the future registry; do not postpone the user's subscription behind all 222 providers. No new providers, executable plugins, subscriptions, billing changes or deployments were implemented by this analysis.
