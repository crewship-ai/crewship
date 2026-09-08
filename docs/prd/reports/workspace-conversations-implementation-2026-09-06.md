# Workspace conversations implementation — 2026-09-06

Baseline: main `56969d7e`, branch `fix/chat-workspace-foundations`.
Specification: [workspace-conversations](../workspace-conversations.md).
Existing direct chat fixes remain in this branch; user WIP in AGENTS.md/CODEX.md
is preserved. No live Dev2 restart or real model invocation was performed by
this session. See the continuation below for observed live deployment state.

## Implemented in the working tree

- `/conversations?conversation=<id>` linked from Chat, including mobile/empty
  entry. Groups invite selected workspace humans; channels inherit workspace
  access. Legacy `/chat/:agent?session=...` routes remain valid.
- Independent SQLite tables, atomic membership/message changes, monotonic
  per-conversation sequences, author-scoped retry keys, bounded message/history
  queries. A retry with a changed body or mention set returns conflict.
- Shared ACL predicates on every new store read/write; removal rechecks on the
  next request and identity-channel invalidation hides a revoked cached view.
- Human sends never provision agents. Read cursors advance explicitly, not
  because a human sent a message. Personal mute suppresses future inbox rows.
- Durable outbox projects one inbox item per recipient/conversation. Sequence
  CAS and transactional read cursors stop duplicate replay/resurrection. Inbox
  previews deliberately contain no private transcript/title/author. Links load
  through the conversation's own ACL. Realtime frames are advisory IDs only.
- Explicit agent join/leave transcript events in channels. Structured mentions
  create one durable job per message/agent. Jobs prepare ordinary queued
  assignments atomically; existing crew capacity, execution locks, credentials
  and policy machinery remain in the run path. The actual execution door
  rechecks channel access. Completion projects one attributed reply, including
  restart/retry handling. Agent-authored text never creates another mention job.
- Backup includes all six conversation tables and conversation-only identities.
  Fork restore remaps identity references, mentions and inbox links. Restore
  preserves history, ACL, mute and read cursors, but fails pending agent jobs,
  cancels their active assignments and acknowledges historical outbox events:
  restoring a backup cannot silently launch paid work or replay old alerts.
  Fresh messages deliver normally. Reverse-lookup indexes cover identity and
  workspace cleanup, including soft-deleted conversations.
- Frontend has real @ autocomplete, selectable targets, accessible keyboard
  selection, retry identity preserved across navigation, user/workspace-scoped
  drafts, last 100 history with Load older, unread state, mute and roster errors.

## Scope limits

Private groups are human-only: existing agent assignment/task/result surfaces
are workspace-visible. Silently copying a private transcript there would break
privacy. Old agent sessions also retain their pre-existing workspace access
semantics; this work does not promise private access for old `visibility` labels.

Channel agent context is bounded to the latest 100 messages at the trigger and
128 KiB. Large reply output is visibly shortened. Group file sharing, threads,
reactions, human mention-specific alerts, owner transfer/archive and corporate
SSO/retention/export policy are follow-ups. No multi-node event bus is installed.

## Validation

- Store tests use a real SQLite WAL file with a five-connection pool: 104 workspace
  members, 10 concurrent senders, 100 unique messages and 100 duplicate retries;
  all persist once, remain ordered and replay after closing/reopening the pool.
  Observed send+retry p95 varied roughly 27–89ms across runs under shared-host
  contention; this is a storage test, not an end-to-end 100-user SLA.
- Store/dispatcher tests cover rollback when outbox insertion fails, workspace
  and participant revocation, mute, read-before-delivery, repeated delivery,
  channel agent join, duplicate mention jobs, remove/readd cancellation and
  requester revocation before reply.
- Assignment tests cover prepare-once/reply-once, oversized output, execution
  access gate, held/resumed agent, 100-held-job fairness and existing run paths.
- Browser acceptance: 13 checks passed against the actual static export and an
  isolated migrated API/WS server with three authenticated users. Covered
  group creation, bidirectional live messages, same-workspace nonmember denial,
  canonical inbox activity, mute, stable retry IDs, revoked cached transcript,
  open channel access, visible agent join, typed mention job creation and mobile
  send. No uncaught JavaScript or dropped realtime events. Real agent execution
  was disabled in this browser fixture; execution/projection paths have Go tests.
- Frontend: static build and TypeScript passed; 661 broader chat/inbox/frontend
  tests passed before final realtime/autocomplete refinements, then 33 scoped
  realtime/conversation tests and 7 final conversation tests passed. ESLint has
  zero errors and 32 pre-existing warnings outside this implementation.
- Full Go run identified route-role manifest, backup registration, foreign-key
  index and API-documentation contract failures. Manifest, backup and index
  fixes passed their targeted checks; the full backup suite passed (101 seconds),
  including fork/restore, and the full notification suite passed after the
  restored-inbox identity fix. All 14 new routes now have explicit API contracts (including bodyless 204
  responses and idempotent send 200/201); generator and documentation tests
  passed. Final `go vet ./...`, migration lint and repository invariants passed.
  The final API regression rerun passed. The initial full Go invocation was
  not green; all failures it reported were fixed and their affected checks
  rerun successfully, rather than rerunning unrelated passing packages.
- Race checks passed for groupchat, groupchatnotify and ws before the final
  restored-inbox identity fix; its regression and full notification suite passed
  afterward. The final running-assignment status projection test also passed.

The migration lint checks immutability of shipped SQL files; its printed
"added" count covers the legacy Go registry, not these four new SQL files.
Actual application of all four files is exercised by migrated test databases.

## Continuation — 2026-09-07

- Dev2 was already reloaded at 2026-09-06 22:30 UTC outside this session. Its
  live `/openapi.json` exactly matches the working-tree spec; `/api/health`
  reports OK and the public `/conversations` route returns HTTP 200.
- The explicit local Dev2 database migration-status command reports schema
  `20260906214858_workspace_conversation_foreign_key_indexes`, 277 applied
  migrations and no outstanding work. No extra restart was needed.
- The stored CLI login returned `session_invalid`; live authenticated message
  flows were not exercised through that expired session. Browser acceptance
  used the isolated authenticated fixture, not a fabricated live login.
- Browser acceptance is now reproducible from
  [the checked-in instructions](../../../e2e/workspace-conversations.md).
  The persisted script passed all 13 scenarios again; the fixture shut down
  cleanly and removed its temporary credentials and stop marker.
- The opt-in 100-active-client HTTP acceptance passed in 5.28 seconds with
  zero failures: ten private groups of ten and a 100-member channel, 400 new
  messages, 400 identical retries, 900 paginated history requests and 200 inbox
  aggregates. Read cursors stayed monotonic, replay did not resurrect unread
  activity, and human transport created zero agent jobs. It uses real TCP HTTP,
  JWT/session and workspace middleware, migrated WAL SQLite and pool size five.
- On this shared 12-logical-CPU host, send p50/p95/p99/max were
  58.6/170.4/221.1/421.2 ms; history-page p95 was 64.8 ms. This short acceptance
  run excludes browser load, WebSocket fanout, TLS/proxy and model/container
  execution; it is not a sustained mixed-workload SLA. Reproduction and complete
  limits are in [the load-test guide](../../../e2e/workspace-conversations-load.md).
  Measured JSON: `/tmp/crewship-chat-http-100-active-2026-09-07.json`.
- Final `go vet ./...`, browser-script syntax and `git diff --check` passed
  after the reproducibility additions. No production source behavior changed
  in this continuation; the new HTTP acceptance is opt-in.
