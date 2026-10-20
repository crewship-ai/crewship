"use client"

import { useEffect, useMemo, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { GiveToPicker, type HeldSkillChange, type LibrarySkill } from "@/components/features/skills/give-to-picker"

import type { AgentSkillRow } from "./agent-canvas-tabs/types"

// "Give skills to {agent}" (A-TOOLS Add skill, K09, CRW-03). This is the Skills
// area's shared GiveToPicker in its agent-to-skills direction (kit request
// #3481); this file only loads the workspace's skills and owns the agent API
// writes (a removal is addressed by assignment id).

export function AgentSkillsPicker({ open, agentId, agentName, workspaceId, assigned, onClose, onApplied }: {
  open: boolean
  agentId: string
  agentName: string
  workspaceId: string
  /** What the agent holds now (GET /agents/{id}/skills). */
  assigned: AgentSkillRow[]
  onClose: () => void
  /** After any write, so the caller reloads the agent's skills. */
  onApplied: () => void
}) {
  const held = useMemo(() => new Map(assigned.map((a) => [a.skill_id, a.id])), [assigned])
  const [library, setLibrary] = useState<LibrarySkill[] | null>(null)
  const [loadFailed, setLoadFailed] = useState(false)

  useEffect(() => {
    if (!open || library !== null) return
    let cancelled = false
    apiFetch(`/api/v1/skills?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((v: LibrarySkill[]) => { if (!cancelled) setLibrary(Array.isArray(v) ? v : []) })
      .catch(() => { if (!cancelled) setLoadFailed(true) })
    return () => { cancelled = true }
  }, [open, library, workspaceId])

  if (!open) return null

  const write = async (c: HeldSkillChange) => {
    const r = c.kind === "add"
      ? await apiFetch(`/api/v1/agents/${agentId}/skills`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ skill_id: c.skillId }) })
      : await apiFetch(`/api/v1/agents/${agentId}/skills/${c.assignmentId}`, { method: "DELETE" })
    if (!r.ok) throw await toApiError(r, `HTTP ${r.status}`)
  }

  return (
    <GiveToPicker
      direction="agent-to-skills"
      agent={{ id: agentId, name: agentName }}
      library={library}
      loadFailed={loadFailed}
      held={held}
      write={write}
      onApplied={onApplied}
      onClose={onClose}
    />
  )
}
