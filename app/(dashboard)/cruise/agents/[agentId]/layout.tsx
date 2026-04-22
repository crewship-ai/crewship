import { AgentLayoutShell } from "@/components/layout/agent-layout-shell"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default async function AgentDetailLayout({
  children,
  params,
}: {
  children: React.ReactNode
  params: Promise<{ agentId: string }>
}) {
  const { agentId } = await params

  return <AgentLayoutShell agentId={agentId}>{children}</AgentLayoutShell>
}
