"use client"

import { Ghost, RotateCcw } from "lucide-react"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { timeAgo } from "@/lib/time"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { isGhost, effectiveStatus, ttlRemaining, latestHireReason } from "@/lib/agent-ephemeral"

interface AgentData {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  agent_role: string
  crew_id: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  last_active_at?: string | null
  crew?: { name: string; slug: string; avatar_style?: string | null } | null
  // PR-D F5 ephemeral lifecycle (server returns these; absent on permanent agents).
  ephemeral?: boolean
  expires_at?: string | null
  expired_at?: string | null
  parent_lead_id?: string | null
  hire_reason?: string | null
}

interface CrewData {
  id: string
  slug: string
  name: string
}

export interface EmptyRosterProps {
  agents: AgentData[]
  crews: CrewData[]
  onAgentSelect: (slug: string) => void
}

const STATUS_COLORS: Record<string, { label: string; dot: string; text: string; pulse?: boolean }> = {
  RUNNING: { label: "Running", dot: "bg-emerald-400", text: "text-emerald-400", pulse: true },
  IDLE: { label: "Idle", dot: "bg-zinc-500", text: "text-muted-foreground" },
  ERROR: { label: "Error", dot: "bg-red-500", text: "text-red-400" },
  STOPPED: { label: "Stopped", dot: "bg-amber-500", text: "text-amber-400" },
  PENDING_REVIEW: { label: "Pending review", dot: "bg-amber-400", text: "text-amber-300" },
  EXPIRED: { label: "Expired", dot: "bg-slate-500", text: "text-slate-400" },
}

function timeSince(iso: string | null | undefined): string {
  if (!iso) return "—"
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts)) return "—"
  const diffMs = Date.now() - ts
  const m = Math.floor(diffMs / 60000)
  if (m < 1) return "just now"
  if (m < 60) return `${m} min ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  return `${d}d ago`
}

/**
 * No-selection state for the canvas: a flat agent roster table. Replaces
 * the previous "All crews & agents" card grid — denser and easier to
 * scan once you have 12+ agents.
 *
 * Click any row to drill into the agent canvas. Ephemeral ("hired") agents
 * carry an EPHEMERAL badge with their TTL / hire reason; once their TTL
 * lapses (expired_at set) the row dims to a ghost and offers Rehire.
 */
export function EmptyRoster({ agents, crews, onAgentSelect }: EmptyRosterProps) {
  const crewById = new Map(crews.map((c) => [c.id, c]))
  // Resolve parent_lead_id → lead name for the "Hired by …" tooltip.
  const nameById = new Map(agents.map((a) => [a.id, a.name]))

  return (
    <div className="px-6 md:px-8 lg:px-12 py-12 max-w-[1180px] mx-auto w-full">
      <div className="text-center mb-10">
        <h1 className="text-3xl font-semibold mb-2">Your fleet</h1>
        <p className="text-muted-foreground text-sm">
          Pick a crew or agent on the left, or create something via the toolbar.
        </p>
      </div>

      {agents.length === 0 ? (
        <div className="rounded-xl border border-white/8 bg-card p-12 text-center">
          <p className="text-sm text-muted-foreground mb-2">No agents yet</p>
          <p className="text-xs text-muted-foreground">
            Use the <span className="text-foreground/80">+ Crew</span> and{" "}
            <span className="text-foreground/80">+ Agent</span> buttons in the toolbar to start.
          </p>
        </div>
      ) : (
        <TooltipProvider delayDuration={150}>
          <div className="rounded-xl border border-white/8 bg-card overflow-hidden">
            <div className="grid grid-cols-[1fr_140px_180px_120px_120px] gap-3 px-4 py-2.5 border-b border-white/8 text-[10px] uppercase tracking-wide text-muted-foreground">
              <span>Agent</span>
              <span>Crew</span>
              <span>Role</span>
              <span>Last active</span>
              <span>Status</span>
            </div>
            <div className="divide-y divide-white/5 text-sm">
              {agents.map((a) => {
                const ghost = isGhost(a)
                const statusKey = effectiveStatus(a)
                const status = STATUS_COLORS[statusKey] || STATUS_COLORS.IDLE
                const crew = a.crew_id ? crewById.get(a.crew_id) : null
                const ttl = a.ephemeral && !ghost ? ttlRemaining(a.expires_at) : ""
                const leadName = a.parent_lead_id ? nameById.get(a.parent_lead_id) : null
                const reason = latestHireReason(a.hire_reason)
                const statusLabel = ghost && a.expired_at
                  ? `Expired · ${timeAgo(a.expired_at)}`
                  : status.label

                return (
                  <div
                    key={a.id}
                    data-expired={ghost ? "true" : undefined}
                    className={cn(
                      "group relative",
                      ghost &&
                        "opacity-60 grayscale-[0.4] hover:opacity-100 hover:grayscale-0 transition-[opacity,filter] duration-150",
                    )}
                  >
                    <button
                      type="button"
                      onClick={() => onAgentSelect(a.slug)}
                      className="w-full grid grid-cols-[1fr_140px_180px_120px_120px] gap-3 px-4 py-2.5 hover:bg-white/[0.03] text-left items-center"
                    >
                      <span className="flex items-center gap-2.5 min-w-0">
                        <img
                          src={getAgentAvatarUrl(a.avatar_seed || a.name, a.avatar_style || a.crew?.avatar_style)}
                          alt=""
                          className="h-6 w-6 rounded-full shrink-0"
                        />
                        <span className="truncate">{a.name}</span>
                        {a.agent_role !== "AGENT" && (
                          <span className="text-[8px] px-1 rounded bg-violet-500/20 text-violet-300 shrink-0">
                            {a.agent_role}
                          </span>
                        )}
                        {a.ephemeral && (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <span className="text-[8px] px-1 rounded bg-cyan-500/15 text-cyan-300 shrink-0 inline-flex items-center gap-0.5">
                                {ghost && <Ghost className="h-2.5 w-2.5" />}
                                EPHEMERAL
                              </span>
                            </TooltipTrigger>
                            <TooltipContent side="top" className="max-w-xs text-xs">
                              <div className="font-medium">Ephemeral hire{leadName ? ` · by ${leadName}` : ""}</div>
                              {ttl && <div className="text-muted-foreground">TTL {ttl}</div>}
                              {ghost && a.expired_at && (
                                <div className="text-muted-foreground">Expired {timeAgo(a.expired_at)}</div>
                              )}
                              {reason && <div className="mt-0.5 text-muted-foreground">Reason: {reason}</div>}
                            </TooltipContent>
                          </Tooltip>
                        )}
                      </span>
                      <span className="text-muted-foreground truncate">{crew?.name ?? "—"}</span>
                      <span className="text-muted-foreground truncate">{a.role_title || "—"}</span>
                      <span className="text-muted-foreground text-xs">{timeSince(a.last_active_at)}</span>
                      <span className={cn("text-[10px] flex items-center gap-1.5", status.text)}>
                        <span className={cn("w-1.5 h-1.5 rounded-full shrink-0", status.dot)} />
                        <span className="truncate">{statusLabel}</span>
                      </span>
                    </button>

                    {ghost && (
                      // Sibling of the selection button (not nested) so we
                      // don't put a button inside a button. Reveals on hover.
                      <Button
                        size="sm"
                        variant="outline"
                        onClick={() =>
                          window.dispatchEvent(
                            new CustomEvent("agent.rehire.request", {
                              detail: { agentId: a.id, agentName: a.name },
                            }),
                          )
                        }
                        className="absolute right-3 top-1/2 -translate-y-1/2 h-6 gap-1 px-2 text-[10px] opacity-0 group-hover:opacity-100 transition-opacity"
                      >
                        <RotateCcw className="h-3 w-3" />
                        Rehire
                      </Button>
                    )}
                  </div>
                )
              })}
            </div>
          </div>
        </TooltipProvider>
      )}

      <div className="mt-6 text-center text-xs text-muted-foreground">
        Bulk operations live in the CLI:{" "}
        <code className="bg-zinc-800 px-1.5 py-0.5 rounded">crewship agent list</code>{" "}
        ·{" "}
        <code className="bg-zinc-800 px-1.5 py-0.5 rounded">
          crewship agent update &lt;slug&gt; --crew &lt;crew&gt;
        </code>
      </div>
    </div>
  )
}
