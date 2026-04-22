import { Suspense } from "react"
import { SchedulePageClient } from "./schedule-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function SchedulePage() {
  return (
    <Suspense>
      <SchedulePageClient />
    </Suspense>
  )
}
