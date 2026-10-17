"use client"

import { useCallback } from "react"
import { toast } from "@/lib/toast"

import { useConfirm } from "@/components/ui/use-confirm"
import { apiFetch } from "@/lib/api-fetch"
import { backfillAgentAvatars } from "@/lib/agent-avatar-persist"

interface AvatarStyleCrew {
  id: string
  name: string
  avatar_style?: string | null
}

/**
 * "Apply to all" and "Reset overrides" for a crew's avatar style. Both repaint
 * every agent in the crew, so each asks first; Reset also drops the faces
 * agents were given one by one.
 */
export function useApplyAvatarStyle({
  crew,
  agentCount,
  workspaceId,
  onChanged,
}: {
  crew: AvatarStyleCrew | null | undefined
  agentCount: number
  workspaceId: string
  onChanged: () => void
}) {
  const { ask, dialog } = useConfirm()

  const apply = useCallback(async (resetOverrides: boolean) => {
    if (!crew) return
    const verb = resetOverrides ? "Reset" : "Apply"
    const style = crew.avatar_style ?? "robots"
    const agents = `${agentCount} agent${agentCount === 1 ? "" : "s"}`
    const ok = await ask({
      title: resetOverrides ? `Reset avatars in ${crew.name}?` : `Apply avatar style to ${crew.name}?`,
      description: `Every agent in ${crew.name} gets the “${style}” style.`,
      consequences: [
        { tone: "warn", text: `${agents} get new faces` },
        resetOverrides
          ? { tone: "lost", text: "Avatars set on individual agents are cleared" }
          : { tone: "kept", text: "Avatars set on individual agents stay" },
      ],
      confirmLabel: resetOverrides ? "Reset overrides" : "Apply to all",
    })
    if (!ok) return
    try {
      const url = `/api/v1/crews/${crew.id}/apply-avatar-style?workspace_id=${workspaceId}${resetOverrides ? "&reset_overrides=true" : ""}`
      const res = await apiFetch(url, { method: "POST" })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      // Applying a style clears every stored render in the crew; store the
      // new faces as part of this edit (#2876). Per-agent outcomes are not an
      // error for the apply itself.
      await backfillAgentAvatars(workspaceId, { crewId: crew.id }).catch(() => undefined)
      toast.success(`${verb} done for ${agents}`)
      onChanged()
    } catch (err) {
      toast.error(`${verb} failed: ${err instanceof Error ? err.message : err}`)
    }
  }, [crew, agentCount, ask, onChanged, workspaceId])

  return { applyAvatarStyle: apply, avatarStyleDialog: dialog }
}
