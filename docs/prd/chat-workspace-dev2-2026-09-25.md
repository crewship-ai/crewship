# Chat workspace UI — dev2 handoff (2026-09-25)

Current behavior is documented in [Chat sidebar filters](chat-sidebar-filters-dev2-2026-09.md). Earlier wireframes are design history; the current client exposes Artifacts and Work, not a Files tab.

- The main application rail and Chat sidebar stay separate. The sidebar contains Favorites, Team Spaces, People and Agents; agent rows have an AI label. Shared lists show four rows initially and an agent history shows five, with explicit expansion. Only one agent history opens at a time outside search.
- Favorites can pin existing channels, people and agents. They move into Favorites without duplicates. This preference is stored in this browser, scoped to the user/workspace; it does not sync across devices.
- Filter contains crew, conversation category, agent session kind and status. Direct is the default. Routine/issue sessions remain accessible, and their origin is visible. The separate Agent activity block and redundant Chat Activity action are removed.
- Names and session titles are searched on the server before pagination, with Unicode-aware case matching. Agent search covers the loaded roster beyond the initial twelve-agent session sample. It does not search message bodies. Additional result pages remain explicit.
- Artifacts opens beside the chat or in an expanded preview while its list stays on the right. Follow checks approximately every five seconds and can be paused. PDF/images, sandboxed HTML and CSV/TSV have previews; XLS/XLSX offer download. Internal agent/run files are excluded from the client list.
- Work shows assigned issues and authored routines. A selected session's exact issue/routine source appears separately, because executing a routine does not make the agent its author. Work cards can reveal linked conversations. Source links open the actual issue or routine run.

## Data and access limits

New routine chats record a nullable run/step association through the existing internal chat creation path. The migration adds a foreign key for correct backup remapping. Older routine chats have origin labels but no guessed source links or title-based backfill. Issue sources use the existing mission/chat identity. Dispatch and memory behavior are unchanged.

Artifacts lists selected file types in the agent namespace, not proven agent-authored files. File endpoints authorize workspace roles; this UI does not establish per-agent RBAC. Restricting a client to one agent requires separate server-side enforcement. Work's authored-routine list remains a recent workspace-visible page, not a complete execution history.

## Verification

The Chat/conversation frontend suite passed 702 tests across 80 files. After the final list compaction, focused navigation tests passed 52 tests across eight files and TypeScript passed. Lint passed with existing warnings; the production export built successfully.

Targeted Go suites passed for API provenance/search, chatbridge, pipeline, groupchat, backup and OpenAPI generation. Source tests cover renaming, cross-workspace rejection, issue identity and deleted routines; search tests cover pagination, private-room exclusion, literal percent signs and Czech Unicode case matching. The pipeline test checks run/step propagation. The full `go test ./... -count=1 -timeout=20m` run reached the package timeout in `internal/api` and `internal/database`; every other package passed. The active tests (`TestVerticalServer_ALostHintIsReplacedByPolling` and `TestCheckpointerLoopTruncatesOnATick`) both passed when run independently (4.0s and 32.5s). The full suite is not claimed green. `go vet ./...`, migration lint and repository invariants passed.

Authenticated dev2 browser smoke verified pins for an agent/channel/person, deduplication, persistence after reload, routine-only filtering, crew selection without hiding shared conversations, live search and the sidebar at 390px. No new endpoint failures or page errors occurred. No routine/model run was started by this smoke test.

Final reload used `sudo systemctl reload crewship-ws@2`; public `/health` and `/chat` returned 200. The authenticated browser smoke was repeated successfully after that reload. Changes remain on PR #2699 without merge.

## Channel details panel refinement

Conversation members and settings now open in a right-hand panel using the same background, borders and typography as Work/Artifacts. Members opens the member section; channel/group creators also get a settings control opening General. General shows name/access and personal notification settings, followed by Members, Channel agents and Workspace activity, without tabs. Existing creator-only management permissions and APIs remain unchanged.

The panel sits beside the transcript when the chat area has at least 720px available. In narrower areas it occupies the chat area; the underlying transcript is hidden from keyboard navigation while preserving its draft. Close/Escape restores focus to the opening control. No workspace backdrop or people modal remains; separate create/expand-conversation dialogs retain their existing behavior.

Validation: the 702 existing Chat/conversation tests passed, plus two new creator/member cases (focused file: 12 passed). TypeScript, lint (0 errors, 30 existing warnings), production build, go vet and the channel activity permission API test passed. Authenticated browser smoke checked desktop side-by-side layout, Members focus, Escape, draft preservation and 390px mobile without overflow or page errors. Backend code is unchanged; the previous full Go package timeout limitation above still applies.

Members styling follow-up: participant management is merged into the single Members list, with avatars, role labels and an inline remove control for eligible group members. The add-member picker also shows avatars. The duplicate Manage people section is removed. The details surface now uses the same bg-card base plus bg-accent/30 layer as Artifacts, with matching small body text and muted uppercase section labels. Conversation tests (53), TypeScript and lint passed; desktop/mobile smoke verified the single list and avatar picker.

Agent picker follow-up: the channel-agent selector uses the shared Select component with actual avatar URL/style/seed in both options and selected value. Conversation tests (53), TypeScript/lint and production build passed. Public-channel member management remains unresolved pending the user's choice of access semantics: current channel participants are all workspace members, while removal is supported only for private groups. No access rules were silently changed.
