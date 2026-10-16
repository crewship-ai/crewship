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
| Plan repository merge policy | `python3 scripts/ci/repository-policy.py --output /tmp/policy-plan.json` | Reads the current main ruleset and prepares a preservation-first queue/CodeQL plan. Explicit `--apply --plan ... --documentation-commit ...` changes live rules only after review; see [the policy runbook](../docs/runbooks/ci-cd-implementation-2026-09-11.md#review-and-apply-repository-policy-2932) |
| Plan repository merge policy | `python3 scripts/ci/repository-policy.py --output /tmp/policy-plan.json` | Reads the current main ruleset and prepares a preservation-first queue/CodeQL plan. Every `--apply --plan ...` requires `--analysis-commit ...` for reviewed source already on main and actual PR canary review; queue activation also requires `--documentation-commit ...`. See [the policy runbook](../docs/runbooks/ci-cd-implementation-2026-09-11.md#review-and-apply-repository-policy-2932) |
| Slides canonical model contract | `node scripts/check-slide-model.mjs` (Node24, Go; run via `crewship-heavy`) | Compares actual Go/TS schema1 canonical JSON and domain-separated deck/element hashes against the shared Unicode fixture |
| Page content registry contract | `node scripts/check-page-content-types.mjs` (Node 24, Go; run via `crewship-heavy`) | Compares actual Go and TypeScript compiled descriptors and derivations against one shared fixture |
| Documentation inventory | `make docs-inventory:strict` | Checks documented API/CLI references and writes ignored inventory reports |
| Built chat XLSX worker | `pnpm test:chat-xlsx-worker` after `pnpm build` | Exercises the shipped local XLSX worker in Chromium with synthetic saved formulas, coordinates, corruption, macros and resource bounds |
| Built chat table worker | `pnpm test:chat-worker` after `pnpm build` | Exercises the actual bundled worker in Chromium with synthetic quoted Unicode data, malformed input and resource bounds; requires the Playwright browser |
| Page content browser compatibility | `scripts/throwaway-server.sh run NAME --binary OWN_BINARY -- node e2e/pages-content-types-smoke.mjs` with `PAGE_TYPES_OUTPUT` | Real embedded empty Page attachment; explicitly injected future metadata must not mount content adapters |
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
| [check-chat-table-worker.mjs](check-chat-table-worker.mjs), [check-chat-xlsx-worker.mjs](check-chat-xlsx-worker.mjs) | Chromium acceptance of built `out/chat-workers` bundles with synthetic CSV/XLSX; runs through `pnpm test:chat-worker` / `pnpm test:chat-xlsx-worker` in CI Playwright PR subset; no live server or external parser |
| [prepare-chat-workers.mjs](prepare-chat-workers.mjs) | Bundles chat preview workers as local JavaScript before frontend dev/build; public output is generated and ignored |
| [prepare-pdf-assets.mjs](prepare-pdf-assets.mjs) | Prepares browser PDF assets; invoked by frontend dev/build commands |
| [prepare-board-assets.mjs](prepare-board-assets.mjs) | Copies pinned Excalidraw fonts and license notices for Board fixtures; `--production` prepares local assets for dev/build |
| [prepare-board-converter.mjs](prepare-board-converter.mjs) | Builds the pinned Mermaid converter into a local browser IIFE; called by Board asset preparation for fixtures and production. Generated `public/board-converter/runner.js` is ignored; production needs no Node process. |
| [gen-avatar-fixtures.mjs](gen-avatar-fixtures.mjs) | Regenerates avatar validator test fixtures |
| [generate-team-avatars.mjs](generate-team-avatars.mjs) | Generates demo team portraits; distinct from validator fixtures |
| [gen-notices.sh](gen-notices.sh) | Regenerates third-party notices from Go dependencies; invoked by `make notices` |
| [gen-license-bundle.sh](gen-license-bundle.sh) | Builds `build/licenses/` + flat copy: Go and npm dependency texts with SHA-256 manifests and project legal files; invoked by `make licenses`; consumed by GoReleaser and nfpm; Docker generates its bundle in build stages |
| [gen-frontend-licenses.mjs](gen-frontend-licenses.mjs) | Collects scoped and unscoped npm dependency texts from the installed locked tree; checks coverage and writes a SHA-256 manifest |
| [check-release-artifacts.sh](check-release-artifacts.sh) | Asserts built archives, deb, rpm and the image actually contain the legal files (`--strict` fails on unverifiable channels); run by release/nightly after packaging |
| [entrypoint.sh](entrypoint.sh) | Agent-container startup; distinct from the server entrypoint under `docker/` |
| [ci/](ci/) | CI planning/verdict, test budgets, browser report comparison, release smoke and publishing helpers; callers in `.github/workflows/` define their environment |

`ci/prepare-native-release.sh /absolute/new/context-directory` prepares the
independent native worker release context: authenticated upstream bytes, runner,
recipe and license bundle. It performs downloads and Go builds, never Docker
publication or provider calls. The main-only `Native Worker Release` workflow
owns candidate publication and signing; see the
[release procedure](../docs/guides/restricted-native-codex.md#independent-release-candidates).

Publishing helpers under `ci/` are workflow components, not general local test
commands. They may publish or remove release/package artifacts.

## Static checks and test infrastructure

| Location | Checks |
| --- | --- |
| [agents-invariants/](agents-invariants/) | The machine-checkable repository rules in AGENTS.md |
| [host-paths/](host-paths/) | AST check for independent host filesystem roots; existing exceptions must remain present, and CI prevents allowlist expansion against the PR base |
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
| [ci/native-runtime.py](ci/native-runtime.md) | Explicitly approved released native GHCR digest and reviewed AppArmor host required; runs the named restricted Codex race acceptance, verifies image/binary identity and removes run-labeled containers/volumes on timeout/cancellation; no paid provider |
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

`acceptance-invitation-browser.mjs` runs only through the owned
`TestInvitationBrowser` fixture and a freshly built production static export.
The fixture is Linux-only and excluded from ordinary tests. Existing CI
`go vet -tags conformance ./...` compiles it; that is not browser execution.
An explicit tagged run fails if its prerequisites are missing.
Run `go test -tags invitation_browser ./internal/api -run ^TestInvitationBrowser$` with
`CREWSHIP_INVITATION_BROWSER=1` and provide the installed
Chromium path through `PLAYWRIGHT_BROWSERS_PATH` when required. The fixture
passes a private temporary JSON file with synthetic accounts and invitation
proofs; the script exercises new-account signup and existing-account acceptance
through real browser sessions. It creates no containers or persistent server
and must not target a shared instance. Output contains scenario statuses only.
`acceptance-member-navigation-browser.mjs` is called only by the opt-in
`TestMemberNavigationBrowser` test after `pnpm build`. It serves that static
export through the production Router against a temporary synthetic database,
checks desktop/mobile navigation and foreground revocation, then closes the
owned httptest server. It creates no execution runtime or Docker resources and
must not target a shared instance. Install Playwright Chromium and preserve its
cache path explicitly when the Go test isolates HOME:

```bash
PLAYWRIGHT_BROWSERS_PATH="$HOME/.cache/ms-playwright" CREWSHIP_MEMBER_NAV_BROWSER=1 go test -tags member_navigation_browser ./internal/api -run '^TestMemberNavigationBrowser$' -count=1 -timeout 6m
```

`CREWSHIP_MEMBER_NAV_OUTPUT` optionally selects the report/screenshot directory;
the default is an OS temporary path. The fixture JSON is created with mode 0600
and deleted by the Go test; reports contain synthetic results, not session tokens.

The member-navigation browser harness is compiled only on Linux with
`member_navigation_browser` or `conformance`. The existing required Go CI job
runs `go vet -tags conformance ./...`, which compiles this test but does not
execute Chromium. Executing either tagged test set requires the explicit browser
environment and real static export; missing configuration fails rather than skips.
`go test -tags conformance ./...` therefore also requires those prerequisites.


`acceptance-artifact-workflow-browser.mjs` is invoked by the Linux-only
`TestArtifactWorkflowBrowser` harness (`artifact_workflow_browser` or
`conformance` build tag). Build the real static export first. It uses an owned
httptest server, migrated temporary database and synthetic preauthenticated DB
sessions with production authorization routes; it does not mock HTTP, perform
login, execute agents, send email, create Docker resources or contact dev4.
It verifies HTML upload, commenter discussion and dispatch denial, exact saved
revision comparison, and restore preserving the original comment anchor.

```bash
crewship-heavy pnpm exec prisma generate # generated TypeScript types only
crewship-heavy pnpm build
crewship-heavy env PLAYWRIGHT_BROWSERS_PATH="$HOME/.cache/ms-playwright" CREWSHIP_ARTIFACT_WORKFLOW_BROWSER=1 go test -tags artifact_workflow_browser ./internal/api -run '^TestArtifactWorkflowBrowser$' -count=1 -timeout 6m
```

`CREWSHIP_ARTIFACT_WORKFLOW_OUTPUT` optionally chooses a local report directory;
otherwise output uses an OS temporary directory. Fixture session tokens remain
in a private mode-0600 temporary file that the Go test removes. Missing opt-in,
static export or Chromium fails rather than skips. The conformance tag permits
compile-only checks (`-run '^$'`); this harness is not automatically executed by
CI and does not establish real-agent acceptance or login-flow coverage.

`acceptance-board-mermaid-browser.mjs` is called only by Linux-only
`TestBoardMermaidWorkflowBrowser` (`board_mermaid_workflow_browser` or
compile-only `conformance`). It uses migrated temporary storage, real Router
handlers and production UI with two synthetic authenticated sessions. The
canonical converter bootstrap and built pinned runner use separate owned HTTPS
loopback sites. It seeds admitted Mermaid source without executing an agent.
An aborted insertion before network leaves the job ready across client loss;
two independently converted proposals are held before the real POSTs race.
The test checks one commit, the existing-result receipt and reload persistence.
It also sends HTML, directives, links, oversized source and an unsupported
diagram directly through the canonical converter's one-shot MessageChannel,
bypassing Studio prevalidation. It requires opaque-origin, readable rejection
without geometry or Studio DOM execution. A third owned HTTPS canary has a
successful browser reachability control; hostile resource successes fail in
both browser evidence and server counters. Attempted and failed requests are
recorded without claiming zero DNS/TCP speculation or general network silence.
It mocks no API responses and creates no Docker or persistent dev resources.

```bash
crewship-heavy env PLAYWRIGHT_BROWSERS_PATH="$HOME/.cache/ms-playwright" CREWSHIP_BOARD_MERMAID_BROWSER=1 go test -tags board_mermaid_workflow_browser ./internal/api -run '^TestBoardMermaidWorkflowBrowser$' -count=1 -timeout 6m
```

Build the real static export first. Missing Chromium, export or opt-in fails;
`CREWSHIP_BOARD_MERMAID_OUTPUT` selects the evidence directory. Synthetic session
credentials remain in a private temporary file removed by the Go fixture.

`acceptance-folder-tree-browser.mjs` is called only by the opt-in Linux
`TestFolderTreeBrowser` fixture after a real `pnpm build`. It exercises folder
creation, nesting, reparenting, reload, hidden ancestors and revocation through
the production Router with synthetic preauthenticated sessions; no HTTP mocks,
shared instance or execution containers. Run through `crewship-heavy`:
`crewship-heavy env CREWSHIP_FOLDER_TREE_BROWSER=1 go test -tags folder_tree_browser ./internal/api
-run '^TestFolderTreeBrowser$' -count=1 -timeout 6m`.
Missing prerequisites fail explicitly; `conformance` vet compiles but does not
execute this browser fixture.

`CREWSHIP_FOLDER_TREE_OUTPUT` optionally selects an owned retained directory for
synthetic tree screenshots, reader-filtered JSON and report. The report records
current source head/dirty state; capture evidence does not establish deployed or
live WebSocket behavior. Keep this directory outside the public source tree.
- `check-slide-mutations.ts` + `slide-mutation-contract/`: execute TS-planned native Slides mutations through actual Go strict decode/semantic apply/canonical output; run through `crewship-heavy pnpm exec tsx scripts/check-slide-mutations.ts`.
- `acceptance-slides-workflow-browser.mjs`: opt-in `slides_workflow_browser` real Router/DB/production static native Slides human flow using owned synthetic sessions; not login/model acceptance. Captures private evidence under operator-selected output.

- `acceptance-docs-rich-browser.mjs`: opt-in `docs_rich_browser` actual Router/SQLite/managed Node/static editor flow with two distinct synthetic human identities. Run after production export through `crewship-heavy env CREWSHIP_DOCS_RICH_BROWSER=1 go test -tags docs_rich_browser ./internal/api -run '^TestDocumentsRichBrowser$' -count=1 -timeout 6m`. Set `CREWSHIP_DOCS_RICH_OUTPUT` to an owned private evidence directory. Covers exact upgrade retry, durable links/code, schema2/3 checkpoint restore and revocation draft retention; no response mocks or model claim.
- `acceptance-slides-comments-browser.mjs`: opt-in `slides_comments_browser` actual Router/SQLite/static native comments flow with distinct synthetic COMMENT and WRITE humans. Run after production export through `crewship-heavy env CREWSHIP_SLIDES_COMMENTS_BROWSER=1 go test -tags slides_comments_browser ./internal/api -run '^TestSlidesCommentsBrowser$' -count=1 -timeout 6m`. `CREWSHIP_SLIDES_COMMENTS_OUTPUT` selects a private owned evidence directory. Missing prerequisites fail; no shared login, response mocks or model claims.


`acceptance-page-identity-browser.mjs` runs only through the owned Linux
`TestPageIdentityBrowser` after a real static export. Synthetic sessions access
the actual Router and database; the fixture renames the original Page and
reuses its slug between identity resolution and detail GET. It checks refusal,
explicit retry, canonical ID retention, clipboard, reload and leaf-folder ACL
revocation with hidden ancestors. No API responses are mocked. Run:
`crewship-heavy env CREWSHIP_PAGE_IDENTITY_BROWSER=1 go test -tags page_identity_browser ./internal/api -run '^TestPageIdentityBrowser$' -count=1 -timeout 6m`.
Missing prerequisites fail. The script prints its private temporary screenshot,
filtered identity JSON and report directory; credentials are not persisted in
evidence. The Go fixture owns and removes its 0600 credential input.

### Native search managed projection gate

The native search materializer's actual Router/private Node test is selected
explicitly, separately from `TestDocumentsFoundationRealProcesses`:

```bash
CREWSHIP_NATIVE_SEARCH_MANAGED=1 go test -tags docs_foundation ./internal/api -run '^TestNativeSearchManagedProjection$' -count=1 -timeout 5m
```

Install the frozen `tools/docs-service` dependencies first and use Node 24.21.0.
The existing Docs foundation workflow runs this gate after its process-recovery
test. It exercises real human CRDT updates, checkpoint restore, exact session
revocation and final query snapshot coverage. A green process-recovery test
alone does not execute this search gate. On the shared development host, run
these commands through `crewship-heavy`.

### Native search browser gate

`acceptance-page-search-browser.mjs` runs through Linux-only
`TestNativeSearchBrowser` with real Router, private Node, canonical Docs/Slides
and a built static export. It uses distinct owned synthetic human sessions,
indexes a private matching Page as owner, tests authorized body-text results,
immutable-ID navigation after rename/slug reuse, actual grant revocation plus
bounded batch expiry and stopped-engine refusal. No HTTP responses are mocked.
After frozen service/frontend installation and production export, run:

```bash
crewship-heavy env CREWSHIP_PAGE_SEARCH_BROWSER=1 go test -tags page_search_browser ./internal/api -run '^TestNativeSearchBrowser$' -count=1 -timeout 6m -v
```

Missing prerequisites fail. `CREWSHIP_PAGE_SEARCH_OUTPUT` selects an owned
private output parent; each execution creates a fresh subdirectory. The script
prints private report, filtered result JSON and full screenshot paths; no credentials are captured in evidence. The
Go fixture owns and removes its 0600 credential input and private Node process.
`acceptance-slides-selected-browser.mjs` runs only through the opt-in Linux
`TestSlidesSelectedBrowser` fixture after a production static export. Run
`CREWSHIP_SLIDES_SELECTED_BROWSER=1 go test -tags slides_selected_browser ./internal/api -run '^TestSlidesSelectedBrowser$'` through `crewship-heavy`, preserving the installed
`PLAYWRIGHT_BROWSERS_PATH` when HOME is isolated. Optional
`CREWSHIP_SLIDES_SELECTED_OUTPUT` selects owned evidence output. It serves real
Router/SQLite/static UI with separate synthetic WRITE/READ sessions, verifies
actual textarea selection, exact lost-ack retry, cancellation, original-revision
conflicts and confirmed result preview. The production worker uses a deterministic
trusted Store replacement instead of model dispatch; this is not model or new
sidecar execution evidence. No execution containers or shared server are used.
