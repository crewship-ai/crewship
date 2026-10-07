<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->



## Type-checking test fixtures

`pnpm test:types` type-checks every test file through `tsconfig.tests.json`
(Vitest globals plus the jest-dom matcher types) and fails on any diagnostic.
It runs in the required Frontend test types CI job (`frontend-test-types`). There is no baseline: a harness that
renders a component without a required prop, or a fixture built to a wire
shape the product does not have, is a red gate, not a warning (#2493).

Type a mock with the signature of what it replaces
(`vi.fn<typeof apiFetch>()`, `Mock<Props["onSelect"]>`) rather than reaching
into `mock.calls` through a cast, and build fixtures as the product type
(`const run = (over: Partial<PipelineRunRecord> = {}): PipelineRunRecord`) so a
renamed field fails here instead of at runtime. `@ts-expect-error` is for a
test that deliberately feeds a wrong shape, and it says why.

## Migrated SQLite test databases

Go tests get databases from `internal/testutil` (`MigratedDB`, `MigratedDBAt`,
`NewMigratedDB`): a private copy of one fully migrated template. Under `-race`
the migration chain costs about a minute, so the template is built once per
source tree and shared by every test binary:

- `CREWSHIP_TEST_SHARED_TEMPLATE` — set to `0` to disable the shared,
  content-addressed template and build a private one per test process. The
  shared template is keyed by the Go version, `go.mod`/`go.sum` and the
  non-test sources under `internal/`, built under a file lock and verified by
  checksum before each copy. Any failure falls back to the private build.
- `CREWSHIP_TESTUTIL_MIGRATED_TEMPLATE` and
  `CREWSHIP_TESTUTIL_MIGRATED_TEMPLATE_EXE` — set by a test process for a
  child it re-executes (for example to run under another time zone), so the
  child copies the parent's template instead of migrating again. The child
  uses it only when its own executable is the one named; otherwise it builds
  its own.

Tests that verify fresh bootstrap or migrations themselves still open their
own database through `database.Open`/`database.Migrate`.
