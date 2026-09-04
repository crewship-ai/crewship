"use client"

import { Ghost, RotateCcw } from "lucide-react"
import { cn } from "@/lib/utils"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { StatusPill } from "@/components/ui/status-pill"
import { timeAgo } from "@/lib/time"
import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip"
import { isGhost, effectiveStatus, ttlRemaining, latestHireReason } from "@/lib/agent-ephemeral"
import { isLeadRole } from "@/lib/agent-role"

interface AgentData {
  id: string
  name: string
  slug: string
  status: string
  role_title: string | null
  /** Straight from the API, stored rows included — normalised at render. */
  agent_role: string
  crew_id: string | null
  avatar_seed?: string | null
  avatar_style?: string | null
  /** Stored avatar render (#1297); null means generate from the seed. */
  avatar_url?: string | null
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

/** The five columns above `md`; below it every row is a one-column card. */
const GRID = "md:grid md:grid-cols-[1fr_140px_180px_120px_130px] md:items-center md:gap-3"

/**
 * No-selection state for the canvas: the agent roster. A dense table on a
 * desktop; on a phone each row stacks to avatar · name · crew and role ·
 * status, because the fixed five-column grid clipped the Agent column away at
 * 390 and 820 (audit-fleet.md §6 P1 5).
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
    <div className="px-4 md:px-8 lg:px-12 py-8 md:py-12 detail-width">
      <div className="text-center mb-8 md:mb-10">
        <h1 className="text-2xl md:text-3xl font-semibold mb-2">Your fleet</h1>
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
          <div className="rounded-xl border border-white/8 bg-card overflow-hidden" data-testid="fleet-roster">
            <div className={cn("hidden px-4 py-2.5 border-b border-white/8 text-[10px] uppercase tracking-wide text-muted-foreground", GRID)}>
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
                const crew = a.crew_id ? crewById.get(a.crew_id) : null
                const ttl = a.ephemeral && !ghost ? ttlRemaining(a.expires_at) : ""
                const leadName = a.parent_lead_id ? nameById.get(a.parent_lead_id) : null
                const reason = latestHireReason(a.hire_reason)
                const statusLabel = ghost && a.expired_at ? `Expired · ${timeAgo(a.expired_at)}` : undefined

                return (
                  <div
                    key={a.id}
                    data-expired={ghost ? "true" : undefined}
                    className={cn(
                      "group relative",
                      ghost &&
                        "opacity-60 grayscale-[0.4] hover:opacity-100 hover:grayscale-0 focus-within:opacity-100 focus-within:grayscale-0 transition-[opacity,filter] duration-150",
                    )}
                  >
                    <button
                      type="button"
                      onClick={() => onAgentSelect(a.slug)}
                      className={cn(
                        "w-full min-h-11 px-4 py-2.5 hover:bg-white/[0.03] text-left",
                        "flex items-center gap-3",
                        GRID,
                      )}
                    >
                      <span className="flex items-center gap-2.5 min-w-0 flex-1 md:flex-none">
                        <AgentAvatar
                          seed={a.avatar_seed || a.name}
                          style={a.avatar_style || a.crew?.avatar_style}
                          agentId={a.id}
                          avatarUrl={a.avatar_url}
                          className="h-8 w-8 md:h-6 md:w-6 rounded-lg md:rounded-full shrink-0"
                        />
                        <span className="min-w-0">
                          <span className="flex items-center gap-1.5 min-w-0">
                            <span className="truncate font-medium md:font-normal">{a.name}</span>
                            {/* Only a lead earns a badge, and it says LEAD rather
                              * than whatever token the row carries (#2197). */}
                            {isLeadRole(a.agent_role) && (
                              <span className="text-[8px] px-1 rounded bg-purple/20 text-purple-hover shrink-0">
                                LEAD
                              </span>
                            )}
                            {a.ephemeral && (
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <span className="text-[8px] px-1 rounded bg-notice/15 text-notice shrink-0 inline-flex items-center gap-0.5">
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
                          {/* Phone: crew and role on one line under the name. */}
                          <span className="md:hidden block truncate text-[11px] text-muted-foreground">
                            {[crew?.name, a.role_title].filter(Boolean).join(" · ") || "—"}
                          </span>
                        </span>
                      </span>
                      <span className="hidden md:block text-muted-foreground truncate">{crew?.name ?? "—"}</span>
                      <span className="hidden md:block text-muted-foreground truncate">{a.role_title || "—"}</span>
                      <span className="hidden md:block text-muted-foreground text-xs">{timeSince(a.last_active_at)}</span>
                      <span className="shrink-0">
                        <StatusPill status={statusKey} label={statusLabel} live={a.status === "RUNNING" && !ghost} />
                      </span>
                    </button>

                    {ghost && (
                      // Sibling of the selection button (not nested) so we
                      // don't put a button inside a button. Reveals on hover
                      // or keyboard focus.
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
                        className="absolute right-3 top-1/2 -translate-y-1/2 h-6 gap-1 px-2 text-[10px] opacity-0 group-hover:opacity-100 focus-visible:opacity-100 transition-opacity"
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
        <code className="bg-muted px-1.5 py-0.5 rounded">crewship agent list</code>{" "}
        ·{" "}
        <code className="bg-muted px-1.5 py-0.5 rounded">
          crewship agent update &lt;slug&gt; --crew &lt;crew&gt;
        </code>
      </div>
    </div>
  )
}
