# Jev webhook router pilot — 2026-09-23

Continuation of [#2629](https://github.com/crewship-ai/crewship/issues/2629) and the CLI-only pilot in [PR #2630](https://github.com/crewship-ai/crewship/pull/2630).

## Implemented boundary

An existing signed routine webhook still authenticates, deduplicates and persists its delivery through the established routine-webhook path. The targeted routine can now include a `decision` step. It sends only the author's rendered `decision.state` to the operator-configured TypeSafe or OpenRouter Decisions endpoint and returns one author-declared option label. Later `agent_run` steps use ordinary `if`/`needs` gates; the model cannot invent a target or bypass the agent's normal admission and policy gates.

A `steps.*` CEL condition whose dependency is missing or whose expression cannot be evaluated now skips its step. This matters for routing: a malformed condition must never turn into a truthy string and wake an agent.

The `review` option is mandatory. A selected probability below the per-step threshold (default 0.9) returns `review`. An absent provider, foreign workspace, undeclared provider host, crew-network denial, invalid model response or provider failure fails the step before any downstream agent starts. Provider calls have the original client's fixed destination, bounded request/response, five-second timeout and no implicit retry. The pilot is enabled only with `CREWSHIP_DECISIONS_PROVIDER`, the corresponding key and `CREWSHIP_DECISIONS_WORKSPACE_ID` in the server environment. No default route changes.

`pipeline.decision.evaluated` records the selected and suggested labels, probability distribution, threshold, model, provider and usage, correlated to the routine run and step. It does not copy the webhook state. This gives Keeper and offline evals evidence without treating a model score as authorization.

The runnable [example](../../scripts/jev-eval/webhook-router.routine.json) has `sre`, `developer`, `review` and `ignore` choices. Its state template selects three event fields rather than forwarding the raw body or signature headers. The routine must declare the provider host in `egress_targets`; the author crew's network policy still applies. Instructions and setup are in [the decisions CLI guide](../cli/decisions.mdx#experimental-webhook-router-routine).

The example's `review` branch is a durable approval waitpoint; `ignore` completes with an auditable decision and no agent call.

## Limits

This is a server pilot, not production-wide routing. The server uses an operator environment key scoped to one workspace; it does not use workspace vault credentials or record provider charges in Paymaster. No live Jev inference or calibration is claimed without a test key. The local SemIf on MacBook Air needs a separate evaluator adapter. A `decision` step is not allowed in a token-zero `agentless` routine. The sample agent slugs must be replaced with agents that actually exist in the author crew.

The current `dry-run` does not invoke the decision provider or predict which branch an agent would take. Actual branch accuracy requires labelled webhook deliveries and an isolated real run. No credentials or automatic Keeper authorization depend on this pilot.

## Dev1 verification on `d0ec80757` (2026-09-23)

`crewship version --remote` reported schema `20260922182944`; `crewship-ws@1` was active and `/api/health` returned `{"status":"ok"}`. The server build was marked dirty because this shared dev1 clone contains pre-existing untracked files; those files were preserved.

A disposable `jev-webhook-probe-20260923` routine with a `decision` step saved, passed server-side validation and was approved. A signed POST to its disposable webhook returned 202 with run `run_cmuedg4rl000384510b70`. Repeating the identical delivery returned 202 with `duplicate=true` and the same run ID. The run was recorded as webhook-triggered and failed at `route` with `decision evaluator is not configured`, as expected: no TypeSafe/OpenRouter key and provider scope were configured on dev1. No agent was in this probe routine. The disposable webhook was deleted and the routine soft-deleted; the run and receipt remain as evidence. The one-time signing secret was not copied into this report and its temporary file was removed.

This verifies deployed ingress, persistence, deduplication and the missing-provider refusal. It does **not** verify live model routing or its accuracy.

The UI-enabled build `376446119` was also built and started on dev1; its server version matched that commit and the public `/api/health` returned `{"status":"ok"}`. The temporary probe was not repeated because the provider remains unconfigured.
