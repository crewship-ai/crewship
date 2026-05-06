"use client"

import { useEffect, useMemo, useState } from "react"
import { useSearchParams } from "next/navigation"
import { DollarSign, Filter, RefreshCw } from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Button } from "@/components/ui/button"
import { PaymasterKpiCard } from "@/components/features/paymaster/kpi-card"
import { SpendChart, type SpendDatum } from "@/components/features/paymaster/spend-chart"
import { TopSpendersTable } from "@/components/features/paymaster/top-spenders-table"
import { TimeRangePicker } from "@/components/features/paymaster/time-range-picker"
import { SubscriptionsPanel } from "@/components/features/paymaster/subscriptions-panel"
import {
  useAgentSpend,
  useCrewSpend,
  useSubscriptionUsage,
  useTopSpenders,
} from "@/hooks/use-paymaster"
import type { PaymasterRange } from "@/lib/types/paymaster"
import { cn } from "@/lib/utils"

/**
 * Paymaster dashboard — aggregates LLM spend across crews, agents, and
 * missions. Reads from `/api/v1/paymaster/*`; degrades to "not yet
 * configured" when the backend isn't shipping Paymaster data.
 *
 * Layout pattern: "Sidebar + main" (filter rail + content). See
 * `docs/design/patterns.md` #2.
 */
export function SpendView() {
  const searchParams = useSearchParams()
  const [range, setRange] = useState<PaymasterRange>("7d")
  const [selectedCrewId, setSelectedCrewId] = useState<string | null>(
    searchParams.get("crew") ?? null,
  )
  // Honour ?crew=<id> deep-link from the Crews Cost stat card. Mirror the
  // query param directly so removing ?crew= clears the selection and a
  // manual Clear inside the UI isn't clobbered by a stale dependency.
  useEffect(() => {
    setSelectedCrewId(searchParams.get("crew") ?? null)
  }, [searchParams])
  // reloadKey bumps on Retry to force the hooks to refetch without
  // flipping range to a different value and back. setRange(range) is a
  // no-op because React bails on identical string state.
  const [reloadKey, setReloadKey] = useState(0)

  const crewSpend = useCrewSpend(range, true, reloadKey)
  const agentSpend = useAgentSpend(selectedCrewId, range, reloadKey)
  const topSpenders = useTopSpenders(range, 10, reloadKey)
  const subscriptions = useSubscriptionUsage(range, reloadKey)

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
  const subscriptionRows = useMemo(
    () => subscriptions.data?.rows ?? [],
    [subscriptions.data],
  )

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
    <div className="flex flex-col h-full bg-background">
      {/* ---- Top strip (h-9, breadcrumb + actions) ---- */}
      <div className="shrink-0 z-20 flex items-center h-9 bg-card border-b border-border/60 px-3 gap-2">
        <DollarSign className="h-3.5 w-3.5 text-foreground/60 shrink-0" />
        <h1 className="text-body font-medium text-foreground/80 truncate">Paymaster</h1>
        <span className="text-[11px] text-muted-foreground hidden sm:inline">LLM spend across crews &amp; agents</span>
        <div className="flex-1" />
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => setReloadKey((k) => k + 1)}
          disabled={crewSpend.loading}
        >
          <RefreshCw className={cn("h-3 w-3 mr-1.5", crewSpend.loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {/* ---- Main 2-column layout (sidebar + main) ---- */}
      <div className="flex-1 min-h-0 grid grid-cols-1 lg:grid-cols-[240px_1fr] overflow-hidden">
        {/* ---- Left filter rail ---- */}
        <aside className="hidden lg:flex flex-col border-r border-border/60 bg-card min-h-0 overflow-y-auto">
          <div className="flex items-center justify-between px-3 py-1.5 border-b border-border/60 shrink-0">
            <span className="text-[10px] font-semibold text-muted-foreground uppercase tracking-wider">
              Filters
            </span>
            <Filter className="h-3 w-3 text-muted-foreground/60" />
          </div>

          <div className="p-3 space-y-4">
            <div>
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1.5">
                Time range
              </div>
              <TimeRangePicker value={range} onChange={setRange} className="w-full" />
            </div>

            <div>
              <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1.5">
                Scope
              </div>
              <div className="rounded-md border border-dashed border-border/60 bg-muted/20 p-2 text-[11px] text-muted-foreground">
                Crew &amp; agent filters coming soon.
              </div>
            </div>

            {selectedCrewId && (
              <div>
                <div className="text-[10px] uppercase tracking-wider text-muted-foreground/80 font-semibold mb-1.5">
                  Drilldown
                </div>
                <div className="flex items-center justify-between gap-2 rounded-md border border-border/60 bg-muted/20 px-2 py-1.5">
                  <span className="text-[11px] text-foreground/80 truncate">{selectedCrewName}</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="h-6 px-1.5 text-[11px] shrink-0"
                    onClick={() => setSelectedCrewId(null)}
                  >
                    Clear
                  </Button>
                </div>
              </div>
            )}
          </div>
        </aside>

        {/* ---- Main content ---- */}
        <main className="min-h-0 overflow-y-auto">
          <div className="p-4 md:p-5 space-y-4">
            {/* Mobile-only time range — on lg the sidebar owns it. */}
            <div className="lg:hidden">
              <TimeRangePicker value={range} onChange={setRange} />
            </div>

            {crewSpend.error && (
              <div className="rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-[12px] text-red-300 flex items-center justify-between gap-2">
                <span>Couldn&apos;t load spend ({crewSpend.error}).</span>
                <Button variant="outline" size="sm" className="h-6 px-2 text-[11px]" onClick={() => setReloadKey((k) => k + 1)}>
                  Retry
                </Button>
              </div>
            )}

            <div className="rounded-md border border-border/40 bg-muted/20 px-3 py-2 text-[11px] text-muted-foreground/90 leading-relaxed">
              <span className="font-medium text-foreground/80">$ figures
              below cover metered API-key calls only.</span>
              {" "}
              Subscription credentials (Anthropic Max, Cursor Pro, Codex via
              ChatGPT, Google AI Pro/Ultra, Copilot Pro+, Factory Droid) are
              flat-rate — no marginal token cost — and are surfaced
              separately below.
            </div>

            <section className="grid grid-cols-2 lg:grid-cols-4 gap-3">
              <PaymasterKpiCard
                label="Metered spend"
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
                    Top spenders (metered)
                  </CardTitle>
                </CardHeader>
                <CardContent className="px-3">
                  <TopSpendersTable rows={topRows} loading={topSpenders.loading && topRows.length === 0} />
                </CardContent>
              </Card>
            </section>

            <section>
              <SubscriptionsPanel
                rows={subscriptionRows}
                loading={subscriptions.loading && subscriptionRows.length === 0}
                error={subscriptions.error}
                notConfigured={subscriptions.notConfigured}
              />
            </section>
          </div>
        </main>
      </div>
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
