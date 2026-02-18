import { Button } from "@/components/ui/button"
import { AgentTabs } from "@/components/layout/agent-tabs"
import { MoreHorizontal } from "lucide-react"
import Link from "next/link"

export function generateStaticParams() {
  return [{ agentId: "_" }]
}

export default function AgentDetailLayout({
  children,
  params,
}: {
  children: React.ReactNode
  params: Promise<{ agentId: string }>
}) {
  return <AgentDetailLayoutInner params={params}>{children}</AgentDetailLayoutInner>
}

async function AgentDetailLayoutInner({
  children,
  params,
}: {
  children: React.ReactNode
  params: Promise<{ agentId: string }>
}) {
  const { agentId } = await params

  return (
    <div className="flex flex-col h-full">
      <div className="bg-background border-b shrink-0">
        <div className="flex h-12 items-center justify-between px-4 sm:px-6">
          <div className="flex items-center gap-2">
            <Button variant="outline" size="sm" className="text-destructive border-destructive/30 hover:bg-destructive/10">
              Stop
            </Button>
            <Button variant="outline" size="sm" asChild>
              <Link href={`/agents/${agentId}/settings`}>Edit</Link>
            </Button>
            <Button variant="ghost" size="icon" className="h-8 w-8">
              <MoreHorizontal className="h-4 w-4" />
            </Button>
          </div>
        </div>
        <AgentTabs agentId={agentId} />
      </div>
      <div className="flex-1 overflow-y-auto">
        {children}
      </div>
    </div>
  )
}
