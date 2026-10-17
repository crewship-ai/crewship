"use client"

import * as React from "react"
import { X } from "lucide-react"
import type { LucideIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"

/**
 * App banners (Harbor 2): the state of the whole app, under the top bar.
 * There are only three kinds — the realtime connection, the container runtime
 * and an available update — and at most ONE shows at a time. A page's own
 * offline banner (`OfflineBanner` in page-state.tsx) joins the same queue, so
 * "Reconnecting…" is never said twice on one screen.
 *
 * Every banner asks for a slot with `useAppBannerSlot(kind, wanted)` and
 * renders only while it holds it. The highest priority wins; two claims of
 * the same priority keep the order they were made in.
 */
export type AppBannerKind = "realtime" | "offline" | "runtime" | "update"

/**
 * Realtime > a page's offline banner > Runtime > Update. The app's realtime
 * banner outranks the page's: both say the same thing, and the app's one is
 * the one that turns into "Reconnected" when the socket returns.
 */
export const APP_BANNER_PRIORITY: Record<AppBannerKind, number> = {
  realtime: 4,
  offline: 3,
  runtime: 2,
  update: 1,
}

interface Claim {
  id: string
  kind: AppBannerKind
  order: number
}

interface QueueValue {
  claim: (id: string, kind: AppBannerKind) => void
  release: (id: string) => void
  winner: string | null
}

const Queue = React.createContext<QueueValue | null>(null)

/** Picks the claim that shows: highest priority, then the earliest claim. */
export function pickBanner(claims: readonly Claim[]): string | null {
  let best: Claim | null = null
  for (const c of claims) {
    if (
      !best ||
      APP_BANNER_PRIORITY[c.kind] > APP_BANNER_PRIORITY[best.kind] ||
      (APP_BANNER_PRIORITY[c.kind] === APP_BANNER_PRIORITY[best.kind] && c.order < best.order)
    ) {
      best = c
    }
  }
  return best?.id ?? null
}

export function AppBannerProvider({ children }: { children: React.ReactNode }) {
  const [claims, setClaims] = React.useState<Claim[]>([])
  const order = React.useRef(0)

  const claim = React.useCallback((id: string, kind: AppBannerKind) => {
    setClaims((prev) => {
      const existing = prev.find((c) => c.id === id)
      if (existing?.kind === kind) return prev
      const rest = prev.filter((c) => c.id !== id)
      return [...rest, { id, kind, order: existing?.order ?? order.current++ }]
    })
  }, [])

  const release = React.useCallback((id: string) => {
    setClaims((prev) => (prev.some((c) => c.id === id) ? prev.filter((c) => c.id !== id) : prev))
  }, [])

  const winner = pickBanner(claims)
  const value = React.useMemo(() => ({ claim, release, winner }), [claim, release, winner])
  return <Queue.Provider value={value}>{children}</Queue.Provider>
}

/**
 * Whether this banner may render now. Outside an AppBannerProvider (a unit
 * test, a public page) there is no queue, so a banner that wants to show does.
 */
export function useAppBannerSlot(kind: AppBannerKind, wanted: boolean): boolean {
  const queue = React.useContext(Queue)
  const id = React.useId()
  const claim = queue?.claim
  const release = queue?.release
  React.useLayoutEffect(() => {
    if (!claim || !release) return
    if (wanted) claim(id, kind)
    else release(id)
  }, [claim, release, id, kind, wanted])
  React.useLayoutEffect(() => {
    if (!release) return
    return () => release(id)
  }, [release, id])
  if (!queue) return wanted
  return wanted && queue.winner === id
}

export type AppBannerTone = "warn" | "ok" | "info"

/**
 * The one look of an app banner (v67 `.rt-banner`): tinted strip, icon, a
 * sentence whose first words are bold, then at most one action and an
 * optional ×. On a phone the action wraps under the text.
 */
export function AppBanner({
  tone,
  icon: Icon,
  children,
  action,
  onDismiss,
  live = "polite",
  className,
  ...rest
}: {
  tone: AppBannerTone
  icon: LucideIcon
  children: React.ReactNode
  /** One button or link (Retry now, How to fix, Release notes). */
  action?: React.ReactNode
  onDismiss?: () => void
  live?: "polite" | "off"
  className?: string
} & Omit<React.ComponentProps<"div">, "children">) {
  return (
    <div className="app-banner-slot">
      <div
        role="status"
        aria-live={live}
        aria-atomic="true"
        data-tone={tone}
        className={cn("page-banner", `page-banner-${tone}`, className)}
        {...rest}
      >
        <Icon className="size-4 shrink-0" aria-hidden />
        <span className="page-banner-text">{children}</span>
        <span className="page-banner-spacer" />
        {action}
        {onDismiss && (
          <Button variant="ghost" size="icon-sm" onClick={onDismiss} aria-label="Dismiss" className="page-banner-dismiss">
            <X className="size-3.5" />
          </Button>
        )}
      </div>
    </div>
  )
}
