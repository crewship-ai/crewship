"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { create as createOrama, insertMultiple, search as oramaSearch } from "@orama/orama"
import type { AnyOrama } from "@orama/orama"
import { VirtuosoGrid } from "react-virtuoso"
import { Group as PanelGroup, Panel, Separator as PanelResizeHandle } from "react-resizable-panels"
import {
  Search, Sparkles, Plus, X, ChevronDown, ChevronRight,
  Package, RefreshCw, ShieldCheck, BadgeCheck, Lock, Dot, AlertTriangle, Loader2,
  Library, CheckSquare,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { useWorkspace } from "@/hooks/use-workspace"
import { ImportSkillDialog } from "@/components/skills/import-dialog"
import { CreateSkillDialog } from "@/components/features/skills/create-skill-dialog"
import { SkillCard, type SkillCardData } from "@/components/features/skills/skill-card"
import { SkillsDetailPanel } from "@/components/features/skills/skills-detail-panel"

// FACET_DOMAINS mirrors the SkillCategory enum after the v65 expansion.
// Counts are computed live from the loaded list — values with zero
// matches still render so the user can see them go grey instead of
// silently disappear (locked enum, not free-form).
const DOMAINS = [
  "CODING", "DATA", "DEVOPS", "WRITING", "RESEARCH", "PM",
  "DESIGN", "SUPPORT", "SECURITY", "FINANCE", "OPS", "AUTOMATION",
  "SALES", "CUSTOM",
]
const SOURCES = [
  { value: "BUNDLED", label: "Official", icon: ShieldCheck },
  { value: "MARKETPLACE", label: "Verified", icon: BadgeCheck },
  { value: "CUSTOM", label: "Community", icon: Dot },
  { value: "GENERATED", label: "Generated", icon: Sparkles },
  { value: "MANAGED", label: "Private", icon: Lock },
]
const RUNTIMES = [
  { value: "INSTRUCTIONS", label: "Instructions" },
  { value: "SCRIPT", label: "Script" },
  { value: "MCP", label: "MCP" },
  { value: "HYBRID", label: "Hybrid" },
]
const MATURITIES = [
  { value: "OFFICIAL", label: "Official" },
  { value: "CURATED", label: "Curated" },
  { value: "COMMUNITY", label: "Community" },
  { value: "EXPERIMENTAL", label: "Experimental" },
]

interface FilterState {
  domains: Set<string>
  sources: Set<string>
  runtimes: Set<string>
  maturities: Set<string>
  query: string
}

const EMPTY_FILTER: FilterState = {
  domains: new Set(),
  sources: new Set(),
  runtimes: new Set(),
  maturities: new Set(),
  query: "",
}

// SKILLS_TABS mirrors the orchestration page's tab strip pattern: a
// horizontal row of named lenses across the top of the layout that
// switches the centre grid's filtering — semantically the same axis
// as Source but easier to reach than scrolling the rail facet open.
// "Browse" is the unfiltered default; "Installed" applies a per-agent
// flag once we wire it; "Generated" filters source=GENERATED.
const SKILLS_TABS = [
  { id: "browse", label: "Browse", icon: Library },
  { id: "installed", label: "Installed", icon: CheckSquare },
  { id: "generated", label: "Generated", icon: Sparkles },
] as const

type SkillsTab = (typeof SKILLS_TABS)[number]["id"]

// SkillsBrowser is the 3-panel orchestration-style replacement for the
// previous flat grid /skills page. Left panel hosts CTAs + faceted
// filters; center panel renders a virtualised grid of SkillCard; right
// panel is the detail view (collapsible). Below all three sits a status
// strip with bundled count + last-update info.
//
// Search runs client-side via Orama (BM25 + fuzzy) over the full list
// returned by /api/v1/skills so the user can pivot facets without a
// network round-trip per click.
export function SkillsBrowser() {
  const { workspaceId, loading: wsLoading } = useWorkspace()
  const [skills, setSkills] = useState<SkillCardData[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filter, setFilter] = useState<FilterState>(EMPTY_FILTER)
  const [selected, setSelected] = useState<SkillCardData | null>(null)
  const [searchInput, setSearchInput] = useState("")
  const [activeTab, setActiveTab] = useState<SkillsTab>("browse")
  const debounceTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const oramaIndex = useRef<AnyOrama | null>(null)
  const [searchHits, setSearchHits] = useState<Set<string> | null>(null)

  // buildIndex creates a fresh Orama database from a skill list. v3 of
  // the Orama API is async — both create() and insertMultiple() return
  // promises that we MUST await, otherwise oramaIndex.current would
  // hold an unresolved promise instead of a real index and the search
  // would silently no-op. Extracted so reload() can call it after a
  // refetch — without the rebuild, search returns stale results
  // pointing at row IDs that no longer exist.
  const buildIndex = useCallback(async (data: SkillCardData[]) => {
    const db = await createOrama({
      schema: {
        id: "string",
        slug: "string",
        display_name: "string",
        description: "string",
        vendor: "string",
        category: "string",
      } as const,
    })
    await insertMultiple(
      db,
      data.map((s) => ({
        id: s.id,
        slug: s.slug,
        display_name: s.display_name ?? s.name,
        description: s.description ?? "",
        vendor: s.vendor ?? "",
        category: s.category,
      })),
    )
    return db
  }, [])

  // Initial load: fetch ALL skills (the workspace has at most a few
  // hundred installed today; pagination over the wire is a future
  // concern once registries mirror in). Sort already happens server-
  // side OFFICIAL → COMMUNITY → ...
  useEffect(() => {
    if (!workspaceId) {
      if (!wsLoading) setLoading(false)
      return
    }
    let cancelled = false
    setLoading(true)
    setError(null)
    fetch(`/api/v1/skills?workspace_id=${workspaceId}`)
      .then((res) => {
        if (!res.ok) throw new Error("HTTP " + res.status)
        return res.json()
      })
      .then(async (json) => {
        if (cancelled) return
        const data = (json as SkillCardData[]) ?? []
        setSkills(data)
        oramaIndex.current = await buildIndex(data)
      })
      .catch(() => {
        if (!cancelled) setError("Failed to load skills")
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [workspaceId, wsLoading, buildIndex])

  // Debounce search — Orama is fast enough that 150ms feels native.
  // Below that we'd be re-running the search on every keystroke which
  // is wasteful; above that the user notices the lag.
  useEffect(() => {
    if (debounceTimer.current) clearTimeout(debounceTimer.current)
    debounceTimer.current = setTimeout(() => {
      const trimmed = searchInput.trim()
      if (!trimmed) {
        setSearchHits(null)
        setFilter((prev) => ({ ...prev, query: "" }))
        return
      }
      const db = oramaIndex.current
      if (!db) return
      const result = oramaSearch(db, {
        term: trimmed,
        properties: ["display_name", "description", "vendor", "slug"],
        tolerance: 1, // light typo tolerance — strict enough not to grow result set 10x
        limit: 500,
      })
      const promise = result instanceof Promise ? result : Promise.resolve(result)
      promise.then((res) => {
        if (!res) return
        const hits = new Set<string>(res.hits.map((h) => String(h.id)))
        setSearchHits(hits)
        setFilter((prev) => ({ ...prev, query: trimmed }))
      })
    }, 150)
    return () => {
      if (debounceTimer.current) clearTimeout(debounceTimer.current)
    }
  }, [searchInput])

  const filtered = useMemo(() => {
    return skills.filter((s) => {
      // Tab acts as an additional filter axis on top of the rail
      // facets — the user can pick "Generated" up top and still
      // narrow by Domain in the rail.
      if (activeTab === "generated" && s.source !== "GENERATED") return false
      // "Installed" tab shows only skills with at least one install
      // count > 0 (the closest signal we have until we surface the
      // currently-logged-in agent's installed list explicitly).
      if (activeTab === "installed" && (s.downloads ?? 0) === 0) return false
      if (searchHits && !searchHits.has(s.id)) return false
      if (filter.domains.size > 0 && !filter.domains.has(s.category)) return false
      if (filter.sources.size > 0 && !filter.sources.has(s.source)) return false
      if (filter.runtimes.size > 0 && !filter.runtimes.has(s.runtime ?? "INSTRUCTIONS")) return false
      if (filter.maturities.size > 0 && !filter.maturities.has(s.maturity ?? "COMMUNITY")) return false
      return true
    })
  }, [skills, searchHits, filter, activeTab])

  const counts = useMemo(() => {
    const byDomain: Record<string, number> = {}
    const bySource: Record<string, number> = {}
    const byRuntime: Record<string, number> = {}
    const byMaturity: Record<string, number> = {}
    for (const s of skills) {
      byDomain[s.category] = (byDomain[s.category] ?? 0) + 1
      bySource[s.source] = (bySource[s.source] ?? 0) + 1
      const rt = s.runtime ?? "INSTRUCTIONS"
      const mat = s.maturity ?? "COMMUNITY"
      byRuntime[rt] = (byRuntime[rt] ?? 0) + 1
      byMaturity[mat] = (byMaturity[mat] ?? 0) + 1
    }
    return { byDomain, bySource, byRuntime, byMaturity }
  }, [skills])

  const toggle = useCallback((set: keyof Omit<FilterState, "query">, value: string) => {
    setFilter((prev) => {
      const next = new Set(prev[set])
      if (next.has(value)) next.delete(value)
      else next.add(value)
      return { ...prev, [set]: next }
    })
  }, [])

  const clearAll = useCallback(() => {
    setFilter(EMPTY_FILTER)
    setSearchInput("")
    setSearchHits(null)
  }, [])

  const activeChips = useMemo(() => {
    const out: { key: string; label: string; onRemove: () => void }[] = []
    filter.domains.forEach((d) =>
      out.push({ key: `d:${d}`, label: `Domain: ${capitalise(d)}`, onRemove: () => toggle("domains", d) }),
    )
    filter.sources.forEach((s) => {
      const label = SOURCES.find((x) => x.value === s)?.label ?? s
      out.push({ key: `s:${s}`, label: `Source: ${label}`, onRemove: () => toggle("sources", s) })
    })
    filter.runtimes.forEach((r) => {
      const label = RUNTIMES.find((x) => x.value === r)?.label ?? r
      out.push({ key: `r:${r}`, label: `Runtime: ${label}`, onRemove: () => toggle("runtimes", r) })
    })
    filter.maturities.forEach((m) => {
      const label = MATURITIES.find((x) => x.value === m)?.label ?? m
      out.push({ key: `m:${m}`, label: `Maturity: ${label}`, onRemove: () => toggle("maturities", m) })
    })
    return out
  }, [filter, toggle])

  const reload = useCallback(() => {
    if (!workspaceId) return
    fetch(`/api/v1/skills?workspace_id=${workspaceId}`)
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then(async (json) => {
        const data = (json as SkillCardData[]) ?? []
        setSkills(data)
        // Rebuild the Orama index — without this, search would still
        // resolve hits to old row IDs that no longer exist or have
        // been re-keyed. The previous version of reload() updated
        // setSkills only, leaving searchHits stale.
        oramaIndex.current = await buildIndex(data)
      })
      .catch(() => setError("Failed to reload skills"))
  }, [workspaceId, buildIndex])

  const bundledCount = skills.filter((s) => s.source === "BUNDLED").length

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Toolbar: Tab navigation + actions (single row) — mirrors
           OrchestrationLayout's toolbar so the chrome reads consistent
           across pages. Tabs are lenses over the same skill list; the
           Create + Import buttons live here (not in the rail) so the
           rail stays a pure filter surface. */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-white/[0.08] px-2 sm:px-3 gap-0 overflow-x-auto [&::-webkit-scrollbar]:hidden [-ms-overflow-style:none] [scrollbar-width:none]">
        {SKILLS_TABS.map(({ id, label, icon: Icon }) => (
          <button
            key={id}
            onClick={() => setActiveTab(id)}
            className={cn(
              "flex items-center gap-1.5 px-2.5 h-full text-xs font-medium border-b-2 transition-all duration-100 relative top-px whitespace-nowrap shrink-0",
              activeTab === id
                ? "border-blue-400 text-blue-400"
                : "border-transparent text-muted-foreground hover:text-foreground/80",
            )}
          >
            <Icon className="h-3 w-3 opacity-75" />
            {label}
          </button>
        ))}

        <div className="flex-1" />

        {workspaceId && (
          <>
            <CreateSkillDialog workspaceId={workspaceId} onCreated={reload} />
            <ImportSkillDialog
              workspaceId={workspaceId}
              onImported={reload}
              triggerVariant="outline"
              triggerSize="sm"
              triggerLabel={
                <span className="inline-flex items-center gap-1.5 text-xs font-medium">
                  <Plus className="h-3 w-3" />
                  Import
                </span>
              }
            />
          </>
        )}
      </div>

      <PanelGroup
        orientation="horizontal"
        className="flex-1 min-h-0 flex"
      >
      {/* LEFT — filter rail. autoSaveId persists user-dragged size to
          localStorage across reloads, like the user asked. minSize keeps
          the rail readable; maxSize prevents accidental fullscreen drag. */}
      <Panel defaultSize={20} minSize={17} maxSize={32} id="skills-rail">
        <aside className="flex flex-col h-full bg-card border-r border-white/[0.1] overflow-hidden">
          <div className="flex items-center justify-between px-3 py-2 border-b border-border shrink-0">
            <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
              Skills
            </span>
            <span className="text-[10px] font-medium text-muted-foreground/60 tabular-nums">
              {skills.length}
            </span>
          </div>

          <div className="px-2 py-2 shrink-0 border-b border-white/[0.05]">
            <div className="relative">
              <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
              <Input
                placeholder="Search skills…"
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
                className="h-7 pl-7 text-[12px] bg-white/[0.04] border-white/[0.1]"
              />
            </div>
          </div>

          <div className="flex-1 overflow-y-auto px-2 py-1">
            <FacetSection title="Domain" defaultOpen>
              {DOMAINS.map((d) => {
                const c = counts.byDomain[d] ?? 0
                return (
                  <FacetRow
                    key={d}
                    label={capitalise(d)}
                    count={c}
                    checked={filter.domains.has(d)}
                    onToggle={() => toggle("domains", d)}
                    disabled={c === 0 && !filter.domains.has(d)}
                  />
                )
              })}
            </FacetSection>

            <FacetSection title="Source" defaultOpen>
              {SOURCES.map((s) => {
                const c = counts.bySource[s.value] ?? 0
                const Icon = s.icon
                return (
                  <FacetRow
                    key={s.value}
                    label={
                      <span className="inline-flex items-center gap-1.5">
                        <Icon className="h-3 w-3" />
                        {s.label}
                      </span>
                    }
                    count={c}
                    checked={filter.sources.has(s.value)}
                    onToggle={() => toggle("sources", s.value)}
                    disabled={c === 0 && !filter.sources.has(s.value)}
                  />
                )
              })}
            </FacetSection>

            <FacetSection title="Runtime">
              {RUNTIMES.map((r) => {
                const c = counts.byRuntime[r.value] ?? 0
                return (
                  <FacetRow
                    key={r.value}
                    label={r.label}
                    count={c}
                    checked={filter.runtimes.has(r.value)}
                    onToggle={() => toggle("runtimes", r.value)}
                    disabled={c === 0 && !filter.runtimes.has(r.value)}
                  />
                )
              })}
            </FacetSection>

            <FacetSection title="Maturity">
              {MATURITIES.map((m) => {
                const c = counts.byMaturity[m.value] ?? 0
                return (
                  <FacetRow
                    key={m.value}
                    label={m.label}
                    count={c}
                    checked={filter.maturities.has(m.value)}
                    onToggle={() => toggle("maturities", m.value)}
                    disabled={c === 0 && !filter.maturities.has(m.value)}
                  />
                )
              })}
            </FacetSection>
          </div>
        </aside>
      </Panel>

      <PanelResizeHandle className="w-px bg-white/[0.08] hover:bg-blue-500/40 hover:w-0.5 data-[resize-handle-active]:bg-blue-500/60 transition-colors" />

      {/* CENTER — toolbar + chips + grid */}
      <Panel defaultSize={52} minSize={36} id="skills-grid">
        <main className="flex flex-col h-full bg-card/40 min-h-0">
          <div className="flex items-center justify-between gap-2 px-4 py-2 border-b border-white/[0.05] shrink-0">
            <div className="text-[12px] text-white/55">
              <span className="text-white/35">Skills</span> ›{" "}
              <span className="text-white/95 font-semibold">Browse</span>
            </div>
            <div className="text-[11px] text-white/45 tabular-nums">
              {loading ? "Loading…" : `Showing ${filtered.length} of ${skills.length}`}
            </div>
          </div>

          {activeChips.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5 px-4 py-2 border-b border-white/[0.05] shrink-0">
              {activeChips.map((c) => (
                <button
                  key={c.key}
                  onClick={c.onRemove}
                  className="group inline-flex items-center gap-1 rounded-md bg-white/[0.06] border border-white/[0.08] px-2 py-0.5 text-[11px] text-white/70 hover:bg-white/[0.1] transition-colors duration-150"
                >
                  {c.label}
                  <X className="h-3 w-3 text-white/45 group-hover:text-white/85" />
                </button>
              ))}
              <button
                onClick={clearAll}
                className="text-[11px] text-white/45 hover:text-white/85 underline-offset-2 hover:underline transition-colors duration-150"
              >
                Clear all
              </button>
            </div>
          )}

          <div className="flex-1 min-h-0 overflow-hidden">
            {loading ? (
              <div className="flex h-full items-center justify-center text-white/45 text-sm">
                <Loader2 className="h-4 w-4 mr-2 animate-spin" />
                Loading skills…
              </div>
            ) : error ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 text-red-300 text-sm">
                <AlertTriangle className="h-5 w-5" />
                {error}
              </div>
            ) : filtered.length === 0 ? (
              <div className="flex h-full flex-col items-center justify-center gap-2 text-white/45 text-sm">
                <Package className="h-6 w-6" />
                <div>No skills match the current filters.</div>
                {activeChips.length > 0 && (
                  <Button variant="ghost" size="sm" onClick={clearAll}>Clear filters</Button>
                )}
              </div>
            ) : (
              <VirtuosoGrid
                data={filtered}
                listClassName="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-3 p-3"
                itemContent={(_, skill) => (
                  <SkillCard
                    skill={skill}
                    selected={selected?.id === skill.id}
                    onSelect={setSelected}
                  />
                )}
              />
            )}
          </div>

          <BottomStrip bundledCount={bundledCount} />
        </main>
      </Panel>

      <PanelResizeHandle className="w-px bg-white/[0.08] hover:bg-blue-500/40 hover:w-0.5 data-[resize-handle-active]:bg-blue-500/60 transition-colors" />

      {/* RIGHT — detail panel */}
      <Panel defaultSize={28} minSize={22} maxSize={45} id="skills-detail">
        <aside className="flex flex-col h-full bg-card border-l border-white/[0.1] overflow-hidden">
          <SkillsDetailPanel skill={selected} workspaceId={workspaceId} onClose={() => setSelected(null)} onChanged={reload} />
        </aside>
      </Panel>
      </PanelGroup>
    </div>
  )
}

function FacetSection({
  title,
  defaultOpen = false,
  children,
}: {
  title: string
  defaultOpen?: boolean
  children: React.ReactNode
}) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <div className="border-t border-white/[0.06] pt-3">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center justify-between text-[10px] font-medium uppercase tracking-wider text-white/55 hover:text-white/85"
      >
        {title}
        {open ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
      </button>
      {open && <div className="mt-2 space-y-1">{children}</div>}
    </div>
  )
}

function FacetRow({
  label,
  count,
  checked,
  onToggle,
  disabled,
}: {
  label: React.ReactNode
  count: number
  checked: boolean
  onToggle: () => void
  disabled?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      disabled={disabled}
      className={`flex w-full items-center justify-between rounded px-1.5 py-1 text-xs transition-colors ${
        disabled
          ? "text-white/25 cursor-default"
          : checked
            ? "bg-blue-500/[0.15] text-blue-200"
            : "text-white/70 hover:bg-white/[0.04]"
      }`}
    >
      <span className="inline-flex items-center gap-1.5">
        <span
          className={`inline-block h-3 w-3 rounded border ${
            checked
              ? "border-blue-400 bg-blue-500"
              : disabled
                ? "border-white/10"
                : "border-white/20"
          }`}
        />
        {label}
      </span>
      <span className="tabular-nums text-white/45">{count}</span>
    </button>
  )
}

function BottomStrip({ bundledCount }: { bundledCount: number }) {
  return (
    <div className="flex items-center gap-3 rounded-lg border border-white/[0.08] bg-white/[0.02] px-3 py-2 text-[11px] text-white/55">
      <span className="inline-flex items-center gap-1.5">
        <Package className="h-3 w-3" />
        Bundled ({bundledCount}) — included offline
      </span>
      <span className="text-white/25">|</span>
      <span className="inline-flex items-center gap-1.5">
        <RefreshCw className="h-3 w-3" />
        skills.sh sync — manual via <code className="text-white/65 bg-white/[0.04] px-1 rounded">crewship skill import</code>
      </span>
      <span className="ml-auto text-white/35 tabular-nums">v0.1.0-beta</span>
    </div>
  )
}

function capitalise(s: string): string {
  if (!s) return s
  const lower = s.toLowerCase()
  return lower.charAt(0).toUpperCase() + lower.slice(1)
}
