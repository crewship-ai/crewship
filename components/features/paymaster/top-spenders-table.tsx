"use client"

import { Badge } from "@/components/ui/badge"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { TopSpenderRow } from "@/lib/types/paymaster"

interface TopSpendersTableProps {
  rows: TopSpenderRow[]
  loading: boolean
}

/** Format a number with thousand separators, no decimals. */
function formatInt(n: number): string {
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(n)
}

/** Resolve display name + scope hint for a row — handles all scope shapes. */
function resolveName(row: TopSpenderRow): { label: string; hint: string } {
  if (row.mission_name) return { label: row.mission_name, hint: "mission" }
  if (row.mission_id) return { label: row.mission_id.slice(0, 8), hint: "mission" }
  if (row.agent_name) return { label: row.agent_name, hint: "agent" }
  if (row.agent_id) return { label: row.agent_id.slice(0, 8), hint: "agent" }
  if (row.crew_name) return { label: row.crew_name, hint: "crew" }
  if (row.crew_id) return { label: row.crew_id.slice(0, 8), hint: "crew" }
  return { label: row.scope ?? "—", hint: row.scope_type ?? "" }
}

/**
 * Dense table of the heaviest spenders. The trend column is intentionally
 * omitted until we have per-row history — surfacing a zero-line sparkline
 * would be misleading.
 */
export function TopSpendersTable({ rows, loading }: TopSpendersTableProps) {
  if (loading) {
    return (
      <div className="py-10 text-center text-[11px] text-muted-foreground">Loading top spenders…</div>
    )
  }
  if (rows.length === 0) {
    return (
      <div className="py-10 text-center text-[11px] text-muted-foreground">
        No spenders to show yet.
      </div>
    )
  }

  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead className="w-10 text-[10px] uppercase tracking-wider text-muted-foreground">#</TableHead>
          <TableHead className="text-[10px] uppercase tracking-wider text-muted-foreground">Scope</TableHead>
          <TableHead className="text-right text-[10px] uppercase tracking-wider text-muted-foreground">Cost</TableHead>
          <TableHead className="text-right text-[10px] uppercase tracking-wider text-muted-foreground">Calls</TableHead>
          <TableHead className="text-right text-[10px] uppercase tracking-wider text-muted-foreground">Tokens</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row, idx) => {
          const { label, hint } = resolveName(row)
          return (
            <TableRow key={`${hint}-${label}-${idx}`}>
              <TableCell className="font-mono text-[11px] text-muted-foreground">{idx + 1}</TableCell>
              <TableCell>
                <div className="flex items-center gap-2 min-w-0">
                  <span className="text-sm truncate">{label}</span>
                  {hint && (
                    <Badge variant="outline" className="text-[10px] font-mono uppercase tracking-wider border-border/60">
                      {hint}
                    </Badge>
                  )}
                </div>
              </TableCell>
              <TableCell className="text-right font-mono tabular-nums text-[12px]">
                ${row.cost_usd.toFixed(4)}
              </TableCell>
              <TableCell className="text-right font-mono tabular-nums text-[12px] text-muted-foreground">
                {formatInt(row.call_count)}
              </TableCell>
              <TableCell className="text-right font-mono tabular-nums text-[12px] text-muted-foreground">
                {formatInt(row.total_tokens)}
              </TableCell>
            </TableRow>
          )
        })}
      </TableBody>
    </Table>
  )
}
