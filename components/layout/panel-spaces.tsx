"use client"

import * as React from "react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"

export interface SettingsSpace<T extends string> {
  key: T
  label: string
  icon: LucideIcon
  /** How much is in the space, when it is worth saying. */
  count?: React.ReactNode
}

/**
 * The spaces of a Settings drill page's panel (Harbor 2 PNL-02, v67
 * `spacesKit`): two to nine places that each show a different page body —
 * Crew links' By crew / All links / Matrix — listed under the way back,
 * each on its number key. 1–9 switch while focus is not in a field and no
 * dialog is open; the key is announced (`aria-keyshortcuts`) and printed.
 *
 * Settings-side until the kit's SecondaryPanel takes spaces (kit request in
 * the area PR); the markup is the kit row's (`row-interactive`, `row-hover`,
 * `row-selected`) so the move is a rename.
 */
export function SettingsSpaces<T extends string>({
  spaces,
  value,
  onChange,
  label = "Spaces",
  className,
}: {
  spaces: readonly SettingsSpace<T>[]
  value: T
  onChange: (key: T) => void
  label?: string
  className?: string
}) {
  useSpaceKeys(spaces.map((s) => s.key), onChange)
  return (
    <div role="tablist" aria-label={label} aria-orientation="vertical" data-slot="settings-spaces" className={cn("flex flex-col gap-px px-1.5 pt-2", className)}>
      {spaces.map((s, i) => {
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
              "kit-tap row-interactive row-hover w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-control",
              on && "row-selected font-medium",
            )}
          >
            <Icon className={cn("h-3.5 w-3.5 shrink-0", on ? "text-foreground" : "text-muted-foreground")} aria-hidden />
            <span className="min-w-0 flex-1 truncate">{s.label}</span>
            {s.count != null && <span className="shrink-0 font-mono text-micro tabular-nums text-muted-foreground">{s.count}</span>}
            <kbd aria-hidden className="shrink-0 rounded border border-border px-1 font-mono text-micro text-muted-foreground-soft">{i + 1}</kbd>
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
