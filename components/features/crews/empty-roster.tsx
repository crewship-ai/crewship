"use client"

import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"

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

const STATUS_COLORS: Record<string, { dot: string; text: string; pulse?: boolean }> = {
  RUNNING: { dot: "bg-emerald-400", text: "text-emerald-400", pulse: true },
  IDLE: { dot: "bg-zinc-500", text: "text-muted-foreground" },
  ERROR: { dot: "bg-red-500", text: "text-red-400" },
  STOPPED: { dot: "bg-amber-500", text: "text-amber-400" },
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
 * Click any row to drill into the agent canvas.
 */
export function EmptyRoster({ agents, crews, onAgentSelect }: EmptyRosterProps) {
  const crewById = new Map(crews.map((c) => [c.id, c]))

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
          <p className="text-xs text-muted-foreground/70">
            Use the <span className="text-foreground/80">+ Crew</span> and{" "}
            <span className="text-foreground/80">+ Agent</span> buttons in the toolbar to start.
          </p>
        </div>
      ) : (
        <div className="rounded-xl border border-white/8 bg-card overflow-hidden">
          <div className="grid grid-cols-[1fr_140px_180px_120px_100px] gap-3 px-4 py-2.5 border-b border-white/8 text-[10px] uppercase tracking-wide text-muted-foreground">
            <span>Agent</span>
            <span>Crew</span>
            <span>Role</span>
            <span>Last active</span>
            <span>Status</span>
          </div>
          <div className="divide-y divide-white/5 text-sm">
            {agents.map((a) => {
              const status = STATUS_COLORS[a.status] || STATUS_COLORS.IDLE
              const crew = a.crew_id ? crewById.get(a.crew_id) : null
              return (
                <button
                  key={a.id}
                  type="button"
                  onClick={() => onAgentSelect(a.slug)}
                  className="w-full grid grid-cols-[1fr_140px_180px_120px_100px] gap-3 px-4 py-2.5 hover:bg-white/[0.03] text-left items-center"
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
                  </span>
                  <span className="text-muted-foreground truncate">{crew?.name ?? "—"}</span>
                  <span className="text-muted-foreground truncate">{a.role_title || "—"}</span>
                  <span className="text-muted-foreground text-xs">{timeSince(a.last_active_at)}</span>
                  <span className={cn("text-[10px] flex items-center gap-1.5", status.text)}>
                    <span className={cn("w-1.5 h-1.5 rounded-full", status.dot)} />
                    {a.status?.toLowerCase() || "idle"}
                  </span>
                </button>
              )
            })}
          </div>
        </div>
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
