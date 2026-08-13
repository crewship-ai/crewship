"use client"

import * as React from "react"
import { Table2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { EM_DASH, defaultEmptyHint, panelGate, provenanceProducedAt } from "./freshness"
import {
  FailedValue,
  NeverProducedValue,
  PanelAge,
  PanelFrame,
  PanelValue,
  resolveNow,
} from "./panel-frame"
import type { PanelProps, TableAlign, TableCell, TableColumn, TablePayload, TableRow } from "./types"

/**
 * `table.v1` — a semantic `<table>` that collapses to a card list in a narrow
 * container (§3, §9).
 *
 * The collapse is a `@container` query on the panel's own box, not a viewport
 * breakpoint: a table panel with `span: 4` is narrow on a 27" monitor, and a
 * viewport breakpoint would keep showing it six columns of nothing.
 *
 * **The card form is a property list, not a stack of stat blocks.** When the
 * table collapses, each row becomes label/value pairs — and the product already
 * has exactly one way to draw a label/value pair: `components/layout/property-row.tsx`.
 * The card list carries PropertyRow's TYPE exactly (120px label column, `gap-3`,
 * a 12px medium muted label over a 14px value) so a collapsed table reads as the
 * same product as an issue detail pane. It spells those sizes as the Pages
 * register's `.type-page-meta` and `.type-page-value` (`app/globals.css`), which
 * are the same two `--typo-*` tokens PropertyRow's `text-label`/`text-body`
 * resolve to — one scale, two names for two surfaces.
 *
 * It does not literally render `PropertyRow`: this list is a `<dl>` with real
 * `<dt>`/`<dd>` elements, so the key/value relationship survives into the
 * accessibility tree and every cell keeps the `data-slot`/`data-basis`
 * attributes the em-dash rule is asserted through. PropertyRow is a `div` with
 * a children slot and can carry neither.
 *
 * What it must NOT be is what it was: an 11px uppercase tracked label over a
 * value at the inherited 16px, which is the stat-block idiom and made a narrow
 * table panel read as a different product from the panel beside it.
 *
 * ## Where it DEPARTS from PropertyRow, and why (the density of a stack)
 *
 * PropertyRow's rhythm — `py-2` and a hairline under every pair but the last —
 * is right for ONE property list. This is N of them. On the live `flotila` page
 * a `span: 4` table panel is five columns by three rows: fifteen pairs, fifteen
 * rules, in a quarter-width column. Every row is individually correct and the
 * stack is a wall, which is the complaint that produced this change.
 *
 * A property list is READ; a stack of property lists is scanned first and read
 * second, and the two want opposite things. So two measurements move, and only
 * these two:
 *
 *  1. **The interior hairlines go.** PropertyRow's rule separates pairs inside
 *     one list, where the card boundary is far away and the eye has to track
 *     across a gap from a 120px label to a distant value. Repeat that list three
 *     times and the rule is no longer the strongest line on screen — the card's
 *     own border is — and twelve interior rules stop being structure and become
 *     texture. Separation moves up one level: per card, which is where the
 *     grouping actually is. Each card already has a border and a tinted surface
 *     saying so; the rules were saying it a second time, badly.
 *  2. **`py-2` becomes `py-1` inside a card, and the card's own padding grows to
 *     `py-2` to pay for it.** The padding a pair needs is the padding that keeps
 *     it off the rule above and below it. With no rules there is nothing to
 *     clear, and the pair only has to stay off its neighbours. The card gains
 *     the breathing room at its edges, where it separates one row of data from
 *     the next, instead of spending it fifteen times in the middle.
 *
 * Together that is roughly a fifth of the panel's height and all of its stripe
 * texture, with nothing made smaller — which is the house's own answer to
 * density, written into `app/globals.css`: *"the density this product wants
 * comes from how much fits in a card, not from how small the letters are."*
 *
 * What was considered and rejected: shrinking the type (the register exists
 * precisely to stop that, and the owner's complaint was that the type reads too
 * small already); and hiding rows on this page (three workstations, and the
 * third one is the one you are looking for). A cap does exist — see
 * `CARD_CAP` — but it is a bound on the unbounded case, not a fix for this one.
 */
export function TablePanel({ panel, data, now, publicView = false, className }: PanelProps) {
  const clock = resolveNow(now)
  const gate = panelGate(data)
  const payload = (data.payload ?? {}) as TablePayload
  const rows = Array.isArray(payload.rows) ? payload.rows : []
  const columns = resolveColumns(payload.columns, rows)

  let body: React.ReactNode
  if (gate.kind === "failed") {
    body = (
      <FailedValue
        failure={data.failure}
        publicView={publicView}
        producedAt={provenanceProducedAt(data.provenance)}
        now={clock}
      />
    )
  } else if (gate.kind === "never") {
    body = <NeverProducedValue hint={data.emptyHint?.trim() || defaultEmptyHint(panel)} />
  } else if (columns.length === 0) {
    // A payload with no columns is a produced payload that describes nothing —
    // still not an em dash, because the producer did run.
    body = (
      <p className="type-page-value text-muted-foreground">
        The latest push declared no columns. Add a `columns` array to the payload.
      </p>
    )
  } else {
    body = (
      <div className="flex flex-col gap-2">
        {gate.dimmed ? (
          <PanelAge producedAt={provenanceProducedAt(data.provenance)} now={clock} />
        ) : null}
        <PanelValue basis="measured" dimmed={gate.dimmed}>
          {rows.length === 0 ? (
            <p className="type-page-value text-muted-foreground">
              No rows in the latest push. The producer ran and returned an empty set.
            </p>
          ) : (
            <div data-slot="panel-container" className="@container/panel">
              <div className="overflow-x-auto">
                <table className="type-page-value hidden w-full border-collapse @md/panel:table">
                  <thead>
                    <tr className="border-b border-border/60">
                      {columns.map((col) => (
                        <th
                          key={col.key}
                          data-key={col.key}
                          scope="col"
                          className={cn(
                            "type-page-label px-2 py-1.5 text-muted-foreground",
                            alignClass(col.align),
                          )}
                        >
                          {col.label ?? col.key}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((row, i) => (
                      <tr key={i} className="border-b border-border/40 last:border-0">
                        {columns.map((col, ci) => (
                          <Cell
                            key={col.key}
                            as="td"
                            column={col}
                            value={cellOf(row, col.key, ci)}
                          />
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {/* The same rows, one card each, for a narrow panel. */}
              <TableCards columns={columns} rows={rows} />
            </div>
          )}
        </PanelValue>
      </div>
    )
  }

  return (
    <PanelFrame
      panel={panel}
      data={data}
      now={clock}
      publicView={publicView}
      className={className}
      icon={Table2}
    >
      {body}
    </PanelFrame>
  )
}

/**
 * How many cards a collapsed table draws before it offers the rest behind a
 * click.
 *
 * The cap is on the CARD form only, and that asymmetry is the argument for it.
 * A wide table spends one row of height per payload row; the card form spends
 * one row per CELL, so the same `maxItems: 200` payload (§11b.12) that is a
 * long-but-ordinary table in a `span: 12` panel is five to ten times that in a
 * `span: 4` column — a panel with no upper bound on its height, inside a page
 * of panels that all have one. §11b.12 accepts 200 rows explicitly because it
 * is "more than anyone reads and more than we will virtualise"; this is the
 * client honouring the second half of that sentence.
 *
 * Eight, because it has to be past every table a human authors for a narrow
 * panel and short of the ones a script generates. The live `flotila` page has
 * three, so nothing on the page that prompted this change is hidden by it —
 * that density was fixed by the rhythm above, not here.
 *
 * Nothing is hidden silently: the button states the true total, so a reader
 * who has only ever seen the collapsed form still knows how many rows exist.
 */
const CARD_CAP = 8

/**
 * The card list. A component rather than an inline map because it is the one
 * part of this panel that holds state — the cap's disclosure — and because the
 * measurements it departs from PropertyRow on are worth one place to read.
 */
function TableCards({ columns, rows }: { columns: TableColumn[]; rows: TableRow[] }) {
  const [showAll, setShowAll] = React.useState(false)
  const capped = rows.length > CARD_CAP && !showAll
  const visible = capped ? rows.slice(0, CARD_CAP) : rows

  return (
    <div data-slot="table-cards" className="flex flex-col gap-2 @md/panel:hidden">
      <ul data-slot="table-card-list" className="flex flex-col gap-2">
        {visible.map((row, i) => (
          <li
            key={i}
            data-slot="table-card"
            className="rounded-lg border border-border/60 bg-surface-subtle px-3 py-2"
          >
            {/*
             * One grid for the whole card, not one per pair: the label column
             * is then a single track and every value in the card starts on the
             * same x, which is the alignment a stack of per-pair grids quietly
             * loses. `minmax(0,120px)` is PropertyRow's 120px, allowed to
             * shrink rather than overflow when a `span: 4` panel gets narrow.
             *
             * `items-center` on the label is PropertyRow's, and it is safe here
             * now: it was `items-stretch` only so that a value wrapping to two
             * lines could not break the pair's hairline into two segments at
             * different heights. There is no hairline inside a card any more —
             * see the file header — so the constraint it existed for is gone.
             */}
            <dl className="type-page-value grid grid-cols-[minmax(0,120px)_minmax(0,1fr)] gap-x-3">
              {columns.map((col, ci) => (
                <React.Fragment key={col.key}>
                  <dt
                    data-slot="table-card-label"
                    className="type-page-meta flex items-center py-1 font-medium text-muted-foreground"
                  >
                    {col.label ?? col.key}
                  </dt>
                  <Cell
                    as="dd"
                    column={col}
                    value={cellOf(row, col.key, ci)}
                    className="py-1"
                  />
                </React.Fragment>
              ))}
            </dl>
          </li>
        ))}
      </ul>
      {rows.length > CARD_CAP ? (
        <button
          type="button"
          data-slot="table-cards-toggle"
          data-expanded={showAll ? "true" : "false"}
          onClick={() => setShowAll((v) => !v)}
          className="type-page-meta self-start rounded-md px-1 py-0.5 text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
        >
          {capped ? `Show all ${rows.length} rows` : `Show first ${CARD_CAP}`}
        </button>
      ) : null}
    </div>
  )
}

/**
 * One cell. A `null` cell is "no basis" and renders the em dash; a `0` is a
 * measured zero and renders `0` (§9b.4) — the same rule the panel value obeys,
 * applied per cell.
 *
 * An empty string is DATA, not an em dash. `panel.table.v1.json` says so —
 * *"`null` is no data and renders as an em dash, which is a different claim
 * from `0` or an empty string"* — and Go agrees: `Cell.IsNoData()` in
 * internal/pages/payload.go is true for JSON null alone. A client that drew a
 * dash over `""` would be reporting "we have nothing to look at" about a cell
 * the producer deliberately emptied, which is the one glyph in this product
 * that must mean the same thing on both sides of the wire.
 *
 * Two presentation rules ride along, and both are here rather than at the two
 * call sites so a `<td>` and its `<dd>` can never drift apart:
 *
 *  - **`tabular-nums` on every cell, not only on numeric ones.** The alignment
 *    a column of figures needs is a property of the COLUMN, and a producer that
 *    pushes a port as `"8083"` must line up under one that pushes `8083`. The
 *    feature only changes the advance width of digit glyphs, so it costs a cell
 *    of words nothing; making it conditional on the JS type would leave the one
 *    case that actually matters — the stringly-typed payload — unaligned.
 *  - **The declared alignment holds in the card form too.** `align: "right"` is
 *    a statement about the value, not about which of the two layouts is on
 *    screen, so the `<dd>` right-aligns inside its grid track exactly as the
 *    `<td>` does inside its column.
 *
 * There is deliberately no colour here. §3 reserves the status colours, and a
 * cell tinted on the strength of its own value would be this panel inventing a
 * second, unlabelled status vocabulary — green in a table that means neither
 * "ok" nor "series 3" but "some number this component decided it liked".
 */
function Cell({
  as: As,
  column,
  value,
  className,
}: {
  as: "td" | "dd"
  column: TableColumn
  value: TableCell
  className?: string
}) {
  const missing = value === null || value === undefined
  return (
    <As
      data-slot="table-cell"
      data-key={column.key}
      data-basis={missing ? "none" : "measured"}
      className={cn(
        "tabular-nums",
        As === "td" ? "px-2 py-1.5" : "type-page-value min-w-0 break-words text-foreground",
        alignClass(column.align),
        missing && "text-muted-foreground-soft",
        className,
      )}
    >
      {missing ? EM_DASH : formatCell(value)}
    </As>
  )
}

function formatCell(value: TableCell): string {
  if (typeof value === "boolean") return value ? "yes" : "no"
  return String(value)
}

function alignClass(align?: TableAlign | null): string {
  if (align === "right") return "text-right"
  if (align === "center") return "text-center"
  return "text-left"
}

/** Keyed rows are the documented shape; positional rows index into columns. */
function cellOf(row: TableRow, key: string, index: number): TableCell {
  if (Array.isArray(row)) return row[index]
  if (row && typeof row === "object") {
    return Object.prototype.hasOwnProperty.call(row, key)
      ? (row as Record<string, TableCell>)[key]
      : null
  }
  return null
}

/**
 * §3 gives `columns[{key,label,align?}]`. When a producer omits them entirely
 * the keys of the first keyed row are the next best truth — better than a
 * blank panel, and it is the only inference made anywhere in this file.
 */
function resolveColumns(
  declared: TableColumn[] | null | undefined,
  rows: TableRow[],
): TableColumn[] {
  const valid = Array.isArray(declared)
    ? declared.filter((c): c is TableColumn => Boolean(c) && typeof c.key === "string" && c.key !== "")
    : []
  if (valid.length > 0) return valid
  const first = rows.find((r) => r && typeof r === "object" && !Array.isArray(r))
  if (!first) return []
  return Object.keys(first as Record<string, TableCell>).map((key) => ({ key, label: key }))
}
