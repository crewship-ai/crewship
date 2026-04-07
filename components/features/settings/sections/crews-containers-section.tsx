"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import { Box, ChevronRight, Globe, Save, Shield, Users } from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import { toast } from "sonner"
import { Badge } from "@/components/ui/badge"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/layout/empty-state"
import { AnimatedNumber } from "@/components/ui/animated-number"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"

const crewColorMap: Record<string, string> = {
  blue: "#3b82f6",
  emerald: "#10b981",
  violet: "#8b5cf6",
  amber: "#f59e0b",
  rose: "#f43f5e",
  cyan: "#06b6d4",
  lime: "#84cc16",
  fuchsia: "#d946ef",
}

const MEMORY_OPTIONS = [
  { value: "512", label: "512 MB" },
  { value: "1024", label: "1 GB" },
  { value: "2048", label: "2 GB" },
  { value: "4096", label: "4 GB" },
  { value: "8192", label: "8 GB" },
] as const

const CPU_OPTIONS = [
  { value: "0.5", label: "0.5" },
  { value: "1", label: "1" },
  { value: "2", label: "2" },
  { value: "4", label: "4" },
] as const

interface CrewData {
  id: string
  name: string
  slug: string
  color?: string | null
  icon?: string | null
  status?: string
  container_memory_mb?: number
  container_cpus?: number
  container_ttl_hours?: number
  network_mode?: string
  allowed_domains?: string
  _count?: { agents: number }
}

interface CrewDraft {
  container_memory_mb: number
  container_cpus: number
  network_mode: string
  allowed_domains: string
}

interface CrewsContainersSectionProps {
  workspaceId: string
}

function buildDraft(crew: CrewData): CrewDraft {
  return {
    container_memory_mb: crew.container_memory_mb ?? 512,
    container_cpus: crew.container_cpus ?? 1,
    network_mode: crew.network_mode ?? "free",
    allowed_domains: crew.allowed_domains ?? "",
  }
}

function hasResourceChanges(draft: CrewDraft, crew: CrewData): boolean {
  const origMemory = crew.container_memory_mb ?? 512
  const origCpus = crew.container_cpus ?? 1
  return draft.container_memory_mb !== origMemory || draft.container_cpus !== origCpus
}

function hasNetworkChanges(draft: CrewDraft, crew: CrewData): boolean {
  const origMode = crew.network_mode ?? "free"
  const origDomains = crew.allowed_domains ?? ""
  return draft.network_mode !== origMode || draft.allowed_domains !== origDomains
}

export function CrewsContainersSection({ workspaceId }: CrewsContainersSectionProps) {
  const [crews, setCrews] = useState<CrewData[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [drafts, setDrafts] = useState<Record<string, CrewDraft>>({})
  const [savingResources, setSavingResources] = useState<Record<string, boolean>>({})
  const [savingNetwork, setSavingNetwork] = useState<Record<string, boolean>>({})

  const fetchCrews = useCallback(async () => {
    try {
      const res = await fetch(`/api/v1/crews?workspace_id=${workspaceId}`)
      if (res.ok) {
        const data = await res.json()
        setCrews(data)
        const initialDrafts: Record<string, CrewDraft> = {}
        for (const crew of data as CrewData[]) {
          initialDrafts[crew.id] = buildDraft(crew)
        }
        setDrafts(initialDrafts)
      }
    } catch {
      /* ignore */
    } finally {
      setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    fetchCrews()
  }, [fetchCrews])

  const updateDraft = useCallback(
    (crewId: string, patch: Partial<CrewDraft>) => {
      setDrafts((prev) => ({
        ...prev,
        [crewId]: { ...prev[crewId], ...patch },
      }))
    },
    [],
  )

  const saveResources = useCallback(
    async (crew: CrewData) => {
      const draft = drafts[crew.id]
      if (!draft) return
      setSavingResources((prev) => ({ ...prev, [crew.id]: true }))
      try {
        const res = await fetch(
          `/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              container_memory_mb: draft.container_memory_mb,
              container_cpus: draft.container_cpus,
            }),
          },
        )
        if (!res.ok) throw new Error("Failed to update container resources")
        setCrews((prev) =>
          prev.map((c) =>
            c.id === crew.id
              ? {
                  ...c,
                  container_memory_mb: draft.container_memory_mb,
                  container_cpus: draft.container_cpus,
                }
              : c,
          ),
        )
        toast.success(`Updated container resources for ${crew.name}`)
      } catch {
        toast.error("Failed to save container resources")
      } finally {
        setSavingResources((prev) => ({ ...prev, [crew.id]: false }))
      }
    },
    [drafts, workspaceId],
  )

  const saveNetwork = useCallback(
    async (crew: CrewData) => {
      const draft = drafts[crew.id]
      if (!draft) return
      setSavingNetwork((prev) => ({ ...prev, [crew.id]: true }))
      try {
        const res = await fetch(
          `/api/v1/crews/${crew.id}?workspace_id=${workspaceId}`,
          {
            method: "PATCH",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              network_mode: draft.network_mode,
              allowed_domains: draft.allowed_domains,
            }),
          },
        )
        if (!res.ok) throw new Error("Failed to update network settings")
        setCrews((prev) =>
          prev.map((c) =>
            c.id === crew.id
              ? {
                  ...c,
                  network_mode: draft.network_mode,
                  allowed_domains: draft.allowed_domains,
                }
              : c,
          ),
        )
        toast.success(`Updated network settings for ${crew.name}`)
      } catch {
        toast.error("Failed to save network settings")
      } finally {
        setSavingNetwork((prev) => ({ ...prev, [crew.id]: false }))
      }
    },
    [drafts, workspaceId],
  )

  const totalAgents = useMemo(
    () => crews.reduce((sum, c) => sum + (c._count?.agents ?? 0), 0),
    [crews],
  )

  if (loading) {
    return (
      <div className="space-y-3">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-16 rounded-lg" />
        ))}
      </div>
    )
  }

  return (
    <div className="space-y-4">
      {/* Stats strip */}
      <div className="grid grid-cols-3 gap-3">
        {[
          { label: "Crews", value: crews.length, color: "bg-blue-500" },
          { label: "Agents", value: totalAgents, color: "bg-emerald-500" },
          { label: "Containers", value: crews.length, color: "bg-cyan-500" },
        ].map(({ label, value, color }) => (
          <div
            key={label}
            className="bg-card border border-white/[0.06] rounded-lg px-4 py-3"
          >
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
          {crews.map((crew, index) => {
            const isExpanded = expandedId === crew.id
            const resolvedColor =
              (crew.color && crewColorMap[crew.color]) || "#64748b"
            const draft = drafts[crew.id]
            const resourceChanged = draft ? hasResourceChanges(draft, crew) : false
            const networkChanged = draft ? hasNetworkChanges(draft, crew) : false

            return (
              <motion.div
                key={crew.id}
                initial={{ opacity: 0, y: 8 }}
                animate={{ opacity: 1, y: 0 }}
                transition={{ duration: 0.25, delay: index * 0.05 }}
                className="bg-card border border-white/[0.06] rounded-lg overflow-hidden transition-colors hover:border-white/[0.1]"
              >
                {/* Crew header */}
                <button
                  className="w-full flex items-center gap-3 px-4 py-3 text-left"
                  onClick={() =>
                    setExpandedId(isExpanded ? null : crew.id)
                  }
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
                      <span className="tabular-nums">
                        {crew._count?.agents ?? 0}
                      </span>
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
                <AnimatePresence initial={false}>
                  {isExpanded && draft && (
                    <motion.div
                      initial={{ height: 0, opacity: 0 }}
                      animate={{ height: "auto", opacity: 1 }}
                      exit={{ height: 0, opacity: 0 }}
                      transition={{ duration: 0.2, ease: "easeInOut" }}
                      className="overflow-hidden"
                    >
                      <div className="px-4 pb-4 pt-1 border-t border-white/[0.04]">
                        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mt-3">
                          {/* Container Resources */}
                          <div className="bg-white/[0.02] border border-white/[0.04] rounded-md p-3">
                            <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-3">
                              Container Resources
                            </div>
                            <div className="space-y-3">
                              {/* Memory */}
                              <div className="space-y-1">
                                <label className="text-[11px] text-muted-foreground/50">
                                  Memory
                                </label>
                                <Select
                                  value={String(draft.container_memory_mb)}
                                  onValueChange={(val) =>
                                    updateDraft(crew.id, {
                                      container_memory_mb: Number(val),
                                    })
                                  }
                                >
                                  <SelectTrigger
                                    size="sm"
                                    className="w-full h-8 bg-white/[0.03] border-white/[0.08] text-[12px]"
                                  >
                                    <SelectValue />
                                  </SelectTrigger>
                                  <SelectContent>
                                    {MEMORY_OPTIONS.map((opt) => (
                                      <SelectItem
                                        key={opt.value}
                                        value={opt.value}
                                        className="text-[12px]"
                                      >
                                        {opt.label}
                                      </SelectItem>
                                    ))}
                                  </SelectContent>
                                </Select>
                              </div>

                              {/* CPUs */}
                              <div className="space-y-1">
                                <label className="text-[11px] text-muted-foreground/50">
                                  CPUs
                                </label>
                                <Select
                                  value={String(draft.container_cpus)}
                                  onValueChange={(val) =>
                                    updateDraft(crew.id, {
                                      container_cpus: Number(val),
                                    })
                                  }
                                >
                                  <SelectTrigger
                                    size="sm"
                                    className="w-full h-8 bg-white/[0.03] border-white/[0.08] text-[12px]"
                                  >
                                    <SelectValue />
                                  </SelectTrigger>
                                  <SelectContent>
                                    {CPU_OPTIONS.map((opt) => (
                                      <SelectItem
                                        key={opt.value}
                                        value={opt.value}
                                        className="text-[12px]"
                                      >
                                        {opt.label}
                                      </SelectItem>
                                    ))}
                                  </SelectContent>
                                </Select>
                              </div>

                              {/* Save resources */}
                              <div className="flex items-center gap-2 pt-1">
                                {resourceChanged && (
                                  <motion.button
                                    initial={{ opacity: 0, scale: 0.95 }}
                                    animate={{ opacity: 1, scale: 1 }}
                                    exit={{ opacity: 0, scale: 0.95 }}
                                    disabled={savingResources[crew.id]}
                                    onClick={() => saveResources(crew)}
                                    className="inline-flex items-center gap-1.5 h-[26px] px-2.5 rounded-[4px] text-[11px] font-medium bg-blue-500/15 border border-blue-500/35 text-blue-400 hover:bg-blue-500/25 transition-colors disabled:opacity-50"
                                  >
                                    <Save className="h-3 w-3" />
                                    {savingResources[crew.id]
                                      ? "Saving..."
                                      : "Save"}
                                  </motion.button>
                                )}
                                {resourceChanged && (
                                  <div className="w-1.5 h-1.5 rounded-full bg-amber-500" />
                                )}
                              </div>
                            </div>
                          </div>

                          {/* Network & Security */}
                          <div className="bg-white/[0.02] border border-white/[0.04] rounded-md p-3">
                            <div className="text-[10px] font-semibold text-muted-foreground/25 uppercase tracking-wider mb-3">
                              Network & Security
                            </div>
                            <div className="space-y-3">
                              {/* Network mode toggle */}
                              <div className="space-y-1">
                                <label className="text-[11px] text-muted-foreground/50">
                                  Network Mode
                                </label>
                                <div className="flex gap-0 rounded-md overflow-hidden border border-white/[0.08]">
                                  <button
                                    onClick={() =>
                                      updateDraft(crew.id, {
                                        network_mode: "free",
                                      })
                                    }
                                    className={cn(
                                      "flex-1 flex items-center justify-center gap-1.5 h-8 text-[11px] font-medium transition-colors",
                                      draft.network_mode === "free"
                                        ? "bg-emerald-500/15 text-emerald-400 border-r border-emerald-500/25"
                                        : "bg-white/[0.02] text-muted-foreground/50 border-r border-white/[0.06] hover:bg-white/[0.04]",
                                    )}
                                  >
                                    <Globe className="h-3 w-3" />
                                    Free
                                  </button>
                                  <button
                                    onClick={() =>
                                      updateDraft(crew.id, {
                                        network_mode: "restricted",
                                      })
                                    }
                                    className={cn(
                                      "flex-1 flex items-center justify-center gap-1.5 h-8 text-[11px] font-medium transition-colors",
                                      draft.network_mode === "restricted"
                                        ? "bg-amber-500/15 text-amber-400"
                                        : "bg-white/[0.02] text-muted-foreground/50 hover:bg-white/[0.04]",
                                    )}
                                  >
                                    <Shield className="h-3 w-3" />
                                    Restricted
                                  </button>
                                </div>
                              </div>

                              {/* Allowed domains (restricted only) */}
                              <AnimatePresence initial={false}>
                                {draft.network_mode === "restricted" && (
                                  <motion.div
                                    initial={{ height: 0, opacity: 0 }}
                                    animate={{ height: "auto", opacity: 1 }}
                                    exit={{ height: 0, opacity: 0 }}
                                    transition={{
                                      duration: 0.15,
                                      ease: "easeInOut",
                                    }}
                                    className="overflow-hidden"
                                  >
                                    <div className="space-y-1">
                                      <label className="text-[11px] text-muted-foreground/50">
                                        Allowed Domains
                                      </label>
                                      <textarea
                                        value={draft.allowed_domains}
                                        onChange={(e) =>
                                          updateDraft(crew.id, {
                                            allowed_domains: e.target.value,
                                          })
                                        }
                                        placeholder="github.com, api.openai.com, registry.npmjs.org"
                                        rows={3}
                                        className="w-full resize-none rounded-md bg-white/[0.03] border border-white/[0.08] text-[12px] text-foreground placeholder:text-muted-foreground/30 px-2.5 py-2 focus:outline-none focus:border-white/[0.15] transition-colors"
                                      />
                                      <p className="text-[10px] text-muted-foreground/30">
                                        Comma-separated list of domains the
                                        container can access
                                      </p>
                                    </div>
                                  </motion.div>
                                )}
                              </AnimatePresence>

                              {/* Save network */}
                              <div className="flex items-center gap-2 pt-1">
                                {networkChanged && (
                                  <motion.button
                                    initial={{ opacity: 0, scale: 0.95 }}
                                    animate={{ opacity: 1, scale: 1 }}
                                    exit={{ opacity: 0, scale: 0.95 }}
                                    disabled={savingNetwork[crew.id]}
                                    onClick={() => saveNetwork(crew)}
                                    className="inline-flex items-center gap-1.5 h-[26px] px-2.5 rounded-[4px] text-[11px] font-medium bg-blue-500/15 border border-blue-500/35 text-blue-400 hover:bg-blue-500/25 transition-colors disabled:opacity-50"
                                  >
                                    <Save className="h-3 w-3" />
                                    {savingNetwork[crew.id]
                                      ? "Saving..."
                                      : "Save"}
                                  </motion.button>
                                )}
                                {networkChanged && (
                                  <div className="w-1.5 h-1.5 rounded-full bg-amber-500" />
                                )}
                              </div>
                            </div>
                          </div>
                        </div>
                      </div>
                    </motion.div>
                  )}
                </AnimatePresence>
              </motion.div>
            )
          })}
        </div>
      )}
    </div>
  )
}
