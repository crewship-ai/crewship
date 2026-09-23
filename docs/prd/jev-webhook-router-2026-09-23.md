# Jev webhook router pilot — 2026-09-23

Continuation of [#2629](https://github.com/crewship-ai/crewship/issues/2629) and the CLI-only pilot in [PR #2630](https://github.com/crewship-ai/crewship/pull/2630).

## Implemented boundary

An existing signed routine webhook still authenticates, deduplicates and persists its delivery through the established routine-webhook path. The targeted routine can now include a `decision` step. It sends only the author's rendered `decision.state` to the operator-configured TypeSafe or OpenRouter Decisions endpoint and returns one author-declared option label. Later `agent_run` steps use ordinary `if`/`needs` gates; the model cannot invent a target or bypass the agent's normal admission and policy gates.

The `review` option is mandatory. A selected probability below the per-step threshold (default 0.9) returns `review`. An absent provider, foreign workspace, undeclared provider host, crew-network denial, invalid model response or provider failure fails the step before any downstream agent starts. Provider calls have the original client's fixed destination, bounded request/response, five-second timeout and no implicit retry. The pilot is enabled only with `CREWSHIP_DECISIONS_PROVIDER`, the corresponding key and `CREWSHIP_DECISIONS_WORKSPACE_ID` in the server environment. No default route changes.

`pipeline.decision.evaluated` records the selected and suggested labels, probability distribution, threshold, model, provider and usage, correlated to the routine run and step. It does not copy the webhook state. This gives Keeper and offline evals evidence without treating a model score as authorization.

The runnable [example](../../scripts/jev-eval/webhook-router.routine.json) has `sre`, `developer`, `review` and `ignore` choices. Its state template selects three event fields rather than forwarding the raw body or signature headers. The routine must declare the provider host in `egress_targets`; the author crew's network policy still applies. Instructions and setup are in [the decisions CLI guide](../cli/decisions.mdx#experimental-webhook-router-routine).

## Limits

This is a server pilot, not production-wide routing. The server uses an operator environment key scoped to one workspace; it does not use workspace vault credentials or record provider charges in Paymaster. No live Jev inference or calibration is claimed without a test key. The local SemIf on MacBook Air needs a separate evaluator adapter. A `decision` step is not allowed in a token-zero `agentless` routine. The sample agent slugs must be replaced with agents that actually exist in the author crew.

The current `dry-run` does not invoke the decision provider or predict which branch an agent would take. Actual branch accuracy requires labelled webhook deliveries and an isolated real run. No credentials or automatic Keeper authorization depend on this pilot.
