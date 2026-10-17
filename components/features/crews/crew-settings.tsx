"use client"

import { useEffect, useState } from "react"
import { Copy } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { useAbilities } from "@/hooks/use-abilities"
import { apiFetch } from "@/lib/api-fetch"
import type { CrewMember } from "@/lib/types/crew"

import { CrCard, CrLine, CrNote } from "./cr-card"
import { CrewMembers } from "./crew-members"
import { SettingsTab } from "./crew-canvas-tabs/settings-tab"
import type { AgentSummary, CrewRecord } from "./crew-canvas-tabs/types"

/** Commands that exist in the CLI today for this crew. */
export function crewCommands(slug: string): string[] {
  return [`crewship crew get ${slug}`, `crewship export crew ${slug}`, `crewship agent list --crew ${slug}`]
}

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

      <CrCard title="Command line" concept="settings">
        {crewCommands(crew.slug).map((cmd) => (
          <CrLine
            key={cmd}
            title={<code className="font-mono text-label">{cmd}</code>}
            right={
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={`Copy ${cmd}`}
                onClick={() => { void navigator.clipboard.writeText(cmd).then(() => toast.success("Command copied"), () => toast.error("Could not copy the command")) }}
              >
                <Copy className="size-3.5" />
              </Button>
            }
          />
        ))}
      </CrCard>
    </div>
  )
}
