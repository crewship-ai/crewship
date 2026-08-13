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
 * The card list therefore carries PropertyRow's measurements verbatim (120px
 * label column, `gap-3`, `py-2`, `text-label` medium muted label, `text-body`
 * value, `border-b border-border/40` hairline, no rule under the last pair) so
 * a collapsed table reads as the same product as an issue detail pane.
 *
 * It does not literally render `PropertyRow`: this list is a `<dl>` with real
 * `<dt>`/`<dd>` elements, so the key/value relationship survives into the
 * accessibility tree and every cell keeps the `data-slot`/`data-basis`
 * attributes the em-dash rule is asserted through. PropertyRow is a `div` with
 * a children slot and can carry neither. The classes are shared; only the
 * elements differ, and this comment is the link — if PropertyRow's density
 * changes, this changes with it.
 *
 * What it must NOT be is what it was: an 11px uppercase tracked label over a
 * value at the inherited 16px, which is the stat-block idiom and made a narrow
 * table panel read as a different product from the panel beside it.
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
      <p className="text-body text-muted-foreground">
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
            <p className="text-body text-muted-foreground">
              No rows in the latest push. The producer ran and returned an empty set.
            </p>
          ) : (
            <div data-slot="panel-container" className="@container/panel">
              <div className="overflow-x-auto">
                <table className="hidden w-full border-collapse text-body @md/panel:table">
                  <thead>
                    <tr className="border-b border-border/60">
                      {columns.map((col) => (
                        <th
                          key={col.key}
                          data-key={col.key}
                          scope="col"
                          className={cn(
                            "px-2 py-1.5 text-micro font-semibold uppercase tracking-wider text-muted-foreground",
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
              <ul data-slot="table-cards" className="flex flex-col gap-2 @md/panel:hidden">
                {rows.map((row, i) => (
                  <li
                    key={i}
                    data-slot="table-card"
                    className="rounded-lg border border-border/60 bg-surface-subtle px-3 py-1"
                  >
                    {/*
                     * One grid for the whole card, not one per pair: the label
                     * column is then a single track and every value in the card
                     * starts on the same x, which is the alignment a stack of
                     * per-pair grids quietly loses. `minmax(0,120px)` is
                     * PropertyRow's 120px, allowed to shrink rather than
                     * overflow when a `span: 4` panel gets narrow.
                     *
                     * The tracks stretch rather than centre, so the two halves
                     * of a pair share one baseline for the hairline under them.
                     * PropertyRow can afford `items-center` because its rule is
                     * on the row; here it is on each cell, and a value that
                     * wraps to two lines would otherwise break the rule into
                     * two segments at different heights. The label centres
                     * itself inside its own stretched cell instead.
                     */}
                    <dl className="grid grid-cols-[minmax(0,120px)_minmax(0,1fr)] gap-x-3 text-body">
                      {columns.map((col, ci) => {
                        // The hairline belongs to the pair, and the last pair
                        // has none — PropertyRow's `last:border-0`, which a
                        // two-element pair in one grid cannot express as a
                        // selector.
                        const rule = ci === columns.length - 1 ? "" : "border-b border-border/40"
                        return (
                          <React.Fragment key={col.key}>
                            <dt
                              data-slot="table-card-label"
                              className={cn(
                                "flex items-center py-2 text-label font-medium text-muted-foreground",
                                rule,
                              )}
                            >
                              {col.label ?? col.key}
                            </dt>
                            <Cell
                              as="dd"
                              column={col}
                              value={cellOf(row, col.key, ci)}
                              className={cn("py-2", rule)}
                            />
                          </React.Fragment>
                        )
                      })}
                    </dl>
                  </li>
                ))}
              </ul>
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
        As === "td" ? "px-2 py-1.5" : "min-w-0 break-words text-body text-foreground",
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
