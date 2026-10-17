"use client"

import { useMemo, useState } from "react"
import { AlertTriangle, Archive, Bot, Check, CircleDashed, Library, Search, Sparkles, type LucideIcon } from "lucide-react"

import {
  SidebarActiveChip,
  SidebarActiveChips,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import { SourceMark } from "./skill-marks"
import {
  FACET_KEYS,
  STATUS_LABEL,
  TRUST_LABEL,
  TRUST_LEVELS,
  VIEW_LABEL,
  activeFilterCount,
  domainMeta,
  panelCounts,
  type FacetCount,
  type FacetKey,
  type SkillFilters,
  type SkillRow,
  type SkillSourceKind,
  type SkillStatus,
  type SkillsAgent,
  type SkillsCrew,
  type SkillsView,
} from "./skills-model"

// The Skills secondary panel (Harbor 2 #3396): search, the active filters,
// the views (keys 1–6), then the facets Source, Domain, Trust, Status and Used
// by crew. Each facet picks one value; its counts leave its own pick out.

const VIEW_ICON: Record<SkillsView, LucideIcon> = {
  library: Library,
  used: Bot,
  unused: CircleDashed,
  attention: AlertTriangle,
  proposed: Sparkles,
  archived: Archive,
}

const SOURCE_TOP = 5

function sourceKind(key: string): SkillSourceKind {
  if (key === "builtin") return "builtin"
  if (key === "workspace") return "workspace"
  return key.includes(":") ? "vendor" : "repo"
}

function Count({ n }: { n: number }) {
  return <span className="ml-auto font-mono text-micro tabular-nums text-muted-foreground-soft">{n.toLocaleString("en-US").replace(/,/g, " ")}</span>
}

function Option({ on, onToggle, children, n, testId }: { on: boolean; onToggle: () => void; children: React.ReactNode; n: number; testId?: string }) {
  return (
    <SidebarRow selected={on} onSelect={onToggle} data-testid={testId}>
      <span className={cn("flex size-3.5 shrink-0 items-center justify-center rounded-sm border", on ? "border-primary bg-primary text-primary-foreground" : "border-control-border")} aria-hidden>
        {on && <Check className="size-2.5" />}
      </span>
      <span className="flex min-w-0 flex-1 items-center gap-1.5 truncate text-foreground/85">{children}</span>
      <Count n={n} />
    </SidebarRow>
  )
}

export interface SkillsExplorerProps {
  skills: SkillRow[]
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  proposedCount: number
  /** Proposed by agents shows only to roles that may approve. */
  canReview: boolean
  filters: SkillFilters
  onChange: (patch: Partial<SkillFilters>) => void
  /** Called after a pick, so a phone can close the panel sheet. */
  onPicked?: () => void
  /** Whether a skill is open: the view rows are then not "selected". */
  skillOpen?: boolean
}

export function visibleViews(canReview: boolean): SkillsView[] {
  return (["library", "used", "unused", "attention", "proposed", "archived"] as SkillsView[]).filter((v) => v !== "proposed" || canReview)
}

export function SkillsExplorer({ skills, agents, crews, proposedCount, canReview, filters, onChange, onPicked, skillOpen = false }: SkillsExplorerProps) {
  const counts = useMemo(() => panelCounts(skills, filters), [skills, filters])
  const [open, setOpen] = useState<Record<FacetKey, boolean | undefined>>({ source: undefined, domain: undefined, trust: undefined, status: undefined, crewId: undefined })
  const [pickQuery, setPickQuery] = useState("")
  const [pickOpen, setPickOpen] = useState(false)

  const pick = (patch: Partial<SkillFilters>) => {
    onChange(patch)
    onPicked?.()
  }
  const toggle = <K extends FacetKey>(key: K, value: SkillFilters[K]) => pick({ [key]: filters[key] === value ? null : value } as Partial<SkillFilters>)
  // A facet with a pick starts open; one without starts folded, so the
  // views stay in reach (v67: a facet opens on demand).
  const isOpen = (k: FacetKey) => open[k] ?? filters[k] != null
  const flip = (k: FacetKey) => setOpen((o) => ({ ...o, [k]: !isOpen(k) }))

  const libraryTotal = counts.views.library
  const sourceLabel = (key: string) => counts.sources.find((s) => s.key === key)?.label ?? key
  const agent = filters.agentId ? agents.find((a) => a.id === filters.agentId) : undefined
  const crewName = (id: string) => crews.find((c) => c.id === id)?.name ?? "Crew"

  const chips: { key: string; label: string; remove: () => void }[] = []
  if (filters.agentId) chips.push({ key: "agent", label: `Agent: ${agent?.name ?? "agent"}`, remove: () => onChange({ agentId: null }) })
  if (filters.source) chips.push({ key: "source", label: sourceLabel(filters.source), remove: () => onChange({ source: null }) })
  if (filters.domain) chips.push({ key: "domain", label: domainMeta(filters.domain).label, remove: () => onChange({ domain: null }) })
  if (filters.trust) chips.push({ key: "trust", label: TRUST_LABEL[filters.trust], remove: () => onChange({ trust: null }) })
  if (filters.status) chips.push({ key: "status", label: STATUS_LABEL[filters.status], remove: () => onChange({ status: null }) })
  if (filters.crewId) chips.push({ key: "crew", label: `Used by ${crewName(filters.crewId)}`, remove: () => onChange({ crewId: null }) })
  const any = activeFilterCount(filters) > 0
  const clearAll = () => onChange(Object.fromEntries([...FACET_KEYS.map((k) => [k, null]), ["agentId", null]]) as Partial<SkillFilters>)

  const sourcesShown: FacetCount[] = counts.sources.slice(0, SOURCE_TOP)
  if (filters.source && !sourcesShown.some((s) => s.key === filters.source)) {
    const cur = counts.sources.find((s) => s.key === filters.source)
    if (cur) sourcesShown.push(cur)
  }
  const pq = pickQuery.trim().toLowerCase()
  const picked = [...counts.sources].sort((a, b) => a.label.localeCompare(b.label)).filter((s) => !pq || s.label.toLowerCase().includes(pq))

  const domains = [...counts.domain.entries()].sort((a, b) => b[1] - a[1])
  const crewRows = crews.map((c) => ({ crew: c, n: counts.crew.get(c.id) ?? 0 })).filter((x) => x.n > 0 || filters.crewId === x.crew.id)

  return (
    <div className="flex h-full flex-col" data-slot="skills-panel">
      <SidebarToolbar>
        <div data-skills-search className="min-w-0 flex-1">
          <SidebarSearch
            value={filters.query}
            onValueChange={(query) => onChange({ query })}
            placeholder={`Search ${libraryTotal.toLocaleString("en-US").replace(/,/g, " ")} skills by name, description or source…`}
            aria-label="Search skills"
          />
        </div>
      </SidebarToolbar>

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        <SidebarActiveChips className="pt-1">
          {chips.map((c) => (
            <SidebarActiveChip key={c.key} label={c.label} onRemove={c.remove}>
              {c.label}
            </SidebarActiveChip>
          ))}
        </SidebarActiveChips>

        <SidebarSection label="Views">
          <nav aria-label="Skill views">
            {visibleViews(canReview).map((v, i) => {
              const Icon = VIEW_ICON[v]
              const n = v === "proposed" ? proposedCount : counts.views[v]
              const selected = !skillOpen && filters.view === v
              return (
                <SidebarRow key={v} selected={selected} onSelect={() => pick({ view: v })} data-testid={`skills-view-${v}`}>
                  <Icon className={cn("size-3.5 shrink-0", v === "attention" && n > 0 ? "text-warn" : "text-muted-foreground")} aria-hidden />
                  {/* The 1–6 key hint sits on the label until SidebarRow passes
                      `title`/`aria-keyshortcuts` through to the row (kit #3423). */}
                  <span className="min-w-0 flex-1 truncate text-foreground/85" title={`${VIEW_LABEL[v]} · press ${i + 1}`} aria-keyshortcuts={String(i + 1)}>
                    {VIEW_LABEL[v]}
                  </span>
                  <Count n={n} />
                </SidebarRow>
              )
            })}
          </nav>
        </SidebarSection>

        <SidebarSection
          label="Filter"
          count={any ? activeFilterCount(filters) : undefined}
          actions={
            any ? (
              <button type="button" onClick={clearAll} className="text-micro text-primary-hover hover:underline">
                Clear
              </button>
            ) : undefined
          }
        />

        <SidebarSection label="Source" collapsible collapsed={!isOpen("source")} onToggle={() => flip("source")}>
          {sourcesShown.map((s) => (
            <Option key={s.key} on={filters.source === s.key} onToggle={() => toggle("source", s.key)} n={s.count}>
              <SourceMark kind={sourceKind(s.key)} size={16} />
              <span className="truncate">{s.label}</span>
            </Option>
          ))}
          {counts.sources.length > SOURCE_TOP && (
            <Popover open={pickOpen} onOpenChange={setPickOpen}>
              <PopoverTrigger asChild>
                <button type="button" className="kit-tap mx-1.5 flex w-[calc(100%-0.75rem)] items-center gap-2 rounded-md px-2 py-1 text-left text-label text-muted-foreground hover:bg-foreground/[0.04] hover:text-foreground">
                  <Search className="size-3.5" aria-hidden />
                  All {counts.sources.length} sources…
                </button>
              </PopoverTrigger>
              <PopoverContent align="start" className="w-72 p-0" aria-label="Source">
                <div className="border-b border-border p-2">
                  <SidebarSearch value={pickQuery} onValueChange={setPickQuery} placeholder="Search owner or repository…" autoFocus />
                </div>
                <div className="max-h-72 overflow-y-auto py-1">
                  {picked.map((s) => (
                    <button
                      key={s.key}
                      type="button"
                      aria-pressed={filters.source === s.key}
                      onClick={() => {
                        setPickOpen(false)
                        toggle("source", s.key)
                      }}
                      className={cn("kit-tap flex w-full items-center gap-2 px-3 py-1.5 text-left text-label hover:bg-foreground/[0.05]", filters.source === s.key && "text-primary-hover")}
                    >
                      <SourceMark kind={sourceKind(s.key)} size={16} />
                      <span className="min-w-0 flex-1 truncate">{s.label}</span>
                      <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{s.count}</span>
                    </button>
                  ))}
                  {picked.length === 0 && <p className="px-3 py-2 text-label text-muted-foreground">No source matches.</p>}
                </div>
              </PopoverContent>
            </Popover>
          )}
        </SidebarSection>

        <SidebarSection label="Domain" collapsible collapsed={!isOpen("domain")} onToggle={() => flip("domain")}>
          {domains.map(([d, n]) => (
            <Option key={d} on={filters.domain === d} onToggle={() => toggle("domain", d)} n={n}>
              {domainMeta(d).label}
            </Option>
          ))}
        </SidebarSection>

        <SidebarSection label="Trust" collapsible collapsed={!isOpen("trust")} onToggle={() => flip("trust")}>
          {TRUST_LEVELS.map((t) => (
            <Option key={t} on={filters.trust === t} onToggle={() => toggle("trust", t)} n={counts.trust.get(t) ?? 0}>
              {TRUST_LABEL[t]}
            </Option>
          ))}
        </SidebarSection>

        <SidebarSection label="Status" collapsible collapsed={!isOpen("status")} onToggle={() => flip("status")}>
          {(Object.keys(STATUS_LABEL) as SkillStatus[]).map((st) => (
            <Option key={st} on={filters.status === st} onToggle={() => toggle("status", st)} n={counts.status.get(st) ?? 0}>
              {STATUS_LABEL[st]}
            </Option>
          ))}
        </SidebarSection>

        <SidebarSection label="Used by crew" collapsible collapsed={!isOpen("crewId")} onToggle={() => flip("crewId")}>
          {crewRows.length === 0 && <p className="px-3 py-1 text-label text-muted-foreground-soft">No crew uses a skill yet.</p>}
          {crewRows.map(({ crew, n }) => (
            <Option key={crew.id} on={filters.crewId === crew.id} onToggle={() => toggle("crewId", crew.id)} n={n} testId={`skills-crew-${crew.slug}`}>
              <CrewIcon icon={crew.icon ?? "users"} color={crew.color} size="sm" className="h-4 w-4 rounded-sm [&>svg]:h-2.5 [&>svg]:w-2.5" />
              <span className="truncate">{crew.name}</span>
            </Option>
          ))}
        </SidebarSection>
      </div>
    </div>
  )
}
