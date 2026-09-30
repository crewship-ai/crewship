<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->



## Type-checking test fixtures

`pnpm test:types` type-checks every test file through `tsconfig.tests.json`
(Vitest globals plus the jest-dom matcher types) and fails on any diagnostic.
It runs in the Frontend Test CI job. There is no baseline: a harness that
renders a component without a required prop, or a fixture built to a wire
shape the product does not have, is a red gate, not a warning (#2493).

Type a mock with the signature of what it replaces
(`vi.fn<typeof apiFetch>()`, `Mock<Props["onSelect"]>`) rather than reaching
into `mock.calls` through a cast, and build fixtures as the product type
(`const run = (over: Partial<PipelineRunRecord> = {}): PipelineRunRecord`) so a
renamed field fails here instead of at runtime. `@ts-expect-error` is for a
test that deliberately feeds a wrong shape, and it says why.
