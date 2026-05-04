"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { create as createOrama, insertMultiple, search as oramaSearch } from "@orama/orama"
import type { AnyOrama } from "@orama/orama"
import { VirtuosoGrid } from "react-virtuoso"
import {
  Search, Sparkles, Plus, X, ChevronDown, ChevronRight,
  Package, RefreshCw, ShieldCheck, BadgeCheck, Lock, Dot, AlertTriangle, Loader2,
  Library, CheckSquare, PanelLeftClose, PanelLeftOpen,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { useIsMobile } from "@/hooks/use-mobile"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { useWorkspace } from "@/hooks/use-workspace"
import { ImportSkillDialog } from "@/components/skills/import-dialog"
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
  const isMobile = useIsMobile()
  // Rail collapse state — fixed-pixel grid template (orchestration
  // pattern). Auto-collapse on mobile so the centre grid has breathing
  // room; user can re-open via the toggle.
  const [railCollapsed, setRailCollapsed] = useState(false)
  useEffect(() => {
    if (isMobile) setRailCollapsed(true)
  }, [isMobile])

  // Detail panel resizable width — drag handle on the panel's left
  // edge, persisted via localStorage so the user's preferred width
  // sticks across reloads. Min/max keep it readable yet capped to
  // a reasonable share of viewport on small screens.
  const [detailWidth, setDetailWidth] = useState<number>(() => {
    if (typeof window === "undefined") return 380
    const stored = window.localStorage.getItem("crewship.skills.detail.width.v1")
    const parsed = stored ? parseInt(stored, 10) : NaN
    if (!Number.isNaN(parsed) && parsed >= 280 && parsed <= 720) return parsed
    return 380
  })
  const detailDragRef = useRef<{ startX: number; startW: number } | null>(null)
  const onDetailDragStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    detailDragRef.current = { startX: e.clientX, startW: detailWidth }
    const onMove = (ev: MouseEvent) => {
      if (!detailDragRef.current) return
      // Drag-left increases width because the handle is on the
      // panel's LEFT edge — pulling left makes the right column
      // wider.
      const delta = detailDragRef.current.startX - ev.clientX
      const next = Math.min(720, Math.max(280, detailDragRef.current.startW + delta))
      setDetailWidth(next)
    }
    const onUp = () => {
      detailDragRef.current = null
      window.removeEventListener("mousemove", onMove)
      window.removeEventListener("mouseup", onUp)
      // Persist the final width once on release rather than every
      // mousemove tick so the storage write isn't on the hot path.
      try {
        window.localStorage.setItem("crewship.skills.detail.width.v1", String(detailWidth))
      } catch {
        // localStorage may be unavailable (private mode etc.); the
        // in-memory state still holds for the session.
      }
    }
    window.addEventListener("mousemove", onMove)
    window.addEventListener("mouseup", onUp)
  }, [detailWidth])

  // Re-persist whenever detailWidth lands a new value (handles direct
  // setState calls outside the drag handler too — e.g. future reset
  // button or keyboard shortcuts).
  useEffect(() => {
    try {
      window.localStorage.setItem("crewship.skills.detail.width.v1", String(detailWidth))
    } catch {
      // ignore — see comment in onDetailDragStart
    }
  }, [detailWidth])
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
    fetch(`/api/v1/skills?workspace_id=${workspaceId}${installedQuery}`)
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
    fetch(`/api/v1/skills?workspace_id=${workspaceId}`)
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
          // Skill authoring is CLI-only by design (crewship skill create).
          // The UI button lived here briefly but the LLM-authoring flow
          // has too many environment-level prerequisites (workspace
          // ANTHROPIC API_KEY etc.) to surface as a one-click action;
          // CLI-first is the v0.1 stance the user picked.
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
        )}
      </div>

      {/* ---- 3-panel grid: rail / centre / detail. Fixed-pixel
           template (orchestration pattern) instead of resizable
           because pixel widths render reliably across viewports;
           react-resizable-panels was collapsing rail to 0px on
           desktop on this layout (Sprint 7.4 finding). Detail
           opens only when a card is selected, so the centre owns
           more breathing room by default. */}
      <div
        className="flex-1 min-h-0 grid relative overflow-hidden"
        style={{
          gridTemplateColumns: isMobile
            ? "1fr"
            : `${railCollapsed ? "44px" : "260px"} 1fr ${selected ? `${detailWidth}px` : "0px"}`,
        }}
      >
        <aside data-panel-id="skills-rail" className={cn(
          "row-span-1 border-r border-white/[0.1] bg-card flex flex-col min-h-0 overflow-hidden",
          isMobile && railCollapsed && "hidden",
        )}>
          <div className="flex items-center justify-between px-2 py-1.5 border-b border-border shrink-0">
            {!railCollapsed && (
              <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
                Skills · {skills.length}
              </span>
            )}
            <Button
              variant="ghost"
              size="icon-xs"
              className="text-muted-foreground/70 hover:text-foreground/70 ml-auto"
              onClick={() => setRailCollapsed((v) => !v)}
              aria-label={railCollapsed ? "Expand filter rail" : "Collapse filter rail"}
            >
              {railCollapsed ? <PanelLeftOpen className="h-3.5 w-3.5" /> : <PanelLeftClose className="h-3.5 w-3.5" />}
            </Button>
          </div>

          {!railCollapsed && (
          <>
          <div style={{ display: "none" }}>
            <span className="text-[10px] font-medium text-muted-foreground/60 tabular-nums">
              {skills.length}
            </span>
          </div>

          <div className="px-2 py-2 shrink-0 border-b border-white/[0.05]">
            <div className="relative">
              <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
              <Input
                placeholder="Search skills…"
                aria-label="Search skills"
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
                // auto-rows-fr makes every row stretch to match the
                // tallest card in that row, so the install-count line
                // anchors at the same vertical position across the grid
                // — addresses the 'rozhodí se výška karet' from the
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
        {selected && (
          <aside data-panel-id="skills-detail" className="relative flex flex-col h-full bg-card border-l border-white/[0.1] overflow-hidden">
            <div
              role="separator"
              aria-label="Resize detail panel"
              aria-orientation="vertical"
              className="absolute left-0 top-0 bottom-0 w-1 -translate-x-1/2 cursor-col-resize z-10 hover:bg-blue-500/40 active:bg-blue-500/60 transition-colors"
              onMouseDown={onDetailDragStart}
            />
            <SkillsDetailPanel skill={selected} workspaceId={workspaceId} onClose={() => setSelected(null)} onChanged={reload} />
          </aside>
        )}
      </div>
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
