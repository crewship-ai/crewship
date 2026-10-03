# Repository layout

Start with [CONTRIBUTING.md](../../CONTRIBUTING.md) to run and change the
product. Use this map to choose where code belongs, the
[documentation map](../README.md) for written guidance, and the
[script catalog](../../scripts/README.md) for maintenance commands.

## Product code

| Location | Responsibility | Put new work here when… |
| --- | --- | --- |
| `cmd/crewship/` | Server/CLI entrypoint and Cobra commands | Adding command parsing, output or wiring; keep reusable domain logic in `internal/` |
| `cmd/crewship-sidecar/` | Credential-injecting proxy entrypoint | Changing sidecar startup/wiring; proxy logic belongs in `internal/sidecar/` |
| `cmd/gen-openapi/`, `cmd/gen-journal-registry/` | API schema and journal registry generators | Changing extraction or generated contracts |
| `internal/` | Go implementation, organized by domain | Implementing API handlers, orchestration, storage, policy or other backend behaviour; use the architecture map in [AGENTS.md](../../AGENTS.md#architecture-map-internal) |
| `app/` | Next.js routes, layouts and route composition | Adding a UI route; the static export does not support Next.js API routes |
| `components/features/` | Feature UI and feature-local helpers | Building a screen or interaction owned by a product feature |
| Other `components/` directories | Shared primitives/layout, administration, branding, icons and existing UI areas | Reusing an established component family; new feature UI normally belongs in `components/features/` |
| `hooks/`, `lib/`, `stores/` | Shared hooks, frontend logic/types and client state | Functionality is shared; keep feature-specific helpers beside their feature |
| `config/`, `schemas/` | Embedded defaults and shared resource schemas | Changing data consumed by the application, rather than tool configuration |
| `prisma/` | TypeScript schema/type generation | Updating the frontend type model; SQLite migrations belong in `internal/database/migrations/` |
| `public/` | Browser-served assets | The file must be delivered to users; new internal mockups and audit evidence do not belong here |
| `web/` | Go embedding boundary for the frontend export | Changing how the built UI is packaged or served |
| `ee/` | Reserved enterprise boundary | Follow its [separate policy](../../ee/README.md); do not infer a released feature from the directory |

Keep tests near the code they protect: Go `*_test.go`, frontend `*.test.ts(x)`
and feature `__tests__/` directories. Avoid creating a general `utils/` or
`misc/` directory when an existing domain owns the behaviour. Moving a domain
boundary is an architectural change, not a cosmetic cleanup.

## Tests, examples and developer tools

| Location | Responsibility |
| --- | --- |
| `e2e/` | Browser acceptance tests, their fixtures and local support servers; [suite selection](../../e2e/README.md) |
| Root `__tests__/` | Existing cross-cutting frontend tests; new feature tests should stay with their feature |
| `testdata/` and package-local `testdata/` | Reproducible test inputs, not live data or run output |
| `tests/` | Existing standalone/manual checks; inspect each script's target and side effects before use, and prefer the established test suites for new coverage |
| `examples/` | Public usage examples that contributors and users can reproduce |
| `scripts/` | Build, verification, release and operational entrypoints; [catalog](../../scripts/README.md) |
| `tools/` | Standalone developer binaries, Pages build worker and the isolated River evaluation module; dependencies/requirements stay with the tool |

`tools/spike-river/` is a retained reproduction harness, not a runtime dependency
of Crewship. The tool's own README explains its separate module and evidence.
The standalone tests under `tests/` remain public reproducible checks. Dated
instance execution reports belong in private context; absence from a CI job
alone is not a reason to delete an executable test.

## Delivery and documentation

| Location | Responsibility |
| --- | --- |
| `.github/` | Public workflows and reusable GitHub Actions |
| `Dockerfile`, `docker/` | Container builds, compose definitions and entrypoints |
| `packaging/`, `.goreleaser.yml` | OS packages, service integration and release artifacts |
| `docs/` | Public user documentation, contracts, decisions and contributor guidance; [map](../README.md) |
| Root Markdown files | Project entrypoints and policies: README, contribution, release, security, governance and licensing |
| Local `CLAUDE.md`, `CODEX.md`, `GEMINI.md`, `.claude/`, `.codex/`, `.cursor/`, `.github/copilot-instructions.md`, `internal-docs/` | Ignored assistant instructions, workstation configuration and optional private-context aliases; shared public contributor rules live in `AGENTS.md`; never product source or published build inputs |

Internal plans, handoffs, private experiments and instance audit evidence live
in [optional private context](private-context.md). Public builds and tests must
remain usable without it.

## Why configuration files stay at the root

The root is also the Go module, frontend package and tool invocation directory.
Configuration files are discoverable entrypoints, not orphaned source code.

| Files | Consumer |
| --- | --- |
| `go.mod`, `go.sum` | Go module graph and pinned toolchain |
| `package.json`, `pnpm-lock.yaml`, `pnpm-workspace.yaml`, `.npmrc` | Frontend commands, dependency resolution and pnpm configuration |
| `next.config.ts`, `tsconfig*.json`, `postcss.config.mjs`, `components.json` | Frontend build, type checking and UI tooling |
| `eslint.config.mjs`, `.golangci.yml` | Code checks |
| `vitest.config.ts`, `vitest.setup.ts`, `playwright*.ts`, `.gremlins.yaml` | Unit, browser and mutation-test configuration |
| `.gitleaks.toml`, `.gitleaksignore` | Secret scanning rules and reviewed historical exceptions |
| `sentry.*.config.ts` | Error-reporting SDK integration |
| `.coderabbit.yaml`, `.mailmap` | Review configuration and contributor identity normalization |
| `.gitignore`, `.dockerignore`, `.env.example` | Local/build exclusions and documented environment settings |
| `Makefile`, `dev.sh`, `dev-server.mjs` | Build and development entrypoints |

Keep these paths stable unless changing every reader and validating the actual
tool invocation. `.gitignore` is not a confidentiality boundary: tracked files
remain tracked, and a forced add can include ignored files. Never place real
credentials in either public or private documentation.

Generated exports, coverage, browser reports, local databases and dependency
directories are working output. Keep them ignored; do not move them into
documentation. Preserve the tracked `web/out/.placeholder.html` even after a
frontend build; see [the embed contract](web-out-embed.md).
