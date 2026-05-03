# Changelog

All notable changes to Crewship are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Pre-1.0 releases may introduce breaking changes in minor versions
(`0.x.0`); patch releases (`0.x.y`) are backwards-compatible fixes.

## [Unreleased]

## [0.1.0-beta.1] — 2026-05-03

First public beta. Self-hosted runtime for AI coding agents — every crew
gets its own Linux container, with cost budgets, approval gates, audit
logs, and encrypted credential vault out of the box.

### Added

- Self-hosted runtime: single Go binary with embedded Next.js UI, embedded
  SQLite DB, and a sidecar proxy for credential injection.
- Crew Journal — append-only event stream as canonical source of truth
  for every observable action; FTS5 search; SSE streaming to the
  `/journal` UI.
- Paymaster — hierarchical LLM cost budgets (workspace → crew → mission →
  agent), per-call ledger written before the LLM request leaves the box.
- Lookout — guardrails: prompt-injection detection, JSON-schema tool-arg
  validation, output parsing, secrets redaction.
- Harbormaster — human-in-the-loop approval queue with sync and async
  modes, configurable timeouts, full decision history.
- Cartographer — checkpoint/fork/restore over journal cursor; non-
  destructive restore returns divergence warnings instead of mutating.
- Quartermaster — eval suite with trajectory replay, regression detection,
  and an LLM-as-judge that uses rubric-shuffle anti-bias.
- Hooks framework — 15 lifecycle event types with shell, HTTP, and
  subagent handlers; `allowedShell=true` required at register time.
- Backup — AGE-encrypted, portable `.tar.zst` bundles at workspace and
  crew scope; retention rotation; advisory locking; forward-compatible
  manifest.
- Keeper — credential gatekeeping with AES-256-GCM versioned encryption
  and an Ollama-backed LLM evaluating per-request access.
- Multi-runtime container support — auto-detection of Docker, Podman,
  Colima, OrbStack, Rancher, nerdctl. Apple Containers on macOS Tahoe+.
- CLI adapters — Claude Code, Codex CLI, Gemini CLI, OpenCode, Cursor
  CLI, Factory Droid, all wired into the orchestrator dispatch table.
- Crew templates — Engineering, Quality, DevOps, and Research crews seed
  ready out of the box; `crewship template apply <slug>` to deploy.
- Issue tracker — Linear-style with labels, projects, sub-issues, and
  bulk operations; `crewship issue …` CLI.
- Multi-workspace support; OWNER/ADMIN/MANAGER/MEMBER/VIEWER server-side
  RBAC enforcement (UI for tier assignment ships in v0.2).
- OpenTelemetry GenAI spans with W3C trace-context propagation; OTLP HTTP
  exporter; every journal entry carries `trace_id`/`span_id`.
- Devcontainer provisioning with mise-managed runtimes, shared cache
  images, and 24-hour registry-digest checks.
- Per-IP rate limiting (10 req/min on auth endpoints, 120 req/min on the
  general API), security headers, single-use OAuth state with 15 min
  expiry.
- Goreleaser pipeline: cross-compiled binaries (Mac amd64+arm64, Linux
  amd64+arm64, Windows amd64), keyless cosign signatures, SPDX +
  CycloneDX SBOMs, Homebrew tap auto-publish.

### v0.2 roadmap

The following ship as packages but are not yet auto-wired into the
runtime in v0.1; they activate via manual API calls today and become
default behaviour in v0.2:

- Episodic memory — vector recall over the journal (selective embedding,
  SQLite BLOB cosine).
- Consolidate — daily Consolidator that extracts learned rules into
  crew memory + Compactor that rolls up low-signal old entries.

The following are planned for v0.2 but not in v0.1 at all:

- PostgreSQL primary database (SQLite is the only supported backend in
  v0.1).
- Kubernetes container provider.
- Skills marketplace (local skill imports work today).
- Workspace-scope memory tier (3-tier today: agent, crew, session).
- Stripe-backed billing tiers / edition gating (v0.1 ships fully
  Apache-2.0 with no edition gating).
- UI for assigning ADMIN/MANAGER/VIEWER workspace roles (server-side
  enforcement is already wired).
- Crew-to-crew handoff with critique exchange.

### Notes

- This is the first tagged release. Public APIs and data models may
  still change in `0.x` minor versions before `1.0`. Pin a commit SHA or
  a specific `v0.x.y` tag if you ship to production.
- The `release` branch tracks deployable state (a 5-minute systemd timer
  on the dogfood prod VM polls it). Push `main:release` to deploy.

[Unreleased]: https://github.com/crewship-ai/crewship/compare/v0.1.0-beta.1...HEAD
[0.1.0-beta.1]: https://github.com/crewship-ai/crewship/releases/tag/v0.1.0-beta.1
