# Script catalog

Run repository commands from the product checkout root. Start with the
entrypoints below; individual scripts document arguments and prerequisites in
their headers or local README. The presence of a script is not a promise that
it is safe against a shared development instance.

## Common entrypoints

| Task | Entrypoint | What it does |
| --- | --- | --- |
| Start development | `./dev.sh start` | Starts the instance's Go/Next.js services; use `status` to inspect first |
| Build a release-style binary | `make build` | Builds and embeds the UI, then builds Go |
| Fast pre-push checks | `bash scripts/verify.sh quick` | CI helper tests, toolchain/build-path checks and workflow lint; not the full test suite |
| Backend pre-push checks | `bash scripts/verify.sh go` | Quick checks plus the Go test wrapper and `go vet` |
| Full pre-push entrypoint | `bash scripts/verify.sh full` | Adds frontend lint, types, coverage and static export build |
| Whole frontend coverage audit | `pnpm test:coverage:full` | Measures handwritten application routes, components, hooks, libraries and stores; writes `coverage/full-frontend/` and fails when any measured file has less than 90% lines or branches. This broader audit is separate from the historical shared-module CI gate and currently reports outstanding coverage debt. |
| CLI subprocess coverage | Set `TEST_CREWSHIP_CLI_COVERAGE_DIR` to an existing absolute output directory when running `go test ./cmd/crewship` | Instruments the shared acceptance-test binary for `cmd/crewship`. Export its counters with `go tool covdata textfmt -i="$TEST_CREWSHIP_CLI_COVERAGE_DIR" -o=cli-subprocess.out`; combine matching statement blocks with the ordinary test profile. The subprocess report alone does not include in-process unit tests. |
| Inspect trusted CI control inventory | `python3 -m unittest discover -s scripts/ci -p test_inventory_guard.py` | Tests the main-owned, metadata-only guard; administrator exceptions require a separate exact-head dispatch. See [trust limits and rollout](../docs/runbooks/ci-inventory-guard.md) |
| PR throughput evidence | `python3 scripts/ci/pr-throughput.py --since YYYY-MM-DD --output /tmp/throughput.json --raw-output /tmp/throughput-raw.json` | Read-only bounded GitHub queries for merged PR latency, workflow attempts, failures and queue/execution; see [metric definitions and limitations](ci/pr-throughput.md) |
| Documentation inventory | `make docs-inventory:strict` | Checks documented API/CLI references and writes ignored inventory reports |
| Browser tests | See [e2e/README.md](../e2e/README.md) | Selects the appropriate suite and target instance |

The verification script's current Go timeout is 25 minutes. Some full local
runs on the shared development server have exceeded that for one package; a
timeout is an incomplete run, not a pass. Record the command and result when
running the full suite with a deliberately adjusted timeout. Mutation tests
(`make mutation`) are a separate, expensive check scoped by `.gremlins.yaml`.

## Build, generated assets and release support

| Script | Responsibility / outputs |
| --- | --- |
| [build-stamp.sh](build-stamp.sh) | Reads Git identity correctly in worktrees; supplies build metadata |
| [embed-web-out.sh](embed-web-out.sh) | Stages/verifies the static export in `web/out/`; preserves the placeholder |
| [prepare-pdf-assets.mjs](prepare-pdf-assets.mjs) | Prepares browser PDF assets; invoked by frontend dev/build commands |
| [gen-avatar-fixtures.mjs](gen-avatar-fixtures.mjs) | Regenerates avatar validator test fixtures |
| [generate-team-avatars.mjs](generate-team-avatars.mjs) | Generates demo team portraits; distinct from validator fixtures |
| [gen-notices.sh](gen-notices.sh) | Regenerates third-party notices from Go dependencies; invoked by `make notices` |
| [gen-license-bundle.sh](gen-license-bundle.sh) | Builds `build/licenses/` + flat copy: Go and npm dependency texts with SHA-256 manifests and project legal files; invoked by `make licenses`; consumed by GoReleaser and nfpm; Docker generates its bundle in build stages |
| [gen-frontend-licenses.mjs](gen-frontend-licenses.mjs) | Collects scoped and unscoped npm dependency texts from the installed locked tree; checks coverage and writes a SHA-256 manifest |
| [check-release-artifacts.sh](check-release-artifacts.sh) | Asserts built archives, deb, rpm and the image actually contain the legal files (`--strict` fails on unverifiable channels); run by release/nightly after packaging |
| [entrypoint.sh](entrypoint.sh) | Agent-container startup; distinct from the server entrypoint under `docker/` |
| [ci/](ci/) | CI planning/verdict, test budgets, browser report comparison, release smoke and publishing helpers; callers in `.github/workflows/` define their environment |

Publishing helpers under `ci/` are workflow components, not general local test
commands. They may publish or remove release/package artifacts.

## Static checks and test infrastructure

| Location | Checks |
| --- | --- |
| [agents-invariants/](agents-invariants/) | The machine-checkable repository rules in AGENTS.md |
| [docs-inventory/](docs-inventory/), [docs-surface-check/](docs-surface-check/) | API/CLI documentation and documentation surface/link checks |
| [lint-migrations/](lint-migrations/) | Migration ordering and immutability |
| [lint-tsformat/](lint-tsformat/) | Timestamp formatting conventions |
| [deps-drift/](deps-drift/) | Dependency drift checks |
| [docker-api-surface/](docker-api-surface/), [dockerfile-sources/](dockerfile-sources/) | Docker API and build-source contracts |
| [go-toolchain-pin.sh](go-toolchain-pin.sh), [pr-image-build-paths.sh](pr-image-build-paths.sh) | Toolchain version and CI image-path consistency |
| [no-root-binaries.sh](no-root-binaries.sh), [skip-budget.sh](skip-budget.sh) | Root build-output guard and test-skip budget (`skip-budget.txt`) |
| [typecheck-tests.mjs](typecheck-tests.mjs) | Test fixture type checking via `pnpm test:types` |
| [check-control-accessibility.tsx](check-control-accessibility.tsx) | Renders controls in Chromium and writes measurements/screenshots into a fresh private directory under the OS temporary directory (printed on stderr and retained for inspection); requires browser dependencies |
| [cli-command-smoke.sh](cli-command-smoke.sh), [cli-exit-code-contract.sh](cli-exit-code-contract.sh) | Built CLI parser/help and exit-code contracts |
| [test-harness-integrity.sh](test-harness-integrity.sh) | Checks the acceptance harness itself |
| [ci/cli-subprocess-race.py](ci/cli-subprocess-race.py) | Runs named authentication, conversation and stream boundaries with both the outer tests and shared CLI subprocess instrumented; rejects missing/skipped tests and preserves JSON execution evidence |
| [ci/general-race-shard.py](ci/general-race-shard.py) | `python3 scripts/ci/general-race-shard.py run INDEX COUNT TIMEOUT_SECONDS`: runs a complete package partition under the race detector; checked-in measured costs balance work, while fresh `go list` inventory determines coverage; writes the partition record to `.ci-results/` |
| [ci/api-race-shard.py](ci/api-race-shard.py) | `python3 scripts/ci/api-race-shard.py run INDEX COUNT TIMEOUT_SECONDS`: runs one race partition of `RACE_SHARD_PACKAGE` (default `internal/api`; CI also partitions `cmd/crewship`); `report ARTIFACT_DIRECTORY COUNT BASELINE_SECONDS` validates that the shards cover the full inventory exactly once and summarizes their combined budget |
| [ci/vitest-coverage-inventory.mjs](ci/vitest-coverage-inventory.mjs) | Internal Vitest merge helper: independently discovers the configured uncovered-source baseline through the locked V8 provider, without running tests or cleaning reports; unsupported provider API changes fail closed |

Go checker directories are run with `go run ./scripts/<directory>` unless their
local instructions say otherwise. Companion `*-test.sh`, `*-test.mjs` and
`*_test.go` files test the tooling; consult the file before treating a test
name as proof that it never contacts a server.

## Integration tests, demonstrations and measurements

| Location | Requirements and effects |
| --- | --- |
| [ci/managed-environments.py](ci/managed-environments.py) | Requires local Docker `alpine:3`; runs five named synthetic environment fixtures with isolated resources; fails on skipped/missing tests; no provider credentials or global cleanup |
| [api-contract/](api-contract/README.md) | API contract/fuzz checks; use its isolation, authentication and pacing instructions |
| [test-harness/](test-harness/) | CLI-driven acceptance scenarios; may create and delete resources on the selected server |
| [test-pages-apps.sh](test-pages-apps.sh) | Pages application acceptance checks; inspect required build/runtime setup |
| [demo/](demo/), [setup-shipfast.sh](setup-shipfast.sh) | Creates/configures demo resources; some scenarios run agents and use provider credits |
| [validate-flow.sh](validate-flow.sh) | CLI → API → Docker flow; includes an optional provider-backed agent run and cleanup |
| [e2e-agent-run-test.sh](e2e-agent-run-test.sh), [e2e-devcontainer-test.sh](e2e-devcontainer-test.sh), [e2e-connections.sh](e2e-connections.sh) | Live integration scenarios with server/container prerequisites and resource mutations |
| [e2e-backup-test.sh](e2e-backup-test.sh), [e2e-backup-container-test.sh](e2e-backup-container-test.sh) | Backup tests; the container round-trip deliberately removes/restores a test canary |
| [verify-memory-bundle.sh](verify-memory-bundle.sh) | Legacy instance/schema probe using direct SQLite writes inside rollback transactions; not read-only, and not for shared/live databases |
| [verify-sidecar-memory-write.sh](verify-sidecar-memory-write.sh) | Starts a sidecar against temporary storage and exercises writes |
| [memory-retrieval-bench/](memory-retrieval-bench/) | Memory retrieval measurements; requires the `memorybench` build tag; inspect corpus and scale options |
| [walkthrough.sh](walkthrough.sh) | Executable historical demo with resource mutations and provider-backed runs/evaluations; use `demo/` for maintained scenarios |

Confirm the target, credentials, data lifecycle and cost before running any
live scenario. Fresh-onboarding tests require a disposable unbootstrapped
instance; resetting a shared developer database is not test setup.

## Repository administration and installation

| Script | Side effects |
| --- | --- |
| [claim-issue.sh](claim-issue.sh) | Posts issue claims/releases; `--check` and `--list` inspect existing claims |
| [review-status.sh](review-status.sh) | Reads reviews; retrigger options post review requests |
| [setup-branch-protection.sh](setup-branch-protection.sh) | Changes remote repository protection settings |
| [deploy-dev.sh](deploy-dev.sh) | Can publish a branch and deploy/restart a remote development instance |
| [install.sh](install.sh) | Downloads/installs the product; inspect installation options |
| [install-hooks.sh](install-hooks.sh) | Installs local Git hooks |
| [install-backup-timer.sh](install-backup-timer.sh) | Installs systemd backup/rotation timers |
| [sentry-bootstrap.sh](sentry-bootstrap.sh), [sentry-setup-alerts.sh](sentry-setup-alerts.sh) | Changes Sentry configuration; bootstrap also writes GitHub Actions secrets |

## Adding or moving a script

Place a reusable helper with the tool or domain that owns it. Keep public build,
test and installation tools here; put private one-off investigations in
[private context](../docs/development/private-context.md). Document callers,
arguments, dependencies, outputs, target selection and side effects. Add an
entry here for a new entrypoint, and test nontrivial executable changes.

Before moving an existing file, update its CI, Makefile, package scripts,
container COPY instructions, imports and documentation together. Duplicate
looking names can have different contracts; do not consolidate on name alone.

`acceptance-restricted-workflow-browser.mjs` runs only against the owned
`TestLiveRestrictedWorkflowBrowser` synthetic fixture and a production static
export. It exercises private routine and declared Page action forms; it must not
be pointed at a shared development instance.
