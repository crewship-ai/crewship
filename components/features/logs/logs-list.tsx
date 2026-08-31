"use client"

import { memo, useState, useCallback } from "react"
import Image from "next/image"
import { Virtuoso } from "react-virtuoso"
import {
  Bot,
  ChevronRight,
  ChevronDown,
  CircleDashed,
  Container,
  Server,
  ShieldCheck,
  User,
  Workflow,
  type LucideIcon,
} from "lucide-react"
import { cn } from "@/lib/utils"
import type { JournalEntry } from "@/lib/types/journal"
import {
  GROUP_COLOR,
  GROUP_LABEL,
  SEVERITY_BG_CLASS,
  SEVERITY_COLOR,
  groupOf,
  severityOf,
} from "@/lib/journal-style"
import { iconForEntryType } from "@/lib/journal-icons"
import { CrewIcon } from "@/components/ui/crew-icon"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { useAvatarStylesVersion } from "@/hooks/use-avatar-styles"
import {
  useJournalLookup,
  type AgentLookup,
  type CrewLookup,
} from "@/hooks/use-journal-lookup"
import { formatRelativeTime } from "@/lib/time"
import { EgressAllowlistAction } from "./egress-allowlist-action"
import { shortenId } from "./ids"

/**
 * Row grid, left to right:
 *
 *   3px   severity bar
 *   84px  HH:MM:SS.mmm      (11px mono, 12 chars ≈ 79px)
 *   38px  identity cluster  (18px avatar + 4px + 15px crew icon = 37px)
 *   124px entry type        (12px icon + 4px + ~18 chars of 10px mono)
 *   1fr   summary
 *   62px  relative age      ("123d ago" ≈ 48px at 10px mono)
 *   14px  chevron
 *
 * Fixed columns plus six 8px gaps come to 373px, against 333px for the
 * pre-#2208 six-column row — the identity cluster and the wider type
 * cell cost the summary 40px. Avatar and crew share one column rather
 * than taking one each (the wireframe's 20px + 22px): both boxes are
 * always painted, placeholder included, so they still line up down the
 * list, and merging them saves a whole 8px grid gap.
 *
 * The full dotted `entry_type` does not always fit — the catalog's
 * median is 18 characters but its tail runs to 41
 * (`pipeline.schedule.circuit_breaker_tripped`). Long types ellipsize;
 * the untruncated string is on the cell's `title` and in the expanded
 * detail. Buying the tail would cost the summary another ~90px, which
 * is not a trade this row can afford.
 */
const ROW_GRID = "3px 84px 38px 124px minmax(0,1fr) 62px 14px"

/**
 * Glyph stand-ins for the actors that have no avatar because they are
 * not agents. A blank cell here reads as missing data; a labelled glyph
 * reads as "this was the system", which is the truth.
 */
const ACTOR_GLYPH: Record<string, LucideIcon> = {
  agent: Bot,
  user: User,
  system: Server,
  keeper: ShieldCheck,
  sidecar: Container,
  orchestrator: Workflow,
}

/**
 * Strip a leading `"<agent>: "` from a summary when the prefix names the
 * very agent whose avatar now sits on the same row. Emit sites write it
 * inconsistently, so this only fires on an exact match against the
 * resolved agent's name or slug — never on a generic `"word: "`, which
 * would eat `"exec.command: ..."` and every message with a colon in it.
 */
function stripAgentPrefix(summary: string, agent: AgentLookup | undefined): string {
  if (!summary || !agent) return summary
  const idx = summary.indexOf(":")
  if (idx <= 0) return summary
  const head = summary.slice(0, idx).trim().toLowerCase()
  if (head !== (agent.name ?? "").toLowerCase() && head !== (agent.slug ?? "").toLowerCase()) {
    return summary
  }
  const rest = summary.slice(idx + 1).trimStart()
  // Never blank the row: a summary that is nothing but the prefix keeps
  // what it had.
  return rest || summary
}

interface LogsListProps {
  entries: JournalEntry[]
  wrap: boolean
  /** When true, autoscroll sticks to the bottom (or top, depending on order). */
  followTail: boolean
  newestFirst: boolean
  /** Called when the user scrolls within `endReachedThreshold` of the bottom. */
  onEndReached?: () => void
  /**
   * Detail-row click handlers. When provided, the corresponding ID
   * appears as a clickable button in the expanded detail so the user
   * can jump from "I see this entry" → "show me everything in this
   * run / from this agent / for this crew" without leaving the page.
   * Omitted handlers degrade silently to plain text.
   */
  onSelectTrace?: (traceId: string) => void
  onSelectAgent?: (agentId: string) => void
  onSelectCrew?: (crewId: string) => void
}

/**
 * Virtualized log stream rendered Grafana Explore-style:
 *   ┃ sev │ HH:MM:SS.mmm │ avatar+crew │ ◈ entry.type │ summary │ age │ ▸
 * Click a row to expand it inline and reveal payload + refs as
 * formatted JSON. Multiple rows can be open at once.
 */
export function LogsList({ entries, wrap, followTail, newestFirst, onEndReached, onSelectTrace, onSelectAgent, onSelectCrew }: LogsListProps) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const toggleExpand = useCallback((id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  // Subscribed once for the whole list rather than once per row: the
  // rows are memoized, so the version travels down as a prop and is
  // what re-renders them when a lazily-imported DiceBear collection
  // lands and the placeholder discs can become real avatars.
  const stylesVersion = useAvatarStylesVersion()

  // A fresh closure per item per render defeats React.memo on LogRow —
  // every row would re-render on every keystroke in the search box.
  // `expanded` is in the dep list (a new Set each toggle), but only the
  // toggled row sees a changed prop, so the rest still bail out.
  const itemContent = useCallback(
    (_: number, e: JournalEntry) => (
      <LogRow
        entry={e}
        wrap={wrap}
        expanded={expanded.has(e.id)}
        onToggle={toggleExpand}
        stylesVersion={stylesVersion}
        onSelectTrace={onSelectTrace}
        onSelectAgent={onSelectAgent}
        onSelectCrew={onSelectCrew}
      />
    ),
    [wrap, expanded, toggleExpand, stylesVersion, onSelectTrace, onSelectAgent, onSelectCrew],
  )

  if (entries.length === 0) {
    return (
      <div className="h-full flex items-center justify-center text-[12px] text-muted-foreground/60 italic">
        No log entries match the current filter.
      </div>
    )
  }

  const followOutput = followTail ? (newestFirst ? false : "auto") : false

  return (
    <Virtuoso
      data={entries}
      followOutput={followOutput as false | "auto"}
      defaultItemHeight={26}
      computeItemKey={(_, e) => e.id}
      endReached={onEndReached}
      increaseViewportBy={{ top: 0, bottom: 600 }}
      itemContent={itemContent}
      className="h-full"
    />
  )
}

/**
 * 18px agent avatar, or a labelled glyph for the actors that are not
 * agents. Mirrors `ScopeBadge` in logs-toolbar.tsx — same seed rules,
 * same fallback discipline — at row scale.
 *
 * An entry whose agent is not in the lookup (deleted agent, lookup
 * still in flight) still gets a stable avatar: the id is a perfectly
 * good DiceBear seed, so the row never flickers between a blank and a
 * face.
 */
function ActorAvatar({
  agent,
  agentId,
  actorType,
}: {
  agent: AgentLookup | undefined
  agentId: string | undefined
  actorType: string
}) {
  if (agentId) {
    const name = agent?.name ?? shortenId(agentId)
    const seed = agent?.avatar_seed || agent?.slug || agentId
    return (
      <Image
        src={getAgentAvatarUrl(seed, agent?.avatar_style ?? null)}
        alt={name}
        title={name}
        width={18}
        height={18}
        className="h-[18px] w-[18px] rounded-[4px] shrink-0 bg-muted/40"
        unoptimized
      />
    )
  }
  const Glyph = ACTOR_GLYPH[actorType] ?? CircleDashed
  const label = `${actorType || "unknown"} actor`
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      className="h-[18px] w-[18px] rounded-[4px] shrink-0 inline-flex items-center justify-center bg-muted/40 border border-border/50"
    >
      <Glyph className="h-2.5 w-2.5 text-muted-foreground" />
    </span>
  )
}

/**
 * 15px crew gradient, via the same shared CrewIcon the crew dropdown
 * uses. An entry with no crew still paints an empty box so the type
 * column below it stays on one vertical line down the list.
 */
function CrewBadge({ crew }: { crew: CrewLookup | undefined }) {
  if (!crew?.icon) {
    return <span aria-hidden className="h-[15px] w-[15px] rounded-[3px] shrink-0 bg-muted/20" />
  }
  const label = `Crew ${crew.name}`
  return (
    <span role="img" aria-label={label} title={label} className="inline-flex shrink-0">
      <CrewIcon
        icon={crew.icon}
        color={crew.color}
        size="sm"
        className="!h-[15px] !w-[15px] !rounded-[3px] [&>svg]:!h-2.5 [&>svg]:!w-2.5"
      />
    </span>
  )
}

const LogRow = memo(function LogRow({
  entry,
  wrap,
  expanded,
  onToggle,
  onSelectTrace,
  onSelectAgent,
  onSelectCrew,
}: {
  entry: JournalEntry
  wrap: boolean
  expanded: boolean
  onToggle: (id: string) => void
  /**
   * Bumped when a lazily-imported DiceBear collection finishes loading.
   * Never read here — it exists so this memo boundary invalidates and
   * getAgentAvatarUrl gets a second chance at a real avatar.
   */
  stylesVersion: number
  onSelectTrace?: (traceId: string) => void
  onSelectAgent?: (agentId: string) => void
  onSelectCrew?: (crewId: string) => void
}) {
  const lookup = useJournalLookup()
  const sev = severityOf(entry.severity)
  const grp = groupOf(entry.entry_type)
  const TypeIcon = iconForEntryType(entry.entry_type)
  const ts = entry.ts
  const tsLabel = formatTime(ts)
  const agent = entry.agent_id ? lookup.agents.get(entry.agent_id) : undefined
  const crew = entry.crew_id ? lookup.crews.get(entry.crew_id) : undefined
  const summary = stripAgentPrefix(entry.summary ?? "", agent)
  const detailId = `log-detail-${entry.id}`
  const toggle = useCallback(() => onToggle(entry.id), [onToggle, entry.id])

  return (
    <div
      onClick={toggle}
      role="button"
      tabIndex={0}
      aria-expanded={expanded}
      aria-controls={expanded ? detailId : undefined}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          toggle()
        }
      }}
      className={cn(
        "group grid gap-2 px-2 py-[3px] items-start cursor-pointer text-[12px] leading-[18px] border-b border-border/30 hover:bg-accent/20",
        expanded && "bg-primary/5",
      )}
      style={{ gridTemplateColumns: ROW_GRID }}
    >
      {/* The bar is the only severity signal on screen; the sr-only
          child is the only one a screen reader gets. `sr-only` is
          position:absolute, so it costs the 3px column no width. */}
      <span className={cn("self-stretch rounded-sm", SEVERITY_BG_CLASS[sev])}>
        <span className="sr-only">Severity: {sev}</span>
      </span>
      <time
        dateTime={ts}
        className="font-mono text-[11px] tabular-nums text-muted-foreground/80 truncate"
      >
        {tsLabel}
      </time>
      <span className="flex items-center gap-1">
        <ActorAvatar agent={agent} agentId={entry.agent_id} actorType={entry.actor_type} />
        <CrewBadge crew={crew} />
      </span>
      <span className="flex items-center gap-1 min-w-0" title={entry.entry_type}>
        <TypeIcon className="h-3 w-3 shrink-0" style={{ color: GROUP_COLOR[grp] }} />
        {/* Group membership is otherwise carried by colour alone. */}
        <span className="sr-only">{GROUP_LABEL[grp]} group</span>
        <span className="font-mono text-[10px] truncate" style={{ color: GROUP_COLOR[grp] }}>
          {entry.entry_type}
        </span>
      </span>
      <span
        className={cn(
          "font-mono text-foreground/90 min-w-0",
          wrap ? "whitespace-pre-wrap break-words" : "truncate whitespace-nowrap",
        )}
      >
        {summary || "—"}
      </span>
      <span className="text-right font-mono text-[10px] tabular-nums text-muted-foreground/70">
        {formatRelativeTime(ts)}
      </span>
      <span className="text-muted-foreground/60 text-[10px] leading-[18px]">
        {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
      </span>

      {expanded && (
        <div
          id={detailId}
          className="col-start-2 col-end-8 mt-1 mb-1 rounded border border-border/60 bg-card/60"
          // Stop click bubbling so interacting with the detail (selecting
          // text in the JSON, clicking trace/agent/crew jump buttons)
          // doesn't collapse the row.
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
          role="region"
          aria-label="Entry detail"
        >
          <Detail
            entry={entry}
            sev={sev}
            onSelectTrace={onSelectTrace}
            onSelectAgent={onSelectAgent}
            onSelectCrew={onSelectCrew}
          />
        </div>
      )}
    </div>
  )
})

function Detail({
  entry,
  sev,
  onSelectTrace,
  onSelectAgent,
  onSelectCrew,
}: {
  entry: JournalEntry
  sev: ReturnType<typeof severityOf>
  onSelectTrace?: (traceId: string) => void
  onSelectAgent?: (agentId: string) => void
  onSelectCrew?: (crewId: string) => void
}) {
  // Each meta row carries an optional jump handler. Fields with a
  // handler render as a button (Filter to this trace / agent / crew);
  // fields without one render as plain text. Severity gets its
  // canonical color even when not interactive.
  type Row = { key: string; value: string | undefined; jump?: () => void; tone?: string }
  const meta: Row[] = [
    { key: "entry_type", value: entry.entry_type },
    { key: "severity", value: entry.severity as string, tone: SEVERITY_COLOR[sev] },
    { key: "actor_type", value: entry.actor_type },
    { key: "actor_id", value: entry.actor_id },
    {
      key: "agent_id",
      value: entry.agent_id,
      jump: entry.agent_id && onSelectAgent ? () => onSelectAgent(entry.agent_id as string) : undefined,
    },
    {
      key: "crew_id",
      value: entry.crew_id,
      jump: entry.crew_id && onSelectCrew ? () => onSelectCrew(entry.crew_id as string) : undefined,
    },
    { key: "mission_id", value: entry.mission_id },
    {
      key: "trace_id",
      value: entry.trace_id,
      jump: entry.trace_id && onSelectTrace ? () => onSelectTrace(entry.trace_id as string) : undefined,
    },
  ]
  return (
    <div className="p-2 space-y-2">
      <div className="flex flex-wrap gap-x-3 gap-y-1 text-[10px] font-mono">
        {meta.map(({ key, value, jump, tone }) =>
          value ? (
            <span key={key} className="text-muted-foreground">
              <span className="opacity-60">{key}:</span>{" "}
              {jump ? (
                <button
                  type="button"
                  onClick={jump}
                  className="text-primary hover:text-primary/80 hover:underline underline-offset-2 transition-colors"
                  title={`Filter timeline to this ${key.replace("_id", "")}`}
                >
                  {value}
                </button>
              ) : (
                <span className="text-foreground/85" style={tone ? { color: tone } : undefined}>
                  {value}
                </span>
              )}
            </span>
          ) : null,
        )}
      </div>
      {/* #1377 — a blocked-egress row carries its own remediation: add the
          denied host to the crew allowlist without leaving the timeline.
          Renders nothing for every other entry type. */}
      <EgressAllowlistAction entry={entry} />
      {entry.payload && Object.keys(entry.payload).length > 0 && (
        <DetailJson title="payload" value={entry.payload} />
      )}
      {entry.refs && Object.keys(entry.refs).length > 0 && (
        <DetailJson title="refs" value={entry.refs} />
      )}
    </div>
  )
}

function DetailJson({ title, value }: { title: string; value: unknown }) {
  let text: string
  try {
    text = JSON.stringify(value, null, 2)
  } catch {
    text = String(value)
  }
  return (
    <div>
      <div className="text-[10px] uppercase tracking-wider text-muted-foreground mb-0.5">{title}</div>
      <pre className="text-[11px] font-mono text-foreground/85 bg-background/60 border border-border/40 rounded p-2 overflow-x-auto whitespace-pre">
        {text}
      </pre>
    </div>
  )
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  const hh = String(d.getHours()).padStart(2, "0")
  const mm = String(d.getMinutes()).padStart(2, "0")
  const ss = String(d.getSeconds()).padStart(2, "0")
  const ms = String(d.getMilliseconds()).padStart(3, "0")
  return `${hh}:${mm}:${ss}.${ms}`
}
