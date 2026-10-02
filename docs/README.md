# Crewship documentation — the map

This file answers one question: **where does a document live, and what is it
allowed to claim.** Every category below has an index of its own; from the
entry instructions ([AGENTS.md](../AGENTS.md), [CONTRIBUTING.md](../CONTRIBUTING.md))
any category is one click from here, its contents one more.

| Category | Directory | What belongs there | Status language allowed |
| --- | --- | --- | --- |
| Contracts and implementation references | [`docs/specs/`](specs/README.md) | Current contracts plus explicitly labelled historical sections; `docs-inventory -strict` checks supported executable references, not every behaviour claim | present tense for verified behaviour; dated status for historical design |
| Architectural decisions | [`docs/decisions/`](decisions/README.md) | Accepted/rejected decisions with their measured evidence and consequences | decided + date |
| Operational runbooks | [`docs/runbooks/`](runbooks/README.md) | Repeatable release and operations procedures | imperative procedure |
| Contributor deep-dives | [`docs/development/`](development/README.md) | Long-form explanations moved out of CONTRIBUTING: embed mechanics, toolchain pinning, review protocol, claim process | explanation |
| UI/UX contracts | [`docs/ux/`](ux/README.md) | UI contracts and public implementation notes | contract + dated research |
| Public implementation design references | [`docs/prd/`](prd/README.md) | Retained design and acceptance context used by public source, tests and contributor documentation | "proposes", "draft" |
| Internal working context | Separate private repository; [access and rules](development/private-context.md) | New internal analyses, business plans, session handoffs and experiments | dated proposal or observation |
| History | [`docs/archive/`](archive/README.md) | Superseded documents preserved for the record; editing them to match the present is falsifying the record | past tense, superseded-by link |
| User docs website | `docs/*.mdx`, `docs/docs.json` | Mintlify-published user documentation (guides, API reference, CLI) — a *published surface*, distinct from the repository navigation this file describes | user-facing |

## Where does a new document go?

1. **It describes behaviour the code implements today, and a gate can check
   it** → `docs/specs/` (and expect `docs-inventory -strict` to hold it to
   the code — that is the point of the directory).
2. **It records a decision and its evidence** → `docs/decisions/`, as an ADR
   with status and date.
3. **It is a repeatable operational procedure** → `docs/runbooks/`.
4. **It is internal research, a future plan, a session handoff or an experiment** →
   the separate private context repository, with status, date and product
   revision. Public proposals intended for community review may still go in
   `docs/prd/`; state their status and public review context explicitly.
   Do not make private context a dependency of public builds or tests.
5. **It is the history of something superseded** → `docs/archive/`, with a
   link back to what replaced it.

Documents are not moved to `docs/archive/` for being old — only for being
superseded with a identifiable replacement. Generated artifacts (e.g. the
docs-inventory reports written by `make docs-inventory` into
`docs/prd/reports/`) are gitignored build outputs, not documents; do not
commit them.

## Entry points

- [Repository layout](development/repository-layout.md) — where code, tests and configuration belong
- [Script catalog](../scripts/README.md) — build, verification and operational commands with their side effects
- [AGENTS.md](../AGENTS.md) — the concise agent/contributor entrypoint
- [CODEX.md](../CODEX.md) — instance map and Codex-specific operating notes
- [CONTRIBUTING.md](../CONTRIBUTING.md) — the contribution workflow (short form; deep-dives in `docs/development/`)
- [README.md](../README.md) — the product README
- [RELEASING.md](../RELEASING.md) — release cutting; the machinery detail is the [CI/CD runbook](runbooks/ci-cd-implementation-2026-09-11.md)
