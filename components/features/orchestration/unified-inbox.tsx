"use client"

import { useMemo, useState } from "react"
import {
  Diamond,
  X,
  PauseCircle,
  CheckCircle2,
  ChevronRight,
  ChevronDown,
  Inbox,
} from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import { Badge } from "@/components/ui/badge"
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import { cn } from "@/lib/utils"
import type { Mission, MissionTask } from "@/lib/types/mission"

interface InboxItem {
  task: MissionTask
  mission: Mission
}

export interface UnifiedInboxProps {
  missions: Mission[]
  onTaskSelect: (task: MissionTask, mission: Mission) => void
  onApproveGate?: (taskId: string, missionId: string) => void
}

function useInboxItems(missions: Mission[]) {
  return useMemo(() => {
    const approvals: InboxItem[] = []
    const failed: InboxItem[] = []
    const blocked: InboxItem[] = []

    for (const mission of missions) {
      for (const task of mission.tasks || []) {
        if (
          task.needs_review &&
          task.status !== "COMPLETED" &&
          task.status !== "SKIPPED"
        ) {
          approvals.push({ task, mission })
        }
        if (task.status === "FAILED") {
          failed.push({ task, mission })
        }
        if (task.status === "BLOCKED") {
          blocked.push({ task, mission })
        }
      }
    }

    return { approvals, failed, blocked, total: approvals.length + failed.length + blocked.length }
  }, [missions])
}

function CountBadge({ count }: { count: number }) {
  return (
    <AnimatePresence mode="wait">
      {count > 0 && (
        <motion.span
          key={count}
          initial={{ scale: 0.6, opacity: 0 }}
          animate={{ scale: 1, opacity: 1 }}
          exit={{ scale: 0.6, opacity: 0 }}
          transition={{ duration: 0.15 }}
        >
          <Badge
            variant="secondary"
            className="h-4 min-w-4 px-1 text-[10px] bg-accent text-muted-foreground border-0"
          >
            {count}
          </Badge>
        </motion.span>
      )}
    </AnimatePresence>
  )
}

interface InboxSectionProps {
  label: string
  icon: React.ReactNode
  items: InboxItem[]
  accentClass: string
  onTaskSelect: (task: MissionTask, mission: Mission) => void
  onApprove?: (taskId: string, missionId: string) => void
  showApprove?: boolean
}

function InboxSection({
  label,
  icon,
  items,
  accentClass,
  onTaskSelect,
  onApprove,
  showApprove,
}: InboxSectionProps) {
  const [open, setOpen] = useState(true)

  if (items.length === 0) return null

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger asChild>
        <button className="w-full flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-accent/50 transition-colors">
          <span className="text-muted-foreground/70 shrink-0">
            {open ? (
              <ChevronDown className="h-3 w-3" />
            ) : (
              <ChevronRight className="h-3 w-3" />
            )}
          </span>
          <span className={cn("shrink-0", accentClass)}>{icon}</span>
          <span className="text-[11px] font-medium text-muted-foreground flex-1 text-left">
            {label}
          </span>
          <CountBadge count={items.length} />
        </button>
      </CollapsibleTrigger>

      <CollapsibleContent>
        <div className="ml-4 pl-2.5 border-l border-border space-y-px">
          {items.map(({ task, mission }) => (
            <button
              key={task.id}
              className="w-full flex items-center gap-2 px-2 py-1.5 rounded-md hover:bg-accent/50 transition-colors text-left group"
              onClick={() => onTaskSelect(task, mission)}
            >
              <div className="flex-1 min-w-0">
                <div className="text-[11px] text-foreground/80 truncate">
                  {task.title}
                </div>
                <div className="flex items-center gap-1.5 mt-0.5">
                  {task.agent_slug && (
                    <span className="text-[10px] font-mono text-muted-foreground/70">
                      @{task.agent_slug}
                    </span>
                  )}
                  <span className="text-[10px] text-muted-foreground/50 truncate">
                    {mission.title}
                  </span>
                </div>
              </div>
              {showApprove && onApprove && (
                <button
                  className="shrink-0 px-1.5 py-0.5 rounded text-[10px] font-medium bg-amber-500/10 text-amber-400 hover:bg-amber-500/20 transition-colors opacity-0 group-hover:opacity-100"
                  onClick={(e) => {
                    e.stopPropagation()
                    onApprove(task.id, mission.id)
                  }}
                >
                  Approve
                </button>
              )}
            </button>
          ))}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}

export function UnifiedInbox({
  missions,
  onTaskSelect,
  onApproveGate,
}: UnifiedInboxProps) {
  const { approvals, failed, blocked, total } = useInboxItems(missions)

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-2 border-b border-border">
        <Inbox className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-xs font-semibold text-muted-foreground">Inbox</span>
        <CountBadge count={total} />
      </div>

      {/* Content — native scroll, Radix ScrollArea doesn't work with flex-1 */}
      <div className="flex-1 min-h-0 overflow-y-auto">
        <div className="p-2 space-y-0.5">
          <InboxSection
            label="Approvals"
            icon={<Diamond className="h-3.5 w-3.5" />}
            items={approvals}
            accentClass="text-amber-400"
            onTaskSelect={onTaskSelect}
            onApprove={onApproveGate}
            showApprove
          />
          <InboxSection
            label="Failed"
            icon={<X className="h-3.5 w-3.5" />}
            items={failed}
            accentClass="text-red-400"
            onTaskSelect={onTaskSelect}
          />
          <InboxSection
            label="Blocked"
            icon={<PauseCircle className="h-3.5 w-3.5" />}
            items={blocked}
            accentClass="text-amber-400"
            onTaskSelect={onTaskSelect}
          />

          {total === 0 && (
            <div className="flex flex-col items-center justify-center py-8 gap-2 text-muted-foreground/50">
              <CheckCircle2 className="h-6 w-6" />
              <span className="text-xs">All clear</span>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
