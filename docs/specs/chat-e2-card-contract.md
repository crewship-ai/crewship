# E2 card projection contract (A1)

This slice supplies typed in-process read handlers, closed Go/Zod models and
handler-produced fixtures for subsequent proposal and decision integrations.
It does **not** register public HTTP endpoints, persist proposals, confirm work
or resolve decisions. A2/C/D must register functional sources, CLI, OpenAPI and
documentation together; a fixture reader is not production storage.
Generated JSON is compact; the Go/Zod sources are the reviewable definitions.

`ChatCardResponse` reuses the Issue reference's event, provenance and run/message
anchor. It adds exactly one `proposal` or `decision` payload with
`schema_version: 1`; existing Issue endpoints and their contract remain unchanged.
The projection handler uses the existing trusted chat audience before and after
source reading. Readers must filter the exact source/chat/workspace for the
current viewer and hide the entire inaccessible object, not return partial
items/counts. Terminal cards do not require an actively running agent.

Proposals contain at most 20 stable-ID issues and a numeric version. States are
`draft`, `accepted`, `discarded`, `expired`. Accepted outcomes identify the human,
reviewed version and created Issue references; terminal actions are read-only.
Create/edit command bodies contain intent only. Confirm identifies the proposal
and reviewed version. Mutations require a stable idempotency key; the functional
mutators and their conflict/retry fixtures belong to A2, not this read slice.

Decisions project existing escalation, waitpoint or whole-run approval sources.
Their version is an opaque adapter token, not another answer-state ledger.
Closed form types are text, textarea, choice, boolean, number and datetime.
Information answers cannot approve an operation. Workflow approvals resolve only
their identified waitpoint; they cannot become whole-run approvals. The source
adapter chooses actions from the existing source policy, not from model text.
Typed waitpoint actions retain their source `action_id`, label and approved/denied
effect through `workflow_actions`; answers and terminal receipts identify the
selected action. The source still validates its frozen form and governs effects. Pending
and terminal states come from the authoritative source; source adapters must
settle expiry and enforce answer/approval rights. Resolution values are bounded
scalars keyed to known fields, with no arbitrary components or JavaScript.

The exported JSON Schema describes structural bounds; Go and Zod additionally
enforce state-dependent receipts, action sets and form constraints. Unicode
lengths count code points; numeric versions/revisions fit JavaScript safe integers. Handler fixtures use injected typed source snapshots
and a real migrated database/host-started run for audience checks; they are not
evidence of proposal persistence, mutations or a live provider.
List envelopes retain E1 replacement-snapshot semantics. Pagination and
version-conflict envelopes are typed here; actual list/mutation handler fixtures
are added with A2/C, not fabricated by this single-card read handler.

Regenerate and verify through the normal managed build/test runner:

```bash
UPDATE_CHAT_E2_FIXTURES=1 go test ./internal/api -run '^TestChatCardProjection' -count=1
UPDATE_CHAT_E2_SCHEMAS=1 pnpm exec vitest run lib/__tests__/chat-e2-cards.test.ts
```

Models: [Go payloads](../../internal/chatentities/cards.go),
[projection handler](../../internal/api/chat_card_projection.go),
[Zod contract](../../lib/chat-e2-cards.ts),
[handler fixtures](../../internal/api/testdata/chat-e2/).

## Subsequent source integration

A2/C register the functional read and mutation endpoints with their CLI,
OpenAPI and documentation. Their integration plan is kept with the task
until those endpoints exist; this spec documents the shipped A1 surface only.

Conflicting versions return 409 with `ChatCardVersionConflict` and an authorized
current snapshot. Reuse of an idempotency key with different intent is a separate
409 error and does not return an unrelated operation's result. Hidden sources
return a generic 404 without a snapshot. Replays must recheck current read access.
Proposal creation is agent-only: the MCP/sidecar transport accepts
`ProposalCreate`; the host derives chat/run/requester from verified origin,
never from a model-supplied chat or identity.
