# Human direct messages — 2026-09-07

Implementation follows [workspace-conversations phase 2](../workspace-conversations.md).
Baseline remains `56969d7e` on `fix/chat-workspace-foundations`, preserving the
prior chat changes and user WIP. Status: implementation validated and deployed to Dev2 on 2026-09-07.

## Product behavior

A colleague picker opens a private human conversation. The same unordered pair
in one workspace reopens one persisted history; opposite-side concurrent first
opens must not create duplicates. Existing two-person groups remain ordinary
groups. `kind` remains `group`, with an explicit `is_direct` subtype marker.

Direct participants are fixed, and agents cannot join. Workspace access is still
required on every request. Deleting a participant must not transform the history
into a group whose creator can add somebody else. Backup/fork must preserve
identity references and canonical pair order after remapping IDs.

The direct creation route accepts only the target user ID, deriving caller and
workspace from authenticated context. The response is 201 for creation or 200
for reuse, with read/mute/unread fields scoped to the requesting user.

## Verification

- Full groupchat store suite passed, including 20 concurrent reverse-order opens
  yielding one created conversation, workspace isolation and fixed membership
  after peer deletion. Direct HTTP authorization/body/reuse tests passed.
- Backup tests passed for direct-pair fork and email identity reconciliation,
  including reversed canonical ID order and a deleted peer's surviving history.
  Registration/scope/every-table backup guards also passed.
- OpenAPI generator and reference-count tests passed: the new route has a strict
  named request and 200/201 response contract; Conversation includes `is_direct`.
- Production frontend build, 38 conversation/realtime tests and ESLint passed
  (zero errors; the same 32 existing warnings elsewhere).
- The expanded isolated browser script passed all 19 scenarios, preserving the
  earlier group/channel cases and adding colleague discovery, reverse opening
  with shared history, unrelated-member denial, fixed People controls and mobile
  reopening. No uncaught JavaScript or dropped realtime errors. Screenshot waits
  were then adjusted to let the closing dialog animation finish.
- Direct-message race tests and final `go vet ./...` passed. Migration
  immutability and repository invariants passed. Full `go test ./... -count=1 -timeout=30m` passed, including the complete
  API, backup and database suites.
No paid model execution is needed for this phase.

## Dev2 deployment

`sudo systemctl reload crewship-ws@2` completed successfully. Both the Go API
and Next.js processes are running. The automatic pre-migration snapshot
`crewship.db.pre-migrate-v20260906214858-to-v20260907085939-20260907T092104Z.bak`
was created (16,338,944 bytes). Local migration-status reports version
`20260907085939_workspace_direct_conversations`, 278 applied migrations and
no outstanding work.

Post-reload health is OK, the public `/conversations` page responds 200 and an
unauthenticated public POST to the direct endpoint is rejected with 401. Live
OpenAPI exactly matches the tested working tree. Authenticated behavior was
verified in the isolated three-user browser fixture; no paid model call or
fabricated live login was used for deployment checks.
