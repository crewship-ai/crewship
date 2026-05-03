import type { CrewTemplateAgent } from "./api"

export type WizardStep = 1 | 2 | 3 | 4 | 5

export type LineupMode = "browse" | "empty"

/**
 * Allowed crew color palette IDs. Mirrors lib/crew-icon.ts → GRADIENT_PALETTES.
 * Backend stores this as a free-form `color TEXT` column but per CLAUDE.md
 * convention only these 8 values are valid; tightening the type prevents
 * arbitrary strings (legacy hex codes, typos) from sneaking into wizard state
 * at compile time.
 */
export type CrewColor =
  | "blue" | "emerald" | "violet" | "amber"
  | "rose" | "cyan" | "lime" | "fuchsia"

const CREW_COLORS: readonly CrewColor[] = [
  "blue", "emerald", "violet", "amber",
  "rose", "cyan", "lime", "fuchsia",
] as const

/** Narrow an arbitrary string (e.g. from a picker callback or template DB row)
 *  into the strict CrewColor union. Falls back to "blue" for legacy hex codes
 *  or unknown values so wizard state stays well-typed end-to-end. */
export function asCrewColor(v: string | null | undefined): CrewColor {
  if (v && (CREW_COLORS as readonly string[]).includes(v)) return v as CrewColor
  return "blue"
}

/**
 * Crew icon name (lucide-react). The full catalog lives in lib/crew-icon.ts
 * (CREW_ICONS) and is too large to enumerate as a literal union; we keep this
 * as a `string` newtype plus a runtime check on entry (CrewIconPickerDialog
 * + step-identity wizard won't write anything not in CREW_ICONS) instead of
 * forcing every test fixture to pull in the 250-entry tuple type.
 */
export type CrewIconName = string

export interface WizardState {
  // Step 1 — Identity
  name: string
  slug: string
  slugTouched: boolean
  description: string
  icon: CrewIconName
  color: CrewColor

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

// Resource bounds — enforced by the wizard's CustomNumberChip and the Review
// step's validation. Backend (crews_create.go) currently only checks > 0, but
// Docker / Apple-containers will fail noisily if asked for 64 GB on a 16 GB
// host or 0 CPUs. These ranges keep the user in the realm of "things that
// might actually run".
export const MEMORY_MIN_MB = 128
export const MEMORY_MAX_MB = 32768
export const CPU_MIN = 0.1
export const CPU_MAX = 16

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
