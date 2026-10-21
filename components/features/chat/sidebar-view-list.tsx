"use client"

import { useEffect, useRef } from "react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { useStableOrder } from "./use-stable-order"
import type { SidebarViewId, SidebarViewMeta, ViewItem } from "./sidebar-views"

const sameItem = (a: ViewItem, b: ViewItem) => a.id === b.id && a.title === b.title && a.unread === b.unread && a.running === b.running && a.kept === b.kept && a.disabled === b.disabled && a.fixture === b.fixture

function isKeyboardFocus(target: EventTarget): boolean {
  try { return target instanceof Element && target.matches(":focus-visible") } catch { return false }
}

export function SidebarViewChips({ views, value, onChange }: { views: SidebarViewMeta[]; value: SidebarViewId; onChange: (id: SidebarViewId) => void }) {
  return <div role="group" aria-label="Chat views" className="flex gap-1 overflow-x-auto px-2 pb-2">
    {views.map((view) => <button key={view.id} type="button" aria-pressed={value === view.id} onClick={() => onChange(view.id)} className={cn("kit-tap min-h-7 coarse:min-h-[44px] shrink-0 rounded-full border px-2.5 text-micro", value === view.id ? "border-primary/40 bg-primary/10 text-foreground" : "border-transparent text-muted-foreground hover:bg-accent")}>{view.label}{!view.live && <span className="sr-only"> (preview data)</span>}</button>)}
  </div>
}

/**
 * A flat list for one smart view. Rows hold still while the pointer or focus is
 * inside (live changes surface as a Refresh offer), and the open chat is never
 * dropped by the filter.
 */
export function SidebarViewList({ label, viewId, query, items, activeId, hold, onHold, onOpen }: {
  label: string
  /** With `query` and `activeId`, the user's own changes: they replace the held snapshot at once. */
  viewId: string
  query: string
  items: ViewItem[]
  activeId: string | null
  hold: boolean
  onHold: (hold: boolean) => void
  onOpen: (item: ViewItem) => void
}) {
  const stable = useStableOrder(items, (item) => item.id, hold, `${viewId}|${query}|${activeId ?? ""}`, sameItem)
  const section = useRef<HTMLElement>(null)
  const pointerInside = useRef(false)
  const focusHold = useRef(false)
  // Unmounting with the pointer inside never fires pointerleave; release the hold ourselves.
  useEffect(() => () => onHold(false), [onHold])
  // A focused row that is removed fires no blur: release the focus hold when focus left the list.
  useEffect(() => {
    if (focusHold.current && !section.current?.contains(document.activeElement)) {
      focusHold.current = false
      if (!pointerInside.current) onHold(false)
    }
  }, [stable.items, onHold])
  return <section ref={section} aria-label={label}
    onPointerEnter={() => { pointerInside.current = true; onHold(true) }}
    onPointerLeave={() => { pointerInside.current = false; if (!focusHold.current) onHold(false) }}
    onFocus={(event) => { if (isKeyboardFocus(event.target)) { focusHold.current = true; onHold(true) } }}
    onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) { focusHold.current = false; if (!pointerInside.current) onHold(false) } }}>
    {stable.pending && <div role="status" className="flex items-center gap-2 px-3 py-1 text-label text-muted-foreground">This list changed.<Button type="button" variant="ghost" size="sm" onClick={stable.refresh}>Refresh</Button></div>}
    {stable.items.length === 0 && <p className="px-3 py-2 text-xs text-muted-foreground">Nothing in {label} right now.</p>}
    {stable.items.map((item) => <button key={item.id} type="button" disabled={item.fixture || item.disabled} title={item.fixture ? "Sample data: not a real conversation" : item.disabled ? "Not loaded yet: this agent's chats have not loaded" : undefined} aria-current={item.id === activeId ? "page" : undefined} onClick={() => onOpen(item)} className={cn("kit-tap mx-1 flex min-h-9 w-[calc(100%-0.5rem)] items-center gap-2 rounded-md px-2 text-left text-xs hover:bg-foreground/[0.04] disabled:opacity-70", item.id === activeId && "bg-primary/10")}>
      <span className="min-w-0 flex-1 truncate">{item.title}</span>
      {item.disabled && <span className="sr-only"> (chats not loaded yet)</span>}
      {item.fixture && <><span className="text-micro text-muted-foreground">Preview</span><span className="sr-only"> (sample data, not a real conversation)</span></>}{item.kept && <span className="text-micro text-muted-foreground">Open chat</span>}
      {item.kind === "agent" && <span className="text-micro text-muted-foreground">AI</span>}
      {item.unread > 0 && <span className="text-micro tabular-nums text-primary">{item.unread}</span>}
    </button>)}
  </section>
}
