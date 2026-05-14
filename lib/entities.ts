/**
 * Aggregator for entity registries — re-exports the focused modules.
 *
 * The original 987-line entities.ts mixed two unrelated registries (crew
 * icons + agent personas) into one file. They live in dedicated modules
 * now; this file remains as a back-compat facade so existing imports
 * (~20 callers across components/) keep working without churn.
 *
 * Prefer importing from the focused modules directly in new code.
 */

export {
  // Generic registry resolver — re-used by both icon and palette lookups.
  resolveEntity,
  // Crew icons.
  type CrewIconDef,
  CREW_ICONS,
  getCrewIconDef,
  // Gradient palettes.
  type GradientPalette,
  GRADIENT_PALETTES,
  getGradientPalette,
  getCrewDotColor,
  // Icon search / categories.
  CREW_ICON_CATEGORIES,
  searchCrewIcons,
} from "./crew-icons"

export {
  // Persona enums.
  type ToolProfile,
  type AgentRole,
  type LLMProvider,
  type CLIAdapter,
  type PersonaCategory,
  // Persona shape + registry.
  type AgentPersona,
  BUILTIN_PERSONAS,
  // Persona search / categorisation.
  filterPersonas,
  categoryCounts,
} from "./agent-personas"
