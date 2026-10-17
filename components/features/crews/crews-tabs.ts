/**
 * The tabs of a crew page and an agent page, and what ?tab= in a link means.
 *
 * Links built elsewhere (Chat, Integrations, the Inbox) still say
 * ?tab=work, ?tab=skills, ?tab=credentials or ?tab=settings; each lands on
 * the tab that holds that content now.
 */
export type CrewTab = "overview" | "team" | "tools" | "env" | "memory" | "settings"
export type AgentTab = "overview" | "persona" | "model" | "tools" | "memory" | "settings"

export const CREW_TABS: { id: CrewTab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "team", label: "Team" },
  { id: "tools", label: "Tools and access" },
  { id: "env", label: "Environment" },
  { id: "memory", label: "Memory" },
  { id: "settings", label: "Settings" },
]

export const AGENT_TABS: { id: AgentTab; label: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "persona", label: "Instructions" },
  { id: "model", label: "Model and limits" },
  { id: "tools", label: "Tools and access" },
  { id: "memory", label: "Memory" },
  { id: "settings", label: "Settings" },
]

const CREW_LEGACY: Record<string, CrewTab> = {
  work: "overview",
  missions: "overview",
  roster: "team",
  skills: "tools",
  credentials: "tools",
  integrations: "tools",
  environment: "env",
  container: "env",
  docker: "env",
}

const AGENT_LEGACY: Record<string, AgentTab> = {
  work: "overview",
  instructions: "persona",
  config: "model",
  skills: "tools",
  credentials: "tools",
  access: "tools",
}

export function crewTabFromParam(raw: string | null | undefined): CrewTab {
  if (!raw) return "overview"
  if (CREW_TABS.some((t) => t.id === raw)) return raw as CrewTab
  return CREW_LEGACY[raw] ?? "overview"
}

export function agentTabFromParam(raw: string | null | undefined): AgentTab {
  if (!raw) return "overview"
  if (AGENT_TABS.some((t) => t.id === raw)) return raw as AgentTab
  return AGENT_LEGACY[raw] ?? "overview"
}

/** Overview is the default and is left out of the URL. */
export function tabParam(tab: string): string | null {
  return tab === "overview" ? null : tab
}
