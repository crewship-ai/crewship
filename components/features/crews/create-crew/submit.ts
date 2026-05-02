import type { WizardState } from "./types"

export interface SubmitResult {
  id: string
  slug: string
  name: string
}

export async function submitCrew(workspaceId: string, state: WizardState): Promise<SubmitResult> {
  if (state.mode === "browse") return submitFromTemplate(workspaceId, state)
  return submitBlank(workspaceId, state)
}

function runtimeBody(state: WizardState): Record<string, unknown> {
  const body: Record<string, unknown> = {
    container_memory_mb: state.memoryMB,
    container_cpus: state.cpus,
    network_mode: state.networkMode,
  }
  if (state.ttlHours !== null && state.ttlHours > 0) {
    body.container_ttl_hours = state.ttlHours
  }
  if (state.networkMode === "restricted" && state.allowedDomains.length > 0) {
    body.allowed_domains = state.allowedDomains
  }
  return body
}

function identityBody(state: WizardState): Record<string, unknown> {
  const body: Record<string, unknown> = {
    name: state.name.trim(),
    slug: state.slug.trim(),
    icon: state.icon,
    color: state.color,
  }
  if (state.description.trim()) body.description = state.description.trim()
  return body
}

async function submitBlank(workspaceId: string, state: WizardState): Promise<SubmitResult> {
  const res = await fetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...identityBody(state), ...runtimeBody(state) }),
  })
  if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`)
  const created = await res.json()
  return { id: created.id, slug: created.slug, name: created.name }
}

// Two-step: template deploy creates crew + agents; we then PATCH for any identity/runtime
// fields the user customized (deploy ignores those overrides today).
async function submitFromTemplate(workspaceId: string, state: WizardState): Promise<SubmitResult> {
  if (!state.pickedTemplateSlug) {
    throw new Error("No template selected")
  }
  const deployRes = await fetch(
    `/api/v1/crew-templates/${encodeURIComponent(state.pickedTemplateSlug)}/deploy?workspace_id=${encodeURIComponent(workspaceId)}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ crew_name: state.name.trim(), crew_slug: state.slug.trim() }),
    },
  )
  if (!deployRes.ok) throw new Error(await deployRes.text() || `HTTP ${deployRes.status}`)
  const deployed = await deployRes.json() as { crew_id: string; crew_name: string; crew_slug: string }

  // Override icon/color/description/runtime on the freshly-deployed crew so user choices
  // win over template defaults. Single PATCH; failure here doesn't roll back the deploy.
  const patchBody: Record<string, unknown> = {
    icon: state.icon,
    color: state.color,
    ...runtimeBody(state),
  }
  if (state.description.trim()) patchBody.description = state.description.trim()

  const patchRes = await fetch(`/api/v1/crews/${encodeURIComponent(deployed.crew_id)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patchBody),
  })
  if (!patchRes.ok) {
    console.warn("Crew created but identity/runtime override failed:", await patchRes.text())
  }

  return { id: deployed.crew_id, slug: deployed.crew_slug, name: deployed.crew_name }
}
