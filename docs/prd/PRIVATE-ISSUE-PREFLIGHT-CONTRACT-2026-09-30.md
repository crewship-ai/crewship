# Private issue preflight

This is the first positive F producer for restricted actors, an explicit request
to run a declared private routine on a project-backed assigned TODO issue. It
does not install a scheduler or invoke the legacy assignment journal.

`POST /api/v1/workspaces/{workspaceId}/issues/{issueId}/private-preflight` accepts
only `agent_id` and `routine_slug`. The authenticated member must retain exact
project.read, target agent.run and the ordinary routine.run role/capability.
Projectless issues, human work, blocked issues and paused/terminal projects deny.
The host selects the current title and brief as typed user inputs to the routine's
declared `task` string input. Additional project.read authority follows every
fresh execution step; unclassified adapters deny.

`restrictedpreflight.CheckSource` must be installed on the private workflow
service before its queue starts. Its immutable origin binding checks project,
assignment, title/brief, status and work/brief revisions at enqueue, dispatch and
delivery. Changing the source or project revokes the durable origin. Current
grant checks remain mandatory; regrant cannot recover a revoked origin.

The writer transaction rechecks the source, existing legacy work, private agent
capacity and conservative budgets (including pending debits from older windows),
then inserts both source reservation and private job atomically. An enabled
positive workspace hard cap is required. This cheap check does not replace the
provider's mandatory priced reservation. Active private issue jobs fence new
active legacy assignments and issue executions while allowing terminal history.

Deduplication binds issue content/revisions, recipe, execution profile, workspace,
principal and membership revision. Only the same current actor can retrieve a
matching private receipt. Other actors see generic busy responses. A completed
job may be followed by a new source revision; failed/canceled jobs remain held
even after edits because their external effects may be unknown. There is no
automatic retry or implicit reconciliation endpoint in this slice.

Source reservations are excluded from backups as runtime authority. Receipt,
context and results remain private and do not enter the shared journal or WS
activity stream. No new timer is added; the existing private queue executes its
durable jobs. Ordinary assigned-issue query preflight remains a boolean signal,
and now excludes active private reservations.
