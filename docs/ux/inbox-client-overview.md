# Client inbox — 2026-09-06

**Public implementation notes.** Retained for contributors because they
explain inbox navigation, identities and action boundaries. The current source
and regression tests live in
[`components/features/inbox-v2/`](../../components/features/inbox-v2/).
Validation counts below record the original implementation runs, not a new
full-suite result for the current checkout.

Issue #2435, continues dashboard PR #2434.

- `/inbox` is the product route. Sidebar, command palette, notifications,
  credentials, crew, routine and activity links use it. `/inbox-v2` is a
  compatibility entry that replaces its URL while retaining query and hash;
  this is a client redirect compatible with Next static export.
- Left column: three clear views (To handle / Updates / History), search,
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


## Remove redundant controls

The toolbar contains identity, counts and connection status only. History is
in the left navigation; the duplicate toolbar History and Review next actions
are removed. Next to review (with remaining count) is offered only on a
completed actionable item's confirmation, when another decision is available
and all sources are healthy. It never advances automatically. That screen
uses its own Back to inbox button instead of a second sticky back bar.

The overview no longer repeats Live or Saved in History captions. Decision
forms omit redundant permission-success text while retaining explanations for
users who cannot decide. Crew navigation lives in the byline, without duplicate
links below the same message. Source navigation, archival guards, evidence and
all supported action handlers remain intact.


## Harbor 2 layout — 2026-10-17

Issue #3385, part of the Harbor 2 redesign (#3366, #3350). Supersedes the
280 px column, the automatic preview and the triage pane described above.

- **Panel (272 px, collapsible, SecondaryPanel).** Toolbar: search (`/`),
  Filter, Select several, Sort. Filter opens the facets *in the panel* —
  State (Unread only, Due within 24 hours), Attention (Decision, Input needed,
  Review, Repair), Source type (the twelve real kinds) and Crew — and active
  facets show as chips ("State: Unread only ×"). Sort is Urgency (expires
  first, the default), Newest or Crew, remembered per user (`inbox.sort`).
  Spaces To handle / Updates / History (keys 1–3) and an Overview row sit
  above the sections. Rows: unread dot, sender avatar, title (bold while
  unread), "{crew} · {kind}", expiry ("in 3h") or age, ×N for merged
  notices.
- **Attention.** The server's `attention_class` wins; rows written before the
  contract are classed by kind (waitpoint → Decision, run needs human →
  Input needed, proposals/memory → Review, failures and schedules → Repair).
  `?attention=approvals|run-alerts|schedule-alerts` keeps its own blocking
  rule and shows as a chip plus a card on the Overview.
- **No automatic preview.** With nothing open the pane is the Overview
  ("Your inbox, at a glance": counts, four attention tiles, By crew,
  Expiring soon, Recent history). A previewed row put a live Approve button in
  front of a reader who never chose it.
- **Top bar.** Inbox › {kind}, Live, position n / N, Previous (K), Next (J),
  Archive (E) — disabled until an open decision is decided, since the server
  refuses to archive it — and the ⋯ menu (Mark unread, Archive, Restore). On
  a phone the bar carries Back; the list, the detail and the Overview are
  separate steps.
- **One detail layout.** Sender and attention, title, message, the decision
  block (heading, expiry, the facts it rests on) with its actions docked
  bottom-right — ghost, secondary, then the primary as the right-most button
  in DOM order — then "Where this came from", expandable run steps and
  context, and who decides beside the buttons a role cannot use. Secrets show
  as `••••` with Reveal. Wide desktop (≥ 1536 px) adds a sender rail with
  "Earlier from".
- **Receipts.** After a decision: the outcome, "Saved … · the record stays in
  History", View record and, when another decision waits, Next to review · N.
  History items open with the same receipt and View in Activity above the
  request they settled.
- **States.** Skeleton rows while loading, "Nothing waits for you" with Open
  Activity and `crewship inbox`, a page error with Retry when the inbox
  cannot load, one banner when a source is partial, and "No longer in your
  inbox" (Open History / Back to To handle) for an unknown `?item=`.
- **Confirmations.** Irreversible run-needs-human actions confirm in the
  shared dialog instead of the browser's `confirm()`.
- **Not shown (no backend yet).** Snooze, inbox sources that do not exist
  yet, free-text answers for agent questions and credential requests, and
  actions for held artifact creation and ready routine triggers. Attention
  and Crew facets count the loaded feed; server-side filters are a separate
  backend issue.
