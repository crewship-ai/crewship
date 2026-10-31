# PRD acceptance suite

The PRD acceptance suite turns the release acceptance scenarios (PRD §7) into
repeatable tests. One command runs every scenario and prints one row per
requirement with its result, the exact build it ran against and the path to its
evidence:

```sh
pnpm acceptance:prd --origin https://your-dev.example   # full mode, deployed instance
pnpm acceptance:prd --quick                             # quick mode, local test server
```

Code: [`e2e/acceptance/prd/`](../../e2e/acceptance/prd/), configuration
[`playwright.prd-acceptance.config.ts`](../../playwright.prd-acceptance.config.ts).
It is a separate suite: the main, PR and nightly Playwright configurations do not
select `*.acceptance.ts` files, and nothing runs it implicitly.

## Modes

| | Quick | Full |
| --- | --- | --- |
| Target | An already running local test server, by default `$PLAYWRIGHT_BASE_URL` or `http://localhost:8080` (the ephemeral server CI's browser jobs start with `crewship seed --offline-demo`) | A deployed instance given by `--origin` or `PRD_ORIGIN` |
| Duration | Minutes | As long as the scenarios need (20-minute timeout per test) |
| Agent | None; tests that need a real agent are **skipped** and recorded as skipped | Seeded agent from `PRD_AGENT_SLUG` / `PRD_AGENT_WORKSPACE_ID`; missing → **failed** |
| Extra accounts | Missing role → skipped | Missing role → failed |
| Use | CI, behind an opt-in switch | Release verification against a dev or staging instance |

`--origin` without `--quick` implies full mode. Full mode refuses to run without
an origin. Neither mode starts a server; start and seed the test server first.

Scenarios create their own disposable QA workspace and delete it afterwards.
They never add members to a shared demo workspace: a membership change there
disconnects other people's open editors. Synthetic people provisioned by a
scenario get `@example.test` addresses and random passwords held only in memory;
the public API cannot delete accounts, so they remain on the target.

## Environment

Every origin, account and location comes from the environment. Nothing in the
repository names a real host or account.

| Variable | Meaning |
| --- | --- |
| `PRD_ORIGIN` | Origin under test (`https://host[:port]`, no path). `--origin` sets it. Browser scenarios that open the native Word/Sheet editor must use the instance's configured public origin: the editor iframe is allowed only there by the CSP `frame-src`, so on `http://localhost:<port>` or any other host name the editor shows "Editor se bezpečně nepřipojil" |
| `PRD_MODE` | `quick` or `full`. The runner sets it from `--quick` / `--full` / `--origin` |
| `PRD_OWNER_EMAIL`, `PRD_OWNER_PASSWORD` | Required. Workspace owner able to create workspaces. Quick mode falls back to `E2E_EMAIL` / `E2E_PASSWORD` |
| `PRD_MEMBER_EMAIL`, `PRD_MEMBER_PASSWORD` | Optional existing second person |
| `PRD_READER_EMAIL`, `PRD_READER_PASSWORD` | Optional existing read-only person |
| `PRD_WORKSPACE_ID` | Optional existing QA workspace; scenarios then reuse it instead of creating one, and do not delete it |
| `PRD_AGENT_SLUG`, `PRD_AGENT_WORKSPACE_ID` | Full mode: seeded agent and the workspace it belongs to |
| `PRD_CLI` | Path of the `crewship` CLI binary for CLI-driven scenarios (`prd.requireCli()`) |
| `PRD_CRAFT_NATIVE` | `on` or `off`: whether the origin has a native Word/Sheet editor profile. Unset, full mode assumes `on` and quick mode reads `GET /api/v1/documents/status`. The craft-lease scenarios decide from it, never from the initialize call they check |
| `PRD_OUTPUT_DIR` | Results directory. The runner's `--output` sets it; default `test-results/prd-acceptance/<mode>-<timestamp>` |
| `PRD_KEEP_WORKSPACES` | `1` keeps created QA workspaces for inspection |
| `PRD_TRACE` | `1` keeps Playwright traces of failed tests. Off by default because traces contain request bodies, including logins |

Global setup logs in each configured role once and stores the session cookies
in `~/.crewship/e2e-auth/prd-acceptance-<role>-<origin hash>.json` (directory
0700, file 0600) next to the other e2e cookie jars. Cookies never go into the
output directory. A wrapper that runs commands in a clean environment must
pass these variables through explicitly.

A login lives 15 minutes, and contexts that share one login share its refresh
token: the editor rotates it, and the next context to present the old token is
treated as a replay, which revokes the whole session (`session_revoked` for
every holder). So global setup only checks that the accounts can sign in; each
test signs the owner in again, and every extra context (`prd.api(role)`,
`prd.browserContext(role)`, crashable browser profiles) gets its own login.

## Cleanup

Tests delete what they create: pages, Designs and Projects are registered
(`trackPage` in `created.ts`) and removed after the test, also after a failure,
with a fresh login; QA workspaces are deleted with everything in them. Names
carry the prefix `PRD ` (slugs `prd-`). Leftovers of earlier or interrupted runs:

```
PRD_OWNER_EMAIL=… PRD_OWNER_PASSWORD=… PRD_AGENT_WORKSPACE_ID=… \
  node e2e/acceptance/prd/run.mjs --origin https://host --cleanup [--dry-run] [--older-than 60]
```

It deletes only workspaces whose slug is `prd-qa-<label>-<10 hex>` AND name starts
with `PRD acceptance `, and pages whose slug starts with `prd-` AND name with
`PRD ` (in `PRD_AGENT_WORKSPACE_ID` / `PRD_WORKSPACE_ID`). Objects matching only
one rule, or younger than `--older-than` minutes (default 60), are listed and kept.

## Output

```
<output>/
  run.json        origin, mode, app SHA (GET /api/v1/system/version → commit), roles, start time
  rows/*.json     one row per test and requirement
  evidence/<scenario>/<test>/…   files the tests wrote
  summary.json    run + counts + sorted rows
  summary.md      the table the runner prints
  playwright/     Playwright output (failure screenshots, optional traces)
```

A row is:

```json
{
  "requirement": "B.project.version",
  "title": "Agent revises the project after a comment",
  "scenario": "project",
  "test": "agent publishes v2 after a comment",
  "result": "passed | failed | skipped",
  "appSha": "<40-character commit of the server>",
  "evidence": "evidence/project/agent-publishes-v2-after-a-comment/result.json",
  "evidenceFiles": ["…"],
  "mode": "full",
  "origin": "https://…",
  "reason": "skip reason or first line of the error",
  "durationMs": 1234,
  "finishedAt": "…"
}
```

Rows are written by the suite's reporter, not by the test body, so failures,
timeouts and skips are recorded too. The runner exits non-zero when Playwright
fails, when any row failed, or when no row was recorded (a suite that ran
nothing proves nothing). A test without a requirement annotation is recorded as
`UNMAPPED …` and failed.

## Adding a scenario

Create `e2e/acceptance/prd/<scenario>.acceptance.ts`. The file name without
`.acceptance.ts` is the scenario name in the table. Import everything from
`./fixtures`:

```ts
import { loginStorageState } from "./auth"
import { expect, prdRequirement, test } from "./fixtures"

test.describe("project", () => {
  test(
    "agent publishes v2 after a comment",
    { annotation: [
      prdRequirement("B.project.version", "Agent revises the project after a comment"),
      prdRequirement("B.project.share", "A shared version stays read-only for the reader"),
    ] },
    async ({ prd, request, page }) => {
      const agent = prd.requireAgent()          // full mode only; skipped in quick mode
      const ws = await prd.qaWorkspace()        // disposable workspace, deleted after the test
      const reader = await prd.provision("VIEWER")
      const readerCtx = await prd.browserContext(await loginStorageState(prd.origin, reader.account))
      // … drive the product through `page` (owner), `request` (owner API) or the CLI …
      await prd.evidence("result.json", { ws: ws.id, version: 2 })
    },
  )
})
```

- `page`, `context` and `request` are authenticated as the owner; their
  `baseURL` is the origin under test.
- `prd.requireFull(reason)` skips in quick mode. Gate only what genuinely needs a
  deployment or a real agent; everything else should also run in quick mode.
- `prd.requireAccount(role)`, `prd.api(role)` and `prd.browserContext(role)`
  use the configured `owner`, `member` and `reader` accounts.
  `prd.provision(role)` creates a new person in the QA workspace instead;
  log that person in with `loginStorageState` from `./auth`.
- `prd.evidence(name, data)` writes a file into the output directory. The first
  file becomes the row's evidence path. Never write credentials, cookies or
  setup links into evidence.
- `prd.appSha`, `prd.mode` and `prd.origin` are available for evidence.
- Clean up anything that outlives the QA workspace. Tests run serially, with no
  retries.

A scenario driven through the CLI uses the same structure: take the binary from
`prd.requireCli()` (`PRD_CLI`), spawn it from the test with `--server prd.origin`
and an isolated `HOME`, and record its output as evidence.

Unit tests for the runner's table and arguments:
`node --test e2e/acceptance/prd/report.test.mjs`.
