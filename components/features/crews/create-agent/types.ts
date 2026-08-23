import type {
  AgentPersona,
  AgentRole,
  CLIAdapter,
  LLMProvider,
  ToolProfile,
} from "@/lib/entities"

/** Source of a persona — drives the source-tab filter in the template browser
 *  popover. Only "builtin" is wired today; the others land with the
 *  agent-templates API. Kept in the type so the persistence shape stays
 *  stable as new sources come online. */
export type PersonaSource = "builtin" | "mine" | "workspace" | "marketplace"

/** Lead behaviour mode. Only meaningful when agentRole === "LEAD". */
export type LeadMode = "active" | "passive"

/** Mutable form state for the single-screen Create Agent dialog. Lives in
 *  React state and survives until the dialog closes. The submit body is a
 *  thin transform of this — see create-agent-dialog.tsx submit(). */
export interface AgentDraft {
  // Identity
  name: string
  slug: string
  /** Set true once the user has manually edited slug — disables auto-derive. */
  slugTouched: boolean
  agentRole: AgentRole
  crewSlug: string
  roleTitle: string
  description: string
  avatarSeed: string
  avatarStyle: string
  /** True once the user has explicitly picked a style/seed via the picker.
   *  When true, picking a persona will NOT overwrite the avatar — same
   *  rule we apply to `roleTitle`. Personal customisations survive
   *  template picks. */
  avatarTouched: boolean

  // Persona — system_prompt source-of-truth resolution:
  // editedPersonaPrompt > customPrompt > selectedPersona.systemPrompt > "".
  /** Picked template; null after "Blank". */
  selectedPersona: AgentPersona | null
  /** Free-text prompt typed by the user with no template active. */
  customPrompt: string
  /** Edited copy of the picked persona's prompt — keeps the source persona
   *  unchanged. */
  editedPersonaPrompt: string | null

  // Runtime
  llmProvider: LLMProvider
  llmModel: string
  cliAdapter: CLIAdapter
  toolProfile: ToolProfile
  memoryEnabled: boolean
  timeoutSeconds: number
  /** Only relevant when agentRole === "LEAD" — backend ignores it otherwise. */
  leadMode: LeadMode
}

export interface CrewLite {
  id: string
  slug: string
  name: string
  /** The crew's own face, so the picker can show it. Optional because
   *  callers that only have {id, slug, name} still work — the picker falls
   *  back to a generic glyph. */
  icon?: string | null
  color?: string | null
  /** Drives the picker's "with agents" / "empty" grouping. */
  agentCount?: number
}

/** Creates the initial draft with sensible defaults. */
export function initialAgentDraft(defaultCrewSlug: string | null): AgentDraft {
  return {
    name: "",
    slug: "",
    slugTouched: false,
    agentRole: "AGENT",
    crewSlug: defaultCrewSlug ?? "",
    roleTitle: "",
    description: "",
    avatarSeed: "",
    avatarStyle: "bottts-neutral",
    avatarTouched: false,

    selectedPersona: null,
    customPrompt: "",
    editedPersonaPrompt: null,

    llmProvider: "ANTHROPIC",
    llmModel: "claude-sonnet-4-6",
    cliAdapter: "CLAUDE_CODE",
    toolProfile: "CODING",
    memoryEnabled: true,
    timeoutSeconds: 1800,
    leadMode: "active",
  }
}

/** Apply a persona's defaults to the draft.
 *  Does NOT touch identity fields (name/slug/crew) — only the persona body
 *  and runtime fields, plus the avatarStyle / roleTitle when the user hasn't
 *  customised them. */
export function applyPersonaDefaults(draft: AgentDraft, persona: AgentPersona): AgentDraft {
  return {
    ...draft,
    selectedPersona: persona,
    customPrompt: "",
    editedPersonaPrompt: null,
    avatarStyle: draft.avatarTouched ? draft.avatarStyle : persona.avatarStyle,
    llmProvider: persona.llmProvider,
    llmModel: persona.llmModel,
    cliAdapter: persona.cliAdapter,
    toolProfile: persona.toolProfile,
    memoryEnabled: persona.memoryEnabled,
    timeoutSeconds: persona.timeoutSeconds,
    roleTitle: draft.roleTitle.trim() === "" ? persona.roleTitle : draft.roleTitle,
  }
}

/** Final prompt to submit. Resolution order:
 *    1. customPrompt (when explicitly typed without a template active)
 *    2. editedPersonaPrompt (template selected, user edited)
 *    3. selectedPersona.systemPrompt (template as-is)
 *    4. empty string (no template, no custom — backend uses generic default) */
export function resolveFinalPrompt(draft: AgentDraft): string {
  if (draft.customPrompt.trim()) return draft.customPrompt.trim()
  if (draft.editedPersonaPrompt !== null) return draft.editedPersonaPrompt
  if (draft.selectedPersona) return draft.selectedPersona.systemPrompt
  return ""
}

/** Fields compared by `isDraftDirty`. Everything on AgentDraft except
 *  `selectedPersona`, which is an object and gets compared by id. Listed
 *  rather than derived from Object.keys so a new field is a compile error
 *  here (`keyof AgentDraft`) instead of a silently unguarded one. */
const DIRTY_KEYS = [
  "name",
  "slug",
  "slugTouched",
  "agentRole",
  "crewSlug",
  "roleTitle",
  "description",
  "avatarSeed",
  "avatarStyle",
  "avatarTouched",
  "customPrompt",
  "editedPersonaPrompt",
  "llmProvider",
  "llmModel",
  "cliAdapter",
  "toolProfile",
  "memoryEnabled",
  "timeoutSeconds",
  "leadMode",
] as const satisfies readonly Exclude<keyof AgentDraft, "selectedPersona">[]

/** True when the surface holds input that closing would throw away.
 *
 *  Measured against the draft the dialog OPENED with, not against a blank
 *  one: a pre-selected crew (from `?crew=` on /crews) and the default model
 *  are not the user's work, and prompting about them would train people to
 *  click through the discard dialog without reading it.
 *
 *  Feeds `dirty` on CreateSurface, which owns the Esc / overlay-click guard
 *  — the two dismissals a per-dialog guard can never see. */
export function isDraftDirty(draft: AgentDraft, defaultCrewSlug: string | null): boolean {
  const base = initialAgentDraft(defaultCrewSlug)
  if ((draft.selectedPersona?.id ?? null) !== (base.selectedPersona?.id ?? null)) return true
  return DIRTY_KEYS.some((k) => draft[k] !== base[k])
}

/** Submit-time validation — true means the Create button is enabled.
 *
 *  Bounds match `internal/api/agents_create.go` exactly:
 *    name 2-100, slug 2-50 + lowercase/digits/hyphens. Anything outside
 *    these is a guaranteed 400 from the backend, so we block client-side
 *    rather than letting the user submit and bounce. */
export function isIdentityValid(draft: AgentDraft): boolean {
  const name = draft.name.trim()
  if (name.length < 2 || name.length > 100) return false
  if (draft.slug.length < 2 || draft.slug.length > 50) return false
  if (!/^[a-z0-9-]{2,}$/.test(draft.slug)) return false
  if (!draft.crewSlug) return false
  return true
}
