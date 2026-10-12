"use client"

import * as React from "react"
import { motion, useReducedMotion } from "motion/react"
import { ArrowRight, Download, Eye, Grid3x3, Link2, List, Plus, Settings as SettingsIcon, ShieldCheck, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"
import { SidebarFacet, SidebarFacetOption, SidebarFilterPopover, SidebarSearch } from "@/components/layout/sidebar-kit"

import {
  DEFAULT_CREW_LINK_FILTERS,
  FILE_LABELS,
  activeFacetCount,
  agentCount,
  byName,
  combinePair,
  connectionBetween,
  crewLinkStats,
  crewMatches,
  dirKey,
  directionSentence,
  directionsOf,
  duplicateNames,
  isOpenNetwork,
  splitPair,
  stateOf,
  type Crew,
  type CrewAgent,
  type CrewLinkFilters,
  type Direction,
  type FileAccess,
} from "./crew-links-model"
import { CrewProfile, CrewTile, LinkMap, agentsLine } from "./crew-profile"
import { useCrewLinks } from "./use-crew-links"
import { settingsTable, settingsTh, settingsTd, settingsTableRowLink } from "@/components/features/settings/shared"

type View = "crew" | "all" | "matrix"
const VIEWS: { key: View; label: string; icon: typeof List }[] = [
  { key: "crew", label: "By crew", icon: Users },
  { key: "all", label: "All links", icon: List },
  { key: "matrix", label: "Matrix", icon: Grid3x3 },
]

interface UrlState { view: View; crew: string; filters: CrewLinkFilters }

/** ?view=, ?crew= (a slug) and the filters (?links= ?agents= ?net= ?q=) keep
 *  the page shareable and reload-safe. Defaults stay out of the URL. */
function readUrl(): UrlState {
  if (typeof window === "undefined") return { view: "crew", crew: "", filters: DEFAULT_CREW_LINK_FILTERS }
  const p = new URLSearchParams(window.location.search)
  const v = p.get("view")
  const links = p.get("links")
  const agents = p.get("agents")
  const net = p.get("net")
  return {
    view: v === "all" || v === "matrix" ? v : "crew",
    crew: p.get("crew") ?? "",
    filters: {
      links: links === "linked" || links === "none" ? links : "all",
      agents: agents === "with" || agents === "none" ? agents : "",
      net: net === "restricted" || net === "open" ? net : "",
      q: p.get("q") ?? "",
    },
  }
}
function writeUrl({ view, crew, filters }: UrlState) {
  const url = new URL(window.location.href)
  const set = (k: string, v: string, keep: boolean) => (keep ? url.searchParams.set(k, v) : url.searchParams.delete(k))
  set("view", view, view !== "crew")
  set("crew", crew, Boolean(crew) && view === "crew")
  set("links", filters.links, filters.links !== "all")
  set("agents", filters.agents, Boolean(filters.agents))
  set("net", filters.net, Boolean(filters.net))
  set("q", filters.q, Boolean(filters.q))
  window.history.replaceState(window.history.state, "", url.toString())
}

const LINK_FACETS: { key: CrewLinkFilters["links"]; label: string }[] = [
  { key: "all", label: "All crews" },
  { key: "linked", label: "Linked" },
  { key: "none", label: "No links" },
]

export function CrewLinksPage() {
  const { workspaceId } = useWorkspace()
  const isMobile = useIsMobile()
  const reduce = useReducedMotion()
  const links = useCrewLinks(workspaceId)
  const { crews, connections, agentsByCrew, loading, loadError, canManage } = links
  const [state, setState] = React.useState(readUrl)
  const { view, crew: crewSlug, filters } = state

  const update = (next: Partial<UrlState>) => setState((prev) => {
    const merged = { ...prev, ...next }
    writeUrl(merged)
    return merged
  })
  const setView = (v: View) => update({ view: v })
  const selectCrew = (slug: string) => update({ view: "crew", crew: slug })
  const setFilters = (next: Partial<CrewLinkFilters>) => update({ filters: { ...filters, ...next } })

  const dirs = React.useMemo(() => directionsOf(connections), [connections])
  const dups = React.useMemo(() => duplicateNames(crews), [crews])
  const stats = crewLinkStats(crews, connections)
  const linkedIds = React.useMemo(() => new Set(connections.flatMap((c) => [c.from_crew_id, c.to_crew_id])), [connections])
  const agentsOf = (c: Crew) => agentsByCrew?.get(c.id) ?? (agentsByCrew ? [] : undefined)
  const sorted = [...crews].sort(byName)
  const shown = sorted.filter((c) => crewMatches(c, filters, linkedIds.has(c.id), agentsOf(c)))
  const withLinks = shown.filter((c) => linkedIds.has(c.id))
  const alone = shown.filter((c) => !linkedIds.has(c.id))
  // An explicitly opened crew stays open even when a filter hides it in the list.
  const selected = crews.find((c) => c.slug === crewSlug) ?? withLinks[0] ?? shown[0] ?? sorted[0]
  const filesUnknown = [...dirs.values()].some((d) => d.files === null)
  const outsOf = (id: string) => sorted.filter((c) => dirs.has(dirKey(id, c.id)))
  const insOf = (id: string) => sorted.filter((c) => dirs.has(dirKey(c.id, id)))
  const facetCount = (key: CrewLinkFilters["links"]) => sorted.filter((c) => crewMatches(c, { ...filters, links: key }, linkedIds.has(c.id), agentsOf(c))).length
  const popoverCount = Number(Boolean(filters.agents)) + Number(Boolean(filters.net))
  const countWhere = (f: (c: Crew) => boolean) => crews.filter(f).length

  const crewItem = (c: Crew, i: number) => (
    <DrillNavItem key={c.id} index={i} selected={view === "crew" && selected?.id === c.id} onSelect={() => selectCrew(c.slug)} title={`${c.name} · ${c.slug}`}
      icon={<CrewTile crew={c} />}
      label={<>{c.name}{dups.has(c.name) && <span className="ml-1.5 font-mono text-micro text-muted-foreground">{c.slug}</span>}</>}
      sub={agentsLine(c, agentsOf(c)) ?? undefined}
      meta={linkedIds.has(c.id) ? <span title="hands work to · receives work from">↗{outsOf(c.id).length} ↙{insOf(c.id).length}</span> : undefined} />
  )

  const toolbar = (
    <>
      <SidebarSearch value={filters.q} onValueChange={(q) => setFilters({ q })} placeholder="Search crews, agents…" />
      <SidebarFilterPopover label="Filter crews" activeCount={popoverCount} onClear={() => setFilters({ agents: "", net: "" })} panelClassName="min-w-[220px]">
        <SidebarFacet label="Agents" resetLabel="Any crew" resetActive={!filters.agents} onReset={() => setFilters({ agents: "" })} first>
          <SidebarFacetOption active={filters.agents === "with"} onToggle={() => setFilters({ agents: filters.agents === "with" ? "" : "with" })}>
            With agents<span className="ml-auto font-mono text-micro text-muted-foreground">{countWhere((c) => (agentCount(c, agentsOf(c)) ?? 0) > 0)}</span>
          </SidebarFacetOption>
          <SidebarFacetOption active={filters.agents === "none"} onToggle={() => setFilters({ agents: filters.agents === "none" ? "" : "none" })}>
            No agents (collectors)<span className="ml-auto font-mono text-micro text-muted-foreground">{countWhere((c) => agentCount(c, agentsOf(c)) === 0)}</span>
          </SidebarFacetOption>
        </SidebarFacet>
        <SidebarFacet label="Network" resetLabel="Any network" resetActive={!filters.net} onReset={() => setFilters({ net: "" })}>
          <SidebarFacetOption active={filters.net === "restricted"} onToggle={() => setFilters({ net: filters.net === "restricted" ? "" : "restricted" })}>
            Restricted<span className="ml-auto font-mono text-micro text-muted-foreground">{countWhere((c) => c.network_mode != null && !isOpenNetwork(c))}</span>
          </SidebarFacetOption>
          <SidebarFacetOption active={filters.net === "open"} onToggle={() => setFilters({ net: filters.net === "open" ? "" : "open" })}>
            Open<span className="ml-auto font-mono text-micro text-muted-foreground">{countWhere(isOpenNetwork)}</span>
          </SidebarFacetOption>
        </SidebarFacet>
      </SidebarFilterPopover>
    </>
  )

  const nav = (
    <>
      <div className="mx-2 mt-1 grid grid-cols-3 gap-0.5 rounded-lg border border-border bg-surface-subtle p-0.5" role="group" aria-label="View">
        {VIEWS.map((v) => (
          <button key={v.key} type="button" aria-pressed={view === v.key} onClick={() => setView(v.key)} data-drill-close
            className={cn("flex h-7 items-center justify-center gap-1 rounded-md text-label font-medium transition-colors", view === v.key ? "bg-card text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
            <v.icon className="h-3.5 w-3.5" />{v.label}
          </button>
        ))}
      </div>
      <DrillNavSection label="Links" count={crews.length}>
        {LINK_FACETS.map((f, i) => {
          const n = facetCount(f.key)
          return (
            <DrillNavItem key={f.key} index={i} selected={filters.links === f.key} pressed={filters.links === f.key} muted={!n} onSelect={() => setFilters({ links: f.key })}
              icon={<span className={cn("h-1.5 w-1.5 shrink-0 rounded-full", f.key === "linked" ? "bg-primary" : f.key === "none" ? "bg-muted-foreground" : "bg-border")} aria-hidden />}
              label={f.label} meta={n} />
          )
        })}
      </DrillNavSection>
      <DrillNavSection label="Linked crews" count={withLinks.length}>
        {withLinks.length ? withLinks.map(crewItem) : <p className="px-2 py-1 text-xs text-muted-foreground">No linked crew matches.</p>}
      </DrillNavSection>
      <DrillNavSection label="Crews without links" count={alone.length}>
        {alone.length ? alone.map((c, i) => crewItem(c, i + withLinks.length)) : <p className="px-2 py-1 text-xs text-muted-foreground">None.</p>}
      </DrillNavSection>
    </>
  )

  const mobileNav = (
    <div className="flex flex-col gap-2">
      <div className="grid grid-cols-3 gap-1 rounded-lg border border-border bg-surface-subtle p-0.5" role="tablist" aria-label="View">
        {VIEWS.map((v) => (
          <button key={v.key} type="button" role="tab" aria-selected={view === v.key} onClick={() => setView(v.key)}
            className={cn("flex h-8 items-center justify-center gap-1.5 rounded-md text-xs font-medium", view === v.key ? "bg-card text-foreground shadow-xs" : "text-muted-foreground")}>
            <v.icon className="h-3.5 w-3.5" />{v.label}
          </button>
        ))}
      </div>
      {view === "crew" && selected && (
        <select aria-label="Crew" value={selected.slug} onChange={(e) => selectCrew(e.target.value)}
          className="h-9 w-full rounded-md border border-control-border bg-surface-subtle px-3 text-control">
          {sorted.map((c) => <option key={c.id} value={c.slug}>{c.name}{dups.has(c.name) ? ` (${c.slug})` : ""}{linkedIds.has(c.id) ? "" : " · no links"}</option>)}
        </select>
      )}
    </div>
  )

  // The filters in words, each removable, above whatever view is open.
  const chips: { key: string; label: string; clear: Partial<CrewLinkFilters> }[] = []
  if (filters.q.trim()) chips.push({ key: "q", label: `“${filters.q.trim()}”`, clear: { q: "" } })
  if (filters.links !== "all") chips.push({ key: "links", label: filters.links === "linked" ? "Linked" : "No links", clear: { links: "all" } })
  if (filters.agents) chips.push({ key: "agents", label: filters.agents === "with" ? "With agents" : "No agents", clear: { agents: "" } })
  if (filters.net) chips.push({ key: "net", label: filters.net === "open" ? "Open network" : "Restricted network", clear: { net: "" } })

  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Crew links"
      icon={Link2}
      description="Who hands work to whom, and whose shared files they see"
      toolbar={toolbar}
      nav={nav}
      mobileNav={mobileNav}
      filterCount={activeFacetCount(filters) + Number(Boolean(filters.q.trim()))}
    >
      {loading ? (
        <div className="space-y-3 p-4 md:p-6"><Skeleton className="h-10 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
      ) : (
        <div className="space-y-4 p-4 md:p-6">
          {chips.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5" data-slot="crew-links-chips">
              {chips.map((c) => (
                <motion.span key={c.key} initial={reduce ? false : { scale: 0.85, opacity: 0 }} animate={{ scale: 1, opacity: 1 }} transition={{ duration: 0.18 }}
                  className="inline-flex h-6 items-center gap-1 rounded-full bg-primary/10 pl-2.5 pr-1 text-micro font-medium text-primary-hover">
                  {c.label}
                  <button type="button" onClick={() => setFilters(c.clear)} aria-label={`Remove filter ${c.label}`} className="grid h-4 w-4 place-items-center rounded-full hover:bg-primary/20">
                    <X className="h-3 w-3" />
                  </button>
                </motion.span>
              ))}
              <button type="button" onClick={() => setFilters(DEFAULT_CREW_LINK_FILTERS)} className="px-1.5 text-label text-muted-foreground hover:text-foreground">Clear all</button>
            </div>
          )}
          {/* The summary answers the audit question before any crew is opened. */}
          <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-muted-foreground" data-slot="crew-links-summary">
            <Stat n={stats.links} one="link" many="links" />
            <Stat n={stats.directions} one="direction" many="directions" />
            {!filesUnknown && <Stat n={stats.delivering} one="direction can deliver files" many="directions can deliver files" />}
            <Stat n={stats.alone} one="crew works alone" many="crews work alone" />
            {shown.length !== crews.length && <span><span className="font-mono tabular-nums text-foreground">{shown.length}</span> of {crews.length} crews shown</span>}
            {!canManage && <span className="ml-auto">Read-only — Managers and up change links.</span>}
          </div>
          {loadError && (
            <p role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">
              Current access could not be verified. What you see may be out of date. <button type="button" className="underline" onClick={() => void links.reload()}>Retry</button>
            </p>
          )}
          {filesUnknown && (
            <p className="rounded-lg border border-border bg-surface-subtle px-3 py-2 text-xs text-muted-foreground">
              This server does not report shared-file access yet, so only work handoff is shown and editable.
            </p>
          )}

          {view === "crew" && selected && (
            <motion.div key={selected.id} initial={reduce ? false : { opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.22, ease: [0.2, 0.7, 0.2, 1] }}>
              <CrewDetail crew={selected} crews={sorted} dirs={dirs} dups={dups} links={links} outs={outsOf(selected.id)} ins={insOf(selected.id)}
                agentsByCrew={agentsByCrew} onSelect={(c) => selectCrew(c.slug)} compact={isMobile} />
            </motion.div>
          )}
          {view === "all" && <AllLinks crews={crews} shown={shown} dirs={dirs} dups={dups} links={links} isMobile={isMobile} />}
          {view === "matrix" && <Matrix crews={shown} dirs={dirs} dups={dups} links={links} />}

          <Legend />
        </div>
      )}
    </DrillPage>
  )
}

// ── Pieces ────────────────────────────────────────────────────────────────

type Links = ReturnType<typeof useCrewLinks>

function Stat({ n, one, many }: { n: number; one: string; many: string }) {
  return <span><span className="font-mono tabular-nums text-foreground">{n}</span> {n === 1 ? one : many}</span>
}

function CrewLabel({ crew, dup }: { crew: Crew; dup: boolean }) {
  return (
    <span className="flex min-w-0 flex-col leading-tight">
      <span className="truncate">{crew.name}</span>
      {dup && <span className="truncate font-mono text-micro text-muted-foreground">{crew.slug}</span>}
    </span>
  )
}

function CrewChip({ crew, dup, agents }: { crew: Crew; dup: boolean; agents?: CrewAgent[] }) {
  const n = agents ? agents.length : null
  return (
    <span className="flex min-w-0 items-center gap-2 text-control font-medium">
      <CrewTile crew={crew} />
      <CrewLabel crew={crew} dup={dup} />
      {n != null && <span className="shrink-0 text-label font-normal text-muted-foreground">· {n} {n === 1 ? "agent" : "agents"}</span>}
    </span>
  )
}

const FILE_ICON: Record<FileAccess, React.ReactNode> = {
  none: null,
  read: <Eye className="h-3 w-3" />,
  read_write: <Download className="h-3 w-3" />,
}
const FILE_TONE: Record<FileAccess, string> = {
  none: "bg-chip-neutral-bg text-chip-neutral-fg",
  read: "bg-chip-ok-bg text-chip-ok-fg",
  read_write: "bg-chip-warn-bg text-chip-warn-fg",
}

function FilesChip({ files }: { files: FileAccess | null }) {
  // Unknown on servers that do not report file access; the page says so once.
  if (files === null) return null
  return <span className={cn("inline-flex h-[22px] items-center gap-1 rounded-full px-2 font-mono text-micro font-semibold", FILE_TONE[files])}>{FILE_ICON[files]}{FILE_LABELS[files]}</span>
}

/** None / View / Deliver for one direction; a chip when it cannot be edited. */
function FilesControl({ from, to, files, links }: { from: Crew; to: Crew; files: FileAccess | null; links: Links }) {
  if (!links.canManage || files === null) return <FilesChip files={files} />
  return (
    <div role="radiogroup" aria-label={`Shared files: ${from.name} to ${to.name}`} className="inline-flex gap-0.5 rounded-md border border-border bg-surface-subtle p-0.5">
      {(["none", "read", "read_write"] as const).map((k) => (
        <button key={k} type="button" role="radio" aria-checked={files === k} disabled={links.pending}
          onClick={() => { if (files !== k) void links.setFileAccess(from, to, k) }}
          className={cn("inline-flex h-7 items-center gap-1 rounded px-2 text-xs transition-colors", files === k ? "bg-card font-medium text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground")}>
          {FILE_ICON[k]}{k === "none" ? "None" : k === "read" ? "View" : "Deliver"}
        </button>
      ))}
    </div>
  )
}

function CrewDetail({ crew, crews, dirs, dups, links, outs, ins, agentsByCrew, onSelect, compact }: {
  crew: Crew; crews: Crew[]; dirs: Map<string, Direction>; dups: Set<string>; links: Links; outs: Crew[]; ins: Crew[]
  agentsByCrew: Map<string, CrewAgent[]> | null; onSelect: (c: Crew) => void; compact: boolean
}) {
  const agentsOf = (c: Crew) => agentsByCrew?.get(c.id) ?? (agentsByCrew ? [] : undefined)
  const remove = (from: Crew, to: Crew) => {
    // Drop one direction; the other one, if any, stays.
    const self = from
    const { in: back } = splitPair(stateOf(connectionBetween(links.connections, from.id, to.id), from.id))
    void links.setPair(self, to, combinePair(false, back))
  }
  const add = (from: Crew, to: Crew) => {
    const { in: back } = splitPair(stateOf(connectionBetween(links.connections, from.id, to.id), from.id))
    void links.setPair(from, to, combinePair(true, back))
  }
  const block = (title: string, hint: string, dir: "out" | "in", list: Crew[]) => (
    <section className="min-w-0 overflow-hidden rounded-card border border-border bg-card" aria-label={title}>
      <header className="flex items-center gap-2 border-b border-border/60 bg-surface-subtle px-4 py-2.5">
        <h3 className="text-sm font-semibold">{title}</h3>
        <span className="font-mono text-micro text-muted-foreground">{list.length}</span>
        <span className="ml-auto text-xs text-muted-foreground">{hint}</span>
      </header>
      {list.length === 0 && (
        <p className="px-4 py-3 text-xs text-muted-foreground">
          {dir === "out" ? `${crew.name} cannot hand work to any crew yet.` : `No crew can hand work to ${crew.name} yet.`}
        </p>
      )}
      {list.map((other) => {
        const [from, to] = dir === "out" ? [crew, other] : [other, crew]
        const d = dirs.get(dirKey(from.id, to.id))!
        return (
          <div key={other.id} className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-border/40 px-4 py-2.5 last:border-b-0">
            <div className="min-w-0 flex-1 basis-48">
              <CrewChip crew={other} dup={dups.has(other.name)} agents={agentsOf(other)} />
              <p className="mt-0.5 pl-9 text-label text-muted-foreground">{directionSentence(from.name, to.name, d.files)}</p>
            </div>
            <FilesControl from={from} to={to} files={d.files} links={links} />
            {links.canManage && (
              <Button variant="ghost" size="icon-sm" aria-label={`Stop ${from.name} handing work to ${to.name}`} title={`Stop ${from.name} handing work to ${to.name}`} disabled={links.pending} onClick={() => remove(from, to)}>
                <X className="h-3.5 w-3.5" />
              </Button>
            )}
          </div>
        )
      })}
      {links.canManage && (
        <AddCrewPicker
          label={dir === "out" ? `Let ${crew.name} hand work to a crew` : `Let a crew hand work to ${crew.name}`}
          candidates={crews.filter((c) => c.id !== crew.id && !list.some((l) => l.id === c.id))}
          dups={dups}
          disabled={links.pending}
          onPick={(c) => (dir === "out" ? add(crew, c) : add(c, crew))}
        />
      )}
    </section>
  )
  return (
    <div className="space-y-4">
      <CrewProfile crew={crew} agents={agentsOf(crew)} />
      <LinkMap crew={crew} ins={ins} outs={outs} dirs={dirs} agentsByCrew={agentsByCrew} onSelect={onSelect} compact={compact} />
      <div className="grid gap-4 xl:grid-cols-2">
        {block("Hands work to", "and what it may do with their files", "out", outs)}
        {block("Receives work from", "and what they may do with its files", "in", ins)}
      </div>
    </div>
  )
}

function AddCrewPicker({ label, candidates, dups, disabled, onPick }: { label: string; candidates: Crew[]; dups: Set<string>; disabled?: boolean; onPick: (c: Crew) => void }) {
  const [open, setOpen] = React.useState(false)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button type="button" disabled={disabled || candidates.length === 0} className="flex w-full items-center gap-2 border-t border-border/40 px-4 py-2.5 text-left text-xs font-medium text-primary-hover hover:bg-[var(--row-hover-bg)] disabled:opacity-50">
          <Plus className="h-3.5 w-3.5" />{label}
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-1.5">
        <div className="max-h-64 overflow-y-auto" role="listbox" aria-label={label}>
          {candidates.map((c) => (
            <button key={c.id} type="button" role="option" aria-selected={false} onClick={() => { setOpen(false); onPick(c) }}
              className="flex w-full items-center rounded-md px-2 py-1.5 text-left text-control hover:bg-accent">
              <CrewChip crew={c} dup={dups.has(c.name)} />
            </button>
          ))}
        </div>
      </PopoverContent>
    </Popover>
  )
}

/** One direction: may `from` hand work to `to`, and what may it do with `to`'s files. */
function DirectionEditor({ from, to, dirs, links, children }: { from: Crew; to: Crew; dirs: Map<string, Direction>; links: Links; children: React.ReactNode }) {
  const [open, setOpen] = React.useState(false)
  const current = dirs.get(dirKey(from.id, to.id))
  const [work, setWork] = React.useState(Boolean(current))
  const [files, setFiles] = React.useState<FileAccess>(current?.files ?? "none")
  React.useEffect(() => {
    if (open) { setWork(Boolean(current)); setFiles(current?.files ?? "none") }
  }, [open, current])
  const filesKnown = !current || current.files !== null
  const apply = async () => {
    const { in: back } = splitPair(stateOf(connectionBetween(links.connections, from.id, to.id), from.id))
    const ok = await links.setPair(from, to, combinePair(work, back))
    if (ok && work && filesKnown && files !== (current?.files ?? "none")) await links.setFileAccess(from, to, files)
    setOpen(false)
  }
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>{children}</PopoverTrigger>
      <PopoverContent className="w-80 space-y-3 p-3.5" aria-label={`${from.name} to ${to.name}`}>
        <div className="flex items-center gap-2 text-sm font-semibold">
          <span className="min-w-0 truncate">{from.name}</span><ArrowRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" /><span className="min-w-0 truncate">{to.name}</span>
        </div>
        <div className="flex items-center justify-between gap-3">
          <div className="text-xs"><div>Can hand work to {to.name}</div><div className="text-muted-foreground">Tasks and messages from {from.name}’s agents</div></div>
          <Switch checked={work} onCheckedChange={setWork} disabled={!links.canManage} aria-label="Can hand work" />
        </div>
        {filesKnown && (
          <div className={cn("flex items-center justify-between gap-3", !work && "pointer-events-none opacity-45")}>
            <div className="text-xs"><div>Shared files</div><div className="text-muted-foreground">{to.name}’s /crew/shared</div></div>
            <div role="radiogroup" aria-label="Shared files" className="inline-flex gap-0.5 rounded-md border border-border bg-surface-subtle p-0.5">
              {(["none", "read", "read_write"] as const).map((k) => (
                <button key={k} type="button" role="radio" aria-checked={files === k} disabled={!links.canManage} onClick={() => setFiles(k)}
                  className={cn("h-7 rounded px-2 text-xs", files === k ? "bg-card font-medium shadow-xs" : "text-muted-foreground")}>
                  {k === "none" ? "None" : k === "read" ? "View" : "Deliver"}
                </button>
              ))}
            </div>
          </div>
        )}
        <p className="rounded-lg border border-border/60 bg-surface-subtle px-2.5 py-2 text-xs text-muted-foreground">
          {work ? directionSentence(from.name, to.name, filesKnown ? files : null) : `${from.name} cannot hand work to ${to.name}`}.
          {dirs.has(dirKey(to.id, from.id)) ? " The other direction is set separately." : ""}
        </p>
        {links.canManage ? (
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" className="h-8 text-xs" onClick={() => setOpen(false)}>Cancel</Button>
            <Button size="sm" className="h-8 text-xs" disabled={links.pending} onClick={() => void apply()}>Apply</Button>
          </div>
        ) : (
          <p className="text-label text-muted-foreground">Only Managers and up change links.</p>
        )}
      </PopoverContent>
    </Popover>
  )
}

function AllLinks({ crews, shown, dirs, dups, links, isMobile }: { crews: Crew[]; shown: Crew[]; dirs: Map<string, Direction>; dups: Set<string>; links: Links; isMobile: boolean }) {
  const byId = new Map(crews.map((c) => [c.id, c]))
  const visible = new Set(shown.map((c) => c.id))
  const filtered = shown.length !== crews.length
  // A direction shows when either end passes the side panel's filters.
  const rows = [...dirs.values()]
    .map((d) => ({ d, from: byId.get(d.from), to: byId.get(d.to) }))
    .filter((r): r is { d: Direction; from: Crew; to: Crew } => Boolean(r.from && r.to))
    .filter((r) => visible.has(r.from.id) || visible.has(r.to.id))
    .sort((a, b) => byName(a.from, b.from) || byName(a.to, b.to))
  return (
    <section className="overflow-hidden rounded-card border border-border bg-card" aria-label="All links">
      <div className="flex items-center gap-2 border-b border-border/60 px-4 py-2">
        <h3 className="text-sm font-semibold">Directions</h3>
        <span className="ml-auto font-mono text-micro text-muted-foreground">{rows.length}</span>
      </div>
      {rows.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{filtered ? "No link involves the crews these filters show." : "No crew can hand work to another yet."}</p>}
      {rows.length > 0 && (isMobile ? (
        <div>
          {rows.map(({ d, from, to }) => (
            <DirectionEditor key={dirKey(d.from, d.to)} from={from} to={to} dirs={dirs} links={links}>
              <button type="button" className="flex w-full flex-col gap-1.5 border-b border-border/40 px-4 py-3 text-left last:border-b-0">
                <span className="flex items-center gap-2 text-control"><CrewChip crew={from} dup={dups.has(from.name)} /><ArrowRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" /><CrewChip crew={to} dup={dups.has(to.name)} /></span>
                <span className="flex items-center gap-2 pl-9"><FilesChip files={d.files} /></span>
              </button>
            </DirectionEditor>
          ))}
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className={settingsTable}>
            <thead>
              <tr>
                {["From", "", "To", "Shared files", "In other words"].map((h) => <th key={h} className={cn(settingsTh, h === "" && "px-1")}>{h}</th>)}
              </tr>
            </thead>
            <tbody>
              {rows.map(({ d, from, to }) => (
                <DirectionEditor key={dirKey(d.from, d.to)} from={from} to={to} dirs={dirs} links={links}>
                  <tr tabIndex={0} className={settingsTableRowLink}>
                    <td className={settingsTd}><CrewChip crew={from} dup={dups.has(from.name)} /></td>
                    <td className={cn(settingsTd, "px-1 text-muted-foreground")}><ArrowRight className="h-3.5 w-3.5" /></td>
                    <td className={settingsTd}><CrewChip crew={to} dup={dups.has(to.name)} /></td>
                    <td className={settingsTd}><FilesChip files={d.files} /></td>
                    <td className={cn(settingsTd, "text-muted-foreground")}>{directionSentence(from.name, to.name, d.files)}</td>
                  </tr>
                </DirectionEditor>
              ))}
            </tbody>
          </table>
        </div>
      ))}
    </section>
  )
}

function Matrix({ crews, dirs, dups, links }: { crews: Crew[]; dirs: Map<string, Direction>; dups: Set<string>; links: Links }) {
  const [hover, setHover] = React.useState<{ r: number; c: number } | null>(null)
  if (crews.length < 2) return <p className="rounded-card border border-border bg-card px-4 py-6 text-center text-xs text-muted-foreground">A link joins two crews; there are not enough to draw a matrix.</p>
  return (
    <section className="overflow-hidden rounded-card border border-border bg-card" aria-label="Matrix">
      <div className="max-h-[70vh] overflow-auto" onMouseLeave={() => setHover(null)}>
        <table className="border-separate border-spacing-0 text-control" role="grid" aria-label="Rows hand work to columns">
          <thead>
            <tr>
              <th className="sticky left-0 top-0 z-30 min-w-[200px] border-b border-r border-border bg-surface-subtle px-3 py-2 text-left align-bottom">
                <span className="eyebrow text-muted-foreground">From ↓ · hands work to →</span>
              </th>
              {/* The crews' own icons head the columns: ten names turned on their
                  side took a third of the card and still truncated. */}
              {crews.map((c, j) => (
                <th key={c.id} scope="col" title={`${c.name} (${c.slug})`} aria-label={`${c.name} (${c.slug})`}
                  className={cn("sticky top-0 z-20 w-14 min-w-14 border-b border-border bg-surface-subtle px-1 py-2 align-bottom", hover?.c === j && "bg-[var(--selection-bg)]")}>
                  <CrewTile crew={c} className="mx-auto" />
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {crews.map((row, i) => (
              <tr key={row.id}>
                <th scope="row" title={`${row.name} · ${row.slug}`}
                  className={cn("sticky left-0 z-10 border-b border-r border-border/60 bg-card px-3 py-1.5 text-left font-normal", hover?.r === i && "bg-[var(--selection-bg)]")}>
                  <CrewChip crew={row} dup={dups.has(row.name)} />
                </th>
                {crews.map((col, j) => {
                  const on = hover && (hover.r === i || hover.c === j)
                  if (row.id === col.id) return <td key={col.id} className={cn("h-11 border-b border-border/40 text-center text-muted-foreground/40", on && "bg-[var(--row-hover-bg)]")}>—</td>
                  const d = dirs.get(dirKey(row.id, col.id))
                  const tone = !d ? "text-muted-foreground/40 hover:bg-accent hover:text-muted-foreground" : d.files === "read_write" ? "bg-chip-warn-bg text-chip-warn-fg" : d.files === "read" ? "bg-chip-ok-bg text-chip-ok-fg" : "bg-chip-info-bg text-chip-info-fg"
                  const label = d ? `${directionSentence(row.name, col.name, d.files)}. Edit` : `${row.name} cannot hand work to ${col.name}. Edit`
                  return (
                    <td key={col.id} className={cn("h-11 border-b border-border/40 text-center", on && "bg-[var(--row-hover-bg)]")} onMouseEnter={() => setHover({ r: i, c: j })}>
                      <DirectionEditor from={row} to={col} dirs={dirs} links={links}>
                        <button type="button" aria-label={label} title={label} className={cn("inline-grid h-8 w-11 place-items-center rounded-md transition-colors", tone)}>
                          {d ? <span className="inline-flex items-center gap-0.5"><ArrowRight className="h-3 w-3" />{d.files ? FILE_ICON[d.files] : null}</span> : "·"}
                        </button>
                      </DirectionEditor>
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  )
}

function Legend() {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
      <span className="inline-flex items-center gap-1.5"><span className="inline-flex h-[22px] items-center gap-1 rounded-full bg-chip-info-bg px-2 font-mono text-micro font-semibold text-chip-info-fg"><ArrowRight className="h-3 w-3" />Work</span>may hand work</span>
      <span className="inline-flex items-center gap-1.5"><FilesChip files="read" />reads their shared files</span>
      <span className="inline-flex items-center gap-1.5"><FilesChip files="read_write" />reads and delivers into their incoming folder</span>
      <span className="ml-auto inline-flex items-center gap-1.5"><ShieldCheck className="h-3.5 w-3.5" />Links never grant shell, credentials or private agent homes.</span>
    </div>
  )
}
