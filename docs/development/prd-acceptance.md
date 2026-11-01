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
| `PRD_HOUR_MINUTES` | Length of the `A.craft.session.hour-word` / `-sheet` work session in minutes. Default 60, and in full mode 60 is required: a shorter run (set `PRD_HOUR_SHORT_RUN=1` to allow it while debugging) does the same work but ends red, so it can never be mistaken for the evidence |
| `PRD_RESTART_COMMAND` | Command that restarts the server under test, for rows that must survive a restart (`B.project.restore-restart`, `A.resilience.restart.*`, `A.craft.session.hour-*`), e.g. `crew restart dev2`. The test waits until `/healthz` reports a new process (lower uptime). Unset and no handshake → the row fails in full mode, is skipped in quick mode. `crew` refuses to run under `crewship-heavy`, so a queued run uses `PRD_RESTART_HANDSHAKE` instead |
| `PRD_RESTART_HANDSHAKE` | Directory for a restart done by the operator's shell: the test writes `restart.request` and waits for `restart.done`; `e2e/acceptance/prd/restart-watcher.sh <dir> <env>` answers it by running `crew restart <env>` |
| `PRD_RESTART_NOTE_FILE` | Optional file; one line (time, reason) is appended before every restart |
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

## Craft session rows

The restart rows (`A.resilience.restart.*`, `A.craft.session.hour-*`) restart
the server under test from inside the test, through `PRD_RESTART_COMMAND`
(for example `crew restart dev3`) or, where that command cannot run under
`crewship-heavy`, through `PRD_RESTART_HANDSHAKE` (a directory: the test writes
`restart.request` and waits for `restart.done`, which a watcher in the operator's
own shell answers); `PRD_RESTART_NOTE_FILE` gets one line before each restart.
The restart is proven by `GET /healthz`: a new process reports a lower uptime.
Without a command or a handshake these rows fail in full mode. Run them serially
and only when needed: a restart interrupts everyone on the instance.

`craft-hour.acceptance.ts` (`A.craft.session.hour-word`, `-sheet`) is one real
work session in the native editor of a deployed origin, driven in a browser
for `PRD_HOUR_MINUTES` (60). A provisioned person types continuously, presses
Enter, changes the font and styles, inserts a table and an image (Sheet: cells
of every data class, formulas, formats, a second sheet) and saves once a
minute. Halfway through, with unsaved typing in the editor, the test runs
the restart command. After the restart it checks that everything saved is
there, that the unsaved typing is offered back as a local draft, that nothing
was saved behind the person's back and that the editor works again after a
reload; then it keeps working, reloads once more and checks the document a
last time. The row's `result.json` carries the duration, the restart time and
the SHA-256 of the final document.

`craft-restart.acceptance.ts` (`A.resilience.restart.word`, `.sheet`) is the
short form of the restart: unsaved typing, restart, reload, draft offered, no
duplicate version, editor usable.

`craft-write-revoked.acceptance.ts` (`A.craft.write-revoked-word`, `-sheet`)
withdraws the write right of a person who holds the open editor and has unsaved
typing: the editor turns read-only, the server refuses further writes, and the
draft is kept and offered (also after a reload once the right returns).

`craft-agent-free-edit.acceptance.ts` (`A.craft.agent.free-edit-word`, `-sheet`)
lets the seeded agent edit a free document while a person watches it in the
browser: the person sees "Upravuje <agent>", then the new version.

### Manual walkthrough: `A.craft.session.hour-word`

The Word hour is a manual row (`mode=manual`) because the Word editor is driven
through a canvas ribbon and, on r5, drops to read-only while typing continues
during an autosave in flight (automation reproduces it in the first cycle:
`PRD_HOUR_WORD_AUTOMATED=1 ... --grep "Word: an hour"`; the failure evidence has
`failure-steps.json`, the editor calls and the lease polls, which show the server
still saw the person as holder). Procedure, on the canonical Craft origin, as a
non-owner person in a QA workspace, recording the screen (video) for the whole hour:

1. Note the app SHA (`GET /api/v1/system/version` -> `commit`) and the start time.
2. Create a Word document, take the editor. For 30 minutes type continuously, press
   Enter, change font and size, apply Heading 1 and bold/italic, insert a 2x2 table
   and a picture. Press Ctrl+S about once a minute. Screenshot after the table and
   after the picture.
3. At minute 30, while typing a steady stream, restart the server (`crew restart dev3`,
   one line in ROZHRANI first). Screenshot the editor right after.
4. After the restart: check that everything saved is there, that the unsaved typing
   is offered as "Místni koncept" (Porovnat), reload, take the editor, type, save.
5. Work 30 more minutes, reload, and compare the document with the screenshots.
   Compute the SHA-256 of the saved bytes (`crewship` CLI or the editor API).

Manual row (same fields as the automatic ones): `id=A.craft.session.hour-word`,
`result=passed|failed`, `appSha`, `mode=manual`, `evidence` (path of the recording and
a notes file with the times, SHA-256 and what the document held after the restart),
`recordedAt`.

## Quota rows that change instance limits

`quota.aggregate.workspace` changes the instance-wide workspace artifact-storage
limit through the product CLI (`crewship admin instance artifact-storage set`,
`PRD_CLI`, signed in as the owner with a private `HOME`). Limits are not per
workspace: the scenario first puts its QA workspace just under the smallest
accepted limit (1 MiB) at the normal limit, lowers the workspace limit for the
checks only (about a minute), and restores the original value in a `finally`
block. While it is lowered, other workspaces over 1 MiB cannot grow (nothing is
deleted). The row's evidence records the original, lowered and restored values
and how long the limit was lowered. Needs an instance admin owner and a native
Word editor on the origin.

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

## Agent workspace

The scenarios that need a real agent (`A.craft.proposal.*`, `A.craft.agent.*`, `B.design.agent-*`, `B.project.agent-*`)
cannot run in a disposable `prd-qa-*` workspace. An agent's model login is a
credential owned by one workspace; `crewship credential` has no way to share it
into another workspace, and copying its value would mean handling a secret the
harness must never see. A new workspace's agent therefore has no credential
(`crewship agent credential-readiness` says so) and its first run fails. So the
agent, its chat and the pages it works on live in `PRD_AGENT_WORKSPACE_ID`,
which on the dev instances is the shared demo workspace.

That workspace has the caps of any workspace: 100 pages (HTTP 422 on create) and
the Project/Design history budget (HTTP 413, or the agent answering that "the
artifact history quota is full"). When other work fills it, these rows fail on
capacity, not on the product; the failure messages say so. The harness never
deletes pages it did not create.

To make the rows independent of the demo workspace, an operator creates one
dedicated agent workspace once (the credential step is the only one that needs
the secret, so it is a person's job, not the harness's):

```
crewship workspace create --name "PRD agent home" --slug prd-agent-home   # not named "PRD acceptance ...": the harness cleanup deletes those
crewship crew create --name agents --workspace prd-agent-home
crewship agent create --name Jamie --slug prd-agent --crew agents --workspace prd-agent-home \
  --llm-provider ANTHROPIC --llm-model claude-haiku-4-5
crewship credential create --type AI_CLI_TOKEN ... --workspace prd-agent-home   # the one manual step
crewship credential assign <credential> prd-agent --workspace prd-agent-home
crewship agent credential-readiness prd-agent --workspace prd-agent-home        # must say ready
crewship workspace artifact-policy show --workspace prd-agent-home              # note "version"
crewship workspace artifact-policy set --workspace prd-agent-home \
  --base-version <version> --enabled=true --crew-id <agents crew id>
```

The agent artifact policy must be enabled and list the agent's crew: Project,
Design and native Craft agent rows are refused with `403` without it.

Add the existing member (`PRD_MEMBER_*`) to that workspace, then run with
`PRD_AGENT_SLUG=prd-agent PRD_AGENT_WORKSPACE_ID=<its id>`. Design must be
enabled there.

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
`node --test e2e/acceptance/prd/report.test.mjs`. The same file checks the
IDs statically (`emitted-ids.mjs` reads the `prdRequirement(...)` calls of the
spec sources): every ID a spec emits must be in `required-ids.json`, either
as a row or under `diagnostic` (the `harness.*` smoke rows, which the RC gate
does not count); every required ID must be emitted by exactly one call; and a
`${...}` placeholder in an ID must be one of the known Word/Sheet loop
variables in `PLACEHOLDERS`. A renamed or forgotten row therefore fails in CI,
not as `missing` on the RC.

## RC gate: required IDs

`pnpm acceptance:prd` alone reports only what ran, so a scenario nobody wrote
shows nothing. The RC gate closes that hole. [`required-ids.json`](../../e2e/acceptance/prd/required-ids.json)
lists every ID the release must prove (PRD §7 and §0), including IDs whose tests
do not exist yet; those are reported as `missing`, which is a failure. Each row
names its canonical origin as a key: `craft` (Word and Sheet editor scenarios) or
`artifacts` (Projects, Design, quota, drafts). The URLs are not stored in the
public repository; pass them with `--origin-map craft=<url>,artifacts=<url>` or
`PRD_RC_ORIGINS`. The gate refuses to run without them. Adding a scenario means
adding its ID here in the same change.

A required ID passes only when its latest row has result `passed`, `mode=full`,
the canonical origin and an `appSha` equal to the release candidate. Exceptions:

- `A.craft.lease.fail-closed` may be `skipped` (servers with a native editor only).
- Rows marked `allowManual` (`A.craft.session.hour-*`) may be `mode=manual` with
  a link to the recording in `evidence` and every check of the row's
  `manualChecks` set to `true` in `checks` (Enter, font, styles, table, image,
  reload, restart, content after restart); otherwise the row fails. No other
  ID accepts a manual record.
- Evidence from an older SHA counts only through `--carryover` (below).

Several runs assemble one RC. Per ID the latest row on the RC SHA wins; a row
on another SHA counts only when none exists on the RC, and then fails as `appSha`
unless a carryover record covers it.

A carried row keeps the SHA it was proven on; the gate never rewrites it to the
RC. It records `carriedFrom` with the `git diff --stat <fromSha> <RC>` over the
row's scenario paths plus `sharedPaths` from `required-ids.json`
(API handlers and their authorization and sharing checks, server and
websocket layers, pages, Design, artifacts, backup, saving, drafts,
migrations, runtime config, the release profile). The diff must be empty and the engine pins (`engines.from`,
`engines.rc`) present and equal. Rows marked `alwaysRerun` never carry over: every
row on the `craft` origin (each drives the native lease or save path that
#4658 and #4662 changed, including `A.lock` and the agent edit rows) and, on
Artifacts, save, durability, work-kept, conflict and restart rows. The origin, result and mode checks
apply to the carried row itself.
Run the gate from a checkout that has both SHAs.

```sh
# gate the run just made
pnpm acceptance:prd --origin <craft url> --rc <SHA> \
  --origin-map craft=<url>,artifacts=<url> --merge <earlier run dir> --manual manual-rows.json

# merge existing runs only, no browser
node e2e/acceptance/prd/report.mjs --rc <SHA> --origin-map craft=<url>,artifacts=<url> \
  --merge runs/craft --merge runs/artifacts --manual manual-rows.json --output close-ab/rc-<SHA>
```

`--merge` takes a run directory or a `summary.json` and may repeat. The gate
prints each problem (`missing`, `result`, `mode`, `origin`, `appSha`,
`evidence`) with its ID and exits non-zero (2 for bad arguments), and writes
`rc-gate.json` (and `rc-gate.md` with `report.mjs --output`). A rejected
carryover is reported as `carryover`.

`manual-rows.json` is an array; the origin defaults to the row's canonical one:

```json
[{ "id": "A.craft.session.hour-word", "result": "passed", "appSha": "<SHA>",
   "evidence": "https://…/recording", "recordedAt": "2026-10-31T10:00:00Z",
   "checks": { "enter": true, "font": true, "styles": true, "table": true, "image": true,
               "reload": true, "restart": true, "content-after-restart": true } }]
```

`carryover.json` is an array:

```json
[{ "id": "B.design.two-humans", "fromSha": "<evidence SHA>",
   "paths": ["e2e/acceptance/prd/design.acceptance.ts", "internal/design"],
   "engines": { "from": "<pins on evidence run>", "rc": "<pins on RC>" } }]
```
