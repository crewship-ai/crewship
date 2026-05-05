"use client"

import { useCallback, useMemo, useState } from "react"
import type { JournalEntry } from "@/lib/types/journal"
import { groupOf, type EntryGroup, GROUP_ORDER } from "@/lib/journal-style"
import { LogsToolbar, type SeverityFilter, type SeverityCounts } from "./logs-toolbar"
import { LogsTypeChips } from "./logs-type-chips"
import { LogsHistogram } from "./logs-histogram"
import { LogsList } from "./logs-list"
import { LogsStatsRail } from "./logs-stats-rail"

interface LogsPanelProps {
  entries: JournalEntry[]
}

/**
 * Grafana Explore-style log viewer for Crow's Nest.
 *
 * Owns its own filter state (search, severity, muted groups, view toggles)
 * and derives every downstream slice from `entries`. Keeps the parent
 * page free of UI bookkeeping so other tabs (Terminal, Network, Filesystem)
 * can render independent slices of the same `entries` prop.
 */
export function LogsPanel({ entries }: LogsPanelProps) {
  const [query, setQuery] = useState("")
  const [severity, setSeverity] = useState<SeverityFilter>("all")
  const [muted, setMuted] = useState<Set<EntryGroup>>(new Set())
  const [live, setLive] = useState(true)
  const [wrap, setWrap] = useState(false)
  const [newestFirst, setNewestFirst] = useState(true)
  const [dedup, setDedup] = useState(false)

  // Pre-compile regex once if the user typed `/.../` syntax.
  const matcher = useMemo(() => buildMatcher(query), [query])

  // Stage 1: severity + search filter (used for type-chip counts).
  const sevSearchFiltered = useMemo(() => {
    if (severity === "all" && !matcher) return entries
    return entries.filter((e) => {
      if (severity !== "all" && e.severity !== severity) return false
      if (matcher && !matcher(e)) return false
      return true
    })
  }, [entries, severity, matcher])

  // Group counts on the post-severity-search set, so chip totals reflect
  // what's actually being shown when the user hasn't muted any group.
  const groupCounts = useMemo(() => {
    const c: Record<EntryGroup, number> = Object.fromEntries(
      GROUP_ORDER.map((g) => [g, 0]),
    ) as Record<EntryGroup, number>
    for (const e of sevSearchFiltered) {
      c[groupOf(e.entry_type)] += 1
    }
    return c
  }, [sevSearchFiltered])

  // Stage 2: apply muted groups.
  const filtered = useMemo(() => {
    if (muted.size === 0) return sevSearchFiltered
    return sevSearchFiltered.filter((e) => !muted.has(groupOf(e.entry_type)))
  }, [sevSearchFiltered, muted])

  // Stage 3: order + dedup.
  const ordered = useMemo(() => {
    const arr = filtered.slice()
    arr.sort((a, b) => {
      const t = new Date(b.ts).getTime() - new Date(a.ts).getTime()
      return newestFirst ? t : -t
    })
    if (!dedup) return arr
    const out: JournalEntry[] = []
    for (const e of arr) {
      const prev = out[out.length - 1]
      if (prev && prev.entry_type === e.entry_type && prev.summary === e.summary) continue
      out.push(e)
    }
    return out
  }, [filtered, newestFirst, dedup])

  // Severity counts for the segmented control are derived from the
  // search-only filter (NOT including the severity filter itself, which
  // would always make every non-active count = 0).
  const severityCounts = useMemo<SeverityCounts>(() => {
    const c: SeverityCounts = { all: 0, info: 0, notice: 0, warn: 0, error: 0 }
    const base = matcher ? entries.filter(matcher) : entries
    c.all = base.length
    for (const e of base) {
      const s = e.severity
      if (s === "info" || s === "notice" || s === "warn" || s === "error") c[s] += 1
    }
    return c
  }, [entries, matcher])

  const onToggleGroup = useCallback((g: EntryGroup) => {
    setMuted((prev) => {
      const next = new Set(prev)
      if (next.has(g)) next.delete(g)
      else next.add(g)
      return next
    })
  }, [])

  const onResetGroups = useCallback(() => setMuted(new Set()), [])

  const onExport = useCallback(() => {
    const blob = new Blob([JSON.stringify(ordered, null, 2)], { type: "application/json" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `crows-nest-logs-${new Date().toISOString().slice(0, 19).replace(/[:.]/g, "-")}.json`
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    URL.revokeObjectURL(url)
  }, [ordered])

  return (
    <div className="flex flex-col h-full min-h-0">
      <LogsToolbar
        query={query}
        onQueryChange={setQuery}
        severity={severity}
        onSeverityChange={setSeverity}
        counts={severityCounts}
        visibleCount={ordered.length}
        totalCount={entries.length}
        live={live}
        onLiveToggle={() => setLive((v) => !v)}
        wrap={wrap}
        onWrapToggle={() => setWrap((v) => !v)}
        newestFirst={newestFirst}
        onNewestToggle={() => setNewestFirst((v) => !v)}
        dedup={dedup}
        onDedupToggle={() => setDedup((v) => !v)}
        onExport={onExport}
      />
      <LogsTypeChips
        counts={groupCounts}
        muted={muted}
        onToggle={onToggleGroup}
        onResetAll={onResetGroups}
      />
      <LogsHistogram entries={filtered} />
      <div className="flex-1 min-h-0 grid" style={{ gridTemplateColumns: "minmax(0,1fr) 280px" }}>
        <div className="border-r border-border/50 min-h-0 overflow-hidden">
          <LogsList
            entries={ordered}
            wrap={wrap}
            followTail={live}
            newestFirst={newestFirst}
          />
        </div>
        <LogsStatsRail entries={ordered} />
      </div>
    </div>
  )
}

/**
 * Build a predicate from the search string. Supports:
 *   /pattern/        → case-insensitive regex on summary + entry_type
 *   path:/some/path  → substring match on payload.path
 *   foo bar          → all-tokens substring match on summary + entry_type
 */
function buildMatcher(q: string): ((e: JournalEntry) => boolean) | null {
  const trimmed = q.trim()
  if (!trimmed) return null

  // /regex/ form
  const rx = trimmed.match(/^\/(.+)\/([imsx]*)$/)
  if (rx) {
    try {
      const re = new RegExp(rx[1], rx[2] || "i")
      return (e) => re.test(e.summary || "") || re.test(e.entry_type)
    } catch {
      // fall through to plain text on invalid regex
    }
  }

  // path:foo bar — split into k:v pairs + free tokens
  const tokens = trimmed.split(/\s+/)
  const kv: Array<[string, string]> = []
  const free: string[] = []
  for (const t of tokens) {
    const m = t.match(/^([a-z_]+):(.+)$/i)
    if (m) kv.push([m[1].toLowerCase(), m[2].toLowerCase()])
    else free.push(t.toLowerCase())
  }

  return (e) => {
    const hay = `${e.summary || ""} ${e.entry_type}`.toLowerCase()
    for (const tok of free) {
      if (!hay.includes(tok)) return false
    }
    if (kv.length > 0) {
      for (const [k, v] of kv) {
        const value = readField(e, k)
        if (!value || !String(value).toLowerCase().includes(v)) return false
      }
    }
    return true
  }
}

function readField(e: JournalEntry, k: string): unknown {
  switch (k) {
    case "type": return e.entry_type
    case "sev": case "severity": return e.severity
    case "agent": case "agent_id": return e.agent_id
    case "crew": case "crew_id": return e.crew_id
    case "mission": case "mission_id": return e.mission_id
    case "trace": case "trace_id": return e.trace_id
    default: return e.payload ? e.payload[k] : undefined
  }
}
