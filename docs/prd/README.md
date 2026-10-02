# PRD — proposals, research and session records

**Purpose.** Existing public design documents and research awaiting
classification. A document here *proposes* or *records*;
it does not claim to describe current behaviour. The docs-inventory reverse
gate deliberately does not scan this tree (a PRD naming a flag that does not
exist yet is the whole point of a PRD). Current contracts live in
[`docs/specs/`](../specs/README.md); decided architecture in
[`docs/decisions/`](../decisions/README.md); superseded history in
[`docs/archive/`](../archive/README.md).

On 2026-09-28 the 23 proposals that lived in `.claude/context/prd/` moved
here: proposals belong to the project, not to an assistant-specific directory.

New internal analyses, handoffs and experiments belong in the
[separate private context repository](../development/private-context.md).
Dated session reports and captured experiment outputs have been archived after
remote hash verification. Historical evidence links point at immutable source
revisions. Public specifications, proposals and their required design context
remain here; a proposal is not a claim that its design has shipped.

**Rule for a new public proposal.** It must be intended for public discussion.
Give the header a status line — `draft` /
`accepted` / `implemented` / `superseded` / `archived` — plus a date, and a
link to the replacement when superseded. Status is derived from evidence
(merged PRs, shipped behaviour), not from the filename. When a proposal
becomes the implemented contract, move it to `docs/specs/` in the same change
that makes the gate scan it.

The table below records the status each document states in its own header
(`—` = no status line; read the document before relying on it). It was
generated during the 2026-09-28 inventory and is refreshed by hand — a
document whose status drifts should be updated when noticed, not silently.

## Release 1.0 spine

| Document | Status stated in header |
| --- | --- |
| [PRD-RELEASE-1-0-QUALITY-AUDIT.md](PRD-RELEASE-1-0-QUALITY-AUDIT.md) | proposed |
| [BACKUP-SINGLE-WRITER-DECISION-2026-10-01.md](BACKUP-SINGLE-WRITER-DECISION-2026-10-01.md) | accepted single-writer decision; implementation/release verification pending |
| [RELEASE-1-0-READINESS-2026-08-10.md](RELEASE-1-0-READINESS-2026-08-10.md) | current (baseline `main` @ 69a8ceb9) |
| [PRD-ISSUES-AND-ROUTINES-2026.md](PRD-ISSUES-AND-ROUTINES-2026.md) | — |
| [CODEX-WORK-ORDER-RELEASE-1-0.md](https://github.com/crewship-ai/crewship/blob/07bfd2360a97dbe1c9f71c03e1d9b5d5d471c993/docs/prd/CODEX-WORK-ORDER-RELEASE-1-0.md) | — (work order) |
| [CODEX-WORK-ORDER-CHAT-1-0.md](https://github.com/crewship-ai/crewship/blob/07bfd2360a97dbe1c9f71c03e1d9b5d5d471c993/docs/prd/CODEX-WORK-ORDER-CHAT-1-0.md) | — (work order) |
| [documentation-contract-testing.md](documentation-contract-testing.md) | proposed |
| [decision-models-archive.md](decision-models-archive.md) | research only; implementation proposal not pursued |

## Durable work, queue and parallelism

| Document | Status stated in header |
| --- | --- |
| [WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md) | decided product & architecture contract; implementation/release verification open |
| [WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md) | proposal to implement, not measurement results |
| [WEBHOOKS-AGENT-PARALLELISM-ANNEX.md](WEBHOOKS-AGENT-PARALLELISM-ANNEX.md) | — |
| [SPIKE-RIVER-SQLITE-1-0.md](SPIKE-RIVER-SQLITE-1-0.md) | executed 2026-09-10, result **reject** — see the [ADR](../decisions/ADR-QUEUE-RIVER-SQLITE-2026-09-10.md) |
| [SPIKE-RUN-ISOLATION-E1.md](SPIKE-RUN-ISOLATION-E1.md) | assignment only, not executed |
| [REVIEW-CLAUDE-DURABLE-WORK-2026-09-11.md](REVIEW-CLAUDE-DURABLE-WORK-2026-09-11.md) | — (review record) |
| [QUEUE-MECHANISM-2026.md](QUEUE-MECHANISM-2026.md) | header says "design draft, no implementation" (2026-05-17) — **stale**: implemented via v93+ (see `internal/api/assignments_*.go` citing it) |
| [ROUTINES-DURABILITY-QUICKWINS.md](ROUTINES-DURABILITY-QUICKWINS.md) | shipped via PR #455 |

## Pages and custom apps

The current contract is [`docs/specs/pages.md`](../specs/pages.md) (panels)
and [pages-apps-v1.md](pages-apps-v1.md) (custom apps v1). History:
[`docs/archive/`](../archive/README.md).

| Document | Status stated in header |
| --- | --- |
| [pages-apps-architecture.md](pages-apps-architecture.md) | proposal to implement (2026-09-08) |
| [pages-apps-v1.md](pages-apps-v1.md) | all four code layers merged under #2472; product acceptance open |
| [pages-apps-counter-review-2026-09-09.md](pages-apps-counter-review-2026-09-09.md) | — (review record) |
| [pages-apps-independent-review-2026-09-09.md](pages-apps-independent-review-2026-09-09.md) | — (review record) |
| [pages-apps-independent-review-followup-2026-09-10.md](pages-apps-independent-review-followup-2026-09-10.md) | branch `feat/pages-apps-project`, 17 commits over 676e16e4 (record) |
| [pages-apps-followup-response-2026-09-10.md](pages-apps-followup-response-2026-09-10.md) | — (review response) |
| [pages-apps-review-audit-2026-09-09.md](pages-apps-review-audit-2026-09-09.md) | review audit for the product owner (2026-09-09) |
| [pages-apps-storage-review-response-2026-09-10.md](pages-apps-storage-review-response-2026-09-10.md) | — (review response) |
| [pages-apps-validation-2026-09-10.md](pages-apps-validation-2026-09-10.md) | — (validation record) |
| [pages-collections-access-analysis-2026-09-12.md](pages-collections-access-analysis-2026-09-12.md) | — |
| [pages-folder-permissions-linux-model-2026-09-13.md](pages-folder-permissions-linux-model-2026-09-13.md) | implemented & merged as of 2026-09-14; acceptance noted inside |
| [pages-folder-delivery-status-2026-09-14.md](pages-folder-delivery-status-2026-09-14.md) | — (delivery status record) |
| [pages-opponent-followup-2026-09-12.md](pages-opponent-followup-2026-09-12.md) | — (review record) |
| [pages-project-authoring-access.md](pages-project-authoring-access.md) | — |
| [pages-settings-editor-implementation.md](pages-settings-editor-implementation.md) | — |
| [pages-settings-editor-prototype.html](pages-settings-editor-prototype.html) | prototype asset |
| [pages-settings-editor-review-proposal-2026-09-10.md](pages-settings-editor-review-proposal-2026-09-10.md) | proposal to decide; P0 implemented |
| [pages-settings-editor-independent-review-2026-09-10.md](pages-settings-editor-independent-review-2026-09-10.md) | — (review record) |
| [pages-settings-editor-counter-review-2026-09-11.md](pages-settings-editor-counter-review-2026-09-11.md) | — (review record) |
| [pages-settings-editor-counter-review-6fdc2cef.md](pages-settings-editor-counter-review-6fdc2cef.md) | — (review record, pinned SHA) |

## Chat surface

| Document | Status stated in header |
| --- | --- |
| [chat-as-a-primary-surface.md](chat-as-a-primary-surface.md) | draft for review (2026-08-12) |
| [unified-chat.md](unified-chat.md) | — |
| [workspace-conversations.md](workspace-conversations.md) | — |
| [CHAT-SURFACE-CODE-AUDIT-2026-08-13.md](CHAT-SURFACE-CODE-AUDIT-2026-08-13.md) | independent code audit for review; not an implementation plan |
| [CHAT-SURFACE-CONTRACT-SWEEP.md](CHAT-SURFACE-CONTRACT-SWEEP.md) | — |
| [chat-artifact-preview-analysis-2026-09-08.md](chat-artifact-preview-analysis-2026-09-08.md) | proposal, not implemented |
| [chat-files-preview-v1.md](chat-files-preview-v1.md) | — |
| [chat-notification-sounds.md](chat-notification-sounds.md) | — |
| [chat-sidebar-filters-dev2-2026-09.md](chat-sidebar-filters-dev2-2026-09.md) | — (dev2 session) |
| [chat-team-workspace.md](chat-team-workspace.md) | — |
| [chat-workspace-dev2-2026-09-25.md](chat-workspace-dev2-2026-09-25.md) | — (dev2 session) |
| [create-surface-parity.md](create-surface-parity.md) | — |
| [inbox-maximum-wireframe.md](inbox-maximum-wireframe.md) | `/inbox-v2` production implementation + follow-on server contract |
| [conversational-onboarding.md](conversational-onboarding.md) | draft rev 3; blocked, not scheduled |

## Routines

| Document | Status stated in header |
| --- | --- |
| [ROUTINES-CLARITY-PRD-2026-09-15.md](ROUTINES-CLARITY-PRD-2026-09-15.md) | — |
| [ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md) | — |
| [ROUTINES-HYBRID-RELEASE-1-0-2026-09-08.md](ROUTINES-HYBRID-RELEASE-1-0-2026-09-08.md) | — |
| [ROUTINES-RELEASE-IMPLEMENTATION-2026-09-08.md](ROUTINES-RELEASE-IMPLEMENTATION-2026-09-08.md) | — |
| [ROUTINES-TEST-VALIDITY-2026-09-09.md](ROUTINES-TEST-VALIDITY-2026-09-09.md) | — |
| [ROUTINES-UX-DECISION-2026-09-08.md](ROUTINES-UX-DECISION-2026-09-08.md) | — |
| [ROUTINES-WORKSPACE-V2-2026-09-08.md](ROUTINES-WORKSPACE-V2-2026-09-08.md) | — |
| [ROUTINES-WORKSPACE-VERIFICATION-2026-09-08.md](ROUTINES-WORKSPACE-VERIFICATION-2026-09-08.md) | — |
| [PRD-ROUTINE-AUTHORING-2026.md](PRD-ROUTINE-AUTHORING-2026.md) | living draft (2026-06-30) |
| [PRD-ROUTINES-MAX-2026.md](PRD-ROUTINES-MAX-2026.md) | design draft (2026-06-28) |
| [WORK-ORDER-2026-09-09-ROUTINES-FIX-AND-UX.md](https://github.com/crewship-ai/crewship/blob/07bfd2360a97dbe1c9f71c03e1d9b5d5d471c993/docs/prd/WORK-ORDER-2026-09-09-ROUTINES-FIX-AND-UX.md) | — (work order) |
| [ENDING-2026-09-09-ROUTINES-REVIEW.md](https://github.com/crewship-ai/crewship/blob/07bfd2360a97dbe1c9f71c03e1d9b5d5d471c993/docs/prd/ENDING-2026-09-09-ROUTINES-REVIEW.md) | — (review record) |

## Credentials, providers and security

| Document | Status stated in header |
| --- | --- |
| [PRD-CREDENTIALS-V2-2026.md](PRD-CREDENTIALS-V2-2026.md) | proposal for approval (2026-07-28) |
| [PRD-CREDENTIAL-DETAIL-2026.md](PRD-CREDENTIAL-DETAIL-2026.md) | Phase 0 complete; D1–D3, D5–D7 decided, D4 open |
| [model-scoped-credentials.md](model-scoped-credentials.md) | draft; **authoritative** for this topic (addendum carries the revision's decisions) |
| [PRD-MODEL-SCOPED-CREDENTIALS-2026.md](PRD-MODEL-SCOPED-CREDENTIALS-2026.md) | superseded by model-scoped-credentials.md (Czech original, revision record) |
| [CREDENTIALS-VAULT.md](CREDENTIALS-VAULT.md) | — |
| [credential-entry-ux-proposal-2026-09-08.md](credential-entry-ux-proposal-2026-09-08.md) | implemented on dev3 — see implementation report |
| [provider-logins.md](provider-logins.md) | analysis for approval (2026-09-06) |
| [provider-logins-implementation.md](provider-logins-implementation.md) | — (implementation record) |
| [agent-identity-signing.md](agent-identity-signing.md) | draft for external audit; not scheduled |
| [agent-isolation-findings-2026-08-01.md](agent-isolation-findings-2026-08-01.md) | draft; annex to agent-identity-signing.md |
| [agent-ask-packs-and-document-intake.md](agent-ask-packs-and-document-intake.md) | draft for review (2026-08-12) |

## Agent capability, memory and Keeper

| Document | Status stated in header |
| --- | --- |
| [AGENT-CONTINUITY-2026.md](AGENT-CONTINUITY-2026.md) | in progress — PR #630 open (design notes) |
| [PRD-AGENT-EVOLUTION-2026.md](PRD-AGENT-EVOLUTION-2026.md) | v3 + decision log §10 |
| [BRIEF-AGENT-PERFORMANCE-2026.md](BRIEF-AGENT-PERFORMANCE-2026.md) | topic brief for research (2026-06-30) |
| [MISSION-OUTCOMES-TO-MEMORY.md](MISSION-OUTCOMES-TO-MEMORY.md) | — (cited by v108 migration) |
| [MEMORY-ROADMAP-2026.md](MEMORY-ROADMAP-2026.md) | — (cited from `internal/memory`, `internal/api`) |
| [memory-retrieval-layer.md](memory-retrieval-layer.md) | draft (2026-08-02) |
| [agent-memory-on-wake.md](agent-memory-on-wake.md) | draft (2026-08-01) |
| [crew-runtime-capacity.md](crew-runtime-capacity.md) | draft (2026-08-01) |
| [PRD-SLASH-CAPABILITIES-2026.md](PRD-SLASH-CAPABILITIES-2026.md) | — |
| [PRD-NOTIFY-CHANNELS-2026.md](PRD-NOTIFY-CHANNELS-2026.md) | Phase 1 complete; D1+D2 decided |
| [PRD-KEEPER-WATCHDOG-2026.md](PRD-KEEPER-WATCHDOG-2026.md) | draft (2026-07-12) |
| [PRD-KEEPER-WATCHDOG-M1-SPEC.md](PRD-KEEPER-WATCHDOG-M1-SPEC.md) | ready to implement |
| [PRD-KEEPER-WATCHDOG-M2A-SPEC.md](PRD-KEEPER-WATCHDOG-M2A-SPEC.md) | ready to implement |
| [PRD-KEEPER-WEAK-MODELS-2026.md](PRD-KEEPER-WEAK-MODELS-2026.md) | P0–P8 implemented & merged (#1635, #1655…) |
| [keeper-configuration.md](keeper-configuration.md) | draft (2026-07-29); proposes `--wire` |
| [KEEPER-CORPUS-ADJUDICATION-2026-08-02.md](KEEPER-CORPUS-ADJUDICATION-2026-08-02.md) | — (adjudication record) |

## OpenCode and CLI adapters

| Document | Status stated in header |
| --- | --- |
| [opencode-go-zen-integration-2026-09-16.md](opencode-go-zen-integration-2026-09-16.md) | — |
| [opencode-provider-expansion-2026-09-18.md](opencode-provider-expansion-2026-09-18.md) | — |
| [opencode-provider-inventory-2026-09-18.json](opencode-provider-inventory-2026-09-18.json) | inventory data |
| [opencode-security-performance-audit-2026-09-20.md](opencode-security-performance-audit-2026-09-20.md) | — (audit) |
| [opencode-zai-coding-plan-2026-09-18.md](opencode-zai-coding-plan-2026-09-18.md) | — |

## awx-omarchy delivery and JEV

| Document | Status stated in header |
| --- | --- |
| [awx-omarchy-product-improvements-2026-09-14.md](awx-omarchy-product-improvements-2026-09-14.md) | as of 2026-09-24: all ten planned iterations merged to `main` (#2567…) |
| [awx-omarchy-implementation-iterations-2026-09-15.md](awx-omarchy-implementation-iterations-2026-09-15.md) | completed & merged in #2567; follow-up scope inside |
| [omarchy-crewship-research-2026-09-11.md](omarchy-crewship-research-2026-09-11.md) | — (research) |
| [jev-research-and-pilot-2026-09-20.md](jev-research-and-pilot-2026-09-20.md) | as of 2026-09-20; issue #2629 |
| [jev-webhook-router-2026-09-23.md](jev-webhook-router-2026-09-23.md) | — |

## Product seeds, demos and onboarding

| Document | Status stated in header |
| --- | --- |
| [demo-business-seed.md](demo-business-seed.md) | — |
| [demo-team-chat.md](demo-team-chat.md) | — |
| [crewship-guide.md](crewship-guide.md) | implemented foundation (2026-08-22) |
| [iteration-quickwins.md](iteration-quickwins.md) | ready to implement; small, additive |
| [issues-human-agent-work-contract-2026-09-07.md](issues-human-agent-work-contract-2026-09-07.md) | original proposal & pre-implementation audit; extended by #2449 |

## Platform and infrastructure proposals

| Document | Status stated in header |
| --- | --- |
| [PRD-DEVCONTAINER-BUILDKIT-2026.md](PRD-DEVCONTAINER-BUILDKIT-2026.md) | design; phase 1 in progress |
| [PRD-BACKUP-V3-POLISH.md](PRD-BACKUP-V3-POLISH.md) | post-merge follow-up |
| [BRIEF-COLOR-TOKENS-2026.md](BRIEF-COLOR-TOKENS-2026.md) | executed 2026-07-27 (referenced by `eslint.config.mjs`) |

## Handoffs (session records)

Session-to-session handoffs; superseded by events, kept for continuity.
Fresh ones (PR still open) matter; old ones are historical record.


## Subdirectories

- [`reports/`](reports/README.md) — session reports and live-instance
  records; its README separates authored reports from the two
  **generated** docs-inventory outputs (gitignored, written by
  `make docs-inventory`).
- [`wireframes/`](wireframes/) — HTML/PNG wireframes referenced by their
  parent proposals.
- [`assets/`](assets/) — image assets referenced by proposals.
