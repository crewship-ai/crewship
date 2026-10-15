# Browser acceptance tests

Choose a suite by its server and data prerequisites. The configurations
below serve different purposes; combining them would mix fresh-instance,
authenticated-server and isolated-fixture tests.

## Suite selection

| Configuration | Tests and prerequisites | Caller |
| --- | --- | --- |
| [playwright.artifact-runtime.config.ts](../playwright.artifact-runtime.config.ts) | Public HTTPS CSP/sandbox characterization; suite-owned fixture, no app or credentials; all three engines | [artifact-runtime.yml](../.github/workflows/artifact-runtime.yml) |
| [playwright.board-editor.config.ts](../playwright.board-editor.config.ts) | Real pinned Excalidraw editor and production Board components: draft saving/retries/conflicts/revocation, static frames, isolated undo/export/Mermaid experiments; explicit mocked API, no product server or credentials | [artifact-runtime.yml](../.github/workflows/artifact-runtime.yml) |
| [playwright.config.ts](../playwright.config.ts) | Main local suite with an explicit `testIgnore` list; requires an authenticated test instance | `pnpm test:e2e`, `pnpm test:e2e:ui` |
| [playwright.pr.config.ts](../playwright.pr.config.ts) | `pr-contract.spec.ts`: authenticated desktop workflow and phone contract | [ci.yml](../.github/workflows/ci.yml), `playwright-pr` job |
| [playwright.fresh.config.ts](../playwright.fresh.config.ts) | `onboarding-wizard.spec.ts`; disposable, never-bootstrapped database, no shared login state | `onboarding-journey` in [ci.yml](../.github/workflows/ci.yml), plus the [devcontainer workflow](../.github/workflows/e2e-devcontainer.yml) |
| [playwright.nightly.config.ts](../playwright.nightly.config.ts) | Authenticated seeded instance; workflow selects gate/drift buckets | [nightly-e2e.yml](../.github/workflows/nightly-e2e.yml) |
| [playwright.reveal.config.ts](../playwright.reveal.config.ts) | `credential-reveal.spec.ts`; isolated fixture server on port 3914 | `playwright-pr` in [ci.yml](../.github/workflows/ci.yml) |
| [playwright.pool-ui.config.ts](../playwright.pool-ui.config.ts) | `provider-pools.spec.ts`; isolated fixture server on port 3913, no provider request | `playwright-pr` in [ci.yml](../.github/workflows/ci.yml) |
| [playwright.restricted.config.ts](../playwright.restricted.config.ts) | `restricted-member-shell.spec.ts`; isolated fixture server on port 3912, every API response mocked | `playwright-pr` in [ci.yml](../.github/workflows/ci.yml); gate guarded by [restricted-shell-gate-test.sh](../scripts/restricted-shell-gate-test.sh) |

The three isolated UI suites serve the built static export; follow their CI
build prerequisites when reproducing them. Their fixed ports are separate
from the live development slots. The shared server/URL settings for the main,
fresh, PR and nightly suites live in [playwright.shared.ts](../playwright.shared.ts).

The Board editor suite instead starts its own loopback Vite fixture. Prepare
local fonts with `node scripts/prepare-board-assets.mjs`, then run
`pnpm exec playwright test --config=playwright.board-editor.config.ts`.
Its deterministic in-memory store is test scaffolding: passing these tests is
not evidence of production authorization, durable storage, or live collaboration.
The Mermaid case exercises a trusted literal and explicit insertion; untrusted
generation input, converter isolation and durable job idempotency require their
own product tests before enabling that feature.

The Chromium touch case uses real touch input against the production Board
components with a simulated API. It covers drawing, moving a pin over its inert
static preview, and inserting the local library. It does not exercise interactive
iframe activation. The runtime component cases use the production React frame,
channel and bootstrap over local HTTPS with a simulated host lease. They verify
Stop/Restart, revision replacement and lease teardown in full Chromium, including
a running infinite loop. They do not establish production domain admission or
authorize enabling the interactive profile.

The artifact runtime preparation cases require the canonical Go bootstrap
harness. Precompile it with `go test -c -o /tmp/pagebuild.test ./internal/pagebuild`,
then set `CREWSHIP_TEST_RUNTIME_BINARY=/tmp/pagebuild.test` when running
`pnpm exec playwright test --config=playwright.artifact-runtime.config.ts`.
On the shared development host, wrap builds/tests with `crewship-heavy`.
The fixture starts the existing Go test server in preparation mode and relays
its actual response headers and HTML into the distinct local HTTPS runtime site;
there is no TypeScript copy of bootstrap JavaScript or CSP. The normal Pages
Apps browser harness mode remains unchanged. These tests do not register a
production artifact runtime route, accept an interactive publication profile,
or establish a zero-egress or renderer termination guarantee.

## What fails CI

The authoritative nightly membership is `GATE_SPECS` and `DRIFT_SPECS` in the
[nightly workflow](../.github/workflows/nightly-e2e.yml). Do not copy those
changing lists into this README.

- **Gate bucket:** failures fail the workflow. Promote a repaired spec only
  after verifying it with the nightly configuration and concurrency.
- **Drift bucket:** existing outcomes are recorded in
  [e2e-drift-baseline.json](../scripts/ci/e2e-drift-baseline.json). New failures,
  flaky results, skips, unexecuted tests and missing tests fail the workflow;
  this bucket is not unconditionally allowed to fail. The workflow also
  maintains an issue describing outstanding test debt.
- **Coverage guard:** detects unclassified browser specs, subject to the
  workflow's explicit exclusions. Update the relevant bucket when adding or
  renaming a spec.

`visual.spec.ts` is explicitly excluded: its checked-in images target macOS,
while CI runs Linux, and several tested routes need updating. Rewriting the
spec and regenerating the appropriate baselines belong together; exclusion is
not evidence of passing visual coverage. The main local configuration has
additional ignored legacy specs; nightly selection is a separate mechanism.

CI installs browsers through the shared
[setup-playwright-browser action](../.github/actions/setup-playwright-browser/action.yml).
That action handles browser caching and dependency installation; use the same
path when changing workflows rather than adding another installation recipe.

## Running against a development instance

Install dependencies and the browser, then select an already-running disposable
or dedicated test instance:

```bash
pnpm install
pnpm exec playwright install chromium
export PLAYWRIGHT_BASE_URL=http://localhost:3013
export CREWSHIP_INSTANCE_ID=local-dev3
# Set E2E_EMAIL and E2E_PASSWORD to an existing test user's credentials.
pnpm test:e2e --project=chromium
```

The URL above is the dev3 example; choose the instance assigned to your work.
Tests can create, update and delete resources. Do not point them at production
or use a shared database as a fresh-onboarding fixture.

Setting a nonempty `PLAYWRIGHT_BASE_URL` disables automatic Next.js startup.
Without it, shared configuration starts/reuses `pnpm dev` at `NEXT_PORT`
(default 3001); this does not start the Go backend. `pnpm test:e2e:ui` uses the
same main configuration with Playwright's interactive interface.

On a host missing browser libraries, install the required dependencies through
your host provisioning or `pnpm exec playwright install --with-deps chromium`.
The latter changes system packages and may require elevated permissions.

## Authentication and server fixtures

[global-setup.ts](global-setup.ts) authenticates once using `E2E_EMAIL` and
`E2E_PASSWORD`, then writes state under
`~/.crewship/e2e-auth/crewship-e2e-auth-<instance>.json`. The instance comes from
`CREWSHIP_INSTANCE_ID`, then `NEXT_PORT`, then `default`; set a distinct value
for concurrent targets. The directory is created with mode 0700 and the state
file is tightened to 0600. It contains authenticated cookies and must not be
committed or attached to an issue.

The main, PR and nightly configurations reuse this state. Use
[fixtures/auth.ts](fixtures/auth.ts) for ordinary authenticated feature specs;
repeated per-test logins can hit authentication rate limits. Fresh-onboarding
and isolated UI fixtures intentionally use different setup.

Some PR/nightly scenarios require server-generated inbox or feedback state
that a provider-free test instance would not otherwise produce. CI starts its
server with `CREWSHIP_E2E_FIXTURES=1`; the opt-in handlers in
[internal/api/e2e_fixtures.go](../internal/api/e2e_fixtures.go) create that state
through the intended producer paths. A normally started server does not expose
those fixture routes. Use a dedicated test instance to reproduce these flows;
the fixture flag is not a requirement for ordinary product operation.

## Adding coverage

Place browser specs in `e2e/`, with shared fixtures in `e2e/fixtures/`. Keep
component and frontend logic tests beside their implementation for Vitest.
Choose the correct configuration and CI bucket, state any provider/Docker/data
prerequisites, and use cleanup appropriate to resources the test creates.

Prefer a shallow route smoke check or a focused feature interaction. The PR
contract subset does not stand in for provider-backed chat or agent execution;
those require their own runtime fixtures. A manual probe or a screenshot from
a past run is not a substitute for a reproducible regression test.

## Deterministic routine executions on a live server

`routines-live-contracts.py` runs 50 saved recipes through an authenticated CLI
against a real server, then repeats one recipe 20 times and checks transform
admission. It creates its own workspace and restricted crew, verifies business
outputs against durable run records, writes each result immediately, and deletes
only its generated workspace. The active CLI profile is never changed.

```bash
crewship-heavy python3 e2e/routines-live-contracts.py \
  --binary /path/to/crewship \
  --server http://127.0.0.1:8082 \
  --output /mnt/dev-workspace/tmp/builds/routines-live-new-run
```

The output directory must be new. Use the same server hostname the CLI token was issued for. Pass `--profile`
when running from a worktree outside the configured directory binding. An OWNER account able to create workspaces and
crews is required. Setup or cleanup failures must be investigated; they are not
successful runs. This suite covers deterministic transforms, expr/CEL, ordered
foreach, DAG dependencies and conditions. Fifty recipes and twenty repetitions
are reported separately. Browser flows, models, HTTP credential injection,
integrations, permissions and other API domains belong in additional suites;
this result cannot certify the whole product. No paid model or external service
is called.

Use `--bucket access` to provision one fresh synthetic account and exercise
MEMBER, MANAGER, ADMIN and VIEWER against saved routines and a second workspace.
The account is intentionally retained because the public API cannot delete it;
its memberships and temporary CLI config are removed. No existing account or
password is changed. Add `--browser` to verify that a MEMBER sees both low and
medium notifications addressed to its crew and its workspace in the browser
API and Inbox UI. This requires the installed Playwright dependency and browser.
Credentials reach the browser driver through stdin and are excluded from reports.

Cross-workspace requests explicitly set `workspace_id` to the target workspace:
the CLI injects its selected workspace into query parameters, and the server
resolves that parameter before the path's workspace ID.

### Complex foreach and real queue contention

Select `--bucket workflows` with the same command and a fresh output directory.
It executes five 32-item foreach recipes with two body steps, upstream outputs,
shared inputs and different parallelism limits. It then enqueues 14 co-due
agentless runs, submitting the two high-priority requests last. Short datetime
waits hold the dispatcher slots so the test can observe whether high-priority
requests enter the saturated first wave. Every result is checked for its own
input-derived output, zero cost, durable completion and lack of lost/duplicate
runs. This measures deferred dispatch, not contention on a model agent.

The queue test takes about 35 seconds after setup and needs a test instance with
an otherwise quiet dispatcher. It has a 65-second polling deadline and records
per-run delay after `fire_at`; concurrent completion order is not guaranteed.
An unobservable first wave fails the check rather than silently claiming
priority coverage.

The workflows bucket also verifies state written by a routine, read by its next
run, repaired through the operator API, and removed/cleared. It checks budget
set/clear and negative-value validation with a zero-cost routine; enforcement
against actual paid spend still requires a separate authorized model scenario.

Harness failure-path checks (simulated subprocess/API failures, no real run):

```bash
crewship-heavy env PYTHONDONTWRITEBYTECODE=1 python3 e2e/routines_live_contracts_test.py
```

Cleanup attempts all fixture resources even when cancellation, logout or a
workspace deletion fails. Temporary credential removal runs in `finally`;
the scenario reports cleanup failures after all attempts.

## Artifact runtime characterization (A0)

`crewship-heavy pnpm exec playwright test --config=playwright.artifact-runtime.config.ts`
executes the public [runtime suite](artifact-runtime/runtime.spec.ts) against
suite-owned HTTPS fixtures, without a Crewship server, login, private files or
external service. It requires OpenSSL and the installed Playwright browsers.
The [workflow](../.github/workflows/artifact-runtime.yml) runs Chromium, Firefox
and WebKit separately on PRs, merge groups and main. Chromium explicitly uses
`channel: chromium` (the full browser), because its process isolation differs
from the default headless shell. No test silently skips an unavailable engine.

Studio, runtime and observer use separate registrable `.test` domains. A bounded
suite-owned CONNECT proxy maps these HTTPS sites to loopback; certificates live
in a temporary directory and are removed at teardown. No host configuration or
certificate trust store is changed. Proxy connections, including deliberately
stalled responses, are destroyed at teardown. Test and action timeouts fail the
run; caught launch/evaluation failures cannot turn into successful results.

These tests characterize the proposed CSP and sandbox fixture, rather than a
shipped artifact server. They assert specific observed blocked attempts within
a bounded observation window and the known self-navigation escape: immediate
navigation, redirects, 204 and stalled responses can reach the observer before
a second iframe `load`. A green result confirms this limitation remains
reproducible; it does not certify zero egress or make second-load removal a
security boundary. The preconnect case records CONNECT tunnels and HTTP hits
but is explicitly inconclusive: absent HTTP traffic cannot establish absence
of DNS/TCP speculation. Both separate-tab closure and cross-site iframe responsiveness/removal are
asserted independently. A browser that freezes Studio fails the iframe test;
passing the tab case does not establish iframe isolation. A console marker emitted
immediately before the hostile loop proves it started before responsiveness is
measured. Closing/removing the browsing context and continued Studio ticks do
not measure underlying renderer process termination or reclaimed CPU.

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
## Docs D1b production traversal

`docs-d1b-acceptance.mjs` runs the embedded application and the managed Node
Docs service through the approved throwaway helper. Build a complete release
bundle first: the selected `crewship` binary needs adjacent `crewship-sidecar`
and `entrypoint.sh`, plus a real embedded static export. In an isolated worktree:

```bash
crewship-heavy make build
crewship-heavy env D1B_ACCEPTANCE_BINARY="$PWD/crewship" \
  D1B_ACCEPTANCE_OUTPUT=/tmp/crewship-docs-d1b-evidence \
  node e2e/docs-d1b-acceptance.mjs
```

Use your own output directory. The installed Playwright Chromium must be
available. The harness creates its own users, sessions, private sockets and
service secret; it never uses a development environment's sessions. It covers
Page creation, concurrent human edits, a lost response with exact retry,
comments, simple formatting, checkpoint comparison/restore, Markdown export
and unchanged Page identity. HTTP barriers delay real requests without
fabricating responses. Reports identify the binary hash and actual Node build.

This traversal explicitly does not perform a model-backed agent request.
A genuine user-to-agent-to-result-card run requires a separately authorized
model/runtime configuration; human semantic API edits are not its substitute.
