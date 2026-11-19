"use client"

import * as React from "react"
import { ArrowRight, ChevronRight, Repeat } from "lucide-react"

import { cn } from "@/lib/utils"
import { Segmented } from "@/components/ui/segmented"
import { ShowMore } from "@/components/ui/show-more"
import { useShowMore } from "@/hooks/use-show-more"
import { settingsTable, settingsTd, settingsTh, settingsTableRowLink } from "@/components/features/settings/shared"

import {
  LEVEL_LABEL,
  MATRIX_LIMIT,
  crewFigures,
  isOpenNetwork,
  levelSentence,
  matrixCrews,
  pairLevels,
  connectionBetween,
  agentCount,
  type Connection,
  type Crew,
  type CrewAgent,
  type CrewLinkFilters,
  type LinkRow,
} from "./crew-links-model"
import { CrewTile } from "./crew-profile"
import { InactiveTag, LEVEL_ICON, LevelTag } from "./level-ladder"

function CrewCell({ crew, dup }: { crew: Crew; dup: boolean }) {
  return (
    <span className="flex min-w-0 items-center gap-2 text-control font-medium">
      <CrewTile crew={crew} />
      <span className="flex min-w-0 flex-col leading-tight">
        <span className="truncate">{crew.name}</span>
        {dup && <span className="truncate font-mono text-micro text-muted-foreground">{crew.slug}</span>}
      </span>
    </span>
  )
}

const LEVEL_CHIPS: { value: CrewLinkFilters["level"]; label: string }[] = [
  { value: "", label: "Any level" },
  { value: "deliver", label: "Deliver files" },
  { value: "view", label: "View files" },
  { value: "work", label: "Work only" },
]

const tone = (l: "off" | "work" | "view" | "deliver") =>
  l === "deliver" ? "bg-chip-warn-bg text-chip-warn-fg" : l === "view" ? "bg-chip-ok-bg text-chip-ok-fg" : l === "work" ? "bg-chip-info-bg text-chip-info-fg" : "text-muted-foreground/40"

/**
 * All links and Can deliver files: one paged list of links, "A → B" or
 * "A ⇄ B" with a pill for each direction. A row opens its first crew's page.
 * All links also filters by level and switches to the matrix.
 */
export function LinksList({ rows, view, level, onLevel, onMode, onOpen, dups, isMobile, resetKey, connections, onCell, canManage }: {
  rows: LinkRow[]
  view: "all" | "deliver" | "matrix"
  level: CrewLinkFilters["level"]
  onLevel: (l: CrewLinkFilters["level"]) => void
  onMode: (m: "all" | "matrix") => void
  onOpen: (c: Crew) => void
  dups: Set<string>
  isMobile: boolean
  resetKey: string
  connections: Connection[]
  /** A matrix cell: open the link dialog for this pair. Managers only. */
  onCell: (a: Crew, b: Crew) => void
  canManage: boolean
}) {
  const paged = useShowMore(rows, { resetKey })
  const matrix = view === "matrix"
  return (
    <section className="overflow-hidden rounded-card border border-border bg-card" aria-label={view === "deliver" ? "Can deliver files" : "All links"}>
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2.5">
        <b className="text-control">{rows.length} {rows.length === 1 ? "link" : "links"}</b>
        {view !== "deliver" ? (
          <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Level">
            {LEVEL_CHIPS.map((c) => (
              <button key={c.value} type="button" aria-pressed={level === c.value} onClick={() => onLevel(c.value)}
                className={cn("inline-flex h-7 items-center rounded-full px-2.5 text-label font-medium transition-colors coarse:h-[44px]",
                  level === c.value ? "bg-primary/10 text-primary-hover" : "bg-surface-subtle text-muted-foreground hover:text-foreground")}>
                {c.label}
              </button>
            ))}
          </div>
        ) : <span className="text-label text-muted-foreground">Open one to change it</span>}
        {view !== "deliver" && (
          <div className="ml-auto">
            <Segmented<"all" | "matrix"> ariaLabel="Show as" value={matrix ? "matrix" : "all"} onChange={onMode}
              options={[{ value: "all", label: "List" }, { value: "matrix", label: "Matrix" }]} />
          </div>
        )}
      </div>

      {matrix ? (
        <MatrixView rows={rows} connections={connections} dups={dups} onCell={onCell} canManage={canManage} />
      ) : (
        <>
          {rows.length === 0 && <p className="px-4 py-6 text-center text-label text-muted-foreground">No link matches these filters.</p>}
          {rows.length > 0 && (isMobile ? (
            <div>
              {paged.shown.map((r) => (
                <button key={r.conn.id} type="button" onClick={() => onOpen(r.a)} className="flex min-h-[44px] w-full items-center gap-2 border-b border-border px-4 py-3 text-left last:border-b-0">
                  <span className="min-w-0 flex-1">
                    <b className="block text-control font-medium">{r.a.name} {r.ba !== "off" ? "⇄" : "→"} {r.b.name}</b>
                    <small className="block text-label text-muted-foreground">
                      {r.inactive ? "Inactive" : `${LEVEL_LABEL[r.ab]}${r.ba !== "off" ? ` · back: ${LEVEL_LABEL[r.ba]}` : ""}`}
                    </small>
                  </span>
                  <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
                </button>
              ))}
            </div>
          ) : (
            <table className={settingsTable}>
              <thead>
                <tr>
                  <th className={settingsTh}>Crews</th>
                  <th className={settingsTh}>First hands to second</th>
                  <th className={settingsTh}>Second hands to first</th>
                  <th className={settingsTh} aria-hidden />
                </tr>
              </thead>
              <tbody>
                {paged.shown.map((r) => (
                  <tr key={r.conn.id} tabIndex={0} className={settingsTableRowLink} aria-label={`${r.a.name} and ${r.b.name}`}
                    onClick={() => onOpen(r.a)} onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(r.a) } }}>
                    <td className={settingsTd}>
                      <span className="flex min-w-0 flex-wrap items-center gap-2">
                        <CrewCell crew={r.a} dup={dups.has(r.a.name)} />
                        {r.ba !== "off" ? <Repeat className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-label="both ways" /> : <ArrowRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-label="one way" />}
                        <CrewCell crew={r.b} dup={dups.has(r.b.name)} />
                        {r.inactive && <InactiveTag />}
                      </span>
                    </td>
                    <td className={settingsTd}><LevelTag level={r.ab} /></td>
                    <td className={settingsTd}><LevelTag level={r.ba} /></td>
                    <td className={settingsTd}><ChevronRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          ))}
          <ShowMore shown={paged.shown.length} total={paged.total} nextCount={paged.nextCount} onMore={paged.showMore} noun="links" paged={paged.paged} />
        </>
      )}
    </section>
  )
}

function MatrixView({ rows, connections, dups, onCell, canManage }: {
  rows: LinkRow[]; connections: Connection[]; dups: Set<string>; onCell: (a: Crew, b: Crew) => void; canManage: boolean
}) {
  const { crews, total: joined } = React.useMemo(() => matrixCrews(rows, connections), [rows, connections])
  const [hover, setHover] = React.useState<{ r: number; c: number } | null>(null)
  if (crews.length < 2) return <p className="px-4 py-6 text-center text-label text-muted-foreground">A link joins two crews; there are not enough to draw a matrix.</p>
  return (
    <>
      <div className="max-h-[70vh] overflow-auto" onMouseLeave={() => setHover(null)}>
        <table className="border-separate border-spacing-0 text-control" role="grid" aria-label="Rows hand work to columns">
          <thead>
            <tr>
              <th className="sticky left-0 top-0 z-30 min-w-[160px] border-b border-r border-border bg-surface-subtle px-3 py-2 text-left align-bottom">
                <span className="eyebrow text-muted-foreground">Hands work to →</span>
              </th>
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
                  <CrewCell crew={row} dup={dups.has(row.name)} />
                </th>
                {crews.map((col, j) => {
                  const on = hover && (hover.r === i || hover.c === j)
                  if (row.id === col.id) return <td key={col.id} className={cn("h-11 border-b border-border/40 text-center text-muted-foreground/40", on && "bg-[var(--row-hover-bg)]")}>—</td>
                  const level = pairLevels(connectionBetween(connections, row.id, col.id), row.id).out
                  const Icon = LEVEL_ICON[level]
                  const label = `${row.name} to ${col.name}: ${LEVEL_LABEL[level]}`
                  const face = level === "off" ? <span aria-hidden>·</span> : <Icon className="h-3.5 w-3.5" aria-hidden />
                  return (
                    <td key={col.id} className={cn("h-11 border-b border-border/40 text-center", on && "bg-[var(--row-hover-bg)]")} onMouseEnter={() => setHover({ r: i, c: j })}>
                      {canManage ? (
                        <button type="button" aria-label={label} title={`${levelSentence(row.name, col.name, level)}. Open`} onClick={() => onCell(row, col)}
                          className={cn("inline-grid h-8 w-11 place-items-center rounded-md transition-colors coarse:h-[44px]", tone(level), level === "off" && "hover:bg-accent hover:text-muted-foreground")}>{face}</button>
                      ) : (
                        <span role="img" aria-label={label} title={levelSentence(row.name, col.name, level)} className={cn("inline-grid h-8 w-11 place-items-center rounded-md", tone(level))}>{face}</span>
                      )}
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="border-t border-border px-4 py-2 text-label text-muted-foreground" data-slot="matrix-note">
        {joined > MATRIX_LIMIT
          ? `The ${MATRIX_LIMIT} most linked of ${joined} crews. A matrix of all of them would be ${joined} × ${joined}; use the list and search for the rest.`
          : `All ${joined} crews these links join. Rows hand work to columns.`}
      </p>
    </>
  )
}

/** All crews and Working alone: a paged table, most linked first. A row opens the crew's page. */
export function CrewsTable({ crews, connections, agentsOf, dups, isMobile, onOpen, resetKey, hint }: {
  crews: Crew[]
  connections: Connection[]
  agentsOf: (c: Crew) => CrewAgent[] | undefined
  dups: Set<string>
  isMobile: boolean
  onOpen: (c: Crew) => void
  resetKey: string
  hint?: string
}) {
  const paged = useShowMore(crews, { resetKey })
  const figures = (c: Crew) => crewFigures(c.id, connections)
  const agents = (c: Crew) => agentCount(c, agentsOf(c))
  return (
    <section className="overflow-hidden rounded-card border border-border bg-card" aria-label="Crews">
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-2.5">
        <b className="text-control">{crews.length} {crews.length === 1 ? "crew" : "crews"}</b>
        <span className="text-label text-muted-foreground">{hint ?? "Most linked first"}</span>
      </div>
      {crews.length === 0 && <p className="px-4 py-6 text-center text-label text-muted-foreground">No crew matches these filters.</p>}
      {crews.length > 0 && (isMobile ? (
        <div>
          {paged.shown.map((c) => {
            const f = figures(c)
            const n = agents(c)
            return (
              <button key={c.id} type="button" onClick={() => onOpen(c)} className="flex min-h-[44px] w-full items-center gap-2.5 border-b border-border px-4 py-3 text-left last:border-b-0">
                <CrewTile crew={c} />
                <span className="min-w-0 flex-1">
                  <b className="block truncate text-control font-medium">{c.name}</b>
                  <small className="block text-label text-muted-foreground">{f.partners ? `works with ${f.partners}` : "works alone"}{n != null ? ` · ${n} ${n === 1 ? "agent" : "agents"}` : ""}</small>
                </span>
                <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
              </button>
            )
          })}
        </div>
      ) : (
        <table className={settingsTable}>
          <thead>
            <tr>
              {["Crew", "Works with", "Hands work to", "Receives from", "Agents", "Network"].map((h) => <th key={h} className={settingsTh}>{h}</th>)}
              <th className={settingsTh} aria-hidden />
            </tr>
          </thead>
          <tbody>
            {paged.shown.map((c) => {
              const f = figures(c)
              const n = agents(c)
              return (
                <tr key={c.id} tabIndex={0} className={settingsTableRowLink} aria-label={`Open ${c.name}`}
                  onClick={() => onOpen(c)} onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onOpen(c) } }}>
                  <td className={settingsTd}><CrewCell crew={c} dup={dups.has(c.name)} /></td>
                  <td className={settingsTd}>{f.partners || <span className="text-muted-foreground">alone</span>}</td>
                  <td className={settingsTd}>{f.out || "—"}</td>
                  <td className={settingsTd}>{f.in || "—"}</td>
                  <td className={settingsTd}>{n == null ? "—" : n || <span className="text-muted-foreground" title="Collects work, has no agents">none</span>}</td>
                  <td className={cn(settingsTd, "text-muted-foreground")}>{c.network_mode == null ? "—" : isOpenNetwork(c) ? "Open" : "Restricted"}</td>
                  <td className={settingsTd}><ChevronRight className="h-3.5 w-3.5 text-muted-foreground" aria-hidden /></td>
                </tr>
              )
            })}
          </tbody>
        </table>
      ))}
      <ShowMore shown={paged.shown.length} total={paged.total} nextCount={paged.nextCount} onMore={paged.showMore} noun="crews" paged={paged.paged} />
    </section>
  )
}
