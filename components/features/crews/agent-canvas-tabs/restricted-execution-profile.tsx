"use client"

import { CreateSurfaceField } from "@/components/layout/create-surface"
import { nativeSelect } from "@/components/features/settings/shared"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"

export type RestrictedProfile = "disabled" | "responses_text" | "native_api_key"

const OPTIONS: { value: RestrictedProfile; label: string }[] = [
  { value: "disabled", label: "Disabled" },
  { value: "responses_text", label: "Isolated text Responses" },
  { value: "native_api_key", label: "Isolated native Codex scratch tools" },
]

/**
 * The agent's restricted client execution profile (#3028). A field of the
 * Edit agent dialog's draft: choosing a value sends nothing, Save applies it
 * after the agent through `saveRestrictedProfile`.
 */
export function RestrictedExecutionField({ value, onChange }: { value: RestrictedProfile; onChange: (next: RestrictedProfile) => void }) {
  return (
    <CreateSurfaceField
      label="Restricted client execution"
      htmlFor="agent-restricted-execution"
      hint="Text Responses and native Codex use an explicit OpenAI API key. Native Codex runs tools in a private scratch sandbox; the server must have its native worker installed. Changing this stops current restricted attempts."
    >
      <select
        id="agent-restricted-execution"
        value={value}
        onChange={(e) => onChange(e.target.value as RestrictedProfile)}
        className={cn(nativeSelect, "w-full sm:w-80")}
      >
        {OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>
    </CreateSurfaceField>
  )
}

/** PUT /api/v1/agents/{id}/restricted-execution; rejects with the server's reason. */
export async function saveRestrictedProfile(agentId: string, workspaceId: string, profile: RestrictedProfile): Promise<RestrictedProfile> {
  const res = await apiFetch(
    `/api/v1/agents/${encodeURIComponent(agentId)}/restricted-execution?workspace_id=${encodeURIComponent(workspaceId)}`,
    { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ profile }) },
  )
  const body = (await res.json().catch(() => ({}))) as { profile?: RestrictedProfile; error?: string }
  if (!res.ok) throw new Error(body.error || `HTTP ${res.status}`)
  return body.profile ?? profile
}
