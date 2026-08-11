# AGENTS.md

Concise entrypoint for AI agents and new contributors working in this repo.
For deep specs see [`.claude/context/prd/`](.claude/context/prd/); for contributor
process see [`CONTRIBUTING.md`](CONTRIBUTING.md).

## What Crewship is

Open-source AI agent orchestration platform. A single Go binary embeds a Next.js
static export; agents run inside Docker containers behind a credential-injecting
sidecar proxy. Orchestration is **queue + trigger** based (cron schedulers,
webhooks, an autonomous assignment queue, durable pipeline waitpoints), not
infinite loops.

## Before you touch an issue: claim it

Ten sessions work this repo at once and all push as the same GitHub account, so
the assignee field cannot distinguish "another session has this" from "that's
me". #1481 duplicated #1471's fix and both sides lost the work.

```bash
scripts/claim-issue.sh <issue>          # checks for a live claim, then claims
scripts/claim-issue.sh <issue> --check  # read-only: who holds it, which PRs touch it
scripts/claim-issue.sh --list           # every open claim in the repo
scripts/claim-issue.sh <issue> --release "why you stopped"
```

Claim **before the first commit**, release in the same thread when you stop —
including when you failed, so the next session starts from your evidence. It
exits 3 without posting if someone else holds it. Full convention and failure
modes: [CONTRIBUTING.md → Claiming an issue](CONTRIBUTING.md#claiming-an-issue-before-you-work-it).

## Build, run, test

```bash
./dev.sh start          # dev: Go :8080 + Next.js :3001 (hot reload). `make up` is an alias.
make build              # prod: pnpm build → static export → embedded → ./crewship
make build:go           # Go binary only (+ sidecar)

# Verification loop — run until green before considering work done:
go test ./... -count=1  # all Go tests (Docker tests self-skip if daemon absent)
go vet ./...            # static analysis
pnpm lint               # ESLint (UI/frontend changes)
pnpm build              # confirm static export still builds (cross-cutting changes)
```

Multi-instance: a clone named `crewship_N` auto-offsets all ports/data/sockets,
so parallel agents/worktrees run conflict-free (see `dev.sh`).

**`web/out` embed:** `web/embed.go` embeds the Next.js export, so `web/out/`
must exist for *any* Go build to compile. It is tracked-by-one-file
(`web/out/.placeholder.html`) so `go build ./...` works in a fresh worktree
with zero setup — **do not `mkdir web/out` or hand-roll an `index.html` stub**,
and do not let a `git add -A` delete the placeholder after a build. Such a
binary has no UI (every UI route → `503` + an explanatory page); run
`make build` for a real one. Details: [`CONTRIBUTING.md`](CONTRIBUTING.md#the-webout-embed).

## Before you merge: confirm the review happened

Wait ~2–5 min after `gh pr create` for CodeRabbit, and never merge before it
posts — merging first kills the run ("Review failed — PR is closed") and the
findings are gone. **A green CodeRabbit check does not mean it reviewed**: when
rate-limited it posts a notice instead and the status still reads `pass`
(description `Review rate limited`, not `Review completed`). Check the posted
bodies, not the check:

```bash
scripts/review-status.sh              # reviewed / throttled / failed / pending / absent per open PR
scripts/review-status.sh 1568 --checks   # one PR, plus its skipped-but-green checks
scripts/review-status.sh --retrigger --dry-run   # queued re-review, spaced by the limit
```

Exit 3 = at least one PR is not reviewed. Same trap, other producers: a check
that concluded `skipped` or `neutral` is green without having run; CodeQL
findings can live only in a run's annotations; and a re-trigger fired too early
answers "✅ Action performed — Review finished." while submitting no review at
all. Details and the re-trigger policy: [CONTRIBUTING.md → Wait for
CodeRabbit](CONTRIBUTING.md#wait-for-coderabbit--and-check-that-it-actually-reviewed).

## Architecture map (`internal/`)

```text
api/            HTTP API (stdlib http.ServeMux, "METHOD /path" patterns)
orchestrator/   agent execution engine — container exec + streaming + env build
sidecar/        credential-injecting forward proxy (127.0.0.1:9119)
keeper/         credential gating + gatekeeper + Phase-2 self-improvement routines
scheduler/      robfig/cron engine — fires scheduled agent runs
pipeline/       pipeline runner: schedules, webhooks, durable waitpoints (HITL)
harbormaster/   approval gate for dangerous actions (approvals_queue)
policy/         per-crew autonomy matrix (strict|guided|trusted|full × warn|block)
paymaster/      cost ledger + budgets + per-model pricing
journal/        durable run/event journal (the shared audit + activity spine)
episodic/ consolidate/ memory/   agent memory: recall before runs, consolidate after
manifest/       declarative `crewship apply` (plan/validate, ~20 resource kinds)
skills/         SKILL.md parser + importer + bundled skills
auth/           JWE (NextAuth v5 compatible) validation
database/       SQLite via database/sql (modernc.org/sqlite, pure Go, no CGO)
provider/       infra interfaces: Container (Docker/Apple), Storage, State
scrubber/ httpsafe/ safepath/   credential scrubbing, SSRF + path-traversal guards
ws/             WebSocket hub (pub/sub channels)
config/         defaults → YAML → env vars (env wins)
```

Frontend: `app/` (Next.js App Router), `components/` (`ui/` = shadcn), `hooks/`,
`lib/` (Zod schemas, CASL permissions).

## Conventions

- **Go**: no ORM — raw `database/sql` with `QueryRowContext`/`ExecContext`. Provider
  interfaces for all infra. RFC 7807 Problem Details for errors. Imports stdlib →
  external → internal. Table-driven tests; `t.Skip()` for optional deps (Docker).
- **DB**: `snake_case`, plural tables; CUID ids (`internal/api/cuid.go`); soft delete
  via `deleted_at`; timestamps TEXT (RFC 3339).
- **Frontend**: Next.js 16 App Router (static export), Tailwind 4 + Radix/shadcn,
  Zod, CASL (`lib/permissions/`). ES modules only. `@/*` path alias.
- **Commits**: conventional (`feat:`, `fix:`, `refactor:`, `docs:`, `test:`,
  `chore:`). Branch from `main`.

## Migrations

**One `.sql` file per migration** in `internal/database/migrations/`. The
directory *is* the registry — there is no central list to edit, which is the
point: two people adding a migration add two files and cannot conflict.

```bash
date -u +%Y%m%d%H%M%S    # the version stamp — filenames are
                         # <YYYYMMDDHHMMSS>_<snake_case_name>.sql
```

Versions must be strictly ascending, so **append, never insert** a stamp older
than one already committed. Sequential numbers are gone: `v1..v169` were
allocated that way and are frozen in the `legacyMigrations` slice in
`migrate.go` — **nothing at or below v169 may move or change**, they are
applied in databases nobody controls (`migrate_version_scheme.go` enforces
this at startup).

Migrations that genuinely need Go (schema discovery at apply time, SQLite
table rebuilds) still go in that slice with a timestamp version. Everything
expressible as plain SQL belongs in a file. `migrations/post_deploy/` runs
after the server is serving instead of blocking boot — read its README first,
it is a contract about what the running code must tolerate, not a free win.

Runs at startup, tracked in `_migrations`. Check your work with
`go run ./scripts/lint-migrations`, which fails if an already-shipped
migration's SQL changed under it.

**Prisma is for TypeScript types only — never run `prisma migrate`.**

Full detail: [`internal/database/migrations/README.md`](internal/database/migrations/README.md)
and [`docs/guides/migrations.mdx`](docs/guides/migrations.mdx).

## NEVER DO

- Never skip the verification loop (`go test` + `go vet`) — or ship without tests.
- Never add API routes under `app/` — static export silently drops them.
- Never use `"sqlite3"` as the driver name — `modernc.org/sqlite` registers `"sqlite"`.
- Never use `npm`/`yarn` — `pnpm` only.
- Never change the GCM byte layout in `internal/encryption/` — breaks all stored creds.
- Never change sidecar UID (1002) or agent UID (1001) — it's a security boundary.
- Never discard WIP — `git stash` before switching branches, never `git checkout .`.
- Never start an issue without checking it for a live claim — the assignee is not
  a lock when every session is the same account.
- Never commit secrets (`.env.local`, real keys).

## When unsure

Check `.claude/context/prd/` before assuming design or requirements. Keep these docs
current after significant changes.
