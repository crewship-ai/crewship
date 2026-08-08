"use client"

// Left rail for /activity-new.
//
// Same shape as the Routines rail on purpose: a search box and a Filter
// trigger in the toolbar, then bucket rows you click, then the entity list.
// Secondary facets (time range, source, severity, agent) live INSIDE the
// filter popover rather than sitting permanently expanded — the first draft
// of this page unfolded all five at once and turned the rail into a wall.
//
// SidebarFilterPopover owns the panel, so a pick never closes it and never
// clears a sibling facet (#1776).

import * as React from "react"
import { Activity, CheckCircle2, PauseCircle, ScrollText, XCircle } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import {
  SidebarCollapseButton,
  SidebarFacet,
  SidebarFacetOption,
  SidebarFilterPopover,
  SidebarSearch,
  SidebarSection,
  SidebarRow,
  SidebarToolbar,
} from "@/components/layout/sidebar-kit"
import { StatusIcon } from "@/components/features/issues/status-icon"
import { PriorityIcon } from "@/components/features/issues/priority-icon"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { CrewIcon } from "@/components/ui/crew-icon"
import { resolveRoutineIcon, resolveRoutineColor } from "@/lib/routine-identity"
import { ACTIVITY_SCOPES, ACTIVITY_SOURCES, type ActivityScope, type ActivitySource } from "@/lib/activity-stream"
import { cn } from "@/lib/utils"

/**
 * What the rail is currently pointed at.
 *
 * A focus is a lens on the SAME activity feed, not a different page: pick an
 * issue and the whole surface — cards, chart, counts — narrows to that
 * issue's activity. That is the difference the user asked for between this
 * rail and the ones on /issues and /routines, which navigate to the entity
 * itself.
 */
export interface EntityFocus {
  kind: "issue" | "routine" | "crew"
  id: string
  label: string
}

export interface SidebarIssue {
  id: string
  identifier?: string
  title: string
  status: string
  priority?: string | null
  assignee_id?: string | null
  assignee_name?: string | null
}

export interface SidebarRoutine {
  id: string
  slug: string
  name: string
  icon?: string | null
  color?: string | null
  invocation_count?: number
  last_invocation_status?: string | null
}

export interface SidebarCrew {
  id: string
  name: string
  icon?: string | null
  color?: string | null
}

// Scope icons + tones are lifted verbatim from the Routines rail's STATUS
// bucket list (routines-explorer.tsx:67). Two rails showing "Failed" with a
// different glyph is the kind of near-miss that makes an app feel assembled
// rather than designed.
const SCOPE_ICON: Record<string, { icon: LucideIcon; tone: string }> = {
  all: { icon: ScrollText, tone: "text-foreground/70" },
  waiting: { icon: PauseCircle, tone: "text-warn" },
  active: { icon: Activity, tone: "text-primary" },
  completed: { icon: CheckCircle2, tone: "text-success" },
  done: { icon: CheckCircle2, tone: "text-success" },
  failed: { icon: XCircle, tone: "text-destructive" },
}

export const TIME_RANGES = [
  { key: "1h", label: "Past hour", ms: 60 * 60_000 },
  { key: "24h", label: "Past 24 hours", ms: 24 * 60 * 60_000 },
  { key: "7d", label: "Past 7 days", ms: 7 * 24 * 60 * 60_000 },
  { key: "30d", label: "Past 30 days", ms: 30 * 24 * 60 * 60_000 },
] as const

export type TimeRangeKey = (typeof TIME_RANGES)[number]["key"]

const SEVERITIES = [
  { key: "error", label: "Error", token: "--destructive" },
  { key: "warn", label: "Warning", token: "--warn" },
  { key: "notice", label: "Notice", token: "--notice" },
  { key: "info", label: "Info", token: "--muted-foreground" },
] as const

export interface FacetState {
  scope: ActivityScope | "all"
  sources: ActivitySource[]
  severities: string[]
  crewIDs: string[]
  agentIDs: string[]
  range: TimeRangeKey
  /** Put the per-minute container/exec telemetry back into the feed. */
  showTelemetry: boolean
}

export const EMPTY_FACETS: FacetState = {
  scope: "all",
  sources: [],
  severities: [],
  crewIDs: [],
  agentIDs: [],
  range: "24h",
  showTelemetry: false,
}

function toggle<T>(list: T[], value: T): T[] {
  return list.includes(value) ? list.filter((v) => v !== value) : [...list, value]
}

function Dot({ token, pulse }: { token: string; pulse?: boolean }) {
  return (
    <span
      aria-hidden
      className={cn("h-1.5 w-1.5 shrink-0 rounded-full", pulse && "animate-pulse")}
      style={{ background: `var(${token})` }}
    />
  )
}

function Count({ n, dim }: { n: number; dim?: boolean }) {
  return (
    <span
      className={cn(
        "ml-auto shrink-0 font-mono text-[10px] tabular-nums",
        dim ? "text-muted-foreground-soft" : "text-muted-foreground",
      )}
    >
      {n}
    </span>
  )
}

export interface ActivitySidebarProps {
  search: string
  onSearchChange: (v: string) => void
  facets: FacetState
  onChange: (next: FacetState) => void
  crews: SidebarCrew[]
  agents: { id: string; name: string; crew_id: string | null }[]
  issues: SidebarIssue[]
  routines: SidebarRoutine[]
  scopeCounts: Record<ActivityScope, number>
  crewCounts: Record<string, number>
  issueCounts: Record<string, number>
  routineCounts: Record<string, number>
  focus: EntityFocus | null
  onFocus: (f: EntityFocus | null) => void
  total: number
  onToggleCollapse: () => void
}

export function ActivitySidebar({
  search,
  onSearchChange,
  facets,
  onChange,
  crews,
  agents,
  issues,
  routines,
  scopeCounts,
  crewCounts,
  issueCounts,
  routineCounts,
  focus,
  onFocus,
  total,
  onToggleCollapse,
}: ActivitySidebarProps) {
  const [openSection, setOpenSection] = React.useState<Record<string, boolean>>({
    scope: true,
    crews: true,
    issues: false,
    routines: false,
  })
  const toggleSection = (k: string) =>
    setOpenSection((s) => ({ ...s, [k]: !s[k] }))

  // Only entities that actually appear in the loaded window are listed, and
  // busiest first. A rail listing 38 routines of which 3 have activity is a
  // rail you scroll past — the point here is "where is the activity", not
  // "what exists".
  const activeIssues = React.useMemo(
    () =>
      issues
        .filter((i) => (issueCounts[i.id] ?? 0) > 0)
        .sort((a, b) => (issueCounts[b.id] ?? 0) - (issueCounts[a.id] ?? 0)),
    [issues, issueCounts],
  )
  const activeRoutines = React.useMemo(
    () =>
      routines
        .filter((r) => (routineCounts[r.slug] ?? 0) > 0)
        .sort((a, b) => (routineCounts[b.slug] ?? 0) - (routineCounts[a.slug] ?? 0)),
    [routines, routineCounts],
  )
  const visibleAgents = React.useMemo(
    () =>
      facets.crewIDs.length === 0
        ? agents
        : agents.filter((a) => a.crew_id != null && facets.crewIDs.includes(a.crew_id)),
    [agents, facets.crewIDs],
  )

  const filterCount =
    facets.sources.length +
    facets.severities.length +
    facets.agentIDs.length +
    (facets.range === "24h" ? 0 : 1) +
    (facets.showTelemetry ? 1 : 0)

  return (
    <div className="flex h-full flex-col">
      <SidebarToolbar>
        <SidebarSearch
          value={search}
          onValueChange={onSearchChange}
          placeholder="Search activity…"
          aria-label="Search activity"
        />
        <SidebarFilterPopover
          label="Filter activity"
          activeCount={filterCount}
          onClear={() =>
            onChange({ ...facets, sources: [], severities: [], agentIDs: [], range: "24h" })
          }
        >
          <SidebarFacet
            first
            label="Time range"
            resetLabel="Past 24 hours"
            resetActive={facets.range === "24h"}
            onReset={() => onChange({ ...facets, range: "24h" })}
          >
            {TIME_RANGES.filter((r) => r.key !== "24h").map((r) => (
              <SidebarFacetOption
                key={r.key}
                active={facets.range === r.key}
                onToggle={() => onChange({ ...facets, range: r.key })}
              >
                {r.label}
              </SidebarFacetOption>
            ))}
          </SidebarFacet>

          <SidebarFacet
            label="Source"
            resetLabel="Everything"
            resetActive={facets.sources.length === 0}
            onReset={() => onChange({ ...facets, sources: [] })}
          >
            {ACTIVITY_SOURCES.map((s) => (
              <SidebarFacetOption
                key={s.key}
                active={facets.sources.includes(s.key)}
                onToggle={() => onChange({ ...facets, sources: toggle(facets.sources, s.key) })}
              >
                <Dot token={s.token} />
                <span className="truncate" title={s.hint}>
                  {s.label}
                </span>
              </SidebarFacetOption>
            ))}
          </SidebarFacet>

          <SidebarFacet
            label="Severity"
            resetLabel="Any severity"
            resetActive={facets.severities.length === 0}
            onReset={() => onChange({ ...facets, severities: [] })}
          >
            {SEVERITIES.map((s) => (
              <SidebarFacetOption
                key={s.key}
                active={facets.severities.includes(s.key)}
                onToggle={() => onChange({ ...facets, severities: toggle(facets.severities, s.key) })}
              >
                <Dot token={s.token} />
                {s.label}
              </SidebarFacetOption>
            ))}
          </SidebarFacet>

          <SidebarFacet
            label="Noise"
            resetLabel="Hide system telemetry"
            resetActive={!facets.showTelemetry}
            onReset={() => onChange({ ...facets, showTelemetry: false })}
          >
            <SidebarFacetOption
              active={facets.showTelemetry}
              onToggle={() => onChange({ ...facets, showTelemetry: !facets.showTelemetry })}
            >
              <span className="truncate" title="container.metrics, snapshots, exec output, status pings">
                Show system telemetry
              </span>
            </SidebarFacetOption>
          </SidebarFacet>

          {visibleAgents.length > 0 && (
            <SidebarFacet
              label="Agent"
              resetLabel="All agents"
              resetActive={facets.agentIDs.length === 0}
              onReset={() => onChange({ ...facets, agentIDs: [] })}
            >
              {visibleAgents.map((a) => (
                <SidebarFacetOption
                  key={a.id}
                  active={facets.agentIDs.includes(a.id)}
                  onToggle={() => onChange({ ...facets, agentIDs: toggle(facets.agentIDs, a.id) })}
                >
                  <span className="truncate">{a.name}</span>
                </SidebarFacetOption>
              ))}
            </SidebarFacet>
          )}
        </SidebarFilterPopover>
        <SidebarCollapseButton collapsed={false} onToggle={onToggleCollapse} />
      </SidebarToolbar>

      <div className="min-h-0 flex-1 overflow-y-auto pb-4">
        <SidebarSection
          label="Status"
          count={ACTIVITY_SCOPES.length + 1}
          collapsible
          collapsed={!openSection.scope}
          onToggle={() => toggleSection("scope")}
          className="border-b border-white/[0.06]"
        >
          {[{ key: "all" as const, label: "All activity" }, ...ACTIVITY_SCOPES].map((s) => {
            const conf = SCOPE_ICON[s.key] ?? SCOPE_ICON.all
            const IconComp = conf.icon
            const isSelected = facets.scope === s.key
            const count = s.key === "all" ? total : scopeCounts[s.key as ActivityScope]
            return (
              <SidebarRow
                key={s.key}
                selected={isSelected}
                onSelect={() => onChange({ ...facets, scope: s.key })}
              >
                {/* A bucket holding nothing dims to match — five rows of
                    equal weight, three of them zero, is what makes a column
                    read as a wall. Same rule as the Routines rail. */}
                <IconComp
                  className={cn(
                    "h-3.5 w-3.5 shrink-0",
                    conf.tone,
                    count === 0 && !isSelected && "opacity-40",
                    s.key === "active" && count > 0 && "animate-pulse",
                  )}
                />
                <span
                  className={cn(
                    "flex-1 truncate",
                    count === 0 && !isSelected ? "text-foreground/40" : "text-foreground/80",
                  )}
                >
                  {s.label}
                </span>
                <span
                  className={cn(
                    "rounded-full px-1.5 py-px text-[10px] tabular-nums",
                    count === 0
                      ? "text-muted-foreground-soft/50"
                      : isSelected
                        ? "bg-primary/15 text-primary"
                        : "bg-white/[0.05] text-muted-foreground",
                  )}
                >
                  {count}
                </span>
              </SidebarRow>
            )
          })}
        </SidebarSection>

        {crews.length > 0 && (
          <SidebarSection
            label="Crews"
            count={crews.length}
            collapsible
            collapsed={!openSection.crews}
            onToggle={() => toggleSection("crews")}
          >
            {crews.map((c) => {
              const n = crewCounts[c.id] ?? 0
              return (
                <SidebarRow
                  key={c.id}
                  selected={facets.crewIDs.includes(c.id)}
                  onSelect={() => {
                    const crewIDs = toggle(facets.crewIDs, c.id)
                    // Dropping a crew drops its agents too — otherwise the
                    // filter keeps narrowing on someone no longer listed.
                    const stillVisible = new Set(
                      agents.filter((a) => a.crew_id && crewIDs.includes(a.crew_id)).map((a) => a.id),
                    )
                    onChange({
                      ...facets,
                      crewIDs,
                      agentIDs:
                        crewIDs.length === 0
                          ? facets.agentIDs
                          : facets.agentIDs.filter((id) => stillVisible.has(id)),
                    })
                  }}
                >
                  {/* The crew's own icon + colour, the same derivation the
                      crew pages use — two surfaces drawing one crew
                      differently is worse than drawing none. */}
                  <CrewIcon
                    icon={c.icon ?? "users"}
                    color={c.color}
                    size="sm"
                    className={cn("!h-4 !w-4 !rounded shrink-0", n === 0 && "opacity-40")}
                  />
                  <span className={cn("truncate flex-1", n === 0 && "text-foreground/40")}>{c.name}</span>
                  <Count n={n} dim={n === 0} />
                </SidebarRow>
              )
            })}
          </SidebarSection>
        )}

        {activeIssues.length > 0 && (
          <SidebarSection
            label="Issues"
            count={activeIssues.length}
            collapsible
            collapsed={!openSection.issues}
            onToggle={() => toggleSection("issues")}
          >
            {activeIssues.map((issue) => (
              <SidebarRow
                key={issue.id}
                selected={focus?.kind === "issue" && focus.id === issue.id}
                onSelect={() =>
                  onFocus(
                    focus?.kind === "issue" && focus.id === issue.id
                      ? null
                      : {
                          kind: "issue",
                          id: issue.id,
                          label: issue.identifier || issue.title,
                        },
                  )
                }
              >
                <span className="relative shrink-0">
                  <StatusIcon status={issue.status} className="h-3.5 w-3.5" />
                  {issue.status === "IN_PROGRESS" && (
                    <span className="absolute -right-0.5 -top-0.5 h-1.5 w-1.5 rounded-full bg-success agent-active-dot" />
                  )}
                </span>
                <span className="w-[44px] shrink-0 truncate font-mono text-[10px] text-foreground/50">
                  {issue.identifier || "--"}
                </span>
                <span className="flex-1 truncate text-foreground/80">{issue.title}</span>
                {issue.assignee_id && (
                  <AgentAvatar
                    seed={issue.assignee_id}
                    alt={issue.assignee_name || ""}
                    className="h-4 w-4 shrink-0 rounded-full"
                  />
                )}
                <PriorityIcon priority={(issue.priority || "none") as never} className="h-3 w-3 shrink-0" />
                <Count n={issueCounts[issue.id] ?? 0} />
              </SidebarRow>
            ))}
          </SidebarSection>
        )}

        {activeRoutines.length > 0 && (
          <SidebarSection
            label="Routines"
            count={activeRoutines.length}
            collapsible
            collapsed={!openSection.routines}
            onToggle={() => toggleSection("routines")}
          >
            {activeRoutines.map((r) => {
              const last = r.last_invocation_status?.toLowerCase()
              const dot =
                last === "completed"
                  ? "bg-success"
                  : last === "failed"
                    ? "bg-destructive"
                    : last === "running"
                      ? "bg-primary animate-pulse"
                      : "bg-muted-foreground/30"
              return (
                <SidebarRow
                  key={r.id}
                  selected={focus?.kind === "routine" && focus.id === r.slug}
                  onSelect={() =>
                    onFocus(
                      focus?.kind === "routine" && focus.id === r.slug
                        ? null
                        : { kind: "routine", id: r.slug, label: r.name },
                    )
                  }
                >
                  {/* Same icon + colour derivation as the routines rail and
                      the routine detail header. */}
                  <span className="relative shrink-0">
                    <CrewIcon
                      icon={resolveRoutineIcon(r as never)}
                      color={resolveRoutineColor(r as never)}
                      size="sm"
                      className="!h-4 !w-4 !rounded shrink-0"
                    />
                    <span
                      aria-hidden
                      className={cn("absolute -bottom-0.5 -right-0.5 h-1.5 w-1.5 rounded-full ring-2 ring-card", dot)}
                    />
                  </span>
                  <span className="flex-1 truncate text-foreground/80">{r.name}</span>
                  <Count n={routineCounts[r.slug] ?? 0} />
                </SidebarRow>
              )
            })}
          </SidebarSection>
        )}
      </div>
    </div>
  )
}
