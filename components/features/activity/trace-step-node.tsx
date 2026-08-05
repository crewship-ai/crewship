"use client"

import { memo, useEffect, useRef, useState, type ReactNode } from "react"
import { Handle, Position, type NodeProps } from "@xyflow/react"
import {
  ArrowLeftRight,
  BellRing,
  Check,
  CircleDot,
  Database,
  Globe,
  PauseCircle,
  FileCode2,
  Repeat,
  ScrollText,
  Sparkles,
  Terminal,
  ThumbsDown,
  ThumbsUp,
  XCircle,
  Zap,
  type LucideIcon,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { cn } from "@/lib/utils"
import { toast } from "sonner"
import type {
  StepKind,
  StepStatus,
  TraceStep,
  TraceStepNodeData,
  TraceTriggerNodeData,
} from "@/lib/trace/types"
import { isAlreadyDecidedError, waitpointDecide } from "@/lib/api/waitpoints"
import { HEATMAP_BORDER_CLASS } from "@/lib/trace/percentile-heatmap"
import { StepHoverCard } from "./step-hover-card"

export type { TraceStepNodeData, TraceTriggerNodeData }

// TraceStepNode — single React Flow node component used for every
// step kind in the trace canvas. The kind drives icon + subtitle; the
// status drives the colored ring + status pip; clicking the node
// selects it (handled by the canvas-level onNodeClick).
//
// Why one component for 6 step kinds: the visual shell is identical
// (icon, label, type chip, subtitle, status pip). Splitting into 6
// components meant 6× the boilerplate without any per-kind layout
// difference. The variation is data, not structure.

// Icon + label for each step kind. The "label" surfaces as a small
// type chip on the node so a colorblind user can still distinguish
// kinds when icons aren't enough.
const KIND_VISUAL: Record<StepKind, { Icon: LucideIcon; label: string; tint: string }> = {
  agent_run: { Icon: Sparkles, label: "agent", tint: "text-purple" },
  http: { Icon: Globe, label: "http", tint: "text-notice" },
  transform: { Icon: ArrowLeftRight, label: "transform", tint: "text-success" },
  code: { Icon: Terminal, label: "code", tint: "text-warn" },
  wait: { Icon: PauseCircle, label: "wait", tint: "text-blue-300" },
  call_pipeline: { Icon: ScrollText, label: "sub-routine", tint: "text-purple" },
  notify: { Icon: BellRing, label: "notify", tint: "text-pink-300" },
  script: { Icon: FileCode2, label: "script", tint: "text-lime-300" },
  query: { Icon: Database, label: "query", tint: "text-cyan-300" },
  foreach: { Icon: Repeat, label: "foreach", tint: "text-orange-300" },
}

// Trigger isn't a real step kind — it's a synthetic node for the
// run's entry point (issue / schedule / webhook / manual). Local;
// nobody outside this file renders the trigger visual.
const TRIGGER_VISUAL = { Icon: Zap, label: "trigger", tint: "text-warn" }

const STATUS_RING: Record<StepStatus, { ring: string; bg: string }> = {
  pending: { ring: "ring-1 ring-white/[0.08]", bg: "bg-card" },
  running: {
    ring: "ring-2 ring-primary/60 shadow-[0_0_20px_rgba(30,123,254,0.25)]",
    bg: "bg-card",
  },
  waiting: {
    ring: "ring-2 ring-warn/60 shadow-[0_0_18px_rgba(251,191,36,0.2)]",
    bg: "bg-card",
  },
  success: { ring: "ring-1 ring-success/40", bg: "bg-card" },
  failed: {
    ring: "ring-2 ring-destructive/60 shadow-[0_0_15px_rgba(244,63,94,0.2)]",
    bg: "bg-card",
  },
  skipped: { ring: "ring-1 ring-white/[0.06] opacity-60", bg: "bg-card" },
}

function StatusPip({ status }: { status: StepStatus }) {
  switch (status) {
    case "running":
      return (
        <span className="absolute -right-1 -top-1 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-primary ring-2 ring-background">
          <Spinner className="h-2 w-2 text-white" />
        </span>
      )
    case "waiting":
      return (
        <span className="absolute -right-1 -top-1 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-warn ring-2 ring-background">
          <PauseCircle className="h-2 w-2 animate-pulse text-white" />
        </span>
      )
    case "success":
      return (
        <span className="absolute -right-1 -top-1 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-success ring-2 ring-background">
          <Check className="h-2 w-2 text-white" />
        </span>
      )
    case "failed":
      return (
        <span className="absolute -right-1 -top-1 flex h-3.5 w-3.5 items-center justify-center rounded-full bg-destructive ring-2 ring-background">
          <XCircle className="h-2 w-2 text-white" />
        </span>
      )
    case "skipped":
      return (
        <span className="absolute -right-1 -top-1 h-3.5 w-3.5 rounded-full bg-white/10 ring-2 ring-background" />
      )
    case "pending":
    default:
      return null
  }
}

/**
 * Which model an agent step will use, as far as the definition says.
 *
 * A pinned `model_override` is exact. Otherwise the `complexity` tag is
 * what the workspace tier map resolves against, so it is the honest
 * answer available without a server round-trip — and it is labelled as
 * a tier, not dressed up as a model name it might not become.
 */
function modelLabel(step: TraceStep): string | null {
  if (step.model_override) {
    // Strip the vendor prefix: "claude:claude-haiku-4-5" reads as
    // haiku-4-5 on a node that has ~120px for it.
    const raw = step.model_override.split(":").pop() ?? step.model_override
    return raw.replace(/^claude-/, "")
  }
  return step.complexity ?? null
}

function subtitleFor(step: TraceStep): ReactNode {
  switch (step.type) {
    case "http": {
      const method = (step.http?.method ?? "GET").toUpperCase()
      const url = step.http?.url ?? ""
      const host = url ? hostnameFromTemplate(url) : ""
      return (
        <>
          <span className="font-mono text-foreground/80">{method}</span>
          {host && <span className="ml-1 truncate text-muted-foreground/70">{host}</span>}
        </>
      )
    }
    case "agent_run": {
      // The model matters more than the agent slug for an agent step:
      // it is what the step will cost and how well it will reason, and
      // it was previously only visible by asking for a dry run.
      const model = modelLabel(step)
      return (
        <>
          {step.agent_slug ? (
            <span className="truncate font-mono text-foreground/80">{step.agent_slug}</span>
          ) : (
            <span className="text-muted-foreground/60">prompt</span>
          )}
          {model && (
            <span className="ml-1.5 shrink-0 rounded border border-border/60 px-1 py-0 font-mono text-[9px] uppercase tracking-wide text-muted-foreground">
              {model}
            </span>
          )}
        </>
      )
    }
    case "transform":
      return (
        <span className="truncate font-mono text-foreground/80">
          {step.transform?.expression ?? "."}
        </span>
      )
    case "code":
      return (
        <span className="truncate font-mono text-foreground/80">
          {step.code?.runtime ?? "code"}
        </span>
      )
    case "wait": {
      const kind = step.wait?.kind ?? "approval"
      return (
        <>
          <span className="font-mono text-foreground/80">{kind}</span>
          {step.wait?.approval_prompt && (
            <span className="ml-1 truncate text-muted-foreground/70">
              · {step.wait.approval_prompt}
            </span>
          )}
        </>
      )
    }
    case "call_pipeline":
      return (
        <span className="truncate font-mono text-foreground/80">
          {step.pipeline_slug ?? "(unknown)"}
        </span>
      )
    case "script":
      return step.script?.path ? (
        <span className="truncate font-mono text-foreground/80">{step.script.path}</span>
      ) : null
    case "notify":
      return step.notify?.to ? (
        <span className="truncate font-mono text-foreground/80">→ {step.notify.to}</span>
      ) : null
    case "query":
      return (
        <span className="truncate font-mono text-foreground/80">
          {step.query?.source ?? "datastore"}
        </span>
      )
    case "foreach":
      return (
        <span className="truncate font-mono text-foreground/80">
          {step.foreach?.items ? `over ${step.foreach.items}` : "loop"}
        </span>
      )
    default:
      return null
  }
}

// hostnameFromTemplate extracts the host portion of a URL even when
// the URL still contains template tokens. Returns the leading segment
// up to the first slash after the scheme, or the raw URL on parse
// failure. We don't need full URL parsing — the user just wants a
// readable subtitle ("api.github.com").
function hostnameFromTemplate(raw: string): string {
  const trimmed = raw.trim()
  // strip scheme
  const noScheme = trimmed.replace(/^https?:\/\//, "")
  const slash = noScheme.indexOf("/")
  const host = slash >= 0 ? noScheme.slice(0, slash) : noScheme
  return host.length > 32 ? host.slice(0, 31) + "…" : host
}

// baseName — last path segment of an artifact path, for the node badge.
function baseName(path: string): string {
  const parts = path.split("/").filter(Boolean)
  return parts[parts.length - 1] || path
}

function TraceStepNodeBase({ data }: NodeProps) {
  const d = data as unknown as TraceStepNodeData
  const { step, status, selected, waitpoint, heatmapBucket, durationMs, costUsd, outputSnippet, errorMessage } = d
  const subSpans = d.subSpans ?? []
  const model = d.model ?? null
  // First concrete tool + artifact across the step's actions — surfaced
  // as node badges so the canvas reads "this step ran ansible + wrote
  // sysfacts.yml" without expanding. Pure derivation, no hooks.
  const toolName = subSpans.find((s) => s.attributes.tool)?.attributes.tool ?? null
  const artifactPath = subSpans.find((s) => s.attributes.artifact_path)?.attributes.artifact_path ?? null
  const visual = KIND_VISUAL[step.type] ?? KIND_VISUAL.agent_run
  const Icon = visual.Icon
  const ring = STATUS_RING[status]
  const heatmapClass = heatmapBucket ? HEATMAP_BORDER_CLASS[heatmapBucket] : ""

  // tabIndex + onKeyDown make the node keyboard-activatable. Enter
  // and Space dispatch a click on the same element, which bubbles up
  // through React Flow's node wrapper and fires the canvas-level
  // onNodeClick handler — same code path as a mouse click.
  const nodeBody = (
    <div
      role="button"
      tabIndex={0}
      aria-label={`${visual.label} step ${step.id}, status ${status}`}
      aria-pressed={selected}
      onKeyDown={(e) => {
        // Only fire when the wrapper itself is focused — keydown
        // bubbles from descendants (e.g. the inline Approve/Deny
        // buttons), and pressing Enter on Approve would otherwise
        // also dispatch a wrapper click that re-selects the step.
        if (e.target !== e.currentTarget) return
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault()
          ;(e.currentTarget as HTMLElement).click()
        }
      }}
      className={cn(
        "relative w-[200px] rounded-lg border border-white/[0.06] px-2.5 py-2 transition-all focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/80",
        ring.bg,
        ring.ring,
        selected && "ring-2 ring-primary",
        "hover:bg-card/80",
        heatmapClass,
      )}
    >
      {/* Top/Bottom, matching the dagre TB rank direction. Handles
          on Left/Right would make every edge leave a node sideways
          and loop back down — the layout and the wiring have to agree
          on which way the graph flows. */}
      <Handle
        type="target"
        position={Position.Top}
        className="!h-2 !w-2 !border-0 !bg-white/30"
        isConnectable={false}
      />
      <Handle
        type="source"
        position={Position.Bottom}
        className="!h-2 !w-2 !border-0 !bg-white/30"
        isConnectable={false}
      />

      <StatusPip status={status} />

      <div className="flex items-center gap-1.5">
        <span className={cn("flex h-5 w-5 items-center justify-center rounded", visual.tint)}>
          <Icon className="h-3.5 w-3.5" />
        </span>
        <span className="truncate font-mono text-xs text-foreground">{step.id}</span>
        <span className="ml-auto rounded bg-white/[0.06] px-1 py-0 text-[9px] uppercase tracking-wider text-muted-foreground">
          {visual.label}
        </span>
      </div>

      <div className="mt-1 flex items-center gap-1 text-[10px]">
        {subtitleFor(step)}
      </div>

      {/* Rich badges — model / tool / artifact + the drill-down count.
        * Only render when the step actually has agent actions, so a run
        * with no sub_spans looks exactly as it did before. */}
      {(model || toolName || artifactPath || subSpans.length > 0) && (
        <div className="mt-1.5 flex flex-wrap items-center gap-1">
          {model && (
            <span className="rounded border border-indigo-500/40 px-1 py-0 text-[9px] font-medium text-indigo-300">
              {model}
            </span>
          )}
          {toolName && (
            <span className="rounded border border-purple/40 px-1 py-0 text-[9px] text-purple">
              {toolName}
            </span>
          )}
          {artifactPath && (
            <span className="max-w-[88px] truncate rounded border border-warn/40 px-1 py-0 text-[9px] text-warn">
              {baseName(artifactPath)}
            </span>
          )}
          {subSpans.length > 0 && (
            <span
              className={cn(
                "ml-auto inline-flex items-center gap-0.5 rounded bg-white/[0.06] px-1 py-0 text-[9px] font-medium",
                selected ? "text-primary" : "text-muted-foreground",
              )}
            >
              {selected ? "▾" : "▸"} {subSpans.length}{" "}
              {subSpans.length === 1 ? "action" : "actions"}
            </span>
          )}
        </div>
      )}

      {waitpoint && <WaitpointActions waitpoint={waitpoint} stepStatus={status} />}
    </div>
  )
  return (
    <StepHoverCard
      payload={{
        step,
        status,
        durationMs,
        costUsd,
        outputSnippet,
        errorMessage,
      }}
    >
      {nodeBody}
    </StepHoverCard>
  )
}

// WaitpointActions — inline Approve/Deny on a paused wait step.
// Canvas-as-resolution-surface pattern: the canvas IS the place
// to resolve the gate, no need to bounce to /inbox for the common
// case.
//
// We stop event propagation on the buttons because React Flow's
// onNodeClick fires on any click within the node — without stopping,
// approve/deny would also re-select the step.
//
// Resolution handling (bugfix 2026-07-02): the pending-waitpoints
// list refreshes on its own cadence, so a stale token can outlive the
// decision — previously the buttons stayed armed after the waitpoint
// was approved elsewhere and every click produced the red "waitpoint:
// already decided or expired" toast. Three exits now land on a muted
// resolved label instead of live buttons:
//   1. the step status (which the run feed advances first) says the
//      gate is past "waiting" → derive approved/denied from it,
//   2. our own decide succeeded,
//   3. the API answered "already decided or expired" → graceful
//      recovery, not an error loop.
type WaitpointResolution = "approved" | "denied" | "decided"

function WaitpointActions({
  waitpoint,
  stepStatus,
}: {
  waitpoint: { token: string; workspaceId: string }
  stepStatus: StepStatus
}) {
  const [busy, setBusy] = useState<"approve" | "deny" | null>(null)
  const [resolution, setResolution] = useState<WaitpointResolution | null>(null)
  // mountedRef guards setBusy after a successful decide — the
  // realtime pipeline.run.* event that fires once the run resumes
  // unmounts this node, and React warns on stale state updates if we
  // setBusy(null) on a dead component. Toast is fine to fire either
  // way (sonner is global) — only the local state needs the guard.
  const mountedRef = useRef(true)
  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
    }
  }, [])
  const decide = async (e: React.MouseEvent, approved: boolean) => {
    e.stopPropagation()
    e.preventDefault()
    setBusy(approved ? "approve" : "deny")
    // try/finally so a transport-level throw (network drop, CORS,
    // …) still clears the busy state. Without it, a thrown fetch
    // would leave both buttons disabled until the node remounts —
    // looks like a hung approval but is really an unhandled error.
    try {
      const res = await waitpointDecide(waitpoint.workspaceId, waitpoint.token, approved)
      if (res.ok) {
        toast.success(approved ? "Approved" : "Denied")
        if (mountedRef.current) setResolution(approved ? "approved" : "denied")
      } else if (isAlreadyDecidedError(res.status, res.error)) {
        // Someone else decided first — swap to the resolved state
        // instead of leaving armed buttons behind a red toast.
        toast.info("Waitpoint already decided", { description: res.error })
        if (mountedRef.current) setResolution("decided")
      } else {
        toast.error(res.error)
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Decide failed")
    } finally {
      if (mountedRef.current) setBusy(null)
    }
  }

  // Externally decided: the step advanced past "waiting" while the
  // stale token is still passed down. success = approved (the run
  // resumed), failed = denied/failed at the gate, skipped = decided
  // some other way — none of them may offer live buttons.
  const external: WaitpointResolution | null =
    stepStatus === "success"
      ? "approved"
      : stepStatus === "failed"
        ? "denied"
        : stepStatus === "skipped"
          ? "decided"
          : null
  const resolved = resolution ?? external

  if (resolved) {
    return (
      <div
        role="status"
        aria-label={`Waitpoint resolved: ${resolved}`}
        className="mt-1.5 flex items-center gap-1 text-[10px] font-medium text-muted-foreground"
      >
        {resolved === "approved" ? (
          <>
            <Check className="h-2.5 w-2.5 text-success/70" />
            <span>approved</span>
          </>
        ) : resolved === "denied" ? (
          <>
            <XCircle className="h-2.5 w-2.5 text-destructive/70" />
            <span>denied</span>
          </>
        ) : (
          <>
            <Check className="h-2.5 w-2.5" />
            <span>already decided</span>
          </>
        )}
      </div>
    )
  }

  return (
    <div className="mt-1.5 flex items-center gap-1">
      <button
        type="button"
        onClick={(e) => decide(e, true)}
        disabled={busy !== null}
        aria-label="Approve waitpoint"
        className={cn(
          "flex items-center gap-1 rounded bg-success/15 px-1.5 py-0.5 text-[10px] font-medium text-success transition-colors hover:bg-success/25 disabled:opacity-50",
        )}
      >
        {busy === "approve" ? (
          <Spinner className="h-2.5 w-2.5" />
        ) : (
          <ThumbsUp className="h-2.5 w-2.5" />
        )}
        Approve
      </button>
      <button
        type="button"
        onClick={(e) => decide(e, false)}
        disabled={busy !== null}
        aria-label="Deny waitpoint"
        className={cn(
          "flex items-center gap-1 rounded bg-destructive/15 px-1.5 py-0.5 text-[10px] font-medium text-destructive transition-colors hover:bg-destructive/25 disabled:opacity-50",
        )}
      >
        {busy === "deny" ? (
          <Spinner className="h-2.5 w-2.5" />
        ) : (
          <ThumbsDown className="h-2.5 w-2.5" />
        )}
        Deny
      </button>
    </div>
  )
}

export const TraceStepNode = memo(TraceStepNodeBase)

// TriggerNode — synthetic entry-point node. Same visual chrome as a
// step node but with a Zap icon, no input handle, and a smaller
// footprint. Renders the source of the run (issue id / schedule cron
// / webhook name / "manual").

function TriggerNodeBase({ data }: NodeProps) {
  const d = data as unknown as TraceTriggerNodeData
  const Icon = TRIGGER_VISUAL.Icon
  // Pin the label to the trigger SOURCE first; only fall through to
  // "manual" when triggered_via really is empty/manual. Previous
  // version dropped issue-triggered runs to "manual" when the
  // identifier was empty (deleted mission), confusing the user about
  // why the run kicked off.
  const label =
    d.triggeredVia === "issue"
      ? d.issueIdentifier || "issue"
      : d.triggeredVia === "schedule"
        ? "schedule"
        : d.triggeredVia === "webhook"
          ? "webhook"
          : d.triggeredVia === "call_pipeline"
            ? "sub-run"
            : "manual"
  return (
    <div
      role="img"
      aria-label={`Trigger ${label}, ${d.pipelineName ?? "routine"}`}
      className="relative w-[180px] rounded-lg border border-white/[0.06] bg-card px-2.5 py-2 ring-1 ring-warn/30"
    >
      <Handle
        type="source"
        position={Position.Bottom}
        className="!h-2 !w-2 !border-0 !bg-white/30"
        isConnectable={false}
      />
      <div className="flex items-center gap-1.5">
        <span className={cn("flex h-5 w-5 items-center justify-center rounded", TRIGGER_VISUAL.tint)}>
          <Icon className="h-3.5 w-3.5" />
        </span>
        <span className="truncate text-xs font-medium text-foreground">{label}</span>
        <span className="ml-auto rounded bg-warn/10 px-1 py-0 text-[9px] uppercase tracking-wider text-warn">
          {TRIGGER_VISUAL.label}
        </span>
      </div>
      <div className="mt-1 flex items-center gap-1 text-[10px] text-muted-foreground/70">
        <CircleDot className="h-2.5 w-2.5" />
        <span className="truncate">{d.pipelineName ?? "(routine)"}</span>
      </div>
    </div>
  )
}

export const TraceTriggerNode = memo(TriggerNodeBase)
