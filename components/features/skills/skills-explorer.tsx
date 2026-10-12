"use client"

import { useMemo, useState } from "react"
import { motion, useReducedMotion } from "motion/react"
import { AlertTriangle, ChevronRight, CircleDashed, Library, Sparkles, Users, type LucideIcon } from "lucide-react"

import {
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarRow,
  SidebarSearch,
  SidebarSection,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { cn } from "@/lib/utils"
import {
  DOMAIN_ORDER,
  MATURITY_LABEL,
  TRUST_LABEL,
  domainMeta,
  explorerCounts,
  popoverFilterCount,
  skillTrust,
  sourceLabel,
  type SkillFilters,
  type SkillRow,
  type SkillsAgent,
  type SkillsCrew,
  type SkillsView,
  type TrustLevel,
} from "./skills-model"

// The /skills left sidebar (#3033), the same explorer as Routines and Issues:
// search + Filter + collapse, then View buckets with count pills, then the
// workspace's crews with their agents (the agent filter the page lacked),
// then the domains in use. Facets that are not worth a section live in the
// kit's Filter popover.

const VIEWS: { id: SkillsView; label: string; icon: LucideIcon; tone: string }[] = [
  { id: "all", label: "All skills", icon: Library, tone: "text-foreground/70" },
  { id: "assigned", label: "On agents", icon: Users, tone: "text-primary" },
  { id: "unassigned", label: "Not assigned", icon: CircleDashed, tone: "text-muted-foreground" },
  { id: "attention", label: "Needs a look", icon: AlertTriangle, tone: "text-warn" },
  { id: "proposed", label: "Proposed by agents", icon: Sparkles, tone: "text-purple" },
]

const TRUST_LEVELS: TrustLevel[] = ["verified", "unverified", "unscanned", "flagged"]

function CountPill({ n, selected }: { n: number; selected: boolean }) {
  return (
    <span
      className={cn(
        "rounded-full px-1.5 py-px text-micro tabular-nums",
        n === 0 ? "text-muted-foreground-soft" : selected ? "bg-primary/15 text-primary-hover" : "bg-foreground/[0.05] text-muted-foreground",
      )}
    >
      {n}
    </span>
  )
}

function Stagger({ i, children }: { i: number; children: React.ReactNode }) {
  const reduce = useReducedMotion()
  return (
    <motion.div
      initial={reduce ? false : { opacity: 0, y: 4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1], delay: Math.min(i, 12) * 0.018 }}
    >
      {children}
    </motion.div>
  )
}

export interface SkillsExplorerProps {
  skills: SkillRow[]
  agents: SkillsAgent[]
  crews: SkillsCrew[]
  proposedCount: number
  filters: SkillFilters
  onChange: (patch: Partial<SkillFilters>) => void
  /** Called after a pick, so a phone can close the drawer. */
  onPicked?: () => void
  onToggleCollapse?: () => void
}

export function SkillsExplorer({
  skills,
  agents,
  crews,
  proposedCount,
  filters,
  onChange,
  onPicked,
  onToggleCollapse,
}: SkillsExplorerProps) {
  const [open, setOpen] = useState({ view: true, holders: true, domain: true })
  // Few crews start open; many start folded so the domains stay in reach.
  // Only an explicit toggle is stored, so crews that load later still get
  // the default.
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const counts = useMemo(() => explorerCounts(skills, filters), [skills, filters])

  const agentsByCrew = useMemo(() => {
    const m = new Map<string, SkillsAgent[]>()
    for (const a of agents) {
      const key = a.crew_id ?? ""
      m.set(key, [...(m.get(key) ?? []), a])
    }
    for (const list of m.values()) list.sort((a, b) => a.name.localeCompare(b.name))
    return m
  }, [agents])

  const domains = useMemo(() => {
    const present = new Set(skills.map((s) => s.category))
    if (filters.domain) present.add(filters.domain)
    return DOMAIN_ORDER.filter((d) => present.has(d))
  }, [skills, filters.domain])

  const pick = (patch: Partial<SkillFilters>) => {
    onChange(patch)
    onPicked?.()
  }

  const facetCounts = useMemo(() => {
    const src = new Map<string, number>()
    const trust = new Map<TrustLevel, number>()
    const mat = new Map<string, number>()
    let needs = 0
    for (const s of skills) {
      src.set(s.source, (src.get(s.source) ?? 0) + 1)
      const t = skillTrust(s).level
      trust.set(t, (trust.get(t) ?? 0) + 1)
      const m = (s.maturity ?? "COMMUNITY").toUpperCase()
      mat.set(m, (mat.get(m) ?? 0) + 1)
      if (s.needs_credentials?.length) needs++
    }
    return { src, trust, mat, needs }
  }, [skills])

  const toggleIn = <T,>(list: T[], v: T) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v])
  let row = 0

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <div data-skills-search className="min-w-0 flex-1">
          <SidebarSearch value={filters.query} onValueChange={(query) => onChange({ query })} placeholder="Search skills, agents…" />
        </div>
        <SidebarFilterPopover
          label="Filter skills"
          activeCount={popoverFilterCount(filters)}
          onClear={() => onChange({ sources: [], trust: [], maturities: [], needsCredential: false })}
          panelClassName="min-w-[230px] max-h-[420px]"
        >
          <SidebarFacet first label="Source" resetLabel="Any source" resetActive={filters.sources.length === 0} onReset={() => onChange({ sources: [] })}>
            {[...facetCounts.src.keys()].sort().map((src) => (
              <SidebarFacetOption key={src} active={filters.sources.includes(src)} onToggle={() => onChange({ sources: toggleIn(filters.sources, src) })}>
                <span className="flex-1">{sourceLabel(src)}</span>
                <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{facetCounts.src.get(src)}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Trust" resetLabel="Any trust" resetActive={filters.trust.length === 0} onReset={() => onChange({ trust: [] })}>
            {TRUST_LEVELS.map((t) => (
              <SidebarFacetOption key={t} active={filters.trust.includes(t)} onToggle={() => onChange({ trust: toggleIn(filters.trust, t) })}>
                <span className="flex-1">{TRUST_LABEL[t]}</span>
                <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{facetCounts.trust.get(t) ?? 0}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Maturity" resetLabel="Any maturity" resetActive={filters.maturities.length === 0} onReset={() => onChange({ maturities: [] })}>
            {Object.entries(MATURITY_LABEL).map(([m, label]) => (
              <SidebarFacetOption key={m} active={filters.maturities.includes(m)} onToggle={() => onChange({ maturities: toggleIn(filters.maturities, m) })}>
                <span className="flex-1">{label}</span>
                <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{facetCounts.mat.get(m) ?? 0}</span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>
          <SidebarFacet label="Requirements" resetLabel="Any" resetActive={!filters.needsCredential} onReset={() => onChange({ needsCredential: false })}>
            <SidebarFacetOption active={filters.needsCredential} onToggle={() => onChange({ needsCredential: !filters.needsCredential })}>
              <span className="flex-1">Needs a credential</span>
              <span className="font-mono text-micro tabular-nums text-muted-foreground-soft">{facetCounts.needs}</span>
            </SidebarFacetOption>
          </SidebarFacet>
        </SidebarFilterPopover>
        {onToggleCollapse && <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />}
      </SidebarToolbar>

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        {/* ── View ── single-select buckets */}
        <SidebarSection
          label="View"
          count={VIEWS.length}
          collapsible
          collapsed={!open.view}
          onToggle={() => setOpen((o) => ({ ...o, view: !o.view }))}
          className="border-b border-foreground/[0.06] pb-1.5"
        >
          {VIEWS.map((v) => {
            const n = v.id === "proposed" ? proposedCount : counts.views[v.id]
            const selected = filters.view === v.id
            const Icon = v.icon
            return (
              <Stagger key={v.id} i={row++}>
                <SidebarRow selected={selected} onSelect={() => pick({ view: v.id })} data-testid={`skills-view-${v.id}`}>
                  <Icon className={cn("h-3.5 w-3.5 shrink-0", v.tone, n === 0 && !selected && "opacity-40")} />
                  <span className={cn("flex-1 truncate", n === 0 && !selected ? "text-muted-foreground-soft" : "text-foreground/80")}>{v.label}</span>
                  <CountPill n={n} selected={selected} />
                </SidebarRow>
              </Stagger>
            )
          })}
        </SidebarSection>

        {/* ── Crews & agents ── who holds what */}
        <SidebarSection
          label="Crews & agents"
          count={agents.length}
          collapsible
          collapsed={!open.holders}
          onToggle={() => setOpen((o) => ({ ...o, holders: !o.holders }))}
          className="border-b border-foreground/[0.06] pb-1.5"
        >
          {crews.length === 0 && agents.length === 0 && (
            <p className="px-3 py-1.5 text-label text-muted-foreground-soft">No agents in this workspace yet.</p>
          )}
          {crews.map((c) => {
            const isOpen = expanded[c.id] ?? (crews.length <= 3 || filters.crewId === c.id)
            const crewSelected = filters.crewId === c.id && !filters.agentId
            const members = agentsByCrew.get(c.id) ?? []
            return (
              <div key={c.id}>
                <Stagger i={row++}>
                  <SidebarRow
                    selected={crewSelected}
                    aria-expanded={isOpen}
                    data-testid={`skills-crew-${c.slug}`}
                    onSelect={() => {
                      // Picking a crew opens it; picking it again clears the
                      // filter and folds it back.
                      if (crewSelected) {
                        setExpanded((e) => ({ ...e, [c.id]: false }))
                        pick({ crewId: null, agentId: null })
                      } else {
                        setExpanded((e) => ({ ...e, [c.id]: true }))
                        pick({ crewId: c.id, agentId: null })
                      }
                    }}
                  >
                    <CrewIcon icon={c.icon ?? "users"} color={c.color} size="sm" className="h-[18px] w-[18px] rounded-md [&>svg]:h-3 [&>svg]:w-3" />
                    <span className="flex-1 truncate text-foreground/80">{c.name}</span>
                    <ChevronRight
                      aria-hidden
                      className={cn("h-3 w-3 shrink-0 text-muted-foreground-soft transition-transform duration-150", isOpen && "rotate-90")}
                    />
                    <span className="w-5 text-right font-mono text-micro tabular-nums text-muted-foreground-soft">{counts.byCrew.get(c.id) ?? 0}</span>
                  </SidebarRow>
                </Stagger>
                {isOpen &&
                  members.map((a) => {
                    const n = counts.byAgent.get(a.id) ?? 0
                    const selected = filters.agentId === a.id
                    return (
                      <Stagger key={a.id} i={row++}>
                        <SidebarRow
                          indent
                          selected={selected}
                          data-testid={`skills-agent-${a.slug}`}
                          onSelect={() => pick(selected ? { agentId: null } : { agentId: a.id, crewId: c.id })}
                        >
                          <AgentAvatar
                            seed={a.avatar_seed ?? a.slug}
                            style={a.avatar_style}
                            avatarUrl={a.avatar_url}
                            alt=""
                            width={16}
                            height={16}
                            className="h-4 w-4 shrink-0 rounded-full bg-foreground/[0.04]"
                          />
                          <span className={cn("flex-1 truncate", n === 0 && !selected ? "text-muted-foreground-soft" : "text-foreground/80")}>{a.name}</span>
                          <span className="w-5 text-right font-mono text-micro tabular-nums text-muted-foreground-soft">{n}</span>
                        </SidebarRow>
                      </Stagger>
                    )
                  })}
              </div>
            )
          })}
        </SidebarSection>

        {/* ── Domain ── only the ones in use */}
        <SidebarSection
          label="Domain"
          count={domains.length}
          collapsible
          collapsed={!open.domain}
          onToggle={() => setOpen((o) => ({ ...o, domain: !o.domain }))}
        >
          {domains.map((d) => {
            const meta = domainMeta(d)
            const n = counts.byDomain.get(d) ?? 0
            const selected = filters.domain === d
            const Icon = meta.icon
            return (
              <Stagger key={d} i={row++}>
                <SidebarRow selected={selected} onSelect={() => pick({ domain: selected ? null : d })}>
                  <Icon className={cn("h-3.5 w-3.5 shrink-0 text-muted-foreground", n === 0 && !selected && "opacity-40")} />
                  <span className={cn("flex-1 truncate", n === 0 && !selected ? "text-muted-foreground-soft" : "text-foreground/80")}>{meta.label}</span>
                  <span className="w-5 text-right font-mono text-micro tabular-nums text-muted-foreground-soft">{n}</span>
                </SidebarRow>
              </Stagger>
            )
          })}
        </SidebarSection>
      </div>
    </div>
  )
}
