"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { SidebarRowTile } from "@/components/layout/row-tile"

export interface PanelSpace<T extends string> {
  key: T
  label: string
  icon: LucideIcon
  /** How much is in the space, when it is worth saying. */
  count?: React.ReactNode
}

/**
 * The spaces of a side panel (Harbor 2 PNL-02, v67 `spacesKit`): two to nine places that each show a different page body —
 * Crew links' By crew / All links / Matrix — listed under the way back,
 * each on its number key. 1–9 switch while focus is not in a field and no
 * dialog is open; the key is announced (`aria-keyshortcuts`) and printed.
 *
 * SecondaryPanel takes them as `spaces`; DrillPage as `spaces` too (SET-07).
 * The markup is the kit row's (`row-interactive`, `row-hover`, `row-selected`).
 */
export function PanelSpaces<T extends string>({
  items,
  value,
  onChange,
  label = "Spaces",
  className,
}: PanelSpacesConfig<T> & { className?: string }) {
  useSpaceKeys(items.map((s) => s.key), onChange)
  return (
    <div role="tablist" aria-label={label} aria-orientation="vertical" data-slot="panel-spaces" className={cn("mx-2 mt-2 flex flex-col gap-px rounded-lg border border-border bg-card p-1", className)}>
      {items.map((s, i) => {
        const on = s.key === value
        const Icon = s.icon
        return (
          <button
            key={s.key}
            type="button"
            role="tab"
            aria-selected={on}
            aria-keyshortcuts={String(i + 1)}
            title={`${s.label} · ${i + 1}`}
            tabIndex={on ? 0 : -1}
            data-drill-close
            onClick={() => onChange(s.key)}
            className={cn(
              "kit-tap row-interactive row-hover min-h-[30px] w-full items-center gap-2 rounded-md px-2 py-1 text-left text-control",
              on && "row-selected font-medium",
            )}
          >
            <SidebarRowTile icon={Icon} active={on} />
            <span className="min-w-0 flex-1 truncate">{s.label}</span>
            {s.count != null && <span className="shrink-0 text-micro tabular-nums text-muted-foreground">{s.count}</span>}
          </button>
        )
      })}
    </div>
  )
}

/** 1–9 pick the n-th space, unless the key is meant for something else: a
 *  field, a modifier chord, or an open dialog (whose own keys win). */
export function useSpaceKeys<T extends string>(keys: readonly T[], onChange: (key: T) => void) {
  const ref = React.useRef({ keys, onChange })
  React.useEffect(() => { ref.current = { keys, onChange } })
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return
      if (!/^[1-9]$/.test(e.key)) return
      const t = e.target as HTMLElement | null
      if (t?.closest?.("input, textarea, select, [contenteditable=''], [contenteditable=true]")) return
      if (document.querySelector("[role=dialog][data-state=open], [role=alertdialog][data-state=open]")) return
      const key = ref.current.keys[Number(e.key) - 1]
      if (key === undefined) return
      e.preventDefault()
      ref.current.onChange(key)
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [])
}

/** What a panel is given to list its spaces. */
export interface PanelSpacesConfig<T extends string = string> {
  items: readonly PanelSpace<T>[]
  value: T
  onChange: (key: T) => void
  /** The tablist's name; "Spaces" by default. */
  label?: string
}
