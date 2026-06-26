"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Activity, Bot, Workflow } from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Button } from "@/components/ui/button"
import { ScrollArea } from "@/components/ui/scroll-area"
import { useActiveRuns, type ActiveRunItem } from "@/hooks/use-active-runs"
import { useWorkspace } from "@/hooks/use-workspace"
import { cn } from "@/lib/utils"

// ActivityBell — the global "what's running now" surface in the toolbar.
// Mirrors InboxBell's pattern (badge + dropdown), but where the inbox is
// "things needing you," this is "work in flight": agent runs and routine
// runs currently executing, live from the realtime stream. Click a row to
// jump to its detail; footer links to the full Activity page.
export function ActivityBell() {
  const router = useRouter()
  const { workspaceId } = useWorkspace()
  const [open, setOpen] = useState(false)
  const { runs, count } = useActiveRuns(workspaceId)

  const recent = runs.slice(0, 6)

  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="relative h-8 w-8"
          aria-label={`Activity: ${count} running`}
        >
          <Activity className="h-4 w-4" />
          <AnimatePresence>
            {count > 0 && (
              <motion.span
                initial={{ scale: 0 }}
                animate={{ scale: 1 }}
                exit={{ scale: 0 }}
                className="absolute -right-0.5 -top-0.5 flex h-4 min-w-[16px] items-center justify-center rounded-full bg-emerald-500 px-1 text-[9px] font-semibold text-white"
              >
                {count > 99 ? "99+" : count}
              </motion.span>
            )}
          </AnimatePresence>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-[360px] p-0">
        <div className="flex items-center justify-between border-b border-white/[0.06] px-3 py-2">
          <span className="text-xs font-medium">Activity</span>
          <span className="inline-flex items-center gap-1 text-[10px] text-muted-foreground">
            {count > 0 && <span className="h-1.5 w-1.5 rounded-full bg-emerald-500 animate-pulse" />}
            {count} running
          </span>
        </div>
        <ScrollArea className="max-h-[400px]">
          {recent.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-2 p-6 text-center">
              <Activity className="h-6 w-6 text-muted-foreground/30" />
              <span className="text-xs text-muted-foreground">Nothing running right now</span>
            </div>
          ) : (
            <ul className="divide-y divide-white/[0.04]">
              {recent.map((item) => (
                <BellRow
                  key={item.id}
                  item={item}
                  onClick={() => {
                    setOpen(false)
                    router.push(item.href)
                  }}
                />
              ))}
            </ul>
          )}
        </ScrollArea>
        <div className="border-t border-white/[0.06] p-2">
          <Link
            href="/activity"
            onClick={() => setOpen(false)}
            className="block w-full rounded px-2 py-1.5 text-center text-xs text-emerald-400 hover:bg-white/[0.04]"
          >
            View all activity →
          </Link>
        </div>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function BellRow({ item, onClick }: { item: ActiveRunItem; onClick: () => void }) {
  const Icon = item.kind === "routine" ? Workflow : Bot
  return (
    <li
      onClick={onClick}
      role="button"
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onClick()
        }
      }}
      className="flex cursor-pointer items-start gap-2 px-3 py-2 hover:bg-white/[0.04]"
    >
      <span className="relative mt-0.5 shrink-0">
        <Icon className={cn("h-3.5 w-3.5", item.kind === "routine" ? "text-violet-300" : "text-blue-300")} />
      </span>
      <div className="min-w-0 flex-1">
        <div className="truncate text-xs font-medium text-foreground">{item.label}</div>
        <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-muted-foreground">
          <span className="h-1 w-1 rounded-full bg-emerald-500 animate-pulse" />
          {item.kind === "routine" ? "Routine" : "Agent"}
          {item.sublabel ? ` · ${item.sublabel}` : ""}
          {item.startedAt ? ` · ${relTime(item.startedAt)}` : ""}
        </div>
      </div>
    </li>
  )
}

function relTime(iso?: string) {
  if (!iso) return ""
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ""
  const diff = Date.now() - d.getTime()
  if (Math.abs(diff) < 60_000) return "just now"
  const mins = Math.round(Math.abs(diff) / 60_000)
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.round(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.round(hrs / 24)}d ago`
}
