import { BriefcaseBusiness, Languages, MessageSquare, Palette, Shapes } from "lucide-react"

// Presentation groups are independent of individual fact titles. Built-in
// extractor keys (internal/usermodel/profile.go) and legacy imports share them.
// New unrecognised keys remain visible in Other; never infer from personal text.
export const preferenceCategories = [
  { id: "communication", label: "Communication", icon: MessageSquare },
  { id: "language", label: "Language", icon: Languages },
  { id: "work", label: "Work", icon: BriefcaseBusiness },
  { id: "appearance", label: "Appearance", icon: Palette },
  { id: "other", label: "Other", icon: Shapes },
] as const

type Category = typeof preferenceCategories[number]["id"]
const fieldCategories: Record<string, Category> = {
  role: "work", owns: "work", constraint: "work", process: "work", tooling: "work", timezone: "work",
  prefers: "work", contact: "communication", language: "language",
  jazyk: "language", styl_odpovedi: "communication", response_style: "communication",
  vzhled_aplikace: "appearance", design_preferences: "appearance", overeni_prace: "work",
}

export function preferenceCategory(key: string) {
  const normalized = key.trim().toLowerCase()
  // Namespaced imports can declare a stable category with any new title.
  // Unknown namespaces fall back intact rather than losing or hiding a note.
  const prefix = normalized.split(/[.:/]/)[0]
  const category = preferenceCategories.find(category => category.id === prefix)
  return category ?? preferenceCategories.find(category => category.id === fieldCategories[normalized]) ?? preferenceCategories[4]
}

export function preferenceLabel(key: string) {
  const labels: Record<string, string> = { jazyk: "Jazyk", styl_odpovedi: "Styl odpovědí", vzhled_aplikace: "Vzhled aplikace", overeni_prace: "Ověření práce", ukazka: "Ukázková data" }
  if (Object.hasOwn(labels, key)) return labels[key]
  const category = preferenceCategory(key)
  const prefix = key.trim().split(/[.:/]/)[0]
  const title = prefix.toLowerCase() === category.id ? key.trim().slice(prefix.length + 1) || key : key
  const text = title.replace(/[_-]+/g, " ").trim()
  return text.charAt(0).toLocaleUpperCase() + text.slice(1)
}
