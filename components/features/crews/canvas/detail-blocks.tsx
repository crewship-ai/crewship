"use client"

import { useState } from "react"
import { motion, AnimatePresence } from "motion/react"
import { AlertTriangle, ArrowUpRight, X } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

import { DetailCell, type DetailCellProps } from "./detail-cell"

// =============================================================================
// The three blocks that sit above the cells on a detail screen, in the order
// the screen answers questions:
//
//   BlockingNotice — what this thing wants FROM YOU. Nothing outranks it.
//   NowRunning     — what it is doing right now.
//   ReachStrip     — what it can touch, collapsed to one row of chips.
//
// ReachStrip exists because the overview grew to eleven cells and stopped
// being readable. Relations that are rarely acted on (skills, tools, memory,
// notification channels) collapse into a single row; clicking one slides out
// the same DetailCell the grid would have shown. Nothing is lost, but the
// grid keeps four cells instead of eleven.
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
      className={cn(
        "flex flex-wrap items-center gap-3 rounded-[10px] border px-4 py-3",
        tone === "warn"
          ? "border-warn/30 bg-warn/[.07]"
          : "border-border bg-card",
      )}
    >
      <span className={cn("inline-flex shrink-0", tone === "warn" ? "text-warn" : "text-notice")}>
        <Icon className="h-4 w-4" />
      </span>
      <div className="min-w-0 flex-1 basis-60 text-body leading-snug">
        <b className="font-semibold">{title}</b> {body}
        {detail && <span className="mt-0.5 block text-label text-muted-foreground">{detail}</span>}
      </div>
      {actions.length > 0 && (
        <div className="flex shrink-0 gap-2">
          {actions.map((a) => (
            <button
              key={a.label}
              type="button"
              onClick={a.onClick}
              className={cn(
                "rounded-lg border px-3 py-1.5 text-label font-medium transition-colors",
                a.primary
                  ? "border-transparent bg-primary text-primary-foreground hover:bg-primary-hover"
                  : "border-border bg-surface-raised text-foreground hover:bg-white/[.09]",
              )}
            >
              {a.label}
            </button>
          ))}
        </div>
      )}
    </motion.div>
  )
}

export interface NowRunningProps {
  label: string
  /** e.g. "krok 3 / 5" — omitted when the backend reports no step count. */
  step?: string
  /** 0–100. Omitted renders an indeterminate shimmer instead of a fill. */
  percent?: number
  meta?: string
  icon?: LucideIcon
  onStop?: () => void
}

export function NowRunning({ label, step, percent, meta, icon: Icon, onStop }: NowRunningProps) {
  return (
    <motion.div
      initial={{ opacity: 0, y: -4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.24, ease: [0.22, 1, 0.36, 1] }}
      className="flex flex-wrap items-center gap-3 rounded-[10px] border border-primary/30 bg-gradient-to-b from-primary/[.08] to-transparent px-4 py-3"
    >
      <span className="h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-success" />
      <span className="inline-flex items-center gap-2 text-body font-semibold">
        {Icon && <Icon className="h-3.5 w-3.5" />}
        {label}
      </span>
      {step && (
        <span className="rounded-md bg-surface-raised px-2 py-0.5 text-micro text-muted-foreground">{step}</span>
      )}
      <span className="h-1.5 min-w-[110px] flex-1 basis-44 overflow-hidden rounded-full bg-surface-raised">
        <motion.span
          className="block h-full rounded-full bg-primary"
          initial={{ width: 0 }}
          animate={{ width: percent === undefined ? "40%" : `${percent}%` }}
          transition={{ duration: 0.7, ease: [0.22, 1, 0.36, 1] }}
        />
      </span>
      {meta && <span className="font-mono text-micro text-muted-foreground-soft">{meta}</span>}
      {onStop && (
        <button
          type="button"
          onClick={onStop}
          className="rounded-lg border border-border bg-surface-raised px-2.5 py-1 text-label transition-colors hover:bg-white/[.09]"
        >
          Zastavit
        </button>
      )}
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
  /** Rendered in the slide-out when the chip is clicked. */
  cell?: Omit<DetailCellProps, "className">
  /**
   * Escape hatch for relations that already have a full component (memory,
   * workspace). Rendered instead of `cell`, in a wider panel — pushing a
   * 700-line manager into a 420px rail would be worse than the tab it
   * replaced.
   */
  render?: () => React.ReactNode
  wide?: boolean
}

const REACH_BG: Record<ReachItem["tone"], string> = {
  primary: "bg-primary",
  success: "bg-success",
  warn: "bg-warn",
  purple: "bg-purple",
  notice: "bg-notice",
  gold: "bg-gold",
}

export function ReachStrip({ items }: { items: ReachItem[] }) {
  const [openId, setOpenId] = useState<string | null>(null)
  const open = items.find((i) => i.id === openId) ?? null

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-0.5 text-micro font-bold uppercase tracking-[.09em] text-muted-foreground-soft max-sm:hidden">
          Dosah
        </span>
        {items.map((item) => {
          const Icon = item.icon
          return (
            <button
              key={item.id}
              type="button"
              onClick={() => setOpenId(item.id)}
              className={cn(
                "inline-flex items-center gap-2 rounded-full border bg-card py-1 pl-1 pr-3 text-label transition-[border-color,transform,background-color]",
                "hover:-translate-y-px hover:border-primary/50 hover:bg-white/[.03]",
                item.alert ? "border-warn/45" : "border-border",
              )}
            >
              <span className={cn("grid h-[22px] w-[22px] place-items-center rounded-full text-white", REACH_BG[item.tone])}>
                <Icon className="h-3.5 w-3.5" />
              </span>
              {item.label}
              <span className={cn("font-mono text-micro", item.alert ? "text-warn" : "text-muted-foreground-soft")}>
                {item.value}
              </span>
            </button>
          )
        })}
      </div>

      <AnimatePresence>
        {open && (
          <motion.div
            className="fixed inset-0 z-50"
            initial={{ opacity: 0 }}
            animate={{ opacity: 1 }}
            exit={{ opacity: 0 }}
            transition={{ duration: 0.2 }}
          >
            <div
              className="absolute inset-0 bg-black/60 backdrop-blur-[3px]"
              onClick={() => setOpenId(null)}
              aria-hidden
            />
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
              <header className="flex items-center gap-2 border-b border-border px-4 py-3">
                <span className="text-default font-semibold">{open.label}</span>
                <button
                  type="button"
                  onClick={() => setOpenId(null)}
                  aria-label="Zavřít"
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
    </>
  )
}

/** Small link used in section headers — "Vše ↗". */
export function SectionLink({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <a href={href} className="ml-auto inline-flex items-center gap-1 text-label font-medium text-primary hover:underline">
      {children}
      <ArrowUpRight className="h-3 w-3" />
    </a>
  )
}
