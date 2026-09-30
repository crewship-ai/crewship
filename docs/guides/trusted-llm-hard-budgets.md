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

This contract currently covers Go middleware requests. Legacy sidecar CLI
forwarding and its asynchronous post-call cost records are a separate path;
that path requires a pre-request host gate before a workspace hard cap can be
claimed for those calls. Trusted users' independently issued external requests
and provider invoices are outside Crewship-managed admission accounting.

Tests: `TestTrustedAndRestrictedShareAtomicHardCap`,
`TestTrustedReservationUnknownErrorsAndKnownUsage`,
`TestHardBudgetActualCodecsRetainUnknownAndSettleKnown`, and
`TestHardBudgetProviderContractRejectsOverrides`.
