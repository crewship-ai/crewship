"use client"

import { memo } from "react"
import Link from "next/link"
import { Handle, Position, type Node, type NodeProps, type NodeTypes } from "@xyflow/react"
import {
  AlertTriangle,
  CircleDot,
  Inbox,
  MessageSquare,
  PauseCircle,
  ScrollText,
  ShieldAlert,
  Workflow,
  Zap,
} from "lucide-react"
import { cn } from "@/lib/utils"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { statusIcon, statusTint } from "@/lib/activity/run-status"
import { relTime } from "@/lib/time"
import { routineHref } from "@/lib/routine-href"
import type {
  OverviewAgentNodeData,
  OverviewAutomationNodeData,
  OverviewInboxNodeData,
  OverviewIssueNodeData,
  OverviewNodeType,
  OverviewRoutineNodeData,
  OverviewRunNodeData,
} from "@/lib/trace/build-overview-graph"

// The React Flow custom nodes for the /activity overview canvas — one
// per link in the cross-feature chain:
//
//   automation ─▶ ┐
//                 ├─▶ routine ─▶ run ─▶ agent
//   issue      ─▶ ┘                 └─▶ inbox
//
//   - OverviewAutomationNode : the rule that fired
//   - OverviewIssueNode      : a Mission/Issue card
//   - OverviewRoutineNode    : a saved pipeline card
//   - OverviewRunNode        : the latest run for that pipeline
//   - OverviewAgentNode      : the agent that executed the run
//   - OverviewInboxNode      : what the run left for a human
//
// Click handlers are NOT on the nodes — the canvas-level onNodeClick
// dispatches based on node id prefix (iss: / rt: / run: / auto: /
// agt: / ibx:). This keeps the components dumb and avoids passing
// callbacks via the data payload. Nodes that have a canonical
// destination (routine, agent, inbox) wrap in a <Link> and stop the
// click from also reaching the canvas.
//
// Colour is `globals.css` tokens only. No literal ever — the palette
// is owned there, and a literal is a colour that stops tracking the
// theme the first time either changes.

// activateOnEnterOrSpace dispatches a click on the wrapper when
// Enter or Space is pressed, gated to events that originated from
// the wrapper itself (so a key press inside an inner button doesn't
// also re-activate the parent). Mirrors the same pattern in
// trace-step-node.tsx so canvas-wide keyboard semantics stay
// consistent.
function activateOnEnterOrSpace(e: React.KeyboardEvent<HTMLElement>) {
  if (e.target !== e.currentTarget) return
  if (e.key === "Enter" || e.key === " ") {
    e.preventDefault()
    ;(e.currentTarget as HTMLElement).click()
  }
}

function IssueNodeBase({ data: d }: NodeProps<Node<OverviewIssueNodeData>>) {
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`Issue ${d.identifier}: ${d.title}`}
      onKeyDown={activateOnEnterOrSpace}
      className="relative w-[200px] rounded-lg border border-blue-500/25 bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/80"
    >
      <Handle type="source" position={Position.Right} className="!h-2 !w-2 !border-0 !bg-blue-400/40" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <CircleDot aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-blue-300" />
        <span className="truncate font-mono text-[10px] text-blue-300">{d.identifier}</span>
        <StatusChip status={d.status} />
      </div>
      <div className="mt-1 truncate text-xs text-foreground/90" title={d.title}>
        {d.title}
      </div>
    </div>
  )
}
export const OverviewIssueNode = memo(IssueNodeBase)

function RoutineNodeBase({ data: d }: NodeProps<Node<OverviewRoutineNodeData>>) {
  // Native <Link> = native anchor semantics. Enter activates it for
  // free; adding role="button" would have stomped that.
  return (
    <Link
      href={routineHref(d.slug)}
      aria-label={`Routine ${d.name}`}
      className="relative block w-[200px] rounded-lg border border-purple/25 bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-purple/80"
      onClick={(e) => e.stopPropagation()}
    >
      <Handle type="target" position={Position.Left} className="!h-2 !w-2 !border-0 !bg-purple/40" isConnectable={false} />
      <Handle type="source" position={Position.Right} className="!h-2 !w-2 !border-0 !bg-purple/40" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <Workflow aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-purple" />
        <span className="truncate text-xs font-medium">{d.name}</span>
      </div>
      <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground/70">
        <ScrollText aria-hidden="true" className="h-2.5 w-2.5" />
        <span className="truncate font-mono">{d.slug}</span>
        {d.invocationCount !== undefined && d.invocationCount > 0 && (
          <span className="ml-auto rounded bg-white/[0.06] px-1 py-0 text-[9px]">
            {d.invocationCount} runs
          </span>
        )}
      </div>
    </Link>
  )
}
export const OverviewRoutineNode = memo(RoutineNodeBase)

function RunNodeBase({ data: d }: NodeProps<Node<OverviewRunNodeData>>) {
  const tint = statusTint(d.status)
  const SI = statusIcon(d.status)
  const isWait = d.isWaitpoint || d.status === "paused"
  // Trigger source is decorative-only in SourceIcon, so the
  // accessible name has to spell it out — otherwise screen-reader
  // users hear "Run prn_abc status completed" with no clue WHY the
  // run fired (cron vs issue vs webhook are clearly distinct in
  // the visual). `via unknown` covers a missing/legacy field.
  const accessibleLabel =
    `Run ${d.runId} triggered via ${d.triggeredVia ?? "unknown"} status ${d.status}`
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={accessibleLabel}
      onKeyDown={activateOnEnterOrSpace}
      className={cn(
        "relative w-[180px] rounded-lg border border-white/[0.06] bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/80",
        isWait && "ring-1 ring-warn/40",
      )}
    >
      <Handle type="target" position={Position.Left} className="!h-2 !w-2 !border-0 !bg-white/30" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <span className={cn("flex h-4 w-4 shrink-0 items-center justify-center rounded-full", tint.bg)}>
          <SI className={cn("h-2.5 w-2.5", tint.icon)} aria-hidden="true" />
        </span>
        <span className="truncate font-mono text-[10px]">{shortId(d.runId)}</span>
        <span className={cn("ml-auto text-[10px] capitalize", tint.text)}>{d.status}</span>
      </div>
      <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground/60">
        <SourceIcon source={d.triggeredVia} />
        <span>{relTime(d.startedAt)}</span>
        {isWait && (
          <span className="ml-auto inline-flex items-center gap-0.5 rounded bg-warn/15 px-1 py-0 text-[9px] font-medium text-warn">
            <PauseCircle className="h-2 w-2" /> awaiting
          </span>
        )}
      </div>
    </div>
  )
}
export const OverviewRunNode = memo(RunNodeBase)

function AutomationNodeBase({ data: d }: NodeProps<Node<OverviewAutomationNodeData>>) {
  // No detail route to link to — an automation is configuration, and
  // the surface that edits it is the routine it fires, which is the
  // very next node. A card that navigates somewhere arbitrary would be
  // worse than one that doesn't navigate at all.
  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`Automation ${d.name}, on ${d.eventType}, ${
        d.deleted ? "deleted" : d.enabled ? "enabled" : "disabled"
      }`}
      onKeyDown={activateOnEnterOrSpace}
      className={cn(
        "relative w-[200px] rounded-lg border border-gold/25 bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-gold/80",
        // A disabled rule stays on the graph — "wired up but off" is a
        // fact worth seeing — but reads as inert rather than live.
        !d.enabled && "opacity-60",
        // A deleted rule appears only in a causal chain, explaining runs it
        // already caused. Dashed rather than merely dimmed, because "off" and
        // "gone" must not be told apart by opacity alone.
        d.deleted && "border-dashed border-muted-foreground/40",
      )}
    >
      <Handle type="source" position={Position.Right} className="!h-2 !w-2 !border-0 !bg-gold/40" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <Zap aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-gold" />
        <span className="truncate text-xs font-medium" title={d.name}>
          {d.name}
        </span>
        <span
          className={cn(
            "ml-auto rounded px-1 py-0 text-[9px] font-medium uppercase tracking-wide",
            d.deleted
              ? "bg-muted-foreground/10 text-muted-foreground line-through"
              : d.enabled
                ? "bg-success/15 text-success"
                : "bg-muted-foreground/10 text-muted-foreground",
          )}
        >
          {d.deleted ? "deleted" : d.enabled ? "on" : "off"}
        </span>
      </div>
      <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground/70">
        <span className="truncate font-mono" title={d.eventType}>
          {d.eventType}
        </span>
      </div>
    </div>
  )
}
export const OverviewAutomationNode = memo(AutomationNodeBase)

function AgentNodeBase({ data: d }: NodeProps<Node<OverviewAgentNodeData>>) {
  // /crews?agent=<slug> is where an agent opens across the app (see
  // the command palette) — same destination, so the graph doesn't
  // invent a second one.
  return (
    <Link
      href={`/crews?agent=${encodeURIComponent(d.slug)}`}
      aria-label={d.crewName ? `Agent ${d.name} of ${d.crewName}` : `Agent ${d.name}`}
      className="relative block w-[180px] rounded-lg border border-primary/25 bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/80"
      onClick={(e) => e.stopPropagation()}
    >
      <Handle type="target" position={Position.Left} className="!h-2 !w-2 !border-0 !bg-primary/40" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <AgentAvatar
          seed={d.avatarSeed || d.slug || d.name}
          style={d.avatarStyle}
          agentId={d.agentId}
          avatarUrl={d.avatarUrl}
          className="h-4 w-4 shrink-0"
        />
        <span className="truncate text-xs font-medium" title={d.name}>
          {d.name}
        </span>
      </div>
      <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground/70">
        {d.crewName ? (
          <span className="truncate">{d.crewName}</span>
        ) : (
          <span className="truncate font-mono">{d.slug}</span>
        )}
      </div>
    </Link>
  )
}
export const OverviewAgentNode = memo(AgentNodeBase)

function InboxNodeBase({ data: d }: NodeProps<Node<OverviewInboxNodeData>>) {
  const KI = inboxKindIcon(d.kind)
  return (
    <Link
      href={`/inbox?item=${encodeURIComponent(d.itemId)}`}
      aria-label={`Inbox ${d.kind.replace(/_/g, " ")}: ${d.title}`}
      className={cn(
        "relative block w-[200px] rounded-lg border border-warn/25 bg-card px-2.5 py-2 transition-colors hover:bg-card/80 focus:outline-none focus-visible:ring-2 focus-visible:ring-warn/80",
        // Blocking means the chain is stopped until someone acts. That
        // is the one thing on this graph a reader must not scroll past.
        d.blocking && "ring-1 ring-warn/40",
      )}
      onClick={(e) => e.stopPropagation()}
    >
      <Handle type="target" position={Position.Left} className="!h-2 !w-2 !border-0 !bg-warn/40" isConnectable={false} />
      <div className="flex items-center gap-1.5">
        <KI aria-hidden="true" className="h-3.5 w-3.5 shrink-0 text-warn" />
        <span className="truncate text-[10px] font-medium uppercase tracking-wide text-warn">
          {d.kind.replace(/_/g, " ")}
        </span>
        {d.priority && (d.priority === "urgent" || d.priority === "high") && (
          <span className="ml-auto rounded bg-destructive/15 px-1 py-0 text-[9px] font-medium uppercase tracking-wide text-destructive">
            {d.priority}
          </span>
        )}
      </div>
      <div className="mt-1 truncate text-xs text-foreground/90" title={d.title}>
        {d.title}
      </div>
    </Link>
  )
}
export const OverviewInboxNode = memo(InboxNodeBase)

/**
 * The overview half of the canvas `nodeTypes` map.
 *
 * Typed against `OverviewNodeType` on purpose: the same union types the
 * width map in build-overview-graph, so registering a component the
 * layout has no width for — the bug that used to land silently, as a
 * card half a width off its column — is a compile error here, and a
 * test asserts it at runtime too.
 */
export const overviewNodeTypes: Record<OverviewNodeType, NodeTypes[string]> = {
  overviewIssue: OverviewIssueNode,
  overviewRoutine: OverviewRoutineNode,
  overviewRun: OverviewRunNode,
  overviewAutomation: OverviewAutomationNode,
  overviewAgent: OverviewAgentNode,
  overviewInbox: OverviewInboxNode,
}

// ── helpers ────────────────────────────────────────────────────────

function StatusChip({ status }: { status: string }) {
  const s = status.toLowerCase()
  const cls =
    s === "in_progress" || s === "running"
      ? "bg-primary/15 text-primary"
      : s === "review"
        ? "bg-purple/15 text-purple"
        : s === "completed" || s === "done"
          ? "bg-success/15 text-success"
          : s === "failed"
            ? "bg-destructive/15 text-destructive"
            : "bg-white/[0.06] text-muted-foreground"
  return (
    <span className={cn("ml-auto rounded px-1 py-0 text-[9px] font-medium uppercase tracking-wide", cls)}>
      {status}
    </span>
  )
}

// SourceIcon — purely decorative glyph next to the relative time.
// Each branch carries aria-hidden so screen readers don't try to
// pronounce the typographic command/lightning/return characters;
// the parent node's aria-label already names the trigger source via
// the run status text.
function SourceIcon({ source }: { source?: string }) {
  if (source === "schedule") return <span aria-hidden="true" className="text-purple">⌘</span>
  if (source === "issue") return <CircleDot aria-hidden="true" className="h-2.5 w-2.5 text-blue-300" />
  if (source === "webhook") return <span aria-hidden="true" className="text-warn">⚡</span>
  if (source === "call_pipeline") return <span aria-hidden="true" className="text-purple">↳</span>
  return <Zap aria-hidden="true" className="h-2.5 w-2.5 text-muted-foreground/60" />
}

// inboxKindIcon — one glyph per inbox kind. Unknown kinds (the server
// grows them faster than this union does) fall back to the generic
// tray rather than rendering nothing.
function inboxKindIcon(kind: string) {
  if (kind === "waitpoint") return PauseCircle
  if (kind === "escalation") return ShieldAlert
  if (kind === "message") return MessageSquare
  if (kind === "failed_run") return AlertTriangle
  return Inbox
}

function shortId(id: string): string {
  if (id.length > 12 && id.startsWith("prn_")) return id.slice(0, 8)
  return id
}
