import { OrchestrationMissionRedirect } from "./redirect-client"

export function generateStaticParams() {
  return [{ missionId: "_" }]
}

// Missions UI retired; deep links redirect to /activity (the unified
// run surface). Stub kept one release for bookmark compat.
export default function Page() {
  return <OrchestrationMissionRedirect />
}
