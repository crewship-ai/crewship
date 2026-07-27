"use client"

import { useCallback, useEffect, useMemo, useState } from "react"
import {
  Box,
  ChevronRight,
  Globe,
  Save,
  Search,
  Shield,
  Users,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { motion, AnimatePresence } from "motion/react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { ButtonGroup } from "@/components/ui/button-group"
import { StatusBadge, StatusDot } from "@/components/ui/status-badge"
import { Skeleton } from "@/components/ui/skeleton"
import { AnimatedNumber } from "@/components/ui/animated-number"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { resolveCrewColor } from "@/lib/colors"
import { apiFetch } from "@/lib/api-fetch"
import { useAbilities } from "@/hooks/use-abilities"
import { isAdminTier } from "@/lib/permissions/tiers"
import { SettingsCard, SettingsRow, SettingsEmpty } from "../shared"
import { PrivilegedCredentialsCard } from "./privileged-credentials-card"


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

/** Reuse the picker's wording so read-only and editable rows never disagree. */
function memoryLabel(mb: number): string {
  return MEMORY_OPTIONS.find((o) => Number(o.value) === mb)?.label ?? `${mb} MB`
}

function cpuLabel(cpus: number): string {
  return CPU_OPTIONS.find((o) => Number(o.value) === cpus)?.label ?? String(cpus)
}

/**
 * Container limits as plain text, for callers below the ADMIN tier.
 *
 * Deliberately renders nothing focusable: a disabled input still reads as
 * "try me", and the PATCH behind it can only answer 403 for these roles.
 */
function CrewLimitsReadOnly({ crew }: { crew: CrewData }) {
  const restricted = (crew.network_mode ?? "free") === "restricted"
  const value = (v: React.ReactNode) => (
    <span className="text-xs text-muted-foreground">{v}</span>
  )
  return (
    <>
      <SettingsRow label="Memory">
        {value(memoryLabel(crew.container_memory_mb ?? 512))}
      </SettingsRow>
      <SettingsRow label="CPUs">
        {value(cpuLabel(crew.container_cpus ?? 1))}
      </SettingsRow>
      <SettingsRow label="Network mode" border={restricted}>
        {value(restricted ? "Restricted" : "Free")}
      </SettingsRow>
      {restricted && (
        <SettingsRow
          label="Allowed domains"
          border={false}
          className="items-start"
        >
          <span className="text-xs text-muted-foreground text-right break-words">
            {crew.allowed_domains?.trim() || "None"}
          </span>
        </SettingsRow>
      )}
      {/* Said once, quietly. For most of the workspace this is the normal
          state, not a failure — muted copy, no alert colour. */}
      <p className="px-4 pb-2.5 text-[11px] text-muted-foreground">
        Container limits are managed by workspace admins.
      </p>
    </>
  )
}

export function CrewsContainersSection({
  workspaceId,
}: CrewsContainersSectionProps) {
  // Container limits are written by PATCH /api/v1/crews/{id}, which the server
  // gates at `roleManage` — ADMIN and up. Gate on the tier helper rather than
  // CASL: CASL grants MANAGER `update Crew`, so gating there would hand a
  // MANAGER an editing form whose Save can only ever 403 (the exact mismatch
  // lib/permissions/tiers.ts documents). Below ADMIN the values ship as text.
  const { role } = useAbilities()
  const canEdit = isAdminTier(role)

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
      const res = await apiFetch(`/api/v1/crews?workspace_id=${workspaceId}`)
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
        const res = await apiFetch(
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
        const res = await apiFetch(
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
        {/* The privileged-credentials override is workspace-scoped, so it
            still applies (and must stay visible) when no crews exist yet. */}
        <PrivilegedCredentialsCard workspaceId={workspaceId} />
        <SettingsCard
          title="Crews"
          description="Per-crew container limits, network policies, and allowed domains"
        >
          <SettingsEmpty>
            <div className="flex flex-col items-center gap-2 py-6">
              <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
                <Box className="h-4 w-4 text-muted-foreground" />
              </div>
              <div className="text-sm font-medium text-foreground/80">
                No crews yet
              </div>
              <div className="max-w-xs">
                Create your first crew to get started with agent orchestration
              </div>
            </div>
          </SettingsEmpty>
        </SettingsCard>
      </div>
    )
  }

  return (
    <div className="space-y-5">
      {/* Workspace-level security override (#1378) — independent of any crew. */}
      <PrivilegedCredentialsCard workspaceId={workspaceId} />

      <SettingsCard
        title="Overview"
        description="Resource footprint across all crews on this workspace"
      >
        <SettingsRow label="Crews">
          <span className="text-xs font-mono tabular-nums text-foreground">
            <AnimatedNumber value={crews.length} />
          </span>
        </SettingsRow>
        <SettingsRow label="Agents">
          <span className="text-xs font-mono tabular-nums text-foreground">
            <AnimatedNumber value={totalAgents} />
          </span>
        </SettingsRow>
        <SettingsRow label="Containers" border={false}>
          <span className="text-xs font-mono tabular-nums text-foreground">
            <AnimatedNumber value={crews.length} />
          </span>
        </SettingsRow>
      </SettingsCard>

      <SettingsCard
        title="Crews"
        description="Per-crew container limits, network policies, and allowed domains"
        actions={
          crews.length >= 5 && (
            <div className="relative shrink-0">
              <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search crews…"
                className="h-7 w-[180px] pl-7 text-xs"
              />
            </div>
          )
        }
      >
        {filteredCrews.length === 0 ? (
          <SettingsEmpty>No crews matching &quot;{search}&quot;</SettingsEmpty>
        ) : (
          filteredCrews.map((crew, index) => {
            const resolvedColor = resolveCrewColor(crew.color)
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
                {/* Crew row (accordion trigger) */}
                <Button
                  type="button"
                  variant="ghost"
                  aria-expanded={isExpanded}
                  onClick={() => setExpandedId(isExpanded ? null : crew.id)}
                  className={cn(
                    "h-auto w-full justify-start gap-3 rounded-none px-4 py-2 font-normal",
                    (isExpanded || !isLast) && "border-b border-border/40",
                  )}
                >
                  <motion.div
                    animate={{ rotate: isExpanded ? 90 : 0 }}
                    transition={{ duration: 0.15 }}
                  >
                    {/* size-* rather than h-/w-, so Button's own icon sizing
                        rule leaves these alone. */}
                    <ChevronRight className="size-3.5 text-muted-foreground" />
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
                      <Users className="size-3" />
                      {crew._count?.agents ?? 0}
                    </div>
                    <StatusBadge
                      status={
                        (crew.status ?? "active") === "active"
                          ? "COMPLETED"
                          : "PENDING"
                      }
                      label={crew.status ?? "active"}
                    />
                  </div>
                </Button>

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
                        {!canEdit ? (
                          <CrewLimitsReadOnly crew={crew} />
                        ) : (
                          <>
                            {/* Memory */}
                            <SettingsRow label="Memory">
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
                                  aria-label="Memory"
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
                            </SettingsRow>

                            {/* CPUs */}
                            <SettingsRow label="CPUs">
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
                                  aria-label="CPUs"
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
                            </SettingsRow>

                            {/* Network mode */}
                            <SettingsRow
                              label="Network mode"
                              border={
                                draft.network_mode === "restricted" ||
                                hasChanges
                              }
                            >
                              <ButtonGroup>
                                <Button
                                  type="button"
                                  variant="outline"
                                  size="xs"
                                  aria-pressed={draft.network_mode === "free"}
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    updateDraft(crew.id, {
                                      network_mode: "free",
                                    })
                                  }}
                                  className={cn(
                                    "h-7 text-label",
                                    draft.network_mode === "free"
                                      ? "bg-accent text-foreground"
                                      : "text-muted-foreground",
                                  )}
                                >
                                  <Globe className="size-3" />
                                  Free
                                </Button>
                                <Button
                                  type="button"
                                  variant="outline"
                                  size="xs"
                                  aria-pressed={
                                    draft.network_mode === "restricted"
                                  }
                                  onClick={(e) => {
                                    e.stopPropagation()
                                    updateDraft(crew.id, {
                                      network_mode: "restricted",
                                    })
                                  }}
                                  className={cn(
                                    "h-7 text-label",
                                    draft.network_mode === "restricted"
                                      ? "bg-accent text-foreground"
                                      : "text-muted-foreground",
                                  )}
                                >
                                  <Shield className="size-3" />
                                  Restricted
                                </Button>
                              </ButtonGroup>
                            </SettingsRow>

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
                                  <SettingsRow
                                    label="Allowed domains"
                                    description="Comma-separated"
                                    border={hasChanges}
                                    className="items-start"
                                  >
                                    <Textarea
                                      aria-label="Allowed domains"
                                      value={draft.allowed_domains}
                                      onChange={(e) =>
                                        updateDraft(crew.id, {
                                          allowed_domains: e.target.value,
                                        })
                                      }
                                      placeholder="github.com, api.openai.com, registry.npmjs.org"
                                      rows={2}
                                      className="w-[280px] min-h-0 resize-none text-label"
                                    />
                                  </SettingsRow>
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
                                            <Spinner className="mr-1.5 size-3" />
                                          ) : (
                                            <Save className="mr-1.5 size-3" />
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
                                            <Spinner className="mr-1.5 size-3" />
                                          ) : (
                                            <Save className="mr-1.5 size-3" />
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
                          </>
                        )}
                      </div>
                    </motion.div>
                  )}
                </AnimatePresence>
              </div>
            )
          })
        )}
      </SettingsCard>
    </div>
  )
}
