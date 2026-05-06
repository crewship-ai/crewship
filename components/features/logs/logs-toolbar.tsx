"use client"

import Image from "next/image"
import { AnimatePresence, motion } from "motion/react"
import { Search, Pause, Play, WrapText, ArrowDownUp, Filter, Download, RefreshCw, Users, User } from "lucide-react"
import { cn } from "@/lib/utils"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { CrewIcon } from "@/components/ui/crew-icon"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { SEVERITY_COLOR } from "@/lib/journal-style"
import type { JournalSeverity } from "@/lib/types/journal"
import { TimeRangePicker, type TimeRange, type CustomRange } from "./time-range-picker"
import { RefreshRatePicker, type RefreshRate } from "./refresh-rate-picker"
import type { BucketRange } from "./logs-histogram"

export type SeverityFilter = "all" | JournalSeverity

export interface SeverityCounts {
  all: number
  info: number
  notice: number
  warn: number
  error: number
}

export interface ScopeOption {
  id: string
  name: string
  /** Crew icon slug (e.g. "ship", "binoculars"). Render via CrewIcon. */
  icon?: string | null
  /** Crew gradient palette key. */
  color?: string | null
  /** Agent dicebear seed — falls back to name. */
  avatarSeed?: string | null
  /** Agent dicebear style — falls back to crew/default. */
  avatarStyle?: string | null
}

export interface ScopeControl {
  /** Empty string = "all". */
  value: string
  options: ScopeOption[]
  onChange: (id: string) => void
}

interface LogsToolbarProps {
  query: string
  onQueryChange: (q: string) => void
  /** Imperative focus handle so the panel can wire `/` shortcut. */
  searchInputRef?: React.RefObject<HTMLInputElement | null>
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

  // Optional time range — when provided renders a picker in the toolbar.
  timeRange?: TimeRange
  onTimeRangeChange?: (r: TimeRange) => void
  customRange?: CustomRange | null
  onCustomRangeChange?: (r: CustomRange) => void

  // Optional scope selectors — when provided renders selects in the toolbar.
  crewScope?: ScopeControl
  agentScope?: ScopeControl

  // Optional refresh button — shows spinner while `loading`.
  onRefresh?: () => void
  loading?: boolean

  /**
   * Currently-active histogram bucket selection. When set, the toolbar
   * renders a clear pill so the user knows their list is narrowed
   * client-side beyond the time-range filter.
   */
  bucketFilter?: BucketRange | null
  onClearBucketFilter?: () => void

  /** Refresh-rate cadence (e.g. "live", "10s"). Renders a picker when provided. */
  refreshRate?: RefreshRate
  onRefreshRateChange?: (r: RefreshRate) => void
}

const SEV_ORDER: SeverityFilter[] = ["all", "info", "notice", "warn", "error"]

export function LogsToolbar({
  query,
  onQueryChange,
  searchInputRef,
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
  timeRange,
  onTimeRangeChange,
  customRange,
  onCustomRangeChange,
  crewScope,
  agentScope,
  onRefresh,
  loading,
  bucketFilter,
  onClearBucketFilter,
  refreshRate,
  onRefreshRateChange,
}: LogsToolbarProps) {
  return (
    <div className="px-3 py-2 border-b border-border/50 bg-card/40 flex flex-wrap items-center gap-2 sticky top-0 z-10 backdrop-blur supports-[backdrop-filter]:bg-card/70">
      {/* search */}
      <div className="relative flex-1 min-w-[240px] max-w-[420px]">
        <Search className="absolute left-2 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" />
        <Input
          ref={searchInputRef}
          value={query}
          onChange={(e) => onQueryChange(e.target.value)}
          placeholder="Search…"
          className="h-7 pl-7 pr-6 text-[12px] font-mono"
        />
        {query && (
          <button
            type="button"
            onClick={() => onQueryChange("")}
            className="absolute right-2 top-1/2 -translate-y-1/2 text-[11px] text-muted-foreground hover:text-foreground font-mono"
            aria-label="Clear search"
          >
            ✕
          </button>
        )}
      </div>

      {/* time range */}
      {timeRange && onTimeRangeChange && (
        <TimeRangePicker
          value={timeRange}
          onChange={onTimeRangeChange}
          customRange={customRange}
          onCustomRangeChange={onCustomRangeChange}
        />
      )}

      {/* refresh cadence */}
      {refreshRate && onRefreshRateChange && (
        <RefreshRatePicker value={refreshRate} onChange={onRefreshRateChange} />
      )}

      {/* scope: crew */}
      {crewScope && (
        <ScopeSelect
          control={crewScope}
          allLabel="All crews"
          fallbackIcon={Users}
          aria="Crew scope"
          kind="crew"
        />
      )}

      {/* scope: agent */}
      {agentScope && (
        <ScopeSelect
          control={agentScope}
          allLabel={crewScope?.value ? "All in crew" : "All agents"}
          fallbackIcon={User}
          aria="Agent scope"
          kind="agent"
        />
      )}

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
                "h-7 px-2 text-[10px] font-mono uppercase tracking-wider flex items-center gap-1 transition-colors border-r border-border/60 last:border-r-0",
                active
                  ? "bg-primary/15 text-primary"
                  : "text-muted-foreground hover:text-foreground",
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

      <AnimatePresence>
        {bucketFilter && onClearBucketFilter && (
          <motion.button
            key="bucket-pill"
            type="button"
            onClick={onClearBucketFilter}
            initial={{ opacity: 0, scale: 0.85, y: -2 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.85, y: -2 }}
            transition={{ type: "spring", damping: 18, stiffness: 320 }}
            className="inline-flex items-center gap-1 h-6 px-2 rounded border border-sky-500/40 bg-sky-500/10 text-[10px] font-mono text-sky-300 hover:bg-sky-500/20"
            title="Clear histogram selection"
          >
            <Filter className="h-3 w-3" />
            {fmtBucketLabel(bucketFilter)}
            <span className="text-base leading-none">×</span>
          </motion.button>
        )}
      </AnimatePresence>

      <span className="inline-flex items-center gap-1.5 px-2 h-6 rounded border border-border/60 bg-card text-[10px] font-mono">
        <span className="text-muted-foreground">visible</span>
        <span className="text-foreground tabular-nums">{visibleCount.toLocaleString()}</span>
        <span className="text-muted-foreground">/ {totalCount.toLocaleString()}</span>
      </span>

      {onRefresh && (
        <button
          type="button"
          onClick={onRefresh}
          disabled={loading}
          className="inline-flex items-center gap-1 h-6 px-2 rounded border border-border/60 bg-card text-[10px] text-muted-foreground hover:text-foreground hover:bg-accent/40 disabled:opacity-50 disabled:cursor-not-allowed"
          title="Refresh from server"
          aria-label="Refresh"
        >
          <RefreshCw className={cn("h-3 w-3", loading && "animate-spin")} />
        </button>
      )}

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

function ScopeSelect({
  control,
  allLabel,
  fallbackIcon: FallbackIcon,
  aria,
  kind,
}: {
  control: ScopeControl
  allLabel: string
  fallbackIcon: React.ComponentType<{ className?: string }>
  aria: string
  kind: "crew" | "agent"
}) {
  const selected = control.value
    ? control.options.find((o) => o.id === control.value) ?? null
    : null

  return (
    <Select
      value={control.value || "__all__"}
      onValueChange={(v) => control.onChange(v === "__all__" ? "" : v)}
    >
      <SelectTrigger
        size="sm"
        className="h-7 px-2 text-[11px] gap-1.5 max-w-[180px] [&>svg]:opacity-60"
        aria-label={aria}
      >
        {selected ? (
          <ScopeBadge option={selected} kind={kind} />
        ) : (
          <FallbackIcon className="h-3 w-3 opacity-70 shrink-0" />
        )}
        <SelectValue>
          {selected?.name ?? allLabel}
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="__all__" className="text-xs">
          <span className="inline-flex items-center gap-2">
            <FallbackIcon className="h-3 w-3 opacity-60" />
            {allLabel}
          </span>
        </SelectItem>
        {control.options.map((o) => (
          <SelectItem key={o.id} value={o.id} className="text-xs">
            <span className="inline-flex items-center gap-2 min-w-0">
              <ScopeBadge option={o} kind={kind} />
              <span className="truncate">{o.name}</span>
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/**
 * 16-px badge rendered next to the scope option name. Crews get the
 * gradient `CrewIcon`; agents get a DiceBear avatar; missing data
 * falls back to a neutral monogram so the dropdown never looks empty.
 */
function ScopeBadge({ option, kind }: { option: ScopeOption; kind: "crew" | "agent" }) {
  if (kind === "crew" && option.icon) {
    return (
      <span className="inline-flex items-center justify-center shrink-0 [&>div]:!h-4 [&>div]:!w-4 [&>div]:!rounded">
        <CrewIcon icon={option.icon} color={option.color} size="sm" className="!h-4 !w-4 !rounded [&>svg]:!h-2.5 [&>svg]:!w-2.5" />
      </span>
    )
  }
  if (kind === "agent") {
    const seed = option.avatarSeed || option.name
    const url = getAgentAvatarUrl(seed, option.avatarStyle ?? null)
    return (
      <Image
        src={url}
        alt=""
        width={16}
        height={16}
        className="h-4 w-4 rounded shrink-0 bg-muted/40"
        unoptimized
      />
    )
  }
  return (
    <span className="h-4 w-4 rounded inline-flex items-center justify-center bg-muted/40 text-[9px] font-mono text-muted-foreground shrink-0">
      {option.name.slice(0, 1).toUpperCase()}
    </span>
  )
}

function fmtBucketLabel(b: BucketRange): string {
  const f = new Date(b.fromMs)
  const t = new Date(b.toMs)
  const sameDay =
    f.getFullYear() === t.getFullYear() &&
    f.getMonth() === t.getMonth() &&
    f.getDate() === t.getDate()
  const fmtTime = (d: Date) =>
    `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
  const fmtDay = (d: Date) =>
    `${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`
  return sameDay
    ? `${fmtTime(f)}–${fmtTime(t)}`
    : `${fmtDay(f)} ${fmtTime(f)} → ${fmtDay(t)} ${fmtTime(t)}`
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
