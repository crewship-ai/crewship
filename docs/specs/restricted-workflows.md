# Restricted private workflows

Restricted members invoke manual routines through `POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run`. Existing role or `routine.run` capability checks remain necessary. Each resolved leaf additionally requires current target-agent run authority; cross-agent delegation requires explicit delegate authority. Unsupported scripts, HTTP steps, cycles, cross-workspace targets and unavailable execution profiles fail before enqueue.

The restricted JSON body accepts only `inputs`, `expected_definition_hash`, `expected_execution_hash` and `delay_seconds` (0–86400). The hashes check the server-resolved recipe and complete execution graph; they grant no authority. `Idempotency-Key` allows retries of the same admission. Admission returns HTTP 202 with `run_id`, `chat_id`, `status`, `restricted: true` and `status_url`.

`GET /api/v1/workspaces/{workspaceId}/restricted-routines` returns an array of authorized routines containing only `slug`, `name`, `definition_hash`, `execution_hash` and input descriptors (`name`, scalar `type`, `required`). `X-Total-Count` counts this authorized projection. Prompts, input defaults, provider credentials, graph bodies and denied routines are excluded.

`GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs` lists the actor's currently authorized private runs. `GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}` returns `run_id`, `status`, `step_outputs` and `created_at`. Outputs are released only after completion and current provenance checks. Foreign, revoked and unavailable receipts return 404. Cleanup-revoked failures do not currently expose a result receipt.

Declared Page actions use the same private queue with their original Page, panel, action and publication policy. Restricted polling through `GET /api/v1/pages/{slug}/application/actions/{pendingId}` requires an exact private job/Page/actor binding and returns `pending_id`, `run_id`, `pending_status`, `run_status`, `restricted`, `routine_revision_pinned` and `step_outputs`. It never falls back to a generic pending run or shared journal for an unmatched ID.

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

The restricted Pages UI uses this directory and the existing declared action endpoints. Published actions include their `publication` in the request. Confirmation remains in host chrome, scalar inputs are collected locally, and accepted runs poll their own private receipt. Unknown submission retries retain the same body and idempotency key; revoked delivery clears the displayed result. Generic application assets and shared WebSocket data are not enabled by this directory.
