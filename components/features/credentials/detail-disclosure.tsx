"use client"

import type { ReactNode } from "react"
import { ChevronDown, type LucideIcon } from "lucide-react"

/** Keep secondary information accessible without competing with primary actions. */
export function DetailDisclosure({ title, icon: Icon, children }: {
  title: string
  icon: LucideIcon
  children: ReactNode
}) {
  return (
    <details className="group/disclosure rounded-xl border border-border bg-card">
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-xl px-4 py-3 text-xs font-medium text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <Icon className="h-4 w-4" aria-hidden="true" />
        {title}
        <ChevronDown className="ml-auto h-3.5 w-3.5 transition-transform group-open/disclosure:rotate-180" aria-hidden="true" />
      </summary>
      <div className="space-y-3 border-t border-border p-3">{children}</div>
    </details>
  )
}
