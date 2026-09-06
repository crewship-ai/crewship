# Client inbox — 2026-09-06

Issue #2435, continues dashboard PR #2434 on dev1.

- `/inbox` is the product route. Sidebar, command palette, notifications,
  credentials, crew, routine and activity links use it. `/inbox-v2` is a
  compatibility entry that replaces its URL while retaining query and hash;
  this is a client redirect compatible with Next static export.
- Left column: three clear views (Needs action / Updates / History), search,
  crew filter, then readable rows with status, title, owner and age. With no
  actionable items the initial view opens Updates. Live arrivals do not
  override the user's chosen view. Desktop collapse leaves the list available
  after resizing to a phone.
- The unselected reading pane prioritizes actual requests, recent updates and
  crew summaries. Workspace-wide notices have a static summary rather than a
  disabled crew-filter button. History includes records and archived notices.
- Details show the message before action controls and supporting technical
  context. Decisions retain permission checks, four-eyes notices, evidence,
  source-specific actions and receipts. Technical context and run steps are
  expandable. Selecting another item resets local detail form/body state.
- Avatars, crew icons and routine identity reuse the product components.
  Issue notifications are correlated by mission ID to real issue assignees;
  a mission's crew lead is never substituted for the assignee. A bounded
  lookup of the 100 most recently updated issues supplies this metadata;
  older/unavailable owners retain a crew identity, not an invented author.
  Routine names/icons and their author crew come from the routine catalogue,
  not from a guessed execution owner. The detail labels issue ownership as
  Assignee. Search and crew filters use the identities displayed on screen.
- Source errors are visible above both mobile panes. A partially loaded or
  failed inbox does not announce all caught up. Existing action/update/history
  classification and approval deduplication remain authoritative.

Validation: 381 focused frontend tests, lint, production export, full Go tests
(with a 40-minute limit and RAM-backed TMPDIR) and go vet. Browser checks cover
responsive list/detail, crew and name filtering, legacy deep links, and the
absence of accidental decision actions while navigating. Public deployment
is verified separately after embedding the export into the Go server.

User refinement, 2026-09-06: Routines is the density reference. The desktop
inbox sidebar is 280px, with search followed by three standard navigation
rows. Updates use 20px identities and two compact lines (title, owner/age),
without card borders, extra kind badges or repeated Open/Unread labels.
Unread remains an accessible dot; decisions retain kind/outcome and expiry.
Touch targets follow the shared sidebar kit. Message bodies are unchanged.


## Unified message reading surface

Details now use one responsive document surface instead of separate metadata,
origin and action cards. The compact identity header precedes the subject and
14px Markdown body. Source links appear once for messages; archived messages
retain navigation without stale action buttons. Archive, unread and restore
live in an accessible options menu with the existing source-managed guards.
Approvals, mission signals and grouped incidents share the same surface and
spacing. Routine proposal diffs and approval impact precede decision actions.
Grouped incidents expose their original individual notices. Technical context
and run steps remain expandable; secrets retain display redaction.

No bodies are rewritten, no summaries are fabricated and no new result-fetch
contract is introduced. An issue-review notification that contains only the
issue title still links to the issue for its full output. Code and tables are
keyboard reachable and horizontally scroll within the document; long text
wraps. Actions wrap on narrow screens, and entrance motion respects reduced
motion. Navigation does not approve or dismiss an item.

Validation: 311 frontend tests, full Go test/vet, lint and production export.
Browser fixtures cover all ten inbox kinds with long Markdown/code/context,
plus pending and completed queue approvals at desktop, tablet and phone sizes.
Public real-data smoke checks are run separately after deployment.
