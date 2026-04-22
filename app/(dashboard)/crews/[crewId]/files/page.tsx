import { CrewFilesClient } from "./crew-files-client"

export function generateStaticParams() {
  return [{ crewId: "_" }]
}

export default function CrewFilesPage() {
  return <CrewFilesClient />
}
