"use client"

import { useState } from "react"
import { apiFetch } from "@/lib/api-fetch"
import { ConfigSelect } from "../canvas/config-field"

export function RestrictedExecutionProfile({ agentId, workspaceId, initialProfile }: {agentId:string;workspaceId:string;initialProfile:string}) {
  const [profile, setProfile] = useState(initialProfile)
  const endpoint = `/api/v1/agents/${encodeURIComponent(agentId)}/restricted-execution?workspace_id=${encodeURIComponent(workspaceId)}`
  return <ConfigSelect
    label="Restricted client execution"
    hint="Choose how restricted clients may use this agent. Text Responses uses an explicit OpenAI API key and has no tools or provider login. Changing this stops current restricted attempts."
    value={profile}
    options={[{value:"disabled",label:"Disabled"},{value:"responses_text",label:"Isolated text Responses"}]}
    onSave={async value => {
      const response = await apiFetch(endpoint, {method:"PUT",headers:{"Content-Type":"application/json"},body:JSON.stringify({profile:value})})
      if (!response.ok) throw new Error("Could not update restricted execution")
      setProfile((await response.json() as {profile:string}).profile)
    }}
  />
}
