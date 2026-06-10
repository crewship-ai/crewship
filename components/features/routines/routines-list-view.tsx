"use client"

import { useMemo, useState } from "react"
import {
  ScrollText,
  RefreshCw,
  ArrowUpDown,
  ArrowUp,
  ArrowDown,
  AlertTriangle,
  Activity,
  Zap,
  CheckCircle2,
  XCircle,
} from "lucide-react"
import type { Pipeline } from "@/hooks/use-pipelines"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { Card, Pill } from "./_shared"

// RoutinesListView — catalog dashboard for the routine list tab.
// Designed so the middle pane adds value the left sidebar can't:
// workspace-wide KPI tiles + a "needs attention" callout for failed
// routines + a richer table with sortable columns. Sidebar stays as
// a navigator (quick jump); this view is the operational overview.

interface Props {
  routines: Pipeline[]
  loading: boolean
  error: string | null
  selectedSlug: string | null
  onSelect: (slug: string) => void
  onRefresh: () => void
}

type SortKey = "name" | "invocation_count" | "last_invoked_at" | "updated_at" | "status"
type SortDir = "asc" | "desc"

export function RoutinesListView({ routines, loading, error, selectedSlug, onSelect, onRefresh }: Props) {
  const [sortKey, setSortKey] = useState<SortKey>("invocation_count")
  const [sortDir, setSortDir] = useState<SortDir>("desc")

  const sorted = useMemo(
    () => [...routines].sort((a, b) => (sortDir === "asc" ? 1 : -1) * compareBy(a, b, sortKey)),
    [routines, sortKey, sortDir],
  )

  const stats = useMemo(() => {
    const total = routines.length
    const active = routines.filter((r) => (r.invocation_count ?? 0) > 0).length
    const totalRuns = routines.reduce((sum, r) => sum + (r.invocation_count ?? 0), 0)
    const failed = routines.filter((r) => {
      const s = r.last_invocation_status?.toLowerCase()
      return s === "failed" || s === "error"
    })
    const succeeded = routines.filter((r) => {
      const s = r.last_invocation_status?.toLowerCase()
      return s === "completed" || s === "succeeded" || s === "success"
    }).length
    const passRate = succeeded + failed.length > 0 ? Math.round((succeeded / (succeeded + failed.length)) * 100) : null
    return { total, active, totalRuns, failed, passRate, succeeded }
  }, [routines])

  const toggleSort = (key: SortKey) => {
    if (sortKey === key) setSortDir((d) => (d === "asc" ? "desc" : "asc"))
    else {
      setSortKey(key)
      setSortDir(key === "name" ? "asc" : "desc")
    }
  }

  return (
    <div className="h-full overflow-auto">
      <div className="space-y-4 p-6">
        {/* ── Header strip ───────────────────────────────────────── */}
        <div className="flex items-center justify-between">
          <div>
            <h2 className="text-base font-semibold tracking-tight">Catalog</h2>
            <p className="text-[12px] text-muted-foreground">
              {loading ? (
                "Loading…"
              ) : error ? (
                <span className="text-rose-400">Error: {error}</span>
              ) : (
                <>
                  <span className="text-foreground/85 tabular-nums">{stats.total}</span> routines in this workspace
                </>
              )}
            </p>
          </div>
          <Button size="sm" variant="ghost" onClick={onRefresh} className="h-8 gap-1.5 text-xs">
            <RefreshCw className={cn("h-3.5 w-3.5", loading && "animate-spin")} />
            Refresh
          </Button>
        </div>

        {/* ── KPI strip ──────────────────────────────────────────── */}
        <section className="grid grid-cols-2 gap-3 md:grid-cols-4">
          <KpiTile
            label="Total routines"
            value={stats.total.toString()}
            sub={`${stats.active} active · ${stats.total - stats.active} idle`}
            tone="blue"
          />
          <KpiTile
            label="Total invocations"
            value={stats.totalRuns.toLocaleString()}
            sub="across the workspace"
            tone="violet"
          />
          <KpiTile
            label="Pass rate"
            value={stats.passRate !== null ? `${stats.passRate}%` : "—"}
            sub={
              stats.passRate !== null
                ? `${stats.succeeded} ok · ${stats.failed.length} failed`
                : "no completed runs yet"
            }
            tone={stats.passRate !== null && stats.passRate >= 90 ? "emerald" : stats.passRate !== null && stats.passRate < 70 ? "rose" : "default"}
          />
          <KpiTile
            label="Needs attention"
            value={stats.failed.length.toString()}
            sub={stats.failed.length === 0 ? "all clean" : "routines with failed last run"}
            tone={stats.failed.length > 0 ? "amber" : "default"}
          />
        </section>

        {/* ── Needs attention callout ────────────────────────────── */}
        {stats.failed.length > 0 && (
          <Card tone="amber" title="Needs attention" icon={AlertTriangle} subtitle={`${stats.failed.length} failed`}>
            <ul className="divide-y divide-border/40">
              {stats.failed.slice(0, 5).map((r) => (
                <li key={r.id}>
                  <button
                    onClick={() => onSelect(r.slug)}
                    className="grid w-full grid-cols-[18px_1fr_auto] items-center gap-3 px-4 py-2.5 text-left transition-colors hover:bg-white/[0.025]"
                  >
                    <XCircle className="h-4 w-4 text-rose-400" />
                    <div className="min-w-0">
                      <div className="truncate text-sm font-medium">{r.name || r.slug}</div>
                      <div className="font-mono text-[11px] text-muted-foreground">{r.slug}</div>
                    </div>
                    <span className="font-mono text-[11px] text-muted-foreground">
                      {r.last_invoked_at ? formatRelative(r.last_invoked_at) : "—"}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </Card>
        )}

        {/* ── Catalog table ──────────────────────────────────────── */}
        {!loading && !error && routines.length === 0 ? (
          <EmptyState />
        ) : error && routines.length === 0 ? (
          <ErrorState message={error} onRetry={onRefresh} />
        ) : (
          <Card title="All routines" subtitle={`${routines.length} total`}>
            <div className="overflow-x-auto">
              <table className="w-full text-[13px]">
                <thead className="border-b border-border/40 bg-card/40 text-[10px] uppercase tracking-wider text-muted-foreground">
                  <tr className="text-left">
                    <th className="px-4 py-2.5 font-semibold">
                      <SortBtn label="Routine" col="name" sortKey={sortKey} sortDir={sortDir} onClick={toggleSort} />
                    </th>
                    <th className="px-3 py-2.5 font-semibold">
                      <SortBtn label="Last status" col="status" sortKey={sortKey} sortDir={sortDir} onClick={toggleSort} />
                    </th>
                    <th className="px-3 py-2.5 text-right font-semibold">
                      <SortBtn
                        label="Runs"
                        col="invocation_count"
                        sortKey={sortKey}
                        sortDir={sortDir}
                        onClick={toggleSort}
                        align="right"
                      />
                    </th>
                    <th className="px-3 py-2.5 font-semibold">
                      <SortBtn label="Last run" col="last_invoked_at" sortKey={sortKey} sortDir={sortDir} onClick={toggleSort} />
                    </th>
                    <th className="px-3 py-2.5 font-semibold">Author</th>
                    <th className="px-3 py-2.5 font-semibold">
                      <SortBtn label="Updated" col="updated_at" sortKey={sortKey} sortDir={sortDir} onClick={toggleSort} />
                    </th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border/40">
                  {sorted.map((r) => (
                    <RoutineRow
                      key={r.id}
                      routine={r}
                      selected={selectedSlug === r.slug}
                      onClick={() => onSelect(r.slug)}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          </Card>
        )}
      </div>
    </div>
  )
}

/* ----------------------------------------------------------------- *
 *  KPI tile (workspace-level — different value than per-routine).   *
 * ----------------------------------------------------------------- */

const KPI_TONE: Record<string, { bg: string; text: string; iconBg: string; Icon: typeof Activity }> = {
  default: { bg: "bg-muted", text: "text-muted-foreground", iconBg: "bg-muted text-muted-foreground", Icon: Activity },
  blue: { bg: "bg-blue-500/20", text: "text-blue-400", iconBg: "bg-blue-500/20 text-blue-400", Icon: Zap },
  violet: { bg: "bg-violet-500/20", text: "text-violet-400", iconBg: "bg-violet-500/20 text-violet-400", Icon: Activity },
  emerald: { bg: "bg-emerald-500/20", text: "text-emerald-400", iconBg: "bg-emerald-500/20 text-emerald-400", Icon: CheckCircle2 },
  amber: { bg: "bg-amber-500/20", text: "text-amber-400", iconBg: "bg-amber-500/20 text-amber-400", Icon: AlertTriangle },
  rose: { bg: "bg-rose-500/20", text: "text-rose-400", iconBg: "bg-rose-500/20 text-rose-400", Icon: XCircle },
}

function KpiTile({
  label,
  value,
  sub,
  tone = "default",
}: {
  label: string
  value: string
  sub?: string
  tone?: keyof typeof KPI_TONE
}) {
  const t = KPI_TONE[tone] ?? KPI_TONE.default
  const Icon = t.Icon
  return (
    <div className="flex flex-col gap-1 rounded-xl border border-border/60 bg-card px-4 py-4">
      <div className="flex items-center justify-between">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{label}</div>
        <div className={cn("flex h-6 w-6 items-center justify-center rounded-md", t.iconBg)}>
          <Icon className="h-3.5 w-3.5" />
        </div>
      </div>
      <div className="mt-1 text-[28px] font-semibold leading-none tabular-nums sm:text-[32px]">{value}</div>
      {sub && <div className="mt-1 text-[11px] text-muted-foreground">{sub}</div>}
    </div>
  )
}

function SortBtn({
  label,
  col,
  sortKey,
  sortDir,
  onClick,
  align = "left",
}: {
  label: string
  col: SortKey
  sortKey: SortKey
  sortDir: SortDir
  onClick: (col: SortKey) => void
  align?: "left" | "right"
}) {
  const active = sortKey === col
  const Icon = !active ? ArrowUpDown : sortDir === "asc" ? ArrowUp : ArrowDown
  return (
    <button
      onClick={() => onClick(col)}
      className={cn(
        "inline-flex items-center gap-1 transition-colors hover:text-foreground",
        active ? "text-foreground" : "",
        align === "right" && "ml-auto justify-end",
      )}
    >
      <span>{label}</span>
      <Icon className="h-3 w-3" />
    </button>
  )
}

function RoutineRow({
  routine,
  selected,
  onClick,
}: {
  routine: Pipeline
  selected: boolean
  onClick: () => void
}) {
  const status = routine.last_invocation_status?.toLowerCase()
  const statusPill = !status ? (
    <span className="text-[11px] text-muted-foreground">never invoked</span>
  ) : (
    <Pill
      tone={
        status === "completed" || status === "succeeded" || status === "success"
          ? "emerald"
          : status === "failed" || status === "error"
            ? "rose"
            : status === "running"
              ? "blue"
              : "default"
      }
      className="capitalize"
    >
      {status}
    </Pill>
  )

  return (
    <tr
      onClick={onClick}
      tabIndex={0}
      role="button"
      aria-label={`Open routine ${routine.name || routine.slug}`}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          onClick()
        }
      }}
      className={cn(
        "cursor-pointer row-hover transition-colors",
        "focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-1 focus:ring-offset-background",
        selected && "row-selected",
      )}
    >
      <td className="px-4 py-3">
        <div className="flex items-center gap-2">
          <ScrollText className="h-4 w-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="truncate text-sm font-medium">{routine.name || routine.slug}</span>
              {routine.ephemeral && (
                <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
                  ephemeral
                </Badge>
              )}
              <LinkedIssuesChip count={routine.linked_issue_count ?? 0} identifiers={routine.linked_issues ?? []} />
            </div>
            <div className="font-mono text-[11px] text-muted-foreground/80">{routine.slug}</div>
          </div>
        </div>
      </td>
      <td className="px-3 py-3">{statusPill}</td>
      <td className="px-3 py-3 text-right font-mono tabular-nums text-foreground/85">{routine.invocation_count}</td>
      <td className="px-3 py-3 text-[12px] text-muted-foreground">
        {routine.last_invoked_at ? formatRelative(routine.last_invoked_at) : "—"}
      </td>
      <td className="px-3 py-3">
        <div className="min-w-0">
          <div className="truncate text-[12px] text-foreground/85">
            {routine.author_agent_name || (routine.author_crew_id ? truncate(routine.author_crew_id, 16) : "—")}
          </div>
          <div className="text-[10px] capitalize text-muted-foreground">
            {routine.authored_via.replace(/_/g, " ")}
          </div>
        </div>
      </td>
      <td className="px-3 py-3 text-[12px] text-muted-foreground">{formatRelative(routine.updated_at)}</td>
    </tr>
  )
}

function LinkedIssuesChip({ count, identifiers }: { count: number; identifiers: string[] }) {
  if (count === 0) return null
  const head = identifiers.slice(0, 2)
  const extra = count - head.length
  const label = extra > 0 ? `${head.join(", ")} +${extra}` : head.join(", ")
  return (
    <Badge
      variant="outline"
      className="border-blue-500/30 bg-blue-500/10 px-1.5 py-0 text-[10px] font-medium text-blue-400"
      title={`Linked to ${count} issue${count === 1 ? "" : "s"}: ${identifiers.join(", ")}${extra > 0 ? "…" : ""}`}
    >
      {label || `${count} issue${count === 1 ? "" : "s"}`}
    </Badge>
  )
}

function ErrorState({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="flex flex-col items-center justify-center rounded-xl border border-border/60 bg-card p-12">
      <ScrollText className="mb-4 h-12 w-12 text-rose-400/40" />
      <h3 className="mb-2 text-sm font-medium text-rose-400">Failed to load routines</h3>
      <p className="mb-4 max-w-xl break-all text-center text-[12px] text-muted-foreground">{message}</p>
      <Button size="sm" variant="outline" onClick={onRetry} className="gap-1.5">
        <RefreshCw className="h-3.5 w-3.5" />
        Retry
      </Button>
    </div>
  )
}

function EmptyState() {
  const quickstarts: Array<{ icon: string; title: string; description: string; cta: string }> = [
    {
      icon: "+",
      title: "Create from a template",
      description:
        "Open the New routine dialog and pick a starter (Empty / Summarize / Two-step). Edit the JSON DSL, then Test & Save.",
      cta: "Click + New routine in the toolbar above",
    },
    {
      icon: "⬇",
      title: "Import from another workspace",
      description:
        "Bundle JSON exports from another Crewship workspace drop in via Import. Slugs are preserved; conflicts update the existing routine.",
      cta: "Click Import in the toolbar above",
    },
    {
      icon: "🤖",
      title: "Let an agent author one",
      description:
        "Agents that spot a repetitive pattern call POST localhost:9119/pipelines/save from inside their container.",
      cta: "Open any chat with an authoring-tier agent",
    },
  ]
  return (
    <Card>
      <div className="flex flex-col items-center justify-center p-12">
        <div className="mb-6 max-w-xl text-center">
          <div className="mb-4 inline-flex h-14 w-14 items-center justify-center rounded-2xl bg-muted">
            <ScrollText className="h-7 w-7 text-muted-foreground" />
          </div>
          <h3 className="mb-2 text-base font-semibold">No routines in this workspace yet</h3>
          <p className="text-[13px] leading-relaxed text-muted-foreground">
            Routines are repeatable AI workflow recipes — workspace-scoped, declarative, AI-authored or hand-written.
            Three ways to get the first one in:
          </p>
        </div>
        <div className="grid w-full max-w-3xl grid-cols-1 gap-3 md:grid-cols-3">
          {quickstarts.map((q) => (
            <div key={q.title} className="rounded-xl border border-border/60 bg-card/60 p-4">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-lg bg-blue-500/20 text-base text-blue-400">
                {q.icon}
              </div>
              <h4 className="mb-1 text-sm font-medium">{q.title}</h4>
              <p className="mb-3 text-[12px] leading-relaxed text-muted-foreground">{q.description}</p>
              <p className="text-[10px] uppercase tracking-wider text-muted-foreground">{q.cta}</p>
            </div>
          ))}
        </div>
      </div>
    </Card>
  )
}

function compareBy(a: Pipeline, b: Pipeline, key: SortKey): number {
  switch (key) {
    case "name":
      return (a.name || a.slug).localeCompare(b.name || b.slug)
    case "invocation_count":
      return a.invocation_count - b.invocation_count
    case "last_invoked_at":
      return (a.last_invoked_at ?? "").localeCompare(b.last_invoked_at ?? "")
    case "updated_at":
      return a.updated_at.localeCompare(b.updated_at)
    case "status":
      return (a.last_invocation_status ?? "").localeCompare(b.last_invocation_status ?? "")
  }
}

function formatRelative(iso: string): string {
  const then = new Date(iso).getTime()
  if (!Number.isFinite(then)) return "—"
  const diffMs = Date.now() - then
  const sec = Math.floor(diffMs / 1000)
  if (sec < 60) return `${sec}s ago`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.floor(min / 60)
  if (hr < 24) return `${hr}h ago`
  const day = Math.floor(hr / 24)
  if (day < 30) return `${day}d ago`
  return new Date(iso).toLocaleDateString()
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + "…"
}
