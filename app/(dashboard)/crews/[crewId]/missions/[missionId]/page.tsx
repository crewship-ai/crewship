import { MissionDetailPageClient } from "./mission-detail-client"

export function generateStaticParams() {
  return [{ missionId: "_" }]
}

export default function MissionDetailPage() {
  return <MissionDetailPageClient />
}
