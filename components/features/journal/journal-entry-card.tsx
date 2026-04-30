"use client"

import { useState } from "react"
import { ChevronDown, ChevronRight, Sparkles, Flag, User } from "lucide-react"
import * as Lucide from "lucide-react"
import Link from "next/link"
import { cn } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { formatRelativeTime } from "@/lib/time"
import { iconForEntryType } from "@/lib/journal-icons"
import { useJournalLookup, type CrewLookup, type AgentLookup, type MissionLookup } from "@/hooks/use-journal-lookup"
import type { JournalEntry } from "@/lib/types/journal"

/** Maps severity → stripe colour. Stripe is the 2-px accent on the left edge. */
const SEVERITY_STRIPE: Record<string, string> = {
  info: "bg-blue-500/70",
  notice: "bg-cyan-500/70",
  warn: "bg-amber-500/80",
  error: "bg-red-500/90",
}

const SEVERITY_PILL: Record<string, string> = {
  info: "bg-blue-500/15 text-blue-300 border-blue-500/30",
  notice: "bg-cyan-500/15 text-cyan-300 border-cyan-500/30",
  warn: "bg-amber-500/15 text-amber-300 border-amber-500/30",
  error: "bg-red-500/15 text-red-300 border-red-500/30",
}

const ACTOR_PILL: Record<string, string> = {
  agent: "bg-violet-500/15 text-violet-300 border-violet-500/30",
  user: "bg-emerald-500/15 text-emerald-300 border-emerald-500/30",
  system: "bg-slate-500/15 text-slate-300 border-slate-500/30",
  keeper: "bg-amber-500/15 text-amber-300 border-amber-500/30",
  sidecar: "bg-cyan-500/15 text-cyan-300 border-cyan-500/30",
  orchestrator: "bg-fuchsia-500/15 text-fuchsia-300 border-fuchsia-500/30",
}

interface JournalEntryCardProps {
  entry: JournalEntry
}

/**
 * One journal row. Handles the common card chrome plus type-specific
 * rendering for summaries, exec commands, and denied Keeper decisions.
 * Unknown types fall back to a neutral card with expandable payload.
 */
export function JournalEntryCard({ entry }: JournalEntryCardProps) {
  const [expanded, setExpanded] = useState(false)
  // Hook calls must come before any early return so React's per-render
  // hook order stays stable.
  const lookup = useJournalLookup()

  const severityKey = typeof entry.severity === "string" ? entry.severity : "info"
  const stripe = SEVERITY_STRIPE[severityKey] ?? SEVERITY_STRIPE.info
  const severityPill = SEVERITY_PILL[severityKey] ?? SEVERITY_PILL.info
  const actorPill = ACTOR_PILL[entry.actor_type] ?? "bg-muted text-muted-foreground border-border"

  // Special case: crew summary entries — gold border + larger treatment.
  if (entry.entry_type === "summary.generated") {
    return <SummaryEntryCard entry={entry} />
  }

  // Keeper denial: red left border, regardless of severity mapping.
  const isDenied = entry.entry_type === "keeper.decision" && isKeeperDenied(entry)
  const borderClass = isDenied
    ? "border-red-500/50"
    : "border-border/50"

  const isExec = entry.entry_type === "exec.command"
  const hasPayload = entry.payload && Object.keys(entry.payload).length > 0
  const TypeIcon = iconForEntryType(entry.entry_type)

  // Pull human-readable names + icons from the workspace lookup. Falls
  // back to id-only rendering when the provider isn't mounted (e.g.
  // when the card is reused outside /journal).
  const crew = entry.crew_id ? lookup.crews.get(entry.crew_id) : undefined
  const agent = entry.agent_id ? lookup.agents.get(entry.agent_id) : undefined
  const mission = entry.mission_id ? lookup.missions.get(entry.mission_id) : undefined

  return (
    <div className={cn("relative rounded-lg border bg-card overflow-hidden transition-colors", borderClass, "hover:border-border")}>
      <div className={cn("absolute inset-y-0 left-0 w-[3px]", stripe)} aria-hidden />
      <div className="pl-4 pr-3 py-2.5">
        <div className="flex items-start gap-2 flex-wrap">
          <Badge variant="outline" className="gap-1 text-[10px] font-mono uppercase tracking-wide border-border/60">
            <TypeIcon className="h-3 w-3 opacity-80" />
            {entry.entry_type}
          </Badge>
          <Badge variant="outline" className={cn("text-[10px] border", severityPill)}>
            {severityKey}
          </Badge>
          <Badge variant="outline" className={cn("text-[10px] border", actorPill)}>
            {entry.actor_type}
            {entry.actor_id && <span className="ml-1 opacity-70 font-mono">{entry.actor_id.slice(0, 6)}</span>}
          </Badge>
          <ContextChips crew={crew} agent={agent} mission={mission} />
          <span className="ml-auto text-[11px] text-muted-foreground font-mono tabular-nums">
            {formatRelativeTime(entry.ts)}
          </span>
        </div>

        <p className="mt-1.5 text-sm text-foreground/90 leading-snug">
          {entry.summary || <span className="text-muted-foreground italic">(no summary)</span>}
        </p>

        {isExec && <ExecCommandDetail entry={entry} />}

        {hasPayload && (
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            aria-expanded={expanded}
            className="mt-1.5 inline-flex items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground transition-colors"
          >
            {expanded ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
            {expanded ? "Hide payload" : "Show payload"}
          </button>
        )}

        {expanded && hasPayload && (
          <pre className="mt-1.5 max-h-64 overflow-auto rounded border border-border/50 bg-muted/30 p-2 text-[10px] font-mono text-muted-foreground">
            {JSON.stringify(entry.payload, null, 2)}
          </pre>
        )}
      </div>
    </div>
  )
}

/**
 * `summary.generated` entries are rendered as hero cards — gold border, larger
 * body, markdown body shown inline. These are the artefacts the product is
 * designed to surface, so they get visual weight over the surrounding feed.
 */
function SummaryEntryCard({ entry }: { entry: JournalEntry }) {
  const body = typeof entry.payload?.body === "string" ? (entry.payload.body as string) : ""
  const title = typeof entry.payload?.title === "string" ? (entry.payload.title as string) : "Crew Summary"
  const [expanded, setExpanded] = useState(body.length < 400)

  return (
    <div className="relative rounded-xl border-2 border-amber-500/60 bg-amber-500/5 overflow-hidden">
      <div className="px-4 py-3">
        <div className="flex items-start gap-2 flex-wrap">
          <Badge className="gap-1 bg-amber-500/20 text-amber-300 border border-amber-500/40">
            <Sparkles className="h-3 w-3" /> Crew Summary
          </Badge>
          <span className="ml-auto text-[11px] text-muted-foreground font-mono tabular-nums">
            {formatRelativeTime(entry.ts)}
          </span>
        </div>
        <h3 className="mt-1.5 text-base font-semibold text-foreground">{title}</h3>
        <p className="mt-1 text-sm text-foreground/90">{entry.summary}</p>
        {body && (
          <>
            <div
              className={cn(
                "mt-2 text-sm text-foreground/85 whitespace-pre-wrap",
                !expanded && "line-clamp-3",
              )}
            >
              {body}
            </div>
            {body.length > 200 && (
              <button
                type="button"
                onClick={() => setExpanded((v) => !v)}
                className="mt-1.5 text-[11px] text-amber-400 hover:text-amber-300 transition-colors"
              >
                {expanded ? "Collapse" : "Read more"}
              </button>
            )}
          </>
        )}
      </div>
    </div>
  )
}

/** Render the `command` and `exit_code` fields for exec.command entries. */
function ExecCommandDetail({ entry }: { entry: JournalEntry }) {
  const cmd = typeof entry.payload?.command === "string" ? (entry.payload.command as string) : ""
  const exit = entry.payload?.exit_code
  const exitNum = typeof exit === "number" ? exit : null
  if (!cmd && exitNum === null) return null
  return (
    <div className="mt-1.5 flex items-start gap-2">
      {cmd && (
        <code className="flex-1 min-w-0 rounded bg-muted/40 border border-border/50 px-2 py-1 font-mono text-[11px] text-foreground/90 break-all">
          $ {cmd}
        </code>
      )}
      {exitNum !== null && (
        <Badge
          variant="outline"
          className={cn(
            "text-[10px] border font-mono",
            exitNum === 0
              ? "bg-emerald-500/15 text-emerald-300 border-emerald-500/40"
              : "bg-red-500/15 text-red-300 border-red-500/40",
          )}
        >
          exit {exitNum}
        </Badge>
      )}
    </div>
  )
}

/** Heuristic: a keeper.decision payload with `decision: "deny"`. */
function isKeeperDenied(entry: JournalEntry): boolean {
  const d = entry.payload?.decision
  if (typeof d !== "string") return false
  return d.toLowerCase() === "deny" || d.toLowerCase() === "denied"
}

// Lightweight palette → tailwind class map for the crew chip border /
// background tint. Mirrors the palette ids from CLAUDE.md (blue,
// emerald, violet, amber, rose, cyan, lime, fuchsia) so the chip
// visually matches the crew row colour the user already sees on
// /crews. Unknown palette → muted slate.
const CREW_CHIP_PALETTE: Record<string, string> = {
  blue: "bg-blue-500/15 text-blue-300 border-blue-500/40",
  emerald: "bg-emerald-500/15 text-emerald-300 border-emerald-500/40",
  violet: "bg-violet-500/15 text-violet-300 border-violet-500/40",
  amber: "bg-amber-500/15 text-amber-300 border-amber-500/40",
  rose: "bg-rose-500/15 text-rose-300 border-rose-500/40",
  cyan: "bg-cyan-500/15 text-cyan-300 border-cyan-500/40",
  lime: "bg-lime-500/15 text-lime-300 border-lime-500/40",
  fuchsia: "bg-fuchsia-500/15 text-fuchsia-300 border-fuchsia-500/40",
}

function crewChipClass(color: string | null | undefined): string {
  return CREW_CHIP_PALETTE[color ?? ""] ?? "bg-slate-500/10 text-slate-300 border-slate-500/30"
}

/**
 * Resolve a lucide icon name (e.g. "code", "rocket") to its component.
 * Crews have a free-form string column for the icon — we coerce the
 * common kebab-case-or-snake_case variants into the PascalCase the
 * lucide-react package exports. Unknown names fall back to undefined
 * (caller renders no icon).
 */
function iconByLucideName(name: string | null | undefined): typeof Flag | undefined {
  if (!name) return undefined
  const pascal = name
    .split(/[-_]/)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join("")
  const lib = Lucide as unknown as Record<string, typeof Flag>
  return lib[pascal] as typeof Flag | undefined
}

/**
 * Render the small chip row that surfaces crew / agent / mission
 * context next to the entry-type badge. All three are optional —
 * lookup miss (no provider, deleted entity, etc.) just hides the chip
 * rather than crashing or rendering raw ids.
 */
function ContextChips({
  crew,
  agent,
  mission,
}: {
  crew: CrewLookup | undefined
  agent: AgentLookup | undefined
  mission: MissionLookup | undefined
}) {
  const CrewIcon = iconByLucideName(crew?.icon)
  return (
    <>
      {crew && (
        <Link
          href={`/crews?crew=${crew.slug}`}
          className={cn(
            "inline-flex items-center gap-1 rounded border px-1.5 py-0.5 text-[10px] hover:opacity-80 transition-opacity",
            crewChipClass(crew.color),
          )}
        >
          {CrewIcon ? <CrewIcon className="h-3 w-3" /> : <Flag className="h-3 w-3 opacity-70" />}
          {crew.name}
        </Link>
      )}
      {agent && (
        <Link
          href={`/crews/agents/${agent.id}`}
          className="inline-flex items-center gap-1 rounded border border-border/60 bg-card px-1.5 py-0.5 text-[10px] text-foreground/80 hover:bg-white/[0.04] transition-colors"
        >
          <User className="h-3 w-3 opacity-60" />
          {agent.name}
        </Link>
      )}
      {mission && (
        <Link
          href={`/missions/${mission.id}`}
          className="inline-flex items-center gap-1 rounded border border-border/60 bg-card px-1.5 py-0.5 text-[10px] text-muted-foreground hover:text-foreground transition-colors max-w-[220px]"
          title={mission.title}
        >
          <Flag className="h-3 w-3 opacity-60" />
          <span className="truncate">{mission.title}</span>
        </Link>
      )}
    </>
  )
}
