# Development — contributor deep-dives

**Purpose.** The long-form explanations a contributor needs once the short
rule in [CONTRIBUTING.md](../../CONTRIBUTING.md) raises a question. Each file
was moved out of the entrypoint on 2026-09-28 so that CONTRIBUTING stays a
workflow a person actually reads, while nothing documented here was lost.

**Rule for a new file.** A deep-dive earns its place when the rule is short
but the reason is long — incident history, toolchain limitations, protocol
mechanics. The entrypoint keeps the rule and links here; do not duplicate the
rule's full text in both places.

## Contents

- [`repository-layout.md`](repository-layout.md) — where product code, tests, tools and root configuration belong.
- [`private-context.md`](private-context.md) — public/private documentation boundary, optional agent discovery and verified migration procedure.
- [`web-out-embed.md`](web-out-embed.md) — why `web/out/.placeholder.html` is tracked, and the two ways to re-break every fresh clone.
- [`build-identity-in-worktrees.md`](build-identity-in-worktrees.md) — why a bare `go build` in a worktree stamps the parent clone, and how `scripts/build-stamp.sh` fixes it.
- [`go-toolchain-pinning.md`](go-toolchain-pinning.md) — the thirteen copies of the Go version and what `scripts/go-toolchain-pin.sh` deliberately does not check.
- [`sentry-vitest-alias.md`](sentry-vitest-alias.md) — why `vitest.config.ts` aliases `@sentry/nextjs` to its browser build.
- [`dependency-drift.md`](dependency-drift.md) — the lockfile drift check and the `deps-drift-ok` label.
- [`house-rules-background.md`](house-rules-background.md) — the reasoning and incident history behind the house rules.
- [`frontend-data-fetching.md`](frontend-data-fetching.md) — React Query conventions: keys, transport, realtime freshness, tests.
- [`changelog-guard.md`](changelog-guard.md) — the Changelog Guard's exact comparison, history and label mechanics.
- [`coderabbit-review-process.md`](coderabbit-review-process.md) — historical bot review protocol; current review requirements are in CONTRIBUTING.md.
- [`claiming-issues.md`](claiming-issues.md) — the claim convention and its failure modes.
- [`test-fixture-types.md`](test-fixture-types.md) — typing test mocks and fixtures so renames fail in `pnpm test:types`.
