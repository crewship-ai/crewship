"use client"

import * as React from "react"
import { Table2 } from "lucide-react"

import { cn } from "@/lib/utils"
import { EM_DASH, defaultEmptyHint, panelGate } from "./freshness"
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
 */
export function TablePanel({ panel, data, now, publicView = false, className }: PanelProps) {
  const clock = resolveNow(now)
  const gate = panelGate(data)
  const payload = (data.payload ?? {}) as TablePayload
  const rows = Array.isArray(payload.rows) ? payload.rows : []
  const columns = resolveColumns(payload.columns, rows)

  let body: React.ReactNode
  if (gate.kind === "failed") {
    body = <FailedValue failure={data.failure} publicView={publicView} />
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
        {gate.dimmed ? <PanelAge producedAt={data.provenance?.producedAt} now={clock} /> : null}
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
                            "px-2 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground",
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
                    className="rounded-lg border border-border/60 bg-surface-subtle p-2.5"
                  >
                    <dl className="grid grid-cols-[minmax(0,auto)_minmax(0,1fr)] gap-x-3 gap-y-1">
                      {columns.map((col, ci) => (
                        <React.Fragment key={col.key}>
                          <dt className="text-[11px] uppercase tracking-wider text-muted-foreground-soft">
                            {col.label ?? col.key}
                          </dt>
                          <Cell as="dd" column={col} value={cellOf(row, col.key, ci)} />
                        </React.Fragment>
                      ))}
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
 * One cell. A missing cell is "no basis" and renders the em dash; a `0` is a
 * measured zero and renders `0` (§9b.4) — the same rule the panel value obeys,
 * applied per cell.
 */
function Cell({
  as: As,
  column,
  value,
}: {
  as: "td" | "dd"
  column: TableColumn
  value: TableCell
}) {
  const missing = value === null || value === undefined || value === ""
  return (
    <As
      data-slot="table-cell"
      data-key={column.key}
      data-basis={missing ? "none" : "measured"}
      className={cn(
        As === "td" ? "px-2 py-1.5" : "min-w-0 break-words",
        typeof value === "number" && "tabular-nums",
        alignClass(column.align),
        missing && "text-muted-foreground-soft",
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
