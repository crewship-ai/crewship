import { toast } from "sonner"
import type { WizardState } from "./types"

export interface SubmitResult {
  id: string
  slug: string
  name: string
  /** True when the crew was created but a follow-up override PATCH failed (icon /
   *  color / runtime / mcp). The crew exists with whatever defaults the create
   *  call applied. Caller should surface this to the user. */
  partial?: boolean
}

// workspace_id MUST be passed as a query parameter — the wsCtx middleware
// (RequireWorkspace) reads it from r.URL.Query() / r.PathValue and rejects
// 400 "workspace_id is required" otherwise.
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

// Container fields accepted by POST /api/v1/crews — runtime_image, devcontainer_config,
// mise_config (mcp_config_json is PATCH-only on the backend, see hasMCPOverride).
function containerCreateBody(state: WizardState): Record<string, unknown> {
  const body: Record<string, unknown> = {}
  if (state.runtimeImage.trim()) body.runtime_image = state.runtimeImage.trim()
  if (state.devcontainerConfig.trim()) body.devcontainer_config = state.devcontainerConfig
  if (state.miseConfig.trim()) body.mise_config = state.miseConfig
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

function hasMCPOverride(state: WizardState): boolean {
  return state.mcpConfig.trim() !== ""
}

async function submitBlank(workspaceId: string, state: WizardState): Promise<SubmitResult> {
  const res = await fetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      ...identityBody(state),
      ...runtimeBody(state),
      ...containerCreateBody(state),
    }),
  })
  if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`)
  const created = await res.json() as { id: string; slug: string; name: string }

  // POST doesn't accept mcp_config_json — patch it after create when set.
  let partial = false
  if (hasMCPOverride(state)) {
    partial = !(await applyOverrides(workspaceId, created.id, { mcp_config_json: state.mcpConfig }))
  }

  return { id: created.id, slug: created.slug, name: created.name, partial }
}

// Two-step: template deploy creates crew + agents; we then PATCH for any identity / runtime /
// container / MCP fields the user customized (deploy ignores those overrides today).
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

  const patchBody: Record<string, unknown> = {
    icon: state.icon,
    color: state.color,
    ...runtimeBody(state),
  }
  if (state.description.trim()) patchBody.description = state.description.trim()
  if (state.runtimeImage.trim()) patchBody.runtime_image = state.runtimeImage.trim()
  if (state.devcontainerConfig.trim()) patchBody.devcontainer_config = state.devcontainerConfig
  if (state.miseConfig.trim()) patchBody.mise_config = state.miseConfig
  if (hasMCPOverride(state)) patchBody.mcp_config_json = state.mcpConfig

  const ok = await applyOverrides(workspaceId, deployed.crew_id, patchBody)

  return {
    id: deployed.crew_id,
    slug: deployed.crew_slug,
    name: deployed.crew_name,
    partial: !ok,
  }
}

// applyOverrides PATCHes a freshly-created crew. Returns true on success, false
// on HTTP failure or empty body — failure is non-fatal (crew exists with create
// defaults) but caller should surface a warning to the user. Toast is also
// fired here so that callers that don't read .partial still flag the regression.
async function applyOverrides(
  workspaceId: string,
  crewId: string,
  body: Record<string, unknown>,
): Promise<boolean> {
  if (Object.keys(body).length === 0) return true
  const res = await fetch(`/api/v1/crews/${encodeURIComponent(crewId)}?workspace_id=${encodeURIComponent(workspaceId)}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
  if (!res.ok) {
    const detail = await res.text()
    console.warn("Crew created but override PATCH failed:", detail)
    toast.warning("Crew created, but some customizations didn't apply", {
      description: "Open crew settings to retry icon, color, runtime or MCP overrides.",
    })
    return false
  }
  return true
}
