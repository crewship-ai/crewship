"use client"

import Link from "next/link"
import { Bot } from "lucide-react"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import type { WorkspaceAgentIdentity } from "@/hooks/use-workspace-agent-directory"

export function RoutineAgentLink({ slug, agent, workspaceId }: { slug: string; agent?: WorkspaceAgentIdentity | null; workspaceId?: string }) {
  return <Link href={`/crews?agent=${encodeURIComponent(slug)}`} title={`Open ${agent?.name || slug}`} className="inline-flex max-w-full items-center gap-2 rounded-full border border-border/60 bg-muted/30 py-1 pl-1 pr-2.5 text-xs transition-colors hover:border-primary/40 hover:bg-muted focus-visible:outline focus-visible:outline-primary">
    {agent ? <AgentAvatar seed={agent.avatar_seed || agent.name} style={agent.avatar_style || agent.crew?.avatar_style || undefined} agentId={agent.id} avatarUrl={agent.avatar_url} workspaceId={workspaceId} className="h-6 w-6" alt="" /> : <span className="flex h-6 w-6 items-center justify-center rounded-full bg-purple/10 text-purple"><Bot className="h-3.5 w-3.5" /></span>}
    <span className="truncate">{agent?.name || slug}</span>
  </Link>
}
