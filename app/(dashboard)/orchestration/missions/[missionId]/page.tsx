import { MissionModesClient } from "./mission-modes-client"

/**
 * Static-export stub — dynamic missions are resolved client-side at
 * runtime. The placeholder keeps `next build --output=export` happy
 * for the orchestration mission detail route.
 */
export function generateStaticParams() {
  return [{ missionId: "_" }]
}

export default function OrchestrationMissionPage() {
  return <MissionModesClient />
}
