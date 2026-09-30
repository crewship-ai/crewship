# Private assigned-issue preflight

## POST

```http
POST /api/v1/workspaces/{workspaceId}/issues/{issueId}/private-preflight
```

Authentication: the session actor must have current restricted membership,
manual routine-run permission, exact agent.run and project.read. The issue must
be a current project-backed TODO issue assigned to that agent and held for agent
work. The host reads its current title and brief; the caller cannot replace them.

Request: JSON `agent_id` and `routine_slug`; required nonempty strings. Unknown
fields are rejected, including principal or replacement task content. The body
limit is 4096 bytes. The routine must be a supported frozen private graph.

Response: 202 JSON `run_id`, `chat_id`, `status` and `restricted: true`.
The initial status is SCHEDULED; a repeated claim returns the same durable
identity and its current private state. No prompt or source text appears in the
receipt. The queue commits the immutable source binding and job together.

Statuses: 400 malformed input, 404 no authorized runnable work or rejected budget,
409 busy or uncertain prior work requiring reconciliation, 503 missing private
workflow runtime. Errors use JSON `error` and do not disclose another actor's
receipt or source text.

## Ownership and recovery

Source, actor, membership revision, routine hash and target profile participate in
deduplication. Legacy assignments and private pending/running jobs share capacity
checks. A finite workspace hard budget is required before preparing an origin;
the model broker separately reserves cost before every upstream request.

Preparation and duplicates do not invoke a model. The existing durable private
workflow queue owns execution; this endpoint installs no second polling ticker or
LLM ownership loop. Source edits, reassignment, human takeover, revoked project
permissions or changed graph/provider authority fence queued and executing work.
Failed or canceled work remains parked even after a source edit. It requires
explicit reconciliation instead of blindly repeating an unknown external effect.

## Synthetic event replay

`TestLivePreflightSameEventReplay` uses the production Docker text worker and a
local TLS provider with strict measured usage. Both paths receive the same fixed
eight-event sample: four empty/foreign events, one runnable issue and three repeat
notifications. Its intentionally forced-model-poll baseline makes eight requests;
durable preflight makes one, with no empty or duplicate model wakes. The test
checks settled tokens, durable reservation-before-wire and queue owner count, and
records time from a runnable notification to the provider request.

This is a bounded protocol canary. Fixed synthetic usage, sequential event replay
and one agent do not predict fleet latency, real provider cost or production
scheduler savings. Larger workload measurements remain separate release evidence.
