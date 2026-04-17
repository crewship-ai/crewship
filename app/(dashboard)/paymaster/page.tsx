"use client"

import { useMemo, useState } from "react"
import { DollarSign } from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { PaymasterKpiCard } from "@/components/features/paymaster/kpi-card"
import { SpendChart, type SpendDatum } from "@/components/features/paymaster/spend-chart"
import { TopSpendersTable } from "@/components/features/paymaster/top-spenders-table"
import { TimeRangePicker } from "@/components/features/paymaster/time-range-picker"
import { useAgentSpend, useCrewSpend, useTopSpenders } from "@/hooks/use-paymaster"
import type { PaymasterRange } from "@/lib/types/paymaster"

/**
 * Paymaster dashboard — aggregates LLM spend across crews, agents, and
 * missions. Reads from `/api/v1/paymaster/*`; degrades to "not yet
 * configured" when the backend isn't shipping Paymaster data.
 */
export default function PaymasterPage() {
  const [range, setRange] = useState<PaymasterRange>("7d")
  const [selectedCrewId, setSelectedCrewId] = useState<string | null>(null)
  // reloadKey bumps on Retry to force the hooks to refetch without
  // flipping range to a different value and back. setRange(range) is a
  // no-op because React bails on identical string state.
  const [reloadKey, setReloadKey] = useState(0)

  const crewSpend = useCrewSpend(range, true, reloadKey)
  const agentSpend = useAgentSpend(selectedCrewId, range, reloadKey)
  const topSpenders = useTopSpenders(range, 10, reloadKey)

  // If either endpoint 404s we still want the "not configured" fallback
  // rather than showing half a dashboard with confusing empty cards —
  // both 404ing is not a precondition, either one is enough. The prior
  // `&&` meant a partial deployment (one handler wired, the other not)
  // still tried to render the dashboard and broke.
  const notConfigured =
    crewSpend.notConfigured || topSpenders.notConfigured

  // Memoise raw row arrays — `?? []` otherwise creates a new array every
  // render, which cascades into downstream useMemo hooks.
  const crewRows = useMemo(() => crewSpend.data?.rows ?? [], [crewSpend.data])
  const agentRows = useMemo(() => agentSpend.data?.rows ?? [], [agentSpend.data])
  const topRows = useMemo(() => topSpenders.data?.rows ?? [], [topSpenders.data])

  const totals = useMemo(() => {
    let totalCost = 0
    let totalCalls = 0
    for (const r of crewRows) {
      totalCost += r.cost_usd
      totalCalls += r.call_count
    }
    return { totalCost, totalCalls }
  }, [crewRows])

  const topAgentLabel = useMemo(() => {
    // Pick the actual max-cost agent rather than trusting the API's row
    // order. The backend sorts by cost DESC today, but the KPI
    // shouldn't silently go wrong if that ever changes (e.g., the
    // frontend starts filtering / re-sorting before we get here).
    const top = topRows.reduce<typeof topRows[number] | null>((best, row) => {
      if (!row.agent_id && !row.agent_name) return best
      if (!best) return row
      return row.cost_usd > best.cost_usd ? row : best
    }, null)
    if (!top) return "—"
    return top.agent_name ?? top.agent_id?.slice(0, 8) ?? "—"
  }, [topRows])

  const crewChartData = useMemo<SpendDatum[]>(
    () =>
      crewRows
        .slice()
        .sort((a, b) => b.cost_usd - a.cost_usd)
        .slice(0, 10)
        .map((r) => ({
          id: r.crew_id,
          name: r.crew_name ?? r.crew_id.slice(0, 8),
          cost_usd: r.cost_usd,
        })),
    [crewRows],
  )

  const agentChartData = useMemo<SpendDatum[]>(
    () =>
      agentRows
        .slice()
        .sort((a, b) => b.cost_usd - a.cost_usd)
        .slice(0, 10)
        .map((r) => ({
          id: r.agent_id,
          name: r.agent_name ?? r.agent_id.slice(0, 8),
          cost_usd: r.cost_usd,
        })),
    [agentRows],
  )

  const selectedCrewName = useMemo(() => {
    if (!selectedCrewId) return ""
    const row = crewRows.find((c) => c.crew_id === selectedCrewId)
    return row?.crew_name ?? selectedCrewId.slice(0, 8)
  }, [crewRows, selectedCrewId]);

  // Rough cost-per-top-spender: total crew spend divided by the number of
  // top-spender rows. The KPI card labels it "Avg cost / mission" as a
  // placeholder — swap this for a real per-mission figure once the
  // backend exposes mission-level rollups.
  const avgCostPerMission = totals.totalCalls === 0 ? 0 : totals.totalCost / Math.max(1, topRows.length)

  if (notConfigured) {
    return <PaymasterNotConfigured />
  }

  const initialLoading = crewSpend.loading && !crewSpend.data && !crewSpend.error

  return (
    <div className="p-4 md:p-6 space-y-5">
      <header className="flex items-center justify-between gap-3 flex-wrap">
        <div className="flex items-center gap-2">
          <DollarSign className="h-4 w-4 text-foreground/60" />
          <h1 className="text-body font-medium text-foreground/80">Paymaster</h1>
        </div>
        <TimeRangePicker value={range} onChange={setRange} />
      </header>

      {crewSpend.error && (
        <div className="rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-[12px] text-red-300 flex items-center justify-between gap-2">
          <span>Couldn&apos;t load spend ({crewSpend.error}).</span>
          <Button variant="outline" size="sm" className="h-6 px-2 text-[11px]" onClick={() => setReloadKey((k) => k + 1)}>
            Retry
          </Button>
        </div>
      )}

      <section className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <PaymasterKpiCard
          label="Total spend"
          value={`$${totals.totalCost.toFixed(2)}`}
          subtitle={`range ${range}`}
        />
        <PaymasterKpiCard
          label="Total calls"
          value={new Intl.NumberFormat().format(totals.totalCalls)}
          subtitle={`${crewRows.length} crews`}
        />
        <PaymasterKpiCard
          label="Avg cost / mission"
          value={`$${avgCostPerMission.toFixed(3)}`}
          subtitle="across top spenders"
        />
        <PaymasterKpiCard
          label="Top agent"
          value={topAgentLabel}
          subtitle="by cost"
        />
      </section>

      <section className="grid grid-cols-1 lg:grid-cols-2 gap-3">
        <Card className="py-3">
          <CardHeader className="px-4 pb-2">
            <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground flex items-center justify-between">
              <span>Spend by crew</span>
              {selectedCrewId && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="h-6 px-1.5 text-[11px]"
                  onClick={() => setSelectedCrewId(null)}
                >
                  Clear
                </Button>
              )}
            </CardTitle>
          </CardHeader>
          <CardContent className="px-3">
            {initialLoading ? (
              <div className="h-[260px] flex items-center justify-center text-[11px] text-muted-foreground">
                Loading…
              </div>
            ) : crewChartData.length === 0 ? (
              <EmptyState />
            ) : (
              <SpendChart
                data={crewChartData}
                onSelect={(id) => setSelectedCrewId(id === selectedCrewId ? null : id)}
                selectedId={selectedCrewId}
              />
            )}
          </CardContent>
        </Card>

        <Card className="py-3">
          <CardHeader className="px-4 pb-2">
            <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
              {selectedCrewId ? `Spend by agent · ${selectedCrewName}` : "Spend by agent"}
            </CardTitle>
          </CardHeader>
          <CardContent className="px-3">
            {!selectedCrewId ? (
              <div className="h-[260px] flex items-center justify-center text-[11px] text-muted-foreground text-center max-w-xs mx-auto">
                Select a crew on the left to see agent-level breakdown.
              </div>
            ) : agentSpend.loading && agentChartData.length === 0 ? (
              <div className="h-[260px] flex items-center justify-center text-[11px] text-muted-foreground">
                Loading…
              </div>
            ) : agentChartData.length === 0 ? (
              <div className="h-[260px] flex items-center justify-center text-[11px] text-muted-foreground">
                No agent spend recorded.
              </div>
            ) : (
              <SpendChart data={agentChartData} color="var(--chart-2)" />
            )}
          </CardContent>
        </Card>
      </section>

      <section>
        <Card className="py-3">
          <CardHeader className="px-4 pb-2">
            <CardTitle className="text-[12px] font-semibold uppercase tracking-wider text-muted-foreground">
              Top spenders
            </CardTitle>
          </CardHeader>
          <CardContent className="px-3">
            <TopSpendersTable rows={topRows} loading={topSpenders.loading && topRows.length === 0} />
          </CardContent>
        </Card>
      </section>
    </div>
  )
}

function EmptyState() {
  return (
    <div className="h-[260px] flex flex-col items-center justify-center text-center">
      <div className="text-[13px] font-medium text-foreground/80">No spend recorded</div>
      <div className="mt-1 text-[11px] text-muted-foreground max-w-xs">
        Agents haven&apos;t made LLM calls yet. Run a mission to start populating this view.
      </div>
    </div>
  )
}

function PaymasterNotConfigured() {
  return (
    <div className="flex flex-col items-center gap-2 py-24 text-center">
      <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
        <DollarSign className="h-4 w-4 text-muted-foreground/60" />
      </div>
      <div className="text-sm font-medium text-foreground/80">Paymaster not yet configured</div>
      <div className="text-[11px] text-muted-foreground max-w-sm">
        The Paymaster spend API isn&apos;t available on this backend. Once LLM
        cost tracking is wired up, aggregated spend will appear here.
      </div>
    </div>
  )
}
