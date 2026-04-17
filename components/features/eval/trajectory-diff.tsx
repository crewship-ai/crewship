"use client"

import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import type { JournalEntry } from "@/lib/types/journal"

interface TrajectoryDiffProps {
  run: JournalEntry | null
  /** Sibling `eval.metric` entries belonging to this run. */
  metrics: JournalEntry[]
}

interface DiffRow {
  name: string
  baseline: number | null
  candidate: number | null
  delta: number | null
  unit?: string
}

function toNum(v: unknown): number | null {
  if (typeof v === "number") return v
  if (typeof v === "string") {
    const n = Number(v)
    return Number.isFinite(n) ? n : null
  }
  return null
}

/** Extract a flat list of diff rows from the metric entries' payloads. */
function buildRows(metrics: JournalEntry[]): DiffRow[] {
  const rows: DiffRow[] = []
  for (const m of metrics) {
    const p = m.payload ?? {}
    const name = typeof p.name === "string" ? (p.name as string) : typeof p.metric === "string" ? (p.metric as string) : "metric"
    const baseline = toNum(p.baseline)
    const candidate = toNum(p.candidate ?? p.value)
    const delta =
      toNum(p.delta) ??
      (baseline !== null && candidate !== null ? candidate - baseline : null)
    const unit = typeof p.unit === "string" ? (p.unit as string) : undefined
    rows.push({ name, baseline, candidate, delta, unit })
  }
  return rows
}

/**
 * Side-by-side metrics table for one eval run. Falls back to an empty
 * state prompt when no metric entries exist for the selected run.
 */
export function TrajectoryDiff({ run, metrics }: TrajectoryDiffProps) {
  if (!run) return null
  const rows = buildRows(metrics)

  if (rows.length === 0) {
    return (
      <div className="rounded-lg border border-border/60 bg-card p-4 text-center">
        <div className="text-sm font-medium text-foreground/80">No comparison data</div>
        <div className="mt-1 text-[11px] text-muted-foreground max-w-sm mx-auto">
          Run a replay to see metrics here.
        </div>
      </div>
    )
  }

  return (
    <div className="rounded-lg border border-border/60 bg-card overflow-hidden">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="text-[10px] uppercase tracking-wider text-muted-foreground">Metric</TableHead>
            <TableHead className="text-right text-[10px] uppercase tracking-wider text-muted-foreground">Baseline</TableHead>
            <TableHead className="text-right text-[10px] uppercase tracking-wider text-muted-foreground">Candidate</TableHead>
            <TableHead className="text-right text-[10px] uppercase tracking-wider text-muted-foreground">Δ</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r, idx) => {
            const direction = r.delta === null ? "flat" : r.delta > 0 ? "up" : r.delta < 0 ? "down" : "flat"
            return (
              <TableRow key={`${r.name}-${idx}`}>
                <TableCell>
                  <div className="flex items-center gap-2">
                    <span className="text-sm">{r.name}</span>
                    {r.unit && (
                      <Badge variant="outline" className="text-[10px] font-mono border-border/60">
                        {r.unit}
                      </Badge>
                    )}
                  </div>
                </TableCell>
                <TableCell className="text-right font-mono tabular-nums text-[12px] text-muted-foreground">
                  {r.baseline === null ? "—" : r.baseline.toFixed(3)}
                </TableCell>
                <TableCell className="text-right font-mono tabular-nums text-[12px]">
                  {r.candidate === null ? "—" : r.candidate.toFixed(3)}
                </TableCell>
                <TableCell
                  className={cn(
                    "text-right font-mono tabular-nums text-[12px]",
                    direction === "up" && "text-emerald-400",
                    direction === "down" && "text-red-400",
                    direction === "flat" && "text-muted-foreground",
                  )}
                >
                  {r.delta === null
                    ? "—"
                    : `${r.delta > 0 ? "+" : ""}${r.delta.toFixed(3)}`}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}
