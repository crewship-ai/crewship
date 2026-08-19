"use client"

import { useEffect } from "react"
import { FileText, Zap, Users } from "lucide-react"
import { motion } from "motion/react"
import { useHotkeys } from "react-hotkeys-hook"

import { Button } from "@/components/ui/button"
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"
import { spring } from "@/lib/motion"
import { useDrawerStore, type DrawerTab } from "@/stores/drawer-store"

interface RailItem {
  id: DrawerTab
  label: string
  icon: typeof FileText
  shortcut?: string
}

// Context tab intentionally dropped from the chat drawer — that surface
// belongs to the agent canvas / settings page, not the per-session chat.
// Keeping the rail tight to Files / Triggers / Team makes the drawer
// feel less like a kitchen-sink and more like a focused chat sidekick.
const ITEMS: RailItem[] = [
  { id: "files", label: "Files", icon: FileText, shortcut: "1" },
  { id: "triggers", label: "Triggers", icon: Zap, shortcut: "2" },
  { id: "team", label: "Team", icon: Users, shortcut: "3" },
]

/**
 * What each panel is called, in one place.
 *
 * The rail is three unlabelled 16px glyphs, and the drawer it opens had no
 * header of its own — so the only way to know which of the three you were
 * looking at was to recognise the icon. The rail's tooltip, the drawer's
 * accessible name and the panel's own heading all read from here so they
 * cannot say three different things.
 *
 * "context" is not a rail button any more (it moved to the agent canvas) but
 * survives in persisted user state, so it keeps a name.
 */
export const DRAWER_TAB_LABELS: Record<DrawerTab, string> = {
  files: "Files",
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

  // Migrate persisted "context" → "files". Depend on activeTab so this
  // also fires after the persist middleware hydrates with the legacy
  // value (which can land after the first render).
  useEffect(() => {
    if (activeTab === "context") setActiveTab("files")
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

  return (
    <TooltipProvider delayDuration={400}>
      <div
        className={cn(
          "flex flex-col items-center gap-1 w-12 shrink-0 border-l bg-background py-2",
          className,
        )}
        role="tablist"
        aria-label="Chat side panels"
      >
        {ITEMS.map(({ id, label, icon: Icon, shortcut }) => {
          const isActive = open && activeTab === id
          return (
            <Tooltip key={id}>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className={cn(
                    "h-9 w-9 relative",
                    isActive && "text-foreground",
                    !isActive && "text-muted-foreground hover:text-foreground",
                  )}
                  role="tab"
                  aria-selected={isActive}
                  aria-controls={`drawer-panel-${id}`}
                  // The shortcut is drawn in the tooltip; without this it is
                  // visual-only, and the tooltip is the thing a keyboard user
                  // is least likely to have seen.
                  aria-keyshortcuts={shortcut ? `Meta+${shortcut}` : undefined}
                  onClick={() => toggle(id)}
                >
                  {isActive && (
                    <motion.span
                      layoutId="rail-active-indicator"
                      transition={spring.snappy}
                      className="absolute inset-y-1 left-0 w-0.5 rounded-r bg-primary"
                    />
                  )}
                  <Icon className="h-4 w-4" />
                  <span className="sr-only">{label}</span>
                </Button>
              </TooltipTrigger>
              <TooltipContent side="left">
                <div className="flex items-center gap-2 text-xs">
                  <span>{label}</span>
                  {shortcut && (
                    <kbd className="rounded border bg-muted px-1 font-mono text-[10px]">
                      ⌘{shortcut}
                    </kbd>
                  )}
                </div>
              </TooltipContent>
            </Tooltip>
          )
        })}
      </div>
    </TooltipProvider>
  )
}
