"use client"

import * as React from "react"
import type { SlideDeck } from "@/lib/slides/model"
import { SlideCanvas } from "./slide-canvas"

const control = "min-h-9 coarse:min-h-[44px] rounded-[var(--radius-row)] border bg-card px-3 text-sm hover:bg-accent disabled:opacity-40"

/** One presentation between the audience window and its presenter window. */
export type PresenterMessage = { kind: "slide"; id: string } | { kind: "end" }

const SESSION = /^[a-z0-9]{16,64}$/

/** A fresh session id for one Presenter view; only the two windows that share it follow each other. */
export function newPresenterSession(): string {
  const bytes = new Uint8Array(12)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, byte => byte.toString(16).padStart(2, "0")).join("")
}

/** The session a presenter window was opened for (`?presenter=<session>`), or null. */
export function presenterSessionOf(search: string): string | null {
  const value = new URLSearchParams(search).get("presenter")
  return value && SESSION.test(value) ? value : null
}

/**
 * The same-origin channel of one presentation session. Every message is
 * checked: a slide must be one of this deck's, anything else is ignored. It
 * carries slide ids only, never notes or content.
 */
export function usePresenterChannel(session: string | null, order: readonly string[], onMessage: (message: PresenterMessage) => void): (message: PresenterMessage) => void {
  const channel = React.useRef<BroadcastChannel | null>(null)
  const listener = React.useRef(onMessage); listener.current = onMessage
  const ids = React.useRef(order); ids.current = order
  React.useEffect(() => {
    if (!session || typeof BroadcastChannel === "undefined") return
    const opened = new BroadcastChannel(`crewship-slides-presenter:${session}`)
    channel.current = opened
    opened.onmessage = event => {
      const data = event.data as unknown
      if (!data || typeof data !== "object") return
      const message = data as Record<string, unknown>
      if (message.kind === "end") listener.current({ kind: "end" })
      else if (message.kind === "slide" && typeof message.id === "string" && ids.current.includes(message.id)) listener.current({ kind: "slide", id: message.id })
    }
    return () => { opened.close(); if (channel.current === opened) channel.current = null }
  }, [session])
  return React.useCallback((message: PresenterMessage) => { try { channel.current?.postMessage(message) } catch { /* the other window is gone */ } }, [])
}

function elapsed(ms: number): string {
  const seconds = Math.max(0, Math.floor(ms / 1000))
  const hours = Math.floor(seconds / 3600), minutes = Math.floor(seconds / 60) % 60, rest = seconds % 60
  const two = (value: number) => String(value).padStart(2, "0")
  return hours ? `${hours}:${two(minutes)}:${two(rest)}` : `${two(minutes)}:${two(rest)}`
}

/**
 * Presenter view: the current slide, the next one, its speaker notes and the
 * elapsed time, for the presenter's own screen. It drives the audience
 * presentation in the other window, which never shows notes.
 */
export function SlidePresenterView({ deck, slideId, onSelect, onEnd, ended }: { deck: SlideDeck; slideId: string; onSelect: (id: string) => void; onEnd: () => void; ended: boolean }) {
  const order = deck.slide_order
  const index = Math.max(0, order.indexOf(slideId))
  const slide = deck.slides.find(entry => entry.id === order[index])
  const nextId = order[index + 1]
  const [start, setStart] = React.useState(() => Date.now())
  const [now, setNow] = React.useState(start)
  React.useEffect(() => {
    if (ended) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [ended])
  const root = React.useRef<HTMLDivElement>(null)
  React.useEffect(() => { root.current?.focus() }, [])
  const go = (position: number) => onSelect(order[Math.max(0, Math.min(order.length - 1, position))])
  return <div ref={root} role="dialog" aria-modal="true" aria-label="Presenter view" tabIndex={-1} data-presenter-view
    className="fixed inset-0 z-50 grid grid-rows-[auto_1fr_auto] gap-3 bg-background p-4 text-foreground outline-none"
    onKeyDown={event => {
      if (event.key === "Tab") {
        // Modal: focus stays among this view's controls.
        const buttons = Array.from(root.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? [])
        const first = buttons[0], last = buttons.at(-1)
        if (event.shiftKey && (document.activeElement === first || document.activeElement === root.current)) { event.preventDefault(); last?.focus() }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus() }
        return
      }
      if ((event.target as HTMLElement).closest("input,textarea,select,[contenteditable=true]")) return
      const next = ["ArrowRight", "PageDown"].includes(event.key) ? index + 1 : ["ArrowLeft", "PageUp"].includes(event.key) ? index - 1
        : event.key === "Home" ? 0 : event.key === "End" ? order.length - 1 : null
      if (next === null) return
      event.preventDefault(); go(next)
    }}>
    <header className="flex flex-wrap items-center gap-3 text-sm">
      <b className="font-semibold">Presenter view</b>
      <span className="text-muted-foreground">Only you see this window. The audience sees the slide.</span>
      <span className="ml-auto font-mono" aria-label="Elapsed time">{elapsed(now - start)}</span>
      <button type="button" className={control} onClick={() => { const value = Date.now(); setStart(value); setNow(value) }}>Reset timer</button>
    </header>
    {ended ? <p role="status" className="self-center justify-self-center text-sm text-muted-foreground">The presentation ended. You can close this window.</p>
      : <div className="grid min-h-0 gap-4 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <section aria-label="Current slide" className="grid min-h-0 content-start gap-2">
          <div className="overflow-hidden rounded-card border shadow-sm"><SlideCanvas deck={deck} slideId={order[index]} /></div>
        </section>
        <div className="grid min-h-0 content-start gap-3">
          <section aria-label="Next slide" className="grid gap-1">
            <h2 className="text-xs font-semibold text-muted-foreground">Next</h2>
            {nextId ? <div className="overflow-hidden rounded-card border opacity-90"><SlideCanvas deck={{ ...deck, slide_order: [nextId], slides: deck.slides.filter(entry => entry.id === nextId) }} slideId={nextId} /></div>
              : <p className="text-sm text-muted-foreground">End of the deck.</p>}
          </section>
          <section aria-label="Speaker notes" className="grid min-h-0 gap-1">
            <h2 className="text-xs font-semibold text-muted-foreground">Speaker notes · slide {index + 1}</h2>
            <p className="max-h-[40vh] overflow-auto whitespace-pre-wrap text-base">{slide?.notes.trim() || "No notes for this slide."}</p>
          </section>
        </div>
      </div>}
    <footer className="flex flex-wrap items-center justify-center gap-3">
      <button type="button" className={control} disabled={ended || index === 0} onClick={() => go(index - 1)}>Previous slide</button>
      <span aria-live="polite" className="font-mono text-sm">{index + 1} / {order.length}</span>
      <button type="button" className={control} disabled={ended || index === order.length - 1} onClick={() => go(index + 1)}>Next slide</button>
      <button type="button" className={control} disabled={ended} onClick={onEnd}>End presentation</button>
    </footer>
  </div>
}
