"use client"

import { Wallet, AlertTriangle } from "lucide-react"
import { Card } from "./_shared"
import { cn } from "@/lib/utils"
import { useBudgetSummary } from "@/hooks/use-routine-budget"

// RoutineBudgetSummaryCard — workspace-wide roll-up of every routine
// with a monthly budget set or spend this month (#1422 item 3). Lives
// on the routines Insights tab, next to the other workspace-health
// panels (Top routines, Recent failures).
export function RoutineBudgetSummaryCard({
  workspaceId,
  onSelect,
}: {
  workspaceId: string
  onSelect: (slug: string) => void
}) {
  const { summary, loading } = useBudgetSummary(workspaceId)

  const rows = summary?.routines ?? []
  const overCount = rows.filter((r) => r.over_budget).length

  return (
    <Card
      title="Budgets"
      icon={Wallet}
      subtitle={summary ? `${rows.length} routine${rows.length === 1 ? "" : "s"} · ${summary.month}` : undefined}
      tone={overCount > 0 ? "amber" : "default"}
    >
      {loading && rows.length === 0 ? (
        <div className="px-4 py-6 text-center text-[13px] text-muted-foreground">Loading…</div>
      ) : rows.length === 0 ? (
        <div className="px-4 py-6 text-center text-[13px] text-muted-foreground">
          No routine has a monthly budget set or spend yet. Set one from a routine&apos;s Overview tab.
        </div>
      ) : (
        <>
          <ul className="divide-y divide-border/40">
            {rows.map((r) => (
              <li key={r.slug}>
                <button
                  type="button"
                  aria-label={`Open routine ${r.slug}`}
                  onClick={() => onSelect(r.slug)}
                  className="flex w-full items-center gap-2.5 px-4 py-2.5 text-left transition-colors hover:bg-white/[0.025] focus:bg-white/[0.025] focus:outline-none focus-visible:ring-1 focus-visible:ring-primary"
                >
                  {r.over_budget && <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-red-400" />}
                  <span className="flex-1 truncate text-sm">{r.slug}</span>
                  <span className={cn("font-mono text-[12px] tabular-nums", r.over_budget ? "text-red-400 font-medium" : "text-foreground/80")}>
                    ${r.spent_usd.toFixed(2)}
                    {r.monthly_budget_usd > 0 && <span className="text-muted-foreground"> / ${r.monthly_budget_usd.toFixed(2)}</span>}
                  </span>
                  {r.monthly_budget_usd > 0 && (
                    <span className={cn("w-12 shrink-0 text-right text-[11px] tabular-nums", r.over_budget ? "text-red-400 font-medium" : "text-muted-foreground")}>
                      {(r.pct_used ?? 0).toFixed(0)}%
                    </span>
                  )}
                </button>
              </li>
            ))}
          </ul>
          {summary && (
            <div className="border-t border-border/40 px-4 py-2.5 text-[11px] text-muted-foreground">
              Total: <span className="font-mono text-foreground/80">${summary.total_spent_usd.toFixed(2)}</span> spent
              {summary.total_budget_usd > 0 && (
                <> of <span className="font-mono text-foreground/80">${summary.total_budget_usd.toFixed(2)}</span> budgeted</>
              )}
            </div>
          )}
        </>
      )}
    </Card>
  )
}
