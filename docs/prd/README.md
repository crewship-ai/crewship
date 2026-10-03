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

## Browse by topic

These descriptions explain what to read a document for. They do not refresh
its implementation status; follow its evidence and the current contract.

[Work and dispatch](#work-and-dispatch) · [Runtime and authority](#runtime-and-authority) · [Pages and authoring](#pages-and-authoring) · [Chat and collaboration](#chat-and-collaboration) · [Memory and guidance](#memory-and-guidance) · [Storage and recovery](#storage-and-recovery) · [Product conventions and verification](#product-conventions-and-verification)

## Work and dispatch

Current boundaries: [restricted workflows](../specs/restricted-workflows.md) and [issue preflight](../specs/private-issue-preflight.md). Queue choice: [decision](../decisions/README.md).

| Document | Context and limits |
| --- | --- |
| [PRD-ISSUES-AND-ROUTINES-2026.md](PRD-ISSUES-AND-ROUTINES-2026.md) | Issue/routine work loop and acceptance tracks; distinguish local branch evidence from merged delivery. |
| [issues-human-agent-work-contract-2026-09-07.md](issues-human-agent-work-contract-2026-09-07.md) | Human/agent work handoff and its implementation/test plan. |
| [QUEUE-MECHANISM-2026.md](QUEUE-MECHANISM-2026.md) | Original per-crew memory admission proposal; its draft header is historical. |
| [WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md](WEBHOOKS-AGENT-PARALLELISM-MEMORY-1-0-2026-09-10.md) | Design context for webhook dispatch, parallelism and memory. |
| [WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md](WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md) | Implementation boundaries for the durable dispatch design. |
| [SPIKE-RIVER-SQLITE-1-0.md](SPIKE-RIVER-SQLITE-1-0.md) | Reproduction protocol for the rejected queue alternative; links the ADR and retained harness. |
| [ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md](ROUTINES-CLIENT-EXPERIENCE-PRD-2026-09-08.md) | Routine authoring and operator experience; includes separate human acceptance limits. |
| [ROUTINES-CLARITY-PRD-2026-09-15.md](ROUTINES-CLARITY-PRD-2026-09-15.md) | Routine inputs, working rules and diagnostics, with dated local verification. |
| [ROUTINES-DURABILITY-QUICKWINS.md](ROUTINES-DURABILITY-QUICKWINS.md) | Historical durability and observability changes shipped through PR #455. |

## Runtime and authority

Start with [restricted context](../specs/restricted-context.md), [member policy](../specs/member-resource-policy.md) and [credentials](../specs/credentials-vault.md).

| Document | Context and limits |
| --- | --- |
| [PRIVATE-ISSUE-PREFLIGHT-CONTRACT-2026-09-30.md](PRIVATE-ISSUE-PREFLIGHT-CONTRACT-2026-09-30.md) | Original private issue admission boundary; start with the current issue-preflight specification. |
| [RESTRICTED-HTTP-BROKER-CONTRACT-2026-09-28.md](RESTRICTED-HTTP-BROKER-CONTRACT-2026-09-28.md) | Fixed-operation broker prototype; explicitly does not enable production entrypoints. |
| [RESTRICTED-RESPONSES-ADAPTER-2026-09-29.md](RESTRICTED-RESPONSES-ADAPTER-2026-09-29.md) | Provider transport boundary and its limits; not application execution acceptance. |
| [RESTRICTED-RUNTIME-CONTROLLER-EXPIRY-2026-09-28.md](RESTRICTED-RUNTIME-CONTROLLER-EXPIRY-2026-09-28.md) | Independent expiry after controller death and its original integration scope. |
| [RESTRICTED-RUNTIME-SERVER-CONTRACT-2026-09-28.md](RESTRICTED-RUNTIME-SERVER-CONTRACT-2026-09-28.md) | Original server/acceptance record; later lease supervision supersedes the stated manager-death limitation. |
| [crew-runtime-capacity.md](crew-runtime-capacity.md) | Runtime capacity constraints and the design referenced by source/tests. |
| [PRD-SLASH-CAPABILITIES-2026.md](PRD-SLASH-CAPABILITIES-2026.md) | Slash-command and per-user capability model referenced by source/tests. |
| [keeper-configuration.md](keeper-configuration.md) | Keeper configuration proposal and rationale referenced by implementation. |
| [model-scoped-credentials.md](model-scoped-credentials.md) | Design for limiting credential use by model; read its proposal status. |
| [provider-logins.md](provider-logins.md) | Provider-account lifecycle, access and refresh context used by source/tests. |
| [opencode-go-zen-integration-2026-09-16.md](opencode-go-zen-integration-2026-09-16.md) | Provider integration contract and dated source revision. |

## Pages and authoring

Start with [Pages](../specs/pages.md) and [Pages Apps](../specs/pages-apps.md).

| Document | Context and limits |
| --- | --- |
| [pages-apps-architecture.md](pages-apps-architecture.md) | Broader application architecture; the Pages Apps specification owns the delivered contract. |
| [pages-collections-access-analysis-2026-09-12.md](pages-collections-access-analysis-2026-09-12.md) | Folder, sharing and sidebar design history; later permissions design supersedes parts. |
| [pages-folder-permissions-linux-model-2026-09-13.md](pages-folder-permissions-linux-model-2026-09-13.md) | Inherited folder permissions and explicitly bounded acceptance evidence. |
| [pages-project-authoring-access.md](pages-project-authoring-access.md) | Authoring authority separated from panel visibility and whole-document reads. |
| [pages-settings-editor-review-proposal-2026-09-10.md](pages-settings-editor-review-proposal-2026-09-10.md) | Editor interactions and acceptance criteria; distinguishes implemented P0 from pending user acceptance. |

## Chat and collaboration

Start with the [chat guide](../guides/chat-sessions.mdx) and [UI/UX contract](../ux/README.md).

| Document | Context and limits |
| --- | --- |
| [chat-as-a-primary-surface.md](chat-as-a-primary-surface.md) | Original navigation and mobile chat proposal; later conversation design extends its scope. |
| [workspace-conversations.md](workspace-conversations.md) | Workspace conversation model with dated local/development evidence. |
| [unified-chat.md](unified-chat.md) | Shared conversation surface for agent sessions, human DMs and mixed groups. |
| [chat-team-workspace.md](chat-team-workspace.md) | Team workspace chat interaction design. |
| [chat-files-preview-v1.md](chat-files-preview-v1.md) | File preview interaction and implementation design. |
| [chat-notification-sounds.md](chat-notification-sounds.md) | Personal audible alert behaviour shared by Chat and Inbox. |
| [inbox-maximum-wireframe.md](inbox-maximum-wireframe.md) | Inbox attention model and no-loss acceptance boundaries. |
| [agent-ask-packs-and-document-intake.md](agent-ask-packs-and-document-intake.md) | Prepared questions and document-intake design. |

## Memory and guidance

Start with the [memory guide](../guides/agent-memory.mdx); [restricted context](../specs/restricted-context.md) owns scoped access.

| Document | Context and limits |
| --- | --- |
| [MISSION-OUTCOMES-TO-MEMORY.md](MISSION-OUTCOMES-TO-MEMORY.md) | Historical outcome-to-memory design referenced by shipped schema migrations. |
| [agent-memory-on-wake.md](agent-memory-on-wake.md) | Recall at agent wake and its surrounding memory design. |
| [memory-retrieval-layer.md](memory-retrieval-layer.md) | Retrieval/storage proposal referenced by the public implementation. |
| [crewship-guide.md](crewship-guide.md) | Persistent product-specialist design and the scope of its implemented foundation. |
| [conversational-onboarding.md](conversational-onboarding.md) | Original conversational onboarding design; its header is historical. |

## Storage and recovery

Start with [quota backup](../specs/quota-service-backup.md) and the [backup guide](../guides/backup.mdx).

| Document | Context and limits |
| --- | --- |
| [BACKUP-SINGLE-WRITER-DECISION-2026-10-01.md](BACKUP-SINGLE-WRITER-DECISION-2026-10-01.md) | Accepted backup ownership decision; header explicitly leaves implementation/release verification pending. |
| [SERVICE-PERSISTENT-QUOTA-CONTRACT-2026-09-30.md](SERVICE-PERSISTENT-QUOTA-CONTRACT-2026-09-30.md) | Fixed service-volume quota design with dated branch/acceptance status; see the current quota-backup specification. |

## Product conventions and verification

Start with the [UI/UX contract](../ux/README.md) and [script catalog](../../scripts/README.md).

| Document | Context and limits |
| --- | --- |
| [BRIEF-COLOR-TOKENS-2026.md](BRIEF-COLOR-TOKENS-2026.md) | Shared colour tokens and enforcement context. |
| [create-surface-parity.md](create-surface-parity.md) | Historical UI consistency criteria referenced by shared components/tests. |
| [demo-business-seed.md](demo-business-seed.md) | Public business demo fixtures and project structure. |
| [demo-team-chat.md](demo-team-chat.md) | Fictional colleague, role and conversation fixtures. |
| [documentation-contract-testing.md](documentation-contract-testing.md) | Original proposal for documentation verification; use the current script catalog for commands. |

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
