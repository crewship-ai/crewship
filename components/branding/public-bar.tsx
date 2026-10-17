import type * as React from "react"

import { cn } from "@/lib/utils"
import { CrewshipLogo } from "@/components/branding/crewship-logo"

/**
 * The top bar of a public page (Harbor 2, #3410) — `/p/<token>` and
 * `/shared-chat`. The app's own top bar without anything that needs an
 * account: the mark, a breadcrumb that says what this is, and at most the
 * page's one action. No bells, no command palette, no user menu, no
 * workspace — the reader has none of them, and each would fire a request
 * that 401s.
 *
 * 48 px on a desk, 52 px on a phone, like the app bar.
 */
export function PublicBar({
  crumb,
  actions,
  className,
}: {
  /** What the reader is looking at: "Shared artifact", "Shared chat". */
  crumb: React.ReactNode
  /** The page's one action, right-aligned. */
  actions?: React.ReactNode
  className?: string
}) {
  return (
    <header
      data-slot="public-bar"
      className={cn(
        "sticky top-0 z-10 flex h-[48px] shrink-0 items-center gap-2 border-b border-border bg-card px-4 max-md:h-[52px] md:px-5",
        className,
      )}
    >
      <CrewshipLogo tight aria-hidden className="h-[22px] w-auto shrink-0 text-foreground" />
      <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-body">
        <span className="font-semibold text-foreground">Crewship</span>
        <span className="text-muted-foreground-soft">/</span>
        <span className="truncate text-muted-foreground" aria-current="page">
          {crumb}
        </span>
      </nav>
      <div className="flex-1" />
      {actions}
    </header>
  )
}
