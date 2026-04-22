import { SkillsPageClient } from "./skills-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function SkillsPage() {
  return <SkillsPageClient />
}
