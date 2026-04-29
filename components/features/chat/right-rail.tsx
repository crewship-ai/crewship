"use client"

import { FileText, Zap, Users, Bookmark } from "lucide-react"
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

const ITEMS: RailItem[] = [
  { id: "files", label: "Files", icon: FileText, shortcut: "1" },
  { id: "triggers", label: "Triggers", icon: Zap, shortcut: "2" },
  { id: "team", label: "Team", icon: Users, shortcut: "3" },
  { id: "context", label: "Context", icon: Bookmark, shortcut: "4" },
]

export function RightRail({ className }: { className?: string }) {
  const { open, activeTab, toggle } = useDrawerStore()

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
