# Client dashboard — 2026-09-06

Scope: main dashboard on dev1, issue #2433. Reuses the existing theme,
SubBar, DashboardCard, AgentAvatar, CrewIcon, StatusPill and entityHref.

Order: attention → review/results and routine activity → crews → one agent
reporting section and Pages → collapsed system details. Real issue owners
and identifiers come from `/api/v1/issues`; separate REVIEW and DONE/COMPLETED
queries prevent recent completions from displacing all review work. Each
query is capped at four; the overview displays four review issues, two done
issues and up to two successful routines from the existing recent-run feed.
“All issues” and Activity lead to the complete lists. No demo output or
simulated completion is used in the production component.

Crew icons and colours come from crew configuration; agent faces use the
shared persisted-avatar component. Routine results without an invoking agent
use the routine icon rather than an invented person. Waiting/paused/queued
routines are shown separately from running routines. Fetch errors retain
available results, explain the partial state and offer retry. The reporting
window applies to the agent metrics, not to the results list.

Hover changes colour/border without moving crew cards. Existing section
entrances respect reduced motion; dashboard stagger is capped at 90 ms.
The live connection label is static. Routine details and issue review use
existing product routes; this change does not redesign their detail panels.
