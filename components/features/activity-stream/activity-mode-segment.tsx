"use client"

import Link from "next/link"

import { cn } from "@/lib/utils"

/**
 * Activity / Spend — the top-bar segment of Activity. OWNER / ADMIN only:
 * Spend was an admin-only Journal tab and stays exactly as gated, so the
 * caller renders this only for them (ActivityWorkspace).
 *
 * Links, not buttons: each side is an address (`/activity`,
 * `/activity?mode=spend`) a person can open in a new tab or send.
 */
export function ActivityModeSegment({ mode }: { mode: "work" | "spend" }) {
  const items = [
    { id: "work", label: "Activity", href: "/activity" },
    { id: "spend", label: "Spend", href: "/activity?mode=spend" },
  ] as const
  return (
    <nav aria-label="Activity or spend" className="inline-flex shrink-0 gap-0.5 rounded-md border border-border bg-muted p-0.5">
      {items.map((item) => {
        const on = mode === item.id
        return (
          <Link
            key={item.id}
            href={item.href}
            aria-current={on ? "page" : undefined}
            className={cn(
              "kit-tap inline-flex h-[calc(var(--control-sm)-6px)] items-center rounded-sm px-2.5 text-label",
              on ? "bg-card font-semibold text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {item.label}
          </Link>
        )
      })}
    </nav>
  )
}
