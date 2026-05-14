"use client"

import { useEffect, useState, useCallback } from "react"
import { ArrowUpCircle, X } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"

type VersionInfo = {
  current: string
  latest: string | null
  newer: boolean
  url: string | null
}

// Local-storage key for the "dismissed" flag. Scoping by version means a
// user who clicks Dismiss on v0.1.5 still sees the banner when v0.1.6 lands;
// otherwise they'd silently miss future updates.
const DISMISS_KEY_PREFIX = "crewship.update-banner.dismissed:"

export function UpdateBanner() {
  const [info, setInfo] = useState<VersionInfo | null>(null)
  const [dismissed, setDismissed] = useState(false)

  const dismissKey = info?.latest ? DISMISS_KEY_PREFIX + info.latest : null

  // Hydrate the dismissed flag once we know which version is being offered.
  useEffect(() => {
    if (!dismissKey) return
    try {
      setDismissed(localStorage.getItem(dismissKey) === "1")
    } catch {
      // localStorage may be unavailable (incognito, SSR fallback)
    }
  }, [dismissKey])

  const check = useCallback(async () => {
    try {
      const res = await apiFetch("/api/v1/system/version")
      if (!res.ok) return
      const data = (await res.json()) as VersionInfo
      setInfo(data)
    } catch {
      // Update check is best-effort; a transient failure should never
      // surface as a scary UI error.
    }
  }, [])

  useEffect(() => {
    check()
    // 1h polling — the backend itself caches for 24h, so this is mostly
    // about reflecting an updated server-side check when the user keeps
    // the dashboard open across a release.
    const interval = setInterval(check, 60 * 60 * 1000)
    const onFocus = () => check()
    window.addEventListener("focus", onFocus)
    return () => {
      clearInterval(interval)
      window.removeEventListener("focus", onFocus)
    }
  }, [check])

  if (!info?.newer || dismissed) return null

  return (
    <div className="flex items-center gap-2 bg-blue-50 border-b border-blue-200 px-4 py-2 text-xs">
      <ArrowUpCircle className="h-3.5 w-3.5 text-blue-600 shrink-0" />
      <span className="text-blue-800">
        Crewship <span className="font-mono">{info.latest}</span> is available
        (you have <span className="font-mono">{info.current}</span>).{" "}
        {info.url && (
          <a
            href={info.url}
            target="_blank"
            rel="noopener noreferrer"
            className="underline font-medium"
          >
            Release notes
          </a>
        )}
      </span>
      <button
        onClick={() => {
          if (dismissKey) {
            try {
              localStorage.setItem(dismissKey, "1")
            } catch {
              // ignore
            }
          }
          setDismissed(true)
        }}
        className="ml-auto text-blue-600 hover:text-blue-800"
        aria-label="Dismiss"
      >
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  )
}
