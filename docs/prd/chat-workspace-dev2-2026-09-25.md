# Chat workspace UI — dev2 handoff (2026-09-25)

The approved layout is captured in [the interactive wireframe](wireframes/chat-sidebar-unified-2026-09.html). This change implements its navigation and the two distinct reading modes on the existing Chat route.

- The Chat subbar follows the Issues and Routines pattern. The left sidebar has collapsible Activity, People, Agents, and Team Spaces sections, search and filters, and a New chat menu.
- The right agent context follows the selected agent and exposes Files, Artifacts, and Work. The old Team destination and the Crew/Workspace file scopes are absent from this chat panel. Detailed agent configuration links to the agent card.
- Files keeps the agent tree on the right. Selecting a file collapses the left chat tree and opens a main reading workspace. Text/code is read-only until Edit, saving uses the existing scoped agent file API and role check, and PDF/images use the existing safe preview. The optional Chat alongside control returns conversation to a narrow column. Closing the file restores the prior left sidebar state.
- Artifacts opens an inline panel beside the chat. It polls the selected agent file every five seconds while Follow is active, increments its revision only when bytes change, and supports Pause. PDF/images, sandboxed HTML, and CSV/TSV have previews. XLS/XLSX offer download until a workbook renderer is available.
- Work displays issues assigned to the agent and routines authored by the agent, with links to their main surfaces. It does not duplicate configuration or credential management.

## Data and access limits

The current API has no artifact creation-provenance index, so the Artifacts tab lists previewable files in the selected agent's file namespace. Its label states that scope rather than claiming every file was created by the agent. The routines list is a recent page of workspace-visible routines filtered by `author_agent_id`; it is not a complete assignment history.

The Files and Artifacts UI uses agent-scoped file endpoints and no longer offers navigation into Crew or Workspace file trees from Chat. Those endpoints currently authorize workspace roles, not a future per-agent sharing policy. A client invited to talk to one agent is **not** thereby restricted to only that agent's files by this UI change; per-agent RBAC requires a separate server-side contract and enforcement before such sharing can be offered safely.
