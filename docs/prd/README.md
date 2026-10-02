# Public implementation design references

This directory retains design and acceptance documents used by public product
source, tests or contributor documentation. Internal research, workstation
handoffs, review transcripts, captured logs and visual experiments live in
[private working context](../development/private-context.md).

A retained design is not proof that every proposed feature shipped. Read its
status, date and implementation references; historical sections describe their
recorded revision. Current executable contracts are indexed in
[docs/specs](../specs/README.md), accepted decisions in
[docs/decisions](../decisions/README.md), and operational procedures in
[docs/runbooks](../runbooks/README.md). The reverse documentation gate excludes
this design directory; that is why current contracts belong in `docs/specs/`.

## Contracts moved to specifications

| Previous path under this directory | Current reference |
| --- | --- |
| `pages-apps-v1.md` | [Pages Apps](../specs/pages-apps.md) |
| `CREDENTIALS-VAULT.md` | [Credentials vault](../specs/credentials-vault.md) |
| `AGENT-ACCESS-POLICY-API-2026-09-29.md` | [Member resource policy](../specs/member-resource-policy.md) |

Shipped migration comments retain their original historical paths. They are
immutable provenance, not imports or dependencies on private documentation.

## Retained design and acceptance context

| Document | Why it remains public |
| --- | --- |
| [BACKUP-SINGLE-WRITER-DECISION-2026-10-01.md](BACKUP-SINGLE-WRITER-DECISION-2026-10-01.md) | Accepted ownership decision; implementation and release gates are explicit. |
| [BRIEF-COLOR-TOKENS-2026.md](BRIEF-COLOR-TOKENS-2026.md) | Shared styling conventions and the checks that enforce them. |
| [MISSION-OUTCOMES-TO-MEMORY.md](MISSION-OUTCOMES-TO-MEMORY.md) | Historical migration/provenance design referenced by shipped schema code. |
| [PRD-ISSUES-AND-ROUTINES-2026.md](PRD-ISSUES-AND-ROUTINES-2026.md) | Issue/routine acceptance design referenced by implementation and regression tests. |
| [PRD-SLASH-CAPABILITIES-2026.md](PRD-SLASH-CAPABILITIES-2026.md) | Capability model and slash-command design referenced by source/tests. |
| [PRIVATE-ISSUE-PREFLIGHT-CONTRACT-2026-09-30.md](PRIVATE-ISSUE-PREFLIGHT-CONTRACT-2026-09-30.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [QUEUE-MECHANISM-2026.md](QUEUE-MECHANISM-2026.md) | Admission-control design referenced by the dispatcher and tests. |
| [RESTRICTED-HTTP-BROKER-CONTRACT-2026-09-28.md](RESTRICTED-HTTP-BROKER-CONTRACT-2026-09-28.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [RESTRICTED-RESPONSES-ADAPTER-2026-09-29.md](RESTRICTED-RESPONSES-ADAPTER-2026-09-29.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [RESTRICTED-RUNTIME-CONTROLLER-EXPIRY-2026-09-28.md](RESTRICTED-RUNTIME-CONTROLLER-EXPIRY-2026-09-28.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [RESTRICTED-RUNTIME-SERVER-CONTRACT-2026-09-28.md](RESTRICTED-RUNTIME-SERVER-CONTRACT-2026-09-28.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [ROUTINES-CLARITY-PRD-2026-09-15.md](ROUTINES-CLARITY-PRD-2026-09-15.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [ROUTINES-DURABILITY-QUICKWINS.md](ROUTINES-DURABILITY-QUICKWINS.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [SERVICE-PERSISTENT-QUOTA-CONTRACT-2026-09-30.md](SERVICE-PERSISTENT-QUOTA-CONTRACT-2026-09-30.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [SPIKE-RIVER-SQLITE-1-0.md](SPIKE-RIVER-SQLITE-1-0.md) | Reproduction protocol for the public rejection ADR and retained harness. |
| [WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [agent-ask-packs-and-document-intake.md](agent-ask-packs-and-document-intake.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [agent-memory-on-wake.md](agent-memory-on-wake.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [chat-as-a-primary-surface.md](chat-as-a-primary-surface.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [chat-files-preview-v1.md](chat-files-preview-v1.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [chat-notification-sounds.md](chat-notification-sounds.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [chat-team-workspace.md](chat-team-workspace.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [conversational-onboarding.md](conversational-onboarding.md) | Original onboarding design referenced by implementation; header is historical. |
| [create-surface-parity.md](create-surface-parity.md) | Historical UI parity criteria referenced by shared components and tests. |
| [crew-runtime-capacity.md](crew-runtime-capacity.md) | Runtime capacity design and constraints referenced by source/tests. |
| [crewship-guide.md](crewship-guide.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [demo-business-seed.md](demo-business-seed.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [demo-team-chat.md](demo-team-chat.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [documentation-contract-testing.md](documentation-contract-testing.md) | Design rationale for public contract-checking tooling. |
| [inbox-maximum-wireframe.md](inbox-maximum-wireframe.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [issues-human-agent-work-contract-2026-09-07.md](issues-human-agent-work-contract-2026-09-07.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [keeper-configuration.md](keeper-configuration.md) | Keeper configuration rationale referenced by implementation. |
| [memory-retrieval-layer.md](memory-retrieval-layer.md) | Retrieval design referenced by the public implementation. |
| [model-scoped-credentials.md](model-scoped-credentials.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [opencode-go-zen-integration-2026-09-16.md](opencode-go-zen-integration-2026-09-16.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [pages-apps-architecture.md](pages-apps-architecture.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [pages-collections-access-analysis-2026-09-12.md](pages-collections-access-analysis-2026-09-12.md) | Folder design history referenced by source; later permissions model supersedes parts. |
| [pages-folder-permissions-linux-model-2026-09-13.md](pages-folder-permissions-linux-model-2026-09-13.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [pages-project-authoring-access.md](pages-project-authoring-access.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [pages-settings-editor-review-proposal-2026-09-10.md](pages-settings-editor-review-proposal-2026-09-10.md) | Editor interaction and acceptance design referenced by tests. |
| [provider-logins.md](provider-logins.md) | Provider-account lifecycle, access and refresh design referenced across source/tests. |
| [unified-chat.md](unified-chat.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |
| [workspace-conversations.md](workspace-conversations.md) | Product behaviour, design constraints or acceptance boundaries; read the dated status in the document. |

## New documents

Internal plans, investigations and session records go to private context.
A new public proposal must be intended for public discussion, state its status
and date, and identify its public issue or PR. Promote an implemented contract
to `docs/specs/` with its code-facing documentation checks. Keep reproducible
public tests and required design context available without private access.

[reports/README.md](reports/README.md) explains ignored generated inventory
output and the one retained regression source fixture. Previously published
working records remain accessible in Git history; removal from this tree does
not make historical copies secret or revoke their licenses.
