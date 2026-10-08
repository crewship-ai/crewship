"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Activity } from "lucide-react"

import { SubBar } from "@/components/layout/sub-bar"
import { LedgerView } from "@/components/features/work/ledger-view"
import { cn } from "@/lib/utils"
import { ActivityStreamView } from "./activity-stream-view"

type Section = "overview" | "work" | "deliveries"

const LEDGER_LABEL = { work: "Work queue", deliveries: "Webhook deliveries" } as const

// /activity and its two ledgers.
//
// The ledgers were tabs above the page (#2979), then rows in the rail that
// swapped the page for a table under a back-bar. They now keep Activity's
// shape (#3012): the rail switches to the ledger's own rows — status, agents,
// events — sliding in from the right as a routine focus does, and the page
// beside it is drawn like the home. "‹ All activity" slides the run list back
// in from the left.
//
// `?section=` stays the address, so every existing deep link and the /work
// redirect still land. Only the selected view is mounted: opening a ledger must
// not also start the journal stream.

export function ActivityWorkspace({ workspaceId }: { workspaceId: string }) {
  const router = useRouter()
  const searchParams = useSearchParams()
  const requested = searchParams.get("section")
  const section: Section = requested === "work" || requested === "deliveries" ? requested : "overview"

  // Whether the overview is coming BACK from a ledger, so its rail slides in
  // from the left then — and not on the page's first paint.
  const cameFromLedger = React.useRef(false)
  const backFromLedger = section === "overview" && cameFromLedger.current
  React.useEffect(() => {
    cameFromLedger.current = section !== "overview"
  }, [section])

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
        <ActivityStreamView workspaceId={workspaceId} onOpenSection={selectSection} railEntersFromLeft={backFromLedger} />
      </div>
    )
  }

  return (
    <div className={cn(height, "flex min-h-0 flex-col bg-background")}>
      <SubBar icon={Activity} title="Activity" section={LEDGER_LABEL[section]} ariaLabel="Activity" />
      <LedgerView workspaceId={workspaceId} section={section} onSection={selectSection} onLeave={() => selectSection("overview")} />
    </div>
  )
}
