"use client"

import { Search, Pause, Play, WrapText, ArrowDownUp, Filter, Download } from "lucide-react"
import { cn } from "@/lib/utils"
import { Input } from "@/components/ui/input"
import { SEVERITY_COLOR } from "@/lib/journal-style"
import type { JournalSeverity } from "@/lib/types/journal"

export type SeverityFilter = "all" | JournalSeverity

export interface SeverityCounts {
  all: number
  info: number
  notice: number
  warn: number
  error: number
}

interface LogsToolbarProps {
  query: string
  onQueryChange: (q: string) => void
  severity: SeverityFilter
  onSeverityChange: (s: SeverityFilter) => void
  counts: SeverityCounts
  visibleCount: number
  totalCount: number
  live: boolean
  onLiveToggle: () => void
  wrap: boolean
  onWrapToggle: () => void
  newestFirst: boolean
  onNewestToggle: () => void
  dedup: boolean
  onDedupToggle: () => void
  onExport?: () => void
}

const SEV_ORDER: SeverityFilter[] = ["all", "info", "notice", "warn", "error"]

export function LogsToolbar({
  query,
  onQueryChange,
  severity,
  onSeverityChange,
  counts,
  visibleCount,
  totalCount,
  live,
  onLiveToggle,
  wrap,
  onWrapToggle,
  newestFirst,
  onNewestToggle,
  dedup,
  onDedupToggle,
  onExport,
}: LogsToolbarProps) {
  return (
    <div className="px-3 py-2 border-b border-border/50 bg-card/40 flex flex-wrap items-center gap-2">
      {/* search */}
      <div className="relative flex-1 min-w-[260px] max-w-[480px]">
        <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
        <Input
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          placeholder="Search   /regex/   path:/home/agent   keeper.decision"
          className="h-7 pl-7 text-[12px] font-mono"
        />
        {query && (
          <button
            type="button"
            onClick={() => onQueryChange("")}
            className="absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-muted-foreground hover:text-foreground font-mono"
            aria-label="Clear search"
          >
            ✕
          </button>
        )}
      </div>

      <span className="opacity-30">│</span>

      {/* severity segmented */}
      <div className="inline-flex rounded border border-border/60 bg-card overflow-hidden">
        {SEV_ORDER.map((s) => {
          const active = severity === s
          const count = counts[s]
          return (
            <button
              key={s}
              type="button"
              onClick={() => onSeverityChange(s)}
              className={cn(
                "h-7 px-2 text-[10px] font-mono uppercase tracking-wider flex items-center gap-1 transition-colors",
                active
                  ? "bg-primary/15 text-primary border-r border-border/60 last:border-r-0"
                  : "text-muted-foreground hover:text-foreground border-r border-border/60 last:border-r-0",
              )}
              style={
                active && s !== "all"
                  ? { color: SEVERITY_COLOR[s as JournalSeverity], background: `${SEVERITY_COLOR[s as JournalSeverity]}1a` }
                  : undefined
              }
            >
              <span>{s}</span>
              <span className="opacity-70 tabular-nums">{count}</span>
            </button>
          )
        })}
      </div>

      <span className="opacity-30">│</span>

      <ToolbarToggle on={live} onClick={onLiveToggle} title={live ? "Pause live tail" : "Resume live tail"}>
        {live ? (
          <>
            <span className="inline-block h-1.5 w-1.5 rounded-full bg-emerald-400 animate-pulse" /> Live
          </>
        ) : (
          <>
            <Pause className="h-3 w-3" /> Paused
          </>
        )}
      </ToolbarToggle>
      <ToolbarToggle on={wrap} onClick={onWrapToggle} title="Wrap long lines">
        <WrapText className="h-3 w-3" /> Wrap
      </ToolbarToggle>
      <ToolbarToggle on={newestFirst} onClick={onNewestToggle} title="Sort newest first">
        <ArrowDownUp className="h-3 w-3" /> {newestFirst ? "Newest" : "Oldest"}
      </ToolbarToggle>
      <ToolbarToggle on={dedup} onClick={onDedupToggle} title="Collapse identical adjacent entries">
        <Filter className="h-3 w-3" /> Dedup
      </ToolbarToggle>

      <div className="flex-1" />

      <span className="inline-flex items-center gap-1.5 px-2 h-6 rounded border border-border/60 bg-card text-[10px] font-mono">
        <span className="text-muted-foreground">visible</span>
        <span className="text-foreground tabular-nums">{visibleCount.toLocaleString()}</span>
        <span className="text-muted-foreground">/ {totalCount.toLocaleString()}</span>
      </span>

      {onExport && (
        <ToolbarToggle on={false} onClick={onExport} title="Export visible entries to JSON">
          <Download className="h-3 w-3" /> Export
        </ToolbarToggle>
      )}

      {!live && (
        <button
          type="button"
          onClick={onLiveToggle}
          className="inline-flex items-center gap-1 h-6 px-2 rounded border border-emerald-500/40 bg-emerald-500/10 text-[10px] text-emerald-300 hover:bg-emerald-500/20"
          title="Resume live tail"
        >
          <Play className="h-3 w-3" /> Resume
        </button>
      )}
    </div>
  )
}

function ToolbarToggle({
  on,
  onClick,
  title,
  children,
}: {
  on: boolean
  onClick: () => void
  title: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      className={cn(
        "inline-flex items-center gap-1 h-6 px-2 rounded border text-[10px] transition-colors",
        on
          ? "bg-sky-500/10 text-sky-300 border-sky-500/40"
          : "bg-card border-border/60 text-muted-foreground hover:text-foreground hover:bg-accent/40",
      )}
    >
      {children}
    </button>
  )
}
