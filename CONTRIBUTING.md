# Contributing to Crewship

Thanks for considering a contribution. This file is the short-form
workflow.

## Before you start

- **Open an issue first** for anything non-trivial so we can sanity-check
  scope before you spend time. Bug fixes with a reproducer are welcome
  without prior discussion.
- **One feature branch per change** — never work directly on `main`.
  Branch naming: `feat/<short-description>`, `fix/<short-description>`,
  `chore/<short-description>`.
- **Claim the issue before your first commit** —
  `scripts/claim-issue.sh <issue>`. Several sessions work this repo at
  once from the same GitHub account; the claim comment is the only thing
  that tells them apart. See
  [Claiming an issue](#claiming-an-issue-before-you-work-it).

## Local development

```bash
pnpm install
cp .env.example .env.local        # set NEXTAUTH_SECRET + ENCRYPTION_KEY
./dev.sh start                    # backend :8080 + frontend :3001
```

Other `./dev.sh` subcommands: `stop`, `restart`, `status`, `seed`,
`nuke`, `logs`. Never start the services manually.

### The `web/out` embed

`web/embed.go` does `//go:embed all:out`, so **`web/out/` must exist at
compile time or nothing in the repo builds** — not the package you
changed, everything. `web/out/` is a gitignored build artifact, so a
fresh clone or `git worktree add` has nothing to embed and every
`go build` / `go test` dies with:

```
web/embed.go:19:12: pattern all:out: no matching files found
```

That error names `web/embed.go`, a file you did not touch, and says
nothing about a missing directory — it has been misread as "my change
doesn't compile" more than once.

**It is fixed, and you should not need to do anything.** One file,
`web/out/.placeholder.html`, is tracked in git precisely to keep the
directory non-empty (see the `.gitignore` block around it). `go build`
and `go test` work in any checkout, worktree included, with no setup.

What you get from that build is a binary with **no UI**: every UI route
answers `503` with a page saying the web UI was not built and naming the
command to run, and `crewship start` logs a warning at boot. Build the
real thing with `make build` (or just run `./dev.sh start`).

Two things to not do:

- **Don't hand-roll a stub** (`echo '<!doctype html>' > web/out/index.html`).
  It satisfies the embed and then serves a blank `200`, which looks like
  a broken frontend instead of a skipped build step.
- **Don't `git add -A` after a build that wiped `web/out/`.** Use
  `scripts/embed-web-out.sh sync` (what `make build` and `dev.sh` call) —
  it preserves the tracked placeholder, so the tree stays clean. Deleting
  the placeholder re-breaks the build for every fresh clone; CI fails with
  a named error if it goes missing.

A release cannot ship a UI-less binary: `scripts/embed-web-out.sh verify`
runs on the release, nightly, image and Binary Build paths and fails
unless `web/out/` holds a real Next.js export (`index.html` **and**
`_next/`).

### Build identity in a worktree (`go build` gets it wrong)

A binary built by a bare `go build` inside `.claude/worktrees/<name>/`
reports the **parent clone's** commit and dirty flag — silently, with
nothing in the output to say so. `crewship version` will happily print a
commit that was never built and "(uncommitted changes)" for a tree with
none (#1686).

It is a Go toolchain limitation, not a repo bug: `cmd/go` recognises a
repository by a `.git` **directory**, and a linked worktree's `.git` is a
**file**, so the search walks up to the enclosing clone and stamps
`vcs.revision` / `vcs.time` / `vcs.modified` from there.

`git` itself honours the `.git` file, so the fix is to ask git and stamp
the answer. `scripts/build-stamp.sh` is the one place that does:

```bash
scripts/build-stamp.sh dirty            # true | false | "" (not a repo)
scripts/build-stamp.sh commit           # full SHA of the tree you are in
go build -ldflags "$(scripts/build-stamp.sh ldflags)" ./cmd/crewship
```

**`make build` and `./dev.sh` already route through it**, so anything they
produce is correct — including every dev slot, where the reported commit
is the only way to tell a stale deploy from a stale CLI. Deploying a slot
from a hand-rolled `go build` re-opens the hole. Never emit a confident
`false` for the dirty bit when it is unknown: `buildinfo` models "nobody
stamped this" as a third state precisely so it is not flattened into
"clean".

### Bumping the Go toolchain (never a one-line change)

Thirteen files name the Go version, and they must all name the same one:

| Where | Line |
|---|---|
| `go.mod` | `toolchain go1.27.0` — the anchor everything else is checked against |
| `Dockerfile` | `FROM golang:1.27.0-alpine` — the compiler for the **shipped** binary |
| ten workflows | `GO_VERSION: "1.27.0"` |
| `.github/workflows/codeql.yml` | a literal `go-version: "1.27.0"` |

`scripts/go-toolchain-pin.sh` parses all of them and fails on disagreement;
it runs in CI's `Shell` job on every PR. Run it before you push:

```bash
bash scripts/go-toolchain-pin.sh
```

Two things it deliberately does **not** do, both worth knowing:

- **It ignores `go.mod`'s `go` directive.** That stays at 1.26 on purpose —
  the language floor is a promise to consumers and is not the same decision
  as which compiler builds the release (#2060).
- **It does not build the image.** The root `Dockerfile` is built by
  `release.yml` and `nightly.yml` only, so image-only breakage (a missing
  `COPY`, a `pnpm prisma generate` regression) is still first caught by
  nightly. That gap is #2064 and is open.

The Dockerfile's `ENV GOTOOLCHAIN=local` is what stops the image from
downloading a different compiler than its `FROM` tag names — the default,
`auto`, would fetch whatever `go.mod`'s `toolchain` line asks for. Leave it as
`local`; a version literal there would be a fourteenth copy of the string, and
it fails backwards — bump the `FROM` tag, forget the literal, and Go downloads
the *old* toolchain and undoes the bump with CI still green.

Do not expect the image build to catch drift for you. Under `local` the
`toolchain` directive is ignored outright, so `toolchain go1.27.1` against a
`golang:1.27.0-alpine` base builds silently with 1.27.0 and exits 0. Only the
`go` directive can fail a build, and it deliberately sits at 1.26. The static
check is the only thing that sees this.

Bumping the toolchain also means **re-checking the analyser pins**.
`golangci-lint` and `govulncheck` each vendor `golang.org/x/tools`, and an
`x/tools` older than the standard library it is asked to analyse dies on
syntax it does not know — 1.26 → 1.27 needed both to move. Their pins carry
comments saying so; the guard cannot check this one for you.

### Version ceilings in `pnpm.overrides`

Most entries under `pnpm.overrides` in `package.json` are security *floors*
(`"ws": ">=8.21.0"`) — raise a transitive dependency past a known CVE. One is
a **ceiling**, and it means the opposite:

| Override | Why |
|---|---|
| `"@sentry/nextjs": "<10.72.0"` | 10.72.0 does not import under a DOM test environment |

JSON has no comments, so the reason cannot live next to the pin. It is this:
the SDK's `node` export condition reaches a vendored bundler plugin that picks
its Node-vs-browser branch on `typeof document === 'undefined'`. Under
`happy-dom` a `document` exists, so it takes the browser branch, builds an
`http:` URL from `document.baseURI`, and hands it to `fileURLToPath` — which
throws `TypeError: The URL must be of scheme file` at module scope. Anything
that transitively imports `@sentry/nextjs` then fails to load at all.

Production is unaffected: `next build` and the server runtime have no
`document`, so they take the Node branch. This is a test-environment fault
only, which is exactly why it reached us through a lockfile regeneration
rather than through a bump anyone reviewed — no `package.json` spec changed.

To lift it, drop the line, `pnpm install`, and run `pnpm test`. If
`lib/__tests__/sentry-scrub.test.ts` still loads, upstream fixed it and the
ceiling can go. Do not raise the ceiling to chase a version without running
that; the failure mode is eleven unrelated suites failing to import, with
every assertion in them still passing, which reads like anything but a
dependency problem.

## Verify any change

Run these locally before pushing — CI will run them too:

```bash
go test ./... -count=1 && go vet ./...      # Go: must pass
pnpm lint && pnpm build                      # Frontend: must pass for UI changes
```

The `cmd/crewship` suite scrubs every `CREWSHIP_*` variable from its own
environment before running, so a shell that exports `CREWSHIP_PROFILE` or
`CREWSHIP_SERVER` for a dev instance can no longer redirect the tests at
your live server. Tests that need one of those vars set it themselves.

For UI changes, also exercise the affected feature in a browser before
declaring it done. Type checking and tests verify code correctness, not
feature correctness.

## House rules (the short list)

- **`pnpm` only** — never `npm` or `yarn`.
- **Migrations are Go-side** in `internal/database/migrate.go`.
  **Never run `prisma migrate`** — Prisma is TypeScript types only.
- **No new API routes in `app/`** — the static export drops them in
  prod. All API routes go in `internal/api/`.
- **Driver name is `"sqlite"`** (not `"sqlite3"`).
- **Sidecar UID 1002, agent UID 1001** — security boundary; do not
  change.
- **No `Co-Authored-By` lines in commits.**
- **Never amend after a pre-commit hook failure** — make a new commit.
- **A detached goroutine in `internal/api` registers with
  `beginBackgroundWork`** (see `internal/api/background.go`). Work that
  outlives its request also outlives the *test* that drove it, where it
  races that test's teardown and fails a bystander at random (#1596).
  A long-lived daemon that genuinely cannot be drained goes in
  `unregisteredSpawnSites` with its reason. A test that hands a storage
  directory to **any** handler with a detached writer takes it from
  `storageDir(t)`, not `t.TempDir()` — `t.TempDir()` fails the test when
  its `RemoveAll` races a late write, and a `t.TempDir()` taken after
  `setupTestDB` is cleaned up *before* the drain runs.
- **`Commands()` on a shared Cobra command is a *write*.** Cobra sorts
  the child slice in place on first call, behind an unsynchronised bool
  — and `ExecuteC` re-arms that bool on **every** run, because
  `InitDefaultHelpCmd` unconditionally removes and re-adds the help
  command. In `cmd/crewship` that let 34 `t.Parallel()` tests enumerate
  `rootCmd` at once, sort the same slice concurrently, and hand back a
  corrupted list — a `WARNING: DATA RACE` blaming a random test in a
  package the PR never touched (#1989). Its `TestMain` now freezes the
  order with `cobra.EnableCommandSorting = false`, taken **after** the
  pristine walk has sorted the tree, so `Commands()` is a pure read for
  the rest of the test binary. Test-scoped: production `main()` still
  sorts, so `crewship --help` ordering is unchanged. What that does
  *not* make safe is **executing** a shared command from a parallel
  test — `ExecuteC` still rewrites the child slice — so don't; build a
  local command tree instead. `cobra_sort_parallel_guard_test.go`
  fails the build on both halves.
- **`go test -race ./internal/api/` needs `-timeout`.** That package
  takes ~23m under the race detector (`ok … 1405s` on a CI runner,
  measured 2026-08-20; local numbers on crewship-dev swing with what
  else is running) and `go test`'s default timeout is 10m,
  so a plain run is **killed mid-test** — it prints a goroutine dump
  headed by whichever `t.Parallel()` test was running when the axe
  fell, and **zero `WARNING: DATA RACE`**. That reads exactly like a
  failure and it is not one; it is the same ghost people chased in
  #1597. Run it as `go test -race -timeout 40m ./internal/api/`.
  CI's dedicated **Go Race (internal/api)** job passes a timeout
  derived from a measured baseline (`RACE_API_BASELINE_SECONDS` in
  `.github/workflows/ci.yml`), so a green CI and a red local run are
  consistent, not contradictory. Before blaming your diff, check the
  log for `WARNING: DATA RACE` — no such line plus a `panic: test
  timed out` means you hit the ceiling, not a race. That ceiling is
  #2031's subject: the CI job now prints its own `ok … Ns` into the
  run summary and warns when it crosses 1.6x the baseline, so budget
  erosion is reported as budget erosion rather than as your bug.
- **Don't hash at production strength in a test.** `internal/api`
  hashes passwords with `bcryptCost` (`internal/api/bcrypt_cost.go`),
  which `TestMain` lowers to `bcrypt.MinCost` for the test binary
  only. bcrypt's cost is exponential, so production's 12 is ~256x
  MinCost, and under `-race` — where blowfish's key schedule is
  instrumented on every array access — a single cost-12 hash costs
  seconds. Never write a literal cost at a call site: four guard tests
  in `bcrypt_cost_test.go` pin the production value at 12, prove the
  test binary really lowered it, reject any call site that passes its
  own number, and reject any write to the var outside the test binary.

## Frontend data fetching

New or migrated client data hooks use **@tanstack/react-query** (the
client is wired in `components/providers.tsx`) instead of hand-rolled
`fetch` + `useState`. Reference implementations:
`hooks/use-dashboard-data.ts` and `hooks/use-inbox.ts`.

- **Query keys**: `[resource, workspaceId, params?]` — e.g.
  `["missions", wsId, { limit: 50 }]`. The workspace id at position 1
  isolates caches across workspace switches and lets
  `invalidateQueries({ queryKey: [resource, wsId] })` scope to one
  workspace. Export a small `…Keys` factory next to the hooks.
- **Transport**: always `apiFetch` from `lib/api-fetch.ts` (never bare
  `fetch`) so 401s go through the shared refresh-once-then-retry path.
  Pass React Query's `signal` through so unmounts abort the request.
- **Freshness**: where a WebSocket event exists (see
  `hooks/use-realtime.tsx`), subscribe with `useRealtimeEvent` and call
  `queryClient.invalidateQueries` — do not poll. A long
  `refetchInterval` (minutes, `refetchIntervalInBackground: false`) is
  acceptable only as a missed-event safety net.
- **Errors**: throw from the `queryFn` when the surface renders an
  error state; map non-ok responses to the slice's empty value for
  best-effort aggregate tiles (see the policy note in
  `hooks/use-dashboard-data.ts`).
- **Tests**: Vitest + `renderHook` with a fresh `QueryClient`
  (`retry: false, gcTime: 0`) per test — see
  `hooks/__tests__/use-dashboard-data.test.tsx`.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/). The
type and scope drive changelog and release tooling, so they are checked
at review time.

```
<type>(<scope>): <imperative summary, ≤70 chars>

<body — what and why; wrap at 80 chars>

<footer — issue refs, breaking-change notes>
```

Common types: `feat`, `fix`, `docs`, `refactor`, `chore`, `test`, `ci`,
`perf`. Scopes mirror top-level package or feature names — `api`,
`keeper`, `sidecar`, `lookout`, `journal`, `orchestrator`, `cli`,
`crews`, `chat`, `memory`, `deps`. Skim `git log` to see the in-flight
conventions before introducing a new scope.

Examples from the actual log:

- `feat(keeper): add L1 fast-path for low-risk credential requests`
- `fix(api): canRole was silently 403-ing on update + delete actions`

Avoid: `update stuff`, `WIP`, `fix typo`. Squash fixups before pushing.

## Pull requests

GitHub auto-fills the body from
[`.github/pull_request_template.md`](.github/pull_request_template.md);
tick the boxes that apply and remove rows that don't.

- Keep PR titles under 70 characters; put the why in the body.
- Reference the issue in the PR body (`Fixes #123`, `Refs #123`).
- Update or add tests when behaviour changes.
- Update this file when you change something a future contributor would
  otherwise have to re-discover.

CI (`ci.yml`) runs `pnpm lint && pnpm build` and
`go test ./... && go vet ./...` on every PR against `main`. The
security workflow runs gitleaks and the dependency audit on the same
trigger. Both must be green for review.

## Changelog entries

`RELEASING.md` cuts release notes from `CHANGELOG.md`'s
`## [Unreleased]` section. That makes a missing entry not untidiness but
a release note that does not exist — and a change nobody is told about.

**A PR that touches `internal/api/`, `cmd/crewship/`, `app/`,
`components/`, `lib/`, `hooks/` or `stores/` must add an entry under
`## [Unreleased]`.** The **Changelog Guard** workflow enforces it. The
last three are peer top-level trees, not sub-directories of the two
above, and they carry as much user-visible behaviour: a chat or socket
fix lands in `hooks/use-chat.ts`, retry and error copy in
`lib/api-error.ts`. Test files inside any of those trees don't count as
user-visible, and Dependabot is exempt by actor.

**The entry has to be under `## [Unreleased]`, and the guard checks that**,
not merely that you touched the file. It compares that one section between
your base and your head, because `RELEASING.md` cuts release notes from it
and nowhere else — a typo fix in a shipped version's section, or a stray
blank line at the bottom, is not a release note and no longer passes. What
it still cannot judge is entry *quality*; `- fix bug` satisfies it. The
guard buys the reviewer that conversation, it does not replace them.

The guard's own logic is unit-tested by
`scripts/changelog-guard-test.sh`, which extracts the step's script
verbatim from the workflow and runs it against a throwaway repository —
including the case that matters most, a `git diff` that dies. That used
to be reported as an empty diff and a green check.

If the change genuinely has no user-visible effect — a chore, an
internal refactor, a test-only fix — apply the **`skip-changelog`**
label. The guard re-runs on `labeled`/`unlabeled`, so it clears within
seconds and no push is needed. Reach for the label when it is true, not
when the entry is inconvenient: the guard exists because 18 user-visible
PRs merged in one window with no trace anywhere — the #2086 audit found
fifteen of them and the backfill turned up three more — including a
breaking credential-model change across 117 files.

Write the entry the way the file already does — lead with the
**user-visible symptom in bold**, then what was actually wrong and what
changed. Mark a change that can break a working setup with
`⚠️ **Behaviour change:**` and say plainly what used to succeed and now
fails. Group under `### Added` / `### Changed` / `### Fixed` /
`### Security`.

`docs/changelog/overview.mdx` is a different surface with a different
job — a curated highlights reel for users, cut per release window, not
a per-PR log. It is deliberately **not** gated; see the scope note at
the top of the page. Adding to it is a release-time editorial call, not
a per-PR obligation.

## CodeRabbit — wait when it is reviewing, not when it is throttled

After `gh pr create`, give CodeRabbit ~2–5 minutes to post its review.
**If a review is coming, do not merge before it does.** Merge first and
the run errors with *"Review failed — PR is closed"*, and any findings
it would have raised are lost for good.

**If it is rate-limited, do not wait for it.** The per-developer limit
runs 30–45 minutes and stacks across a batch, so waiting buys a queue
position rather than a verdict and stalls everything behind it. Review
the PR yourself instead — a red-first test plus a mutation proving the
test has teeth is the standard this repo applies anyway, and it is a
real review where a queue position is not. Then say in the PR what was
machine-reviewed and what was not, and queue a re-review for **your own
PR** (`scripts/review-status.sh --retrigger <PR>`) so it still lands.

Red CI is a separate gate and stays absolute: throttled or not, never
merge on a failing check.

The trap is that the rule does not verify itself. CodeRabbit reports
through a commit **status**, and when it hits the per-developer review
limit that status is:

```
$ gh pr checks 1568
CodeRabbit    pass    0    Review rate limited
```

`pass` — the same word, colour and position as a PR that really was
read, where the description says `Review completed`. On 2026-07-30
eleven of twelve open PRs carried that green and none of them had been
reviewed. Waiting for the check is not the same as waiting for a review.

So ask the thing that cannot lie about it — the posted comments and
reviews themselves:

```bash
scripts/review-status.sh                 # every open PR
scripts/review-status.sh 1568            # one
scripts/review-status.sh --checks        # + skipped-but-green CI checks
```

It reports one state per PR: **reviewed** (a review was submitted, with
its actionable-comment count), **throttled** (a rate-limit notice was
posted instead — not reviewed), **failed**, **pending** (still inside
the window), **absent** (window elapsed, nothing arrived), or
**unknown** when the API call itself failed. Exit code 3 means at least
one PR is not reviewed. It also flags a review that covers an older
commit than the current head: real review, wrong code.

Two of those states arrive as *the same bytes*. A rate-limited
CodeRabbit sometimes still submits an `APPROVED` review with an empty
body and no inline comments; so does a review on the CHILL profile that
read the diff and found nothing. The tie-breaker is the walkthrough
comment, which on a finished review names the range it read —
`between <base> and <head>`. An empty approval counts as reviewed only
when a walkthrough names that same commit; otherwise it stays the
non-event the green check was. So read the headline, not just the tick:
`review approved, 0 actionable comment(s) (empty body; the walkthrough
records a completed review of <sha>)` is a clean review, and any PR
still described as approving "with no content" is not.

Throttling is a queue problem, not something to fight. Re-request the
reviews serially, seeded from the "next review available in N minutes"
the notice itself carries:

```bash
scripts/review-status.sh --retrigger --dry-run   # see the schedule, post nothing
scripts/review-status.sh --retrigger 2227        # re-request that one
scripts/review-status.sh --retrigger --all       # …or every PR above (long-lived; background it)
```

`--retrigger` refuses to run without a target, because it posts and each
post spends the one slot the limit replenishes — an unscoped run spends
other sessions' slots and pushes your own PR to the back of the queue it
just filled (#2231). Firing `@coderabbitai review` at every throttled PR
at once just re-throttles all but the first. And read the answer carefully: a
re-trigger fired while the limit is still in force comes back with

> ✅ **Action performed** — Review finished.

and nothing else. That reply acknowledges the *command*; no review was
submitted, and CodeRabbit will not re-review a commit it has already
seen. `review-status.sh` flags it rather than counting it.

**The same failure shape, other producers.** `--checks` reports them on
the PR's head commit:

- Jobs that concluded `skipped` — green in the checks list without
  having run. (The Go twin of this is why `scripts/skip-budget.sh`
  exists: `go test ./...` prints `ok` for a package whose every test
  called `t.Skip`.)
- Jobs that concluded `neutral`, which `gh pr checks` renders as
  `skipping`. CodeQL reports this way.
- Green jobs carrying **annotations** — CodeQL findings surface in a
  run's annotations, where no check status shows them.

None of this is enforced. CI runs only the script's offline classifier
tests (`scripts/review-status-test.sh`); nothing blocks a merge on a
missing review, because whether a green CodeRabbit status may block a
merge is branch-protection policy and the repo owner's call. The script
is the instrument; the judgement stays with the person merging.

## Issues

Use one of the templates in
[`.github/ISSUE_TEMPLATE/`](.github/ISSUE_TEMPLATE):

- **Bug report** — include a minimal repro, the version
  (`crewship --version`), and the relevant `journalctl` /
  `./dev.sh logs` excerpt.
- **Feature request** — describe the user-facing problem first, then
  the proposed solution. Implementation details can come in the PR.

Security issues are handled separately — see
[SECURITY.md](SECURITY.md). Please don't open public issues for them.

## Claiming an issue before you work it

Several agent sessions work this repo in parallel — ten at once is
normal, and there are ~40 worktrees under `.claude/worktrees/`. They all
push as the same GitHub account, so **the assignee field cannot tell
"taken by another session" from "that's me"**. It is not a lock.

What it costs when nobody claims: #1481 re-fixed what #1471 had already
fixed two hours earlier, from another session, better. It surfaced as a
merge conflict, after both sides had done the work.

### The convention

**Before your first commit on an issue, post a claim comment naming
clone + branch + UTC time. Release it in the same thread when you stop —
whether it shipped or not.**

```bash
scripts/claim-issue.sh 1488                 # checks first, then claims
scripts/claim-issue.sh 1488 --check         # read-only: who holds it?
scripts/claim-issue.sh --list               # every open claim in the repo
scripts/claim-issue.sh 1488 --release "hypothesis unconfirmed, see above"
```

Claiming *checks before it posts*. If another clone or branch holds the
issue it prints the claim and exits **3** without commenting, so you find
out before the work, not at the merge conflict. `--dry-run` prints the
comment and posts nothing; `--force` overrides a refusal (say why in the
thread first). Exit codes: `0` clear or claimed · `2` usage error · `3`
held by another session.

The comment shape is plain text and hand-writable — the script only fills
in what you would otherwise mistype:

```
**CLAIM** — clone `crewship_3` · branch `fix/schedule-editor-save` · 2026-07-30T20:58Z
**RELEASE** — clone `crewship_3` · branch `fix/schedule-editor-save` · 2026-07-30T22:10Z
```

A release ends the claim(s) it names by **clone alone** (#2107) — branch is
recorded for readability but is not part of the match, because a claim is
posted before the feature branch exists (see below) and would otherwise
never be released from the branch that replaced it. A hand-written release
that names a branch but no clone still ends that branch's claims and leaves
the rest — global cancellation is reserved for a release that names neither
field, which is what "released it" means when someone types it without
ceremony.

**Release even when you failed.** #1482 was claimed, the hypothesis did
not hold, and the session said so and released — so the next one started
from evidence instead of re-deriving it. A dead end, written down, is
worth more than a silent unassign.

### When it goes wrong

- **A claim with nobody behind it (session died).** Claims older than 24h
  are reported as `STALE`. Stale still blocks `claim-issue.sh` — that is
  deliberate, because "old" and "abandoned" are not the same thing. Post
  in the thread that you are taking it over, then `--force`. Tune the
  threshold with `CLAIM_STALE_HOURS`.
- **No claim, but a branch or PR already exists.** `--check` also lists
  open PRs and local branches naming the issue number, because someone
  who got as far as pushing has effectively claimed it whether or not
  they commented. Treat that as held: ask in the thread first.
- **The claim is honest but the work was abandoned** — released with a
  reason, or claimed months ago with nothing pushed. The claim comment is
  a record, not a reservation: re-claim it, and say in the thread what
  you are picking up from the previous attempt.
- **The script guesses your identity wrong.** It reads the clone from the
  checkout path (`crewship_3`) and the branch from `git rev-parse`. A fresh
  `git worktree` (the normal way an agent session starts, before its first
  commit) checks out an auto-minted `worktree-agent-<hash>` branch; the
  script refuses that value on sight and falls back to the upstream branch
  if one is already tracked, otherwise the worktree path — no action needed
  from you. A detached-HEAD checkout (branch is the literal `HEAD`) or a
  container path with no `crewship_N` in it still guesses badly. Set
  `CLAIM_CLONE` / `CLAIM_BRANCH` to state it instead:

  ```bash
  CLAIM_CLONE=crewship_3 CLAIM_BRANCH=fix/aux-status scripts/claim-issue.sh 1488
  ```

- **You claimed only part of the issue.** Say so in the claim comment.
  A claim that names its scope ("items 1–2, not the Keeper panel") lets
  another session take the rest instead of the whole thing stalling.

The discipline that costs nothing and saves the most: **grep the issue
tracker before starting, not after the merge conflict.**

## License and contributor terms

The project ships under [Apache License 2.0](LICENSE). Contributions are
accepted under the same terms — by opening a PR you agree that:

- Your contribution is your own original work, or you have explicit
  permission to submit it under Apache-2.0.
- You grant the project's users a perpetual, worldwide, royalty-free
  license to use, modify, and redistribute the contribution, including
  the patent grant in section 3 of the license.
- You retain copyright on your contribution; the license is what
  governs use.

We do not currently require a CLA or DCO sign-off. If that changes,
we will say so here and in the PR template.
