"use client"

import * as React from "react"
import type { SlideDeck } from "@/lib/slides/model"
import { SlideCanvas } from "./slide-canvas"
import { isNativeSlideDeck } from "./native-model"

const control = "min-h-9 coarse:min-h-12 rounded border px-3"

/** Presentation is a modal native view, not an export or durable write. */
export function SlidePresent({ deck, initialSlideId, onClose }: { deck: unknown; initialSlideId: string; onClose: () => void }) {
  if (!isNativeSlideDeck(deck) || !deck.slide_order.includes(initialSlideId)) return <p role="alert">This presentation format or target is unavailable.</p>
  return <NativePresentation deck={deck} initialSlideId={initialSlideId} onClose={onClose} />
}

function NativePresentation({ deck, initialSlideId, onClose }: { deck: SlideDeck; initialSlideId: string; onClose: () => void }) {
  const [index, setIndex] = React.useState(Math.max(0, deck.slide_order.indexOf(initialSlideId)))
  const root = React.useRef<HTMLDivElement>(null)
  const close = React.useRef(onClose)
  close.current = onClose
  React.useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    root.current?.focus()
    return () => { if (previous?.isConnected) previous.focus() }
  }, [])
  const current = Math.min(index, deck.slide_order.length - 1)
  return <div ref={root} role="dialog" aria-modal="true" aria-label="Present slides" tabIndex={-1} className="fixed inset-0 z-50 flex flex-col bg-black p-3 text-white outline-none" onKeyDown={event => {
    const target = event.target as HTMLElement
    if (target.closest("input,textarea,select,[contenteditable=true]")) return
    if (event.key === "Escape") { event.preventDefault(); close.current() }
    if (["ArrowRight", "PageDown"].includes(event.key)) { event.preventDefault(); setIndex(value => Math.min(deck.slide_order.length - 1, value + 1)) }
    if (["ArrowLeft", "PageUp"].includes(event.key)) { event.preventDefault(); setIndex(value => Math.max(0, value - 1)) }
    if (event.key === "Home") { event.preventDefault(); setIndex(0) }
    if (event.key === "End") { event.preventDefault(); setIndex(deck.slide_order.length - 1) }
    if (event.key === "Tab") {
      const buttons = Array.from(root.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? [])
      const first = buttons[0], last = buttons.at(-1)
      if (event.shiftKey && (document.activeElement === first || document.activeElement === root.current)) { event.preventDefault(); last?.focus() }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
    }
  }}>
    <div className="m-auto w-full" style={{ maxWidth: `min(100%, calc((100dvh - 160px) * ${deck.width} / ${deck.height}))` }}><SlideCanvas deck={deck} slideId={deck.slide_order[current]} /></div>
    <div className="flex shrink-0 flex-wrap items-center justify-center gap-3 py-2">
      <button className={control} disabled={current === 0} onClick={() => setIndex(current - 1)}>Previous slide</button>
      <span aria-live="polite">{current + 1} / {deck.slide_order.length}</span>
      <button className={control} disabled={current === deck.slide_order.length - 1} onClick={() => setIndex(current + 1)}>Next slide</button>
      <button className={control} onClick={onClose}>Close presentation</button>
    </div>
  </div>
}
