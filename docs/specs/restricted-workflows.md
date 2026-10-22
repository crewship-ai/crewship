# Restricted private workflows

Status (2026-10-01): execution remains an explicit operator opt-in through immutable runtime image configuration. Manual routines, declared Page actions and assigned-issue preflight use the common `work` ledger and `dispatch.Dispatcher` for claims, capacity, leases and recovery. The isolated Docker manager still uses its own CLI transport; direct chat/CLI execution and provider lifecycle integration remain separate release work. This integration does not establish release-1.0 acceptance. Backup write barriers and post-recovery queue holds also apply to private dispatch. If a quiet window refuses runtime admission after a claim but before execution, the private job retains its authority and waits for the common dispatcher's bounded retry; graph failures are not automatically retried. A running private job holds its writer lease through completion, so a backup may time out and retry rather than snapshot active writes.

Restricted members invoke manual routines through `POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run`. An explicit root-routine run grant is required; Guests do not need a workspace-wide `routine.run` capability. Each resolved leaf additionally requires current target-agent run authority; cross-agent delegation requires explicit delegate authority. Unsupported scripts, HTTP steps, cycles, cross-workspace targets and unavailable execution profiles fail before enqueue.

The restricted JSON body accepts only `inputs`, `expected_definition_hash`, `expected_execution_hash` and `delay_seconds` (0–86400). The hashes check the server-resolved recipe and complete execution graph; they grant no authority. `Idempotency-Key` allows retries of the same admission. Admission returns HTTP 202 with `run_id`, `chat_id`, `status`, `restricted: true` and `status_url`. Missing or denied graph dependencies return the opaque 404 response; operational graph lookup failures return 503 without storage details. Unsupported declarations retain HTTP 400.

`GET /api/v1/workspaces/{workspaceId}/restricted-routines` returns an array of authorized routines containing only `slug`, `name`, `definition_hash`, `execution_hash` and input descriptors (`name`, scalar `type`, `required`). `X-Total-Count` counts this authorized projection. Prompts, input defaults, provider credentials, graph bodies and denied routines are excluded.

`GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs` lists the actor's currently authorized private runs. `GET /api/v1/workspaces/{workspaceId}/restricted-routine-runs/{runId}` returns `run_id`, `status`, `step_outputs` and `created_at`. Outputs are released only after completion and current provenance checks. Foreign, revoked and unavailable receipts return 404. Cleanup-revoked failures do not currently expose a result receipt.

Declared Page actions use the same common dispatch ownership with private domain authority with their original Page, panel, action and publication policy. Restricted polling through `GET /api/v1/pages/{slug}/application/actions/{pendingId}` requires an exact private job/Page/actor binding and returns `pending_id`, `run_id`, `pending_status`, `run_status`, `restricted`, `routine_revision_pinned` and `step_outputs`. It never falls back to a generic pending run or shared journal for an unmatched ID.

Execution is bounded to 16 agent leaves, 64 graph nodes and nesting depth four. Frozen server-selected provider slots and opaque completion proofs retain provenance across delegated calls and a return to the original agent. Revoked source authority, recipe publication or provider grants invalidate descendants. Queued work requires a live, unsuspended principal (NULL or empty suspension timestamp means active) and rechecks authority before launch and while running; uncertain interrupted execution is not automatically retried. Workspace budget reservations precede provider requests, and missing terminal usage retains the conservative debit.

These projections use private context and delivery rather than shared activity streams. The routine UI selects them using the server-derived current membership access mode. Text and native execution require an explicitly installed typed adapter for the configured profile; unsupported profiles have no trusted fallback.

## Guest-published routines

A routine whose HEAD carries a Guest publication manifest runs only through the
manual `POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/run` admission
above (`crewship routine run <slug> --wait`). Page actions, issue preflight,
schedules, webhooks, nested `call_pipeline` and resume refuse it. Admission
recompiles the exact published version: every step must be a direct `agent_run`
on the agent, crew and `responses_text`/`native_api_key` profile recorded at
publication, and both the caller's grants (root-routine Run, agent Run and
Delegate) and the publisher's frozen View/Edit/Publish and agent bindings must
be live, with the same grant, publisher membership and issuer identities and a
live trusted OWNER/ADMIN issuer. A run-only Guest can invoke another Guest's
publication within both sets of grants.

Run authority belongs to those grants and the accounts, not to a browser
session or CLI token: logging out does not stop an accepted run. Every
administrator session revocation (`POST /api/v1/admin/users/{userId}/sessions/revoke-all`,
single-session revoke and host-side `crewship admin invalidate-sessions`) also
writes a durable `users.access_revoked_at` marker in the same transaction, so a
user who had already logged out is still stopped.

At every step boundary, immediately before each provider call, ONE composite
query rechecks the whole authority of the run: the origin attempt, member
revision and account, private chat and agent profile, the caller's grants, the
exact HEAD publication and every frozen authoring/execution binding (same grant,
publisher membership and live trusted OWNER/ADMIN issuer), the revocation marker
of the caller and publisher, the current provider declaration of every recorded
agent (credential, grant, credential revision, model and profile must equal the
manifest, so a rotated credential stops the run), the frozen recipe and provider
slots, the dispatch lease and the run's parent attempt. A failed check stops the
run before the next step; no further provider request is made. Published runs do
not poll authority between boundaries; the existing lease monitor still cancels
a run whose dispatch ownership is lost, and the run's deadline cancels an
in-flight provider call. The manifest's recorded grant/key expiry is not used;
provider slots keep their existing live expiry checks.

A published run has a hard 30-minute limit from its acceptance, fixed when it
is accepted. Deferred start is refused: `delay_seconds` other than 0 returns
HTTP 422 for a published routine. Moving HEAD (republish or rollback) also stops
an older run, as a changed recipe does for legacy runs. Results stay private to
the run's caller; other Guests and the publisher receive the opaque 404. A
completed run's receipt is also hidden once its authority is later revoked;
this is accepted, the run itself is unaffected.

Acceptance appends a chained `audit.entity_created` journal event
(`action: published_run_started`) with the run, routine, version, version ID,
authority fingerprint and publisher. A stop appends `audit.entity_updated`
(`action: published_run_stopped`) in the same transaction as the failed state,
with `reason` one of `grant_revoked`, `membership_changed`, `account_suspended`,
`sessions_revoked`, `issuer_authority_lost`, `provider_changed`, `unpublished`,
`publication_changed`, `max_duration`, `authority_changed` or
`execution_failed`. Events carry no inputs or outputs. Exception: stopping
never waits for the audit writer. If the stop event cannot be appended, the
failed state still commits without it and the failure is logged; the start
event, by contrast, is required for admission.

## Authorized routine catalog

```http
GET /api/v1/workspaces/{workspaceId}/restricted-routines
```

Authentication requires the current workspace member's bearer token or session.
The request has no JSON body. The HTTP 200 response is an array of the authorized
routine metadata described above; `X-Total-Count` is the filtered count. Unavailable or unauthorized projections return 404; the authentication and
workspace middleware can reject the request before projection.

### Private execution not installed

When the server has no private execution runtime, a restricted member of the
workspace receives HTTP 503 with
`{"error": "...", "code": "restricted_runtime_unavailable"}` from this catalog,
from `GET /api/v1/workspaces/{workspaceId}/restricted-pages`, from routine run
admission and from restricted Page action dispatch. The code is a server-wide
fact and is sent only after the caller is established as a restricted member;
every access refusal stays the opaque 404 with no code. Clients show "Private
execution isn't installed on this server — ask an administrator" only for this
code; any other failure, including a 503 without it, reads as "unavailable", and
a successful empty array reads as "nothing available with your current access".

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

The CLI forwards the selected action hash with `crewship page action --expected-intent-hash <hash>`.
Use the `intent_hash` from the authorized Page catalog and preserve it across
retries; a changed declaration requires a fresh selection. `crewship routine run
<slug> --wait` polls the actor-scoped private receipt, including delayed runs.
Machine formats produce one receipt or final result document. Failed, canceled
and reconciliation-required results exit nonzero; authorization failure stops
polling without printing a cached result.

Admission receipts use `SCHEDULED` or replayed `DEDUPED`; both require private-result polling. Live result states are lowercase `pending`, `running`, `completed`, `failed`, `canceled` or `needs_reconciliation`. Forms remain active through admission and running states, and stop polling only on terminal results or authorization failure.

## Common dispatch ownership

Acceptance commits the private domain row and content-free shared scheduling
identity together. Domain IDs and private authority remain host-selected.
Shared agent/server capacity uses the existing serial-agent limits. Wake hints
are post-commit optimizations with a two-second recovery poll; there is no
private 250ms idle claim loop. Recovery also follows the shared lease sweep.
If initial dispatcher recovery fails, unclaimed receipts remain pending while
recovery retries with exponential backoff capped at 30 seconds. Cancellation
interrupts that wait; an unsafe empty-kind configuration is not retried. Work
is not claimed before the dispatcher's recovery pass succeeds.

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
