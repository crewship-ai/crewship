"use client"

import Link from "next/link"
import { ArrowRight, CornerUpRight } from "lucide-react"

import { EmptyState } from "@/components/layout/empty-state"
import { Button } from "@/components/ui/button"

/**
 * What a stale `?tab=` link shows for the beat before the redirect lands.
 *
 * Purely presentational — the layout owns the `router.replace`, because the
 * jump has to start before this renders, not after. It exists at all because a
 * redirect fired on mount leaves a blank panel until the route changes, and
 * because a link someone followed deliberately deserves one line saying where
 * it went. The link is the no-JS/failed-navigation floor, not the happy path.
 * The kit's empty state, so it reads like every other "nothing here" pane.
 */
export function SectionMoved({ href, label }: { href: string; label: string }) {
  return (
    <EmptyState
      size="inline"
      icon={CornerUpRight}
      title={`Moved to ${label}`}
      description="This section now lives with the thing it configures, so there is one place to change it and one place to audit it. Taking you there…"
    >
      <Button asChild size="sm" variant="outline" className="mt-4 gap-1.5">
        <Link href={href}>
          Go to {label}
          <ArrowRight className="size-3" aria-hidden />
        </Link>
      </Button>
    </EmptyState>
  )
}
