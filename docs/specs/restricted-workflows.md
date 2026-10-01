# Restricted private workflows

Status (2026-10-01): execution remains an explicit operator opt-in through immutable runtime image configuration. Manual routines, declared Page actions and assigned-issue preflight use the common `work` ledger and `dispatch.Dispatcher` for claims, capacity, leases and recovery. The isolated Docker manager still uses its own CLI transport; direct chat/CLI execution and provider lifecycle integration remain separate release work. This integration does not establish release-1.0 acceptance. Backup write barriers and post-recovery queue holds also apply to private dispatch. A running private job holds its writer lease through completion, so a backup may time out and retry rather than snapshot active writes.

Restricted members invoke manual routines through `POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run`. Existing role or `routine.run` capability checks remain necessary. Each resolved leaf additionally requires current target-agent run authority; cross-agent delegation requires explicit delegate authority. Unsupported scripts, HTTP steps, cycles, cross-workspace targets and unavailable execution profiles fail before enqueue.

The restricted JSON body accepts only `inputs`, `expected_definition_hash`, `expected_execution_hash` and `delay_seconds` (0–86400). The hashes check the server-resolved recipe and complete execution graph; they grant no authority. `Idempotency-Key` allows retries of the same admission. Admission returns HTTP 202 with `run_id`, `chat_id`, `status`, `restricted: true` and `status_url`.

`GET /api/v1/workspaces/{workspaceId}/restricted-routines` returns an array of authorized routines containing only `slug`, `name`, `definition_hash`, `execution_hash` and input descriptors (`name`, scalar `type`, `required`). `X-Total-Count` counts this authorized projection. Prompts, input defaults, provider credentials, graph bodies and denied routines are excluded.

`GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs` lists the actor's currently authorized private runs. `GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}` returns `run_id`, `status`, `step_outputs` and `created_at`. Outputs are released only after completion and current provenance checks. Foreign, revoked and unavailable receipts return 404. Cleanup-revoked failures do not currently expose a result receipt.

Declared Page actions use the same common dispatch ownership with private domain authority with their original Page, panel, action and publication policy. Restricted polling through `GET /api/v1/pages/{slug}/application/actions/{pendingId}` requires an exact private job/Page/actor binding and returns `pending_id`, `run_id`, `pending_status`, `run_status`, `restricted`, `routine_revision_pinned` and `step_outputs`. It never falls back to a generic pending run or shared journal for an unmatched ID.

Execution is bounded to 16 agent leaves, 64 graph nodes and nesting depth four. Frozen server-selected provider slots and opaque completion proofs retain provenance across delegated calls and a return to the original agent. Revoked source authority, recipe publication or provider grants invalidate descendants. Queued work rechecks authority before launch and while running; uncertain interrupted execution is not automatically retried. Workspace budget reservations precede provider requests, and missing terminal usage retains the conservative debit.

These projections use private context and delivery rather than shared activity streams. The routine UI selects them using the server-derived current membership access mode. Text and native execution require an explicitly installed typed adapter for the configured profile; unsupported profiles have no trusted fallback.

## Authorized routine catalog

```http
GET /api/v1/workspaces/{workspaceId}/restricted-routines
```

Authentication requires the current workspace member's bearer token or session.
The request has no JSON body. The HTTP 200 response is an array of the authorized
routine metadata described above; `X-Total-Count` is the filtered count. Unavailable or unauthorized projections return 404; the authentication and
workspace middleware can reject the request before projection.

## Private receipt list

```http
GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs
```

Authentication requires the current actor's workspace bearer token or session.
The request has no JSON body. The HTTP 200 response is an array of authorized
`run_id`, `status`, `step_outputs`, `created_at` objects. Only this actor's private
receipts are considered. Unavailable or unauthorized projections return 404;
authentication and workspace middleware apply first.

## Private receipt result

```http
GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}
```

Authentication requires the current actor's workspace bearer token or session.
The request has no JSON body. The HTTP 200 response contains `run_id`, `status`,
`step_outputs` and `created_at`; unavailable, foreign or revoked results return
404. Authentication and workspace middleware apply before projection.

`GET /api/v1/workspaces/{workspaceId}/restricted-pages` returns Pages with at least one currently executable declared action. Each row contains `slug`, `name`, optional current `publication`, and `actions` (`panel_id`, `id`, `label`, collected input descriptors and optional host confirmation). The count includes only returned Pages. Existing Page/panel visibility and action role floors, every graph resource grant and installed profile eligibility apply before disclosure. Page applications additionally require the current published declaration; drafts and publication/declaration mismatches are omitted. Fixed parameters, input defaults and producer data are excluded.

The restricted Pages UI uses this directory and the existing declared action endpoints. Published actions include their `publication` in the request. Confirmation remains in host chrome, scalar inputs are collected locally, and accepted runs poll their own private receipt. Unknown submission retries retain the same body and idempotency key; each result fetch checks current authority, and a rejected poll clears the displayed run. Generic application assets and shared WebSocket data are not enabled by this directory.

Restricted Page catalog actions expose `intent_hash`, binding the exact declared action, publication and complete frozen execution graph/provider slots. Restricted Page dispatch requires `expected_intent_hash`; missing hashes return 400 and changed intent is denied before admission. Trusted dispatch retains its existing payload contract. UI retries preserve the same hash, body and idempotency key.

Admission receipts use `SCHEDULED` or replayed `DEDUPED`; both require private-result polling. Live result states are lowercase `pending`, `running`, `completed`, `failed` or `needs_reconciliation`. Forms remain active through admission and running states, and stop polling only on terminal results or authorization failure.

## Common dispatch ownership

Acceptance commits the private domain row and content-free shared scheduling
identity together. Domain IDs and private authority remain host-selected.
Shared agent/server capacity uses the existing serial-agent limits. Wake hints
are post-commit optimizations with a two-second recovery poll; there is no
private 250ms idle claim loop. Recovery also follows the shared lease sweep.

Every graph authority root binds to a common run ID and generation. Current
lease, generation and state fence descendants at authority resolution, graph
boundaries and completion commit. Backup writers and queue holds remain in force.
Unexpired attempts are preserved. Expired claims without start intent return to
the queue; expired start intents require reconciliation. Legacy running jobs
are adopted into reconciliation and their old executable handles are retired.
Paid or externally effective graphs are never classified as automatically
retryable merely because execution returned an error.

A captured graph result is released only after shared dispatch success. A
container stop that cannot be independently confirmed retains capacity and
withholds outputs. Currently authorized private receipts show
`needs_reconciliation` without outputs. Trusted workspace owners/admins may
inspect content-free scheduling facts and use existing explicit Work resolution
with current generation, stop evidence and a reason. Other members cannot list,
read, cancel, replay or resolve these records through generic Work routes.
Private replay always requires a new authorized routine or Page admission;
resolving to success requires an already captured private completion.

A graph failure with confirmed physical cleanup is terminal `failed`, releasing
shared agent capacity without retrying the graph or asserting that no paid
request or external effect occurred. Failure before graph execution is likewise
terminal. Failed physical cleanup and recovered start intents retain
`needs_reconciliation` and their capacity hold until explicitly resolved.
