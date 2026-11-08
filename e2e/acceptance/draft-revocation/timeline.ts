// What the page would paint, step by step, recorded inside the page. A poll from
// the test sees a state some milliseconds late and cannot tell what was on screen
// first. A requestAnimationFrame callback runs right before a paint, so a sample
// taken there with `paint: true` is what the person sees.
import type { CDPSession, Page } from "@playwright/test"

export interface Sample {
  at: number
  /** What took this sample: "visible", "hidden", "mutation" or "paint". */
  why: string
  paint: boolean
  visibility: string
  /** The native editor frame: not there, hidden, transparent or shown. */
  frame: "none" | "hidden" | "transparent" | "shown"
  inert: boolean
  /** The Project source: the editor, the read-only recovery field, the kept-draft offer, or none. */
  source: "editor" | "recovery" | "offer" | "none"
  /** A native kept draft is on offer ("Local draft · version"). */
  offer: boolean
  status: string
}

/** Installed with addInitScript; runs in the top page only. */
export const recordTimeline = () => {
  if (window.top !== window) return
  const WATCH_MS = 6_000
  const samples: Sample[] = []
  const shot = () => {
    const frame = document.querySelector<HTMLElement>('iframe[title$=" editor"]')
    const style = frame ? getComputedStyle(frame) : null
    // A concealed field keeps its box, so its focus survives; opacity says it is unseen.
    const seen = (el: Element | null | undefined) => {
      if (!el || !el.getClientRects().length) return false
      for (let at: Element | null = el; at; at = at.parentElement) if (getComputedStyle(at).opacity === "0") return false
      return true
    }
    const has = (label: string) => seen(document.querySelector(`textarea[aria-label="${label}"]`))
    const restore = Array.from(document.querySelectorAll("button")).some((b) => b.textContent === "Restore source draft" && seen(b))
    return {
      visibility: document.visibilityState,
      frame: !style ? "none" as const : style.visibility === "hidden" ? "hidden" as const : style.opacity === "0" ? "transparent" as const : "shown" as const,
      inert: frame?.hasAttribute("inert") ?? false,
      source: has("Source file") ? "editor" as const : has("Source recovery draft") ? "recovery" as const : restore ? "offer" as const : "none" as const,
      offer: Array.from(document.querySelectorAll("span")).some((s) => s.textContent?.startsWith("Local draft · version")),
      status: document.querySelector('[role="status"]')?.textContent?.slice(0, 60) ?? "",
    }
  }
  // Tracked apart: a state first seen by a mutation is not yet a painted one, and
  // its paint must still be recorded or a flash would pass as never painted.
  let last = "", painted = ""
  const take = (why: string, paint = false) => {
    const state = shot(), key = JSON.stringify(state)
    if (key === (paint ? painted : last)) return
    last = key
    if (paint) painted = key
    samples.push({ at: Date.now(), why, paint, ...state })
  }
  // Only for a few seconds after a return: a page sampled all the time is a slower
  // page, and these cases are about timing.
  let until = 0, observer: MutationObserver | undefined
  const paint = () => {
    if (Date.now() > until) { observer?.disconnect(); observer = undefined; return }
    take("paint", true)
    requestAnimationFrame(paint)
  }
  document.addEventListener("visibilitychange", () => {
    take(document.visibilityState)
    if (document.visibilityState !== "visible") return
    painted = ""
    const running = Date.now() <= until
    until = Date.now() + WATCH_MS
    if (running) return
    observer = new MutationObserver(() => take("mutation"))
    observer.observe(document.documentElement, { subtree: true, childList: true, attributes: true, attributeFilter: ["style", "inert", "hidden"] })
    requestAnimationFrame(paint)
  }, true)
  ;(window as unknown as { __timeline: Sample[] }).__timeline = samples
}

export const timeline = (page: Page) => page.evaluate(() => (window as unknown as { __timeline?: Sample[] }).__timeline ?? [])

/** Samples from the tab's return on, with `ms` counted from that return. */
export async function sinceReturn(page: Page, returned: number) {
  const samples = await timeline(page)
  const back = samples.find((s) => s.why === "visible" && s.at >= returned)?.at ?? samples.find((s) => s.at >= returned && s.visibility === "visible")?.at
  if (back === undefined) throw new Error("the page never recorded its return")
  return { back, steps: samples.filter((s) => s.at >= back).map(({ at, ...rest }) => ({ ms: at - back, ...rest })) }
}

/** The frames painted since the return. Throws when there is none: a check over no
 * paint proves nothing. */
export function painted<T extends { paint: boolean }>(steps: T[]) {
  const frames = steps.filter((s) => s.paint)
  if (!frames.length) throw new Error("no paint was recorded after the return")
  return frames
}

/** Puts a hidden page into the frozen lifecycle state Chrome uses for background
 * tabs: its timers, stream readers and listeners stop until it is thawed. */
export async function freeze(cdp: CDPSession) { await cdp.send("Page.setWebLifecycleState", { state: "frozen" }) }
export async function thaw(cdp: CDPSession) { await cdp.send("Page.setWebLifecycleState", { state: "active" }) }
