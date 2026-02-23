import { SkillDetailPageClient } from "./skill-detail-client"

export function generateStaticParams() {
  return [{ skillId: "_" }]
}

export default function SkillDetailPage() {
  return <SkillDetailPageClient />
}
