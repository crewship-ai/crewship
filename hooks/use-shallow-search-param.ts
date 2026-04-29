"use client"

import { useCallback, useEffect, useState } from "react"
import { useSearchParams } from "next/navigation"

/**
 * Local state for a single URL search param that updates the URL via
 * window.history.replaceState — never through next/navigation. Use this
 * for in-page selection / tab state where calling router.replace would
 * force the dashboard layout subtree to re-evaluate (the chat-page-client
 * fix from feat/chat-ui-overhaul). The visible symptom of NOT using this
 * hook for same-path query updates is a brief full-screen Loader2 spinner
 * from app/(dashboard)/layout.tsx every time the user picks a tab or
 * selects an item — because the auth provider briefly re-suspends.
 *
 * Initial value comes from useSearchParams once at mount; subsequent
 * reads are from local state. popstate keeps it in sync with browser
 * back/forward. Deep-links and refresh still work — we own writes via
 * the History API directly so the URL bar stays correct.
 *
 * @example
 *   const [section, setSection] = useShallowSearchParam("section", "skills")
 *   <Tabs value={section ?? "skills"} onValueChange={setSection} />
 */
export function useShallowSearchParam(
  key: string,
  fallback: string | null = null,
): [string | null, (value: string | null) => void] {
  const searchParams = useSearchParams()
  const [value, setValue] = useState<string | null>(() => searchParams.get(key) ?? fallback)

  const set = useCallback(
    (next: string | null) => {
      setValue(next)
      if (typeof window === "undefined") return
      const params = new URLSearchParams(window.location.search)
      if (next === null || next === "" || next === fallback) {
        params.delete(key)
      } else {
        params.set(key, next)
      }
      const qs = params.toString()
      const url = qs ? `${window.location.pathname}?${qs}` : window.location.pathname
      if (window.location.pathname + window.location.search !== url) {
        window.history.replaceState(null, "", url)
      }
    },
    [key, fallback],
  )

  useEffect(() => {
    if (typeof window === "undefined") return
    const onPop = () => {
      const params = new URLSearchParams(window.location.search)
      setValue(params.get(key) ?? fallback)
    }
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [key, fallback])

  return [value, set]
}
