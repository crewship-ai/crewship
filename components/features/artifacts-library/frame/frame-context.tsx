"use client"

import * as React from "react"
import { createPortal } from "react-dom"
import type { LucideIcon } from "lucide-react"
import { cn } from "@/lib/utils"
import type { AgentMode, FrameLayout, SlotName } from "./types"

/** A drawer section a body mounted: a tab, or an agent mode's form (hidden from the tab row). */
export interface SectionMeta {
  id: string
  label: string
  icon?: LucideIcon
  /** Sort key among the kind's own tabs; common tabs keep their fixed order. */
  order?: number
  hidden?: boolean
}

interface FrameContextValue {
  layout: FrameLayout
  slotLayer: HTMLElement | null
  /** The phone's top row: view on the left, output on the right (see SLOT_POSITION). */
  topDock: { view: HTMLElement | null; output: HTMLElement | null }
  sections: ReadonlyMap<string, SectionMeta>
  register: (meta: SectionMeta) => () => void
  target: (id: string) => HTMLElement | null
  /** Opens the drawer on a tab, or closes it with null. */
  openDrawer: (tab: string | null) => void
  drawerTab: string | null
}

export const FrameContext = React.createContext<FrameContextValue | null>(null)

/** The open artifact's frame, for a body that needs its layout or drawer. Null outside a frame. */
export function useArtifactFrame(): Pick<FrameContextValue, "layout" | "openDrawer" | "drawerTab"> | null {
  const frame = React.useContext(FrameContext)
  return frame ? { layout: frame.layout, openDrawer: frame.openDrawer, drawerTab: frame.drawerTab } : null
}

/**
 * Where each slot floats over the stage. Desktop and tablet follow
 * `ART.slot`. On a phone, view and output share one top row (the frame's
 * dock): view fills it and scrolls sideways, output keeps the top-right
 * corner. Every body already leaves that band free, so output never floats
 * over the reading area. Tools are a bottom bar with nav above it, viewport
 * is hidden, panel is a bottom sheet.
 */
export const SLOT_POSITION: Record<SlotName, { wide: string; phone: string }> = {
  view: { wide: "left-3 top-3", phone: "min-w-0 flex-1 overflow-x-auto whitespace-nowrap [scrollbar-width:none] [&>*]:shrink-0" },
  output: { wide: "right-3 top-3", phone: "shrink-0" },
  tools: { wide: "bottom-3 left-1/2 -translate-x-1/2 max-w-[calc(100%-1.5rem)] overflow-x-auto [scrollbar-width:none]", phone: "inset-x-2 bottom-2 justify-start overflow-x-auto [scrollbar-width:none] [&>*]:shrink-0" },
  viewport: { wide: "right-3 bottom-3", phone: "hidden" },
  nav: { wide: "left-3 bottom-3", phone: "left-2 bottom-[3.75rem]" },
  panel: { wide: "left-3 top-16 bottom-16 w-[244px] flex-col items-stretch p-0", phone: "inset-x-0 bottom-0 h-[62%] flex-col items-stretch rounded-b-none rounded-t-xl p-0 z-20" },
}

/**
 * A floating control group inside the content, in the same place for the
 * same purpose in every kind:
 *   view     what am I looking at (mode, page, version)        top left
 *   output   present, play, run, export, refresh                top right
 *   tools    create and edit, ONLY while editing                bottom centre
 *   viewport zoom, fit, device width, counts                    bottom right
 *   nav      move through content (filmstrip, sheets, n / N)    bottom left
 *   panel    optional navigator (layers, outline)               left edge
 * Menus inside a slot must portal (components/ui DropdownMenu, Popover):
 * the phone view row scrolls and would clip an inline menu.
 */
export function ArtifactSlot({ name, label, className, children }: { name: SlotName; label?: string; className?: string; children: React.ReactNode }) {
  const frame = React.useContext(FrameContext)
  if (!frame?.slotLayer) return null
  const phone = frame.layout === "phone"
  const docked = phone && (name === "view" || name === "output")
  const host = docked ? frame.topDock[name as "view" | "output"] : frame.slotLayer
  if (!host) return null
  const position = SLOT_POSITION[name][phone ? "phone" : "wide"]
  return createPortal(
    <div role={name === "tools" || name === "view" || name === "nav" ? "toolbar" : "group"} aria-label={label ?? name}
      data-artifact-slot={name} data-layout={frame.layout}
      className={cn("pointer-events-auto z-10 flex items-center gap-0.5 rounded-lg border border-border/60 bg-card/95 p-[3px] shadow-md backdrop-blur", docked ? "relative" : "absolute", position, className)}>
      {children}
    </div>,
    host,
  )
}

/** An icon control for a slot (30 px, 44 px on touch). */
export const ArtifactTool = React.forwardRef<HTMLButtonElement, React.ButtonHTMLAttributes<HTMLButtonElement> & { icon: LucideIcon; label: string; on?: boolean; text?: React.ReactNode }>(
  function ArtifactTool({ icon: Icon, label, on, text, className, ...props }, ref) {
    return <button ref={ref} type="button" aria-label={label} title={label} aria-pressed={on} {...props}
      className={cn("inline-flex h-[30px] min-w-[30px] items-center justify-center gap-1.5 rounded-[var(--radius-row)] px-[7px] text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40 coarse:min-h-[44px] coarse:min-w-[44px]",
        on && "bg-primary/12 text-primary-hover", className)}>
      <Icon aria-hidden className="size-4" />{text && <span>{text}</span>}
    </button>
  })

export const ArtifactToolSeparator = () => <span aria-hidden className="mx-[3px] h-[18px] w-px shrink-0 bg-border" />

function useSection(meta: SectionMeta) {
  const frame = React.useContext(FrameContext)
  const register = frame?.register
  const { id, label, icon, order, hidden } = meta
  React.useEffect(() => register?.({ id, label, icon, order, hidden }), [register, id, label, icon, order, hidden])
  return frame?.target(id) ?? null
}

/**
 * Content for one drawer tab, from the body that owns its data. Mounting it
 * is what makes the tab (and its header button) appear; unmounting removes
 * it. `tab` is "comments", "history", "info" or a kind tab id. The content
 * stays mounted while the drawer is closed, so its state survives.
 */
export function ArtifactDrawerSection({ tab, label, icon, order, children }: { tab: string; label: string; icon?: LucideIcon; order?: number; children: React.ReactNode }) {
  const target = useSection({ id: tab, label, icon, order })
  return target ? createPortal(children, target) : null
}

/** An agent mode the kind sends through its own existing form (Craft task, comment queue). Mounted only while the backend admits it. */
export function ArtifactAgentMode({ mode, children }: { mode: Exclude<AgentMode, "ask">; children: React.ReactNode }) {
  const target = useSection({ id: `agent:${mode}`, label: mode, hidden: true })
  return target ? createPortal(children, target) : null
}
