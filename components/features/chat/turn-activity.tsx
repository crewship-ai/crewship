"use client"

import { useEffect, useRef, useState } from "react"
import { AlertTriangle, Ban, Check, Circle, CircleDot, CircleSlash, Copy, Hand, X } from "lucide-react"
import type { ChatTurn, TurnPart } from "@/hooks/use-chat"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import {
  ACTIVITY_STATE_LABEL,
  currentStepLabel,
  flattenSteps,
  formatElapsed,
  redactSensitiveKeys,
  settleRetries,
  stepPhrase,
  toolNameOf,
  toolState,
  turnActivitySummary,
  type ActivityState,
} from "@/lib/chat-activity"
import { cn } from "@/lib/utils"
import type { ToolNode } from "./turn-grouping"

/** A clock that ticks once a second while `active`. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [active])
  return now
}

/** How long a finished turn ran: the run's own duration when the result
 *  reported it, else the span of its parts. Undefined when neither is known. */
function finishedMs(turn: ChatTurn): number | undefined {
  const reported = turn.parts.find((p) => p.type === "result")?.metadata?.duration_ms
  if (typeof reported === "number" && reported > 0) return reported
  const start = turn.timestamp.getTime()
  const end = Math.max(...turn.parts.map((p) => p.timestamp.getTime()))
  return end - start >= 1000 ? end - start : undefined
}

const plural = (n: number) => `${n} ${n === 1 ? "step" : "steps"}`

interface TurnActivityProps {
  /** Every tool step of the turn that is not shown in the transcript itself. */
  tools: ToolNode[]
  turn: ChatTurn
  order?: (part: TurnPart) => number | undefined
  onCopy: (content: string) => Promise<boolean>
}

/**
 * The one activity line of an assistant turn. While the agent works it is a
 * calm working graphic, one sentence of what the agent does now and the time;
 * afterwards an honest "Done · 41 s · 12 steps" (or Finished / Failed with what
 * went wrong). The steps themselves open in a dialog, never in the transcript.
 */
export function TurnActivity({ tools, turn, order, onCopy }: TurnActivityProps) {
  const streaming = !!turn.isStreaming
  const [open, setOpen] = useState(false)
  const now = useNow(streaming)
  const steps = flattenSteps(tools)
  const raw = steps.map((s) => toolState(s, streaming))
  const { states, retried } = settleRetries(steps, raw, order)
  // Steps that failed and were then done again successfully: they stay Failed,
  // and say that the retry worked.
  const recovered = new Set(steps.filter((_, i) => raw[i] === "failed" && states[i] === "succeeded"))
  const summary = turnActivitySummary(states, { retried, turnState: turn.turnState, streaming })

  let line: string
  let sentence: string | undefined
  let elapsed: string | undefined
  let tone = summary.tone
  if (streaming) {
    const last = turn.parts[turn.parts.length - 1]
    const waiting = summary.title === "Needs approval"
    const live = waiting
      ? "Waiting for your approval…"
      : currentStepLabel(steps, states) ?? (last?.type === "text" ? "Writing the answer…" : "Working…")
    tone = waiting ? "attention" : "running"
    sentence = live
    elapsed = formatElapsed(Math.max(0, now - turn.timestamp.getTime()))
    line = `${live} · ${elapsed}`
  } else {
    const ms = finishedMs(turn)
    line = [summary.title, ms !== undefined ? formatElapsed(ms) : "", plural(steps.length), summary.detail].filter(Boolean).join(" · ")
  }

  return (
    <div data-testid="turn-activity" className="flex min-w-0">
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogTrigger asChild>
          <button
            type="button"
            data-activity-tone={tone}
            className="-ml-1.5 inline-flex min-w-0 max-w-full items-center gap-2 rounded-md px-1.5 py-1 text-left text-label text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 coarse:min-h-[44px]"
          >
            {/* No mark of its own while the agent works: the face in the gutter
                is the turn's one working indicator (see ThinkingAvatar). */}
            {streaming && tone === "running" ? null : tone === "attention" ? (
              <AlertTriangle className="h-3.5 w-3.5 shrink-0 text-warn" role="img" aria-label={summary.detail || summary.title} />
            ) : tone === "ok" ? (
              <Check className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            ) : (
              <CircleDot className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            )}
            {sentence !== undefined ? (
              <span className="min-w-0 truncate">
                <span data-testid="activity-sentence" className="font-medium text-foreground">{sentence}</span>
                <span aria-hidden="true"> · </span>
                <span data-testid="activity-elapsed" className="tabular-nums">{elapsed}</span>
              </span>
            ) : (
              <span className="min-w-0 truncate">{line}</span>
            )}
          </button>
        </DialogTrigger>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>Steps</DialogTitle>
            <DialogDescription>{line}</DialogDescription>
          </DialogHeader>
          <StepList tools={tools} streaming={streaming} recovered={recovered} onCopy={onCopy} />
        </DialogContent>
      </Dialog>
    </div>
  )
}

function StepIcon({ state }: { state: ActivityState }) {
  const cls = "mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground"
  switch (state) {
    case "succeeded": return <Check className={cls} aria-hidden="true" />
    case "failed": return <X className={cls} aria-hidden="true" />
    case "denied": return <Ban className={cls} aria-hidden="true" />
    case "interrupted": return <CircleSlash className={cls} aria-hidden="true" />
    case "awaiting_approval": return <Hand className={cls} aria-hidden="true" />
    case "running": return <Circle className={cn(cls, "motion-safe:animate-pulse")} aria-hidden="true" />
    default: return <CircleDot className={cls} aria-hidden="true" />
  }
}

interface StepProps { streaming: boolean; recovered: ReadonlySet<ToolNode>; onCopy: (content: string) => Promise<boolean> }

function StepList({ tools, ...rest }: StepProps & { tools: ToolNode[] }) {
  return (
    <ol className="flex flex-col">
      {tools.map((node, i) => (
        <StepRow key={node.call.id ?? i} node={node} {...rest} />
      ))}
    </ol>
  )
}

/** The raw call: the command for a shell step, else the (redacted) input. */
function rawInput(node: ToolNode): string | undefined {
  const input = node.call.activity ? node.call.activity.parameters : node.call.metadata?.input
  if (input === undefined || input === null) return undefined
  const o = input as Record<string, unknown>
  if (toolNameOf(node) === "Bash" && typeof o.command === "string") return `$ ${o.command}`
  return JSON.stringify(redactSensitiveKeys(input), null, 2)
}

/** What the step returned. A typed activity carries its own bounded output and
 *  never falls back to a legacy result body; a legacy call shows its result. */
function rawOutput(node: ToolNode): string | undefined {
  if (node.call.activity) return node.call.activity.output || undefined
  return node.result?.content || (node.call.type === "tool_result" ? node.call.content : undefined) || undefined
}

function StepRow({ node, streaming, recovered, onCopy }: StepProps & { node: ToolNode }) {
  const state = toolState(node, streaming)
  const input = rawInput(node)
  const output = rawOutput(node)
  const diff = node.call.activity?.diff
  const truncated = node.call.activity?.truncated === true
  return (
    <li data-testid="activity-step" data-state={state} className="flex flex-col gap-1 py-1.5">
      <div className="flex items-start gap-2 text-label">
        <StepIcon state={state} />
        <span className="min-w-0 flex-1 break-words text-foreground">{stepPhrase(node, state === "running" ? "doing" : "done")}</span>
        {state !== "succeeded" && (
          <span data-testid="step-state" className="shrink-0 text-muted-foreground">
            {ACTIVITY_STATE_LABEL[state]}{state === "failed" && recovered.has(node) ? " · retried, then succeeded" : ""}
          </span>
        )}
      </div>
      {(input || output || diff || truncated) && <StepRaw input={input} output={output} diff={diff} truncated={truncated} onCopy={onCopy} />}
      {node.children?.map((child, i) => (
        <div key={child.call.id ?? i} data-testid="nested-activity" className="ml-1.5 border-l border-border pl-3">
          <ol><StepRow node={child} streaming={streaming} recovered={recovered} onCopy={onCopy} /></ol>
        </div>
      ))}
    </li>
  )
}

function StepRaw({ input, output, diff, truncated, onCopy }: { input?: string; output?: string; diff?: string; truncated?: boolean; onCopy: (content: string) => Promise<boolean> }) {
  const [copied, setCopied] = useState<"idle" | "copied" | "failed">("idle")
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const copy = async () => {
    const ok = await onCopy([input, output, diff].filter(Boolean).join("\n\n"))
    clearTimeout(timer.current)
    setCopied(ok ? "copied" : "failed")
    timer.current = setTimeout(() => setCopied("idle"), 2000)
  }
  return (
    <details data-testid="step-raw" className="group/raw ml-6">
      <summary className="w-fit cursor-pointer select-none list-none text-micro text-muted-foreground hover:text-foreground [&::-webkit-details-marker]:hidden">
        <span className="group-open/raw:hidden">Show details</span>
        <span className="hidden group-open/raw:inline">Hide details</span>
      </summary>
      <div className="mt-1 overflow-hidden rounded-md border border-border bg-muted/30">
        <div className="flex justify-end border-b border-border px-1 py-0.5">
          <button
            type="button"
            onClick={() => void copy()}
            aria-label="Copy command and output"
            className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-micro text-muted-foreground hover:bg-muted hover:text-foreground coarse:min-h-[44px]"
          >
            {copied === "copied" ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
            <span aria-live="polite">{copied === "copied" ? "Copied" : copied === "failed" ? "Copy failed" : "Copy"}</span>
          </button>
        </div>
        {input && <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-all px-3 py-2 font-mono text-micro">{input}</pre>}
        {output && <pre className={cn("max-h-64 overflow-auto whitespace-pre-wrap break-all px-3 py-2 font-mono text-micro text-muted-foreground", input && "border-t border-border")}>{output}</pre>}
        {diff && <pre data-testid="step-diff" className={cn("max-h-64 overflow-auto whitespace-pre-wrap break-all px-3 py-2 font-mono text-micro", (input || output) && "border-t border-border")}>{diff}</pre>}
        {truncated && <p className="border-t border-border px-3 py-1.5 text-micro text-muted-foreground">Details truncated</p>}
      </div>
    </details>
  )
}
