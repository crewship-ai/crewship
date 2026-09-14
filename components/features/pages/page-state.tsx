"use client"

/**
 * How the four freshness states look on the Pages SURFACE — the rail's facet
 * rows, the list icons, the overview's breakdown.
 *
 * The inside of a panel already has its vocabulary
 * (`panels/freshness.ts`: `panelStateWord` gives "current" / "stale" /
 * "failed" / "no data yet", the right-hand answer word in a card header). This
 * is the other half: the words §9b.1 pins for the STATUS facet — All · Fresh ·
 * Stale · Failed · Never produced — and one icon per state so a list row
 * carries its state without relying on colour alone (§3).
 */

import { CircleCheck, CircleDashed, CircleX, Clock } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import type { PanelState } from "@/components/features/pages/panels/types"
import type { DetailTone } from "@/components/ui/detail"

export interface PageStateMeta {
  /** The facet label, exactly as §9b.1 writes it. */
  label: string
  icon: LucideIcon
  /** Text tone token — never a hex. */
  tone: string
  dot: string
  /**
   * The same state as a `Pill` tone, for surfaces built on `ui/detail` —
   * the editor's cards say "Fresh" in a pill, the way an issue says its
   * status, rather than in coloured text beside an icon.
   */
  pill: DetailTone
}

export const PAGE_STATE_META: Record<PanelState, PageStateMeta> = {
  fresh: { label: "Fresh", icon: CircleCheck, tone: "text-success", dot: "bg-success", pill: "success" },
  stale: { label: "Stale", icon: Clock, tone: "text-warn", dot: "bg-warn", pill: "warn" },
  failed: { label: "Failed", icon: CircleX, tone: "text-destructive", dot: "bg-destructive", pill: "destructive" },
  never_produced: {
    label: "Never produced",
    icon: CircleDashed,
    tone: "text-muted-foreground",
    dot: "bg-muted-foreground/30",
    pill: "default",
  },
}

/**
 * Facet order: worst last is wrong here. §9b.1 writes the row as
 * "All · Fresh · Stale · Failed · Never produced" and that is the order a
 * reader scans, so it is the order the options render in.
 */
export const PAGE_STATE_ORDER = ["fresh", "stale", "failed", "never_produced"] as const

/**
 * Compile-time exhaustiveness. A state added to the closed vocabulary and not
 * to the list above makes `Exclude<…>` stop being `never`, and this constant
 * stops typechecking — a facet quietly missing an option is a filter that
 * hides rows, and that failure is invisible at runtime.
 */
export const PAGE_STATE_FACET_IS_EXHAUSTIVE: Exclude<
  PanelState,
  (typeof PAGE_STATE_ORDER)[number]
> extends never
  ? true
  : false = true

/**
 * A page with no readable state renders no glyph at all. §9b.4 forbids
 * inventing a fourth thing for "no data"; the em dash already means "no basis
 * to compute", and that is what the caller prints.
 */
export function pageStateMeta(state: PanelState | null): PageStateMeta | null {
  return state ? PAGE_STATE_META[state] : null
}
