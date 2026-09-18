# Z.AI GLM Coding Plan — P1 implementation record

Date: 2026-09-18. Issue: #2621 (created per the expansion analysis's
one-issue-per-slice rule). Branch `feat/opencode-zai-coding-plan`, based on
`feat/opencode-go-zen` at `555ab2573` — a stacked slice, because the OpenCode
runner plumbing it builds on lives in PR #2619 and is not merged. Companion
documents: [integration PRD](opencode-go-zen-integration-2026-09-16.md),
[acceptance handoff](opencode-go-zen-handoff-2026-09-18.md),
[provider expansion analysis](opencode-provider-expansion-2026-09-18.md).

## What this slice is

The user's purchased **Z.AI GLM Coding Plan** subscription as a first-class
Crewship product, end to end: Add provider → encrypted storage → access
grants → agent/model selection → OpenCode run → streaming → usage/errors.

## Verified product facts

- Native identity `zai-coding-plan`, endpoint
  `https://api.z.ai/api/coding/paas/v4`, SDK `@ai-sdk/openai-compatible`
  (Bearer-only auth). Verified against the deployed host CLI's own runtime
  catalog: opencode 1.18.31, `~/.cache/opencode/models.json`. The catalog
  lists glm-5.3, glm-5.3-flash, glm-5.3-highspeed, glm-5.2, glm-4.7,
  glm-5-turbo. Catalog presence is not account entitlement.
- Upstream reads `ZHIPU_API_KEY` for all four z.ai/zhipu products (inventory
  JSON), so Crewship uses its own slot `ZAI_CODING_PLAN_API_KEY`. Existing
  metered `ZAI` credentials are untouched.

## Implementation

- Canonical identity `ZAI_CODING_PLAN` (alias `ZAI-CODING-PLAN`) in
  `internal/providerlogin`; slot `ZAI_CODING_PLAN_API_KEY`; plan label
  "GLM Coding Plan"; mode `api_key` (a subscription key, like OpenCode Go).
- `internal/llmroute`: spec `ZAI_CODING_PLAN` → route `/llm/zai-coding-plan`,
  upstream `api.z.ai` + `/api/coding/paas/v4`, Bearer auth replacement,
  RequireCredential. One register() — not part of the opencode.ai gateway
  loop, because the host, base path and auth surface differ.
- Orchestrator: `openCodeProviderIDs` + the explicit-payer guard in
  `qualifyOpenCodeModel` (a bare model on a Coding Plan agent can never be
  re-stamped to a vendor provider); native prefix rewrite in
  `resolveRoutedProvider`; options-only `OPENCODE_CONFIG_CONTENT` override
  (preserves the native SDK/model metadata); auth-file slot → `zai-coding-plan`
  with dummy key when routed.
- API: agent/onboarding/model-list provider surfaces, curated
  `zai_coding_plan` models in `config/models.json` (glm-5.3 default),
  partial-suggestions rule preserved (custom native model IDs stay valid).
- Paymaster: no rate row and none invented — `zai-coding-plan` resolves
  SourceNone; subscription limits are not token-derived prices.
- Frontend: provider card, connection guide (console + docs links,
  subscription-not-metered note), brand/icon, wizard presentation,
  provider/model pickers, `ZAI_CODING_PLAN` LLM provider enum everywhere,
  billing facts on the login detail.
- Metered↔subscription separation is fail-closed: a `ZAI` credential never
  routes the Coding Plan spec and readiness reports it missing (pinned in
  tests).

## Automated evidence

Executed on the stacked branch after the changes:

- `go test ./internal/llmroute ./internal/providerlogin ./internal/credprovider ./internal/paymaster -count=1` — ok.
- `go test ./internal/orchestrator ./internal/sidecar -count=1` — ok (incl.
  `TestOpenCodeGatewaysRouteWithoutExposingKeys` with the ZAI case and
  `TestOpenCodeZAIMeteredKeyDoesNotRouteCodingPlan`,
  `TestZAICodingPlanProxy`).
- `go test ./internal/api -count=1` — ok (incl. the extended
  provider-login/agent-update tables).
- `go vet` on all touched packages — clean.
- Targeted vitest: add-credential-wizard, provider-logins-sidebar,
  credentials page-readiness, agent-model-settings — 89 passed.

Full-suite, lint and build results are recorded in the PR after CI; the
authoritative record is the checks on the pushed commit.

## Not done here (explicitly)

- Live acceptance with the real subscription key on dev3 — NOT PERFORMED.
  Keys must be entered through the UI by the user; no key was requested,
  stored or committed. Live checks owed: streamed completion, tool call,
  invalid-key error text, quota-denied error, both desktop and phone.
- Zhipu/BigModel regional variants (`zhipuai`, `zhipuai-coding-plan`) —
  separate products, out of scope until an account exists.
- Z.AI vision/search/reader MCP services — a separate tools decision.
- The P2+ shared provider registry — this slice stays a vertical cut that the
  registry can later absorb; `zai-coding-plan` is one declarative identity
  away from it, not a precedent for unbounded enum growth.
