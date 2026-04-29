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

export function RightRail({ className }: { className?: string }) {
  const { open, activeTab, toggle, setActiveTab } = useDrawerStore()

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
