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

## Verification requirements

Use synthetic upstream endpoints and isolated HOME/XDG directories to verify
provider selection, native transport, account fencing and usage metadata.
Cover independent credential slots, custom models and agent updates.
Internal probe transcripts and deployment checklists are retained privately.
Synthetic transport success is not evidence of successful paid inference.
