"use client"

// Activity › Spend (OWNER / ADMIN; J04, v67 `spendView`).
//
//   KPI row     Spend · 24 h · Spend · {this month} · Model calls · 24 h · Most expensive
//   By crew     this month, one bar per crew, as a share of the month
//   Most expensive work · 24 h
//   The note: admins only; on subscription plans the cost is an estimate.
//
// The windows are v67's: the last 24 hours and the calendar month so far.
// Following the top bar's range instead is a proposed deviation (ODCH-V09),
// not built. The month's "of $80 budget" needs a workspace budget the
// backend does not have yet (#3475), so nothing pretends there is one.
//
// Data: GET /api/v1/journal/spend?window=24h (total, calls per agent, the
// most expensive runs) and GET /api/v1/paymaster/spend/by-crew?since=<the
// 1st> (the month by crew). Each card fails on its own.

import * as React from "react"
import Link from "next/link"
import { z } from "zod"

import { PageError } from "@/components/layout/page-state"
import { CrewIcon } from "@/components/ui/crew-icon"
import { Skeleton } from "@/components/ui/skeleton"
import { useApiResource } from "@/hooks/use-api-resource"
import { useJournalLookup } from "@/hooks/use-journal-lookup"
import { useJournalSpend } from "@/hooks/use-journal-spend"
import { cn } from "@/lib/utils"

// by-crew sends input_tokens / output_tokens; only these fields are read.
const monthByCrewSchema = z.object({
  rows: z.array(z.object({ crew_id: z.string(), cost_usd: z.number(), call_count: z.number() })),
})
type MonthByCrew = z.infer<typeof monthByCrewSchema>

export function formatSpend(usd: number): string {
  if (usd > 0 && usd < 0.01) return `$${usd.toFixed(4)}`
  return `$${usd.toFixed(2)}`
}

/** Midnight on the 1st of this month, local time. */
export function startOfMonth(now: Date): Date {
  return new Date(now.getFullYear(), now.getMonth(), 1)
}

function Kpi({ label, value, note, loading }: { label: string; value: React.ReactNode; note?: React.ReactNode; loading: boolean }) {
  return (
    <div data-kpi className="min-w-0 border-border px-4 py-3.5 [&:nth-child(n+2)]:border-l max-md:[&:nth-child(3)]:border-l-0 max-md:[&:nth-child(n+3)]:border-t">
      <small className="block text-body text-muted-foreground">{label}</small>
      {loading ? (
        <Skeleton className="mt-1 h-7 w-20" />
      ) : (
        <span className="flex min-w-0 items-baseline gap-1.5">
          <b className="text-title font-semibold tabular-nums">{value}</b>
          {note && <em className="truncate text-label not-italic text-muted-foreground">{note}</em>}
        </span>
      )}
    </div>
  )
}

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="overflow-hidden rounded-xl border border-border bg-card">
      <h2 className="border-b border-border px-4 py-3 text-body font-semibold">{title}</h2>
      {children}
    </section>
  )
}

export function ActivitySpendView({ workspaceId }: { workspaceId: string }) {
  const lookup = useJournalLookup()
  const [now] = React.useState(() => new Date())
  const month = now.toLocaleDateString("en-GB", { month: "long" })
  const since = startOfMonth(now).toISOString()

  const day = useJournalSpend(workspaceId, "24h", 4)
  const byCrew = useApiResource<MonthByCrew>(
    `/api/v1/paymaster/spend/by-crew?workspace_id=${encodeURIComponent(workspaceId)}&since=${encodeURIComponent(since)}`,
    { schema: monthByCrewSchema, fallback: { rows: [] }, on404: "error" },
  )

  const calls = React.useMemo(() => (day.data?.by_agent ?? []).reduce((n, r) => n + r.call_count, 0), [day.data])
  const top = day.data?.top_runs ?? []
  const crews = React.useMemo(() => [...(byCrew.data?.rows ?? [])].sort((a, b) => b.cost_usd - a.cost_usd), [byCrew.data])
  const monthTotal = crews.reduce((n, r) => n + r.cost_usd, 0)

  // The cost reads are OWNER/ADMIN on the server (#3590). A 403 here (the page
  // already hides this view from other roles) is one quiet note, not a failed
  // load: no error cards, no toast.
  if (day.error === "HTTP 403" || byCrew.error === "HTTP 403") {
    return (
      <div className="mx-auto grid min-w-0 max-w-7xl gap-3.5 p-4 md:p-6">
        <header className="min-w-0">
          <h1 className="text-title font-semibold tracking-tight">Spend</h1>
        </header>
        <p className="rounded-xl border border-border bg-card px-4 py-3 text-body text-muted-foreground">
          Spend is unavailable: only workspace owners and admins can see costs.
        </p>
      </div>
    )
  }

  return (
    <div className="mx-auto grid min-w-0 max-w-7xl gap-3.5 p-4 md:p-6">
      <header className="min-w-0">
        <h1 className="text-title font-semibold tracking-tight">Spend</h1>
        <p className="mt-0.5 text-body text-muted-foreground">What the work cost, by crew and by piece of work</p>
      </header>

      <div className="grid grid-cols-2 overflow-hidden rounded-xl border border-border bg-card md:grid-cols-4">
        <Kpi label="Spend · 24 h" value={formatSpend(day.data?.total_cost_usd ?? 0)} loading={day.loading && !day.data} />
        <Kpi label={`Spend · ${month}`} value={byCrew.error ? "—" : formatSpend(monthTotal)} loading={byCrew.loading && !byCrew.data} />
        <Kpi label="Model calls · 24 h" value={calls.toLocaleString("en-GB")} loading={day.loading && !day.data} />
        <Kpi
          label="Most expensive"
          value={top[0] ? formatSpend(top[0].cost_usd) : "—"}
          note={top[0]?.label}
          loading={day.loading && !day.data}
        />
      </div>

      {day.error && <PageError title="Could not load the last 24 hours" description="Spend by crew below is up to date. One part failed; the rest works." detail={day.error} />}
      {day.data?.truncated && (
        <p className="text-label text-warn">The last 24 hours exceed the aggregation cap; the figures cover the most recent rows only.</p>
      )}

      <Card title={`By crew · ${month}`}>
        {byCrew.error ? (
          <PageError title="Could not load spend by crew" description="The last 24 hours above are up to date. One part failed; the rest works." detail={byCrew.error} />
        ) : byCrew.loading && !byCrew.data ? (
          <div className="p-4">
            <Skeleton className="h-16 w-full" />
          </div>
        ) : crews.length === 0 ? (
          <p className="px-4 py-3 text-body text-muted-foreground">No spend recorded this month.</p>
        ) : (
          <ul className="py-1.5">
            {crews.map((r) => {
              const crew = r.crew_id ? lookup.crews.get(r.crew_id) : undefined
              const share = monthTotal > 0 ? Math.round((r.cost_usd / monthTotal) * 100) : 0
              return (
                <li key={r.crew_id || "none"} className="grid grid-cols-[auto_minmax(0,9rem)_minmax(0,1fr)_auto] items-center gap-2.5 px-4 py-2">
                  <CrewIcon icon={crew?.icon ?? ""} color={crew?.color} size="sm" />
                  <span className="truncate text-body">{crew?.name ?? (r.crew_id ? r.crew_id : "No crew")}</span>
                  <span aria-hidden className="h-2 overflow-hidden rounded-full bg-muted">
                    <span data-bar className="block h-full rounded-full bg-primary" style={{ width: `${share}%` }} />
                  </span>
                  <span className="text-right font-mono text-label tabular-nums">{formatSpend(r.cost_usd)}</span>
                </li>
              )
            })}
          </ul>
        )}
      </Card>

      <Card title="Most expensive work · 24 h">
        {day.loading && !day.data ? (
          <div className="p-4">
            <Skeleton className="h-16 w-full" />
          </div>
        ) : top.length === 0 ? (
          <p className="px-4 py-3 text-body text-muted-foreground">{day.error ? "Not available while the last 24 hours fail to load." : "No spend in the last 24 hours."}</p>
        ) : (
          <ul>
            {top.map((r) => (
              <li key={`${r.kind}:${r.id}`} className="border-b border-border last:border-b-0">
                <Link
                  href={r.kind === "run" ? `/activity?run=${encodeURIComponent(r.id)}` : `/activity?pipeline=${encodeURIComponent(r.label)}`}
                  className={cn("kit-tap flex min-w-0 items-center gap-3 px-4 py-2.5 hover:bg-muted/60")}
                >
                  <span className="min-w-0 flex-1 truncate text-body font-medium">{r.label}</span>
                  <span className="shrink-0 font-mono text-label tabular-nums text-muted-foreground">{formatSpend(r.cost_usd)}</span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <p className="text-label text-muted-foreground">Admins only. On subscription plans the cost is an estimate, not money spent.</p>
    </div>
  )
}
