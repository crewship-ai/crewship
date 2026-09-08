# Team Chat on Dev2 — 2026-09-07

Implements [the Team Chat PRD](../chat-team-workspace.md) on top of the existing
[unified Chat](../unified-chat.md). Working branch `fix/chat-workspace-foundations`,
base `56969d7e`; changes are still uncommitted. This report describes the deployed
working tree, not a merged release or a claim that Dev2 equals latest upstream main.

## Delivered

- Shared human/profile and stored agent portraits, deterministic colored initials,
  room icons, caller-relative DM titles, date separators, compact adjacent author
  groups and the existing safe Markdown renderer. No fake presence indicators.
- Two distinct demo human accounts (Klára and Tomáš), separate authenticated
  messages and private greetings, plus a persistent team planning channel.
- Creator-controlled issue/routine activity subscriptions from the durable journal.
  Trusted Crewship cards contain work links, use normal inbox/read/mute delivery,
  and do not mention or start agents. Restart/replay and backup contracts are
  described in [the activity report](chat-channel-activity-2026-09-07.md).
- Explicitly mentioned agents receive a timestamped, bounded work snapshot scoped
  to this workspace, prioritizing named issue identifiers. Routine data excludes
  private/ephemeral routines. The snapshot is not an unlimited live search.

## Real Dev2 verification

[Open the team demo](https://crewship-dev2.unifylab.cz/chat?conversation=cmtr5ax63f55c237429b3098e46bb7b9e&workspace_id=cmtplws8h000276fa560f).

The live CLI scenario passed six checks: creator/member ACL, actual issue event,
actual routine completion, no system-triggered agent jobs, real Mařena answer and
idempotent mention retry. [Machine-readable evidence](chat-team-live-dev2-2026-09-07.json)
and [reproduction instructions](../../../e2e/chat-team-live.md).

Demo issue **COP-1** remains unassigned in **TODO**. The manual routine
`chat-team-demo-check` completed with `CREWSHIP_TEAM_ROUTINE_OK`, zero model cost.
Mařena answered **COP-1 / TODO** with its correct workspace-qualified issue link;
the status question did not change the issue. Retrying reused one completed job
and one answer. The two colleague messages were authored using distinct actual
accounts, not fabricated authors on owner messages. Account credentials are kept
outside Git in `/srv/crewship/.dev2-chat-demo/` with restricted permissions.

One initial demo routine execution failed because the supplied sample used an
unsupported transform expression. Its real failure card remains visible. The
sample was corrected to the supported identity expression `.` and the subsequent
run completed. Routine CLI save/run also exposed existing text output in JSON
mode; the harness now explicitly handles those commands and validates persisted
results. Neither initial failure is counted as a passing run.

Dev2 was reloaded through `crewship-ws@2`; migration `20260907111020` is applied,
with 279 applied migrations and none outstanding. The normal pre-migration backup
`crewship.db.pre-migrate-v20260907085939-to-v20260907111020-20260907T113011Z.bak`
exists (17,309,696 bytes). After the final UI reload, the live CLI scenario passed
again using its original issue, run and completed agent job. Public HTTPS `/api/health` returned `status: ok`.

## Browser evidence

Normal public Dev2 login as Klára verified human initials, the participant roster,
trusted Crewship cards and the real agent answer, with no browser runtime errors
or mobile horizontal overflow. The final recheck clicked the actual COP-1 link
in the same tab without an external-site prompt and verified Mařena’s avatar in
the mention picker. Screenshots: [desktop](assets/chat-team-dev2/desktop.png),
[participants](assets/chat-team-dev2/desktop-people.png),
[mobile channel](assets/chat-team-dev2/mobile.png),
[mobile DM](assets/chat-team-dev2/mobile-direct.png).

## Automated verification

- 642 frontend tests across 70 files passed during development; the final focused
  conversation suite passed 34 tests after the last UI changes. These overlap.
- Final isolated browser suite: **32 scenarios passed**, no JavaScript errors,
  including original new-session/DM/group/mention/read/mute flows, profile identity,
  activity persistence and ACLs, Markdown and mobile/desktop message geometry.
  Visual inspection found and fixed a renderer height causing mention metadata
  to overlap the next message; final screenshots no longer overlap.
- Full frontend lint: zero errors, 32 existing warnings. Production `pnpm build`
  passed. `go vet ./...` passed. Legacy migration immutability lint passed.
- Focused real IssueHandler/journal and pipeline producer/projection integration,
  store privacy/replay, migrated backup restore/fork and actual-schema work-context
  tests passed. The new migration was exercised against the live database as well.
- Final focused race-detector run passed for activity projection, trusted origin and
  work-context preparation (`internal/groupchat` and `internal/api`).
- Full `go test ./... -count=1 -timeout=30m` completed: all application, API,
  database and other code packages passed. Its only failure was
  `scripts/docs-inventory`: the OpenAPI documentation still quoted 611 operations
  instead of 613 after adding activity endpoints. Updated the documentation to
  the generated counts, then `go test ./scripts/docs-inventory -count=1` passed.
  The final verification is this full run plus the targeted documentation recheck;
  the full 20-minute suite was not repeated for a prose-only correction.

## Boundaries

This is a foundation for team communication, not feature parity with Slack.
Private DMs/groups remain human-only because agent execution records are workspace
visible. Agent collaboration and automated activity belong to workspace channels.
Threads, reactions, shared-room file workflows, retrospective transcript summaries,
per-project activity routing, retention/SSO and enterprise capacity acceptance are
not included. SQLite remains the current storage; these results do not establish
an SLA for 100 concurrent corporate users or validate a multi-node deployment.
