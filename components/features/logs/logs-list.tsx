"use client"

import { entityHref } from "@/lib/entity-links"

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
// A static class, not an inline `gridTemplateColumns`: the template is
// one fixed value, so Tailwind can see it at build time.
// Below md the row is two lines — the summary, then time · type · actor —
// in three columns; the seven-column layout only starts at md. A 373px
// fixed grid on a 390px screen showed the timestamps and nothing else.
const ROW_GRID =
  "grid-cols-[3px_minmax(0,1fr)_14px] md:grid-cols-[3px_84px_38px_124px_minmax(0,1fr)_62px_14px]"

/**
 * Glyph stand-ins for the actors that have no avatar because they are
 * not agents. A blank cell here reads as missing data; a labelled glyph
 * reads as "this was the system", which is the truth.
 */
// Module constants, not inline literals: Virtuoso compares these props
// by identity, and a fresh object per render is the same mistake the
// old inline `itemContent` made.
const computeItemKey = (_: number, e: JournalEntry) => e.id
const INCREASE_VIEWPORT_BY = { top: 0, bottom: 600 }

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
  // An agent with a blank name would otherwise match a blank head and
  // eat the prefix of a summary like "  : something".
  if (!head) return summary
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
  /** Detail-row jump: narrow the timeline to this row's issue. */
  onSelectMission?: (missionId: string) => void
}

/**
 * Virtualized log stream rendered Grafana Explore-style:
 *   ┃ sev │ HH:MM:SS.mmm │ avatar+crew │ ◈ entry.type │ summary │ age │ ▸
 * Click a row to expand it inline and reveal payload + refs as
 * formatted JSON. Multiple rows can be open at once.
 */
export function LogsList({ entries, wrap, followTail, newestFirst, onEndReached, onSelectTrace, onSelectAgent, onSelectCrew, onSelectMission }: LogsListProps) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const toggleExpand = useCallback((id: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  // Both subscribed once for the whole list, never per row. React.memo
  // does not stop a context change from re-rendering a component, so a
  // row that read the lookup itself would re-render on every provider
  // update — and the provider flips `loading` on and off around each
  // refetch, so a single `agent.updated` event would sweep the whole
  // mounted list three times whether or not the data changed. Reading
  // the maps here means only a real map swap reaches the rows.
  const { agents, crews, missions } = useJournalLookup()
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
        agent={e.agent_id ? agents.get(e.agent_id) : undefined}
        crew={e.crew_id ? crews.get(e.crew_id) : undefined}
        missionIdentifier={e.mission_id ? missions.get(e.mission_id)?.identifier : undefined}
        stylesVersion={stylesVersion}
        onSelectTrace={onSelectTrace}
        onSelectAgent={onSelectAgent}
        onSelectCrew={onSelectCrew}
        onSelectMission={onSelectMission}
      />
    ),
    [wrap, expanded, toggleExpand, agents, crews, missions, stylesVersion, onSelectTrace, onSelectAgent, onSelectCrew, onSelectMission],
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
      computeItemKey={computeItemKey}
      endReached={onEndReached}
      increaseViewportBy={INCREASE_VIEWPORT_BY}
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
  // Gated on the ACTOR, never on `agent_id` alone. A non-agent actor
  // routinely names an agent — `chat.user_message` (actor `user`),
  // `container.snapshot`, `conversation.compacted` and `sidecar.stale`
  // (actor `system`) all emit with `AgentID` set, because the agent is
  // what the event is *about*. Branching on the id would put the agent's
  // face on a human's message and tell a screen reader the agent said it.
  const name = agent?.name || (agentId ? shortenId(agentId) : undefined)
  if (actorType === "agent" && agentId) {
    // Seed precedence is `avatar_seed || name`, matching ScopeBadge in
    // logs-toolbar.tsx. The two render the same agent on the same screen
    // — the dropdown and the row — so a different rule here would draw
    // two different faces for one agent. Keep them in lockstep.
    const seed = agent?.avatar_seed || agent?.name || agentId
    return (
      <Image
        src={getAgentAvatarUrl(seed, agent?.avatar_style ?? null)}
        alt={name ?? agentId}
        title={name ?? agentId}
        width={18}
        height={18}
        className="h-[18px] w-[18px] rounded-[4px] shrink-0 bg-muted/40"
        unoptimized
      />
    )
  }
  const Glyph = ACTOR_GLYPH[actorType] ?? CircleDashed
  // The agent stays in the label when the entry carries one: a system
  // row about Morgan's container should still say Morgan, just not
  // claim Morgan acted.
  const who = actorType || "unknown"
  const label = name ? `${who} actor, agent ${name}` : `${who} actor`
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
    // A div, not a span: CrewIcon renders a div, which is not phrasing
    // content and cannot legally nest inside one.
    <div role="img" aria-label={label} title={label} className="inline-flex shrink-0">
      <CrewIcon
        icon={crew.icon}
        color={crew.color}
        size="sm"
        className="!h-[15px] !w-[15px] !rounded-[3px] [&>svg]:!h-2.5 [&>svg]:!w-2.5"
      />
    </div>
  )
}

const LogRow = memo(function LogRow({
  entry,
  wrap,
  expanded,
  onToggle,
  agent,
  crew,
  missionIdentifier,
  onSelectTrace,
  onSelectAgent,
  onSelectCrew,
  onSelectMission,
}: {
  entry: JournalEntry
  wrap: boolean
  expanded: boolean
  onToggle: (id: string) => void
  /** Resolved by the list, so the row never subscribes to the lookup. */
  agent: AgentLookup | undefined
  crew: CrewLookup | undefined
  /** ENG-4 for the row's mission, when the lookup knows it — the link back to the issue. */
  missionIdentifier?: string
  /**
   * Bumped when a lazily-imported DiceBear collection finishes loading.
   * Never read here — it exists so this memo boundary invalidates and
   * getAgentAvatarUrl gets a second chance at a real avatar.
   */
  stylesVersion: number
  onSelectTrace?: (traceId: string) => void
  onSelectAgent?: (agentId: string) => void
  onSelectCrew?: (crewId: string) => void
  onSelectMission?: (missionId: string) => void
}) {
  const sev = severityOf(entry.severity)
  const grp = groupOf(entry.entry_type)
  const TypeIcon = iconForEntryType(entry.entry_type)
  const ts = entry.ts
  const tsLabel = formatTime(ts)
  // The prefix is redundant only because the avatar is showing that
  // agent — which it only does for an agent actor.
  const summary =
    entry.actor_type === "agent"
      ? stripAgentPrefix(entry.summary ?? "", agent)
      : entry.summary ?? ""
  const detailId = `log-detail-${entry.id}`
  const toggle = useCallback(() => onToggle(entry.id), [onToggle, entry.id])

  return (
    <div
      onClick={toggle}
      className={cn(
        "group grid gap-2 px-2 py-[3px] items-start cursor-pointer text-[12px] leading-[18px] border-b border-border/30 hover:bg-accent/20",
        ROW_GRID,
        expanded && "bg-primary/5",
      )}
    >
      {/* The bar is the only severity signal on screen; the sr-only
          child is the only one a screen reader gets. `sr-only` is
          position:absolute, so it costs the 3px column no width. */}
      <span className={cn("self-stretch rounded-sm", SEVERITY_BG_CLASS[sev])}>
        <span className="sr-only">Severity: {sev}</span>
      </span>
      <time
        dateTime={ts}
        className="hidden font-mono text-[11px] tabular-nums text-muted-foreground/80 truncate md:block"
      >
        {tsLabel}
      </time>
      {/* A div, not a span: CrewBadge renders a div once the crew has an
          icon, and a div inside a span is invalid — the parser closes the
          span and reparents the div out of this 38px column. */}
      <div className="hidden items-center gap-1 md:flex">
        <ActorAvatar agent={agent} agentId={entry.agent_id} actorType={entry.actor_type} />
        <CrewBadge crew={crew} />
      </div>
      {/* The group colour stays an inline style on purpose. It is
          category data, not theme (BRIEF-COLOR-TOKENS-2026 §2), so it has
          no semantic token; the equivalent static classes would be raw
          palette classes (`text-emerald-400`, …), which eslint.config.mjs
          bans in components/**. GROUP_COLOR is also the single source for
          the same 18 colours in the chips row, stats rail and histogram
          legend, where they are consumed as a background value — a
          parallel class map would be a second copy to keep in sync. */}
      <span className="hidden items-center gap-1 min-w-0 md:flex" title={entry.entry_type}>
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
      {/* The second line below md: what the hidden columns carried. */}
      <span
        className="col-start-2 flex min-w-0 items-center gap-1.5 font-mono text-[10px] text-muted-foreground/70 md:hidden"
        aria-hidden
      >
        <span className="tabular-nums">{tsLabel}</span>
        <span style={{ color: GROUP_COLOR[grp] }} className="truncate">{entry.entry_type}</span>
        {agent?.name && <span className="truncate">· {agent.name}</span>}
        {crew?.name && <span className="truncate">· {crew.name}</span>}
      </span>
      <span className="hidden text-right font-mono text-[10px] tabular-nums text-muted-foreground/70 md:block">
        {formatRelativeTime(ts)}
      </span>
      {/* The disclosure is this button, not the row container. The
          container used to carry role="button" with the detail rendered
          *inside* it, which made the detail's own buttons (the trace /
          agent / crew jumps, the egress allowlist action) nested
          interactive content, and grew the row's accessible name to
          include the whole payload JSON once expanded. A real button
          beside the region it controls is the shape aria-controls
          expects. The row keeps its click handler for the mouse. */}
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls={expanded ? detailId : undefined}
        aria-label={`${expanded ? "Collapse" : "Expand"} detail for ${entry.entry_type} at ${tsLabel}`}
        onClick={(e) => {
          // The container handles the click for mouse users; without
          // this the row would toggle twice and land back where it was.
          e.stopPropagation()
          toggle()
        }}
        className="text-muted-foreground/60 text-[10px] leading-[18px] cursor-pointer"
      >
        {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
      </button>

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
            missionIdentifier={missionIdentifier}
            onSelectTrace={onSelectTrace}
            onSelectAgent={onSelectAgent}
            onSelectCrew={onSelectCrew}
            onSelectMission={onSelectMission}
          />
        </div>
      )}
    </div>
  )
})

function Detail({
  entry,
  sev,
  missionIdentifier,
  onSelectTrace,
  onSelectAgent,
  onSelectCrew,
  onSelectMission,
}: {
  entry: JournalEntry
  sev: ReturnType<typeof severityOf>
  missionIdentifier?: string
  onSelectTrace?: (traceId: string) => void
  onSelectAgent?: (agentId: string) => void
  onSelectCrew?: (crewId: string) => void
  onSelectMission?: (missionId: string) => void
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
    {
      key: "mission_id",
      value: entry.mission_id,
      jump: entry.mission_id && onSelectMission ? () => onSelectMission(entry.mission_id as string) : undefined,
    },
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
      {/* The way back to the issue from its journal — the leg the one timeline
          never had. Only when the lookup knows the identifier; a cuid has no page. */}
      {entry.mission_id && missionIdentifier && (
        <a
          href={entityHref({ kind: "issue", identifier: missionIdentifier })}
          className="inline-flex items-center gap-1 rounded border border-border/60 px-1.5 py-0.5 text-[10px] font-mono text-primary hover:border-primary"
          data-testid="journal-open-issue"
        >
          open issue {missionIdentifier} →
        </a>
      )}
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
