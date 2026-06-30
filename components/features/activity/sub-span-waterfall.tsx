"use client"

import { useMemo, useState } from "react"
import { Check, ChevronRight, FileText, Loader2, X } from "lucide-react"
import { cn } from "@/lib/utils"
import { formatDurationDecimal } from "@/lib/time"
import type { SubSpan } from "@/lib/trace/types"
import { layoutSubSpans } from "@/lib/trace/sub-spans"
import {
  SUB_SPAN_BAR_CLASS,
  SUB_SPAN_STATUS_COLOR,
  SubSpanIcon,
} from "./sub-span-visual"

// SubSpanWaterfall — the drill-down view for one step: a compact Gantt
// lane (each agent action positioned by started_at/duration within the
// step window) + a per-action detail expander. Lives in the detail
// panel so the animated canvas keeps the step-level flow and this is
// the new layer beneath it. Pure-data driven (layoutSubSpans), so it
// renders deterministically for tests + replay.

export function SubSpanWaterfall({
  spans,
  onOpenArtifact,
}: {
  spans: SubSpan[]
  onOpenArtifact?: (path: string) => void
}) {
  const bars = useMemo(() => layoutSubSpans(spans), [spans])
  const [openIdx, setOpenIdx] = useState<number | null>(null)

  if (spans.length === 0) {
    return (
      <div className="flex h-24 flex-col items-center justify-center gap-2 text-center">
        <FileText className="h-5 w-5 text-muted-foreground/30" />
        <div className="text-xs text-muted-foreground/60">
          No agent actions recorded for this step.
        </div>
      </div>
    )
  }

  return (
    <div className="space-y-0.5">
      {bars.map(({ span, leftPct, widthPct }, i) => {
        const open = openIdx === i
        return (
          <div key={`${span.kind}:${span.name}:${i}`}>
            <button
              type="button"
              onClick={() => setOpenIdx(open ? null : i)}
              aria-expanded={open}
              className={cn(
                "grid w-full grid-cols-[1fr_88px] items-center gap-2 rounded px-1 py-1 text-left transition-colors hover:bg-white/[0.04]",
                open && "bg-white/[0.05]",
              )}
            >
              {/* name + badges */}
              <div className="flex min-w-0 items-center gap-1.5">
                <ChevronRight
                  className={cn(
                    "h-3 w-3 shrink-0 text-muted-foreground/40 transition-transform",
                    open && "rotate-90",
                  )}
                />
                <span className="grid h-5 w-5 shrink-0 place-items-center">
                  <SubSpanIcon kind={span.kind} tool={span.attributes.tool} className="h-3.5 w-3.5" />
                </span>
                <span className="truncate text-[11px] font-medium text-foreground">
                  {span.name}
                </span>
                {span.attributes.tool && (
                  <span className="shrink-0 rounded border border-violet-500/30 px-1 text-[9px] text-violet-300">
                    {span.attributes.tool}
                  </span>
                )}
                {span.attributes.artifact_path && (
                  <span className="shrink-0 rounded border border-amber-500/30 px-1 text-[9px] text-amber-300">
                    {fileName(span.attributes.artifact_path)}
                  </span>
                )}
              </div>
              {/* mini waterfall lane */}
              <div className="relative h-3 w-full overflow-hidden rounded-sm bg-white/[0.04]">
                <span
                  className={cn(
                    "absolute top-0 h-full rounded-sm",
                    SUB_SPAN_BAR_CLASS[span.kind],
                    span.status === "error" && "ring-1 ring-rose-500/60",
                    span.status === "running" && "animate-pulse",
                  )}
                  style={{ left: `${leftPct}%`, width: `${widthPct}%` }}
                />
              </div>
            </button>

            {open && <SpanDetail span={span} onOpenArtifact={onOpenArtifact} />}
          </div>
        )
      })}
    </div>
  )
}

function SpanDetail({
  span,
  onOpenArtifact,
}: {
  span: SubSpan
  onOpenArtifact?: (path: string) => void
}) {
  return (
    <div className="ml-5 mb-1 space-y-2 rounded border border-white/[0.06] bg-background/60 p-2">
      <dl className="grid grid-cols-[68px_1fr] gap-x-2 gap-y-1 text-[11px]">
        <dt className="text-muted-foreground/60">status</dt>
        <dd className={cn("inline-flex items-center gap-1", SUB_SPAN_STATUS_COLOR[span.status])}>
          <StatusGlyph status={span.status} />
          {span.status}
        </dd>
        {typeof span.durationMs === "number" && (
          <>
            <dt className="text-muted-foreground/60">duration</dt>
            <dd className="font-mono text-foreground/80">{formatDurationDecimal(span.durationMs)}</dd>
          </>
        )}
        {span.attributes.host && (
          <>
            <dt className="text-muted-foreground/60">egress</dt>
            <dd className="font-mono text-blue-300">{span.attributes.host}</dd>
          </>
        )}
        {span.attributes.model && (
          <>
            <dt className="text-muted-foreground/60">model</dt>
            <dd className="font-mono text-indigo-300">{span.attributes.model}</dd>
          </>
        )}
      </dl>

      {span.detail && (
        <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded bg-[#0a0b0d] p-2 font-mono text-[10.5px] leading-relaxed text-foreground/80">
          {span.detail}
        </pre>
      )}

      {span.attributes.artifact_path && (
        <button
          type="button"
          onClick={() => onOpenArtifact?.(span.attributes.artifact_path as string)}
          className="inline-flex items-center gap-1.5 rounded border border-white/[0.08] bg-background px-2 py-1 text-[11px] text-foreground transition-colors hover:border-amber-500/40 hover:bg-amber-500/5"
        >
          <FileText className="h-3 w-3 text-amber-300" />
          <span className="font-mono">{fileName(span.attributes.artifact_path)}</span>
          <span className="text-muted-foreground/50">open</span>
        </button>
      )}
    </div>
  )
}

function StatusGlyph({ status }: { status: SubSpan["status"] }) {
  if (status === "running") return <Loader2 className="h-3 w-3 animate-spin" />
  if (status === "error") return <X className="h-3 w-3" />
  return <Check className="h-3 w-3" />
}

function fileName(path: string): string {
  const parts = path.split("/").filter(Boolean)
  return parts[parts.length - 1] || path
}
