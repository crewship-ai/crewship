# Browser acceptance tests

Choose a suite by its server and data prerequisites. The six configurations
below serve different purposes; combining them would mix fresh-instance,
authenticated-server and isolated-fixture tests.

## Suite selection

| Configuration | Tests and prerequisites | Caller |
| --- | --- | --- |
| [playwright.config.ts](../playwright.config.ts) | Main local suite with an explicit `testIgnore` list; requires an authenticated test instance | `pnpm test:e2e`, `pnpm test:e2e:ui` |
| [playwright.pr.config.ts](../playwright.pr.config.ts) | `pr-contract.spec.ts`: authenticated desktop workflow and phone contract | [ci.yml](../.github/workflows/ci.yml), `playwright-pr` job |
| [playwright.fresh.config.ts](../playwright.fresh.config.ts) | `onboarding-wizard.spec.ts`; disposable, never-bootstrapped database, no shared login state | `onboarding-journey` in [ci.yml](../.github/workflows/ci.yml), plus the [devcontainer workflow](../.github/workflows/e2e-devcontainer.yml) |
| [playwright.nightly.config.ts](../playwright.nightly.config.ts) | Authenticated seeded instance; workflow selects gate/drift buckets | [nightly-e2e.yml](../.github/workflows/nightly-e2e.yml) |
| [playwright.reveal.config.ts](../playwright.reveal.config.ts) | `credential-reveal.spec.ts`; isolated fixture server on port 3914 | `playwright-pr` in [ci.yml](../.github/workflows/ci.yml) |
| [playwright.pool-ui.config.ts](../playwright.pool-ui.config.ts) | `provider-pools.spec.ts`; isolated fixture server on port 3913, no provider request | `playwright-pr` in [ci.yml](../.github/workflows/ci.yml) |

The two isolated UI suites serve the built static export; follow their CI
build prerequisites when reproducing them. Their fixed ports are separate
from the live development slots. The shared server/URL settings for the main,
fresh, PR and nightly suites live in [playwright.shared.ts](../playwright.shared.ts).

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
