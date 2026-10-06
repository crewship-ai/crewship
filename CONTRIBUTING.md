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

The long-form explanations that used to live in this file moved to
[docs/development/](docs/development/README.md): the [`web/out` embed](docs/development/web-out-embed.md),
[build identity in a worktree](docs/development/build-identity-in-worktrees.md),
[Go toolchain pinning](docs/development/go-toolchain-pinning.md),
[the Sentry vitest alias](docs/development/sentry-vitest-alias.md) and
[dependency drift](docs/development/dependency-drift.md).

## CI and release verification

Use `bash scripts/verify.sh quick` before pushing (`go` and `full` add the
full language suites). CI, Security and CodeQL expose stable aggregate
results; a planned skip is checked against the change plan rather than
accepted merely because GitHub renders it green. The real image build now
runs as part of CI Result, including for frontend changes.

The publication flow, exact-SHA gates, artifact identities, local commands
and runtime credential requirements are documented in
[the CI/CD implementation runbook](docs/runbooks/ci-cd-implementation-2026-09-11.md).

## Verify any change

Run the relevant checks locally before pushing. CI routes checks by change type
(see Pull requests below); unknown paths and main pushes run the full suite:

```bash
go test ./... -count=1 -timeout 40m && go vet ./... # Go: must pass
pnpm lint && pnpm build                      # Frontend: must pass for UI changes
```

The full Go suite can exceed the default ten-minute timeout on shared machines.
The explicit timeout is a ceiling, not a reason to ignore a failed or hanging
test. For preliminary checks and script side effects, see the
[script catalog](scripts/README.md); for code placement, see the
[repository layout](docs/development/repository-layout.md).

The `cmd/crewship` suite scrubs every `CREWSHIP_*` variable from its own
environment before running, so a shell that exports `CREWSHIP_PROFILE` or
`CREWSHIP_SERVER` for a dev instance can no longer redirect the tests at
your live server. Tests that need one of those vars set it themselves.
After that scrub, the harness sets `CREWSHIP_NO_SLUG_CACHE=1` so CLI child
processes bypass the shared workspace slug cache: an ephemeral test server
can reuse another fixture's URL and slug with a different workspace ID.
Disk-cache behavior is tested explicitly in `internal/cli/slugcache_test.go`.

For UI changes, also exercise the affected feature in a browser before
declaring it done. Type checking and tests verify code correctness, not
feature correctness.

## House rules (the short list)

- **`pnpm` only** — never `npm` or `yarn`.
- **Migrations are one `.sql` file each** in `internal/database/migrations/` —
  the directory *is* the registry, versions strictly ascending. Go-side
  migrations only for cases that genuinely need schema discovery or table
  rebuilds. **Never run `prisma migrate`** — Prisma is TypeScript types only.
  Full detail: [`internal/database/migrations/README.md`](internal/database/migrations/README.md).
- **No new API routes in `app/`** — the static export drops them in
  prod. All API routes go in `internal/api/`.
- **Driver name is `"sqlite"`** (not `"sqlite3"`).
- **Sidecar UID 1002, agent UID 1001** — security boundary; do not
  change.
- **No `Co-Authored-By` lines in commits.**
- **Never amend after a pre-commit hook failure** — make a new commit.
- **A detached goroutine in `internal/api` registers with
  `beginBackgroundWork`** (`internal/api/background.go`) — work that outlives
  its request also outlives the test that drove it. Background and the
  `storageDir(t)` rule: [house-rules background](docs/development/house-rules-background.md).
- **`Commands()` on a shared Cobra command is a *write*.** Never execute a
  shared command from a parallel test; build a local command tree. Why:
  [house-rules background](docs/development/house-rules-background.md#commands-on-a-shared-cobra-command-is-a-write).
- **`go test -race ./internal/api/` needs `-timeout 40m`** — the package
  takes ~23m under the race detector and the default 10m kills it mid-test in
  a way that looks exactly like a failure. Details:
  [house-rules background](docs/development/house-rules-background.md#go-test--race-internalapi-needs--timeout).
- **Don't hash at production strength in a test.** Never write a literal
  bcrypt cost at a call site — guard tests reject it. Details:
  [house-rules background](docs/development/house-rules-background.md#dont-hash-at-production-strength-in-a-test).

## Frontend data fetching

New or migrated client data hooks use **@tanstack/react-query** (wired in
`components/providers.tsx`) with `apiFetch` from `lib/api-fetch.ts`, not
hand-rolled `fetch` + `useState`; reference implementations:
`hooks/use-dashboard-data.ts` and `hooks/use-inbox.ts`.

The full conventions — query keys, transport, freshness via realtime events,
error mapping, and test setup — live in
[docs/development/frontend-data-fetching.md](docs/development/frontend-data-fetching.md).

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

CI classifies the actual Git diff. Code changes run the frontend, embedded
server, browser and Docker checks; frontend-only changes skip the Go matrix.
Documentation-only PRs retain the workflow/invariant and security verdicts.
Unknown paths trigger the full suite, as do main pushes.

The root Dockerfile builds and boots on every code PR through the reusable
`pr-image-build.yml` job. It tests `linux/amd64` without pushing; publication
checks both amd64 and arm64. `scripts/pr-image-build-paths.sh` verifies that
CI calls the image workflow and its final verdict requires the image job.

## Changelog entries

`RELEASING.md` cuts release notes from `CHANGELOG.md`'s `## [Unreleased]`
section. That makes a missing entry not untidiness but a release note that
does not exist — and a change nobody is told about.

**A PR that touches `internal/api/`, `internal/orchestrator/`,
`internal/harbormaster/`, `cmd/crewship/`, `app/`, `components/`, `lib/`,
`hooks/` or `stores/` must add an entry under `## [Unreleased]`.** The
**Changelog Guard** workflow enforces it; test files inside those trees don't
count as user-visible, and Dependabot is exempt by actor.

Write the entry the way the file already does — lead with the **user-visible
symptom in bold**, then what was actually wrong and what changed. Mark a
change that can break a working setup with `⚠️ **Behaviour change:**`. If the
change genuinely has no user-visible effect, apply the **`skip-changelog`**
label — when it is true, not when the entry is inconvenient. The guard's
history, its exact comparison, and the label mechanics:
[docs/development/changelog-guard.md](docs/development/changelog-guard.md).

## Review before merge

CodeRabbit was retired on 2026-10-02. Its status and comments are no longer a
merge prerequisite; do not queue or wait for bot reviews.

Review the final diff for behavior, permissions, data lifecycle, error handling
and compatibility. Record material findings and their resolution in the PR,
along with the exact checks run and any untested acceptance boundaries. Label
self-review honestly; it does not constitute an independent review.

Required CI checks must pass. A skipped or neutral check is not evidence that
the scenario ran. For behavioral fixes, include a regression test that exercises
the failure at the affected boundary (CLI/API, restart, Docker or restore).
Historical bot tooling remains available for old PR investigations only; see
[the archived process](docs/development/coderabbit-review-process.md).

## Merge through the queue

After recording the review and validation, enqueue with
`gh pr merge <PR> --auto --squash`. GitHub CLI enables auto-merge while required
checks are pending and adds an eligible PR to a required merge queue. Do not
repeatedly merge main into the branch just to chase other queued merges: the
queue tests its candidate against the current base. Use
`--match-head-commit <SHA>` when submitting a reviewed head to prevent a concurrent push from
changing the candidate.

The queue policy uses one PR per merge group and becomes usable only after
actual PR and merge-group canaries prove that every required context is emitted.
`CI Result`, `Security Result`, `CodeQL Result` and `CI Inventory Guard` (once
required) must execute on `merge_group`; a planned skip must still match the
change plan. Main pushes retain the full exact-SHA verification required by
publication. Queueing is not permission to remove those checks.

A PR or merge group that changes protected CI controls also needs a visible,
exact-SHA exception recorded through the trusted-main inventory guard. A new
head or group SHA needs fresh review; this exception does not bypass required
checks or authorize an administrative merge.

Do not use `--admin` or a direct REST merge to bypass the queue. An emergency
exception requires a recorded reason, responsible operator, reviewed head SHA
and successful required checks for that exact SHA in the PR before merging.

Measure changes with [the read-only PR throughput collector](scripts/ci/pr-throughput.md).
Compare complete PR open-to-merge time, evidenced final substantive push-to-merge
time, workflow queue/execution, failure rate and reruns. A new run after a push
or automatic branch update is not a rerun attempt.

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

Several agent sessions work this repo in parallel — ten at once is normal —
and they all push as the same GitHub account, so **the assignee field cannot
tell "taken by another session" from "that's me"**. It is not a lock. What it
costs when nobody claims: #1481 re-fixed what #1471 had already fixed two
hours earlier, from another session, better.

**Before your first commit on an issue, post a claim comment naming clone +
branch + UTC time. Release it in the same thread when you stop — whether it
shipped or not.**

```bash
scripts/claim-issue.sh 1488                 # checks first, then claims
scripts/claim-issue.sh 1488 --check         # read-only: who holds it?
scripts/claim-issue.sh --list               # every open claim in the repo
scripts/claim-issue.sh 1488 --release "hypothesis unconfirmed, see above"
```

Claiming checks before it posts (exit 3 = held by another session). Failure
modes — stale claims, claims with nobody behind them, partial-scope claims,
identity guessing in fresh worktrees — are documented in
[docs/development/claiming-issues.md](docs/development/claiming-issues.md).
The discipline that costs nothing and saves the most: **grep the issue
tracker before starting, not after the merge conflict.**

## License and contributor terms

Crewship is maintained by **Unify Technology, s.r.o.** (Czech Republic,
Company ID: 17266637). See [GOVERNANCE.md](GOVERNANCE.md) for project stewardship
and [NOTICE](NOTICE) for company identification.

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
