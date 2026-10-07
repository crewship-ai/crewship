"use client"

import { useRouter, useSearchParams } from "next/navigation"
import { Activity, ChevronLeft, ChevronRight, ClipboardList, Webhook } from "lucide-react"

import { SubBar } from "@/components/layout/sub-bar"
import { WorkLayout } from "@/components/features/work/work-layout"
import { cn } from "@/lib/utils"
import { ActivityStreamView } from "./activity-stream-view"

type Section = "overview" | "work" | "deliveries"

const LEDGERS = [
  { key: "work", label: "Work queue", icon: ClipboardList },
  { key: "deliveries", label: "Webhook deliveries", icon: Webhook },
] as const

// /activity and its two ledgers.
//
// The ledgers were tabs ABOVE the page's sub-bar — a dark strip no other page
// has, sitting over the "Activity · N events" header that every page starts
// with (#2979). They are places, so the activity rail lists them as rows, and
// each opens the way an issue or a routine opens: the page header first, then a
// "‹ Back to activity" bar naming where you are.
//
// `?section=` stays the address, so every existing deep link and the /work
// redirect still land. Only the selected view is mounted: opening a ledger must
// not also start the journal stream.
export function ActivityWorkspace({ workspaceId }: { workspaceId: string }) {
  const router = useRouter()
  const searchParams = useSearchParams()
  const requested = searchParams.get("section")
  const section: Section = requested === "work" || requested === "deliveries" ? requested : "overview"

  function selectSection(value: Section) {
    const params = new URLSearchParams(searchParams.toString())
    if (value === "overview") params.delete("section")
    else params.set("section", value)
    const query = params.toString()
    router.push(query ? `/activity?${query}` : "/activity", { scroll: false })
  }

  const height = "h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))]"

  if (section === "overview") {
    return (
      <div className={cn(height, "min-h-0 overflow-hidden")}>
        <ActivityStreamView workspaceId={workspaceId} onOpenSection={selectSection} />
      </div>
    )
  }

  const current = LEDGERS.find((l) => l.key === section)!
  return (
    <div className={cn(height, "flex min-h-0 flex-col bg-background")}>
      <SubBar icon={Activity} title="Activity" section={current.label} ariaLabel="Activity" />
      {/* The Issues back-bar, to the class: the way out first, then where you
          are. The two ledgers sit side by side because a delivery opens its
          work, and the reader needs the way back to the delivery. */}
      <nav aria-label="Ledger" className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
        <button
          type="button"
          onClick={() => selectSection("overview")}
          className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          Back to activity
        </button>
        <ChevronRight aria-hidden className="h-3.5 w-3.5 shrink-0 text-muted-foreground/40" />
        {LEDGERS.map((l) => {
          const on = l.key === section
          const Icon = l.icon
          return (
            <button
              key={l.key}
              type="button"
              aria-current={on ? "page" : undefined}
              onClick={() => selectSection(l.key)}
              className={cn(
                "inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs transition-colors",
                on ? "bg-muted font-medium text-foreground" : "text-muted-foreground hover:bg-muted hover:text-foreground",
              )}
            >
              <Icon className="h-3.5 w-3.5" />
              {l.label}
            </button>
          )
        })}
      </nav>
      {/* Same entrance as an opened issue: 12px in from the right, 0.2s. Not
          keyed on the section — a delivery opening its work must keep the
          work it selected. */}
      <div className="min-h-0 flex-1 overflow-hidden animate-in fade-in-0 slide-in-from-right-3 duration-200 ease-out">
        <WorkLayout workspaceId={workspaceId} tab={section} onTabChange={selectSection} />
      </div>
    </div>
  )
}
