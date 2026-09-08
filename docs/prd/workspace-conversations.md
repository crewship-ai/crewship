# Workspace conversations

Date: 2026-09-06. Status: first implementation validated locally; Dev2 API/schema observed deployed on 2026-09-07. Not a corporate production-readiness claim.
Baseline: main `56969d7e`. Owner direction in the Dev2 session explicitly advances
`chat-as-a-primary-surface.md` §7 beyond that document's original no-new-model scope.

## Product contract

The unified Chat phase is specified in [unified-chat](unified-chat.md). Human
rooms now belong under `/chat?conversation=...`, with `/conversations` retained
only for old-link compatibility; there is one user-facing Chat surface.

Crewship keeps existing agent sessions and their links. People can also create
named workspace channels and participant-only groups without selecting or running
an agent. A group with two people remains a normal group. Dedicated direct-message
discovery and deduplication are the next implementation phase below. Left navigation selects conversations;
the right context presents actual conversation participants. Existing agent Team
remains explicitly a crew roster, not a private conversation roster.

Channels are readable by current workspace members. Groups are readable only by
current workspace members explicitly in the group, including their creator.
Workspace administration alone does not silently grant access to a private group.
Adding someone grants access to existing history. Removing someone revokes future
API access; already downloaded content cannot be recalled. New participants and
access changes must be explained in the UI. No new attachment route is introduced
until the same conversation ACL can protect storage and downloads.

## Durable model and delivery

Use SQLite WAL with existing pooling and migrations. New conversation records
are independent of legacy chats and agent lifecycle. Membership, authorship,
monotonic per-conversation sequence and author-scoped client idempotency keys are
explicit. Message persistence and its outbox event commit atomically. Retries with
the same key and different contents return conflict. Reads are cursor-paginated;
reconnect reloads persisted history, not only process-memory frames.

Conversation writes, inbox projection and agent-job transactions share a
context-aware admission queue per database handle before acquiring a pooled
connection. This prevents queued chat writers from exhausting the read pool
or repeatedly overtaking each other in SQLite busy waits. SQLite still arbitrates
writes from other subsystems and processes; this is not a distributed lock.

Outbox delivery is at least once. Consumers must be idempotent. Realtime events
are advisory invalidations on authorized user channels, containing identifiers
only. Durable history is authoritative. Inbox aggregates unread activity per
recipient/conversation, respects monotonically advancing read cursors and must
not leak private transcript contents after access revocation. Human sends do not
resolve, provision or execute an agent.

Channel agent participation uses a separate integration: explicit agent membership,
visible history-access join event, structured mention IDs, one durable dispatch
per source message/agent, existing orchestration admission/credentials/policy,
and completion projected back into the conversation. No automatic agent-to-agent
mention loops. Private groups do not accept agents because legacy execution
records are workspace-readable. Channel context supplied to a run is bounded
to the latest 100 messages at its triggering message and 128 KiB; older history
is not silently promised as unlimited model context. Dispatch and reply must
both be tested before the integration is described as complete.

## Scale and operational scope

Ten concurrent human senders and a workspace roster of 100+ are acceptance
scenarios, not a performance guarantee. Measure on isolated real SQLite files
with WAL and a five-connection pool; include duplicate retries and reopen/replay.
An in-memory test alone is not evidence about SQLite write contention.
The 100-active-client HTTP acceptance runs in the normal and race Go suites; see
[reproduction and measured limits](../../e2e/workspace-conversations-load.md).
It tests short concurrent bursts, not sustained mixed agent/chat production load.
A single-node deployment remains the current target. PostgreSQL and distributed
fanout are future operational options, not dependencies of this change. Before
corporate launch, separately validate host capacity under mixed agent/chat load,
backup restore, retention/deletion/export and identity provisioning requirements.

## Backup and restore contract

All seven conversation tables, including direct-pair lookup, participate in workspace backup, including humans
who only participate in conversations and agents without a crew. Fork restore
remaps references and inbox deep links. History, membership, mute preferences
and read cursors survive restore. Pending agent jobs become explicitly failed,
linked active assignments are cancelled and historical outbox events are
acknowledged. This deliberately prevents restoring a backup from launching paid
agent work or replaying old notifications; fresh messages still deliver normally.

## Acceptance

- Create, list and reopen a group/channel; preserve legacy agent session flows.
- Real authors, stable ordered history, idempotent retries and pagination.
- Ten concurrent humans: no missing/duplicated committed messages; record latency.
- Cross-workspace and same-workspace nonmember access fail closed on every new
  conversation read/write. Removed members immediately lose subsequent access.
- Outbox replay after restart does not duplicate inbox activity or resurrect read
  messages. Offline recipient can open the conversation from the canonical inbox.
- Browser checks cover desktop/mobile creation, send/retry, history and navigation.
- Go tests/vet, frontend tests/lint/static build, migration and route contracts pass.

Record actual validation and outstanding scope in the implementation report;
never infer corporate readiness from a passing single-room concurrency test.

## Implemented boundaries

Personal mute suppresses future inbox projection, not history or unread counts.
Already delivered inbox activity is not retroactively erased by muting.
Only the creator manages a group's participants or a channel's agents; owner
transfer/archive are follow-ups. Workspace channels inherit human access from
workspace membership, so their roster has no misleading per-channel removal.
Old agent chats keep their existing workspace access semantics; this change does
not relabel them as private DMs. Group file attachments, threaded replies,
reactions, human mention-specific alerts and corporate retention/SSO policies
are not part of this first runnable group surface.

## Phase 2 — human direct messages (2026-09-07)

Implemented, validated and deployed to Dev2; see
[verification and deployment report](reports/chat-direct-messages-2026-09-07.md).

Open a direct message by selecting another current workspace member. The server
returns the same conversation for that unordered pair within the workspace,
including concurrent first opens from opposite sides. Reloading or opening from
the colleague picker does not create a second history. Existing two-person groups
are not silently converted or merged.

A direct message has exactly two original human participants, with the same
workspace-membership and private-history ACL as groups. Neither participant nor
a workspace administrator can add a third person or an agent through conversation
membership controls. Use a new group to expand a discussion. A persistent direct
marker survives identity deletion so a direct history cannot become a mutable
group accidentally. Removing workspace access revokes subsequent reads/writes.

`POST /api/v1/conversations/direct` accepts only `user_id`, derives the caller
from authentication, and returns a Conversation: 201 when created, 200 when
reopened. `is_direct` distinguishes this subtype from ordinary groups; `kind`
remains `group` for API compatibility. Pair identities and uniqueness must survive
backup/fork remapping, including reordering new IDs into canonical pair order.

Acceptance: discover/open from desktop and mobile; no self-chat; concurrent
reverse-pair opens return one ID; reopening preserves messages; cross-workspace
and unrelated-member access stays denied; direct membership cannot be expanded;
backup/fork preserves deduplication and private history. No model is invoked by
opening or sending a human direct message.
