<p align="center">
  <img src="crewship.svg" height="80" alt="Crewship" />
</p>

<h1 align="center">Crewship</h1>

<p align="center">
  <strong>Self-hosted runtime for AI coding agents.</strong><br/>
  Real Linux containers. Your hardware. Your keys. Your data.
</p>

<p align="center">
  <a href="https://github.com/crewship-ai/crewship/actions/workflows/ci.yml"><img src="https://github.com/crewship-ai/crewship/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI" /></a>
  <a href="https://github.com/crewship-ai/crewship/actions/workflows/security.yml"><img src="https://github.com/crewship-ai/crewship/actions/workflows/security.yml/badge.svg?branch=main" alt="Security" /></a>
  <a href="https://github.com/crewship-ai/crewship/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License" /></a>
  <a href="https://golang.org/doc/devel/release.html"><img src="https://img.shields.io/badge/go-1.26-00ADD8.svg?logo=go" alt="Go 1.26" /></a>
</p>

> **Status: v0.1 beta — open beta.** APIs and data models are still
> moving; pin a tag (or commit SHA) if you ship to production. The
> [Beta status & limitations](#beta-status--limitations) section below
> spells out what's ready, what's WIP, and what's not yet wired up. See
> also [CHANGELOG.md](CHANGELOG.md) and [RELEASING.md](RELEASING.md).

---

## What is Crewship?

Crewship turns a [Claude Code](https://claude.com/claude-code) session
into a fleet of agents that share a workspace, talk to each other, and
keep state between runs. Adapter scaffolds for Codex CLI, Gemini CLI,
OpenCode, Cursor CLI, and Factory Droid are in the tree but
**not production-ready in this beta** — see
[Beta status & limitations](#beta-status--limitations).

Each crew gets its own Linux container — a real machine where its
agents can install services, run databases, mount volumes, and build a
working system together. The whole environment — code, data,
conversations, audit trail — runs on your hardware and packs into
portable encrypted backups.

You bring the API keys. Crewship keeps them off disk, off the wire,
and out of agent processes.

## What's in the box

Labels: ✅ **stable** in v0.1 beta · 🟡 **early** (works but contract
may shift) · 🚧 **WIP** (scaffolded, not yet usable end-to-end).

- ✅ **Real Linux containers** — one per crew, isolated network,
  non-root UID, read-only root, cap-drop ALL. Agents can install,
  build, and run anything Linux supports.
- ✅ **Claude Code adapter** — Anthropic's `claude` CLI, both
  Max-subscription and API-key auth paths. The production-tested
  runtime for v0.1 beta.
- 🚧 **Other CLI adapters** — Codex, Gemini, OpenCode, Cursor, Factory
  Droid have adapter scaffolds in `internal/orchestrator/adapter_*.go`,
  but only Claude Code is exercised across the full feature set. Other
  adapters may run but lack the parity testing required for beta sign-off.
- ✅ **Encrypted credential vault** — AES-256-GCM at rest, piped over a
  Unix socket to a sidecar (UID 1002) that injects per-request, never
  to the agent process directly.
- ✅ **Outbound scrubber** — 13+ credential patterns redacted from
  agent stdout before it leaves the container.
- ✅ **Skills as portable playbooks** — author or import a `SKILL.md`,
  attach to one agent or a whole crew. Tested with the Claude Code
  adapter.
- 🟡 **Routines** — JSON DSL for AI-authored workflows. Six step types
  (`agent_run`, `call_pipeline`, `http`, `code`, `wait`, `transform`),
  DAG parallelism via `needs[]`, cron + HMAC-signed webhooks,
  human-in-the-loop waitpoints, immutable version history. The schema
  may evolve in beta — see
  [`docs/guides/routines.mdx`](docs/guides/routines.mdx).
- ✅ **Harbormaster (approvals)** — risky tool calls pause for human
  sign-off; the agent waits. `crewship approvals` CLI + web UI.
- ✅ **Paymaster (cost ledger)** — every LLM call recorded with token
  counts and dollar cost; per-workspace budgets enforced. `crewship cost`
  + `/api/v1/paymaster/*`.
- ✅ **Lookout (input guard)** — argument injection and prompt-injection
  guardrails on LLM inputs.
- ✅ **Keeper (credential gatekeeper)** — optional rule-based gate on
  which credentials a given agent can pull from the sidecar.
- ✅ **Crew Journal** — append-only event stream: every LLM call, tool
  use, prompt, response, decision. Searchable (FTS5), exportable.
- ✅ **Backup & restore** — Age-encrypted bundles capture an entire
  workspace or crew (code, data, conversations, journal) so nothing
  agents create disappears.
- 🟡 **Episodic memory & Consolidate** — packages exist; full
  auto-wiring lands in v0.2.
- ✅ **Single binary** — Next.js static export embedded into the Go
  server. No Node.js at runtime, no separate services to deploy.

## Beta status & limitations

This is an **open beta**. The pieces marked ✅ above have been used by
the maintainer in production-shaped workloads; the pieces marked 🟡 and
🚧 are still being shaped. Specifically for v0.1 beta:

- **Only the Claude Code (Anthropic) adapter is recommended for real
  work.** Codex / Gemini / OpenCode / Cursor / Factory Droid have
  adapter implementations but not yet the integration tests, prompt
  tuning, or scrubber rules to call them production-ready. They will
  load and may run, but expect rough edges — file issues with logs.
- **PostgreSQL is on the v0.2 roadmap.** Beta runs on SQLite via
  `modernc.org/sqlite` (single-binary, WAL mode, no extra services).
- **Telemetry is ON by default in v0.1 beta.** Anonymous crash reports
  flow to the maintainer's Sentry to give a solo team enough signal to
  fix bugs. Disable with `crewship telemetry off` (sticky), or
  redirect to your own Sentry with `CREWSHIP_SENTRY_DSN=...`. Full
  details in [docs/guides/telemetry](docs/guides/telemetry.mdx).
  Reverts to opt-in for v1.0 GA.
- **APIs may break across minor bumps** (v0.1 → v0.2). Patch bumps
  inside a minor (v0.1.0 → v0.1.1) are backwards-compatible.
- **No multi-host clustering.** One Crewship instance manages many
  crews on its own host; the architecture allows a future scheduler
  layer but it is not in beta.

Found a beta-blocker? [Open an issue][issues] — the
`beta-blocker` label gets priority triage.

[issues]: https://github.com/crewship-ai/crewship/issues/new/choose

## Install

Three supported paths — pick whichever fits your machine. Full details
in [docs/guides/install](docs/guides/install.mdx).

```bash
# macOS / Linux — Homebrew
brew install crewship-ai/tap/crewship

# Any Unix — signed installer (fetch the script direct from the repo)
curl -fsSL https://raw.githubusercontent.com/crewship-ai/crewship/main/scripts/install.sh | bash

# Self-hosted — Docker
# `:latest` auto-tracks the most recent stable release. Pin a specific tag
# (e.g. `:v0.1.0-beta.1`) once you've validated it in your environment.
docker pull ghcr.io/crewship-ai/crewship:latest
```

> The short `crewship.ai/install` redirect lands once the project website
> goes live; until then, fetch the script straight from the repo as
> shown above.

Then bring it up:

```bash
crewship start
```

Defaults: HTTP on `:8080`, SQLite at `~/.crewship/crewship.db`.
Override with `CREWSHIP_PORT` / `--db file:/path`. Container runtime
required (Docker, Podman, Colima, OrbStack, or Apple Containers) —
`crewship doctor` autodetects and tells you what's missing.

On first load the web UI walks a 6-step onboarding wizard (workspace
→ crew → agent → credentials → done). Demo data: `crewship seed`.

## Build from source (developers)

```bash
git clone https://github.com/crewship-ai/crewship.git
cd crewship
pnpm install                            # frontend deps (pnpm required)
cp .env.example .env.local              # then fill NEXTAUTH_SECRET + ENCRYPTION_KEY
./dev.sh start                          # SQLite, hot-reload, both ports
```

The dev server splits the Go binary (`:8080`) from Next.js (`:3001`)
for fast iteration. Other `./dev.sh` subcommands: `stop`, `restart`,
`status`, `seed`, `nuke`, `logs`, `logs:go`, `logs:next`.

### Single-binary production build

```bash
make build      # pnpm build → cp -r out web/out → go build -o crewship
./crewship start
```

`make build` is end-to-end. **Don't** run `pnpm build` followed
directly by `go build` — the `cp -r out web/out` step in between is
required, otherwise the embedded UI drifts out of sync with the
Next.js output.

## Stack

| Layer | Technology |
|---|---|
| UI | Next.js 16 (static export), React 19, Tailwind 4, shadcn/ui |
| Auth | NextAuth.js v5 (Auth.js), JWT + refresh tokens |
| Database | SQLite via `modernc.org/sqlite`, Go-side migrations (no Prisma at runtime) |
| Backend | Go 1.26 (`crewship`) — REST + WebSocket, Docker orchestration |
| Agent runtime | Docker containers, Claude Code adapter (plus scaffolds for 5 more — see [Beta status](#beta-status--limitations)) |
| IPC | HTTP-over-Unix-socket on `/tmp/crewship.sock` (X-Internal-Token auth) |

> **Prisma is TypeScript-types only.** All schema changes go through
> `internal/database/migrate.go`. Never run `prisma migrate`.

## How the code is organised

```
app/                   Next.js frontend (static-export)
components/            UI components — shadcn/ui + feature folders
hooks/  lib/  stores/  Frontend hooks, utilities, Zustand stores
docs/                  Mintlify-rendered user docs (docs.crewship.ai)

cmd/crewship/          Main binary (subcommands: start, seed, doctor,
                       skill, crew, routine, paymaster, …)
cmd/crewship-sidecar/  In-container sidecar (credentials, MCP, memory)

internal/api/          HTTP + WebSocket handlers (~50 REST endpoints)
internal/orchestrator/ Agent run loop — dispatch, exec, handoff
internal/orchestrator/adapter_*.go
                       Per-CLI adapters (claude ✅, codex/gemini/
                       cursor/opencode/droid 🚧 scaffolded)
internal/sidecar/      Sidecar coordinator + bridges (keeper, MCP, memory)
internal/database/     SQLite schema + Go migrations
internal/journal/      Append-only event stream (canonical source of truth)
internal/keeper/       Credential gatekeeping (optional Ollama-backed)
internal/harbormaster/ Human-in-the-loop approval queue
internal/scrubber/     Outbound text secret redaction
internal/paymaster/    LLM cost ledger + budget enforcement primitives
internal/lookout/      Argument injection + prompt-injection guardrails
internal/backup/       Age-encrypted bundle export / restore
internal/provider/     Pluggable container / storage / state backends
internal/connectors/   MCP/OAuth integration manifests (🟡 early)
internal/episodic/     Long-term episodic memory (🟡 early, full wiring in v0.2)
internal/crashreport/  Opt-out Sentry crash reporting in v0.1 beta
internal/update/       GitHub-Releases version check + update banner

ee/                    Enterprise add-ons (separate license, empty today)
dev.sh                 Local dev orchestration
scripts/               Release + dev tooling (install.sh, sentry-bootstrap.sh, …)
prisma/                Prisma schema (TypeScript types only — do NOT migrate)
```

## Verify a change

```bash
go test ./... && go vet ./...        # backend
pnpm test && pnpm exec tsc --noEmit  # frontend
```

## Contributing

PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for workflow,
house rules, and commit conventions. Open an issue first to discuss
larger changes.

Security: see [SECURITY.md](SECURITY.md). Do not file public issues
for vulnerabilities.

Community: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — Contributor
Covenant 2.1 applies in every project space.

## Community & links

- **Docs:** [docs.crewship.ai](https://docs.crewship.ai)
- **Discord:** community help + showcase (invite link on
  [crewship.ai](https://crewship.ai))
- **Reddit:** [r/Crewship](https://reddit.com/r/Crewship) for
  discussion + showcases
- **X / Twitter:** [@crewshipai](https://twitter.com/crewshipai)
- **Bluesky:** [@crewship.ai](https://bsky.app/profile/crewship.ai)
- **YouTube:** [@crewshipai](https://youtube.com/@crewshipai)
- **GitHub Discussions:** [crewship-ai/crewship/discussions](https://github.com/crewship-ai/crewship/discussions)

## License

[Apache License 2.0](LICENSE) — free to use, modify, distribute.

Copyright 2025-2026 Unify Technology s.r.o.
