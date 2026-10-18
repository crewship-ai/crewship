/**
 * Appearance style — the Harbor 2 visual style and its knobs (#3334).
 *
 * Why a second preference key: `appearance` (lib/theme/appearance.ts) is
 * written whole by every client, including ones built before styles existed.
 * Keeping the style in `appearance.style` means such a client cannot drop it
 * when it saves an accent or a theme. In the other direction the theme and the
 * accent live ONLY in `appearance`, so they always win when rendering: a style
 * merely suggests an accent (and, for a single-scheme style, a theme) at the
 * moment it is picked.
 *
 * Rendering order: Harbor tokens → the style's block (`data-style` on <html>)
 * → knobs → accent and theme. A style is a registry row here plus a block of
 * token values in app/styles/styles.css; nothing else knows which style is
 * active, so this file and the stylesheets are the only places `data-style`
 * is read or written (a test pins that).
 *
 * A value this build does not understand — a style or knob from a newer
 * release — is not rendered, but it is kept, and written back unchanged.
 */

import type { AccentChoice } from "@/lib/theme/accents"
import type { ThemeChoice } from "@/lib/theme/appearance"

/** The per-user preference key (GET/PUT /api/v1/me/preferences/{key}). */
export const STYLE_PREFERENCE_KEY = "appearance.style"
/** This browser's copy, read by the boot script before first paint. */
export const STYLE_STORAGE_KEY = "crewship-appearance-style"

export type Scheme = "light" | "dark"

export interface StyleDef {
  id: string
  /** The schemes the style has a palette for. */
  schemes: Scheme[]
  /** The accent suggested when the style is picked. */
  accent: AccentChoice
}

export interface StyleState {
  v: 1
  style: string
  knobs: Record<string, string>
}

/** Gallery sections, in order. */
export const STYLE_CATEGORIES = ["Crewship", "AI", "Development", "Productivity", "Design", "Communication", "Enterprise", "System"] as const
export type StyleCategory = (typeof STYLE_CATEGORIES)[number]

/** A registry row: the rendering facts plus what the gallery shows. */
export interface StyleInfo extends StyleDef {
  name: string
  category: StyleCategory
  blurb: string
  /** Rail, card, accent and dark ground, for a glance before the live preview loads. */
  swatch: [string, string, string, string]
}

export const HARBOR: StyleInfo = {
  id: "harbor",
  name: "Harbor",
  category: "Crewship",
  blurb: "The default: neutral grey, white cards, a blue accent.",
  schemes: ["light", "dark"],
  accent: "blue",
  swatch: ["#F3F4F6", "#FFFFFF", "#1E7BFE", "#0F1013"],
}

/** Every style this build can render. */
export const STYLES: StyleInfo[] = [
  HARBOR,
  { id: "harbor-night", name: "Harbor Night", category: "Crewship", blurb: "Deep navy ground with a sky-blue accent. Dark only.", schemes: ["dark"], accent: "sky", swatch: ["#070D18","#111A2E","#38BDF8","#0B1220"] },
  { id: "ink", name: "Ink", category: "AI", blurb: "Black and white, maximum contrast, no extra colour.", schemes: ["light","dark"], accent: "graphite", swatch: ["#F7F7F8","#FFFFFF","#64748B","#121212"] },
  { id: "blush", name: "Blush", category: "AI", blurb: "Warm and playful: soft pink, rounded corners.", schemes: ["light","dark"], accent: "pink", swatch: ["#FCF6F3","#FFFFFF","#EC4899","#1A1418"] },
  { id: "sunrise", name: "Sunrise", category: "AI", blurb: "Warm cream surfaces with square corners.", schemes: ["light","dark"], accent: "pink", swatch: ["#FFFAF0","#FFFFFF","#EC4899","#181109"] },
  { id: "pewter", name: "Pewter", category: "AI", blurb: "Monochrome greys with sharp lines.", schemes: ["light","dark"], accent: "blue", swatch: ["#FAFAFA","#FFFFFF","#1E7BFE","#0A0A0A"] },
  { id: "terracotta", name: "Terracotta", category: "AI", blurb: "Warm paper and clay tones, calm and human, softly rounded.", schemes: ["light","dark"], accent: "pink", swatch: ["#F0EEE6","#FFFFFF","#EC4899","#171716"] },
  { id: "lagoon", name: "Lagoon", category: "AI", blurb: "Warm cream with a calm teal; made for reading.", schemes: ["light","dark"], accent: "teal", swatch: ["#F3F3EE","#FFFFFF","#0EA5C4","#161717"] },
  { id: "slate", name: "Slate", category: "Development", blurb: "Cool grey with an indigo accent; dark feels at home.", schemes: ["light","dark"], accent: "indigo", swatch: ["#F4F5F8","#FFFFFF","#6366F1","#0F1011"] },
  { id: "mono", name: "Mono", category: "Development", blurb: "Clean black and white with crisp lines.", schemes: ["light","dark"], accent: "graphite", swatch: ["#FAFAFA","#FFFFFF","#64748B","#0A0A0A"] },
  { id: "cool-grey", name: "Cool Grey", category: "Development", blurb: "Restrained greys, blue links, dense content.", schemes: ["light","dark"], accent: "blue", swatch: ["#F6F8FA","#FFFFFF","#1E7BFE","#0D1117"] },
  { id: "plum", name: "Plum", category: "Development", blurb: "Deep violet surfaces with a bright accent.", schemes: ["light","dark"], accent: "violet", swatch: ["#F7F5FB","#FFFFFF","#8B5CF6","#13111C"] },
  { id: "ember", name: "Ember", category: "Development", blurb: "Premium dark surfaces with sharp detail.", schemes: ["light","dark"], accent: "pink", swatch: ["#F4F4F5","#FFFFFF","#EC4899","#111113"] },
  { id: "beacon", name: "Beacon", category: "Development", blurb: "White sidebar, a strong blue action and square, dashboard-dense corners.", schemes: ["light","dark"], accent: "blue", swatch: ["#F2F2F2","#FFFFFF","#1E7BFE","#181818"] },
  { id: "carbon", name: "Carbon", category: "Development", blurb: "Charcoal surfaces with fine borders; at home in the dark.", schemes: ["light","dark"], accent: "graphite", swatch: ["#F4F4F4","#FFFFFF","#64748B","#171717"] },
  { id: "meadow", name: "Meadow", category: "Development", blurb: "Developer-dark neutrals with a fresh green-teal accent.", schemes: ["light","dark"], accent: "teal", swatch: ["#F8F9FA","#FFFFFF","#0EA5C4","#171717"] },
  { id: "heather", name: "Heather", category: "Development", blurb: "A hint of violet on tight, square-cornered, data-dense surfaces.", schemes: ["light","dark"], accent: "indigo", swatch: ["#FBFAFD","#FFFFFF","#6366F1","#17161B"] },
  { id: "aubergine", name: "Aubergine", category: "Development", blurb: "Deep purple-tinged surfaces that keep errors easy to spot.", schemes: ["light","dark"], accent: "indigo", swatch: ["#FAF9FB","#FFFFFF","#6366F1","#19131E"] },
  { id: "lilac", name: "Lilac", category: "Productivity", blurb: "Calm project surfaces with a soft violet.", schemes: ["light","dark"], accent: "indigo", swatch: ["#F5F5F7","#FFFFFF","#6366F1","#151519"] },
  { id: "quartz", name: "Quartz", category: "Productivity", blurb: "Near black and white, colour only on actions.", schemes: ["light","dark"], accent: "blue", swatch: ["#FAFAFA","#FFFFFF","#1E7BFE","#121214"] },
  { id: "parchment", name: "Parchment", category: "Productivity", blurb: "Warm white paper and ink, hairline borders, quiet.", schemes: ["light","dark"], accent: "blue", swatch: ["#F7F7F5","#FFFFFF","#1E7BFE","#141414"] },
  { id: "coral", name: "Coral", category: "Productivity", blurb: "Warm white documents with a coral-orange accent.", schemes: ["light","dark"], accent: "pink", swatch: ["#F8F7F5","#FFFFFF","#EC4899","#171616"] },
  { id: "iris", name: "Iris", category: "Productivity", blurb: "Soft lavender surfaces and a bright violet accent.", schemes: ["light","dark"], accent: "indigo", swatch: ["#EDE7FF","#FFFFFF","#6366F1","#181424"] },
  { id: "twilight", name: "Twilight", category: "Productivity", blurb: "Deep violet-blue darks with an elegant, restrained light.", schemes: ["light","dark"], accent: "indigo", swatch: ["#F4F3F8","#FFFFFF","#6366F1","#151526"] },
  { id: "orchid", name: "Orchid", category: "Design", blurb: "Playful purple with large round shapes.", schemes: ["light","dark"], accent: "indigo", swatch: ["#F6F3FF","#FFFFFF","#6366F1","#160F2C"] },
  { id: "marigold", name: "Marigold", category: "Design", blurb: "Playful and bright: very round corners over a warm off-white.", schemes: ["light","dark"], accent: "blue", swatch: ["#F7F6F3","#FFFFFF","#1E7BFE","#151428"] },
  { id: "cobalt", name: "Cobalt", category: "Design", blurb: "High contrast with a vivid blue accent and generous corners.", schemes: ["light","dark"], accent: "sky", swatch: ["#F3F3F3","#FFFFFF","#38BDF8","#111111"] },
  { id: "studio", name: "Studio", category: "Design", blurb: "A designer’s workspace in neutral grey with a bright blue accent.", schemes: ["light","dark"], accent: "sky", swatch: ["#F5F5F5","#FFFFFF","#38BDF8","#171717"] },
  { id: "charcoal", name: "Charcoal", category: "Communication", blurb: "Black and warm grey surfaces, softly rounded.", schemes: ["light","dark"], accent: "pink", swatch: ["#F5F5F3","#FFFFFF","#EC4899","#141414"] },
  { id: "mulberry", name: "Mulberry", category: "Communication", blurb: "Friendly plum-tinted surfaces with a green action colour.", schemes: ["light","dark"], accent: "teal", swatch: ["#F5EDF5","#FFFFFF","#0EA5C4","#15171A"] },
  { id: "console", name: "Console", category: "Communication", blurb: "Layered blue-grey darks and an indigo accent, like a game lobby.", schemes: ["light","dark"], accent: "blue", swatch: ["#E3E5E8","#FFFFFF","#1E7BFE","#1C1D20"] },
  { id: "signal", name: "Signal", category: "Enterprise", blurb: "Dark dashboard surfaces with very sharp corners.", schemes: ["light","dark"], accent: "indigo", swatch: ["#F4F5F5","#FFFFFF","#6366F1","#111217"] },
  { id: "bluebell", name: "Bluebell", category: "Enterprise", blurb: "Cool pale grey, a violet-blue accent and a deep navy dark mode.", schemes: ["light","dark"], accent: "indigo", swatch: ["#F6F8FA","#FFFFFF","#6366F1","#0C1829"] },
  { id: "regal", name: "Regal", category: "Enterprise", blurb: "Purple accents on cool greys with sharp corners for dense charts.", schemes: ["light","dark"], accent: "violet", swatch: ["#F4F4F8","#FFFFFF","#8B5CF6","#16161D"] },
  { id: "cerulean", name: "Cerulean", category: "Enterprise", blurb: "Corporate blue on near-white, with tight corners.", schemes: ["light","dark"], accent: "blue", swatch: ["#F7F8F9","#FFFFFF","#1E7BFE","#16191C"] },
  { id: "denim", name: "Denim", category: "Enterprise", blurb: "Soft neutral greys, gentle shadows and an office blue.", schemes: ["light","dark"], accent: "blue", swatch: ["#F5F5F5","#FFFFFF","#1E7BFE","#161616"] },
  { id: "sandstone", name: "Sandstone", category: "Enterprise", blurb: "Beige surfaces and an orange accent; playful but serious.", schemes: ["light","dark"], accent: "pink", swatch: ["#F3F4EF","#FFFFFF","#EC4899","#17181E"] },
  { id: "frost", name: "Frost", category: "System", blurb: "Translucent-feeling pale greys with a clear system blue.", schemes: ["light","dark"], accent: "blue", swatch: ["#E8E8EA","#FFFFFF","#1E7BFE","#171717"] },
  { id: "pebble", name: "Pebble", category: "System", blurb: "Grouped-list greys, black dark mode and a bright system blue.", schemes: ["light","dark"], accent: "blue", swatch: ["#F7F7FA","#FFFFFF","#1E7BFE","#000000"] },
  { id: "wisteria", name: "Wisteria", category: "System", blurb: "Tonal lavender surfaces with pill-shaped controls.", schemes: ["light","dark"], accent: "indigo", swatch: ["#F6F2F9","#F9F6FB","#6366F1","#121116"] },
  { id: "pane", name: "Pane", category: "System", blurb: "Layered neutral greys with a deep blue accent and tight corners.", schemes: ["light","dark"], accent: "blue", swatch: ["#F3F3F3","#FFFFFF","#1E7BFE","#1B1B1B"] },
  { id: "dove", name: "Dove", category: "System", blurb: "Soft grey sidebar, roomy rounded cards and a friendly blue.", schemes: ["light","dark"], accent: "blue", swatch: ["#EBEBED","#FFFFFF","#1E7BFE","#121215"] },
  { id: "tangerine", name: "Tangerine", category: "System", blurb: "Neutral greys warmed by an orange accent; aubergine-leaning darks.", schemes: ["light","dark"], accent: "pink", swatch: ["#EBEBEB","#FFFFFF","#EC4899","#181818"] },
  { id: "skyline", name: "Skyline", category: "System", blurb: "Light grey panels with a sky-blue accent and compact spacing.", schemes: ["light","dark"], accent: "sky", swatch: ["#EFF0F1","#FFFFFF","#38BDF8","#0D0F10"] },
]

/**
 * The knobs: each changes one aspect of the look, as tokens only, on top of
 * whichever style is active. The first option is the default and is stored as
 * no entry at all, so a knob left alone costs nothing and a client that does
 * not know a knob never sees it.
 */
export interface KnobDef {
  label: string
  default: string
  options: { value: string; label: string }[]
}
export const KNOBS: Record<string, KnobDef> = {
  radius: {
    label: "Corners",
    default: "default",
    options: [
      { value: "default", label: "Style default" },
      { value: "sharp", label: "Sharp" },
      { value: "round", label: "Round" },
    ],
  },
  cards: {
    label: "Cards",
    default: "border",
    options: [
      { value: "border", label: "Border" },
      { value: "shadow", label: "Shadow" },
    ],
  },
  lines: {
    label: "Lines",
    default: "default",
    options: [
      { value: "default", label: "Default" },
      { value: "subtle", label: "Subtle" },
      { value: "strong", label: "Strong" },
    ],
  },
}
const KNOB_ATTR = (name: string) => `data-knob-${name}`

/** A knob's value if this build knows it, else the default. */
export function knobValue(state: StyleState | null, name: string): string {
  const def = KNOBS[name]
  const v = state?.knobs[name]
  return def && v !== undefined && def.options.some((o) => o.value === v) ? v : def.default
}

/** The state with one knob set. The default is dropped; unknown knobs stay. */
export function setKnobValue(state: StyleState | null, name: string, value: string): StyleState {
  const knobs = { ...(state?.knobs ?? {}) }
  if (value === KNOBS[name]?.default) delete knobs[name]
  else knobs[name] = value
  return { v: 1, style: state?.style ?? HARBOR.id, knobs }
}

/** Reset style: Harbor, every knob at its default. Written, not deleted, so
 *  the reset reaches the account's other devices. */
export function resetStyleState(): StyleState {
  return { v: 1, style: HARBOR.id, knobs: {} }
}

/**
 * Paint a style on an element: `data-style` and one attribute per knob that is
 * off its default. Harbor stands in where the style is unknown or has no
 * palette for the scheme (see renderedStyle); a knob value this build does not
 * know paints nothing. The stored state is never touched.
 */
export function applyStyleAttributes(el: HTMLElement, state: StyleState | null, scheme: Scheme, registry: StyleDef[] = STYLES): void {
  el.setAttribute("data-style", renderedStyle(state, scheme, registry))
  for (const name of Object.keys(KNOBS)) {
    const v = knobValue(state, name)
    if (v === KNOBS[name].default) el.removeAttribute(KNOB_ATTR(name))
    else el.setAttribute(KNOB_ATTR(name), v)
  }
}

const ID = /^[a-z0-9-]{1,32}$/
const KNOB_VALUE_MAX = 64

/** A stored value, judged on its own. Unknown-but-well-formed is kept. */
export function parseStyleState(value: unknown): StyleState | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) return null
  const v = value as Record<string, unknown>
  if (typeof v.style !== "string" || !ID.test(v.style)) return null
  if (v.knobs !== undefined && (typeof v.knobs !== "object" || v.knobs === null || Array.isArray(v.knobs))) return null
  const knobs: Record<string, string> = {}
  for (const [k, val] of Object.entries((v.knobs as Record<string, unknown>) ?? {})) {
    if (ID.test(k) && typeof val === "string" && val.length <= KNOB_VALUE_MAX) knobs[k] = val
  }
  return { v: 1, style: v.style, knobs }
}

export function serializeStyleState(s: StyleState): string {
  return JSON.stringify({ v: 1, style: s.style, knobs: s.knobs })
}

export function readStyleState(): StyleState | null {
  try {
    const raw = localStorage.getItem(STYLE_STORAGE_KEY)
    return raw ? parseStyleState(JSON.parse(raw)) : null
  } catch {
    return null
  }
}

export function writeStyleState(s: StyleState): void {
  try {
    localStorage.setItem(STYLE_STORAGE_KEY, serializeStyleState(s))
  } catch {
    // Blocked storage: the choice holds for this page only.
  }
}

/** Reset style: back to Harbor with default knobs. */
export function clearStyleState(): void {
  try {
    localStorage.removeItem(STYLE_STORAGE_KEY)
  } catch {
    // nothing stored
  }
}

function find(id: string, registry: StyleDef[]): StyleDef | undefined {
  return registry.find((s) => s.id === id)
}

/**
 * The style actually painted. Harbor stands in for a style this build does not
 * know, and for a style shown in a scheme it has no palette for — the pair a
 * half-finished write on another device can leave behind. Nothing is written:
 * the stored value stays for the device that understands it, or until the
 * missing half of the write arrives.
 */
export function renderedStyle(state: StyleState | null, scheme: Scheme, registry: StyleDef[] = STYLES): string {
  const def = state ? find(state.style, registry) : undefined
  return def && def.schemes.includes(scheme) ? def.id : HARBOR.id
}

/** The theme choices a style can be shown in; System only when it has both. */
export function allowedSchemes(id: string, registry: StyleDef[] = STYLES): ThemeChoice[] {
  const def = find(id, registry)
  if (!def || def.schemes.length > 1) return ["dark", "light", "system"]
  return [...def.schemes]
}

export interface StyleSelection {
  style: StyleState
  accent: AccentChoice
  /** Set only when the current theme is one the style cannot show. */
  theme?: ThemeChoice
}

/**
 * What picking a style writes: the style (keeping the user's knobs), its
 * suggested accent, and — for a single-scheme style when the current theme is
 * another one or System — that scheme as the theme.
 */
export function planStyleSelection(
  id: string,
  current: StyleState | null,
  theme: ThemeChoice | undefined,
  registry: StyleDef[] = STYLES,
): StyleSelection {
  const def = find(id, registry)
  if (!def) throw new Error(`unknown style: ${id}`)
  const plan: StyleSelection = { style: { v: 1, style: def.id, knobs: { ...(current?.knobs ?? {}) } }, accent: def.accent }
  if (def.schemes.length === 1 && theme !== def.schemes[0]) plan.theme = def.schemes[0]
  return plan
}

/** Random changes the style only — never scheme, text size, motion or density. */
export function randomStyle(registry: StyleDef[], rng: () => number, knobs: Record<string, string> = {}): StyleState {
  const def = registry[Math.min(registry.length - 1, Math.floor(rng() * registry.length))]
  return { v: 1, style: def.id, knobs: { ...knobs } }
}

/**
 * Pre-paint part of APPEARANCE_BOOT_SCRIPT (it defines `d` and `s`). Writes
 * only a known style id and known knob values; anything else paints Harbor
 * and the defaults.
 */
const KNOB_TABLE = Object.fromEntries(Object.entries(KNOBS).map(([name, k]) => [name, k.options.map((o) => o.value).filter((v) => v !== k.default)]))
export const STYLE_BOOT_SNIPPET = `try{var sv=${JSON.stringify(HARBOR.id)},sr=s.getItem(${JSON.stringify(
  STYLE_STORAGE_KEY,
)}),kt=${JSON.stringify(KNOB_TABLE)};if(sr){var so=JSON.parse(sr);if(so&&${JSON.stringify(STYLES.map((x) => x.id))}.indexOf(so.style)>-1)sv=so.style;if(so&&so.knobs)for(var kn in kt)if(kt[kn].indexOf(so.knobs[kn])>-1)d.setAttribute("data-knob-"+kn,so.knobs[kn])}d.setAttribute("data-style",sv)}catch(e){d.setAttribute("data-style",${JSON.stringify(
  HARBOR.id,
)})}`
