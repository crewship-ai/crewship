import { ACCENTS, STYLE_ACCENTS, WORKSPACE_ACCENT, type AccentChoice } from "@/lib/theme/accents"
import type { ThemeChoice } from "@/lib/theme/appearance"

// One word per value of the look, shared by the "Your look" line and the
// workspace default's description.

/** "Blue", "Team colour", and for a style-only accent its colour name ("Clay"). */
export function accentName(id: AccentChoice): string {
  if (id === WORKSPACE_ACCENT) return "Team colour"
  const preset = ACCENTS.find((a) => a.id === id)
  if (preset) return preset.label
  const own = STYLE_ACCENTS.find((a) => a.id === id)
  return own ? own.id.charAt(0).toUpperCase() + own.id.slice(1) : String(id)
}

export function themeName(theme: ThemeChoice): string {
  return theme === "system" ? "System" : theme === "dark" ? "Dark" : "Light"
}
