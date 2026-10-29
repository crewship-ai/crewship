"use client"

import { useQuery, useQueryClient } from "@tanstack/react-query"
import { useCrewManagement } from "@/hooks/use-crew-management"
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
  const management = useCrewManagement(workspaceId,crew.id)
  const isAdmin = management.trusted && (role === "OWNER" || role === "ADMIN")
  const client = useQueryClient()
  const rosterKey = ["crew-members",workspaceId,crew.id,management.actorID]
  const roster = useQuery({
    queryKey:rosterKey,enabled:management.trusted && !!management.actorID,retry:false,staleTime:0,gcTime:0,
    queryFn:async({signal})=>{
      const response = await apiFetch(`/api/v1/crews/${crew.id}/members?workspace_id=${encodeURIComponent(workspaceId)}`,{signal})
      if(!response.ok)throw new Error("People could not be loaded")
      const rows:unknown = await response.json()
      if(!Array.isArray(rows))throw new Error("People could not be loaded")
      return rows as CrewMember[]
    },
  })
  const members = management.trusted && management.actorID && !roster.isError ? roster.data ?? null : null
  const setMembers = (rows:CrewMember[])=>client.setQueryData(rosterKey,rows)

  return (
    <div className="space-y-4" data-testid="crew-settings">
      <SettingsTab
        workspaceId={workspaceId}
        crew={crew}
        agentsForCrew={agents}
        patch={patch}
        applyAvatarStyle={onApplyAvatarStyle}
        onDelete={onDelete}
        trailing={<>
        <CrCard title="People" concept="settings" count={members?.length}>
          {roster.isError ? (
            <CrNote tone="error">People could not be loaded.</CrNote>
          ) : members === null ? (
            <CrNote>Loading…</CrNote>
          ) : (
            <div className="border-t border-border p-3">
              <p className="mb-2 text-label text-muted-foreground">Everyone with a workspace role can see this crew. A crew role can only raise someone’s workspace role here.</p>
              {management.loading && <CrNote>Checking crew management access…</CrNote>}
              {management.error && <CrNote tone="error">Crew management access could not be checked. <button type="button" onClick={()=>void management.refresh()}>Retry access check</button></CrNote>}
              <CrewMembers key={JSON.stringify(rosterKey)} members={members} crewId={crew.id} workspaceId={workspaceId} canEdit={isAdmin} canManage={isAdmin || management.canManage} onMembersChange={setMembers} />
            </div>
          )}
        </CrCard>

        {isAdmin && <CrewBudgetCard workspaceId={workspaceId} crewId={crew.id} />}

        <CrewCommandLine workspaceId={workspaceId} crew={crew} />
        </>}
      />
    </div>
  )
}
