# Chat sidebar navigation on dev2

The sidebar has three independent destinations: People, agent sessions, and
Team Spaces. The Activity rows (All, Direct, Routines, Issues) classify only
agent sessions. Choosing Direct or Routines must not hide People or Team Spaces.

The search box matches visible people, agent names and session titles, and
workspace channel or group titles. Starting a search moves Activity to All so
routine and issue sessions can be found without first guessing their category.
The Filter popover can narrow to Agent sessions, People, or Team Spaces. Its
unread, running, and named-agent facets apply only to agent sessions; picking
one automatically shows that section. Clicking the selected named agent again
clears that facet and restores all sections when no other agent facet remains.

All in Activity clears the sidebar search and filters. Clicking an already
active Direct, Routines, or Issues row returns to All. Clicking the active
agent's row again collapses that agent's session list while keeping the open
chat intact; the row loses its blue highlight until reopened. The separate
chevron has the same fold behavior. Section and agent folds remain animated.

Team Spaces retain their existing workspace conversation permissions and
loading behavior. This change does not change backend chat kinds or access.
