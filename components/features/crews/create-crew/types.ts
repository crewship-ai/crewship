import type { CrewTemplateAgent } from "./api"

export type WizardStep = 1 | 2 | 3 | 4 | 5

export type LineupMode = "browse" | "empty"

export interface WizardState {
  // Step 1 — Identity
  name: string
  slug: string
  slugTouched: boolean
  description: string
  icon: string
  color: string

  // Step 2 — Lineup
  mode: LineupMode
  pickedTemplateSlug: string | null
  pickedTemplateMeta: { name: string; agentCount: number; agents: { name: string; agent_role: string }[] } | null

  // Step 3 — Runtime
  memoryMB: number
  cpus: number
  ttlHours: number | null
  networkMode: "free" | "restricted"
  allowedDomains: string[]

  // Step 4 — Container (image+features+MCP). Strings to match the existing
  // RuntimeConfig and MCPConfigEditor `value` props; empty string = "use server default".
  runtimeImage: string
  devcontainerConfig: string
  miseConfig: string
  mcpConfig: string
}

export const INITIAL_STATE: WizardState = {
  name: "",
  slug: "",
  slugTouched: false,
  description: "",
  icon: "code",
  color: "blue",
  mode: "browse",
  pickedTemplateSlug: null,
  pickedTemplateMeta: null,
  memoryMB: 4096,
  cpus: 2,
  ttlHours: null,
  networkMode: "free",
  allowedDomains: [],
  runtimeImage: "",
  devcontainerConfig: "",
  miseConfig: "",
  mcpConfig: "",
}

export const MEMORY_PRESETS = [
  { label: "512 MB", value: 512 },
  { label: "1 GB", value: 1024 },
  { label: "2 GB", value: 2048 },
  { label: "4 GB", value: 4096 },
  { label: "8 GB", value: 8192 },
]

export const CPU_PRESETS = [
  { label: "0.5", value: 0.5 },
  { label: "1", value: 1 },
  { label: "2", value: 2 },
  { label: "4", value: 4 },
]

export const TTL_PRESETS = [
  { label: "Never", value: null },
  { label: "1 h", value: 1 },
  { label: "4 h", value: 4 },
  { label: "24 h", value: 24 },
]

// Re-export so step components can import a single types module.
export type { CrewTemplateAgent }
