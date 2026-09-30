<!-- Moved from CONTRIBUTING.md in the 2026-09-28 repository-clarity
     reorganisation; the entrypoint keeps the rule, this file keeps the
     full explanation. Index: docs/development/README.md -->

## House rules — the background

The rule list lives in
[CONTRIBUTING.md → House rules](../../CONTRIBUTING.md#house-rules-the-short-list).
What follows is the reasoning and incident history behind the four rules whose
one-line form does not carry enough context to be applied correctly.

### A detached goroutine in `internal/api` registers with `beginBackgroundWork`

See `internal/api/background.go`. Work that outlives its request also outlives
the *test* that drove it, where it races that test's teardown and fails a
bystander at random (#1596). A long-lived daemon that genuinely cannot be
drained goes in `unregisteredSpawnSites` with its reason. A test that hands a
storage directory to **any** handler with a detached writer takes it from
`storageDir(t)`, not `t.TempDir()` — `t.TempDir()` fails the test when its
`RemoveAll` races a late write, and a `t.TempDir()` taken after `setupTestDB`
is cleaned up *before* the drain runs.

### `Commands()` on a shared Cobra command is a *write*

Cobra sorts the child slice in place on first call, behind an unsynchronised
bool — and `ExecuteC` re-arms that bool on **every** run, because
`InitDefaultHelpCmd` unconditionally removes and re-adds the help command. In
`cmd/crewship` that let 34 `t.Parallel()` tests enumerate `rootCmd` at once,
sort the same slice concurrently, and hand back a corrupted list — a
`WARNING: DATA RACE` blaming a random test in a package the PR never touched
(#1989). Its `TestMain` now freezes the order with
`cobra.EnableCommandSorting = false`, taken **after** the pristine walk has
sorted the tree, so `Commands()` is a pure read for the rest of the test
binary. Test-scoped: production `main()` still sorts, so `crewship --help`
ordering is unchanged. What that does *not* make safe is **executing** a
shared command from a parallel test — `ExecuteC` still rewrites the child
slice — so don't; build a local command tree instead.
`cobra_sort_parallel_guard_test.go` fails the build on both halves.

### `go test -race ./internal/api/` needs `-timeout`

That package takes ~23m under the race detector (`ok … 1405s` on a CI runner,
measured 2026-08-20; local numbers on crewship-dev swing with what else is
running) and `go test`'s default timeout is 10m, so a plain run is **killed
mid-test** — it prints a goroutine dump headed by whichever `t.Parallel()`
test was running when the axe fell, and **zero `WARNING: DATA RACE`**. That
reads exactly like a failure and it is not one; it is the same ghost people
chased in #1597. Run it as `go test -race -timeout 40m ./internal/api/`.
CI's dedicated **Go Race (internal/api)** job passes a timeout derived from a
measured baseline (`RACE_API_BASELINE_SECONDS` in `.github/workflows/ci.yml`),
so a green CI and a red local run are consistent, not contradictory. Before
blaming your diff, check the log for `WARNING: DATA RACE` — no such line plus
a `panic: test timed out` means you hit the ceiling, not a race. That ceiling
is #2031's subject: the CI job now prints its own `ok … Ns` into the run
summary and warns when it crosses 1.6x the baseline, so budget erosion is
reported as budget erosion rather than as your bug.

### Don't hash at production strength in a test

`internal/api` hashes passwords with `bcryptCost` (`internal/api/bcrypt_cost.go`),
which `TestMain` lowers to `bcrypt.MinCost` for the test binary only. bcrypt's
cost is exponential, so production's 12 is ~256x MinCost, and under `-race` —
where blowfish's key schedule is instrumented on every array access — a single
cost-12 hash costs seconds. Never write a literal cost at a call site: four
guard tests in `bcrypt_cost_test.go` pin the production value at 12, prove the
test binary really lowered it, reject any call site that passes its own
number, and reject any write to the var outside the test binary.
