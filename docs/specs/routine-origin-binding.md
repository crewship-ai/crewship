# Routine chat authority bindings

A routine created from a verified human chat retains that chat's authority.
Revoking its author or deleting the source chat makes future work fail closed.
A normal public edit does not erase this binding. A fresh verified chat save
can rebind the current head without altering immutable version history.

Unpinned schedules without their own requesting-chat origin resolve the current
head's binding at dispatch. Pinned schedules retain the selected version's
binding. Schedules explicitly created by a chat retain their own origin.

## Operator recovery

OWNER/ADMIN operators can inspect and explicitly clear bindings using:

- `GET /api/v1/workspaces/{workspaceId}/pipelines/{slug}/origin-binding`
- `POST /api/v1/workspaces/{workspaceId}/pipelines/{slug}/origin-binding`

Both require a real user session or an unscoped user CLI token. Agent/internal
credentials, scoped tokens and restricted accounts cannot use this surface.
The GET returns `pipeline_id`, nullable `origin_id`, nullable `draft`
(`id`, `revision`, nullable `origin_id`) and `schedules` containing `id`, nullable
`origin_id`, nullable `pinned_version` and nullable `version_origin_id`. These opaque binding identifiers are available
only on this operator surface, not on ordinary routine responses.

POST requires `action: "clear"`, an operator `reason`, and `expected_origin_id`
matching the current head exactly; explicit JSON `null` means unbound. Optional
`schedules` lists at most 100 unique objects, each with its own `id` and
`expected_origin_id`. Every selected schedule must belong to this routine.
A stale head or schedule yields `409 origin_binding_conflict`; nothing changes.
A bound draft additionally requires `draft: {id, expected_revision, expected_origin_id}`
matching its current binding exactly. Omitting this review returns
`409 draft_binding_review_required`; mismatches return `409 draft_binding_conflict`.

The clear and user-attributed audit record commit in one transaction. It clears
only the current head, an explicitly reviewed draft and enumerated schedules, restoring the existing standing
routine execution policy. Immutable versions and already pending/running work
keep their original authority. Selecting a pinned schedule whose immutable version retains an origin returns
`409 pinned_version_authority_immutable` and clears nothing. The version must be
changed deliberately through ordinary schedule editing; this API never erases
immutable version authority. A cleared draft gets a new revision, so publication
requests prepared before the clear conflict instead of restoring its old origin. Further schedules are unchanged. This endpoint
never accepts a replacement receipt or a caller-selected source run.

```bash
crewship routine origin-binding get daily-report
crewship routine origin-binding clear daily-report --expected-origin ORIGIN_ID --reason "Operator reviewed standing automation"
crewship routine origin-binding clear daily-report --expected-origin null --schedule SCHEDULE_ID=ORIGIN_ID --reason "Reviewed explicit schedule grant"
crewship routine origin-binding clear daily-report --expected-origin ORIGIN_ID --draft DRAFT_ID=REVISION=ORIGIN_ID --reason "Reviewed draft and standing grant"
```

## Transport and child runs

Sidecar routine requests derive source identity from the run bearer token.
Schedule creation carries that authenticated token as an internal proof; the
backend verifies workspace, crew, agent and run against the source fields.
Unicode/case-fold aliases of dispatcher-owned JSON fields cannot override this
identity. Legacy crews without run tokens retain their existing issue MCP
behavior; they do not persist chat operation keys without a durable run scope.

Routine agent steps admit a distinct durable `ROUTINE` child run before agent
execution. Admission failure fails the step. The admitted run participates in
agent status, run lists and journal broadcasts, and supplies the child's run
credential and authority. Confirmed completion records a terminal event;
detached work stays active for reconciliation.
