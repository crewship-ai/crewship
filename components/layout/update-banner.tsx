"use client"

import { useEffect, useState, useCallback } from "react"
import { ArrowUpCircle } from "lucide-react"
import { apiFetch } from "@/lib/api-fetch"
import { Button } from "@/components/ui/button"
import { AppBanner, useAppBannerSlot } from "@/components/layout/app-banners"

type VersionInfo = {
  current: string
  latest: string | null
  newer: boolean
  url: string | null
}

// Local-storage key for the "dismissed" flag. Scoping by version means a
// user who clicks Dismiss on v0.1.5 still sees the banner when v0.1.6 lands;
// otherwise they'd silently miss future updates.
export const DISMISS_KEY_PREFIX = "crewship.update-banner.dismissed:"

/**
 * The update notice (Harbor 2 info banner): "Crewship 0.9 is available. You
 * have 0.8." with Release notes and ×. Lowest priority of the app's one
 * banner slot: it waits while the connection or the runtime needs attention.
 */
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

  const slot = useAppBannerSlot("update", Boolean(info?.newer) && !dismissed)
  if (!slot || !info) return null

  return (
    <AppBanner
      tone="info"
      icon={ArrowUpCircle}
      data-state="update"
      onDismiss={() => {
        if (dismissKey) {
          try {
            localStorage.setItem(dismissKey, "1")
          } catch {
            // ignore
          }
        }
        setDismissed(true)
      }}
      action={
        info.url ? (
          <Button asChild size="sm" variant="outline" className="page-banner-action">
            <a href={info.url} target="_blank" rel="noopener noreferrer">Release notes</a>
          </Button>
        ) : undefined
      }
    >
      <b>
        Crewship <span className="font-mono">{info.latest}</span> is available.
      </b>{" "}
      You have <span className="font-mono">{info.current}</span>.
    </AppBanner>
  )
}
