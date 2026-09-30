<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## Dependency drift (the check on lockfile PRs)

Every PR that touches `pnpm-lock.yaml`, `go.mod` or `go.sum` gets a
**Dependency drift** check (`.github/workflows/deps-drift.yml`, driven by
`scripts/deps-drift`). It diffs the resolved version of every *direct*
dependency between the merge base and the PR — read straight from the
lockfile's `importers` section and from `go.mod`, no install at either
side — and posts the result as a table in the job summary and in one PR
comment that is updated in place. Direct dependencies only: ~200 rows a
person will actually read, against the tens of thousands of lockfile lines
nobody does.

It fails on exactly one thing:

> A direct dependency's resolved version changed while its spec in
> `package.json` / `go.mod` did not.

That is the #2237 shape. Two Dependabot PRs each moved `@sentry/nextjs`
10.71.0 → 10.72.0 — the version the alias above works around — with
`package.json` still saying `^10.70.0` on both, because Dependabot regenerates
the whole lockfile per branch. Nothing declared it, nothing reviewed it
(CodeRabbit ignores both `dependabot[bot]` and the lockfile), and CI reported
it as eleven suites failing to import. A normal bump moves the spec and the
resolution together and stays green; everything else — added and removed
dependencies, a spec widened with nothing new resolving, a `go.sum`-only
change — is printed and never blocks. For Go, "resolved" is the `require`
version unless a `replace` directive redirects it, so adding or changing a
`replace` without touching the require line is drift too.

When the drift is wanted (you regenerated the lockfile on purpose, or a
transitive floor forced it), apply the **`deps-drift-ok`** label: the
failure becomes a warning, the table still posts, and the check re-runs on
the label event without a push. Otherwise, either pin the spec so the bump
is declared, or regenerate the lockfile against the base without it.

Locally, the same table for your branch:

```bash
go run ./scripts/deps-drift -base origin/main -head HEAD
```
