"use client"

import { useState } from "react"
import Link from "next/link"
import { ChevronDown, Plus } from "lucide-react"
import { cn } from "@/lib/utils"
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import type { CrewsRoleFilter, CrewsStatusFilter } from "@/hooks/use-crews-selection"
import { CreateCrewDialog } from "./create-crew-dialog"
import { CreateAgentDialog } from "./create-agent-dialog"

const STATUS_OPTIONS: { value: CrewsStatusFilter; label: string; dot?: string }[] = [
  { value: "all", label: "All" },
  { value: "RUNNING", label: "Running", dot: "bg-emerald-500" },
  { value: "IDLE", label: "Idle", dot: "bg-gray-400" },
  { value: "ERROR", label: "Error", dot: "bg-red-500" },
  { value: "STOPPED", label: "Stopped", dot: "bg-amber-500" },
]

const ROLE_OPTIONS: { value: CrewsRoleFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "AGENT", label: "Agent" },
  { value: "LEAD", label: "Lead" },
  { value: "COORDINATOR", label: "Coordinator" },
]

export interface CrewsSubbarProps {
  workspaceId: string
  crewSlug: string | null
  agentSlug?: string | null
  crewName: string | null
  agentName: string | null
  statusFilter: CrewsStatusFilter
  roleFilter: CrewsRoleFilter
  onStatusChange: (s: CrewsStatusFilter) => void
  onRoleChange: (r: CrewsRoleFilter) => void
  onCrewCreated: () => void
  onAgentCreated: (slug: string) => void
  /** Optional: shown only on mobile to open the explorer drawer. */
  onOpenExplorer?: () => void
  /** Crews list for the agent-creation form. */
  crews: { id: string; name: string; slug: string }[]
}

/**
 * Page-level chrome strip: breadcrumb on the left, filters in the middle,
 * page-specific create CTAs on the right. Same pattern as /orchestration's
 * tab+actions strip; will be lifted into a shared component in a follow-up
 * PR so /skills, /credentials, /runs, /paymaster reuse it.
 */
export function CrewsSubbar({
  workspaceId,
  crewSlug,
  agentSlug: _agentSlug,
  crewName,
  agentName,
  statusFilter,
  roleFilter,
  onStatusChange,
  onRoleChange,
  onCrewCreated,
  onAgentCreated,
  onOpenExplorer,
  crews,
}: CrewsSubbarProps) {
  const [createCrewOpen, setCreateCrewOpen] = useState(false)
  const [createAgentOpen, setCreateAgentOpen] = useState(false)
  const [createAgentDefaultCrew, setCreateAgentDefaultCrew] = useState<string | null>(null)

  const statusLabel = STATUS_OPTIONS.find((s) => s.value === statusFilter)?.label ?? "All"
  const roleLabel = ROLE_OPTIONS.find((r) => r.value === roleFilter)?.label ?? "All"

  return (
    <>
      <div className="h-10 shrink-0 border-b border-white/8 bg-card flex items-center px-3 gap-2 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {onOpenExplorer && (
          <button
            type="button"
            className="md:hidden p-1 rounded hover:bg-white/5 text-foreground/80"
            onClick={onOpenExplorer}
            aria-label="Open explorer"
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
              <line x1="3" y1="12" x2="21" y2="12" />
              <line x1="3" y1="6" x2="21" y2="6" />
              <line x1="3" y1="18" x2="21" y2="18" />
            </svg>
          </button>
        )}

        {/* Breadcrumb */}
        <nav className="flex items-center gap-1.5 text-xs min-w-0">
          <Link href="/crews" className="text-muted-foreground hover:text-foreground/80 shrink-0">
            Crews
          </Link>
          {(crewName || agentName) && <span className="text-muted-foreground/50 shrink-0">/</span>}
          {crewName && (
            <span className={cn("truncate", agentName ? "text-foreground/70" : "text-foreground/90 font-medium")}>
              {crewName}
            </span>
          )}
          {agentName && <span className="text-muted-foreground/50 shrink-0">/</span>}
          {agentName && <span className="text-foreground font-medium truncate">{agentName}</span>}
        </nav>

        <span className="text-muted-foreground/40 mx-1 shrink-0">·</span>

        {/* Filters */}
        <div className="flex items-center gap-1.5 text-xs shrink-0">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                className={cn(
                  "px-2 py-1 rounded border transition-colors flex items-center gap-1.5 shrink-0",
                  statusFilter === "all"
                    ? "border-white/10 text-foreground/80 hover:bg-white/5"
                    : "border-blue-500/45 bg-blue-500/15 text-blue-300",
                )}
              >
                Status: {statusLabel}
                <ChevronDown className="h-2.5 w-2.5 opacity-60" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="min-w-[140px]">
              {STATUS_OPTIONS.map((opt) => (
                <DropdownMenuItem
                  key={opt.value}
                  onClick={() => onStatusChange(opt.value)}
                  className={cn("text-xs gap-2", statusFilter === opt.value && "font-medium text-blue-300")}
                >
                  {opt.dot ? <span className={cn("h-2 w-2 rounded-full", opt.dot)} /> : <span className="h-2 w-2" />}
                  {opt.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                className={cn(
                  "px-2 py-1 rounded border transition-colors flex items-center gap-1.5 shrink-0",
                  roleFilter === "all"
                    ? "border-white/10 text-foreground/80 hover:bg-white/5"
                    : "border-blue-500/45 bg-blue-500/15 text-blue-300",
                )}
              >
                Role: {roleLabel}
                <ChevronDown className="h-2.5 w-2.5 opacity-60" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="min-w-[140px]">
              {ROLE_OPTIONS.map((opt) => (
                <DropdownMenuItem
                  key={opt.value}
                  onClick={() => onRoleChange(opt.value)}
                  className={cn("text-xs", roleFilter === opt.value && "font-medium text-blue-300")}
                >
                  {opt.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>

        {/* Create CTAs */}
        <div className="ml-auto flex items-center gap-1.5 shrink-0">
          <button
            type="button"
            onClick={() => setCreateCrewOpen(true)}
            className="text-xs px-2.5 py-1 rounded border border-white/10 hover:bg-white/5 text-foreground flex items-center gap-1.5"
          >
            <Plus className="h-3 w-3" />
            Crew
          </button>
          <button
            type="button"
            data-crews-add-agent
            onClick={() => {
              setCreateAgentDefaultCrew(crewSlug)
              setCreateAgentOpen(true)
            }}
            className="text-xs px-2.5 py-1 rounded bg-blue-500 hover:bg-blue-400 text-white flex items-center gap-1.5 transition-colors"
            title={crewSlug ? `New agent in ${crewName ?? crewSlug}` : "New agent (pick crew in dialog)"}
          >
            <Plus className="h-3 w-3" />
            Agent
          </button>
        </div>
      </div>

      <CreateCrewDialog
        workspaceId={workspaceId}
        open={createCrewOpen}
        onOpenChange={setCreateCrewOpen}
        onCreated={onCrewCreated}
      />
      <CreateAgentDialog
        workspaceId={workspaceId}
        open={createAgentOpen}
        onOpenChange={setCreateAgentOpen}
        defaultCrewSlug={createAgentDefaultCrew}
        crews={crews}
        onCreated={onAgentCreated}
      />
    </>
  )
}
