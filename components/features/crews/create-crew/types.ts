import type { CrewTemplateAgent } from "./api"

export type WizardStep = 1 | 2 | 3 | 4

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

/** Longest slug POST /api/v1/crews accepts (crews_create.go). */
export const SLUG_MAX = 50

/**
 * Coerce anything typed into a slug the server will accept.
 *
 * The rule is `^[a-z0-9][a-z0-9_-]*$` at 2–50 characters
 * (internal/api/helpers.go → validSlugRe, crews_create.go → the length
 * check). The field used to accept any string and only describe the rule in
 * a hint, so `Engineering Team!` typed fine, survived three more steps and
 * came back as a 400 on Create — the worst place to learn a format.
 *
 * Two deliberate omissions, both because this runs on every keystroke:
 *
 *  · **A trailing hyphen is left alone.** This runs on the field the user is
 *    typing INTO, so its own output is the next keystroke's input. Trimming
 *    would eat the space in "my crew": "my " normalises to "my-", trimming
 *    gives back "my", and the next character lands as "myc". The server
 *    accepts a trailing hyphen anyway — only the FIRST character is fenced.
 *    `slugFromName` does trim, because deriving re-reads the whole name.
 *  · **Length is capped, not floored.** Two characters is the minimum, but
 *    enforcing it mid-word would block you from ever typing the first one.
 *    The step's own validation already gates Continue on it.
 */
export function normalizeSlug(raw: string): string {
  return raw
    .toLowerCase()
    .replace(/[^a-z0-9_-]+/g, "-")
    // Collapse runs of separators. This second pass is what the old
    // `[^a-z0-9]+ → "-"` got for free by treating hyphens as invalid too;
    // keeping "_" valid means "Foo   ---   Bar" arrives here as
    // "foo-----bar" and has to be folded explicitly. The run's first
    // character wins, so "a__b" stays an underscore.
    .replace(/[-_]{2,}/g, (run) => run[0])
    // Leading -/_ is the one thing validSlugRe rejects outright.
    .replace(/^[-_]+/, "")
    .slice(0, SLUG_MAX)
}

/**
 * The slug a crew name implies, for the auto-fill while Slug is untouched.
 *
 * Same rule as `normalizeSlug` plus a trailing trim, which is safe here and
 * only here: the field being edited is Name, so each keystroke re-derives
 * from the full name and no separator the user typed is ever lost.
 */
export function slugFromName(name: string): string {
  return normalizeSlug(name).replace(/[-_]+$/, "")
}

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
  // Open, and said out loud.
  //
  // This was "restricted" so the wizard would inherit the backend's fail-safe
  // (database/crew_defaults.go), which is the right default for a code path
  // that forgets to choose. The wizard is not that path: it asks, and the
  // answer it proposes is the product decision — open now, throttled later,
  // with the allowlist built and one switch away. An allowlist that is still
  // maturing fails as a silent timeout deep inside a run, and defaulting to
  // it hands every new crew that failure shape.
  //
  // The server constant is untouched on purpose: anything that creates a crew
  // WITHOUT saying what it wants still gets restricted.
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
