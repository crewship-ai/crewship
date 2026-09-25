"use client"

import { useEffect } from "react"
import { FileText, LayoutGrid, ListTodo } from "lucide-react"
import { useHotkeys } from "react-hotkeys-hook"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { useDrawerStore, type DrawerTab } from "@/stores/drawer-store"

interface RailItem {
  id: DrawerTab
  label: string
  icon: typeof FileText
  shortcut?: string
}

// The shortcut number is derived from the position rather than written down,
// so removing an entry cannot leave ⌘3 pointing at the second icon.
const RAIL_PANELS: Omit<RailItem, "shortcut">[] = [
  { id: "files", label: "Files", icon: FileText },
  { id: "artifacts", label: "Artifacts", icon: LayoutGrid },
  { id: "work", label: "Work", icon: ListTodo },
]

const ITEMS: RailItem[] = RAIL_PANELS.map((item, i) => ({
  ...item,
  shortcut: String(i + 1),
}))

/**
 * What each panel is called, in one place.
 *
 * The rail's button label, the drawer's accessible name and the
 * panel's own heading all read from here, so they cannot say three different
 * things. The rail carried no visible label at all until the label moved onto
 * the button; readers no longer need to hover to learn what it opens.
 *
 * "context" is not a rail button any more (it moved to the agent canvas) but
 * survives in persisted user state, so it keeps a name.
 */
export const DRAWER_TAB_LABELS: Record<DrawerTab, string> = {
  files: "Files",
  artifacts: "Artifacts",
  work: "Work",
  triggers: "Triggers",
  team: "Team",
  context: "Context",
}

export function RightRail({ className }: { className?: string }) {
  // Narrow selectors — the rail re-rendered on width drags and mode flips
  // it never reads.
  const open = useDrawerStore((s) => s.open)
  const activeTab = useDrawerStore((s) => s.activeTab)
  const toggle = useDrawerStore((s) => s.toggle)
  const setActiveTab = useDrawerStore((s) => s.setActiveTab)

  // Migrate tabs removed from the chat surface. Depend on activeTab so this
  // also fires after the persist middleware hydrates with the legacy
  // value (which can land after the first render).
  useEffect(() => {
    if (activeTab === "context" || activeTab === "team" || activeTab === "triggers") setActiveTab("files")
  }, [activeTab, setActiveTab])

  useHotkeys(
    ["mod+b"],
    () => toggle(),
    { preventDefault: true },
    [toggle],
  )

  useHotkeys(
    ["mod+1", "mod+2", "mod+3", "mod+4"],
    (_, info) => {
      const idx = Number(info.keys?.[0]) - 1
      if (idx >= 0 && idx < ITEMS.length) toggle(ITEMS[idx].id)
    },
    { preventDefault: true },
    [toggle],
  )

  return <div className={cn("relative z-30 flex w-14 shrink-0 flex-col items-center gap-0.5 border-l bg-accent/30 py-2", className)} role="tablist" aria-label="Chat side panels">
    {ITEMS.map(({ id, label, icon: Icon, shortcut }) => {
      const isActive = open && activeTab === id
      return <Button key={id} variant="ghost" className={cn("h-auto w-full flex-col gap-1 rounded-md px-0 py-2 text-muted-foreground hover:bg-white/[0.03] hover:text-foreground", isActive && "text-foreground")} role="tab" aria-selected={isActive} aria-controls={`drawer-panel-${id}`} aria-keyshortcuts={shortcut ? `Meta+${shortcut}` : undefined} onClick={() => toggle(id)}>
        <Icon className="h-4 w-4" />
        <span className="text-[11px] font-medium leading-none tracking-tight">{label}</span>
      </Button>
    })}
  </div>
}
