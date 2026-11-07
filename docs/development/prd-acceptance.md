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
| `PRD_ORIGIN` | Origin under test (`https://host[:port]`, no path). `--origin` sets it. Browser scenarios that open the native Word/Sheet editor must use the instance's configured public origin: the editor iframe is allowed only there by the CSP `frame-src`, so on `http://localhost:<port>` or any other host name the page refuses the editor at once and names the configured address ("Editor je dostupný jen na adrese …"). "Editor se bezpečně nepřipojil" is a different failure: the editor did not finish its start within the bootstrap deadline |
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
The API contexts of the long rows (`outage-lostack/actor.ts`, used by
`craft-hour`) outlive the 15-minute access cookie, so they renew the session on
a `no_credentials`/`session_expired` 401 and repeat the call
(`session-refresh.ts`). Each of them has a login of its own (never the
browser's) and only refreshes that session; it never logs in again during a
row, so a revoked session (whose refresh is refused) still fails the row.

The rows that type unsaved lines (handoff, revoked right, restart, hour) count a
line as accepted only when the editor took input before and after it. The
status line alone does not show that: with unsaved work it reads "Neuložený
koncept" also after the custody moved away, so the custody toolbar is read too
("Požádat o předání" or "Pouze čtení" there means the editor no longer takes
input; `craft-session/lines.ts`, `editorTakesInput`). Every per-line check is
written to `lines.json` before the row asserts on the copy.

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

A cycle's typing counts as saved only when the server head holds every marker
it typed (`craft-session/confirm.ts`). The save label is not proof: a step
earlier in the same cycle (the table, the picture, the second sheet) saves too,
so its "Uloženo · verze N" can be on screen before the cycle types anything. A
cycle whose markers are not on the server fails with the label, the baseline
and the head version, and its markers are not counted as saved work. The Word
table step types the cell text (with a space on each side, so it cannot glue
onto a marker) right after the 2x2 insert and checks on the server, within the
step, that it is inside a table cell. When it is not, the row records the
failure "typing right after 2x2 insert did not reach the document" (#4877: the
ribbon insert does not return the keyboard to the document) and gives the
keyboard back through a click on the ribbon's Insert tab, never through a
click at a fixed point of the page; the caret stays where the insert put it.
Before and after each attempt the step compares every saved marker on the
server head: a marker split by the cell text is reported as `harness-input`
with the surrounding text and is not counted as a lost save, and a marker gone
without the cell text in it stays a possible loss. A failed table step runs at
most once more in a later cycle, and never again after it changed saved text.
Failure lines carry the status line with `data-save-phase`/`data-save-reason`,
and step screenshots are kept per cycle (`step-table-c<cycle>-*.png`).

The hour rows keep their QA workspace (it is not deleted after the row) and
write its workspace and page ids to `document-ref.json` and `result.json`, so
the stored versions can be read after a failure. `result.json` also carries a
compact evidence chain per cycle (`chain`): the status line and save trace the
editor showed right after the typing (the canvas text itself is not observable
from the page), every save request of the cycle (SHA-256 of the body, base
version, time, and the reply status and version as finally answered, with the
requests still unanswered at the cycle's end listed apart), the stored head after the cycle
(version, SHA-256, how many saved markers it holds and the first missing ones,
where the table cell text is), and the label shown after the save; plus
`checkpoints` before and after the restart and after each reload (version,
SHA-256, equality with the previous checkpoint, the marker report). The chain
and checkpoints are also written to `chain.json` however the row ends (a session
that throws, a failed final assertion, a pass).

Both files also carry `saveAccounting`, how each save of the hour happened. A
save's origin is the save request that committed its version, not a key press:
the ops trail records every request when it is sent, and every harness press
records the trail length at dispatch (`opsAtPress`).

- `autosaves`: save waits (one per cycle, one more after an "Ověřit výsledek"
  press, and the Sheet warm-up) whose version was committed by a request sent
  before any press. `lateAutosaves` counts those whose reply arrived only after
  the harness had pressed Ctrl+S: the press merely confirmed bytes already saved
  (#5075).
- `forcedCtrlS`, `forcedCtrlSOutcomes` and `forcedCtrlSEvents`: every Control+s
  the harness pressed, either in the save wait after 4 s without a new version or
  in the Word table step after 4 s with the cell text not yet in a table cell of
  the server head and the status "Neuložený koncept". Each event is written at
  dispatch and closed in a `finally`, with the cycle, time, reason, `endedAt`,
  whether a new version followed, and an `outcome`: `new-save` (a request sent
  after the press committed the version), `confirm-only` (an earlier autosave
  did), `no-version`, `unattributed` (no answered save carries the version), or
  `incomplete` (the wait was interrupted after the press).
- `ambiguousSaves` / `ambiguousSaveOps`: committing save requests sent within
  30 s after a forced press. They are counted neither as autosaves nor as
  forced saves, and their press's outcome is `unattributed`.

The limit, plainly: the harness cannot see what triggered a save request. An
autosave that was already being prepared when Ctrl+S went out (engine snapshot,
local draft write, validation) sends its request after the press, exactly like
the forced save would. The editor bounds each of those three steps by its own
10 s deadline and promises nothing shorter, so any committing request within
30 s after the press is reported as ambiguous. In practice this means most
forced presses that are followed by a save come out `unattributed`; `new-save`
is claimed only for a request sent more than 30 s after the press. The count of
presses (`forcedCtrlS`) is exact either way.
- `editorReconciles` / `editorReconcileOps`: reconcile requests sent outside
  every press window. `reconcilesDuringPressWait` / `...Ops`: reconcile requests
  sent between a press (Ctrl+S or "Ověřit výsledek") and 2 s after the end of
  its wait. The requests carry no trigger, so the network trail cannot tell the
  press from the editor's own retry timer; these are reported apart as cause
  unknown, never attributed to either side.
- `verifyPresses`: "Ověřit výsledek" presses; each press records `waitEndedAt`.

The raw records are in `chain.json` as `saveWaits` and `forcedCtrlS`. The row's
note starts with the measured press counts, then the inferred origins, e.g.
"forced Ctrl+S 6×, Ověřit výsledek 0×; inferred from request timing, not proven
(#5112): autosave 57×, press outcomes 6 unattributed, automatic reconcile 0×".
Only the press counts are measured. The editor does not report what triggered a
save, so autosave counts, press outcomes, ambiguous saves and reconcile causes are
inferred from request timing and must not be quoted as fact (#5112). This is
accounting, not a gate: a forced Ctrl+S never fails the row; the server head
still decides whether a cycle's typing was saved.

`pageStalls` (name kept for the evidence readers) lists gaps of at least 10 s
between the editor page's own `craft/proposals` polls; `pageStallsDescription`
says so in the evidence. They are polling gaps, not a measured renderer or
input stall. The note's "longest interruption" (`interruptions.longestStallMs`)
is the longest merged span of such gaps and "Výsledek nepotvrzen" spells up to
an "Ověřit výsledek" press.

`craft-restart.acceptance.ts` (`A.resilience.restart.word`, `.sheet`) is the
short form of the restart: unsaved typing, restart, reload, draft offered, no
duplicate version, editor usable.

`craft-write-revoked.acceptance.ts` (`A.craft.write-revoked-word`, `-sheet`)
withdraws the write right of a person who holds the open editor and has unsaved
typing: the editor turns read-only, the server refuses further writes, and the
draft is kept in the browser but offered to nobody while the right is gone (#5073,
as draft-revocation proves for the same frame). Once the right returns and the
person takes the editor, the draft is offered with every typed line, once and in
order.

`craft-soak.acceptance.ts` changes a fixed-width counter in every save, because an
identical save at the head creates no version (#5075).

`craft-agent-free-edit.acceptance.ts` (`A.craft.agent.free-edit-word`, `-sheet`)
lets the seeded agent edit a free document while a person watches it in the
browser: the person sees `Upravuje <agent>`, then the new version.

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
Word editor, so its canonical origin is the craft origin (#4795).

## Role matrix rows

`role-matrix.acceptance.ts` (`role.*`) changes a person's right on a Project or a
Design while it is open, in the disposable QA workspace with provisioned people.
It needs no agent or model key. The rows are diagnostic: they run on a deployed
release and report, and the RC gate does not count them.

- `role.project.comment-*`: a Comment grant reads the shared version and saves
  comments; every content change is refused and offered nowhere.
- `role.design.read-*`: a Read grant reads the shared version; REST writes and a
  raw socket update are refused, and unshared live edits never reach the browser.
- `role.*.revoke-open`, `role.*.member-removal`: the open tab drops the content
  and says so, a later read, a reload and a new socket are refused. Each row
  first proves that the tab follows new content while the right holds, and
  records every sighting of later content in HTTP bodies and socket frames.
- `role.design.stale-merge`, `role.design.conflict`: one person's Design socket
  is cut in the browser (`routeWebSocket`, HTTP stays up) and both keep working.
  The Design routes take no base version, so a stale person never gets a `409`;
  the only way to a conflict is a stale live canvas. Yjs picks the surviving
  text by client order, so the row checks the notice whichever side won.
- `role.*.revoke-draft`, `role.project.revoke-read-draft`: the right goes with
  unsaved work open. The server takes nothing, the work is kept, and it
  returns with the right. A Project source draft is kept in the browser but
  offered to nobody while the write right is gone, in the open tab as after a
  reload: an open tab is no right of its own (#5073). When the read right goes too, the page has to say the
  draft was deleted: the row proves its storage probe sees the draft first, then
  requires it gone from the screen and from IndexedDB and localStorage, and
  absent after the right returns.

## Projects without a flag change

`project-default.acceptance.ts` (`B.project.default-on`,
`B.project.artifacts-links`, #4994/#4995) is the one project scenario that
never calls `enableProjects()`: its fresh QA workspace must have no
`artifact_projects` override and Projects must already be available. Through
the API it creates v1, publishes v2, gets `409` for a save from the stale base,
shares v2 and restores v1 as v3; in the browser it opens the links those calls
returned. Every returned `url` must be `/artifacts/<page_id>` with the QA
workspace, and the share link must carry `version=2`; the owner and a
provisioned `VIEWER` opening it see v2, not Latest, and an old
`/pages/<slug>?artifact=1&version=2` link lands on the same address. The reader
lists only v2, cannot read v1 or v3 and cannot publish, restore or share. Last,
the workspace override is set off: availability is false, the project routes
answer `404` and the Artifacts detail shows no project preview. On an instance
whose administrator turned the `artifact_projects` default off the row fails by
design. The other project scenarios keep calling `enableProjects()`, which is a
no-op on a default instance.

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
as a row or under `diagnostic` (the `harness.*` smoke rows and the `role.*`
rows, which the RC gate does not count); every required ID must be emitted by exactly one call; and a
`${...}` placeholder in an ID must be one of the known Word/Sheet loop
variables in `PLACEHOLDERS`. A renamed or forgotten row therefore fails in CI,
not as `missing` on the RC.

## Draft revocation proof

`e2e/acceptance/draft-revocation/` proves in a real browser what happens to an
unsaved Word, Sheet and Project draft when access changes: delivery of the
revocation, the client's reaction and the return to the tab or the network, each
timed apart. An offline browser cannot learn of a revocation immediately, so the
offline cases measure the reconnect, not delivery. Hidden-tab cases run in a full
Chromium attached with `noDefaults`, the only setup here where a background tab
really hides. Frozen-tab cases freeze the page through the browser's lifecycle
state, and the frame-by-frame timeline is recorded inside the page, so it shows
what was painted and not what a later poll found. Unchanged returns compare real
screenshots of the editor, which stay in the evidence, and fail on a blank or
one-colour image or on lost content. They then go on from the keyboard alone and
read the server through the API, so a return is proven by work that continues.

It rewrites a server's database and cuts its connections, so it cannot run against
a deployed origin and it is not a row of this suite. `pnpm acceptance:prd` does not
pick it up, its `A.craft.draft.*` IDs are not in `required-ids.json`, and `--gate`
does not count them. **A green RC gate says nothing about these cases.** Run this
command on the release candidate as well:

```bash
pnpm acceptance:draft-revocation \
  --binary "$RC_BINARY" \
  --native-env "$WORD_ENV" --native-env "$GRID_ENV" \
  --output "$OUT"
```

Run it directly, not through `crewship-heavy`: the throwaway server has to stay
outside the queue to be able to stop, and the command puts Playwright through
`crewship-heavy` itself where the host has it. It takes about an hour. It needs
`xvfb-run`, `sqlite3` and a full Chromium; the headless shell alone cannot hide a
tab. It picks Playwright's own Chromium, else the newest one under
`PLAYWRIGHT_BROWSERS_PATH` (default `~/.cache/ms-playwright`), and `--chrome` names
another.

`--binary` is the server to prove, with the embedded app built from the same
commit. `--native-env` names env files holding the `CREWSHIP_CRAFT_NATIVE_*` settings
of both engines. Other lines are ignored, agents stay off, and a missing Word or
Sheet setting stops the run before it starts. It starts a server from
`scripts/throwaway-server.sh` with Page projects and those settings, puts
`revocation-proxy.mjs` in front of it, seeds it, runs every case once and stops
everything. `--grep RE` narrows the run.

| Exit code | Meaning |
| --- | --- |
| 0 | Every listed test ran and passed |
| 1 | A test failed or was skipped, or a listed test left no result. One skip fails the run |
| 2 | Setup failed: arguments, engine settings, tools, server or seed |
| 3 | Partial: a `--grep` run passed. Never read this as a pass |

`$OUT` holds `verdict.json` (verdict, counts, app SHA, the Chromium used),
`summary.md`, `evidence/` with `timing.json` or `timeline.json` per test, and
`playwright/`. Set `REVOCATION_HIDDEN_MS` to change how long the Word case keeps
its tab hidden; the default of 330000 passes the five minutes after which Chromium
throttles hidden timers.

## Native walk

`e2e/acceptance/native-walk/` walks one short working cycle in the native Word
editor (`walk.word`) and one in the Sheet editor (`walk.sheet`) on a deployed
origin, in full headed Chromium under Xvfb, as a person provisioned in a disposable
workspace that is deleted afterwards. Word: typing, Enter, a heading and the No
Spacing style from the ribbon, bold. Sheet: three cells entered with Enter. Then,
for both: autosave, ctrl+s with and without a change, reload, more work, a save
whose reply is lost on the way back, the next save, and release. Each step records
input, confirmation and longest render stall in ms, the versions before and after,
the status, and how often "Ověřit výsledek" was pressed; every press is a failure.

It proves on the RC that the content reaches the server with its styles and cells,
that ctrl+s with a change makes exactly one version and without a change none, that
a reload leaves the document and renders it, that a lost reply is recovered without
a second version, and that a release succeeds and leaves no unsaved draft. A page
error anywhere in the walk fails it. The versions come from the save receipts alone,
checked after the last step and its reload: they are exactly the committed saves the
browser saw plus the dropped reply, they run from the starting head without a gap or
a repeat, the head after the final reload is the last receipt, and nothing saves
after that.

It does not prove:

- **GPU rendering.** The host has no GPU; Chromium renders through SwiftShader in
  software. Stall times are an upper bound for this host, not a client's.
- **Typing latency.** Opening (about 8–9 s) and reloading (about 19–20 s, including the
  wait for the closed page's lease, `leaseWaitMs` in the step note) are measured limits of what a person waits
  for, not per-keystroke latency.
- **Version history.** The listing (`GET …/craft/document/versions`) is behind the
  workspace flag `craft_version_history`, which stays off until the feature is
  accepted. The flag-off 404 (`native version history is not enabled`) is recorded
  as `history: "not-enabled"` and listed under `notVerified` in `verdict.json`; any
  other non-200 fails, and a 200 must list exactly the observed versions.

```bash
pnpm acceptance:native-walk \
  --origin https://crewship-dev3.unifylab.cz \
  --rc "$RC_SHA" \
  --env-file "$HOME/.config/crewship/prd-qa-dev3.env" \
  --output "$OUT"
```

`--rc` is the full commit the origin must serve clean (`GET /api/v1/system/version`).
`--env-file` holds the PRD accounts and is sourced inside the queued job, so no
credential is on a command line. Only the `PRD_OWNER_*`, `PRD_MEMBER_*`,
`PRD_READER_*`, `PRD_WORKSPACE_ID` and `PRD_KEEP_WORKSPACES` assignments reach the run,
and the run's own settings, `PRD_TRACE=0` among them, are set over them. Without
`--env-file` the shell's `PRD_*` account variables are used. Each run gets a random
`runId`, which `run.json`, every row, each `walk.json`, `finished.json` and
`reporter.json` must carry; artefacts of another run on the same RC and origin do not
count. `reporter.json` is written only when every callback of the PRD reporter returned
(Playwright swallows reporter exceptions), and its row count must match the rows read
and the tests listed. Each walk also reads the deployed build at its start and its end;
both must be `--rc`, clean, so a deploy during the run cannot pass as the RC. Run the command directly: it puts Playwright through `crewship-heavy` itself
where the host has it. `--output` must be new or empty. It needs `xvfb-run` and a
full Chromium (Playwright's own, else the newest under `PLAYWRIGHT_BROWSERS_PATH`, or
`--chrome`); the headless shell is refused. `--grep RE` narrows the run.

| Exit code | Meaning |
| --- | --- |
| 0 | PASS: both walks listed, each left exactly one passed row, Playwright exited 0 and wrote this run's `finished.json`, and each `walk.json` has every step, no press, no page error, a released document and contiguous versions |
| 1 | FAIL: a walk, a row (also an unmapped or extra one), the evidence or the run itself |
| 2 | SETUP: arguments, browser, credentials, or the origin does not serve `--rc` clean, or `run.json` is missing |
| 3 | PARTIAL: a `--grep` run passed. Never read this as a pass |

The verdict never reads Playwright's text output. `$OUT` holds `verdict.json`
(verdict, reasons, `notVerified`, counts, app SHA, the Chromium path and version),
`summary.md`, `run.json`, `finished.json`, `reporter.json`, `rows/`,
`evidence/native-walk/<test>/walk.json` with a screenshot after the reload, and
`playwright/`. The rules are `e2e/acceptance/native-walk/verdict.mjs`, tested by
`node --test e2e/acceptance/native-walk/verdict.test.mjs`.

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

A required ID passes only when the latest row of every test that emits it (each
checked on its own, carried rows included) has result `passed`, `mode=full`,
the canonical origin and an `appSha` equal to the release candidate. Exceptions:

- No row may be `skipped`. `A.craft.lease.fail-closed` is proven on the
  artifacts origin, which runs the RC without a native editor profile
  (`GET /api/v1/documents/status` reports `enabled: false`): run it there with
  `PRD_CRAFT_NATIVE=off`, and leave it out of the craft-origin run
  (`-- --grep-invert "native editor off"`), whose server has native editors
  and would only skip it (#4779).
- Rows marked `allowManual` (`A.craft.session.hour-*`) may be `mode=manual` with
  a link to the recording in `evidence` and every check of the row's
  `manualChecks` set to `true` in `checks` (Enter, font, styles, table, image,
  reload, restart, content after restart); otherwise the row fails. No other
  ID accepts a manual record.
- Evidence from an older SHA counts only through `--carryover` (below).

Several runs assemble one RC. Per ID and test the latest row on the RC SHA wins
(a rerun supersedes that test's earlier attempt) and the ID takes the worst of
its tests, so a later passing test never hides a failed one; a manual record
replaces the automated rows of its ID. A row
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
