"use client"

import { motion } from "motion/react"
import { AlertTriangle } from "lucide-react"
import { DetailCard, Pill, TickRow } from "@/components/ui/detail"
import type { LucideIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"


// =============================================================================
// The blocks above the cells, in the order the screen answers questions:
//
//   BlockingNotice — what this thing wants FROM YOU. Nothing outranks it.
//   NowRunning     — what it is doing right now.
//
// Both speak the routine screen's vocabulary: a card with an UPPERCASE header
// and ticks on the ragged right edge.
//
// ReachStrip used to live here — a row of chips, each sliding a panel in from
// the right. It is gone. Three of its chips held a list and four held an entire
// tab component, so the one pattern needed two widths, and six of the seven
// duplicated something already on the screen. The lists are cards in the grid
// now and the managers are centred dialogs. Recover it from git if a surface
// ever genuinely wants a drawer; do not bring it back to avoid adding a card.
// =============================================================================

export interface BlockingNoticeAction {
  label: string
  onClick: () => void
  primary?: boolean
}

export interface BlockingNoticeProps {
  title: string
  body: React.ReactNode
  detail?: React.ReactNode
  actions?: BlockingNoticeAction[]
  /** `warn` for decisions, `notice` for advisories that do not block. */
  tone?: "warn" | "notice"
  icon?: LucideIcon
}

export function BlockingNotice({
  title, body, detail, actions = [], tone = "warn", icon: Icon = AlertTriangle,
}: BlockingNoticeProps) {
  return (
    <motion.div
      initial={{ opacity: 0, y: -6 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.24, ease: [0.22, 1, 0.36, 1] }}
    >
      <DetailCard tone={tone === "warn" ? "warn" : "default"} className={tone === "warn" ? "bg-warn/[.06]" : undefined}>
        <div className="flex flex-wrap items-center gap-3">
          <span className={cn("inline-flex shrink-0", tone === "warn" ? "text-warn" : "text-notice")}>
            <Icon className="h-4 w-4" />
          </span>
          <div className="type-row min-w-0 flex-1 basis-60 leading-snug">
            <b className="font-semibold">{title}</b> {body}
            {detail && <span className="type-meta mt-0.5 block text-muted-foreground">{detail}</span>}
          </div>
          {actions.length > 0 && (
            <div className="flex shrink-0 gap-2">
              {actions.map((a) => (
                <Button key={a.label} variant={a.primary ? "soft" : "outline"} size="sm" onClick={a.onClick}>
                  {a.label}
                </Button>
              ))}
            </div>
          )}
        </div>
      </DetailCard>
    </motion.div>
  )
}

export interface NowRunningStep {
  label: string
  detail?: string
  status: "ok" | "failed" | "running" | "pending"
  meta?: string
}

export interface NowRunningProps {
  label: string
  step?: string
  percent?: number
  meta?: string
  icon?: LucideIcon
  onStop?: () => void
  /** Rendered as "how it runs" ticks under the header, like the routine screen. */
  steps?: NowRunningStep[]
}

export function NowRunning({ label, step, percent, meta, icon: Icon, onStop, steps }: NowRunningProps) {
  return (
    <motion.div
      initial={{ opacity: 0, y: -4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.24, ease: [0.22, 1, 0.36, 1] }}
    >
      <DetailCard tone="success" bare>
        <div className="flex flex-wrap items-center gap-3 px-4 py-3">
          <span className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-success" />
          <span className="type-row inline-flex items-center gap-2 font-semibold">
            {Icon && <Icon className="h-3.5 w-3.5" />}
            {label}
          </span>
          {step && <Pill tone="blue">{step}</Pill>}
          <span className="h-1.5 min-w-[110px] flex-1 basis-44 overflow-hidden rounded-full bg-surface-raised">
            <motion.span
              className="block h-full rounded-full bg-primary"
              initial={{ width: 0 }}
              animate={{ width: percent === undefined ? "40%" : `${percent}%` }}
              transition={{ duration: 0.7, ease: [0.22, 1, 0.36, 1] }}
            />
          </span>
          {meta && <span className="type-meta font-mono text-muted-foreground-soft">{meta}</span>}
          {onStop && (
            <Button variant="outline" size="xs" onClick={onStop}>Stop</Button>
          )}
        </div>
        {steps && steps.length > 0 && (
          <div className="border-t border-hairline px-4 py-2">
            <div className="type-section mb-1 text-muted-foreground-soft">How it runs</div>
            {steps.map((s) => (
              <TickRow key={s.label} label={s.label} detail={s.detail} status={s.status} meta={s.meta} />
            ))}
          </div>
        )}
      </DetailCard>
    </motion.div>
  )
}
