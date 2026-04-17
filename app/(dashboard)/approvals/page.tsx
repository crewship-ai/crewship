"use client"

import { useMemo, useState } from "react"
import { Gavel, RefreshCw, ShieldCheck } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { ApprovalCard } from "@/components/features/approvals/approval-card"
import { ApprovalDetail } from "@/components/features/approvals/approval-detail"
import { useApprovals } from "@/hooks/use-approvals"
import type { ApprovalRow, ApprovalStatus } from "@/lib/types/approvals"

type FilterKey = "pending" | "decided" | "all"

const FILTERS: { key: FilterKey; label: string; apiStatus: ApprovalStatus }[] = [
  { key: "pending", label: "Pending", apiStatus: "pending" },
  { key: "decided", label: "Decided", apiStatus: "all" }, // client-side filter below
  { key: "all", label: "All", apiStatus: "all" },
]

/**
 * Harbor Master HITL inbox. Left rail = status filter; main = list of
 * approvals; clicking a card opens the right-side detail sheet.
 */
export default function ApprovalsPage() {
  const [filter, setFilter] = useState<FilterKey>("pending")
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)

  const apiStatus = FILTERS.find((f) => f.key === filter)?.apiStatus ?? "pending"
  const { rows, loading, error, notConfigured, refresh, patchRow } = useApprovals({
    status: apiStatus,
    pollMs: 15000,
  })

  // "Decided" is pending=no — we filter client-side because the backend
  // enumerates each decided status separately.
  const visibleRows = useMemo(() => {
    if (filter === "decided") return rows.filter((r) => r.status !== "pending")
    return rows
  }, [rows, filter])

  const selectedRow = useMemo(
    () => visibleRows.find((r) => r.id === selectedId) ?? null,
    [visibleRows, selectedId],
  )

  const pendingCount = rows.filter((r) => r.status === "pending").length

  function openRow(row: ApprovalRow) {
    setSelectedId(row.id)
    setSheetOpen(true)
  }

  function handleDecided(id: string, status: "approved" | "denied", comment: string) {
    patchRow(id, {
      status,
      comment,
      decided_at: new Date().toISOString(),
    })
  }

  if (notConfigured) {
    return (
      <div className="flex flex-col items-center gap-2 py-24 text-center">
        <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
          <ShieldCheck className="h-4 w-4 text-muted-foreground/60" />
        </div>
        <div className="text-sm font-medium text-foreground/80">Approvals not yet configured</div>
        <div className="text-[11px] text-muted-foreground max-w-sm">
          The approvals API isn&apos;t available on this backend.
        </div>
      </div>
    )
  }

  return (
    <div className="flex flex-col lg:flex-row gap-6 p-4 md:p-6 min-h-[calc(100vh-48px)]">
      <aside className="w-full lg:w-56 shrink-0 space-y-3">
        <div className="flex items-center gap-2">
          <Gavel className="h-4 w-4 text-foreground/60" />
          <h1 className="text-body font-medium text-foreground/80">Approvals</h1>
        </div>

        <nav className="flex lg:flex-col gap-1">
          {FILTERS.map((f) => {
            const active = filter === f.key
            const count = f.key === "pending" ? pendingCount : null
            return (
              <button
                key={f.key}
                type="button"
                onClick={() => setFilter(f.key)}
                className={cn(
                  "flex items-center justify-between w-full px-2.5 py-1.5 rounded text-xs transition-colors",
                  active
                    ? "bg-primary/15 text-primary"
                    : "text-muted-foreground hover:bg-muted/50 hover:text-foreground",
                )}
              >
                <span>{f.label}</span>
                {count !== null && count > 0 && (
                  <Badge variant="outline" className="text-[10px] bg-amber-500/15 text-amber-300 border-amber-500/40">
                    {count}
                  </Badge>
                )}
              </button>
            )
          })}
        </nav>
      </aside>

      <div className="flex-1 min-w-0 space-y-3">
        <div className="flex items-center justify-between">
          <div className="text-[11px] font-mono text-muted-foreground uppercase tracking-wider">
            {visibleRows.length} {filter} {visibleRows.length === 1 ? "approval" : "approvals"}
          </div>
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => refresh()}
            disabled={loading}
          >
            <RefreshCw className={cn("h-3 w-3 mr-1.5", loading && "animate-spin")} />
            Refresh
          </Button>
        </div>

        {error && (
          <div className="rounded-lg border border-red-500/40 bg-red-500/5 px-3 py-2 text-[12px] text-red-300 flex items-center justify-between gap-2">
            <span>Couldn&apos;t load approvals ({error}).</span>
            <Button variant="outline" size="sm" className="h-6 px-2 text-[11px]" onClick={() => refresh()}>
              Retry
            </Button>
          </div>
        )}

        {!loading && visibleRows.length === 0 && !error && (
          <div className="flex flex-col items-center gap-2 py-16 text-center">
            <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
              <ShieldCheck className="h-4 w-4 text-muted-foreground/60" />
            </div>
            <div className="text-sm font-medium text-foreground/80">Inbox clear</div>
            <div className="text-[11px] text-muted-foreground max-w-sm">
              {filter === "pending"
                ? "No pending approvals. Agents haven't requested anything yet."
                : "Nothing here yet."}
            </div>
          </div>
        )}

        <div className="space-y-2">
          {visibleRows.map((row) => (
            <ApprovalCard
              key={row.id}
              row={row}
              active={row.id === selectedId && sheetOpen}
              onSelect={() => openRow(row)}
            />
          ))}
        </div>
      </div>

      <ApprovalDetail
        row={selectedRow}
        open={sheetOpen}
        onOpenChange={setSheetOpen}
        onDecided={handleDecided}
      />
    </div>
  )
}
