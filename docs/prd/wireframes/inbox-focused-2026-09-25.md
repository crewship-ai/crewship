# Inbox focused layout — audit and wireframe

Date: 2026-09-25. Scope: product/UX proposal in draft PR #2697, **not a production UI change**.
Open [`inbox-focused-2026-09-25.html`](inbox-focused-2026-09-25.html) locally to inspect the interactive desktop and mobile prototype. Its content is sample data adapted from the dev3 demo workspace, not a live API view.

## Verified current behavior

The current `/inbox` route merges active and resolved `inbox_items`, pending and decided `approvals_queue` rows, and mission task signals (`components/features/inbox-v2/inbox-v2.tsx`). It loads every page of the first two sources before deriving Needs action, Updates and History. The source-health banner already warns when a read fails. The desktop layout uses a left explorer and a right triage dashboard when no item is selected.

| Finding | Evidence | Impact / proposed change |
|---|---|---|
| The same navigation is repeated in both panes. | The explorer has three views and a crew filter; `InboxTriage` repeats Updates, By crew and Recent history. | The right pane should show the selected item's full context. History remains in the left navigation, and crew selection stays beside the list. |
| “Waiting for you” includes routine schedules. | `feeds.action` includes any actionable row; `needsHumanDecision` separately excludes schedule notices. The left section heading still says “Waiting for you”. | Split the list into **Decision required** and **Operational alerts**. A schedule can offer Run now or Re-enable without implying an agent is blocked on a human answer. |
| “Routine alerts” is too narrow for the full feed. | Inbox kinds include failed runs, missed schedules, circuit breakers, webhook failures, automation enqueue failures and run-needs-human. Mission tasks may also be blocked or failed. | Use the broader **Operational alerts** label; still name the specific source and effect in each row. A run-needs-human item belongs with decisions when a valid human action exists. |
| The type menu is engine-shaped and incomplete. | `INBOX_V2_TYPES` offers seven older inbox kinds plus approval and mission. `internal/inbox/writer.go` currently admits ten inbox kinds; the menu omits `run_needs_human`, `webhook_fire_failed` and `automation_enqueue_failed`. | Default filters should express human intent (view, crew, unread, deadline if applicable). Put complete, human-labelled source types under Advanced and show only options that occur in the current view. |
| Deadline choices are hard to interpret. | `deadlineBucket` is based on `timeout_at`; Within the hour and Today are mutually exclusive, and future dates beyond today have no explicit choice. Many notices have no deadline. | Use a plain **Due within 24 hours** option only where meaningful. Keep exact deadline in the item; do not display a permanent three-option deadline panel for ordinary updates. |
| Search promises more than it checks. | The current placeholder mentions agents and crews, while `matchesSearch` checks title, summary, subject and category only; it does not search resolved roster names. | Either index resolved agent/crew names and slugs or narrow the placeholder. The wireframe demonstrates a separate crew search. |
| The crew picker is visually ambiguous. | Current sidebar uses a native `<select>` with crew names alone; dev3 has two “Coolify infrastructure sampler” names. Crew also contributes to the filter badge while its control is outside that popover. | One searchable single-select picker in the sidebar. Show name, slug and relevant item count; keep `All crews` explicit. Do not duplicate crew controls elsewhere. |
| The dashboard approval headline overstates client work. | On dev3 it counts 13 active waitpoint/escalation rows. Twelve are grouped curator system advisories with no client decision; the Inbox overview correctly shows one human decision. | The eventual dashboard metric should count requests with a valid human action contract. This needs a shared classification/aggregate, not a copy change inside Inbox alone. Until then, do not label raw escalation counts as approvals waiting. |
| Opening an item currently marks an unread inbox row read. | `openEntry` patches unread to read. | If the desktop list previews the first decision by default, preview selection must not silently mark it read. Mark as read after explicit opening or an agreed view threshold; keep this state transition testable. |

The older [`inbox-maximum-wireframe.md`](../inbox-maximum-wireframe.md) specifies a stronger server contract: producer-emitted attention class, action contract and durable decision receipts. Those are follow-on architecture requirements. This wireframe changes the layout and language using the current sources; it does not claim that contract is already implemented.

## Proposed screen

1. Left column: three destinations — **To handle**, **Updates**, **History** — followed by search, one crew picker and one secondary filter button. Active filters appear as removable chips. The list groups true decisions above operational alerts; it does not hide specific types behind colour alone.
2. Right column: one selected item's detail, not another dashboard. On desktop the most urgent decision can be previewed without changing unread state; on mobile the list opens a full-screen detail with Back. Preserve direct links and the dashboard `attention` filter.
3. Decision detail: request, source/subject, deadline when present, concrete effect, evidence and source action. The sample uses an Open issue action because the full COO-5 question is not available in the screenshot. The production renderer must use that source's actual action contract.
4. Updates and History use the same list/detail structure. History separates actual decisions from archived notices; an archived notice is not presented as an approval.
5. Keep the existing degraded-source notice. When some source fails, never imply the Inbox is complete or empty.

The prototype deliberately shows a few sample entries rather than pretending all 39 updates are present. Its count labels echo the dev3 snapshot so the layout can be judged against the current density.

## Implementation sequence after design approval

1. Replace the default triage card layout with a reading pane and preserve `?item=` / `?attention=` navigation. Distinguish preview from an explicit read to avoid changing unread state on initial load.
2. Split action rows into decision and operational-alert sections; update navigation counts from the same classification. Remove duplicated By crew and Recent history cards.
3. Replace the native crew selector with a searchable, keyboard-accessible single-select based on the existing component library. Include slug and count for duplicate names. For production, follow the [WAI-ARIA combobox pattern](https://www.w3.org/WAI/ARIA/apg/patterns/combobox/) or use native radio controls in a labelled dialog/popover as the prototype does.
4. Move complete source-type filtering under Advanced, align it with `internal/inbox.AllKinds` plus approval/mission signals, and fix search semantics. Test empty, loading, degraded, long-title, keyboard and phone states.
5. Align the dashboard attention aggregate with actual client decisions in a separate data-contract change; a UI-only count cannot be exact once server pagination/windowing is involved.

## Prototype checks

The standalone HTML passed `node --check` on its inline script. Chromium at 1440 and 390 px loaded without page errors or horizontal overflow. View switching, crew search (including two distinct same-name crews), filter popover, Escape dismissal and mobile list/detail navigation were exercised. No production service was changed for this proposal.
