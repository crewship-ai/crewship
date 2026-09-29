"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Shield, ChevronRight, ChevronLeft, RefreshCw, Download, ScrollText, Cpu } from "lucide-react"
import { motion, AnimatePresence } from "motion/react"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import type { StatusTone } from "@/lib/format-status"
import { cn } from "@/lib/utils"
import { apiFetch } from "@/lib/api-fetch"
import { UserAvatar, personLabel } from "@/components/ui/user-avatar"
import { SettingsCard, SettingsEmpty } from "../shared"
import {
  AUDIT_SOURCES,
  DEFAULT_AUDIT_FILTERS,
  activeFilterChips,
  auditQueryParams,
  filtersFromSearch,
  filtersToSearch,
  type AuditFilters,
} from "../audit-log/audit-filters"
import { AuditToolbar, type AuditPerson } from "../audit-log/audit-toolbar"

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
  const [filters, setFilters] = useAuditFilters()
  const [page, setPage] = useState(1)
  const [pagination, setPagination] = useState<AuditPagination | null>(null)
  const [exporting, setExporting] = useState(false)
  const people = useWorkspacePeople(workspaceId)
  const abortRef = useRef<AbortController | null>(null)

  // Cap on rows an export may walk across pages. The audit table can
  // grow large in long-lived workspaces; an unbounded fetch loop would
  // pin the main thread and stall every other settings interaction
  // while it walks pagination. 10k is enough for >6 months of typical
  // CRUD activity and small enough to stay snappy.
  const EXPORT_MAX_ROWS = 10_000

  // Every filter change starts again from page one.
  const changeFilters = useCallback((next: Partial<AuditFilters>) => {
    setFilters(next)
    setPage(1)
    setExpandedId(null)
  }, [setFilters])

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
      // auditQueryParams is the one place the query is built: it drops the
      // filters a trail does not honour, so a filter never claims to narrow a
      // list the server returned unfiltered.
      const params = auditQueryParams(workspaceId, filters, page, PAGE_SIZE, new Date())
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
  }, [workspaceId, filters, page])

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
      // One "now" for the whole walk, so a relative range does not slide
      // between the first page and the last.
      const now = new Date()
      for (let p = 1; p <= pageCount; p++) {
        // Same query as the table: the export is exactly what is on screen.
        const params = auditQueryParams(workspaceId, filters, p, PAGE_SIZE, now)
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
        entity_name: log.entity_name ?? "",
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
      a.download = `audit-log-${filters.source}-${new Date().toISOString().slice(0, 10)}.csv`
      a.click()
      URL.revokeObjectURL(url)
      if (total > EXPORT_MAX_ROWS) {
        setError(
          `Export capped at ${EXPORT_MAX_ROWS.toLocaleString()} rows (total matches: ${total.toLocaleString()}). Narrow the time range or filters for a complete export.`,
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
  }, [workspaceId, pagination, filters])

  useEffect(() => { fetchLogs() }, [fetchLogs])

  const total = pagination?.total ?? 0
  const totalPages = pagination?.total_pages ?? 1
  const rangeStart = (page - 1) * PAGE_SIZE + 1
  const rangeEnd = Math.min(page * PAGE_SIZE, total)
  const filtered = activeFilterChips(filters, {}).length > 0

  return (
    <SettingsCard icon={ScrollText}
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
      <div className="flex flex-wrap items-center gap-1 border-b border-border/60 px-4 pt-2" role="tablist" aria-label="Audit trail">
        {AUDIT_SOURCES.map((s) => {
          const on = filters.source === s.value
          return (
            <button
              key={s.value}
              type="button"
              onClick={() => changeFilters({ ...DEFAULT_AUDIT_FILTERS, source: s.value, range: filters.range, from: filters.from, to: filters.to })}
              aria-pressed={on}
              title={s.hint}
              className={cn(
                "-mb-px border-b-2 px-3 pb-2 pt-1 text-xs font-medium transition-colors",
                on ? "border-primary text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              {s.label}
            </button>
          )
        })}
      </div>

      <AuditToolbar filters={filters} onChange={changeFilters} people={people} />

      {/* Error with stale data */}
      {error && logs.length > 0 && (
        <div role="alert" className="text-[11px] text-destructive px-4 py-2 border-b border-border/40 bg-destructive/5">
          {error}
        </div>
      )}

      {/* Column heads — the rows below are a table, read left to right. */}
      {!loading && logs.length > 0 && (
        <div className="hidden grid-cols-[14px_72px_minmax(0,12rem)_minmax(0,1fr)_auto] items-center gap-3 border-b border-border/60 bg-surface-subtle px-4 py-1.5 sm:grid">
          <span />
          <span className="eyebrow text-muted-foreground">Time</span>
          <span className="eyebrow text-muted-foreground">Person</span>
          <span className="eyebrow text-muted-foreground">Event</span>
          <span />
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
      ) : logs.length === 0 ? (
        <SettingsEmpty>
          <div className="flex flex-col items-center gap-3 py-6">
            <div className="icon-tile flex h-10 w-10 items-center justify-center rounded-lg">
              <Shield className="h-4 w-4" />
            </div>
            <div>
              <div className="text-sm font-medium text-foreground/80">
                {filtered ? "No events match these filters" : "No activity yet"}
              </div>
              <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
                {filtered ? "Widen the time range or clear a filter." : "All state-changing actions will be logged here."}
              </div>
            </div>
          </div>
        </SettingsEmpty>
      ) : (
        <>
          {byDay(logs).map((bucket) => (
            <div key={bucket.day}>
              {/* A day is the unit people actually search in ("what happened
                  on the 27th"), and it is free to compute — the server
                  already returns newest-first. */}
              <h3 className="sticky top-0 z-10 border-b border-border/40 bg-card/95 px-4 py-1.5 font-mono text-[11px] font-medium uppercase tracking-[0.08em] text-muted-foreground backdrop-blur">
                {dayHeading(bucket.day)}
                <span className="ml-2 font-mono text-[11px] font-normal normal-case tracking-normal text-muted-foreground-soft">
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
                {rangeStart}–{rangeEnd} of {total.toLocaleString()} events
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
 * Audit filters kept in the page URL (audit_* params next to ?tab=audit), so a
 * reload, a shared link or Back lands on the same slice of the log. Uses the
 * History API directly: the filters are this section's state, not a route.
 */
function useAuditFilters(): [AuditFilters, (next: Partial<AuditFilters>) => void] {
  const [filters, setState] = useState<AuditFilters>(() =>
    typeof window === "undefined" ? DEFAULT_AUDIT_FILTERS : filtersFromSearch(window.location.search),
  )
  const update = useCallback((next: Partial<AuditFilters>) => {
    setState((prev) => {
      const merged = { ...prev, ...next }
      if (typeof window !== "undefined") {
        const search = filtersToSearch(merged, window.location.search)
        window.history.replaceState(window.history.state, "", `${window.location.pathname}${search}${window.location.hash}`)
      }
      return merged
    })
  }, [])
  return [filters, update]
}

/** The workspace's people, for the Person filter. Empty until it loads; a
 *  failure leaves the filter with "Everyone" only. */
function useWorkspacePeople(workspaceId: string): AuditPerson[] {
  const [people, setPeople] = useState<AuditPerson[]>([])
  useEffect(() => {
    let cancelled = false
    apiFetch(`/api/v1/workspaces/${encodeURIComponent(workspaceId)}/members`)
      .then(async (res) => (res.ok ? res.json() : []))
      .then((rows: unknown) => {
        if (cancelled || !Array.isArray(rows)) return
        const list = rows
          .map((r) => (r && typeof r === "object" ? (r as { user?: { id?: string; email?: string; full_name?: string | null } }).user : null))
          .filter((u): u is { id: string; email: string; full_name?: string | null } => Boolean(u?.id))
          .map((u) => ({ id: u.id, label: personLabel(u.full_name, u.email ?? "") || u.email }))
          .sort((a, b) => a.label.localeCompare(b.label))
        setPeople(list)
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [workspaceId])
  return useMemo(() => people, [people])
}

/** The verb's tone: what was made, changed, removed or broke. */
function verbTone(action: string): StatusTone {
  const tail = (action.includes(".") ? action.slice(action.lastIndexOf(".") + 1) : action).toLowerCase()
  if (/^(create|created|hired|rehired|add|added|invite|invited)$/.test(tail)) return "success"
  if (/^(delete|deleted|remove|removed|revoke|revoked|failed|deny)$/.test(tail)) return "danger"
  if (/^(revealed|download|reencrypt|escalate|rotate)$/.test(tail)) return "warn"
  if (/^(update|updated|role_change|patch|rename)$/.test(tail)) return "blue"
  return "muted"
}

/** An id-shaped name (run_…, msg_…, cuids) is shortened; the full value is on hover and in the details. */
function shortIdLabel(label: string): string {
  if (label.length > 28 && /^[a-z]+_[0-9a-z_]+$/i.test(label)) return `${label.slice(0, 14)}…${label.slice(-6)}`
  return label
}

function Actor({ user }: { user: AuditLog["user"] }) {
  const label = personLabel(user?.full_name, user?.email ?? "")
  if (!label) {
    return (
      <span className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
        <span className="grid h-5 w-5 shrink-0 place-items-center rounded-full bg-accent"><Cpu className="h-3 w-3" aria-hidden /></span>
        System
      </span>
    )
  }
  return (
    <span className="flex min-w-0 items-center gap-2 text-xs">
      <UserAvatar name={user?.full_name} email={user?.email ?? ""} className="h-5 w-5 shrink-0" textClassName="text-[8px]" />
      <span className="truncate text-foreground">{label}</span>
    </span>
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
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls={`audit-detail-${log.id}`}
        className={cn(
          "grid w-full items-center gap-x-3 gap-y-1 border-b border-border/40 px-4 py-2 text-left transition-colors hover:bg-[var(--row-hover-bg)]",
          "grid-cols-[14px_minmax(0,1fr)_auto] sm:grid-cols-[14px_72px_minmax(0,12rem)_minmax(0,1fr)_auto]",
          expanded && "bg-[var(--selection-bg)]",
        )}
        onClick={onToggle}
      >
        <ChevronRight
          className={cn(
            "h-3 w-3 shrink-0 text-muted-foreground transition-transform duration-150",
            expanded && "rotate-90 text-foreground",
          )}
        />
        <span className="hidden font-mono text-[11px] tabular-nums text-muted-foreground sm:block">
          {formatTimeOfDay(log.created_at)}
        </span>
        <span className="hidden min-w-0 sm:block"><Actor user={log.user} /></span>
        <span className="flex min-w-0 items-center gap-2 text-xs">
          <StatusPill tone={verbTone(log.action)} label={actionVerb(log.action)} />
          <span className="shrink-0 text-muted-foreground">{entityNoun(log.entity_type)}</span>
          <span className="truncate font-medium text-foreground" title={label}>{shortIdLabel(label)}</span>
        </span>
        <span className="flex items-center gap-2">
          <span className="font-mono text-[11px] tabular-nums text-muted-foreground sm:hidden">{formatTimeOfDay(log.created_at)}</span>
          {security && <StatusPill tone="warn" label="Access" />}
        </span>
      </button>

      <AnimatePresence initial={false}>
        {expanded && (
          <motion.div
            id={`audit-detail-${log.id}`}
            role="region"
            initial={{ height: 0, opacity: 0 }}
            animate={{ height: "auto", opacity: 1 }}
            exit={{ height: 0, opacity: 0 }}
            transition={{ duration: 0.15, ease: "easeInOut" }}
            className="overflow-hidden border-b border-border/40 bg-surface-subtle"
          >
            <div className="px-4 py-3 sm:pl-[calc(1rem+14px+0.75rem)]">
              <div className="grid max-w-3xl gap-3 sm:grid-cols-2">
                <div>
                  <div className="eyebrow mb-1 text-muted-foreground">
                    Action
                  </div>
                  <div className="font-mono text-[11px] text-foreground/80">{log.action}</div>
                </div>
                <div>
                  <div className="eyebrow mb-1 text-muted-foreground">
                    Entity
                  </div>
                  <div className="truncate font-mono text-[11px] text-foreground/80" title={log.entity_id ?? ""}>
                    {log.entity_type} · {log.entity_id ?? "—"}
                  </div>
                </div>
                <div>
                  <div className="eyebrow mb-1 text-muted-foreground">
                    IP address
                  </div>
                  <div className="font-mono text-[11px] text-foreground/80">{log.ip_address ?? "—"}</div>
                </div>
                <div>
                  <div className="eyebrow mb-1 text-muted-foreground">
                    User agent
                  </div>
                  <div className="truncate font-mono text-[11px] text-foreground/80" title={log.user_agent ?? ""}>
                    {log.user_agent ?? "—"}
                  </div>
                </div>
                {log.metadata && Object.keys(log.metadata).length > 0 && (
                  <div className="sm:col-span-2">
                    <div className="eyebrow mb-1 text-muted-foreground">
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
      <button
        type="button"
        aria-expanded={open}
        className={cn(
          "grid w-full items-center gap-x-3 gap-y-1 border-b border-border/40 px-4 py-2 text-left transition-colors hover:bg-[var(--row-hover-bg)]",
          "grid-cols-[14px_minmax(0,1fr)_auto] sm:grid-cols-[14px_72px_minmax(0,12rem)_minmax(0,1fr)_auto]",
          open && "bg-[var(--selection-bg)]",
        )}
        onClick={() => setOpen((v) => !v)}
      >
        <ChevronRight
          className={cn(
            "h-3 w-3 shrink-0 text-muted-foreground transition-transform duration-150",
            open && "rotate-90 text-foreground",
          )}
        />
        <span className="hidden font-mono text-[11px] tabular-nums text-muted-foreground sm:block">
          {formatTimeOfDay(first.created_at)}
        </span>
        <span className="hidden min-w-0 sm:block"><Actor user={first.user} /></span>
        <span className="flex min-w-0 items-center gap-2 text-xs">
          <StatusPill tone={verbTone(first.action)} label={`${group.logs.length} × ${actionVerb(first.action)}`} />
          <span className="text-muted-foreground">
            {entityNoun(first.entity_type)}
            {group.logs.length === 1 ? "" : "s"}
          </span>
        </span>
        <span className="flex items-center gap-2">
          <span className="font-mono text-[11px] tabular-nums text-muted-foreground sm:hidden">{formatTimeOfDay(first.created_at)}</span>
          {security && <StatusPill tone="warn" label="Access" />}
        </span>
      </button>

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
