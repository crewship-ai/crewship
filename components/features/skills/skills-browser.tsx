"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { spring } from "@/lib/motion"
import { create as createOrama, insertMultiple, search as oramaSearch } from "@orama/orama"
import type { AnyOrama } from "@orama/orama"
import { VirtuosoGrid } from "react-virtuoso"
import {
  Sparkles,
  Plus,
  X,
  Package,
  RefreshCw,
  ShieldCheck,
  BadgeCheck,
  Lock,
  AlertTriangle,
  Library,
  CheckSquare,
  PanelLeftClose,
  PanelLeftOpen,
  Users,
  Code2,
  Database,
  Cloud,
  PenLine,
  Microscope,
  ListChecks,
  Palette,
  LifeBuoy,
  Shield,
  DollarSign,
  Settings,
  Workflow,
  HandCoins,
  Box,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { useIsMobile } from "@/hooks/use-mobile"
import { Button } from "@/components/ui/button"
import { useWorkspace } from "@/hooks/use-workspace"
import { useUserPreference } from "@/hooks/use-user-preference"
import { ImportSkillDialog } from "@/components/skills/import-dialog"
import { SubBar } from "@/components/layout/sub-bar"
import {
  SidebarToolbar,
  SidebarSearch,
  SidebarSection,
  SidebarRow,
} from "@/components/layout/sidebar-kit"
import { SkillCard, type SkillCardData } from "@/components/features/skills/skill-card"
import { SkillsDetailPanel } from "@/components/features/skills/skills-detail-panel"

// FACET_DOMAINS mirrors the SkillCategory enum after the v65 expansion.
// Counts are computed live from the loaded list — values with zero
// matches still render so the user can see them go grey instead of
// silently disappear (locked enum, not free-form).
// Domain icons help the eye land on the right facet at a glance —
// 14 text rows all look identical. Colours are applied per-row in the
// rail render so disabled rows can grey-out uniformly without per-icon
// tint drift.
const DOMAINS: Array<{ value: string; icon: typeof Code2 }> = [
  { value: "CODING", icon: Code2 },
  { value: "DATA", icon: Database },
  { value: "DEVOPS", icon: Cloud },
  { value: "WRITING", icon: PenLine },
  { value: "RESEARCH", icon: Microscope },
  { value: "PM", icon: ListChecks },
  { value: "DESIGN", icon: Palette },
  { value: "SUPPORT", icon: LifeBuoy },
  { value: "SECURITY", icon: Shield },
  { value: "FINANCE", icon: DollarSign },
  { value: "OPS", icon: Settings },
  { value: "AUTOMATION", icon: Workflow },
  { value: "SALES", icon: HandCoins },
  { value: "CUSTOM", icon: Box },
]
// Source colours map to trust tier — emerald=official, sky=verified,
// neutral=community, violet=generated, amber=private. Tailwind text-*
// classes are applied directly on the icon so the count column keeps
// the standard muted colour.
const SOURCES = [
  { value: "BUNDLED", label: "Official", icon: ShieldCheck, colour: "text-emerald-400" },
  { value: "MARKETPLACE", label: "Verified", icon: BadgeCheck, colour: "text-sky-400" },
  { value: "CUSTOM", label: "Community", icon: Users, colour: "text-white/55" },
  { value: "GENERATED", label: "Generated", icon: Sparkles, colour: "text-violet-400" },
  { value: "MANAGED", label: "Private", icon: Lock, colour: "text-amber-400" },
]
const RUNTIMES = [
  { value: "INSTRUCTIONS", label: "Instructions" },
  { value: "SCRIPT", label: "Script" },
  { value: "MCP", label: "MCP" },
  { value: "HYBRID", label: "Hybrid" },
]

// Detail panel width bounds. The hard max stays at 720 for ultrawide
// monitors, but at render the value is also clamped against the
// container so a saved 720 doesn't push the panel past the viewport
// on smaller screens (the grid uses overflow-hidden, which would
// silently clip the right edge instead of producing a scrollbar).
const DETAIL_MIN_PX = 280
const DETAIL_HARD_MAX_PX = 720
const CENTRE_MIN_PX = 360
const RAIL_OPEN_PX = 280
const RAIL_COLLAPSED_PX = 44
// Maturity dot colours mirror the bundled-skill ORDER BY in
// internal/api/skills.go — OFFICIAL > CURATED > COMMUNITY >
// EXPERIMENTAL. The dot is the visual weight; without it all four
// rows read as equal-weight text.
const MATURITIES = [
  { value: "OFFICIAL", label: "Official", dot: "bg-emerald-400" },
  { value: "CURATED", label: "Curated", dot: "bg-sky-400" },
  { value: "COMMUNITY", label: "Community", dot: "bg-white/40" },
  { value: "EXPERIMENTAL", label: "Experimental", dot: "bg-amber-400" },
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
  const isMobile = useIsMobile()
  // Rail collapse state — fixed-pixel grid template (orchestration
  // pattern). Auto-collapse on mobile so the centre grid has breathing
  // room; user can re-open via the toggle.
  const [railCollapsed, setRailCollapsed] = useState(false)
  useEffect(() => {
    if (isMobile) setRailCollapsed(true)
  }, [isMobile])
  // Per-facet section collapse state, lifted out of the (previously
  // internal-state) FacetSection so the shared <SidebarSection> can be
  // driven as a controlled component. Domain + Source open by default;
  // Runtime + Maturity start collapsed — same defaults as before.
  const [facetOpen, setFacetOpen] = useState({
    domain: true,
    source: true,
    runtime: false,
    maturity: false,
  })
  const toggleFacetSection = useCallback((key: keyof typeof facetOpen) => {
    setFacetOpen((prev) => ({ ...prev, [key]: !prev[key] }))
  }, [])

  // Detail panel resizable width — drag handle on the panel's left
  // edge, persisted PER USER via /api/v1/me/preferences. Browser-
  // local fallback keeps first-paint snappy; the hook flushes the
  // server-side value over local state on mount, so the same user on
  // a different machine still gets their saved width.
  const [detailWidth, setDetailWidth] = useUserPreference<number>(
    "skills.detail.width",
    380,
  )
  // Track the grid container width so the panel's effective max can be
  // recomputed on viewport resize. Without this clamp, a saved 720px
  // preference renders past the viewport's right edge on narrower
  // screens — the grid's overflow-hidden then silently truncates the
  // detail content (and the install button) instead of scrolling.
  const gridRef = useRef<HTMLDivElement | null>(null)
  const [gridWidth, setGridWidth] = useState(0)
  useEffect(() => {
    const el = gridRef.current
    if (!el || typeof ResizeObserver === "undefined") return
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width ?? 0
      setGridWidth(w)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const railWidthPx = railCollapsed ? RAIL_COLLAPSED_PX : RAIL_OPEN_PX
  // Effective max only clamps the upper bound — no DETAIL_MIN_PX floor.
  // Flooring at 280 inflates the panel back up when the viewport is
  // tight (e.g. 800px wide with the rail open, only 180px available),
  // which then squeezes the centre column under its 360px minimum —
  // the exact symptom this clamp was meant to prevent. If the panel
  // gets uncomfortably narrow, the user can collapse the rail to reclaim
  // space; we don't auto-pump the max.
  const detailMaxEffective = gridWidth > 0
    ? Math.min(DETAIL_HARD_MAX_PX, Math.max(0, gridWidth - railWidthPx - CENTRE_MIN_PX))
    : DETAIL_HARD_MAX_PX
  // Drag handler reads the latest effective max via ref so a viewport
  // resize mid-drag clamps to the new bound without re-binding the
  // listener.
  const detailMaxRef = useRef(DETAIL_HARD_MAX_PX)
  detailMaxRef.current = detailMaxEffective
  // Two-sided clamp: a stale or externally-written persisted width below
  // DETAIL_MIN_PX would otherwise render an unusably narrow panel even
  // on wide screens. Floor at DETAIL_MIN_PX, but step out of the way
  // when detailMaxEffective is itself below that — e.g. on a tight
  // viewport where the centre column has already taken priority.
  const detailWidthRendered = detailMaxEffective < DETAIL_MIN_PX
    ? detailMaxEffective
    : Math.min(detailMaxEffective, Math.max(DETAIL_MIN_PX, detailWidth))
  const detailDragRef = useRef<{ startX: number; startW: number } | null>(null)
  const onDetailDragStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    detailDragRef.current = { startX: e.clientX, startW: detailWidthRendered }
    const onMove = (ev: MouseEvent) => {
      if (!detailDragRef.current) return
      // Drag-left increases width because the handle is on the
      // panel's LEFT edge — pulling left makes the right column
      // wider.
      const delta = detailDragRef.current.startX - ev.clientX
      const next = Math.min(
        detailMaxRef.current,
        Math.max(DETAIL_MIN_PX, detailDragRef.current.startW + delta),
      )
      setDetailWidth(next)
    }
    const onUp = () => {
      detailDragRef.current = null
      window.removeEventListener("mousemove", onMove)
      window.removeEventListener("mouseup", onUp)
    }
    window.addEventListener("mousemove", onMove)
    window.addEventListener("mouseup", onUp)
  }, [detailWidthRendered, setDetailWidth])
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
    // Switching to "Installed" pushes the filter server-side so we get
    // the actual agent_skills join result rather than guessing from
    // downloads. Other tabs use the unfiltered list.
    const installedQuery = activeTab === "installed" ? "&installed=1" : ""
    apiFetch(`/api/v1/skills?workspace_id=${workspaceId}${installedQuery}`)
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
  }, [workspaceId, wsLoading, buildIndex, activeTab])

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
      // narrow by Domain in the rail. "Installed" comes from the
      // /api/v1/skills?installed=1 narrowed list (handled in the
      // load effect below) so this filter is a no-op for it.
      if (activeTab === "generated" && s.source !== "GENERATED") return false
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
    // Clear the previous error before the new fetch so a successful
    // reload after a transient failure doesn't leave the centre panel
    // stuck on the error state.
    setError(null)
    apiFetch(`/api/v1/skills?workspace_id=${workspaceId}`)
      .then((res) => (res.ok ? res.json() : Promise.reject()))
      .then(async (json) => {
        const data = (json as SkillCardData[]) ?? []
        setSkills(data)
        // Rebuild the Orama index — without this, search would still
        // resolve hits to old row IDs that no longer exist or have
        // been re-keyed.
        oramaIndex.current = await buildIndex(data)
      })
      .catch(() => setError("Failed to reload skills"))
  }, [workspaceId, buildIndex])

  const bundledCount = skills.filter((s) => s.source === "BUNDLED").length

  return (
    <div className="flex flex-col h-[calc(100vh-48px)] bg-background">
      {/* ---- Sub-bar: identity + tab lenses + Import action. Uses the
           shared <SubBar> so the chrome reads consistent across pages.
           Tabs are lenses over the same skill list; Import is a neutral
           (ghost) row-1 action — skill authoring stays CLI-only by
           design (crewship skill create), so Import is the only surfaced
           action here. */}
      <SubBar
        icon={Library}
        title="Skills"
        description={loading ? "Loading…" : `${skills.length} skills · ${bundledCount} bundled`}
        ariaLabel="Skills"
        tabs={SKILLS_TABS.map((t) => ({ id: t.id, label: t.label, icon: t.icon }))}
        activeTab={activeTab}
        onTabChange={(id) => setActiveTab(id)}
        actions={
          workspaceId ? (
            <ImportSkillDialog
              workspaceId={workspaceId}
              onImported={reload}
              triggerVariant="ghost"
              triggerSize="sm"
              triggerClassName="h-7 gap-1.5 text-xs"
              triggerLabel={
                <span className="inline-flex items-center gap-1.5 text-xs font-medium">
                  <Plus className="h-3 w-3" />
                  Import
                </span>
              }
            />
          ) : undefined
        }
      />

      {/* ---- 3-panel grid: rail / centre / detail. Fixed-pixel
           template (orchestration pattern) instead of resizable
           because pixel widths render reliably across viewports;
           react-resizable-panels was collapsing rail to 0px on
           desktop on this layout (Sprint 7.4 finding). Detail
           opens only when a card is selected, so the centre owns
           more breathing room by default. */}
      <div
        ref={gridRef}
        className="flex-1 min-h-0 grid relative overflow-hidden"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${railCollapsed ? `${RAIL_COLLAPSED_PX}px` : `${RAIL_OPEN_PX}px`} 1fr ${selected ? `${detailWidthRendered}px` : "0px"}`,
        }}
      >
        <aside data-panel-id="skills-rail" className={cn(
          "row-span-1 border-r border-white/[0.1] bg-card flex flex-col min-h-0 overflow-hidden",
          isMobile && railCollapsed && "hidden",
        )}>
          {railCollapsed ? (
            <div className="flex items-center justify-center px-2 py-2 shrink-0">
              <Button
                variant="ghost"
                size="icon-xs"
                className="text-muted-foreground/70 hover:text-foreground/70"
                onClick={() => setRailCollapsed(false)}
                aria-label="Expand filter rail"
              >
                <PanelLeftOpen className="h-3.5 w-3.5" />
              </Button>
            </div>
          ) : (
            <>
              {/* Rail starts at the toolbar — the "Skills · N" identity now
                  lives in the sub-bar, not here. Skills are facet-driven, so
                  the facet sections ARE the filter; the toolbar carries search
                  only (+ the collapse toggle). */}
              <SidebarToolbar>
                <SidebarSearch
                  value={searchInput}
                  onValueChange={setSearchInput}
                  placeholder="Search skills…"
                  aria-label="Search skills"
                />
                <Button
                  variant="ghost"
                  size="icon-xs"
                  className="text-muted-foreground/70 hover:text-foreground/70 shrink-0"
                  onClick={() => setRailCollapsed(true)}
                  aria-label="Collapse filter rail"
                >
                  <PanelLeftClose className="h-3.5 w-3.5" />
                </Button>
              </SidebarToolbar>

              <div className="flex-1 overflow-y-auto py-1">
                <SidebarSection
                  label="Domain"
                  collapsible
                  collapsed={!facetOpen.domain}
                  onToggle={() => toggleFacetSection("domain")}
                >
                  {DOMAINS.map((d) => {
                    const c = counts.byDomain[d.value] ?? 0
                    const Icon = d.icon
                    const isDisabled = c === 0 && !filter.domains.has(d.value)
                    return (
                      <FacetRow
                        key={d.value}
                        label={
                          <span className="inline-flex items-center gap-1.5">
                            <Icon className={cn(
                              "h-3 w-3",
                              isDisabled ? "text-white/25" : "text-white/65",
                            )} />
                            {capitalise(d.value)}
                          </span>
                        }
                        count={c}
                        checked={filter.domains.has(d.value)}
                        onToggle={() => toggle("domains", d.value)}
                        disabled={isDisabled}
                      />
                    )
                  })}
                </SidebarSection>

                <SidebarSection
                  label="Source"
                  collapsible
                  collapsed={!facetOpen.source}
                  onToggle={() => toggleFacetSection("source")}
                >
                  {SOURCES.map((s) => {
                    const c = counts.bySource[s.value] ?? 0
                    const Icon = s.icon
                    const isDisabled = c === 0 && !filter.sources.has(s.value)
                    return (
                      <FacetRow
                        key={s.value}
                        label={
                          <span className="inline-flex items-center gap-1.5">
                            <Icon className={cn(
                              "h-3 w-3",
                              isDisabled ? "text-white/25" : s.colour,
                            )} />
                            {s.label}
                          </span>
                        }
                        count={c}
                        checked={filter.sources.has(s.value)}
                        onToggle={() => toggle("sources", s.value)}
                        disabled={isDisabled}
                      />
                    )
                  })}
                </SidebarSection>

                <SidebarSection
                  label="Runtime"
                  collapsible
                  collapsed={!facetOpen.runtime}
                  onToggle={() => toggleFacetSection("runtime")}
                >
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
                </SidebarSection>

                <SidebarSection
                  label="Maturity"
                  collapsible
                  collapsed={!facetOpen.maturity}
                  onToggle={() => toggleFacetSection("maturity")}
                >
                  {MATURITIES.map((m) => {
                    const c = counts.byMaturity[m.value] ?? 0
                    const isDisabled = c === 0 && !filter.maturities.has(m.value)
                    return (
                      <FacetRow
                        key={m.value}
                        label={
                          <span className="inline-flex items-center gap-2">
                            <span className={cn(
                              "h-1.5 w-1.5 rounded-full",
                              isDisabled ? "bg-white/15" : m.dot,
                            )} />
                            {m.label}
                          </span>
                        }
                        count={c}
                        checked={filter.maturities.has(m.value)}
                        onToggle={() => toggle("maturities", m.value)}
                        disabled={isDisabled}
                      />
                    )
                  })}
                </SidebarSection>
              </div>
            </>
          )}
        </aside>

        {/* CENTER — toolbar + chips + grid */}
        <main data-panel-id="skills-grid" className="flex flex-col h-full bg-card/40 min-h-0 overflow-hidden">
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
                <Spinner className="h-4 w-4 mr-2" />
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
                // auto-rows-fr makes every row stretch to match the
                // tallest card in that row, so the install-count line
                // anchors at the same vertical position across the grid
                // — addresses the "card heights jump around in a row"
                // round-12 user feedback.
                listClassName="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 auto-rows-fr gap-3 p-3"
                itemClassName="min-h-[180px]"
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

        {/* RIGHT — detail panel. Only mounted when a card is
            selected so the centre grid has full width otherwise. The
            left edge has a drag handle (1px hit zone widens to 4px on
            hover) that resizes the panel; orchestration uses the same
            pattern via the bottom drawer separator. */}
        <AnimatePresence>
          {selected && (
            <motion.aside
              key="skills-detail"
              data-panel-id="skills-detail"
              initial={{ opacity: 0, x: 24 }}
              animate={{ opacity: 1, x: 0 }}
              exit={{ opacity: 0, x: 24 }}
              transition={spring.smooth}
              className="relative flex flex-col h-full bg-card border-l border-white/[0.1] overflow-hidden"
            >
              <div
                role="separator"
                aria-label="Resize detail panel"
                aria-orientation="vertical"
                className="absolute left-0 top-0 bottom-0 w-1 -translate-x-1/2 cursor-col-resize z-10 hover:bg-blue-500/40 active:bg-blue-500/60 transition-colors"
                onMouseDown={onDetailDragStart}
              />
              <SkillsDetailPanel skill={selected} workspaceId={workspaceId} onClose={() => setSelected(null)} onChanged={reload} />
            </motion.aside>
          )}
        </AnimatePresence>
      </div>
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
  // Routes through the shared SidebarRow → ListRow so an active facet gets
  // the tokenized brand accent-bar (not a hardcoded blue fill). The checkbox
  // + trust-tinted icon/dot + count are composed inline. Disabled rows drop
  // out of the interaction (no onSelect) and grey out.
  return (
    <SidebarRow
      as="div"
      selected={checked}
      onSelect={disabled ? undefined : onToggle}
      className={cn(
        "justify-between items-center",
        disabled && "pointer-events-none opacity-40",
      )}
    >
      <span className={cn("inline-flex items-center gap-1.5 min-w-0", checked && "text-primary-hover")}>
        <span
          className={cn(
            "inline-block h-3 w-3 rounded border shrink-0",
            checked
              ? "border-primary bg-primary"
              : disabled
                ? "border-white/10"
                : "border-white/20",
          )}
        />
        {label}
      </span>
      <span className="tabular-nums text-white/45 shrink-0">{count}</span>
    </SidebarRow>
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
