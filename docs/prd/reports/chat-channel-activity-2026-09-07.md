# Opt-in workspace channel activity — 2026-09-07

Implemented as a projection of the existing durable journal, not a second
execution engine. Channels can receive issue creation/update/assignment and
routine run start/completed/failed events. Routine step telemetry, comments,
chat transcripts, model outputs, notification delivery events and secrets are
not copied into cards. Cards include the current issue title or routine name alongside its identifier/slug (escaped and bounded to 200 Unicode characters), and link
back with workspace scope. Physically deleted issues, soft-deleted crews, and hidden, deleted or ephemeral
routines are excluded.

## Contract

`GET /api/v1/conversations/{conversationId}/activity` returns
`{"issues":false,"routines":false}` until explicitly enabled. Existing workspace
members may read channel settings; only the channel creator may change them.
Private groups and direct messages reject this feature. `PUT` requires both
boolean fields, rejects unknown fields, and returns the accepted settings.

CLI parity:

```sh
crewship chat room activity ROOM_ID
crewship chat room activity ROOM_ID --issues=true --routines=true
crewship chat room activity ROOM_ID --issues=false --routines=false
```

The normal CLI server/workspace/authentication options apply. Setting only one
flag is rejected rather than silently resetting the other subscription.

## Delivery and recovery

Each family has a persisted per-workspace journal sequence cursor in
`workspace_conversation_activity` (migration `20260907111020`). Enabling a family
starts at the current journal head: no surprise historical backfill, including
when reenabling after a disabled interval. Repeating an already-enabled setting
preserves its cursor and pending events.

The existing notification worker polls every second. Up to 50 source events per
channel are projected per pass, ordered by journal sequence. Each channel batch
commits message sequencing, system cards, notification outbox rows and cursors
atomically under SQLite's existing immediate transaction helper. Restart or
concurrent drain cannot duplicate cards; an outbox write failure rolls back the
cursor and messages. Existing inbox aggregation, mute/read and user-scoped
realtime invalidations apply unchanged. Projection failure is logged separately
and does not prevent normal human-message notifications from draining.

Cards carry trusted `source_kind=activity`, no human or agent author, and no structured mentions. The origin is set only by the projector and survives author deletion; a caller-controlled client ID cannot impersonate Crewship. They never
create agent jobs or invoke models. Issues use fixed event labels and canonical issue
status names from `internal/statuses`, not arbitrary journal payload text. Existing `mission.status_change`
also covers some non-status edits, so the generic card correctly says "Issue
updated" unless a known destination status is available.

Backup exports subscription rows and remaps channel references on fork. Restore
retains the row but resets both enabled flags and cursors to zero, requiring an
explicit new opt-in. It therefore cannot replay a source workspace's cursor
namespace. Generated cards' workspace-qualified links follow a fork; human
markdown is untouched.

## Verification

- Store scenarios: default off; historical exclusion; owner/member/outsider ACL;
  private group rejection; cross-workspace source denial; hidden/deleted/ephemeral
  resources omitted; concurrent replay; no agent jobs; bounded catch-up; disable/
  reenable; idempotent settings update; atomic rollback/retry.
- HTTP: defaults, authenticated member read, outsider denial, creator-only update,
  malformed body and missing authentication; route-role manifest regenerated.
- CLI: authenticated GET/PUT contracts with workspace propagation, both explicit
  toggle flags, partial-update rejection before making a request.
- Backup: actual migrated dump/restore/fork includes subscriptions, follows new
  channel IDs and restores disabled; source dump remains reusable; human text is
  not rewritten.
- OpenAPI: two added endpoints, named strict request/response schema. Generated
  specification contains 613 operations.

Live Dev2 results belong in the parent implementation report; the checks above
are isolated automated tests and should not be described as a live deployment.

Follow-up contracts use the real IssueHandler PATCH endpoint and journal writer
before projection, plus the real pipeline run completion/failure emitter.
Missions have no `deleted_at` column: issue deletion is physical, and the source
lookup joins an existing mission and a non-deleted crew. The current issue
producer adds structured optional `from`/`to` fields on status transitions; the
old payload-contract test's introduction describes an earlier version.
