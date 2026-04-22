"use client"

import Link from "next/link"
import { motion } from "motion/react"
import {
  X, MessageSquare, ScrollText, Settings, ExternalLink,
  Gavel, Briefcase, AlertTriangle, Inbox, Crown,
  Brain, CalendarClock,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { timeAgo } from "@/lib/time"
import { useAgentInbox } from "@/hooks/use-agent-inbox"

interface AgentBrief {
  id: string
  name: string
  slug: string
  agent_role: string
  crew?: { slug: string } | null
}

export interface CrewsAgentInboxProps {
  agent: AgentBrief
  /** `schedule_next_run` ISO timestamp — echoed here as a countdown chip. */
  scheduleNextRun?: string | null
  /** `memory_enabled` flag from agent detail — defaults false for display. */
  memoryEnabled?: boolean
  /** `lead_mode` from agent detail; shown only when agent_role=LEAD. */
  leadMode?: string | null
  workspaceId: string
  onClose: () => void
}

/**
 * Right-panel "Inbox & Actions" view (Phase 10). Unique value:
 *   - pending work items (approvals / assignments / escalations)
 *   - recent peer messages
 *   - compact status chips (lead mode, memory, schedule countdown)
 * Does NOT duplicate agent profile/stats/runs — those live in the
 * center CrewsAgentInline pane.
 */
export function CrewsAgentInbox({
  agent, workspaceId: _workspaceId,
  scheduleNextRun, memoryEnabled, leadMode,
  onClose,
}: CrewsAgentInboxProps) {
  const { inbox, loading } = useAgentInbox(agent.id)
  const agentPath = `/crews/agents/${agent.id}`
  const scheduleCountdown = scheduleNextRun
    ? formatCountdown(new Date(scheduleNextRun).getTime() - Date.now())
    : null
  const isLead = agent.agent_role === "LEAD"

  return (
    <motion.div
      key={agent.id}
      initial={{ opacity: 0, x: 12 }}
      animate={{ opacity: 1, x: 0 }}
      exit={{ opacity: 0, x: 12 }}
      transition={{ duration: 0.15, ease: "easeOut" }}
      className="h-full border-l border-white/[0.1] bg-card flex flex-col"
    >
      {/* Header — minimal; the full identity lives in center */}
      <div className="flex items-center gap-2 p-4 border-b border-border shrink-0">
        <Inbox className="h-4 w-4 text-muted-foreground shrink-0" />
        <span className="text-label font-semibold uppercase tracking-wider text-muted-foreground truncate">
          Inbox &amp; actions
        </span>
        <Button variant="ghost" size="icon-xs" className="ml-auto text-muted-foreground" onClick={onClose} aria-label="Close">
          <X className="h-4 w-4" />
        </Button>
      </div>

      <div className="flex-1 overflow-y-auto p-4 space-y-4">
        {/* Quick actions */}
        <div className="grid grid-cols-3 gap-2">
          <Button size="sm" className="h-8 text-micro gap-1" asChild>
            <Link href={`${agentPath}/chat`}>
              <MessageSquare className="h-3 w-3" />
              Chat
            </Link>
          </Button>
          <Button variant="outline" size="sm" className="h-8 text-micro gap-1" asChild>
            <Link href={`${agentPath}/logs`}>
              <ScrollText className="h-3 w-3" />
              Logs
            </Link>
          </Button>
          <Button variant="outline" size="sm" className="h-8 text-micro gap-1" asChild>
            <Link href={`${agentPath}/settings`}>
              <Settings className="h-3 w-3" />
              Settings
            </Link>
          </Button>
        </div>
        <Button variant="ghost" size="sm" className="w-full h-7 text-micro text-muted-foreground gap-1.5" asChild>
          <Link href={agentPath}>
            Open full agent page
            <ExternalLink className="h-3 w-3" />
          </Link>
        </Button>

        {/* INBOX section */}
        <section className="space-y-2">
          <div className="flex items-center justify-between">
            <h3 className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">
              Inbox
            </h3>
            {inbox && (
              <span className="text-micro text-muted-foreground/70 tabular-nums">
                {inbox.approvals_pending + inbox.assignments_open + inbox.escalations_open}
              </span>
            )}
          </div>
          {loading && !inbox ? (
            <InboxSkeleton />
          ) : (
            <div className="space-y-1">
              <InboxRow
                icon={Gavel}
                count={inbox?.approvals_pending ?? 0}
                label="approvals pending"
                href={`/approvals?agent_id=${agent.id}`}
              />
              <InboxRow
                icon={Briefcase}
                count={inbox?.assignments_open ?? 0}
                label="assignments open"
                href={
                  agent.crew?.slug
                    ? `/crews/crews/${agent.crew.slug}?tab=journal`
                    : `/crews/agents/${agent.id}`
                }
              />
              <InboxRow
                icon={AlertTriangle}
                count={inbox?.escalations_open ?? 0}
                label={`escalation${inbox?.escalations_open === 1 ? "" : "s"}`}
                href={
                  agent.crew?.slug
                    ? `/crews/crews/${agent.crew.slug}?tab=journal`
                    : `/crews/agents/${agent.id}`
                }
              />
            </div>
          )}
        </section>

        {/* PEER MESSAGES */}
        {inbox && inbox.peer_messages.length > 0 && (
          <section className="space-y-2">
            <h3 className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">
              Peer messages
            </h3>
            <div className="space-y-1.5">
              {inbox.peer_messages.map((pm) => (
                <Link
                  key={pm.id}
                  href={
                    agent.crew?.slug
                      ? `/crews/crews/${agent.crew.slug}?tab=journal`
                      : `/crews/agents/${agent.id}`
                  }
                  className="block rounded-md hover:bg-white/[0.04] transition-colors p-1.5 -m-1.5"
                >
                  <div className="flex items-center gap-2">
                    <span className={cn(
                      "text-micro font-medium shrink-0",
                      pm.direction === "incoming" ? "text-amber-400" : "text-muted-foreground/70",
                    )}>
                      {pm.direction === "incoming" ? "←" : "→"} {pm.from_agent_name}
                    </span>
                    <span className="text-micro text-muted-foreground tabular-nums ml-auto shrink-0">
                      {timeAgo(pm.created_at)}
                    </span>
                  </div>
                  <p className="text-label text-foreground/80 truncate mt-0.5">
                    {pm.question}
                  </p>
                </Link>
              ))}
            </div>
          </section>
        )}

        {/* STATUS chips */}
        <section className="space-y-2">
          <h3 className="text-micro font-semibold uppercase tracking-wider text-muted-foreground">
            Status
          </h3>
          <div className="space-y-1.5">
            {isLead && (
              <StatusChip
                icon={Crown}
                label="Lead mode"
                value={leadMode === "passive" ? "passive" : "active"}
                tone={leadMode === "passive" ? "muted" : "emerald"}
              />
            )}
            <StatusChip
              icon={Brain}
              label="Memory"
              value={memoryEnabled ? "on" : "off"}
              tone={memoryEnabled ? "emerald" : "muted"}
            />
            {scheduleCountdown && (
              <StatusChip
                icon={CalendarClock}
                label="Schedule"
                value={`next: ${scheduleCountdown}`}
                tone="primary"
              />
            )}
          </div>
        </section>
      </div>
    </motion.div>
  )
}

function InboxRow({
  icon: Icon, count, label, href,
}: {
  icon: React.ElementType
  count: number
  label: string
  href: string
}) {
  const active = count > 0
  return (
    <Link
      href={href}
      className={cn(
        "flex items-center gap-2 py-1.5 px-2 -mx-2 rounded-md transition-colors",
        active
          ? "hover:bg-white/[0.06] text-foreground"
          : "text-muted-foreground/60 pointer-events-none opacity-70",
      )}
    >
      <span className={cn(
        "flex items-center justify-center h-6 w-6 rounded-full",
        active ? "bg-amber-500/15 text-amber-400" : "bg-muted/40 text-muted-foreground/60",
      )}>
        <Icon className="h-3 w-3" />
      </span>
      <span className="text-label tabular-nums font-semibold">
        {count}
      </span>
      <span className="text-label truncate">{label}</span>
      {active && (
        <ExternalLink className="h-3 w-3 text-muted-foreground/50 ml-auto shrink-0" />
      )}
    </Link>
  )
}

function StatusChip({
  icon: Icon, label, value, tone,
}: {
  icon: React.ElementType
  label: string
  value: string
  tone: "emerald" | "muted" | "primary"
}) {
  const toneClass = tone === "emerald"
    ? "text-emerald-400 bg-emerald-500/10"
    : tone === "primary"
      ? "text-primary bg-primary/10"
      : "text-muted-foreground bg-muted/40"
  return (
    <div className="flex items-center gap-2 text-micro">
      <span className={cn("flex items-center justify-center h-5 w-5 rounded-full shrink-0", toneClass)}>
        <Icon className="h-3 w-3" />
      </span>
      <span className="text-muted-foreground uppercase tracking-wider w-20 shrink-0">{label}</span>
      <span className="font-medium text-foreground/85 tabular-nums">{value}</span>
    </div>
  )
}

function InboxSkeleton() {
  return (
    <div className="space-y-1 animate-pulse">
      {Array.from({ length: 3 }).map((_, i) => (
        <div key={i} className="flex items-center gap-2 py-1.5">
          <div className="h-6 w-6 rounded-full bg-muted/40" />
          <div className="h-3 w-6 bg-muted/40 rounded" />
          <div className="h-3 w-24 bg-muted/30 rounded" />
        </div>
      ))}
    </div>
  )
}

function formatCountdown(ms: number): string {
  if (ms <= 0) return "imminent"
  const mins = Math.floor(ms / 60_000)
  if (mins < 60) return `${mins}m`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ${mins % 60}m`
  const days = Math.floor(hrs / 24)
  return `${days}d ${hrs % 24}h`
}
