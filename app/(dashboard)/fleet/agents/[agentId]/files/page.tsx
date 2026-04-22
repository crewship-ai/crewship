import { FilesPageClient } from "./files-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function FilesPage() {
  return <FilesPageClient />
}
