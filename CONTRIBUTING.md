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

A release names the claim it ends (same clone + branch). A release naming
neither ends every open claim on that issue.

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
  checkout path (`crewship_3`) and the branch from `git rev-parse`. In a
  detached-HEAD worktree that branch is the literal `HEAD`, and in a
  container the path may carry no `crewship_N` at all — either way every
  such session claims under the same name and the gate stops telling them
  apart. Set `CLAIM_CLONE` / `CLAIM_BRANCH` to state it instead:

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
