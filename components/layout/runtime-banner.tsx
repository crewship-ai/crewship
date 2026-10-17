"use client"

import { useEffect, useState, useCallback, useRef } from "react"
import { AlertTriangle } from "lucide-react"
import Link from "next/link"
import { apiFetch } from "@/lib/api-fetch"
import { useIsInstanceAdmin } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { AppBanner, useAppBannerSlot } from "@/components/layout/app-banners"

export const DOCKER_HELP_URL = "https://docs.docker.com/get-docker/"

/**
 * The runtime banner (Harbor 2): "Docker is not running. Agents can’t start."
 * with How to fix. Dismissed until the next page load, or until the runtime
 * comes back and goes away again. It shares the app's one banner slot, below
 * the realtime banner and above the update notice.
 */
export function RuntimeBanner() {
  const [visible, setVisible] = useState(false)
  const [dismissed, setDismissed] = useState(false)
  const request = useRef<AbortController | null>(null)
  const instanceAdmin = useIsInstanceAdmin()

  const check = useCallback(async () => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    try {
      // apiFetch instead of raw fetch — without it, an expired session
      // returns 401 every 30s, the user never gets redirected to /login,
      // and the toolbar/banner sit on stale state forever.
      const res = await apiFetch("/api/v1/system/runtime", { signal: controller.signal })
      if (!res.ok || controller.signal.aborted) return
      const data = await res.json()
      if (controller.signal.aborted) return
      setVisible(!data.available)
      if (data.available) setDismissed(false)
    } catch {
      // silently ignore
    }
  }, [])

  useEffect(() => {
    check()
    const interval = setInterval(check, 30_000)
    const onFocus = () => check()
    window.addEventListener("focus", onFocus)
    return () => {
      request.current?.abort()
      clearInterval(interval)
      window.removeEventListener("focus", onFocus)
    }
  }, [check])

  const slot = useAppBannerSlot("runtime", visible && !dismissed)
  if (!slot) return null

  return (
    <AppBanner
      tone="warn"
      icon={AlertTriangle}
      data-state="runtime"
      onDismiss={() => setDismissed(true)}
      action={
        <span className="inline-flex flex-wrap items-center gap-2">
          {instanceAdmin && (
            <Button asChild size="sm" variant="ghost" className="page-banner-action">
              <Link href="/admin">Admin</Link>
            </Button>
          )}
          <Button asChild size="sm" variant="outline" className="page-banner-action">
            <a href={DOCKER_HELP_URL} target="_blank" rel="noopener noreferrer">How to fix</a>
          </Button>
        </span>
      }
    >
      <b>Docker is not running.</b> Agents can’t start.
    </AppBanner>
  )
}
