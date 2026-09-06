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
