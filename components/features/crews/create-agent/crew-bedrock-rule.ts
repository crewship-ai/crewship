"use client"

import { useEffect, useState } from "react"

import { apiFetch } from "@/lib/api-fetch"
import { BEDROCK_SLOT } from "@/lib/credentials/bedrock"

export interface CrewBedrockRule {
  /** The Bedrock account new members of the crew pay with. */
  credentialId: string
  /** Its default native model id. */
  model: string
}

/**
 * The crew's Bedrock account rule, if it has one: the CREW binding on the
 * Bedrock slot that carries a default model. New agents in the crew that are
 * on Bedrock (or have no provider) inherit it on the server.
 */
export function useCrewBedrockRule(workspaceId: string, crewId: string | null): CrewBedrockRule | null {
  const [rule, setRule] = useState<CrewBedrockRule | null>(null)
  useEffect(() => {
    setRule(null)
    if (!crewId) return
    const controller = new AbortController()
    void apiFetch(`/api/v1/credentials/bindings?workspace_id=${encodeURIComponent(workspaceId)}&scope=CREW&crew_id=${encodeURIComponent(crewId)}`, { signal: controller.signal })
      .then((res) => (res.ok ? res.json() : null))
      .then((body: { bindings?: Array<{ credential_id: string; crew_id: string | null; scope: string; slot: string; bedrock_model?: string | null }> } | null) => {
        const row = (body?.bindings ?? []).find((b) => b.scope === "CREW" && b.crew_id === crewId && b.slot === BEDROCK_SLOT && b.bedrock_model)
        if (!controller.signal.aborted) setRule(row ? { credentialId: row.credential_id, model: row.bedrock_model! } : null)
      })
      .catch(() => undefined)
    return () => controller.abort()
  }, [workspaceId, crewId])
  return rule
}
