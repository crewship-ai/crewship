"use client"

import { useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { AlertTriangle, Radar, X } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DetailCard, EntityChip, Pill, TickRow } from "@/components/ui/detail"
import { cn } from "@/lib/utils"

import { DetailCell, type DetailCellProps } from "./detail-cell"

// =============================================================================
// The blocks above the cells, in the order the screen answers questions:
//
//   BlockingNotice — what this thing wants FROM YOU. Nothing outranks it.
//   NowRunning     — what it is doing right now.
//   ReachStrip     — what it can touch.
//
// All three are the routine screen's vocabulary: a card with an UPPERCASE
// header, ticks on the ragged right edge, entity chips for anything the
// surface points at. ReachStrip is the direct analogue of that screen's
// "WHAT IT TOUCHES · blast radius", down to the chips — the difference is
// that clicking one here slides out the full list instead of navigating.
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

export interface ReachItem {
  id: string
  icon: LucideIcon
  label: string
  /** Short count string — "2 / 5", "128", "2 · 1 čeká". */
  value: string
  tone: "primary" | "success" | "warn" | "purple" | "notice" | "gold"
  /** Draws attention to the chip — something in that list needs a decision. */
  alert?: boolean
  cell?: Omit<DetailCellProps, "className">
  /** Escape hatch for relations that already have a full component. */
  render?: () => React.ReactNode
  wide?: boolean
  /** Groups chips into labelled rows, as "WHAT IT TOUCHES" does. */
  group?: string
}

const CHIP_TONE: Record<ReachItem["tone"], "blue" | "success" | "warn" | "purple" | "default"> = {
  primary: "blue",
  success: "success",
  warn: "warn",
  purple: "purple",
  notice: "default",
  gold: "warn",
}

export interface ReachStripProps {
  items: ReachItem[]
  /**
   * `row` sits with the tabs as a line of chips — chrome, read once on
   * arrival. `card` is the blast-radius card, for surfaces where reach is
   * the subject rather than the navigation.
   */
  variant?: "row" | "card"
}

export function ReachStrip({ items, variant = "row" }: ReachStripProps) {
  const [openId, setOpenId] = useState<string | null>(null)
  const open = items.find((i) => i.id === openId) ?? null

  const groups = items.reduce<Record<string, ReachItem[]>>((acc, item) => {
    const key = item.group ?? "Reach"
    ;(acc[key] ??= []).push(item)
    return acc
  }, {})

  const drawer = (
    <AnimatePresence>
      {open && (
        <motion.div
          className="fixed inset-0 z-50"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.2 }}
        >
          <div className="absolute inset-0 bg-black/60 backdrop-blur-[3px]" onClick={() => setOpenId(null)} aria-hidden />
          <motion.aside
            role="dialog"
            aria-label={open.label}
            initial={{ x: "100%" }}
            animate={{ x: 0 }}
            exit={{ x: "100%" }}
            transition={{ duration: 0.32, ease: [0.22, 1, 0.36, 1] }}
            className={cn(
              "absolute inset-y-0 right-0 flex flex-col border-l border-border bg-surface-subtle",
              open.wide ? "w-[min(760px,96vw)]" : "w-[min(420px,92vw)]",
            )}
          >
            <header className="flex items-center gap-2 border-b border-hairline px-4 py-3">
              <span className="type-section text-foreground/70">{open.label}</span>
              <button
                type="button"
                onClick={() => setOpenId(null)}
                aria-label="Close"
                className="ml-auto inline-flex rounded-md p-1 text-muted-foreground transition-colors hover:bg-white/[.07] hover:text-foreground"
              >
                <X className="h-4 w-4" />
              </button>
            </header>
            <div className="flex-1 overflow-auto p-3">
              {open.render ? open.render() : open.cell ? <DetailCell {...open.cell} tall /> : null}
            </div>
          </motion.aside>
        </motion.div>
      )}
    </AnimatePresence>
  )

  // A line of chips under the tabs. Chips rather than plain links because the
  // grid below is chips-in-cards: the same shape says these two rows are one
  // family — what the agent has, and what it can reach.
  if (variant === "row") {
    return (
      <>
        <div className="flex flex-wrap items-center gap-1.5 py-0.5">
          {items.map((item) => (
            <EntityChip
              key={item.id}
              icon={item.icon}
              label={item.label}
              note={item.value}
              tone={item.alert ? "warn" : CHIP_TONE[item.tone]}
              onClick={() => setOpenId(item.id)}
            />
          ))}
        </div>
        {drawer}
      </>
    )
  }

  return (
    <>
      <DetailCard title="What it touches" subtitle="blast radius" icon={Radar} bare>
        {Object.entries(groups).map(([group, groupItems]) => (
          <div key={group} className="flex flex-wrap items-center gap-2 border-b border-hairline px-4 py-2.5 last:border-b-0">
            <span className="type-meta w-24 shrink-0 uppercase tracking-wide text-muted-foreground-soft">{group}</span>
            {groupItems.map((item) => (
              <EntityChip
                key={item.id}
                icon={item.icon}
                label={item.label}
                note={item.value}
                tone={item.alert ? "warn" : CHIP_TONE[item.tone]}
                onClick={() => setOpenId(item.id)}
              />
            ))}
          </div>
        ))}
      </DetailCard>

      {drawer}
    </>
  )
}
