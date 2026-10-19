"use client"

import { useEffect, useState } from "react"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"
import type { CrewMember } from "@/lib/types/crew"

import { CrCard, CrNote } from "./cr-card"
import { CrewMembers } from "./crew-members"
import { CrewBudgetCard } from "./crew-budget"
import { CrewCommandLine } from "./crew-yaml"
import { SettingsTab } from "./crew-canvas-tabs/settings-tab"
import type { AgentSummary, CrewRecord } from "./crew-canvas-tabs/types"

export { crewCommands } from "./crew-yaml"

/**
 * C-SET: what the Crew administration dialog held, on the page — profile,
 * autonomy, people, the command line and the danger zone (ADMIN+ only).
 */
export function CrewSettings({
  workspaceId,
  crew,
  agents,
  patch,
  onApplyAvatarStyle,
  onDelete,
}: {
  workspaceId: string
  crew: CrewRecord
  agents: AgentSummary[]
  patch: (body: Record<string, unknown>) => Promise<void>
  onApplyAvatarStyle: (resetOverrides: boolean) => void
  onDelete?: () => void
}) {
  const { role } = useAbilities()
  const isAdmin = role === "OWNER" || role === "ADMIN"
  const [members, setMembers] = useState<CrewMember[] | null>(null)
  const [membersFailed, setMembersFailed] = useState(false)

  useEffect(() => {
    let cancelled = false
    setMembersFailed(false)
    apiFetch(`/api/v1/crews/${crew.id}/members?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((data: CrewMember[]) => { if (!cancelled) setMembers(Array.isArray(data) ? data : []) })
      .catch(() => { if (!cancelled) { setMembers([]); setMembersFailed(true) } })
    return () => { cancelled = true }
  }, [crew.id, workspaceId])

  return (
    <div className="space-y-4" data-testid="crew-settings">
      <SettingsTab
        workspaceId={workspaceId}
        crew={crew}
        agentsForCrew={agents}
        patch={patch}
        applyAvatarStyle={onApplyAvatarStyle}
        onDelete={onDelete}
      />

      <CrCard title="People" concept="settings" count={members?.length}>
        {membersFailed ? (
          <CrNote tone="error">People could not be loaded.</CrNote>
        ) : members === null ? (
          <CrNote>Loading…</CrNote>
        ) : (
          <div className="border-t border-border p-3">
            <p className="mb-2 text-label text-muted-foreground">Everyone with a workspace role can see this crew. A crew role can only raise someone’s workspace role here.</p>
            <CrewMembers members={members} crewId={crew.id} workspaceId={workspaceId} canEdit={isAdmin} onMembersChange={setMembers} />
          </div>
        )}
      </CrCard>

      {isAdmin && <CrewBudgetCard workspaceId={workspaceId} crewId={crew.id} />}

      <CrewCommandLine workspaceId={workspaceId} crew={crew} />
    </div>
  )
}
