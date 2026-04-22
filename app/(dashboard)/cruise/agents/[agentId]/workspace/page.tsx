import { WorkspacePageClient } from "@/components/features/agents/workspace/workspace-page-client"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function WorkspacePage() {
  return <WorkspacePageClient />
}
