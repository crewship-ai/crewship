"use client"

import { useCallback, useEffect, useState } from "react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { apiFetch } from "@/lib/api-fetch"
import { toast } from "@/lib/toast"

import { CrCard, CrLine, CrNote } from "./cr-card"
import { formatSpend } from "./crew-spend"

interface CrewBudget {
  limit_usd: number | null
  mode?: string
  spent_usd: number
  state: "none" | "ok" | "warn" | "exceeded" | string
}

const usd = (n: number) => `$${n.toFixed(2)}`

/** What the limit has done so far, in words (never colour alone). */
function describe(b: CrewBudget): { text: string; tone: "muted" | "warn" | "bad" } | null {
  if (b.limit_usd == null || b.limit_usd <= 0) return null
  const pct = Math.round((b.spent_usd / b.limit_usd) * 100)
  if (b.state === "exceeded") {
    return { text: b.mode === "soft" ? `Over the limit · ${pct} % used` : `Over the limit · ${pct} % used · new runs stop`, tone: "bad" }
  }
  if (b.state === "warn") return { text: `Warning · ${pct} % used`, tone: "warn" }
  return { text: `${pct} % used`, tone: "muted" }
}

/**
 * Settings › Budget (v67 "Budget"): the crew's monthly spend limit, the
 * crew-scope row of budget_limits that admission already enforces
 * (GET/PUT/DELETE /api/v1/crews/{id}/budget, ADMIN and above). Rendered only
 * for people who may use it.
 */
export function CrewBudgetCard({ workspaceId, crewId }: { workspaceId: string; crewId: string }) {
  const url = `/api/v1/crews/${encodeURIComponent(crewId)}/budget?workspace_id=${encodeURIComponent(workspaceId)}`
  const [budget, setBudget] = useState<CrewBudget | null>(null)
  const [failed, setFailed] = useState(false)
  const [draft, setDraft] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setFailed(false)
    try {
      const res = await apiFetch(url)
      if (!res.ok) throw new Error(String(res.status))
      setBudget((await res.json()) as CrewBudget)
    } catch {
      setBudget(null)
      setFailed(true)
    }
  }, [url])
  useEffect(() => {
    void load()
  }, [load])

  const call = async (method: "PUT" | "DELETE", body?: unknown) => {
    setBusy(true)
    setError(null)
    try {
      const res = await apiFetch(url, {
        method,
        ...(body ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : {}),
      })
      if (!res.ok) throw new Error(String(res.status))
      const next = (await res.json()) as CrewBudget
      setBudget(next)
      setDraft("")
      toast.success(method === "PUT" ? "Monthly limit set" : "Monthly limit removed")
    } catch {
      setError(method === "PUT" ? "The limit could not be saved. Nothing was changed; try again." : "The limit could not be removed. Nothing was changed; try again.")
    } finally {
      setBusy(false)
    }
  }

  const set = () => {
    const n = Number(draft.trim())
    if (!draft.trim() || !Number.isFinite(n) || n <= 0) {
      setError("Enter a limit greater than 0, in US dollars.")
      return
    }
    void call("PUT", { limit_usd: n })
  }

  const status = budget ? describe(budget) : null
  return (
    <CrCard title="Budget" concept="activity">
      {failed ? (
        <CrNote tone="error" action={<Button size="sm" variant="outline" onClick={() => void load()}>Retry</Button>}>
          The budget could not be loaded.
        </CrNote>
      ) : budget === null ? (
        <CrNote>Loading…</CrNote>
      ) : (
        <>
          <CrLine
            title="Monthly limit"
            sub={
              <>
                Spend this month {formatSpend(budget.spent_usd)}. Soft warns at 80 %, hard stops new runs at 100 %.
              </>
            }
            right={<b className="text-body tabular-nums">{budget.limit_usd == null ? "No limit" : usd(budget.limit_usd)}</b>}
          />
          {status && (
            <p
              role="status"
              className={
                status.tone === "bad" ? "border-t border-border px-4 py-2 text-label text-chip-danger-fg"
                : status.tone === "warn" ? "border-t border-border px-4 py-2 text-label text-chip-warn-fg"
                : "border-t border-border px-4 py-2 text-label text-muted-foreground"
              }
            >
              {status.text}
            </p>
          )}
          <div className="flex flex-wrap items-center gap-2 border-t border-border px-4 py-2.5">
            <Input
              aria-label="Monthly limit in US dollars"
              inputMode="decimal"
              placeholder={budget.limit_usd == null ? "e.g. 80" : String(budget.limit_usd)}
              value={draft}
              disabled={busy}
              onChange={(e) => { setDraft(e.target.value); setError(null) }}
              onKeyDown={(e) => { if (e.key === "Enter") set() }}
              className="h-8 w-32"
            />
            <Button size="sm" variant="outline" disabled={busy} onClick={set}>Set limit</Button>
            {budget.limit_usd != null && (
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => void call("DELETE")}>Remove limit</Button>
            )}
          </div>
          {error && <p role="alert" className="px-4 pb-3 text-label text-chip-danger-fg">{error}</p>}
        </>
      )}
    </CrCard>
  )
}
