import { MissionTimelineClient } from "./mission-timeline-client"

/**
 * Static-export stub — dynamic missions are resolved client-side at
 * runtime. The placeholder keeps `next build --output=export` happy.
 */
export function generateStaticParams() {
  return [{ id: "_" }]
}

export default function MissionTimelinePage() {
  return <MissionTimelineClient />
}
