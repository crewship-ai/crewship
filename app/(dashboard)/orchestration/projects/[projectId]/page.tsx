import { ProjectDetailClient } from "./project-detail-client"

export function generateStaticParams() {
  return [{ projectId: "_" }]
}

export default function ProjectDetailPage() {
  return <ProjectDetailClient />
}
