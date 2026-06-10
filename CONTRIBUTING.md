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

## Local development

```bash
pnpm install
cp .env.example .env.local        # set NEXTAUTH_SECRET + ENCRYPTION_KEY
./dev.sh start                    # backend :8080 + frontend :3001
```

Other `./dev.sh` subcommands: `stop`, `restart`, `status`, `seed`,
`nuke`, `logs`. Never start the services manually.

## Verify any change

Run these locally before pushing — CI will run them too:

```bash
go test ./... -count=1 && go vet ./...      # Go: must pass
pnpm lint && pnpm build                      # Frontend: must pass for UI changes
```

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
