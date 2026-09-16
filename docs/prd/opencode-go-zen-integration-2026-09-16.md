# OpenCode Go and Zen integration — 2026-09-16

Issue: #2618. Branch based on origin/main fc2ebb841.

## Provider contract

Official sources checked on 2026-09-16:

- https://opencode.ai/docs/go/
- https://opencode.ai/docs/zen/
- https://opencode.ai/docs/providers/
- https://models.dev/api.json
- https://github.com/anomalyco/opencode/blob/dev/packages/opencode/src/provider/provider.ts

Go is a subscription with model-dependent usage windows. Zen is a metered
model gateway. Both connect with an API key from the OpenCode console; no
workspace-management API or OAuth handshake is required for inference.
OpenCode workspaces administer members, model access, keys and billing. Crewship
stores keys and applies its existing workspace/crew/agent access grants.
Operators may add separately named credentials for multiple accounts or keys.

Go uses native provider `opencode-go` and `/zen/go/v1`; Zen uses `opencode`
and `/zen/v1`. Those are separate billing choices even when the same key works
with both. Crewship does not switch between them automatically. Go's optional
Use balance setting is controlled upstream and may consume Zen credit after
subscription limits. Additional keys do not imply additional subscription quota.

## Implementation

- Add provider: separate OpenCode Go and OpenCode Zen cards with API-key setup
  guidance, console links and billing explanations.
- Store as encrypted PROVIDER_LOGIN with mode `api_key`. Here mode describes
  authentication/delivery, not the commercial plan: Go labels explicitly say
  subscription. No invented expiry, refresh, account ID or quota balance.
- Stored providers: `OPENCODE` and `OPENCODE_GO`. Native model prefixes remain
  `opencode/` and `opencode-go/`.
- Separate grant slots: `OPENCODE_API_KEY` and `OPENCODE_GO_API_KEY`. The latter
  is Crewship's slot, not a claimed vendor environment variable. OpenCode itself
  uses OPENCODE_API_KEY for both; its auth.json supports separate provider keys.
- Selected gateway routes through a credential-required sidecar route. The
  native auth file and generated options carry dummy auth. A missing or wrong
  product key cannot fall through to the other product's route.
- Options-only provider overrides retain OpenCode's model-specific SDKs and
  model metadata. The proxy replaces SDK auth headers and preserves session and
  user-agent headers; response usage follows messages, responses/chat or Google
  wire formats. No forced OpenAI-compatible SDK for all models.
- Agent create/edit includes Go/Zen provider selection and the OpenCode runner.
  A small curated model list is shared by API and UI; custom native model IDs
  remain available. This is not live account-specific model discovery.
- Zen estimates use its own models.dev price snapshot, not the original model
  vendor's price. Go token traffic is observed, but its marginal charges cannot
  be inferred: included subscription use and upstream balance overage are not
  distinguishable from token counts. No Go rate card or quota percentage is
  fabricated. The upstream console remains authoritative for billing/limits.

## Scope and validation limits

This increment supports the OpenCode runner, not gateway-specific configuration
of Claude Code/Codex/Gemini CLI or Keeper's internal LLM. Workspace provisioning,
subscription purchase, billing changes, automatic quota failover and live balance
synchronization remain in the OpenCode console. No database migration is needed.
Real paid inference requires a user-provided Go/Zen account and has not been
performed. Fixture tests and local CLI transport probes do not verify plan
entitlement or actual billed amounts.

## Verification evidence

- Credentials/provider/agent Vitest selection: 347 tests passed; follow-up
  agent-selection/detail tests: 20 passed; model-catalog tests: 13 passed.
- Production `pnpm build` passed. TypeScript check passed. ESLint passed with
  30 warnings and no errors.
- Playwright provider-first workflow passed at 1280 px and 390 px using the
  production static export and fixture APIs. Screenshots inspected under
  `/tmp/opencode-{go,zen}-{1280,390}.png`; no real credentials were created.
- OpenCode 1.18.31 native transport probe, with isolated HOME/XDG directories
  and dummy auth, reached a local server for Go Kimi K3, MiniMax M3 and
  GPT-5.6 Luna, and Zen Claude Sonnet 5 and Gemini 3.1 Pro. It preserved native
  endpoint shapes, sent a stable x-opencode-session per conversation, and
  identified itself as OpenCode. Auxiliary Zen requests used the same local
  gateway. Each probe intentionally stopped with a local 401. Evidence:
  `/tmp/opencode-native-probe.log`. A second probe using the exact options-only
  production configuration (no disabled-provider override) confirmed Go and
  Zen auxiliary traffic stays on the selected gateway:
  `/tmp/opencode-native-exact-probe.log`.
- Complete targeted suites for orchestrator, sidecar, llmroute, providerlogin,
  modelcatalog and paymaster passed. API regressions cover encrypted storage,
  independent slots, plan labels, agent updates and custom model IDs.
- `go vet ./...`, `scripts/verify.sh quick`, and agent invariants passed.
- Full Go-suite outcome is recorded in the PR after completion; log:
  `/tmp/opencode-full-go.log`. No deployment or successful paid inference is
  implied by this verification.
