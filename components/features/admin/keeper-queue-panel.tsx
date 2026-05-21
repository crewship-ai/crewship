"use client"

// PR-F2 — Keeper P2 queue panels (admin).
//
// Surfaces the four Phase-2 keeper review types (PRD §6 F4.1–4.4) as
// sub-tabs of a single admin section:
//
//   F4.1 Skill Review       — skills proposed for inclusion in a crew.
//   F4.2 Behavior           — sampled post-tool-call behavior reviews.
//   F4.3 Memory Health      — daily AGENT.md / CREW.md health sweeps.
//   F4.4 Negative Learning  — failure-driven lessons proposals.
//
// Backend reads from a single source of truth: the existing
//   GET /api/v1/admin/keeper/requests?limit=200
// endpoint returns the full keeper_requests join (agent_name,
// credential_name, decision, risk_score, ollama_prompt, ollama_raw_response).
// We fetch once and filter client-side by request_type rather than
// hitting the server 4× — keeps the panel snappy and avoids growing
// the API surface for the same data. If the table outgrows ~200 rows
// per type we'll add `?request_type=` server-side; not warranted yet.
//
// Row click → modal showing the full LLM prompt + raw response, which
// are already stored on the keeper_requests row by the gatekeeper.

import React, { useCallback, useEffect, useMemo, useState } from "react"
import { RefreshCw, Shield, Brain, Activity, Sparkles, BookOpen } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { StatusBadge } from "@/components/ui/status-badge"
import { SettingsCard } from "@/components/features/settings/shared"
import {
  Sheet, SheetContent, SheetHeader, SheetTitle,
} from "@/components/ui/sheet"
import { cn } from "@/lib/utils"
import { redactSecrets } from "@/app/(dashboard)/admin/utils"
import type { KeeperLogEntry } from "@/app/(dashboard)/admin/types"

// Sub-tab keys mirror the request_type enum values in
// internal/keeper/types.go so we can filter rows by string equality.
type P2Type = "skill_review" | "behavior" | "memory_health" | "negative_learning"

interface SubTab {
  key: P2Type
  label: string
  icon: React.ElementType
  prdRef: string
  emptyHint: string
}

const SUBTABS: SubTab[] = [
  {
    key: "skill_review",
    label: "Skill review",
    icon: Sparkles,
    prdRef: "F4.1",
    emptyHint: "No skill reviews pending. Skills queued for crew inclusion appear here.",
  },
  {
    key: "behavior",
    label: "Behavior",
    icon: Activity,
    prdRef: "F4.2",
    emptyHint: "No behavior reviews pending. Sampled post-tool-call checks appear here.",
  },
  {
    key: "memory_health",
    label: "Memory health",
    icon: Brain,
    prdRef: "F4.3",
    emptyHint: "No memory health reviews pending. Daily AGENT.md / CREW.md sweeps appear here.",
  },
  {
    key: "negative_learning",
    label: "Negative learning",
    icon: BookOpen,
    prdRef: "F4.4",
    emptyHint: "No negative-learning proposals pending. Failure-driven lessons appear here.",
  },
]

function decisionStatusKey(decision: string | null | undefined): string {
  switch (decision) {
    case "ALLOW":    return "COMPLETED"
    case "DENY":     return "FAILED"
    case "ESCALATE": return "BLOCKED"
    case "PENDING":  return "IN_PROGRESS"
    default:         return "PENDING"
  }
}

export interface KeeperQueuePanelProps {
  workspaceId: string | null | undefined
}

export const KeeperQueuePanel = React.memo(function KeeperQueuePanel({
  workspaceId,
}: KeeperQueuePanelProps) {
  const [active, setActive] = useState<P2Type>("skill_review")
  const [entries, setEntries] = useState<KeeperLogEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [selected, setSelected] = useState<KeeperLogEntry | null>(null)

  const fetchEntries = useCallback(async (signal?: AbortSignal) => {
    if (!workspaceId) {
      // Clear stale data from a previous workspace so the operator
      // never sees rows from workspace A after switching to B.
      setEntries([])
      setSelected(null)
      return
    }
    setLoading(true)
    setErr(null)
    try {
      // Pull a wide window so all four sub-tabs can filter from one
      // payload. Server caps at 200 (keeper_log.go).
      const r = await fetch(
        `/api/v1/admin/keeper/requests?workspace_id=${encodeURIComponent(workspaceId)}&limit=200`,
        { signal },
      )
      if (!r.ok) {
        setErr(`Failed to load keeper requests (${r.status})`)
        return
      }
      const data = (await r.json()) as KeeperLogEntry[]
      if (signal?.aborted) return
      setEntries(Array.isArray(data) ? data : [])
    } catch (e) {
      // Aborts are expected when workspaceId changes mid-flight.
      if (e instanceof DOMException && e.name === "AbortError") return
      setErr((e as Error).message)
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }, [workspaceId])

  useEffect(() => {
    // Abort in-flight request when workspaceId changes — without this,
    // a late response from workspace A could overwrite the state for
    // workspace B and the operator sees the wrong tenant's data.
    const controller = new AbortController()
    void fetchEntries(controller.signal)
    return () => controller.abort()
  }, [fetchEntries])

  const byType = useMemo(() => {
    const buckets: Record<P2Type, KeeperLogEntry[]> = {
      skill_review: [],
      behavior: [],
      memory_health: [],
      negative_learning: [],
    }
    for (const e of entries) {
      const t = e.request_type as P2Type
      if (buckets[t]) buckets[t].push(e)
    }
    return buckets
  }, [entries])

  const activeTab = SUBTABS.find((t) => t.key === active)!
  const rows = byType[active]

  return (
    <div className="space-y-4">
      {/* ── Header ── */}
      <div className="flex items-end justify-between gap-3">
        <div>
          <h3 className="text-body font-medium text-foreground/80 leading-none">
            Keeper Phase 2 reviews
          </h3>
          <p className="text-[11px] text-muted-foreground mt-1 leading-snug max-w-2xl">
            Pending reviews from the four Phase-2 evaluator paths
            (skills, behavior, memory health, negative learning). Click a
            row to see the full LLM prompt and raw response.
          </p>
        </div>
        <Button
          variant="outline"
          size="sm"
          className="h-7 px-2.5 text-xs"
          onClick={() => { void fetchEntries() }}
          disabled={loading || !workspaceId}
        >
          <RefreshCw className={cn("mr-1.5 h-3 w-3", loading && "animate-spin")} />
          Refresh
        </Button>
      </div>

      {/* ── Sub-tabs ──
          Keyboard nav per WAI-ARIA tabs pattern:
            ArrowLeft / ArrowRight cycle tabs
            Home jumps to first; End jumps to last
            Roving tabIndex — only the active tab is in the tab order;
              the others get -1 so Tab steps over the group, and arrow
              keys move within the group once focus is in it. */}
      <div
        role="tablist"
        aria-label="Keeper Phase 2 review types"
        className="flex gap-1 border-b border-border/60"
        onKeyDown={(e) => {
          if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(e.key)) return
          e.preventDefault()
          const i = SUBTABS.findIndex((t) => t.key === active)
          let next = i
          if (e.key === "ArrowLeft") next = (i - 1 + SUBTABS.length) % SUBTABS.length
          if (e.key === "ArrowRight") next = (i + 1) % SUBTABS.length
          if (e.key === "Home") next = 0
          if (e.key === "End") next = SUBTABS.length - 1
          const nextKey = SUBTABS[next].key
          setActive(nextKey)
          // Move DOM focus to the newly-activated tab so the visual +
          // a11y states stay in sync. Tabs are queried by data-tab-key
          // (added below) — using id would risk clashing with other
          // panels on the same page.
          requestAnimationFrame(() => {
            const target = e.currentTarget.querySelector<HTMLButtonElement>(
              `[data-tab-key="${nextKey}"]`,
            )
            target?.focus()
          })
        }}
      >
        {SUBTABS.map((tab) => {
          const Icon = tab.icon
          const count = byType[tab.key].length
          const isActive = tab.key === active
          return (
            <button
              key={tab.key}
              role="tab"
              aria-selected={isActive}
              aria-controls={`p2-panel-${tab.key}`}
              data-tab-key={tab.key}
              tabIndex={isActive ? 0 : -1}
              onClick={() => setActive(tab.key)}
              className={cn(
                "inline-flex items-center gap-1.5 px-3 py-2 text-xs border-b-2 -mb-px transition-colors",
                isActive
                  ? "border-emerald-500 text-emerald-300 font-medium"
                  : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              <Icon className="h-3 w-3" />
              {tab.label}
              <span className="text-[10px] text-muted-foreground/70 font-mono">{tab.prdRef}</span>
              {count > 0 && (
                <span className={cn(
                  "ml-1 inline-flex items-center justify-center h-4 min-w-4 px-1 rounded-full text-[10px] font-mono",
                  isActive ? "bg-emerald-500/20 text-emerald-200" : "bg-muted text-muted-foreground",
                )}>
                  {count}
                </span>
              )}
            </button>
          )
        })}
      </div>

      {/* ── Body ── */}
      <div id={`p2-panel-${active}`} role="tabpanel">
        {err && (
          <div className="rounded border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-300 mb-3">
            {err}
          </div>
        )}

        {loading ? (
          <Skeleton className="h-[240px] rounded-xl" />
        ) : (
          <SettingsCard
            title={activeTab.label}
            description={
              rows.length === 0
                ? `0 reviews · ${activeTab.prdRef}`
                : `${rows.length} review${rows.length === 1 ? "" : "s"} · ${activeTab.prdRef}`
            }
          >
            {rows.length === 0 ? (
              <div className="flex items-center justify-center py-10 text-center px-4">
                <div className="text-[11px] text-muted-foreground/60 max-w-sm">
                  {activeTab.emptyHint}
                </div>
              </div>
            ) : (
              <>
                {/* Desktop header */}
                <div className="hidden md:grid md:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_90px_60px_minmax(0,1.4fr)_120px] items-center gap-3 px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/60 border-b border-border/60">
                  <div>Agent</div>
                  <div>Crew</div>
                  <div>Decision</div>
                  <div className="text-right">Risk</div>
                  <div>Reason</div>
                  <div>Created</div>
                </div>
                {rows.map((entry, idx) => (
                  <button
                    key={entry.id}
                    type="button"
                    onClick={() => setSelected(entry)}
                    className={cn(
                      "flex flex-col gap-1 md:grid md:grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_90px_60px_minmax(0,1.4fr)_120px] md:items-center md:gap-3 w-full px-4 py-2.5 text-left hover:bg-white/[0.02] transition-colors",
                      idx < rows.length - 1 && "border-b border-border/40",
                    )}
                  >
                    <div className="text-xs font-medium truncate">{entry.agent_name}</div>
                    <div className="text-[11px] text-muted-foreground font-mono truncate">
                      {entry.crew_id || "—"}
                    </div>
                    <div>
                      <StatusBadge
                        status={decisionStatusKey(entry.decision)}
                        label={entry.decision ?? "PENDING"}
                        className="text-[10px]"
                      />
                    </div>
                    <div className="text-[11px] text-muted-foreground font-mono md:text-right tabular-nums">
                      {entry.risk_score != null ? `${entry.risk_score}/10` : "—"}
                    </div>
                    <div className="text-[11px] text-muted-foreground truncate italic">
                      {entry.reason || "—"}
                    </div>
                    <div className="text-[11px] text-muted-foreground font-mono truncate">
                      {new Date(entry.created_at).toLocaleString()}
                    </div>
                  </button>
                ))}
              </>
            )}
          </SettingsCard>
        )}
      </div>

      {/* ── Detail sheet ── */}
      <Sheet
        open={!!selected}
        onOpenChange={(open) => { if (!open) setSelected(null) }}
      >
        <SheetContent side="right" className="sm:max-w-2xl w-full overflow-y-auto">
          <SheetHeader>
            <SheetTitle className="flex items-center gap-2 text-sm">
              <Shield className="h-3.5 w-3.5" />
              {activeTab.label} · keeper decision
            </SheetTitle>
          </SheetHeader>
          {selected && (
            <div className="space-y-4 px-1 mt-4">
              <div className="grid grid-cols-2 gap-3">
                <DetailField label="Agent" value={selected.agent_name} />
                <DetailField label="Crew" value={selected.crew_id || "—"} mono />
                <div>
                  <FieldLabel>Decision</FieldLabel>
                  <StatusBadge
                    status={decisionStatusKey(selected.decision)}
                    label={selected.decision ?? "PENDING"}
                    className="mt-1 text-[10px]"
                  />
                </div>
                <DetailField
                  label="Risk score"
                  value={selected.risk_score != null ? `${selected.risk_score}/10` : "—"}
                />
                <DetailField label="Request type" value={selected.request_type} mono />
                <DetailField label="Created" value={new Date(selected.created_at).toLocaleString()} />
              </div>

              <DetailBlock label="Intent">
                <div className="text-[11px] bg-muted/40 border border-border/60 rounded-md p-2.5">
                  {redactSecrets(selected.intent)}
                </div>
              </DetailBlock>

              {selected.reason && (
                <DetailBlock label="Reason">
                  <div className="text-[11px] bg-muted/40 border border-border/60 rounded-md p-2.5">
                    {redactSecrets(selected.reason)}
                  </div>
                </DetailBlock>
              )}

              <DetailBlock label="LLM prompt">
                {selected.ollama_prompt ? (
                  <pre className="text-[10px] bg-muted/60 border border-border/60 rounded-md p-2.5 overflow-x-auto whitespace-pre-wrap font-mono max-h-[280px] overflow-y-auto">
                    {redactSecrets(selected.ollama_prompt)}
                  </pre>
                ) : (
                  <div className="text-[11px] text-muted-foreground italic bg-muted/40 border border-border/60 rounded-md p-2.5">
                    Not available
                  </div>
                )}
              </DetailBlock>

              <DetailBlock label="LLM raw response">
                {selected.ollama_raw_response ? (
                  <pre className="text-[10px] bg-muted/60 border border-border/60 rounded-md p-2.5 overflow-x-auto whitespace-pre-wrap font-mono max-h-[280px] overflow-y-auto">
                    {redactSecrets(selected.ollama_raw_response)}
                  </pre>
                ) : (
                  <div className="text-[11px] text-muted-foreground italic bg-muted/40 border border-border/60 rounded-md p-2.5">
                    Not available
                  </div>
                )}
              </DetailBlock>

              <div className="pt-3 border-t border-border/60">
                <div className="text-[10px] text-muted-foreground/60">
                  Request ID:{" "}
                  <span className="font-mono">{selected.id}</span>
                </div>
              </div>
            </div>
          )}
        </SheetContent>
      </Sheet>
    </div>
  )
})

function FieldLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="text-[10px] font-semibold text-muted-foreground/60 uppercase tracking-wider">
      {children}
    </div>
  )
}

function DetailField({ label, value, mono }: { label: string; value: React.ReactNode; mono?: boolean }) {
  return (
    <div>
      <FieldLabel>{label}</FieldLabel>
      <div className={cn("text-xs text-foreground/80 mt-1 truncate", mono && "font-mono")}>
        {value}
      </div>
    </div>
  )
}

function DetailBlock({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <FieldLabel>{label}</FieldLabel>
      <div className="mt-1">{children}</div>
    </div>
  )
}
