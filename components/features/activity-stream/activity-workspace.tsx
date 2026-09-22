"use client"

import { useRouter, useSearchParams } from "next/navigation"

import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { WorkLayout } from "@/components/features/work/work-layout"
import { ActivityStreamView } from "./activity-stream-view"

// Keep the stream's existing deep links intact when switching views. Only the
// selected view is mounted: opening the ledger must not also start the stream.
export function ActivityWorkspace({ workspaceId }: { workspaceId: string }) {
  const router = useRouter()
  const searchParams = useSearchParams()
  const requested = searchParams.get("section")
  const section = requested === "work" || requested === "deliveries" ? requested : "overview"

  function selectSection(value: string) {
    const params = new URLSearchParams(searchParams.toString())
    if (value === "overview") params.delete("section")
    else params.set("section", value)
    const query = params.toString()
    router.push(query ? `/activity?${query}` : "/activity", { scroll: false })
  }

  return (
    <Tabs value={section} onValueChange={selectSection} className="h-[calc(100dvh-var(--app-header-h)-var(--mobile-tab-bar-h))] min-h-0 gap-0">
      <div className="shrink-0 overflow-x-auto border-b border-hairline px-2">
        <TabsList aria-label="Activity views" variant="line" className="coarse:min-h-12">
          <TabsTrigger value="overview" className="coarse:min-h-12">Overview</TabsTrigger>
          <TabsTrigger value="work" className="coarse:min-h-12">Work</TabsTrigger>
          <TabsTrigger value="deliveries" className="coarse:min-h-12">Deliveries</TabsTrigger>
        </TabsList>
      </div>
      <TabsContent value={section} className="min-h-0 overflow-hidden">
        {section === "overview" ? (
          <ActivityStreamView workspaceId={workspaceId} />
        ) : (
          <WorkLayout workspaceId={workspaceId} tab={section} onTabChange={selectSection} />
        )}
      </TabsContent>
    </Tabs>
  )
}
