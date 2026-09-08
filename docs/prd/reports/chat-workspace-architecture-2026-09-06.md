# Chat workspace architecture audit — 2026-09-06

Read-only findings reviewed on Dev2; bounded Team repair implemented alongside this report. Main baseline for implementation: `e7eb4361`. This report describes code evidence, not a live multi-user acceptance test.

## Existing foundations

- `internal/database/migrate_consts_v118_group_chat.go`: human `chat_participants`, `chats.visibility`, message `author_user_id`.
- `internal/api/chat_participants.go`: participant list/add/remove; creator or workspace OWNER/ADMIN manages roster; adding promotes visibility to group. No participant management UI currently calls this API.
- `internal/chatbridge/bridge.go`, `mention.go`: group human messages persist and broadcast without running the attached agent unless its slug is mentioned. Tests cover this no-reply path.
- `internal/api/agent_chats.go`: per-user unread cursors and mark-read clears reply inbox item.
- `internal/chatnotify/notify.go`: persisted assistant replies fan out to creator and participants, deduplicated per user/chat; scrubbed previews, subscription/read-cursor suppression.
- `docs/prd/chat-as-a-primary-surface.md` §7: group threads are an intended extension; only mentioned agents should wake, and joining agents must appear as visible events because they gain history access.

## Contracts still missing

**Private is not an access boundary today.** `internal/ws/channel_auth.go:isSessionOwner` checks workspace membership, not ownership/participants. Participant reads also allow workspace members. `ListChats` is agent/workspace scoped. Do not present existing private visibility as a promise of private human DMs. A future shared access predicate must cover history, search, WS subscribe/send/resume, attachments, exports and notifications.

**An agent still owns every chat.** The original chats schema has a single NOT NULL agent_id and cascading deletion. `conversation_messages.agent_id` is NOT NULL too. Human-only conversations and several independent agent authors require migration plus resolver/API changes, not just roster controls. Never alter shipped migration constants.

**Human offline delivery is absent.** The no-mention branch persists/broadcasts/done but invokes no inbox notifier; the notifier interface is assistant-only. It also resolves agent configuration and checks pending hire before reaching the human-only branch, coupling human delivery to agent health.

**Mentions are text, not actor references.** `components/features/chat/chat-panel.tsx` creates human mention slugs from display names. There is no stable human mention ID or human notification dispatch. This repair distinguishes human/agent icons and enables mobile autocomplete, but does not change that backend identity contract. Group UI still uses participant-name count while the backend retains group visibility after participant removal.

**Current Team means peer requests.** `right-panel-tabs/team-tab.tsx` previously rendered only agent-to-agent `peer_conversations`, with no human roster. It filtered after the API's default 50-row crew page, producing misleading emptiness when other agents dominated recent activity. It had no refresh/retry.

## Bounded improvement in this change

Team displays actual crew agents and assigned human crew members using existing workspace-authorized APIs, distinct from an explicitly labelled Agent collaboration history. These are crew members, not claimed conversation participants. Agent links open existing agent chat routes. Failed requests show retry; refresh reloads current data. Agent requests filter by `agent_id` before SQL pagination, with stable ordering and newer/older controls. Workspace and crew predicates remain mandatory. Agent roster is bounded to 500 rows and explicitly indicates that ceiling when reached.

This does not create group conversations, change privacy, alter inbox behavior or automate agent invitations. Team uses workspace-keyed React Query caching and existing realtime events for peer requests, crew changes and agent lifecycle/status changes; Refresh remains available. Independent section errors preserve the other successful sections, including when human roster access fails. Full questions and answers can be expanded.

## Incremental target

1. Stabilize direct session creation and navigation; keep old `/chat/:agent?session=:id` deep links.
2. Separate conversation type (`agent_direct`, `human_direct`, `group`, `channel`) from access scope (`workspace`, `participants`) and agent response policy. Preserve old rows and migrate incrementally. Add typed actor membership or separate human/agent rosters with stable IDs and explicit author attribution.
3. Ship one workspace/crew shared channel with human transport independent of agents. Persist message first and then optionally enqueue agent work. Display roster and join/leave events; clearly describe whether a newly joined agent receives earlier history.
4. Store structured mention targets. Validate access and participant membership server-side. Dispatch idempotently per `(message_id, agent_id)` through existing queue, budget, approval and per-agent execution locks; show pending/running/failed reply state. Agent-authored mentions must not start an unbounded agent loop.
5. Generalize inbox events for human messages, agent replies and mentions. Respect author, actual access, notification preferences, mute, read cursor and presence. Stable message deep links and replay/dedupe are part of delivery. Reconsider suppressing every group response notification to the requesting human: they may have left before completion.
6. Introduce private human DMs only once consistent ACL applies across every read/write surface, including exported files and revoked WS sessions. Existing workspace collaboration can precede this.

## Acceptance evidence required before group launch

- Two humans share messages and attribution, including simultaneous sends, reconnect/replay and refresh.
- Offline recipient gets one accurate inbox entry; opening/reading clears it without resurrection races.
- No agent mention means no execution or runtime dependency; one mention wakes only that agent once.
- Cross-workspace and nonparticipant reads, search, files and WS fail closed according to the selected access scope; removal revokes ongoing access.
- Agent deletion does not remove a human group's durable history.
- Existing direct sessions and notification links still work.

Open product decision: are human DM/group messages intended to be private from other workspace members, or workspace-readable collaboration? Current implementation provides the latter access boundary regardless of private/group labels.
