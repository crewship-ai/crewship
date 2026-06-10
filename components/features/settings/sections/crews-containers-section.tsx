"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Box,
  ChevronRight,
  Globe,
  Loader2,
  Save,
  Search,
  Shield,
  Users,
} from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { resolveCrewColor } from "@/lib/colors"


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

function Row({
  label,
  description,
  children,
  border = true,
}: {
  label?: React.ReactNode
  description?: string
  children: React.ReactNode
  border?: boolean
}) {
  return (
    <div
      className={cn(
        "flex items-center justify-between gap-4 px-4 py-2.5",
        border && "border-b border-border/40 last:border-b-0",
      )}
    >
      <div className="shrink-0">
        {typeof label === "string" ? (
          <div className="text-xs text-foreground">{label}</div>
        ) : (
          label
        )}
        {description && (
          <div className="text-[11px] text-muted-foreground/80 mt-0.5">
            {description}
          </div>
        )}
      </div>
      <div className="flex items-center gap-2 min-w-0 justify-end">
        {children}
      </div>
    </div>
  )
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
  return (
    draft.container_memory_mb !== origMemory ||
    draft.container_cpus !== origCpus
  )
}

function hasNetworkChanges(draft: CrewDraft, crew: CrewData): boolean {
  const origMode = crew.network_mode ?? "free"
  const origDomains = crew.allowed_domains ?? ""
  return (
    draft.network_mode !== origMode || draft.allowed_domains !== origDomains
  )
}

export function CrewsContainersSection({
  workspaceId,
}: CrewsContainersSectionProps) {
  const [crews, setCrews] = useState<CrewData[]>([])
  const [loading, setLoading] = useState(true)
  const [drafts, setDrafts] = useState<Record<string, CrewDraft>>({})
  const [savingResources, setSavingResources] = useState<
    Record<string, boolean>
  >({})
  const [savingNetwork, setSavingNetwork] = useState<Record<string, boolean>>(
    {},
  )
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [search, setSearch] = useState("")

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

  const filteredCrews = useMemo(() => {
    if (!search.trim()) return crews
    const q = search.toLowerCase().trim()
    return crews.filter(
      (c) =>
        c.name.toLowerCase().includes(q) ||
        c.slug.toLowerCase().includes(q),
    )
  }, [crews, search])

  if (loading) {
    return (
      <div className="space-y-3">
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className="h-16 rounded-lg" />
        ))}
      </div>
    )
  }

  if (crews.length === 0) {
    return (
      <div className="space-y-5">
        <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
          <div className="flex flex-col items-center justify-center py-12 text-center">
            <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center mb-3">
              <Box className="h-4 w-4 text-muted-foreground" />
            </div>
            <div className="text-sm font-medium text-foreground/80">No crews yet</div>
            <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
              Create your first crew to get started with agent orchestration
            </div>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      {/* Overview section */}
      <section className="space-y-2.5">
        <div>
          <h3 className="text-body font-medium text-foreground/80 leading-none">Overview</h3>
          <p className="text-[11px] text-muted-foreground mt-1 leading-snug">
            Resource footprint across all crews on this workspace
          </p>
        </div>
        <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
          <Row label="Crews">
            <span className="text-xs font-mono tabular-nums text-foreground">
              <AnimatedNumber value={crews.length} />
            </span>
          </Row>
          <Row label="Agents">
            <span className="text-xs font-mono tabular-nums text-foreground">
              <AnimatedNumber value={totalAgents} />
            </span>
          </Row>
          <Row label="Containers" border={false}>
            <span className="text-xs font-mono tabular-nums text-foreground">
              <AnimatedNumber value={crews.length} />
            </span>
          </Row>
        </div>
      </section>

      {/* Crews accordion section */}
      <section className="space-y-2.5">
        <div className="flex items-end justify-between gap-3">
          <div>
            <h3 className="text-body font-medium text-foreground/80 leading-none">Crews</h3>
            <p className="text-[11px] text-muted-foreground mt-1 leading-snug">
              Per-crew container limits, network policies, and allowed domains
            </p>
          </div>
          {crews.length >= 5 && (
            <div className="relative shrink-0">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search crews…"
                className="h-7 w-[180px] pl-7 text-xs"
              />
            </div>
          )}
        </div>

        <div className="rounded-xl border border-border/60 bg-card overflow-hidden">
            {filteredCrews.length === 0 ? (
              <div className="px-4 py-8 text-center text-[11px] text-muted-foreground">
                No crews matching &quot;{search}&quot;
              </div>
            ) : (
              filteredCrews.map((crew, index) => {
                const resolvedColor =
                  resolveCrewColor(crew.color)
                const draft = drafts[crew.id]
                const isExpanded = expandedId === crew.id
                const resourceChanged = draft
                  ? hasResourceChanges(draft, crew)
                  : false
                const networkChanged = draft
                  ? hasNetworkChanges(draft, crew)
                  : false
                const hasChanges = resourceChanged || networkChanged
                const isLast = index === filteredCrews.length - 1

                return (
                  <div key={crew.id}>
                    {/* Crew row (clickable) */}
                    <button
                      type="button"
                      onClick={() =>
                        setExpandedId(isExpanded ? null : crew.id)
                      }
                      className={cn(
                        "flex items-center gap-3 w-full px-4 py-2 text-left transition-colors hover:bg-muted/40",
                        !isLast && !isExpanded && "border-b border-border/40",
                        isExpanded && "border-b border-border/40",
                      )}
                    >
                      <motion.div
                        animate={{ rotate: isExpanded ? 90 : 0 }}
                        transition={{ duration: 0.15 }}
                      >
                        <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
                      </motion.div>
                      <div
                        className="h-2.5 w-2.5 rounded-full shrink-0"
                        style={{ backgroundColor: resolvedColor }}
                      />
                      <span className="text-body text-foreground font-medium truncate">
                        {crew.name}
                      </span>
                      <span className="text-label text-muted-foreground font-mono truncate">
                        {crew.slug}
                      </span>
                      <div className="flex items-center gap-2 ml-auto shrink-0">
                        <div className="flex items-center gap-1 text-label text-muted-foreground font-mono tabular-nums">
                          <Users className="h-3 w-3" />
                          {crew._count?.agents ?? 0}
                        </div>
                        <StatusBadge
                          status={(crew.status ?? "active") === "active" ? "COMPLETED" : "PENDING"}
                          label={crew.status ?? "active"}
                        />
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
                          <div
                            className={cn(
                              "bg-surface-subtle pl-10",
                              !isLast && "border-b border-border/40",
                            )}
                          >
                            {/* Memory */}
                            <Row label="Memory">
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
                                  className="w-[120px] h-8 text-label"
                                >
                                  <SelectValue />
                                </SelectTrigger>
                                <SelectContent>
                                  {MEMORY_OPTIONS.map((opt) => (
                                    <SelectItem
                                      key={opt.value}
                                      value={opt.value}
                                      className="text-label"
                                    >
                                      {opt.label}
                                    </SelectItem>
                                  ))}
                                </SelectContent>
                              </Select>
                            </Row>

                            {/* CPUs */}
                            <Row label="CPUs">
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
                                  className="w-[120px] h-8 text-label"
                                >
                                  <SelectValue />
                                </SelectTrigger>
                                <SelectContent>
                                  {CPU_OPTIONS.map((opt) => (
                                    <SelectItem
                                      key={opt.value}
                                      value={opt.value}
                                      className="text-label"
                                    >
                                      {opt.label}
                                    </SelectItem>
                                  ))}
                                </SelectContent>
                              </Select>
                            </Row>

                            {/* Network mode */}
                            <Row
                              label="Network mode"
                              border={
                                draft.network_mode === "restricted" ||
                                hasChanges
                              }
                            >
                              <div className="flex gap-0 rounded-md overflow-hidden border border-border">
                                <button
                                  type="button"
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    updateDraft(crew.id, {
                                      network_mode: "free",
                                    })
                                  }}
                                  className={cn(
                                    "flex items-center justify-center gap-1.5 h-7 px-3 text-label font-medium transition-colors border-r border-border",
                                    draft.network_mode === "free"
                                      ? "bg-accent text-foreground"
                                      : "bg-transparent text-muted-foreground hover:bg-muted/60",
                                  )}
                                >
                                  <Globe className="h-3 w-3" />
                                  Free
                                </button>
                                <button
                                  type="button"
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    updateDraft(crew.id, {
                                      network_mode: "restricted",
                                    })
                                  }}
                                  className={cn(
                                    "flex items-center justify-center gap-1.5 h-7 px-3 text-label font-medium transition-colors",
                                    draft.network_mode === "restricted"
                                      ? "bg-accent text-foreground"
                                      : "bg-transparent text-muted-foreground hover:bg-muted/60",
                                  )}
                                >
                                  <Shield className="h-3 w-3" />
                                  Restricted
                                </button>
                              </div>
                            </Row>

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
                                  <div
                                    className={cn(
                                      "flex items-start justify-between gap-4 px-4 py-2.5",
                                      hasChanges &&
                                        "border-b border-border/40",
                                    )}
                                  >
                                    <div className="shrink-0 pt-1.5">
                                      <div className="text-body text-foreground">
                                        Allowed domains
                                      </div>
                                      <div className="text-label text-muted-foreground mt-0.5">
                                        Comma-separated
                                      </div>
                                    </div>
                                    <textarea
                                      value={draft.allowed_domains}
                                      onChange={(e) =>
                                        updateDraft(crew.id, {
                                          allowed_domains: e.target.value,
                                        })
                                      }
                                      placeholder="github.com, api.openai.com, registry.npmjs.org"
                                      rows={2}
                                      className="w-[280px] resize-none rounded-md bg-background border border-border text-label text-foreground placeholder:text-muted-foreground px-2.5 py-2 focus:outline-none focus:border-ring transition-colors"
                                    />
                                  </div>
                                </motion.div>
                              )}
                            </AnimatePresence>

                            {/* Save row */}
                            <AnimatePresence initial={false}>
                              {hasChanges && (
                                <motion.div
                                  initial={{ height: 0, opacity: 0 }}
                                  animate={{ height: "auto", opacity: 1 }}
                                  exit={{ height: 0, opacity: 0 }}
                                  transition={{ duration: 0.15 }}
                                  className="overflow-hidden"
                                >
                                  <div className="flex items-center justify-between gap-4 px-4 py-2.5">
                                    <div className="flex items-center gap-2">
                                      <StatusDot status="BLOCKED" />
                                      <span className="text-label text-muted-foreground">
                                        Unsaved changes
                                      </span>
                                    </div>
                                    <div className="flex items-center gap-2">
                                      {resourceChanged && (
                                        <Button
                                          type="button"
                                          size="sm"
                                          disabled={savingResources[crew.id]}
                                          onClick={(e) => {
                                            e.stopPropagation()
                                            saveResources(crew)
                                          }}
                                        >
                                          {savingResources[crew.id] ? (
                                            <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                                          ) : (
                                            <Save className="mr-1.5 h-3 w-3" />
                                          )}
                                          {savingResources[crew.id]
                                            ? "Saving..."
                                            : "Save Resources"}
                                        </Button>
                                      )}
                                      {networkChanged && (
                                        <Button
                                          type="button"
                                          size="sm"
                                          disabled={savingNetwork[crew.id]}
                                          onClick={(e) => {
                                            e.stopPropagation()
                                            saveNetwork(crew)
                                          }}
                                        >
                                          {savingNetwork[crew.id] ? (
                                            <Loader2 className="mr-1.5 h-3 w-3 animate-spin" />
                                          ) : (
                                            <Save className="mr-1.5 h-3 w-3" />
                                          )}
                                          {savingNetwork[crew.id]
                                            ? "Saving..."
                                            : "Save Network"}
                                        </Button>
                                      )}
                                    </div>
                                  </div>
                                </motion.div>
                              )}
                            </AnimatePresence>
                          </div>
                        </motion.div>
                      )}
                    </AnimatePresence>
                  </div>
                )
              })
            )}
        </div>
      </section>
    </div>
  )
}
