"use client"

import * as React from "react"
import { ArrowRight, Download, Eye, Grid3x3, Link2, List, Plus, Search, Settings as SettingsIcon, ShieldCheck, Users, X } from "lucide-react"

import { cn } from "@/lib/utils"
import { useWorkspace } from "@/hooks/use-workspace"
import { useIsMobile } from "@/hooks/use-mobile"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { DrillNavItem, DrillNavSection, DrillPage } from "@/components/layout/drill-page"

import {
  FILE_LABELS,
  byName,
  combinePair,
  connectionBetween,
  crewLinkStats,
  dirKey,
  directionSentence,
  directionsOf,
  duplicateNames,
  splitPair,
  stateOf,
  type Crew,
  type Direction,
  type FileAccess,
} from "./crew-links-model"
import { useCrewLinks } from "./use-crew-links"

type View = "crew" | "all" | "matrix"
const VIEWS: { key: View; label: string; icon: typeof List }[] = [
  { key: "crew", label: "By crew", icon: Users },
  { key: "all", label: "All links", icon: List },
  { key: "matrix", label: "Matrix", icon: Grid3x3 },
]

/** ?view= and ?crew= (a slug) keep the page shareable and reload-safe. */
function readUrl(): { view: View; crew: string } {
  if (typeof window === "undefined") return { view: "crew", crew: "" }
  const p = new URLSearchParams(window.location.search)
  const v = p.get("view")
  return { view: v === "all" || v === "matrix" ? v : "crew", crew: p.get("crew") ?? "" }
}
function writeUrl(view: View, crew: string) {
  const url = new URL(window.location.href)
  if (view === "crew") url.searchParams.delete("view"); else url.searchParams.set("view", view)
  if (crew && view === "crew") url.searchParams.set("crew", crew); else url.searchParams.delete("crew")
  window.history.replaceState(window.history.state, "", url.toString())
}

export function CrewLinksPage() {
  const { workspaceId } = useWorkspace()
  const isMobile = useIsMobile()
  const links = useCrewLinks(workspaceId)
  const { crews, connections, loading, loadError, canManage } = links
  const [{ view, crew: crewSlug }, setNav] = React.useState(readUrl)
  const [query, setQuery] = React.useState("")
  const [onlyLinked, setOnlyLinked] = React.useState(false)

  const setView = (v: View) => { setNav((n) => ({ ...n, view: v })); writeUrl(v, crewSlug) }
  const selectCrew = (slug: string) => { setNav({ view: "crew", crew: slug }); writeUrl("crew", slug) }

  const dirs = React.useMemo(() => directionsOf(connections), [connections])
  const dups = React.useMemo(() => duplicateNames(crews), [crews])
  const stats = crewLinkStats(crews, connections)
  const linkedIds = React.useMemo(() => new Set(connections.flatMap((c) => [c.from_crew_id, c.to_crew_id])), [connections])
  const matches = (c: Crew) => !query || `${c.name} ${c.slug}`.toLowerCase().includes(query.trim().toLowerCase())
  const sorted = [...crews].sort(byName)
  const withLinks = sorted.filter((c) => linkedIds.has(c.id) && matches(c))
  const alone = sorted.filter((c) => !linkedIds.has(c.id) && matches(c))
  const selected = crews.find((c) => c.slug === crewSlug) ?? withLinks[0] ?? sorted[0]
  const filesUnknown = [...dirs.values()].some((d) => d.files === null)
  const outsOf = (id: string) => sorted.filter((c) => dirs.has(dirKey(id, c.id)))
  const insOf = (id: string) => sorted.filter((c) => dirs.has(dirKey(c.id, id)))

  const nav = (
    <>
      <DrillNavSection label="View">
        {VIEWS.map((v) => (
          <DrillNavItem key={v.key} selected={view === v.key} onSelect={() => setView(v.key)} icon={<v.icon className="h-3.5 w-3.5 shrink-0 opacity-70" />} label={v.label} />
        ))}
      </DrillNavSection>
      {view === "crew" && (
        <>
          <div className="px-3 pt-3">
            <label className="flex h-8 items-center gap-2 rounded-md border border-border bg-surface-subtle px-2.5 text-muted-foreground focus-within:border-ring">
              <Search className="h-3.5 w-3.5 shrink-0" />
              <input type="search" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Find a crew…" aria-label="Find a crew" className="w-full bg-transparent text-control text-foreground outline-none placeholder:text-muted-foreground" />
            </label>
          </div>
          {withLinks.length > 0 && (
            <DrillNavSection label="Linked" count={withLinks.length}>
              {withLinks.map((c) => (
                <DrillNavItem key={c.id} selected={selected?.id === c.id} onSelect={() => selectCrew(c.slug)} title={`${c.name} · ${c.slug}`}
                  icon={<CrewIcon icon={c.icon || "briefcase"} color={c.color} size="sm" />}
                  label={<CrewLabel crew={c} dup={dups.has(c.name)} />}
                  meta={<span title="hands work to · receives work from">↗{outsOf(c.id).length} ↙{insOf(c.id).length}</span>} />
              ))}
            </DrillNavSection>
          )}
          {alone.length > 0 && (
            <DrillNavSection label="No links" count={alone.length}>
              {alone.map((c) => (
                <DrillNavItem key={c.id} muted selected={selected?.id === c.id} onSelect={() => selectCrew(c.slug)} title={`${c.name} · ${c.slug}`}
                  icon={<CrewIcon icon={c.icon || "briefcase"} color={c.color} size="sm" />}
                  label={<CrewLabel crew={c} dup={dups.has(c.name)} />} />
              ))}
            </DrillNavSection>
          )}
          {!withLinks.length && !alone.length && <p className="px-4 pt-3 text-xs text-muted-foreground">No crew matches “{query}”.</p>}
        </>
      )}
      {view !== "crew" && (
        <div className="px-3 pt-4">
          <label className="flex cursor-pointer items-center gap-2 text-xs text-muted-foreground">
            <Switch checked={onlyLinked} onCheckedChange={setOnlyLinked} aria-label="Only crews with links" />
            Only crews with links
          </label>
        </div>
      )}
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

  return (
    <DrillPage
      parent={{ href: "/settings", label: "Settings", icon: SettingsIcon }}
      title="Crew links"
      icon={Link2}
      description="Who hands work to whom, and whose shared files they see"
      nav={nav}
      mobileNav={mobileNav}
    >
      {loading ? (
        <div className="space-y-3 p-4 md:p-6"><Skeleton className="h-10 rounded-lg" /><Skeleton className="h-[320px] rounded-card" /></div>
      ) : (
        <div className="space-y-4 p-4 md:p-6">
          {/* The summary answers the audit question before any crew is opened. */}
          <div className="flex flex-wrap items-center gap-x-5 gap-y-1.5 text-xs text-muted-foreground" data-slot="crew-links-summary">
            <Stat n={stats.links} one="link" many="links" />
            <Stat n={stats.directions} one="direction" many="directions" />
            {!filesUnknown && <Stat n={stats.delivering} one="direction can deliver files" many="directions can deliver files" />}
            <Stat n={stats.alone} one="crew works alone" many="crews work alone" />
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
            <CrewDetail crew={selected} crews={sorted} dirs={dirs} dups={dups} links={links} outs={outsOf(selected.id)} ins={insOf(selected.id)} />
          )}
          {view === "all" && <AllLinks crews={crews} dirs={dirs} dups={dups} links={links} query={query} onQuery={setQuery} isMobile={isMobile} />}
          {view === "matrix" && <Matrix crews={sorted.filter((c) => !onlyLinked || linkedIds.has(c.id))} dirs={dirs} dups={dups} links={links} />}

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
      {dup && <span className="truncate font-mono text-[10.5px] text-muted-foreground">{crew.slug}</span>}
    </span>
  )
}

function CrewChip({ crew, dup }: { crew: Crew; dup: boolean }) {
  return (
    <span className="flex min-w-0 items-center gap-2 text-[13px] font-medium">
      <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="sm" />
      <CrewLabel crew={crew} dup={dup} />
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
  return <span className={cn("inline-flex h-[22px] items-center gap-1 rounded-full px-2 font-mono text-[10.5px] font-semibold", FILE_TONE[files])}>{FILE_ICON[files]}{FILE_LABELS[files]}</span>
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

function CrewDetail({ crew, crews, dirs, dups, links, outs, ins }: { crew: Crew; crews: Crew[]; dirs: Map<string, Direction>; dups: Set<string>; links: Links; outs: Crew[]; ins: Crew[] }) {
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
        <span className="font-mono text-[11px] text-muted-foreground">{list.length}</span>
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
              <CrewChip crew={other} dup={dups.has(other.name)} />
              <p className="mt-0.5 pl-8 text-[11.5px] text-muted-foreground">{directionSentence(from.name, to.name, d.files)}</p>
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
      <div className="flex items-center gap-3">
        <CrewIcon icon={crew.icon || "briefcase"} color={crew.color} size="md" />
        <div className="min-w-0">
          <h2 className="truncate text-lg font-semibold tracking-[-0.02em]">{crew.name}</h2>
          <p className="font-mono text-[11px] text-muted-foreground">{crew.slug}</p>
        </div>
      </div>
      <div className="grid gap-4 xl:grid-cols-2">
        {block("Hands work to", "and what it may read there", "out", outs)}
        {block("Receives work from", "and what they may read here", "in", ins)}
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
              className="flex w-full items-center rounded-md px-2 py-1.5 text-left text-[13px] hover:bg-accent">
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
          <p className="text-[11px] text-muted-foreground">Only Managers and up change links.</p>
        )}
      </PopoverContent>
    </Popover>
  )
}

function AllLinks({ crews, dirs, dups, links, query, onQuery, isMobile }: { crews: Crew[]; dirs: Map<string, Direction>; dups: Set<string>; links: Links; query: string; onQuery: (q: string) => void; isMobile: boolean }) {
  const byId = new Map(crews.map((c) => [c.id, c]))
  const q = query.trim().toLowerCase()
  const rows = [...dirs.values()]
    .map((d) => ({ d, from: byId.get(d.from), to: byId.get(d.to) }))
    .filter((r): r is { d: Direction; from: Crew; to: Crew } => Boolean(r.from && r.to))
    .filter((r) => !q || `${r.from.name} ${r.from.slug} ${r.to.name} ${r.to.slug}`.toLowerCase().includes(q))
    .sort((a, b) => byName(a.from, b.from) || byName(a.to, b.to))
  return (
    <section className="overflow-hidden rounded-card border border-border bg-card" aria-label="All links">
      <div className="flex items-center gap-2 border-b border-border/60 px-3 py-2">
        <label className="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-md border border-border bg-surface-subtle px-2.5 text-muted-foreground sm:max-w-xs">
          <Search className="h-3.5 w-3.5 shrink-0" />
          <input type="search" value={query} onChange={(e) => onQuery(e.target.value)} placeholder="Find a crew…" aria-label="Find a link by crew" className="w-full bg-transparent text-control text-foreground outline-none" />
        </label>
        <span className="ml-auto font-mono text-[11px] text-muted-foreground">{rows.length} directions</span>
      </div>
      {rows.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{q ? `No link involves “${query}”.` : "No crew can hand work to another yet."}</p>}
      {rows.length > 0 && (isMobile ? (
        <div>
          {rows.map(({ d, from, to }) => (
            <DirectionEditor key={dirKey(d.from, d.to)} from={from} to={to} dirs={dirs} links={links}>
              <button type="button" className="flex w-full flex-col gap-1.5 border-b border-border/40 px-4 py-3 text-left last:border-b-0">
                <span className="flex items-center gap-2 text-[13px]"><CrewChip crew={from} dup={dups.has(from.name)} /><ArrowRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" /><CrewChip crew={to} dup={dups.has(to.name)} /></span>
                <span className="flex items-center gap-2 pl-8"><FilesChip files={d.files} /></span>
              </button>
            </DirectionEditor>
          ))}
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-[13px]">
            <thead>
              <tr className="border-b border-border/60 bg-surface-subtle">
                {["From", "", "To", "Shared files", "In other words"].map((h) => <th key={h} className="eyebrow px-3 py-2 text-left text-muted-foreground">{h}</th>)}
              </tr>
            </thead>
            <tbody>
              {rows.map(({ d, from, to }) => (
                <DirectionEditor key={dirKey(d.from, d.to)} from={from} to={to} dirs={dirs} links={links}>
                  <tr tabIndex={0} className="cursor-pointer border-b border-border/40 last:border-b-0 hover:bg-[var(--row-hover-bg)] focus-visible:bg-[var(--row-hover-bg)] focus-visible:outline-none">
                    <td className="px-3 py-2.5"><CrewChip crew={from} dup={dups.has(from.name)} /></td>
                    <td className="px-1 text-muted-foreground"><ArrowRight className="h-3.5 w-3.5" /></td>
                    <td className="px-3 py-2.5"><CrewChip crew={to} dup={dups.has(to.name)} /></td>
                    <td className="px-3 py-2.5"><FilesChip files={d.files} /></td>
                    <td className="px-3 py-2.5 text-muted-foreground">{directionSentence(from.name, to.name, d.files)}</td>
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
        <table className="border-separate border-spacing-0 text-[13px]" role="grid" aria-label="Rows hand work to columns">
          <thead>
            <tr>
              <th className="sticky left-0 top-0 z-30 min-w-[200px] border-b border-r border-border bg-surface-subtle px-3 py-2 text-left align-bottom">
                <span className="eyebrow text-muted-foreground">From ↓ · hands work to →</span>
              </th>
              {crews.map((c, j) => (
                <th key={c.id} scope="col" title={`${c.name} · ${c.slug}`}
                  className={cn("sticky top-0 z-20 h-40 w-14 min-w-14 border-b border-border bg-surface-subtle align-bottom", hover?.c === j && "bg-[var(--selection-bg)]")}>
                  <div className="mx-auto flex max-h-36 items-center gap-1.5 overflow-hidden whitespace-nowrap py-2 text-xs font-medium [transform:rotate(180deg)] [writing-mode:vertical-rl]">
                    <CrewIcon icon={c.icon || "briefcase"} color={c.color} size="sm" />
                    <span className="truncate">{c.name}{dups.has(c.name) ? ` · ${c.slug}` : ""}</span>
                  </div>
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
      <span className="inline-flex items-center gap-1.5"><span className="inline-flex h-[22px] items-center gap-1 rounded-full bg-chip-info-bg px-2 font-mono text-[10.5px] font-semibold text-chip-info-fg"><ArrowRight className="h-3 w-3" />Work</span>may hand work</span>
      <span className="inline-flex items-center gap-1.5"><FilesChip files="read" />reads their shared files</span>
      <span className="inline-flex items-center gap-1.5"><FilesChip files="read_write" />reads and delivers into their incoming folder</span>
      <span className="ml-auto inline-flex items-center gap-1.5"><ShieldCheck className="h-3.5 w-3.5" />Links never grant shell, credentials or private agent homes.</span>
    </div>
  )
}
