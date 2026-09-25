# Chat navigation on dev2 — September 25 refinement

The existing main rail, Chat subbar, transcript and right Artifacts/Work rail
remain. The Chat subbar no longer duplicates the global Activity link.

The secondary sidebar contains Favorites, Team Spaces, People and Agents.
Agent rows carry an AI label. One agent's history is expanded at a time
(search results may reveal multiple matching agents). Selecting the currently
expanded agent collapses it without closing the conversation. New session
remains an explicit draft action inside that history; opening a row never
starts a model. Existing deep links and independent human/agent drafts remain.

Favorites use stars to move agents and existing human/channel conversations
to a common section without duplicating them below. IDs are stored in this
browser under the user/workspace scope, not synchronized between devices.
Names/content are not persisted. Pinned rooms beyond the initial page are
resolved through the ordinary authorized conversation detail endpoint.
Favorites do not grant access and still obey search and filters.

Direct agent conversations are the default. Filter contains Show (all,
agent sessions, people, team spaces), agent session kind (Direct / All /
Routines / Issues), crew, unread agent sessions and running agents. Crew
retains its real icon/colour and All crews choice. Crew alone does not hide
people or channels. Routines/Issues choose agent-only display; Show → All
conversations can include people/channels alongside that agent history.
Clear resets Direct, all sections, all crews and status filters. Active filters
remain visible outside the popover. There is no separate Agent activity block.
The existing Routines kind partition also includes scheduled/webhook and
agent-delegation sessions; origin labels distinguish these cases.

Search matches names and session titles, not message bodies. Workspace room
and agent session title matching now happens on the server before pagination.
The sidebar searches every loaded roster agent with at most four concurrent
requests (rather than only the initial twelve-agent, ten-session sample).
Additional matches are explicitly paginated; errors/loading are visible.
Searching preserves the selected session-kind scope. The shared room pager
remains outside the sections so zero matches cannot hide the way to more data.

## Work and provenance

Routine steps continue creating normal execution chats; no memory or dispatch
semantics change. New routine chats record nullable pipeline_run_id and
pipeline_step_id. The internal create path validates that the run, routine and
agent belong to the same workspace before recording them. A nullable foreign
key to pipeline_runs lets backup remapping retain the relationship and
clearing a deleted run removes the dangling reference.

Agent chat lists support opt-in source=1, exact chat_id/routine_id narrowing,
and q title search. Source enrichment uses an exact workspace-scoped join,
not title parsing. Issue sources use their existing mission/chat identity.
Old routine sessions without a recorded link retain their origin label but
have no fabricated source link; no backfill based on names/timestamps runs.

The transcript header can open the recorded source in Work. That source is
shown separately from the agent's assigned issues/authored routines: an agent
executing a step need not be the routine author. Source links open the actual
issue or routine run. Expand Conversations in a Work card to open explicitly
linked transcripts. Work is a work-resource view; sidebar filters select
conversation records. Creating issues/routines via agent tools remains the
existing chat flow, not a frontend-simulated creation action.

Artifacts remain a workspace-authorized view of agent-namespaced files, not
per-agent RBAC or proof of authorship. No new permission boundary is implied.

## Verification

Unit/integration tests cover favorites isolation and deduplication, preserved
drafts/navigation, search beyond the initial agent cap, source links after
renaming, cross-workspace rejection and private-room search exclusion. Runtime
verification and full-suite results are recorded in the implementation handoff.
