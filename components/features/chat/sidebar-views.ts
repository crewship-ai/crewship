/**
 * Smart views of the chat sidebar (CH-26). Pure: what is in a view, which
 * views exist, and which have a real data source today.
 *
 * Live today: Unreads (room and agent-session unread counts) and Running
 * (agent status RUNNING — an agent that is running is NOT a running selected
 * chat, so the label says "Agents running"). Mentions, Threads, Saved,
 * Drafts, Waiting for you and Review have no source yet (FE-REQ-F6-3) and
 * render from fixtures behind NEXT_PUBLIC_CHAT_SIDEBAR_SMART_SECTIONS.
 */

export type SidebarViewId = "all" | "unreads" | "mentions" | "threads" | "saved" | "drafts" | "waiting" | "running" | "review"

export interface SidebarViewMeta { id: SidebarViewId; label: string; live: boolean }

export const SIDEBAR_VIEWS: SidebarViewMeta[] = [
  { id: "all", label: "All", live: true },
  { id: "unreads", label: "Unreads", live: true },
  { id: "mentions", label: "Mentions", live: false },
  { id: "threads", label: "Threads", live: false },
  { id: "saved", label: "Saved", live: false },
  { id: "drafts", label: "Drafts", live: false },
  { id: "waiting", label: "Waiting for you", live: false },
  { id: "running", label: "Agents running", live: true },
  { id: "review", label: "Review", live: false },
]

export function availableViews(smartSections: boolean): SidebarViewMeta[] {
  return SIDEBAR_VIEWS.filter((view) => view.live || smartSections)
}

export interface ViewItem {
  /** `room:<id>` or `agent:<id>` — the same ids favorites use. */
  id: string
  kind: "room" | "agent"
  title: string
  unread: number
  running: boolean
  /** Fixture rows are previews: shown, labelled, not openable. */
  fixture?: boolean
}

export function itemsForView(view: SidebarViewId, all: ViewItem[], fixtures: Partial<Record<SidebarViewId, ViewItem[]>>): ViewItem[] {
  switch (view) {
    case "all": return all
    case "unreads": return all.filter((item) => item.unread > 0)
    case "running": return all.filter((item) => item.kind === "agent" && item.running)
    default: return (fixtures[view] ?? []).map((item) => ({ ...item, fixture: true }))
  }
}

/**
 * The open chat stays in the list even when the view would filter it out, so
 * choosing a filter never closes or hides the conversation being read. It is
 * appended (not inserted) so no other row moves.
 */
export function keepActive(shown: ViewItem[], all: ViewItem[], activeId: string | null): { items: ViewItem[]; activeOutside: boolean } {
  if (!activeId || shown.some((item) => item.id === activeId)) return { items: shown, activeOutside: false }
  const active = all.find((item) => item.id === activeId)
  return active ? { items: [...shown, active], activeOutside: true } : { items: shown, activeOutside: false }
}

export const VIEW_FIXTURES: Partial<Record<SidebarViewId, ViewItem[]>> = {
  mentions: [{ id: "fx:m1", kind: "room", title: "#launch: Priya mentioned you", unread: 1, running: false }],
  threads: [{ id: "fx:t1", kind: "room", title: "Thread in #ops: rollback plan", unread: 2, running: false }],
  saved: [{ id: "fx:s1", kind: "room", title: "Saved: deploy checklist", unread: 0, running: false }],
  drafts: [{ id: "fx:d1", kind: "room", title: "Draft to Ada", unread: 0, running: false }],
  waiting: [{ id: "fx:w1", kind: "agent", title: "Ada needs your approval", unread: 0, running: false }],
  review: [{ id: "fx:r1", kind: "agent", title: "Bob proposed a document change", unread: 0, running: false }],
}
