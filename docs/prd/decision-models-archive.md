# Decision-model research archive

Status: research and manual experiments only. The implementation proposed in
[PR #2630](https://github.com/crewship-ai/crewship/pull/2630) was closed without
merging on 2026-09-25. This archive adds no server decision evaluator, CLI
command, routine step, frontend control, runtime configuration, or default
behavior to Crewship. The old PR remains available as a code reference for a
future implementation.

## Retained evidence

- [Jev research and CLI pilot](jev-research-and-pilot-2026-09-20.md) and its
  [verification records](reports/jev-2026-09-20/verification.md). These describe
  the historical hosted-provider branch; the CLI commands require that branch.
- [Webhook router design and dev1 probe](jev-webhook-router-2026-09-23.md),
  likewise historical and not installed in `main`.
- [Local SemIf/MLX Mac results](reports/semif-mac-2026-09-24/README.md),
  [extended Keeper and robustness tests](reports/semif-mac-2026-09-24/extended/README.md),
  and the [manual signed-webhook pilot](reports/semif-mac-2026-09-24/dev1-webhook-pilot.md).
- [Standalone datasets, scorers, fixtures and tests](../../scripts/jev-eval/README.md).

SemIf is an independent open implementation of the decision-interface pattern;
its Qwen/MLX results are not Jev model results. The synthetic data and short
dev1 probes are useful for reproducing behavior, not for a production accuracy
claim. In the option-order stress test, high-scoring incorrect automatic routes
appeared. Any later runtime feature needs real redacted Crewship examples,
separate per-task acceptance criteria, a persistent local service, workspace
isolation, and a review fallback. Credential permission remains with Keeper's
existing gate.

The manual dev1 webhook fixtures are development-only. Their two signed
webhooks and target routines were disabled after this archival closeout; the
definitions, receipts and run history remain available as evidence. No
production decision service is configured to use this MacBook Air pilot.
