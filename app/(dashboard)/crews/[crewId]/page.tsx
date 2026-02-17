import { CrewDetailClient } from "./crew-detail"

export function generateStaticParams() {
  return [{ crewId: "_" }]
}

export default function CrewDetailPage() {
  return <CrewDetailClient />
}
