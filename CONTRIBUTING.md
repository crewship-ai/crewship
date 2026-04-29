# Contributing to Crewship

Thanks for considering a contribution. This file is the short-form
workflow; the substantive rules and anti-patterns live in
[CLAUDE.md](CLAUDE.md) — please skim it before opening a PR.

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

The full set with rationale is in [CLAUDE.md](CLAUDE.md). Highlights:

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

## Pull requests

- Keep PR titles under 70 characters; put the why in the body.
- Reference the issue in the PR body (`Fixes #123`, `Refs #123`).
- Update or add tests when behaviour changes.
- Update `CLAUDE.md` (and this file) when you change something a
  future contributor would otherwise have to re-discover.

## Security issues

Please don't open public issues for security problems. See
[SECURITY.md](SECURITY.md).

## License

By submitting a contribution you agree to license it under
[Apache-2.0](LICENSE), the project's primary license. If your change
touches the `ee/` directory it is governed by the enterprise license
in that subtree.
