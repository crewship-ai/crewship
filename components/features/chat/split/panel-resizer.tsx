"use client"

import { useCallback, useEffect, useRef, useState } from "react"

/** The right panel's share of the row, in percent: wide enough for a Sheet to be the main surface. */
export const SPLIT_DEFAULT = 45, SPLIT_MIN = 25, SPLIT_MAX = 80
const STEP = 5
const key = (userId: string) => `crewship.chat-split:v1:${userId}`
const clamp = (value: number) => Math.min(SPLIT_MAX, Math.max(SPLIT_MIN, Math.round(value)))

function stored(userId: string | null | undefined): number {
  if (!userId) return SPLIT_DEFAULT
  try {
    const value = Number(localStorage.getItem(key(userId)))
    return Number.isFinite(value) && value >= SPLIT_MIN && value <= SPLIT_MAX ? value : SPLIT_DEFAULT
  } catch { return SPLIT_DEFAULT }
}

/** The panel's width for this person, kept in this browser. */
export function useSplitWidth(userId: string | null | undefined): [number, (value: number) => void] {
  const [width, setWidth] = useState(() => stored(userId))
  useEffect(() => { setWidth(stored(userId)) }, [userId])
  const set = useCallback((value: number) => {
    const next = clamp(value)
    setWidth(next)
    if (userId) try { localStorage.setItem(key(userId), String(next)) } catch { /* not kept: the width still applies now */ }
  }, [userId])
  return [width, set]
}

/**
 * The divider between the chat (left) and the right panel: dragged with the pointer, moved with ← and → (Home: widest
 * panel, End: narrowest), double-click goes back to the default. A visible grip; the line itself is the target.
 */
export function PanelResizer({ value, onChange, controls }: { value: number; onChange: (value: number) => void; controls: string }) {
  const dragging = useRef<number | null>(null)
  const fromPointer = (element: HTMLElement, clientX: number) => {
    const row = element.parentElement?.getBoundingClientRect()
    if (row && row.width > 0) onChange(((row.right - clientX) / row.width) * 100)
  }
  return <div role="separator" aria-orientation="vertical" aria-label="Resize the panel" aria-controls={controls}
    aria-valuenow={value} aria-valuemin={SPLIT_MIN} aria-valuemax={SPLIT_MAX} aria-valuetext={`Panel ${value}% wide`} tabIndex={0}
    title="Drag to resize. Double-click to reset."
    className="group relative z-10 -mx-1 flex w-2 shrink-0 cursor-col-resize touch-none select-none items-center justify-center outline-none max-lg:hidden"
    onKeyDown={event => {
      const next = event.key === "ArrowLeft" ? value + STEP : event.key === "ArrowRight" ? value - STEP : event.key === "Home" ? SPLIT_MAX : event.key === "End" ? SPLIT_MIN : null
      if (next === null) return
      event.preventDefault(); onChange(next)
    }}
    onDoubleClick={() => onChange(SPLIT_DEFAULT)}
    onPointerDown={event => {
      if (event.button !== 0) return
      dragging.current = event.pointerId
      event.currentTarget.setPointerCapture?.(event.pointerId)
      event.preventDefault()
    }}
    onPointerMove={event => { if (dragging.current === event.pointerId) fromPointer(event.currentTarget, event.clientX) }}
    onPointerUp={event => { if (dragging.current === event.pointerId) { dragging.current = null; event.currentTarget.releasePointerCapture?.(event.pointerId) } }}
    onPointerCancel={() => { dragging.current = null }}>
    <span aria-hidden className="h-full w-px bg-border transition-colors group-hover:bg-primary/60 group-focus-visible:bg-primary" />
    <span aria-hidden className="absolute h-8 w-1.5 rounded-full border bg-background shadow-sm transition-colors group-hover:border-primary/60 group-focus-visible:border-primary" />
  </div>
}
