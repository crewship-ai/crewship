# Human requests in an agent inbox

The Agent Overview **Human inbox** card and `crewship agent inbox human` project
canonical human sources alongside the existing teammate inbox. The original
agent inbox endpoint remains a peer/workload summary. Human projection is an
additive authorized read, not another dispatch queue.

## Sources and delivery

Supported sources are messages mentioning this agent in an existing joined
workspace channel, issues created by the current viewer and delegated to this
agent, and canonical host-chat user turns visible to the viewer. Private chat
messages retain their original audience; sealed artifact-context chats remain
on their existing audited chat surface. The projection does not copy source
bodies into mailbox records or grant access to another human's private session.

The UI message composer and CLI `agent inbox send` use the existing conversation
send endpoint. The channel and exactly one mentioned agent are explicit;
joining an agent is a separate authorized operation. The task composer and CLI
`agent inbox task` create and start a canonical issue. Existing approval,
delegate, project and start gates remain effective. Public starts capture a
host-issued `web_issue` execution origin, even without a private chat.
Creation uses an atomic idempotency receipt. The start request also carries a
stable key, but the canonical start endpoint does not provide operation replay.
After a lost response or a start 400, wrappers may read back that same issue
through its authorized GET. A matching delegate with an already-running or
terminal state is reported as **observed existing state**, not proof that the
retry started work. Authentication, authorization and conflict errors are never
masked. BACKLOG/TODO or unavailable readback preserves the original error and
key. No recovery path automatically creates or starts another task.

## Read receipts

Listing an inbox, receiving a WebSocket event, or observing a running job does
not mark anything read. An actual agent uses the argument-free MCP tool
`inbox_read`. Its verified current run may acknowledge only its host-recorded
source. The host checks the current work attempt and generation, source
membership, original requester's live authority and immutable admission.
Cancelled or superseded work cannot acknowledge a source.

Receipts bind channel and chat content digests or the frozen issue work input
and brief revision. Edited content becomes unread; an old run cannot read a new
unapproved brief. Digests for private host-chat admissions live only in private
host state, not journal/SSE/WebSocket payloads. The tool returns the canonical
source and a durable read timestamp. Repeating the same read preserves that
timestamp. It does not complete work or approve anything.

Legacy channel jobs without a source-authority snapshot use the current requester,
channel and target access checks for dispatch and acknowledgement. Newly admitted
jobs must also match the recorded source authority and digest. Older chat runs without a canonical mirrored admission
commitment may continue through their existing execution path but cannot invent
an inbox read receipt. Portable workspace forks exclude runtime-bound receipts
and run commitments; local database rollback checkpoints retain them.

## UI updates and boundaries

The card operates in the existing authenticated workspace context. Restricted
context retains its existing route deny policy. It listens to existing inbox,
conversation, issue and run events and polls every 30 seconds when the workspace feature is enabled. Generation-bound
queries discard stale responses after context or refresh changes. A source
revocation is enforced on every server read; polling bounds refresh where no
source event is delivered. Human send acceptance, agent read and work completion
are displayed as separate facts.

Implementation: `internal/api/agent_human_inbox.go`,
`internal/groupchat/agent_inbox.go`,
`components/features/crews/agent-human-inbox.tsx`, and
`cmd/crewship/cmd_agent_human_inbox.go`.


## Human inbox HTTP contract

`GET /api/v1/agents/{agentId}/human-inbox`

Authentication: a valid bearer token or session cookie, current workspace
membership and existing trusted-session route access. Source audiences are
rechecked on every request; this endpoint grants no agent execution authority.

Request: path parameter `agentId`; select the workspace with `workspace_id` or
`X-Workspace-ID`. There is no request body or read acknowledgement parameter.

Response: JSON object with required `items` array and `read_semantics` string.
Each item always contains string `id`, `kind`, `content`, `state`, `href`,
`created_at` and nullable string `read_at`. `read_at: null` means agent read is
not verified for this current source version. The response does not imply
receipt merely because `state` is running or completed.

Statuses: 200 for an authorized projection (including an empty list), 401 for
missing authentication/workspace context, 403 for denied workspace or restricted
session access, 404 for an unavailable agent or a disabled flag, and 5xx for
unavailable database or source reads. Errors use a JSON `error` string.

## Rollout and retention

The `agent_human_inbox` feature is off by default. Enable it with a workspace
feature-flag override. Older channel jobs with no recorded source digest and
authority epoch continue to use current channel and workspace access. New
jobs carry the source digest and current authority epoch.

Host chat admission records an optional source commitment inside the existing
work admission transaction. An active run without a recorded source returns
a readable unavailable-source error. The work retention sweep removes source
commitments and read receipts for attempts that ended more than 30 days ago.
An attempt deleted by a hard purge also loses its evidence in that transaction.
