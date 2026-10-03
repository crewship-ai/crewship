# Crewship documentation — the map

Start with the question you are trying to answer. User guides explain how to
use Crewship; specifications describe implementation contracts; design records
explain the reasoning and acceptance limits. A proposal, a local test result
and a released feature are different kinds of evidence.

## Choose a reading path

| Your task | Start here | Then read |
| --- | --- | --- |
| Use Crewship for the first time | [Installation](guides/install.mdx), [first crew](guides/first-crew.mdx) | [Writing tasks](guides/writing-good-tasks.mdx), [orchestration](guides/orchestration.mdx) |
| Change the product | [Contributor workflow](../CONTRIBUTING.md), [repository layout](development/repository-layout.md) | The topic below, then its implementation and tests |
| Understand why something works this way | The current specification below | Its linked design record and [architecture decisions](decisions/README.md) |
| Operate or release it | [Troubleshooting](guides/troubleshooting.mdx), [release entrypoint](../RELEASING.md) | [Runbooks](runbooks/README.md) |
| Record internal research or a handoff | [Private context rules](development/private-context.md) | The private repository's topic index; never make it a public build dependency |

## Find a product topic

Read the user guide for the workflow, the contract for its boundaries, and the
design for rationale. The design index preserves each document's dated context;
linking it here does not promote a proposal to shipped behaviour.

| Topic | User workflow | Contract or implementation reference | Design context |
| --- | --- | --- | --- |
| Work, routines and scheduling | [Orchestration](guides/orchestration.mdx), [scheduling](guides/scheduling.mdx) | [Restricted workflows](specs/restricted-workflows.md), [issue preflight](specs/private-issue-preflight.md) | [Work and dispatch](prd/README.md#work-and-dispatch) |
| Runtime, access and credentials | [Credentials](guides/credentials.mdx), [devcontainers](guides/devcontainers.mdx) | [Restricted context](specs/restricted-context.md), [member policy](specs/member-resource-policy.md), [credential vault](specs/credentials-vault.md) | [Runtime and authority](prd/README.md#runtime-and-authority) |
| Pages, files and applications | [Pages](guides/pages.mdx), [app operations](guides/pages-apps-operations.mdx) | [Pages](specs/pages.md), [Pages Apps](specs/pages-apps.md), [project files](specs/project-files.md) | [Pages and authoring](prd/README.md#pages-and-authoring) |
| Chat, inbox and collaboration | [Chat sessions](guides/chat-sessions.mdx), [inbox](guides/inbox.mdx) | [UI/UX contract](ux/README.md), [inbox implementation](ux/inbox-client-overview.md) | [Chat and collaboration](prd/README.md#chat-and-collaboration) |
| Memory and agent guidance | [Agent memory](guides/agent-memory.mdx), [agent brief](guides/agent-brief.mdx) | [Restricted context](specs/restricted-context.md) for scoped memory access | [Memory and guidance](prd/README.md#memory-and-guidance) |
| Storage, quotas and recovery | [Backup](guides/backup.mdx), [service quotas](guides/service-disk-quotas.mdx) | [Quota backup](specs/quota-service-backup.md), [container cleanup](specs/container-cleanup.md) | [Storage and recovery](prd/README.md#storage-and-recovery) |
| Shared UI and verification | [Demo walkthrough](guides/chat-demo-walkthrough.mdx) | [UI/UX contract](ux/README.md), [API response shapes](specs/response-shape-contract.md) | [Product conventions and verification](prd/README.md#product-conventions-and-verification) |

## Documentation categories

These are storage locations, not competing sources of truth. Each category has
its own index. The topic table above connects categories without moving files
that source code and tests already reference.

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

## Write a document with context

1. **It describes a current implementation contract** → `docs/specs/`.
   Link implementation/tests and state acceptance limits;
   `docs-inventory -strict` checks supported executable references.
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

Give a new or substantially revised authored document a short opening that
answers the following. Apply this to current documents, not immutable imported
records or generated references.

- **Purpose and audience:** what question it answers and who should use it.
- **Status and evidence:** proposal, accepted decision, implementation reference
  or historical record; include a date and issue/PR or source revision. State
  outstanding acceptance limits. A timestamp alone is not verification.
- **Scope:** what the document covers and which related contract owns the rest.
- **Related reading:** link to the current contract, relevant decision/design,
  implementation or tests, and a replacement when superseded.

Add it to the owning category index and, for a new product area, the topic map
above. Keep one authoritative explanation and link to it from other documents.
Update the contract with behaviour changes; preserve the original conclusions
of dated designs and add a pointer when a newer record supersedes them.

Documents are not moved to `docs/archive/` for being old — only for being
superseded with an identifiable replacement. Generated artifacts (e.g. the
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

## What the checks establish

`go run ./scripts/docs-surface-check` checks the published MDX surface, its
navigation and links. `go run ./scripts/docs-inventory -strict` checks supported
API/CLI references against code. Neither proves every prose claim or checks
all repository Markdown links. When changing these indexes, verify their local
links and anchors too. Generated API/CLI documentation is maintained through
its generators; `docs/docs.json` owns website navigation, while this README
owns navigation for contributors reading the repository.
