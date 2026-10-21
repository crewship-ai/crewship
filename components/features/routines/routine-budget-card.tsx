"use client"

import { useState } from "react"
import { Wallet, Pencil } from "lucide-react"
import { toast } from "@/lib/toast"
import { Card } from "./_shared"
import { Progress } from "@/components/ui/progress"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { cn } from "@/lib/utils"
import { useRoutineBudget } from "@/hooks/use-routine-budget"
import { COST_HIDDEN_NOTE, isCostShown } from "@/lib/cost-visibility"

// RoutineBudgetCard — spent-this-month-vs-cap meter for the routine
// detail page (#1422 item 3). Distinct from the routine's DSL
// max_cost_usd (a per-run hard gate, shown in the DSL/Editor tab, not
// here): this is a monthly operator-set cap compared against actual
// spend, editable inline.
//
// Renders three states:
//   - loading: nothing (avoids a layout flash before the first fetch)
//   - no budget set: a quiet nudge with just this month's spend
//   - budget set: a progress bar (amber card + red fill past 90%) plus
//     the exact $spent / $cap figures
export function RoutineBudgetCard({
  workspaceId,
  slug,
}: {
  workspaceId?: string
  slug: string
}) {
  const { budget, loading, setBudget } = useRoutineBudget(workspaceId, slug)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState("")
  const [saving, setSaving] = useState(false)

  if (loading && !budget) return null

  // Below ADMIN the server sends the state without the amounts (#3590):
  // say over / within / none, never "$0.00", and offer no editor (setting
  // a cap is OWNER/ADMIN on the server too).
  if (budget && !isCostShown(budget.spent_usd)) {
    const state = !budget.has_budget ? "No budget set" : budget.over_budget ? "Over budget this month" : "Within budget this month"
    return (
      <Card title="Budget" subtitle={budget.month} icon={Wallet} tone={budget.over_budget ? "warn" : "default"}>
        <div className="px-3 py-3 text-xs text-muted-foreground" data-testid="routine-budget-state">
          <span className={cn(budget.over_budget && "font-medium text-destructive")}>{state}</span>
          <span className="block">{COST_HIDDEN_NOTE}</span>
        </div>
      </Card>
    )
  }

  const startEdit = () => {
    setDraft(budget?.has_budget && isCostShown(budget.monthly_budget_usd) ? String(budget.monthly_budget_usd) : "")
    setEditing(true)
  }

  const save = async () => {
    const amount = Number(draft)
    if (!Number.isFinite(amount) || amount < 0) {
      toast.error("Enter a non-negative number")
      return
    }
    setSaving(true)
    try {
      await setBudget(amount)
      setEditing(false)
      toast.success(amount === 0 ? "Budget cleared" : `Budget set to $${amount.toFixed(2)}/month`)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to save budget")
    } finally {
      setSaving(false)
    }
  }

  const overBudget = !!budget?.over_budget
  const pctUsed = budget?.pct_used ?? 0
  const spent = budget?.spent_usd ?? 0

  return (
    <Card
      title="Budget"
      subtitle={budget?.month}
      icon={Wallet}
      tone={overBudget ? "warn" : "default"}
      action={
        !editing && (
          <button
            type="button"
            onClick={startEdit}
            className="text-muted-foreground hover:text-foreground"
            aria-label={budget?.has_budget ? "Edit budget" : "Set budget"}
          >
            <Pencil className="h-3.5 w-3.5" />
          </button>
        )
      }
    >
      {editing ? (
        <div className="flex items-center gap-2 px-3 py-3">
          <span className="text-xs text-muted-foreground">$</span>
          <Input
            type="number"
            min={0}
            step="0.01"
            autoFocus
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder="0 = no cap"
            className="h-7 text-xs"
          />
          <span className="text-xs text-muted-foreground">/mo</span>
          <Button size="sm" className="h-7 px-2 text-xs" disabled={saving} onClick={save}>
            Save
          </Button>
          <Button
            size="sm"
            variant="ghost"
            className="h-7 px-2 text-xs"
            disabled={saving}
            onClick={() => setEditing(false)}
          >
            Cancel
          </Button>
        </div>
      ) : !budget?.has_budget ? (
        <div className="px-3 py-3 text-xs text-muted-foreground">
          No budget set — ${spent.toFixed(2)} spent this month.
          <button
            type="button"
            onClick={startEdit}
            className="ml-1 text-primary hover:underline"
          >
            Set one
          </button>
        </div>
      ) : (
        <div className="px-3 py-3">
          <Progress
            value={Math.min(100, pctUsed)}
            className="h-[6px]"
            indicatorClassName={cn(
              "transition-all",
              overBudget ? "bg-destructive" : pctUsed >= 90 ? "bg-warn" : "bg-primary",
            )}
          />
          <div className="mt-1.5 flex items-center justify-between font-mono text-[11px] text-muted-foreground">
            <span className={cn(overBudget && "text-destructive font-medium")}>
              ${spent.toFixed(2)} of ${(budget.monthly_budget_usd ?? 0).toFixed(2)}
            </span>
            <span className={cn(overBudget && "text-destructive font-medium")}>
              {pctUsed.toFixed(0)}%{overBudget ? " over" : ""}
            </span>
          </div>
        </div>
      )}
    </Card>
  )
}
