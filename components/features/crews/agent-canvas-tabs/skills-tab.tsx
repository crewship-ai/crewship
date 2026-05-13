"use client"

import { CredentialsManager, SkillsManager } from "../agent-canvas-managers"

export interface SkillsTabProps {
  agentId: string
  agentSlug: string
  workspaceId: string
  onAgentChanged: () => void
}

export function SkillsTab({ agentId, agentSlug, workspaceId, onAgentChanged }: SkillsTabProps) {
  return (
    <div className="space-y-7">
      <SkillsManager agentId={agentId} agentSlug={agentSlug} workspaceId={workspaceId} onChange={onAgentChanged} />
      <CredentialsManager agentId={agentId} agentSlug={agentSlug} workspaceId={workspaceId} onChange={onAgentChanged} />
    </div>
  )
}
