"use client"

import * as React from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { Activity } from "lucide-react"

import { SubBar } from "@/components/layout/sub-bar"
import { LedgerView } from "@/components/features/work/ledger-view"
import { JournalLedger } from "@/components/features/journal/journal-ledger"
import { JournalSpendView } from "@/components/features/journal/journal-spend-view"
import { useAbilities } from "@/hooks/use-abilities"
import { useJournalLookup } from "@/hooks/use-journal-lookup"
import { canSeeJournalViews, journalViewOf, lookupResolvers, memberActivityUrl } from "@/lib/journal-redirect"
import { cn } from "@/lib/utils"
import { ActivityModeSegment } from "./activity-mode-segment"
import { ActivityStreamView } from "./activity-stream-view"

export type ActivitySection = "overview" | "work" | "deliveries" | "log" | "runs"

const LEDGER_LABEL = { work: "Work queue", deliveries: "Webhook deliveries" } as const

// /activity and its ledgers.
//
// The ledgers were tabs above the page (#2979), then rows in the rail that
// swapped the page for a table under a back-bar. They now keep Activity's
// shape (#3012): the rail switches to the ledger's own rows — status, agents,
// events — sliding in from the right as a routine focus does, and the page
// beside it is drawn like the home. "‹ All activity" slides the run list back
// in from the left.
//
// The Journal lives here too (F-02), for OWNER / ADMIN only, exactly as it
// was gated before: `?section=log` (technical log), `?section=runs` (fleet
// runs) and `?mode=spend` (the Activity / Spend segment). Anyone else who
// opens one of those URLs is sent to the Activity list with the filters the
// list can express (lib/journal-redirect) — this is the one gate for them.
//
// `?section=` stays the address, so every existing deep link and the /work
// redirect still land. Only the selected view is mounted: opening a ledger must
// not also start the journal stream.

export function ActivityWorkspace({ workspaceId }: { workspaceId: string }) {
  const router = useRouter()
  const searchParams = useSearchParams()
  const search = searchParams.toString()
  const { role, loading: roleLoading } = useAbilities()
  const lookup = useJournalLookup()
  const admin = canSeeJournalViews(role)

  const journalView = journalViewOf(new URLSearchParams(search), "/activity")
  const requested = searchParams.get("section")
  const section: ActivitySection =
    requested === "work" || requested === "deliveries" || requested === "log" || requested === "runs" ? requested : "overview"

  // An admin-only view opened by someone else: replace it with the list.
  // `replace`, because the reader did not ask to be here; the role must be
  // known first so an admin's link is never rewritten while it loads.
  const denied = journalView !== null && !roleLoading && !admin
  React.useEffect(() => {
    if (!denied || !journalView) return
    router.replace(memberActivityUrl(new URLSearchParams(search), journalView, lookupResolvers(lookup)), { scroll: false })
  }, [denied, journalView, search, lookup, router])

  function selectSection(value: ActivitySection) {
    // The Journal views speak their own URL vocabulary (ms timestamps, `_id`
    // keys); carrying it into the list or back would misread it. Entering or
    // leaving one of them starts clean.
    const crossesJournal = value === "log" || value === "runs" || section === "log" || section === "runs"
    const params = crossesJournal ? new URLSearchParams() : new URLSearchParams(search)
    if (value === "overview") params.delete("section")
    else params.set("section", value)
    params.delete("mode")
    const query = params.toString()
    router.push(query ? `/activity?${query}` : "/activity", { scroll: false })
  }

  const height = "h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))]"

  // Never paint an admin-only surface before the role says so.
  if (journalView !== null && (roleLoading || !admin)) return null

  const mode = journalView === "spend" ? "spend" : "work"
  const segment = admin ? <ActivityModeSegment mode={mode} /> : undefined

  if (journalView === "spend") {
    return (
      <div className={cn(height, "flex min-h-0 flex-col bg-background")}>
        <SubBar icon={Activity} title="Activity" section="Spend" ariaLabel="Activity" actions={segment} />
        <main className="flex min-h-0 flex-1 flex-col overflow-hidden" aria-label="Spend">
          <JournalSpendView workspaceId={workspaceId} workspaceLoading={false} />
        </main>
      </div>
    )
  }

  if (section === "log" || section === "runs") {
    return (
      <div className={cn(height, "flex min-h-0 flex-col bg-background")}>
        <JournalLedger view={section} actions={segment} />
      </div>
    )
  }

  if (section === "overview") {
    return (
      <div className={cn(height, "min-h-0 overflow-hidden")}>
        <ActivityStreamView
          workspaceId={workspaceId}
          onOpenSection={selectSection}
          journalLedgers={admin}
          modeSegment={segment}
        />
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
