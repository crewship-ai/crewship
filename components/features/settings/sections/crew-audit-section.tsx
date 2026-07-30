"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { Shield, ChevronRight, ChevronLeft, Search, RefreshCw, Download } from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import { Skeleton } from "@/components/ui/skeleton"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { personLabel } from "@/components/ui/user-avatar"
import { SettingsCard, SettingsEmpty } from "../shared"

interface AuditLog {
  id: string
  action: string
  entity_type: string
  entity_id: string | null
  /** The name of the thing this row touched, resolved server-side. Null when
   *  the target has no name to give (a backup path, a hard-deleted row). */
  entity_name: string | null
  metadata: Record<string, unknown> | null
  ip_address: string | null
  user_agent: string | null
  user: { id: string; email: string; full_name: string | null } | null
  created_at: string
}

interface AuditPagination { page: number; limit: number; total: number; total_pages: number }

// The entity types the server actually writes. Four of the six entries here
// used to match nothing at all — the handlers for crews, credentials, members
// and workspace settings recorded no audit rows, so those filters could only
// ever return an empty list. They are real now; keep this list and the write
// sites in step, or the filter starts lying again.
const categories = [
  { label: "All", value: "all" },
  { label: "Agents", value: "AGENT" },
  { label: "Crews", value: "CREW" },
  { label: "Crew links", value: "CREW_LINK" },
  { label: "Credentials", value: "CREDENTIAL" },
  { label: "People", value: "WorkspaceMember" },
  { label: "Workspace", value: "WORKSPACE" },
]

const dateRanges = [
  { label: "Last 24h", value: "24h" },
  { label: "Last 7d", value: "7d" },
  { label: "Last 30d", value: "30d" },
  { label: "All time", value: "all" },
]

function getDateFrom(range: string): string | undefined {
  const now = new Date()
  switch (range) {
    case "24h": return new Date(now.getTime() - 24 * 60 * 60 * 1000).toISOString()
    case "7d": return new Date(now.getTime() - 7 * 24 * 60 * 60 * 1000).toISOString()
    case "30d": return new Date(now.getTime() - 30 * 24 * 60 * 60 * 1000).toISOString()
    default: return undefined
  }
}

const PAGE_SIZE = 50

/** Normalize API response — handles both nested and flat user/metadata shapes */
function normalizeLog(raw: Record<string, unknown>): AuditLog {
  let user: AuditLog["user"] = null
  if (raw.user && typeof raw.user === "object") {
    const u = raw.user as Record<string, unknown>
    user = { id: String(u.id ?? ""), email: String(u.email ?? ""), full_name: (u.full_name as string | null) ?? null }
  } else if (raw.user_email) {
    user = { id: "", email: String(raw.user_email), full_name: (raw.user_name as string | null) ?? null }
  }

  let metadata: Record<string, unknown> | null = null
  if (typeof raw.metadata === "string") {
    try { metadata = JSON.parse(raw.metadata) } catch { metadata = null }
  } else if (raw.metadata && typeof raw.metadata === "object") {
    metadata = raw.metadata as Record<string, unknown>
  }

  return {
    id: String(raw.id ?? ""),
    action: String(raw.action ?? ""),
    entity_type: String(raw.entity_type ?? ""),
    entity_id: (raw.entity_id as string | null) ?? null,
    entity_name: (raw.entity_name as string | null) ?? null,
    metadata,
    ip_address: (raw.ip_address as string | null) ?? null,
    user_agent: (raw.user_agent as string | null) ?? null,
    user,
    created_at: String(raw.created_at ?? ""),
  }
}


// ── Reading a row ────────────────────────────────────────────────────────

/**
 * The four trails a workspace keeps. They are separate tables on purpose —
 * the keeper ledger is append-only, and merging it into a general log would
 * cost exactly the guarantee it exists for — so this switch changes what the
 * page READS, not where anything is stored.
 */
const SOURCES = [
  { value: "workspace", label: "Workspace", hint: "Settings, people, crews, credentials" },
  { value: "crews", label: "Crews", hint: "Cross-crew dispatch, messages, shared files" },
  { value: "credentials", label: "Credentials", hint: "Which secret was used, revealed or rotated" },
  { value: "keeper", label: "Keeper", hint: "Every gatekeeper decision, append-only" },
] as const

type AuditSource = (typeof SOURCES)[number]["value"]

/**
 * Actions that change who can reach what.
 *
 * Not a severity score — a row is either about access or it is not. An agent
 * being created and the privileged-credentials boundary being switched off
 * are both "an update" to a flat renderer, and only one of them is worth
 * waking someone up for.
 */
const SECURITY_ACTIONS = [
  "credential.",
  "member.role",
  "workspace.update",
  "admin.reencrypt",
  "backup.download",
  "crew_link.",
  "agent.hire",
  "keeper.",
]

function isSecurityRelevant(log: AuditLog): boolean {
  const a = log.action.toLowerCase()
  if (a === "deny" || a === "escalate") return true
  return SECURITY_ACTIONS.some((prefix) => a.startsWith(prefix))
}

/** Past-tense verb for the sentence, from the action's last segment. */
function actionVerb(action: string): string {
  const tail = action.includes(".") ? action.slice(action.lastIndexOf(".") + 1) : action
  const map: Record<string, string> = {
    create: "created", update: "updated", delete: "deleted",
    role_change: "changed the role of", reencrypt: "re-encrypted",
    download: "downloaded", rotate: "rotated", revealed: "revealed",
    hired: "hired", rehired: "re-hired",
  }
  return map[tail] ?? tail.replace(/_/g, " ")
}

/** The kind of thing, in the words a person would use. */
function entityNoun(entityType: string): string {
  const map: Record<string, string> = {
    AGENT: "agent", CREW: "crew", CREW_LINK: "crew link", CREDENTIAL: "credential",
    WORKSPACEMEMBER: "member", WORKSPACE: "workspace", BACKUP: "backup",
    CONNECTOR: "connector", AGENT_RUN: "run", KEEPER_REQUEST: "keeper request",
  }
  return map[entityType.toUpperCase()] ?? entityType.toLowerCase().replace(/_/g, " ")
}

/** What the row points at: its name, or the id when there is no name. */
function entityLabel(log: AuditLog): string {
  if (log.entity_name && log.entity_name.trim() !== "") return log.entity_name
  return log.entity_id ?? ""
}

/**
 * A run of adjacent events that are the same event repeated.
 *
 * A reseed writes fifty-six agent deletions in two seconds; rendering
 * fifty-six identical lines buries the one line that mattered that day. Only
 * ADJACENT rows fold, and only when the actor, the verb and the kind all
 * match — a fold that reordered or merged across actors would be inventing a
 * story the log does not tell.
 */
interface AuditGroup {
  key: string
  logs: AuditLog[]
}

function foldRuns(logs: AuditLog[]): AuditGroup[] {
  const out: AuditGroup[] = []
  for (const log of logs) {
    const sig = `${log.action}|${log.entity_type}|${log.user?.email ?? ""}`
    const last = out[out.length - 1]
    if (last && last.key === sig) last.logs.push(log)
    else out.push({ key: sig, logs: [log] })
  }
  return out
}

/** Day buckets, newest first, in the order the server already returned. */
function byDay(logs: AuditLog[]): { day: string; logs: AuditLog[] }[] {
  const out: { day: string; logs: AuditLog[] }[] = []
  for (const log of logs) {
    const day = (log.created_at || "").slice(0, 10)
    const last = out[out.length - 1]
    if (last && last.day === day) last.logs.push(log)
    else out.push({ day, logs: [log] })
  }
  return out
}

function dayHeading(day: string): string {
  if (!day) return "Undated"
  const today = new Date().toISOString().slice(0, 10)
  const yesterday = new Date(Date.now() - 86_400_000).toISOString().slice(0, 10)
  if (day === today) return "Today"
  if (day === yesterday) return "Yesterday"
  return new Date(day + "T00:00:00Z").toLocaleDateString(undefined, {
    weekday: "short", day: "numeric", month: "long",
  })
}

interface CrewAuditSectionProps {
  workspaceId: string
}

export function CrewAuditSection({ workspaceId }: CrewAuditSectionProps) {
  const [logs, setLogs] = useState<AuditLog[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [source, setSource] = useState<AuditSource>("workspace")
  const [category, setCategory] = useState("all")
  const [dateRange, setDateRange] = useState("7d")
  const [searchQuery, setSearchQuery] = useState("")
  const [page, setPage] = useState(1)
  const [pagination, setPagination] = useState<AuditPagination | null>(null)
  const [exporting, setExporting] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  // Cap on rows an export may walk across pages. The audit table can
  // grow large in long-lived workspaces; an unbounded fetch loop would
  // pin the main thread and stall every other settings interaction
  // while it walks pagination. 10k is enough for >6 months of typical
  // CRUD activity and small enough to stay snappy.
  const EXPORT_MAX_ROWS = 10_000

  const fetchLogs = useCallback(async (opts?: { silent?: boolean }) => {
    // Abort any in-flight request
    abortRef.current?.abort()
    const controller = new AbortController()
    abortRef.current = controller

    const silent = opts?.silent ?? false
    if (silent) setRefreshing(true)
    else setLoading(true)
    setError(null)
    try {
      const params = new URLSearchParams({ workspace_id: workspaceId, page: String(page), limit: String(PAGE_SIZE), source })
      // entity_type is the workspace trail's vocabulary; the others index by
      // their own shape, and passing a filter the server would ignore is a
      // filter that lies about what you are looking at.
      if (source === "workspace" && category !== "all") params.set("entity_type", category)
      const dateFrom = getDateFrom(dateRange)
      if (dateFrom) params.set("date_from", dateFrom)

      const res = await apiFetch(`/api/v1/audit?${params}`, { signal: controller.signal })
      if (!res.ok) {
        setError(`Failed to load audit logs (${res.status})`)
        return
      }
      const raw = await res.json()
      const data = Array.isArray(raw.data) ? raw.data.map(normalizeLog) : []
      setLogs(data)
      setPagination(raw.pagination ?? null)
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") return
      setError("Failed to load audit logs")
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [workspaceId, source, category, dateRange, page])

  // Cancellable export — for 10k-row exports the page-walk can run
  // for many seconds; users need a way to bail without abandoning
  // the route. The controller is stashed on a ref so the Cancel
  // button can call .abort() and the catch block can distinguish
  // a user cancellation (DOMException 'AbortError') from a real
  // network failure.
  const exportAbortRef = useRef<AbortController | null>(null)

  const handleCancelExport = useCallback(() => {
    exportAbortRef.current?.abort()
  }, [])

  const handleExport = useCallback(async () => {
    const total = pagination?.total ?? 0
    if (!workspaceId || total === 0) return
    const controller = new AbortController()
    exportAbortRef.current = controller
    setExporting(true)
    try {
      const all: AuditLog[] = []
      const totalToFetch = Math.min(total, EXPORT_MAX_ROWS)
      const pageCount = Math.ceil(totalToFetch / PAGE_SIZE)
      for (let p = 1; p <= pageCount; p++) {
        // Same params as fetchLogs, for the same reason: an export that
        // drops `source` hands back the workspace trail under a filename
        // that says Keeper, and entity_type is the workspace trail's
        // vocabulary alone.
        const params = new URLSearchParams({
          workspace_id: workspaceId,
          page: String(p),
          limit: String(PAGE_SIZE),
          source,
        })
        if (source === "workspace" && category !== "all") params.set("entity_type", category)
        const dateFrom = getDateFrom(dateRange)
        if (dateFrom) params.set("date_from", dateFrom)
        const res = await apiFetch(`/api/v1/audit?${params}`, { signal: controller.signal })
        if (!res.ok) {
          setError("Export failed — partial results discarded")
          return
        }
        const raw = await res.json()
        const data = Array.isArray(raw.data) ? raw.data.map(normalizeLog) : []
        all.push(...data)
        if (all.length >= EXPORT_MAX_ROWS) break
      }
      if (all.length === 0) return
      const rows = all.map((log) => ({
        timestamp: log.created_at,
        action: log.action,
        entity_type: log.entity_type,
        entity_id: log.entity_id,
        user: personLabel(log.user?.full_name, log.user?.email ?? ""),
        ip_address: log.ip_address ?? "",
      }))
      // Neutralise spreadsheet-formula prefixes (=, +, -, @) so an
      // attacker-controlled entity_id or user field can't exfiltrate
      // data when the CSV is opened in Excel/Numbers/Sheets.
      const toCsvCell = (value: unknown): string => {
        const raw = String(value ?? "")
        const safe = /^[\t\r\n ]*[=+\-@]/.test(raw) ? `'${raw}` : raw
        return `"${safe.replace(/"/g, '""')}"`
      }
      const header = Object.keys(rows[0] ?? {}).join(",")
      const csv = [
        header,
        ...rows.map((r) => Object.values(r).map(toCsvCell).join(",")),
      ].join("\n")
      const blob = new Blob([csv], { type: "text/csv" })
      const url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      // Name the trail: four sources land in the same downloads folder,
      // and "audit-log-<date>" for all of them is not a filename.
      a.download = `audit-log-${source}-${new Date().toISOString().slice(0, 10)}.csv`
      a.click()
      URL.revokeObjectURL(url)
      if (total > EXPORT_MAX_ROWS) {
        setError(
          `Export capped at ${EXPORT_MAX_ROWS.toLocaleString()} rows (total matches: ${total.toLocaleString()}). Narrow the date range or category for a complete export.`,
        )
      }
    } catch (err) {
      // User-cancelled exports aren't failures — clear the loading
      // state quietly without surfacing a scary message.
      if (err instanceof DOMException && err.name === "AbortError") return
      setError("Export failed")
    } finally {
      exportAbortRef.current = null
      setExporting(false)
    }
  }, [workspaceId, pagination, source, category, dateRange])

  useEffect(() => { fetchLogs() }, [fetchLogs])

  // Reset page when filters change
  function handleCategoryChange(value: string) {
    setCategory(value)
    setPage(1)
  }
  function handleDateRangeChange(value: string) {
    setDateRange(value)
    setPage(1)
  }

  const filteredLogs = searchQuery
    ? logs.filter(
        (log) =>
          log.action.toLowerCase().includes(searchQuery.toLowerCase()) ||
          log.entity_type.toLowerCase().includes(searchQuery.toLowerCase()) ||
          personLabel(log.user?.full_name, log.user?.email ?? "").toLowerCase().includes(searchQuery.toLowerCase()),
      )
    : logs

  const total = pagination?.total ?? 0
  const totalPages = pagination?.total_pages ?? 1
  const rangeStart = (page - 1) * PAGE_SIZE + 1
  const rangeEnd = Math.min(page * PAGE_SIZE, total)

  return (
    <SettingsCard
      title="Audit log"
      description="Every state-changing action on this workspace, immutably recorded"
      actions={
        <>
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={() => fetchLogs({ silent: true })}
            disabled={loading || refreshing}
            aria-label="Refresh audit log"
          >
            <RefreshCw className={cn("h-3 w-3 mr-1.5", refreshing && "animate-spin")} />
            Refresh
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="h-7 px-2.5 text-xs"
            onClick={handleExport}
            disabled={(pagination?.total ?? 0) === 0 || exporting || loading}
            aria-label="Export audit log to CSV"
          >
            <Download className={cn("h-3 w-3 mr-1.5", exporting && "animate-pulse")} />
            {exporting ? "Exporting…" : "Export CSV"}
          </Button>
          {exporting && (
            <Button
              variant="ghost"
              size="sm"
              className="h-7 px-2.5 text-xs text-muted-foreground hover:text-foreground"
              onClick={handleCancelExport}
              aria-label="Cancel export"
            >
              Cancel
            </Button>
          )}
        </>
      }
    >
      {/* ── Which trail ──
          Four separate tables, one place to read them. The tables stay split
          — the keeper ledger is append-only on purpose — so this changes what
          is read, never where anything is stored. */}
      <div className="flex flex-wrap items-center gap-1 border-b border-border/40 px-4 py-2">
        {SOURCES.map((s) => (
          <button
            key={s.value}
            type="button"
            onClick={() => { setSource(s.value); setCategory("all"); setPage(1); setExpandedId(null) }}
            aria-pressed={source === s.value}
            title={s.hint}
            className={cn(
              "rounded-md px-2.5 py-1 text-[11px] font-medium transition-colors",
              source === s.value
                ? "bg-accent text-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {s.label}
          </button>
        ))}
      </div>

      {/* ── Filter bar ── */}
      <div className="flex items-center gap-2 flex-wrap px-4 py-3 border-b border-border/40">
        {/* entity_type is the workspace trail's vocabulary, and fetchLogs only
            sends it for that source. Rendering the buttons on the other trails
            left them pressable and pressed while filtering nothing — the
            selector said "Credentials" over an unfiltered Keeper log. */}
        {source === "workspace" && (
        <div
          className="inline-flex items-center gap-0.5 p-0.5 rounded-md bg-muted/40 border border-border/60"
          role="group"
          aria-label="Filter by category"
        >
          {categories.map((cat) => (
            <Button
              key={cat.value}
              type="button"
              variant="ghost"
              size="sm"
              aria-pressed={category === cat.value}
              onClick={() => handleCategoryChange(cat.value)}
              className={cn(
                "h-6 px-2.5 rounded text-[11px] font-medium",
                category === cat.value
                  ? "bg-accent text-foreground"
                  : "text-muted-foreground hover:text-foreground",
              )}
            >
              {cat.label}
            </Button>
          ))}
        </div>
        )}
        <Select value={dateRange} onValueChange={handleDateRangeChange}>
          <SelectTrigger aria-label="Date range" className="w-[120px] h-7 text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {dateRanges.map((dr) => (
              <SelectItem key={dr.value} value={dr.value} className="text-xs">{dr.label}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="relative flex-1 min-w-[160px] max-w-[260px]">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3 w-3 text-muted-foreground" />
          <Input
            aria-label="Filter events on this page"
            placeholder="Filter this page…"
            className="pl-7 h-7 text-xs"
            value={searchQuery}
            onChange={(e) => setSearchQuery(e.target.value)}
          />
        </div>
      </div>

      {/* Error with stale data */}
      {error && logs.length > 0 && (
        <div role="alert" className="text-[11px] text-destructive px-4 py-2 border-b border-border/40 bg-destructive/5">
          {error}
        </div>
      )}

      {/* Content */}
      {loading ? (
        Array.from({ length: 5 }).map((_, i) => (
          <div key={i} className={cn("px-4 py-2.5", i < 4 && "border-b border-border/40")}>
            <Skeleton className="h-3.5 w-full" />
          </div>
        ))
      ) : error && logs.length === 0 ? (
        // Only take over the pane when there is nothing to take over FROM.
        // A failed background refresh used to replace a perfectly good table
        // with a full-page error — and print the same message twice, once
        // here and once in the stale-data banner above, which promises the
        // opposite ("your rows are still here, they're just old").
        <div className="p-6 text-center">
          <p role="alert" className="text-xs text-destructive mb-3">{error}</p>
          <Button variant="outline" size="sm" className="h-7 px-2.5 text-xs" onClick={() => fetchLogs()}>
            Retry
          </Button>
        </div>
      ) : filteredLogs.length === 0 ? (
        <SettingsEmpty>
          <div className="flex flex-col items-center gap-3 py-6">
            <div className="w-10 h-10 rounded-lg bg-muted/50 flex items-center justify-center">
              <Shield className="h-4 w-4 text-muted-foreground" />
            </div>
            <div>
              <div className="text-sm font-medium text-foreground/80">
                {searchQuery ? "No matching events" : "No activity yet"}
              </div>
              <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
                {searchQuery ? "Try a different search term" : "All state-changing actions will be logged here."}
              </div>
            </div>
          </div>
        </SettingsEmpty>
      ) : (
        <>
          {byDay(filteredLogs).map((bucket) => (
            <div key={bucket.day}>
              {/* A day is the unit people actually search in ("what happened
                  on the 27th"), and it is free to compute — the server
                  already returns newest-first. */}
              <h3 className="sticky top-0 z-10 border-b border-border/40 bg-card/95 px-4 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground backdrop-blur">
                {dayHeading(bucket.day)}
                <span className="ml-2 font-mono text-[10px] font-normal normal-case tracking-normal text-muted-foreground/60">
                  {bucket.logs.length}
                </span>
              </h3>
              {foldRuns(bucket.logs).map((group) =>
                group.logs.length > 1 ? (
                  <FoldedRun key={group.logs[0].id} group={group} />
                ) : (
                  <AuditRow
                    key={group.logs[0].id}
                    log={group.logs[0]}
                    expanded={expandedId === group.logs[0].id}
                    onToggle={() =>
                      setExpandedId(expandedId === group.logs[0].id ? null : group.logs[0].id)
                    }
                  />
                ),
              )}
            </div>
          ))}

          {/* Pagination */}
          {total > 0 && (
            <div className="flex items-center justify-between gap-2 flex-wrap px-4 py-2.5 border-t border-border/40">
              <span className="text-[11px] text-muted-foreground font-mono tabular-nums">
                Showing {rangeStart}–{rangeEnd} of {total}
              </span>
              <div className="flex items-center gap-1.5">
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={page <= 1}
                  onClick={() => setPage((p) => Math.max(1, p - 1))}
                >
                  <ChevronLeft className="h-3 w-3 mr-1" />
                  Previous
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 px-2 text-xs"
                  disabled={page >= totalPages}
                  onClick={() => setPage((p) => p + 1)}
                >
                  Next
                  <ChevronRight className="h-3 w-3 ml-1" />
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </SettingsCard>
  )
}


/**
 * One event, as a sentence: who, then what they did to which thing.
 *
 * The old row was "who · [verb] · TYPE · id-prefix", which never said WHICH
 * thing — you could not tell "created agent Riley" from "created agent Sam"
 * without going and looking the id up somewhere else.
 */
function AuditRow({
  log, expanded, onToggle,
}: { log: AuditLog; expanded: boolean; onToggle: () => void }) {
  const label = entityLabel(log)
  const security = isSecurityRelevant(log)
  return (
    <div data-audit-weight={security ? "security" : "routine"}>
      <Button
        type="button"
        variant="ghost"
        aria-expanded={expanded}
        aria-controls={`audit-detail-${log.id}`}
        className={cn(
          "flex h-auto w-full items-center justify-between gap-3 rounded-none border-b border-border/40 px-4 py-2 text-left font-normal",
          expanded && "bg-accent/50",
        )}
        onClick={onToggle}
      >
        <span className="flex min-w-0 items-center gap-2.5">
          <ChevronRight
            className={cn(
              "h-3 w-3 shrink-0 text-muted-foreground transition-transform duration-150",
              expanded && "rotate-90 text-foreground",
            )}
          />
          <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
            {formatTimeOfDay(log.created_at)}
          </span>
          <span className="min-w-0 truncate text-xs">
            <span className="text-foreground/80">
              {personLabel(log.user?.full_name, log.user?.email ?? "") || "System"}
            </span>{" "}
            <span className="text-muted-foreground">{actionVerb(log.action)}</span>{" "}
            <span className="text-muted-foreground">{entityNoun(log.entity_type)}</span>{" "}
            <span className="font-medium text-foreground">{label}</span>
          </span>
        </span>
        {security && (
          <span className="shrink-0 rounded-full border border-warn/40 bg-warn/10 px-1.5 py-0.5 text-[9px] font-medium uppercase tracking-wide text-warn">
            access
          </span>
        )}
      </Button>

      <AnimatePresence initial={false}>
        {expanded && (
          <motion.div
            id={`audit-detail-${log.id}`}
            role="region"
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.15, ease: "easeInOut" }}
            className="overflow-hidden border-b border-border/40 bg-muted/20"
          >
            <div className="px-4 py-3 pl-11">
              <div className="grid max-w-3xl gap-3 sm:grid-cols-2">
                <div>
                  <div className="mb-0.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    Action
                  </div>
                  <div className="font-mono text-[11px] text-foreground/80">{log.action}</div>
                </div>
                <div>
                  <div className="mb-0.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    Entity
                  </div>
                  <div className="truncate font-mono text-[11px] text-foreground/80" title={log.entity_id ?? ""}>
                    {log.entity_type} · {log.entity_id ?? "—"}
                  </div>
                </div>
                <div>
                  <div className="mb-0.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    IP address
                  </div>
                  <div className="font-mono text-[11px] text-foreground/80">{log.ip_address ?? "—"}</div>
                </div>
                <div>
                  <div className="mb-0.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    User agent
                  </div>
                  <div className="truncate font-mono text-[11px] text-foreground/80" title={log.user_agent ?? ""}>
                    {log.user_agent ?? "—"}
                  </div>
                </div>
                {log.metadata && Object.keys(log.metadata).length > 0 && (
                  <div className="sm:col-span-2">
                    <div className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                      Details
                    </div>
                    <pre className="max-h-32 overflow-auto rounded border border-border/60 bg-muted/40 p-2 font-mono text-[10px] text-muted-foreground">
                      {JSON.stringify(log.metadata, null, 2)}
                    </pre>
                  </div>
                )}
              </div>
              <div className="mt-3 flex items-center gap-1.5 text-[10px] text-muted-foreground">
                <Shield className="h-3 w-3" />
                This record is immutable.
              </div>
            </div>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

/**
 * A burst of the same event, folded to one line until asked to unfold.
 *
 * A reseed writes fifty-six agent deletions in two seconds. Rendered flat,
 * they bury whatever else happened that day under identical text; folded,
 * the day reads as "one thing happened fifty-six times" — which is what it
 * was — and the individual rows are one click away.
 */
function FoldedRun({ group }: { group: AuditGroup }) {
  const [open, setOpen] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const first = group.logs[0]
  const security = isSecurityRelevant(first)
  return (
    <div data-audit-weight={security ? "security" : "routine"}>
      <Button
        type="button"
        variant="ghost"
        aria-expanded={open}
        className={cn(
          "flex h-auto w-full items-center justify-between gap-3 rounded-none border-b border-border/40 px-4 py-2 text-left font-normal",
          open && "bg-accent/50",
        )}
        onClick={() => setOpen((v) => !v)}
      >
        <span className="flex min-w-0 items-center gap-2.5">
          <ChevronRight
            className={cn(
              "h-3 w-3 shrink-0 text-muted-foreground transition-transform duration-150",
              open && "rotate-90 text-foreground",
            )}
          />
          <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground">
            {formatTimeOfDay(first.created_at)}
          </span>
          <span className="min-w-0 truncate text-xs">
            <span className="text-foreground/80">
              {personLabel(first.user?.full_name, first.user?.email ?? "") || "System"}
            </span>{" "}
            <span className="font-medium text-foreground">
              {group.logs.length} × {actionVerb(first.action)}
            </span>{" "}
            <span className="text-muted-foreground">
              {entityNoun(first.entity_type)}
              {group.logs.length === 1 ? "" : "s"}
            </span>
          </span>
        </span>
      </Button>

      {open && (
        <div className="border-b border-border/40 bg-muted/10 pl-6">
          {group.logs.map((log) => (
            <AuditRow
              key={log.id}
              log={log}
              expanded={expandedId === log.id}
              onToggle={() => setExpandedId(expandedId === log.id ? null : log.id)}
            />
          ))}
        </div>
      )}
    </div>
  )
}

/** Time of day only — the date is already on the group heading above. */
function formatTimeOfDay(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return "--:--"
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })
}
