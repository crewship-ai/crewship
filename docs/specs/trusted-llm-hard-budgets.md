# Trusted managed LLM requests under hard budgets

The Go `llm.Middleware` path reserves a maximum debit before calling an official
API-key provider whenever its resolved scope has an enabled hard or tiered USD
budget. It uses the same SQLite writer transaction and pending debit ledger as
restricted broker requests. Concurrent trusted and restricted admissions cannot
both spend the same remaining amount. Restricted traffic still independently
requires an enabled workspace hard or tiered budget.

Uncapped and soft-only calls keep their existing behavior. Subscription calls
remain outside this USD contract; an imported login is not an API-key financial
budget or a subscription spending guarantee.

A hard-capped request requires a known official OpenAI or Anthropic endpoint,
ordinary API-key authentication, a known model context/output ceiling, and a
closed text/function-tool request codec. Custom endpoints, unsupported provider
parameters, additional body overrides, alternate authentication, unsupported
Anthropic betas and unpriced models are rejected before upstream delivery.
`EstimatedInputTokens` is never a hard-cap bound.

Input reservations cover the entire known model context, including tool and
protocol overhead. Output reservations use the actual supported wire limit or
the absolute model output ceiling. Rates are pinned at conservative maxima
across applicable context tiers and input/cache channels. This can require a
larger available balance than the actual short request will consume.

Hard-capped calls make one upstream attempt. Transparent network retries are
disabled because a timeout or 5xx response may already have incurred a charge.
A caller retry obtains a new reservation.

Validated terminal usage reduces the same ledger debit. OpenAI total prompt
counts are converted to the existing fresh/cache ledger semantics; Anthropic
fresh input, cache reads and cache creation are summed for the reservation and
remain distinct in reports. Missing, duplicate, negative, fractional, over-bound,
partial, canceled or failed usage retains the full maximum. Crashes leave the
pending maximum durable. Known trusted usage is labeled an estimate because the
pinned ceiling rates can overstate a cheaper tier or fresh-input channel.

Go middleware reserves metered requests. Legacy sidecar CLI forwarding uses
the conservative host denial gate described below. Trusted users' independently
issued external requests and provider invoices are outside Crewship-managed admission accounting.

Tests: `TestTrustedAndRestrictedShareAtomicHardCap`,
`TestTrustedReservationUnknownErrorsAndKnownUsage`,
`TestHardBudgetActualCodecsRetainUnknownAndSettleKnown`, and
`TestHardBudgetProviderContractRejectsOverrides`.

## Legacy sidecar admission

Managed legacy sidecars synchronously call the host's workspace-bound
`POST /api/v1/internal/cost/admit` before credential injection or network
forwarding, and again before rotation grace replay. The host checks the current
acting agent, crew, exact selected credential, provider, active grants and billing
domain from its database. A request or environment billing label cannot exempt a
metered credential from the gate. Missing IPC, failed admission and unbound
internal master tokens fail closed.

The legacy adapter cannot prove an output ceiling or reserve and settle a
specific request. Enabled hard or tiered budgets therefore deny legacy
traffic even when the budget has remaining funds; use the restricted broker for
that traffic. Because mission attribution is unavailable, any enabled mission
hard budget in the workspace denies legacy traffic conservatively.
Opaque CONNECT tunnels have no proven credential or payer and are denied under
any hard budget in the workspace. Soft-only and uncapped admitted requests retain
the existing post-response cost observer; the admission gate records no debit.

Subscriptions remain outside the USD hard budget contract. The temporary legacy
gate also denies them under applicable hard budgets: ID-only admission cannot
prove that a cached token matches the current credential revision, so replacing
an API-key row with a login row cannot exempt a still-valid cached metered key.
This denial does not establish a subscription financial cap or enable the
restricted login adapter.
The restricted runtime's mandatory reservation broker is separate from the
legacy sidecar and continues to enforce its own cumulative financial limits.

Rollout requires the updated managed sidecar binary. Rebuild and reconcile
existing sidecars before claiming this legacy denial contract; an older proxy
never calls the new host admission endpoint.
