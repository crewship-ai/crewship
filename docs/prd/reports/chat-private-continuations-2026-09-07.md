# Private conversation continuations — 2026-09-07

The Chat participant flow can start a separate private group from a DM or an
explicitly workspace-visible channel from a private discussion. It never changes
an existing conversation's audience or copies private history. Workspace agent
execution is still workspace-readable, so private group/DM agent membership is
not relaxed. The alternative is a new channel with explicit visibility and fresh
history, containing only the agent's new join notice.

POST `/api/v1/conversations/{conversationId}/continue` accepts `kind`, `title`,
`member_ids`, optional `agent_id`, and required stable `client_id`. A group needs
a DM source and additional humans; a channel requires an agent. Existing humans
are included. Source access, current workspace membership, agent/crew visibility,
new room, roster, optional agent join notice/outbox and durable retry mapping
commit in one immediate transaction. It creates no agent job.

Migration `20260907131137_workspace_conversation_continuations` stores a retry
mapping keyed by source, requester and client ID. Canonical requests sort/dedup
human IDs and normalize title whitespace. Identical retries return the target
under its current ACL; changed payloads return conflict. Target/source and
requester foreign keys remove stale mappings on deletion. Backup includes the
mapping, remaps room/user/agent references and canonical request JSON, and repeats
request-member remapping after user email reconciliation.

CLI parity: `chat room continue` with `--kind`, `--title`, `--member`, `--agent`
and required `--client-id`. The original DM remains the canonical two-person
room and its history remains unreadable to newly invited humans.

Verification covers concurrent retries, durable identity, empty group history,
unchanged canonical DM and ACL, zero agent jobs on channel join, rollback of a
complete channel when recording its retry mapping fails, outside-workspace
invite rejection, HTTP 201/200/409 behavior, strict body rejection, CLI request
contracts, and a migrated dump→fork→email-reconcile→restore→retry integration.
