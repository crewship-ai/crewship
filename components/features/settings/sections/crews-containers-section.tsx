"use client"

import { useCallback, useEffect, useState } from "react"
import { Box, ChevronRight, Users } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/layout/empty-state"
import { cn } from "@/lib/utils"
import { AnimatedNumber } from "@/components/ui/animated-number"

const crewColorMap: Record<string, string> = {
  blue: "#3b82f6", emerald: "#10b981", violet: "#8b5cf6", amber: "#f59e0b",
  rose: "#f43f5e", cyan: "#06b6d4", lime: "#84cc16", fuchsia: "#d946ef",
}

interface CrewData {
  id: string
  name: string
  slug: string
  color?: string | null
  icon?: string | null
  status?: string
  _count?: { agents: number }
  container_config?: {
    memory_mb?: number
    cpus?: number
    ttl_seconds?: number
  } | null
}

interface CrewsContainersSectionProps {
  workspaceId: string
}

export function CrewsContainersSection({ workspaceId }: CrewsContainersSectionProps) {
  const [crews, setCrews] = useState<CrewData[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedId, setExpandedId] = useState<string | null>(null)

  const fetchCrews = useCallback(async () => {
    try {
      const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data = await res.json()
        setCrews(data)
      }
    } catch { /* ignore */ }
    finally { setLoading(false) }
  }, [workspaceId])

  useEffect(() => { fetchCrews() }, [fetchCrews])

  if (loading) {
    return (
      <div className="space-y-3">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-16 rounded-lg" />
        ))}
      </div>
    )
  }

  const totalAgents = crews.reduce((sum, c) => sum + (c._count?.agents ?? 0), 0)

  return (
    <div className="space-y-4">
      {/* Stats strip */}
      <div className="grid grid-cols-3 gap-3">
        {[
          { label: "Crews", value: crews.length, color: "bg-blue-500" },
          { label: "Agents", value: totalAgents, color: "bg-emerald-500" },
          { label: "Containers", value: crews.length, color: "bg-cyan-500" },
        ].map(({ label, value, color }) => (
          <div key={label} className="bg-card border border-white/[0.06] rounded-lg px-4 py-3">
            <div className="flex items-center gap-1.5 mb-1">
              <div className={`w-1.5 h-1.5 rounded-full ${color}`} />
              <span className="text-[10px] text-muted-foreground/50 uppercase tracking-wider font-medium">
                {label}
              </span>
            </div>
            <div className="text-[18px] font-mono font-semibold text-foreground tabular-nums">
              <AnimatedNumber value={value} />
            </div>
          </div>
        ))}
      </div>

      {/* Crew list */}
      {crews.length === 0 ? (
        <div className="bg-card border border-white/[0.06] rounded-lg p-8">
          <EmptyState
            icon={Box}
            title="No crews yet"
            description="Create your first crew to get started with agent orchestration"
          />
        </div>
      ) : (
        <div className="space-y-2">
          {crews.map((crew) => {
            const isExpanded = expandedId === crew.id
            const resolvedColor = (crew.color && crewColorMap[crew.color]) || "#64748b"

            return (
              <div
                key={crew.id}
                className="bg-card border border-white/[0.06] rounded-lg overflow-hidden transition-colors hover:border-white/[0.1]"
              >
                {/* Crew header */}
                <button
                  className="w-full flex items-center gap-3 px-4 py-3 text-left"
                  onClick={() => setExpandedId(isExpanded ? null : crew.id)}
                >
                  <ChevronRight
                    className={cn(
                      "h-3.5 w-3.5 text-muted-foreground/40 transition-transform duration-150 shrink-0",
                      isExpanded && "rotate-90",
                    )}
                  />
                  <div
                    className="w-3 h-3 rounded-full shrink-0"
                    style={{ backgroundColor: resolvedColor }}
                  />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-[13px] font-medium text-foreground truncate">
                        {crew.name}
                      </span>
                      <span className="text-[11px] text-muted-foreground/40 font-mono">
                        {crew.slug}
                      </span>
                    </div>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <div className="flex items-center gap-1 text-[11px] text-muted-foreground/50 font-mono">
                      <Users className="h-3 w-3" />
                      <span className="tabular-nums">{crew._count?.agents ?? 0}</span>
                    </div>
                    <Badge
                      variant="outline"
                      className={cn(
                        "text-[9px] font-medium",
                        crew.status === "active"
                          ? "border-emerald-500/30 text-emerald-400"
                          : "border-white/[0.08] text-muted-foreground/50",
                      )}
                    >
                      {crew.status ?? "active"}
                    </Badge>
                  </div>
                </button>

                {/* Expanded content */}
                {isExpanded && (
                  <div className="px-4 pb-4 pt-1 border-t border-white/[0.04]">
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mt-3">
                      {/* Container config */}
                      <div className="bg-white/[0.02] border border-white/[0.04] rounded-md p-3">
                        <div className="text-[10px] text-muted-foreground/50 uppercase tracking-wider font-semibold mb-2">
                          Container Config
                        </div>
                        <div className="space-y-1.5 text-[12px]">
                          <div className="flex justify-between">
                            <span className="text-muted-foreground/60">Memory</span>
                            <span className="font-mono text-foreground tabular-nums">
                              {crew.container_config?.memory_mb ?? 512} MB
                            </span>
                          </div>
                          <div className="flex justify-between">
                            <span className="text-muted-foreground/60">CPUs</span>
                            <span className="font-mono text-foreground tabular-nums">
                              {crew.container_config?.cpus ?? 1}
                            </span>
                          </div>
                          <div className="flex justify-between">
                            <span className="text-muted-foreground/60">TTL</span>
                            <span className="font-mono text-foreground tabular-nums">
                              {crew.container_config?.ttl_seconds ?? 3600}s
                            </span>
                          </div>
                        </div>
                      </div>

                      {/* Quick info */}
                      <div className="bg-white/[0.02] border border-white/[0.04] rounded-md p-3">
                        <div className="text-[10px] text-muted-foreground/50 uppercase tracking-wider font-semibold mb-2">
                          Details
                        </div>
                        <div className="space-y-1.5 text-[12px]">
                          <div className="flex justify-between">
                            <span className="text-muted-foreground/60">Agents</span>
                            <span className="font-mono text-foreground tabular-nums">
                              {crew._count?.agents ?? 0}
                            </span>
                          </div>
                          <div className="flex justify-between">
                            <span className="text-muted-foreground/60">Color</span>
                            <div className="flex items-center gap-1.5">
                              <div
                                className="w-2.5 h-2.5 rounded-full"
                                style={{ backgroundColor: resolvedColor }}
                              />
                              <span className="font-mono text-foreground">{crew.color ?? "default"}</span>
                            </div>
                          </div>
                          <div className="flex justify-between">
                            <span className="text-muted-foreground/60">Container</span>
                            <span className="font-mono text-foreground">
                              crewship-team-{crew.slug}
                            </span>
                          </div>
                        </div>
                      </div>
                    </div>
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
