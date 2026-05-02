import type { WizardState, AgentDraft } from "./types"

export interface SubmitResult {
  id: string
  slug: string
  name: string
}

export async function submitCrew(workspaceId: string, state: WizardState): Promise<SubmitResult> {
  switch (state.mode) {
    case "browse":
      return submitFromTemplate(workspaceId, state)
    case "ai":
      return submitFromAI(workspaceId, state)
    case "empty":
    default:
      return submitBlank(workspaceId, state)
  }
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
  // Don't fail the whole submission if PATCH fails — crew exists with template defaults.
  if (!patchRes.ok) {
    console.warn("Crew created but identity/runtime override failed:", await patchRes.text())
  }

  return { id: deployed.crew_id, slug: deployed.crew_slug, name: deployed.crew_name }
}

async function submitFromAI(workspaceId: string, state: WizardState): Promise<SubmitResult> {
  if (!state.aiResult) throw new Error("No AI suggestion to submit")

  // 1. Create the crew with user's identity + runtime.
  const crewRes = await fetch(`/api/v1/crews?workspace_id=${encodeURIComponent(workspaceId)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ...identityBody(state), ...runtimeBody(state) }),
  })
  if (!crewRes.ok) throw new Error(await crewRes.text() || `HTTP ${crewRes.status}`)
  const created = await crewRes.json() as { id: string; slug: string; name: string }

  // 2. Create each suggested agent. Sequential — agent slugs must be unique within
  // workspace and we want deterministic ordering for the lead-first roster.
  const errors: string[] = []
  for (const agent of state.aiResult.agents) {
    try {
      await createAgent(workspaceId, created.id, agent)
    } catch (e) {
      errors.push(`${agent.name}: ${e instanceof Error ? e.message : String(e)}`)
    }
  }
  if (errors.length > 0) {
    // Surface partial failure but keep the crew — user can retry agent creation manually.
    console.warn("Crew created but some agents failed:", errors)
  }

  return { id: created.id, slug: created.slug, name: created.name }
}

async function createAgent(workspaceId: string, crewId: string, agent: AgentDraft): Promise<void> {
  const body: Record<string, unknown> = {
    name: agent.name,
    slug: agent.slug,
    crew_id: crewId,
    agent_role: agent.agent_role,
    cli_adapter: agent.cli_adapter || "CLAUDE_CODE",
    tool_profile: agent.tool_profile || "general",
    timeout_seconds: 600,
    memory_enabled: true,
  }
  if (agent.role_title) body.role_title = agent.role_title
  if (agent.system_prompt) body.system_prompt = agent.system_prompt
  if (agent.llm_provider) body.llm_provider = agent.llm_provider
  if (agent.llm_model) body.llm_model = agent.llm_model

  const res = await fetch(`/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await res.text() || `HTTP ${res.status}`)
}
