"use client"

import { CredentialsManager, SkillsManager } from "../agent-canvas-managers"
import { AgentConnectorsCard } from "@/components/features/integrations/composio/access-editor"

export interface SkillsTabProps {
  agentId: string
  agentSlug: string
  agentName: string
  agentCrew?: string | null
  workspaceId: string
  onAgentChanged: () => void
}

// "Skills & Tools" is the agent's *access* surface — what it can do and reach:
// Skills (capabilities) → Integrations (Composio apps + per-tool scope) →
// Credentials (the secrets those skills/integrations authenticate with). Kept
// distinct from Settings, which is behaviour/config (prompt, runtime, learning).
export function SkillsTab({
  agentId,
  agentSlug,
  agentName,
  agentCrew,
  workspaceId,
  onAgentChanged,
}: SkillsTabProps) {
  return (
    <div className="space-y-7">
      <SkillsManager agentId={agentId} agentSlug={agentSlug} workspaceId={workspaceId} onChange={onAgentChanged} />
      <section className="space-y-3">
        <h2 className="text-lg font-semibold">Integrations</h2>
        <AgentConnectorsCard
          agentId={agentId}
          agentName={agentName}
          agentCrew={agentCrew}
          workspaceId={workspaceId}
        />
      </section>
      <CredentialsManager agentId={agentId} agentSlug={agentSlug} workspaceId={workspaceId} onChange={onAgentChanged} />
    </div>
  )
}
