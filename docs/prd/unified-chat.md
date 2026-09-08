# Unified Chat — 2026-09-07

Personal audible alerts are specified in [Chat and Inbox sounds](chat-notification-sounds.md).

Owner direction: agent sessions, human DMs, private human groups and mixed
workspace channels belong in the existing **Chat** tab. A separate
`Conversations` product surface is not the intended information architecture.
This extends [workspace conversations](workspace-conversations.md), preserving
its server-side access, identity, retry, inbox and agent-dispatch contracts.

## Design

[Interactive wireframe](wireframes/chat-unified.html) is the earlier standalone proposal
with sample data, clickable room types, draft switching, mock send and mobile
layout. It is not a screenshot of a live account or a promise of unimplemented
permissions. The implementation uses the existing Crewship components.

One left sidebar navigates agent sessions, people and groups/channels. Its common
New chat menu offers Agent, Person, Group and Workspace channel. Facets/search
narrow the loaded navigation without granting or implying access. Existing
Routines/Issues session scopes stay available. Main content changes in place,
with one composer belonging to the selected conversation. The right context
shows actual participants for human rooms and existing Files/Team for agents.

A mixed workspace channel contains humans and explicitly joined agents. Agent
execution still requires a structured mention. Private DMs/groups remain human
only because legacy agent execution records are workspace-readable. The UI must
state this boundary rather than making a misleading private-agent promise.

Buzz's official [README](https://github.com/block/buzz) and
[VISION](https://github.com/block/buzz/blob/main/VISION.md) describe shared human
and agent rooms. Our design inference is to adopt common navigation and visible
participation while using Crewship's existing queue, inbox and permissions.
This does not introduce Nostr, federation or voice, nor certify Buzz's runtime.

## Routing and compatibility

- Canonical human room: `/chat?conversation=<id>`.
- Existing agent deep link: `/chat/<slug>?session=<id>` remains functional.
- `/conversations?...` remains an invisible compatibility redirect to `/chat`,
  preserving conversation and workspace query parameters and using replace
  history so Back does not trap the user in a redirect loop.
- Inbox and fork-restored notifications produce canonical Chat links. Old
  notifications retain working compatibility paths. Internal API model names
  and `/api/v1/conversations` are unchanged.
- Explicit human query selection takes precedence over stale agent state.
  Selecting an agent clears human selection. Back/Forward restores selection,
  and each actor/workspace/thread retains its own draft.
- Opening a page or an agent draft must not create a session or execute a model.
  New agent session starts on the existing first-send path.

## Acceptance matrix

| Scenario | Expected result |
|---|---|
| Existing Mařena/Ava deep link | Open named session/history; no creation on arrival |
| Human DM from New chat | Same pair history, correct selection in shared sidebar |
| Agent → human → Back/Forward | URL, highlight, transcript and draft agree |
| New agent session | New draft, previous history/input absent; no POST until send |
| Old human bookmark/inbox | Canonical Chat page with preserved room/query scope |
| Workspace/account change | No stale private transcript, draft or late navigation |
| Group/channel creation | Correct access explanation and one shared sidebar |
| Two humans + two joined agents | Structured mentions queue exactly one job per target; replies do not loop |
| Removed/nonmember participant | API denied; cached private history hidden |
| Mobile navigation | Visible list/back and New chat; no overlapping composer/context |
| Backup identity reconciliation | Existing inbox aggregate follows reconciled participant ID |
| Keyboard and errors | Search/new menus usable; recoverable requests offer retry |

Record the measured test results and live deployment separately from this
contract. Do not infer that one isolated browser fixture read the user's live
session or exercised paid model execution.

Measured results and Dev2 deployment: [2026-09-07 report](reports/chat-unification-2026-09-07.md).

CLI parity and real multi-user Dev2 acceptance: [Chat CLI](../../e2e/chat-cli-live.md).
Workspace rooms live under `crewship chat room`; existing agent sessions retain
their commands. The CLI exercises the same workspace permissions, read cursors,
idempotency keys and structured agent mentions as the shared Chat interface.

Next owner-directed implementation: [team identity, work context and activity](chat-team-workspace.md).

## Sidebar and continuation refinement — 2026-09-07

Replace the competing top facet grid and split conversation lists with one New
chat entry, common search, and collapsible People, Agents and Team spaces
sections. Show each agent once, with expandable session history and an explicit
New session action. Routines/issues are secondary agent-history scopes; opening
or retrying unavailable history must never silently create a session. Preserve
deep links, Back/Forward, independent drafts and scoped pagination.

From a direct message, Add people creates a new private group containing the
original pair plus selected colleagues. The original DM remains canonical and
private, and its history is not copied. From a private DM/group, Invite agent
explicitly creates a workspace-visible channel with current participants and
the chosen agent. Public visibility is stated before submission. Private history
remains private; agent membership alone never starts a run. Existing channels
continue to offer their normal agent membership control.

Continuation creation is atomic and durable-idempotent using a client operation
ID. Repeating an identical request opens the same target; a changed request using
that ID conflicts. The UI preserves an uncertain attempt across dialog dismissal,
locks its inputs until resolution and does not navigate after cancellation.
Membership and workspace checks apply again on each retry. This is a new room,
not an in-place broadening of access to an existing private conversation.

Current implementation and acceptance: [sidebar/continuation report](reports/chat-navigation-dev2-2026-09-07.md).
