"use client"

import Link from "next/link"
import { ArrowRight, CornerUpRight } from "lucide-react"

/**
 * What a stale `?tab=` link shows for the beat before the redirect lands.
 *
 * Purely presentational — the layout owns the `router.replace`, because the
 * jump has to start before this renders, not after. It exists at all because a
 * redirect fired on mount leaves a blank panel until the route changes, and
 * because a link someone followed deliberately deserves one line saying where
 * it went. The anchor is the no-JS/failed-navigation floor, not the happy path.
 */
export function SectionMoved({ href, label }: { href: string; label: string }) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="mb-3 flex h-9 w-9 items-center justify-center rounded-lg bg-muted/50">
        <CornerUpRight className="h-4 w-4 text-muted-foreground" />
      </div>
      <div className="text-xs font-medium text-foreground/80">Moved to {label}</div>
      <p className="mt-1 max-w-sm text-[11px] text-muted-foreground">
        This section now lives with the thing it configures, so there is one
        place to change it and one place to audit it. Taking you there…
      </p>
      <Link
        href={href}
        className="mt-4 inline-flex items-center gap-1.5 text-[11px] font-medium text-primary hover:underline"
      >
        Go to {label}
        <ArrowRight className="size-3" />
      </Link>
    </div>
  )
}
