"use client"

import { useCallback, useMemo } from "react"
import { useRouter } from "next/navigation"

/**
 * List → detail → Back on a phone (phone pilot, MOBIL-DOPORUCENI 2a §3).
 *
 *  - `open(href)` pushes the detail and notes, in session storage, that the
 *    entry below it in the history is this area's list.
 *  - `back()` goes back in the history when that is true — so the app's Back
 *    and the browser's Back land on the same entry — and otherwise (a direct
 *    link, a new tab) replaces the detail with the area's list. It never
 *    leaves the app: plain `history.back()` would, from a direct link.
 *  - `saveList` / `readList` and `rememberScroll` / `restoreScroll` keep the
 *    list's search, filters and scroll while the list is not mounted, and
 *    across a reload (session storage is per tab and survives one).
 *
 * Everything is keyed by area AND workspace: switching workspace never brings
 * back the list state of the one you left. Without a workspace id nothing is
 * stored.
 */

interface Stored<S> {
  /** The detail URL `open` pushed (pathname + search). */
  detail?: string | null
  state?: S | null
  scroll?: number | null
}

export function listDetailKey(area: string, workspaceId: string): string {
  return `crewship.list-detail:${area}:${workspaceId}`
}

function read<S>(key: string | null): Stored<S> {
  if (!key) return {}
  try {
    const raw = sessionStorage.getItem(key)
    return raw ? (JSON.parse(raw) as Stored<S>) : {}
  } catch {
    return {}
  }
}

function write<S>(key: string | null, patch: Stored<S>) {
  if (!key) return
  try {
    sessionStorage.setItem(key, JSON.stringify({ ...read<S>(key), ...patch }))
  } catch {
    // Private mode or a full quota: the list simply starts fresh.
  }
}

function here(): string {
  return typeof window === "undefined" ? "" : window.location.pathname + window.location.search
}

// A reload brings the list back: the address the page was reloaded at counts
// as returning for as long as it is the address (the page may mount more than
// once while it loads); a client navigation elsewhere ends it.
let reloadedAt: string | null | undefined
function reloadedHere(): boolean {
  if (typeof window === "undefined" || typeof performance === "undefined") return false
  if (reloadedAt === undefined) {
    const nav = performance.getEntriesByType?.("navigation")?.[0] as PerformanceNavigationTiming | undefined
    reloadedAt = nav?.type === "reload" ? here() : null
  }
  return reloadedAt !== null && reloadedAt === here()
}

export interface PhoneListDetail<S> {
  /**
   * True when the list is being returned to — a detail opened from it is (or
   * was just) on screen, or the page was reloaded — rather than entered anew,
   * which opens the area's default (MOBIL-DOPORUCENI §2, "Běžný vstup").
   */
  returning: () => boolean
  /** The list is on screen again: the detail under it is gone. */
  settle: () => void
  open: (href: string, options?: { replace?: boolean }) => void
  back: () => void
  saveList: (state: S) => void
  readList: () => S | null
  rememberScroll: (el: HTMLElement | null) => void
  /** Applies a remembered scroll; true once it did (the list was tall enough). */
  restoreScroll: (el: HTMLElement | null) => boolean
}

export function usePhoneListDetail<S>({
  area,
  workspaceId,
  listHref,
}: {
  area: string
  workspaceId: string | null | undefined
  /** Where Back goes when the history has no list under the detail. */
  listHref: string
}): PhoneListDetail<S> {
  const router = useRouter()
  const key = workspaceId ? listDetailKey(area, workspaceId) : null

  const open = useCallback((href: string, options?: { replace?: boolean }) => {
    // Replacing one detail with the next (J / K) keeps the list under it.
    const below = options?.replace ? read<S>(key).detail != null : true
    write<S>(key, { detail: below ? href : null })
    if (options?.replace) router.replace(href)
    else router.push(href)
  }, [key, router])

  const back = useCallback(() => {
    const { detail } = read<S>(key)
    write<S>(key, { detail: null })
    if (detail && detail === here()) window.history.back()
    else router.replace(listHref)
  }, [key, listHref, router])

  const returning = useCallback(() => read<S>(key).detail != null || reloadedHere(), [key])
  const settle = useCallback(() => write<S>(key, { detail: null }), [key])

  const saveList = useCallback((state: S) => write<S>(key, { state }), [key])
  const readList = useCallback(() => read<S>(key).state ?? null, [key])

  const rememberScroll = useCallback((el: HTMLElement | null) => {
    if (el) write<S>(key, { scroll: el.scrollTop })
  }, [key])

  const restoreScroll = useCallback((el: HTMLElement | null) => {
    const scroll = read<S>(key).scroll
    if (!el || scroll == null) return false
    // Not tall enough yet (rows still loading): try again later.
    if (el.scrollHeight - el.clientHeight < scroll) return false
    el.scrollTop = scroll
    return true
  }, [key])

  return useMemo(
    () => ({ open, back, returning, settle, saveList, readList, rememberScroll, restoreScroll }),
    [open, back, returning, settle, saveList, readList, rememberScroll, restoreScroll],
  )
}
