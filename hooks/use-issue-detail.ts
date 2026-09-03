"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useSearchParams } from "next/navigation"
import type { Mission } from "@/lib/types/mission"

/**
 * A selection that lives in the URL.
 *
 * The issues page used to hold the open issue in component state, which cost
 * three things at once: the link could not be shared, a refresh lost the
 * selection, and Back left /issues entirely instead of closing the detail.
 * `?project=` was the mirror-image bug — read on arrival, never written — so
 * the URL and the screen disagreed the moment anybody clicked.
 *
 * Writes go through `history.pushState`, not `router.push`:
 *
 *   - pushState, because a selection SHOULD be a history entry. That is what
 *     makes Back mean "close this" rather than "leave".
 *   - the History API rather than next/navigation, because a same-path query
 *     change through the router re-evaluates the dashboard layout subtree and
 *     flashes the auth provider's full-screen spinner. See
 *     hooks/use-shallow-search-param.ts, which learned this the hard way; the
 *     only difference here is push versus replace.
 *
 * Reads come from `useSearchParams` rather than `window.location`, because
 * the App Router renders the new route before window.location catches up — a
 * ⌘K jump to /issues?project=X from /issues would otherwise read the old URL.
 * The ref makes each incoming value win exactly once, so a re-render never
 * drags the reader back to a selection they have since clicked away from.
 */
export interface UrlSelectionOptions {
  /**
   * Older spellings of the same parameter that inbound links still use
   * (`/routines?routine=` where the page reads `?slug=`). Read when the key is
   * absent; rewritten to the key on the first write, so a link that arrived
   * under the alias leaves under the canonical name.
   */
  aliases?: readonly string[]
}

export function readUrlSelection(params: URLSearchParams, key: string, aliases: readonly string[] = []): string | null {
  const direct = params.get(key)
  if (direct) return direct
  for (const alias of aliases) {
    const v = params.get(alias)
    if (v) return v
  }
  return null
}

export function useUrlSelection(key: string, options: UrlSelectionOptions = {}) {
  const aliases = options.aliases ?? NO_ALIASES
  const fromUrl = readUrlSelection(useSearchParams(), key, aliases)
  const [value, setValue] = useState<string | null>(fromUrl)
  const applied = useRef<string | null>(fromUrl)

  useEffect(() => {
    if (applied.current === fromUrl) return
    applied.current = fromUrl
    setValue(fromUrl)
  }, [fromUrl])

  // Back / forward. The listener owns the state after a pop, so `applied` is
  // reset with it — otherwise the effect above would immediately re-apply the
  // value the reader just navigated away from.
  useEffect(() => {
    if (typeof window === "undefined") return
    const onPop = () => {
      const next = readUrlSelection(new URLSearchParams(window.location.search), key, aliases)
      applied.current = next
      setValue(next)
    }
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [key, aliases])

  const select = useCallback(
    (next: string | null, opts?: { replace?: boolean }) => {
      applied.current = next
      setValue(next)
      if (typeof window === "undefined") return
      // Read the live query so the OTHER selection param survives: opening an
      // issue inside a project must not throw away ?project=.
      const params = new URLSearchParams(window.location.search)
      if (next) params.set(key, next)
      else params.delete(key)
      for (const alias of aliases) params.delete(alias)
      const qs = params.toString()
      const url = qs ? `${window.location.pathname}?${qs}` : window.location.pathname
      if (window.location.pathname + window.location.search === url) return
      // `replace` is for corrections rather than navigations — an id nothing
      // matches, say. Pushing one would put the bad URL behind the good one,
      // so Back returns to it, the correction fires again, and the reader is
      // bounced forward every time they try to leave.
      if (opts?.replace) window.history.replaceState(null, "", url)
      else window.history.pushState(null, "", url)
    },
    [key, aliases],
  )

  return [value, select] as const
}

const NO_ALIASES: readonly string[] = []

/**
 * Which issue the /issues page has open.
 *
 * Deliberately thin. It used to also own the issue's comments and a
 * refresh-after-update chain with two sequencing guards, because the detail
 * pane was a dumb renderer fed from here. The detail is now
 * `IssueDetailSurface`, which fetches its own comments, activity, relations
 * and runs against the identifier — so this hook's whole job is the URL and
 * a lookup, and the guards it needed went with the work they were guarding.
 */
export function useIssueDetail({
  issues,
  onIssueSelected,
}: {
  /** The loaded issue list, for resolving the URL's identifier to a row. */
  issues: Mission[]
  /** Called when the reader opens an issue — clears the task detail-context. */
  onIssueSelected?: () => void
}) {
  const [selectedIdentifier, setSelectedIdentifier] = useUrlSelection("issue")

  const selectedIssue = useMemo(() => {
    if (!selectedIdentifier) return null
    return (
      issues.find((i) => i.identifier === selectedIdentifier) ??
      issues.find((i) => i.id === selectedIdentifier) ??
      null
    )
  }, [issues, selectedIdentifier])

  const handleIssueSelect = useCallback(
    (issue: Mission) => {
      const key = issue.identifier ?? issue.id
      // Clicking the open issue again closes it — the board's toggle.
      if (key === selectedIdentifier) {
        setSelectedIdentifier(null)
        return
      }
      setSelectedIdentifier(key)
      onIssueSelected?.()
    },
    [selectedIdentifier, setSelectedIdentifier, onIssueSelected],
  )

  const handleIssueClose = useCallback(() => {
    setSelectedIdentifier(null)
  }, [setSelectedIdentifier])

  return {
    selectedIdentifier,
    selectedIssue,
    handleIssueSelect,
    handleIssueClose,
  } as const
}
