import { describe, it, expect, beforeEach, afterEach, vi } from "vitest"
import { readFileSync, readdirSync } from "node:fs"
import path from "node:path"

import { ACCENTS, isAccentChoice, STYLE_ACCENTS } from "@/lib/theme/accents"
import {
  STYLES,
  HARBOR,
  STYLE_CATEGORIES,
  KNOBS,
  STYLE_BOOT_SNIPPET,
  STYLE_STORAGE_KEY,
  allowedSchemes,
  applyStyleAttributes,
  planStyleSelection,
  resetStyleState,
  setKnobValue,
} from "@/lib/theme/appearance-style"
import { stylePreviewAttrs } from "@/lib/theme/style-preview"

// STY-01..05 (PRD §11): the registry, the token blocks and the knobs. A style
// is a block of token values and nothing else; components never learn which
// style is active.

const root = path.resolve(__dirname, "../../..")
const read = (f: string) => readFileSync(path.join(root, f), "utf8")
const stylesCss = read("app/styles/styles.css")
const globals = read("app/globals.css")
const bare = stylesCss.replace(/\/\*[\s\S]*?\*\//g, "")
const globalsBare = globals.replace(/\/\*[\s\S]*?\*\//g, "")

// Names of the products the first batch was drawn from (ODCH-10): none of
// them may appear in an id, a name, a description or the stylesheet.
const BRANDS = [
  "claude", "anthropic", "openai", "chatgpt", "linear", "gitlab", "github", "miro", "cloudflare", "cursor", "notion", "framer", "lovable", "vercel", "stripe", "slack",
  "supabase", "railway", "sentry", "raycast", "perplexity", "mistral", "v0", "coda", "height", "attio", "arc", "superhuman", "figma", "pitch", "discord", "intercom",
  "datadog", "grafana", "atlassian", "jira", "fluent", "microsoft", "posthog", "macos", "apple", "ios", "android", "google", "windows", "gnome", "ubuntu", "kde",
  "confluence", "pajamas", "primer", "geist", "segoe", "roboto", "yaru", "breeze", "libadwaita", "plasma", "material", "sequoia", "tahoe", "mica", "canonical",
]

// Wireframe v67 shows 42 styles under product names. The app carries each one
// under a neutral name: wireframe id -> app id. Harbor and Harbor Night are
// Crewship's own and have no counterpart.
const WIREFRAME_PORTS: Record<string, string> = {
  openai: "ink", lovable: "blush", mistral: "sunrise", v0: "pewter", linear: "slate", vercel: "mono", github: "cool-grey", railway: "plum",
  raycast: "ember", height: "lilac", attio: "quartz", pitch: "orchid", intercom: "charcoal", grafana: "signal",
  claude: "terracotta", cloudflare: "beacon", cursor: "carbon", supabase: "meadow", gitlab: "heather", sentry: "aubergine", perplexity: "lagoon",
  notion: "parchment", coda: "coral", arc: "iris", superhuman: "twilight", miro: "marigold", framer: "cobalt", figma: "studio", slack: "mulberry",
  discord: "console", stripe: "bluebell", datadog: "regal", atlassian: "cerulean", fluent: "denim", posthog: "sandstone", macos: "frost", ios: "pebble",
  android: "wisteria", windows: "pane", gnome: "dove", ubuntu: "tangerine", kde: "skyline",
}

describe("registry (STY-01, STY-04)", () => {
  it("Harbor is first and the default; the first batch follows", () => {
    expect(STYLES[0]).toBe(HARBOR)
    expect(STYLES.length).toBeGreaterThanOrEqual(13)
  })

  it("carries every wireframe v67 style under a neutral name: 42 ports + Harbor + Harbor Night", () => {
    expect(Object.keys(WIREFRAME_PORTS)).toHaveLength(42)
    expect(STYLES).toHaveLength(44)
    const ids = new Set(STYLES.map((s) => s.id))
    for (const [wireframe, id] of Object.entries(WIREFRAME_PORTS)) expect(ids.has(id), `${wireframe} -> ${id}`).toBe(true)
    expect(new Set(Object.values(WIREFRAME_PORTS)).size).toBe(42)
    expect(new Set(STYLES.map((s) => s.name)).size).toBe(STYLES.length)
  })

  it("styles are listed grouped by category, in gallery order", () => {
    const order = STYLES.map((s) => STYLE_CATEGORIES.indexOf(s.category))
    expect(order).toEqual([...order].sort((a, b) => a - b))
  })

  it("each style carries its own accent from the design; the hidden ones are not in the picker", () => {
    const style = (id: string) => STYLES.find((s) => s.id === id)!
    expect(style("terracotta").accent).toBe("clay")
    expect(ACCENTS.map((a) => a.id as string)).not.toContain("clay")
    const hidden = new Set(STYLE_ACCENTS.map((a) => a.id as string))
    const carried = STYLES.filter((s) => s.id !== "harbor" && s.id !== "harbor-night").map((s) => s.accent as string)
    for (const a of carried) expect(hidden.has(a), a).toBe(true)
    // Every hidden accent belongs to some style; one spare is not allowed.
    expect([...hidden].filter((a) => !carried.includes(a))).toEqual([])
    expect(new Set(carried).size).toBeGreaterThanOrEqual(40)
  })

  it("ids are unique and well-formed", () => {
    const ids = STYLES.map((s) => s.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const id of ids) expect(id).toMatch(/^[a-z0-9-]{1,32}$/)
  })

  it("Harbor Night is its own dark-only style with its own accent", () => {
    const night = STYLES.find((s) => s.id === "harbor-night")!
    expect(night.name).toBe("Harbor Night")
    expect(night.schemes).toEqual(["dark"])
    expect(night.accent).not.toBe(HARBOR.accent)
    expect(night.category).toBe("Crewship")
  })

  it("every style names itself, says what it is, sits in a known category and suggests a real accent", () => {
    for (const s of STYLES) {
      expect(s.name.length, s.id).toBeGreaterThan(1)
      expect(s.blurb.length, s.id).toBeGreaterThan(10)
      expect(STYLE_CATEGORIES, s.id).toContain(s.category)
      expect(isAccentChoice(s.accent), s.id).toBe(true)
      expect(s.swatch, s.id).toHaveLength(4)
      for (const c of s.swatch) expect(c).toMatch(/^#[0-9A-F]{6}$/)
    }
  })

  it("names are neutral: no product or company name anywhere (ODCH-10)", () => {
    const text = [...STYLES.flatMap((s) => [s.id, s.name, s.blurb, s.category]), stylesCss].join(" ").toLowerCase()
    for (const brand of BRANDS) expect(text, brand).not.toMatch(new RegExp(`(^|[^a-z0-9])${brand}([^a-z0-9]|$)`))
  })

  it("every category in use is listed, in gallery order", () => {
    const used = new Set(STYLES.map((s) => s.category))
    for (const c of STYLE_CATEGORIES) expect(used.has(c), c).toBe(true)
  })
})

describe("token blocks (STY-02)", () => {
  it("each style has exactly one block per scheme it supports, and no block for another", () => {
    const selectors = [...bare.matchAll(/([^{}]+)\{/g)].map((m) => m[1].trim())
    for (const s of STYLES) {
      expect(selectors.includes(`[data-style="${s.id}"]:not(.dark)`), `${s.id} light`).toBe(s.schemes.includes("light"))
      expect(selectors.includes(`.dark[data-style="${s.id}"]`), `${s.id} dark`).toBe(s.schemes.includes("dark"))
      expect(selectors.includes(`[data-style="${s.id}"]`), `${s.id} shape`).toBe(true)
    }
  })

  it("no block belongs to a style the registry does not list", () => {
    const ids = new Set(STYLES.map((s) => s.id))
    for (const m of bare.matchAll(/\[data-style="([a-z0-9-]+)"\]/g)) expect(ids.has(m[1]), m[1]).toBe(true)
  })

  it("a style block only sets custom properties — no rules about components", () => {
    for (const m of bare.matchAll(/([^{}]*data-style="[^{}]*)\{([^{}]*)\}/g)) {
      for (const decl of m[2].split(";").map((d) => d.trim()).filter(Boolean)) expect(decl, `${m[1].trim()} ${decl}`).toMatch(/^--[a-z0-9-]+:\s*\S/)
    }
  })

  it("Harbor's block restates the base tokens exactly, so a preview of Harbor inside another style resets to it", () => {
    const decls = (body: string) => new Map([...body.matchAll(/--([a-z0-9-]+):\s*([^;]+);/g)].map((m) => [m[1], m[2].trim()] as const))
    const blockOf = (selector: string) => [...bare.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find(([, sel]) => sel.trim() === selector)![2]
    const base = {
      // Comments stripped: a comment that mentions "--card: 1=4.78" is not
      // the token (the block was once copied from the commented source).
      light: decls(globals.slice(globals.indexOf(":root {\n  /* ── Light surfaces"), globals.indexOf("\n.dark {")).replace(/\/\*[\s\S]*?\*\//g, "")),
      dark: decls(globals.slice(globals.indexOf("\n.dark {"), globals.indexOf("@theme inline")).replace(/\/\*[\s\S]*?\*\//g, "")),
    }
    for (const [scheme, selector] of [["light", '[data-style="harbor"]:not(.dark)'], ["dark", '.dark[data-style="harbor"]']] as const) {
      const own = decls(blockOf(selector))
      expect(own.size, scheme).toBeGreaterThan(15)
      for (const [name, value] of own) expect(value, `--${name} (${scheme})`).toBe(base[scheme].get(name))
    }
  })

  it("every token in a style block is one CSS value: no comment text leaked into it", () => {
    // a11y.spec.ts New Agent dialog (nightly 39130662018): --card in Harbor
    // held "1=4.78/4.69, …" from a chart comment, so bg-card was transparent
    // and every dialog read its text against the scrim.
    for (const m of stylesCss.matchAll(/([^{}]*data-style="[^{}]*)\{([^{}]*)\}/g)) {
      for (const d of m[2].matchAll(/--([a-z0-9-]+):\s*([^;]*);/g)) {
        expect(d[2], `${m[1].trim()} --${d[1]}`).not.toMatch(/\/\*|\*\/|\n\s*\*|=/)
      }
    }
  })

  it("Harbor's card is the base card colour in both schemes", () => {
    expect(globalsBare).toMatch(/:root \{[\s\S]*?--card: #FFFFFF;/)
    expect(bare).toMatch(/\[data-style="harbor"\]:not\(\.dark\) \{[^}]*--card: #FFFFFF;/)
    expect(bare).toMatch(/\.dark\[data-style="harbor"\] \{[^}]*--card: #181A1F;/)
  })

  it("globals.css loads the styles after the accents", () => {
    expect(globals).toMatch(/@import "\.\/styles\/accents\.css";\s*@import "\.\/styles\/styles\.css";/)
  })
})

describe("no style outside the theme files (STY-02 guard)", () => {
  const ALLOWED = [/^lib\/theme\//, /^app\/styles\//, /^app\/globals\.css$/]
  const SKIP_DIRS = new Set(["node_modules", ".next", ".git", "__tests__", "e2e", "test-results", "lib/generated", ".claude", ".orca"])
  const PATTERN = /data-style|dataset\.style|dataset\["style"\]|dataset\['style'\]/
  const walk = (dir: string): string[] =>
    readdirSync(path.join(root, dir), { withFileTypes: true }).flatMap((e) => {
      const rel = dir ? `${dir}/${e.name}` : e.name
      if (e.isDirectory()) return SKIP_DIRS.has(e.name) || SKIP_DIRS.has(rel) ? [] : walk(rel)
      return /\.(tsx?|css|mjs|jsx?)$/.test(e.name) && !/\.test\./.test(e.name) ? [rel] : []
    })

  it("components, hooks and pages never read or write data-style", () => {
    const offenders = ["app", "components", "hooks", "lib"]
      .flatMap(walk)
      .filter((f) => !ALLOWED.some((re) => re.test(f)))
      .filter((f) => PATTERN.test(read(f)))
    expect(offenders).toEqual([])
  })
})

describe("fonts (STY-05, ODCH-11)", () => {
  it("a style loads no font and names no family", () => {
    expect(bare).not.toMatch(/font-family|@font-face|@import|url\(/)
    expect(bare).not.toMatch(/--font-/)
  })

  it("the app still bundles only Inter and JetBrains Mono, from its own files", () => {
    const layout = read("app/layout.tsx")
    expect(layout.match(/localFont\(/g)).toHaveLength(2)
    expect(layout).not.toMatch(/next\/font\/google|fonts\.googleapis|fonts\.gstatic/)
  })
})

describe("knobs (STY-03)", () => {
  it("the knobs v67 offers as pure token changes: corners, cards, lines", () => {
    expect(Object.keys(KNOBS).sort()).toEqual(["cards", "lines", "radius"])
    expect(KNOBS.radius.options.map((o) => o.value)).toEqual(["default", "sharp", "round"])
    expect(KNOBS.cards.options.map((o) => o.value)).toEqual(["border", "shadow"])
    expect(KNOBS.lines.options.map((o) => o.value)).toEqual(["default", "subtle", "strong"])
    for (const k of Object.values(KNOBS)) expect(k.options[0].value).toBe(k.default)
  })

  it("corners scale all six radius tokens by one factor", () => {
    for (const t of ["radius-sm", "radius-md", "radius-lg", "radius-xl", "radius-card", "radius-row"]) {
      expect(globals, t).toMatch(new RegExp(`--${t}:[^;]*var\\(--radius-k, 1\\)`))
    }
    expect(bare).toMatch(/\[data-knob-radius="sharp"\]\s*\{\s*--radius-k:\s*0\.4;/)
    expect(bare).toMatch(/\[data-knob-radius="round"\]\s*\{\s*--radius-k:\s*1\.5;/)
  })

  it("cards are border-only by default; Shadow adds one to rounded-card surfaces", () => {
    expect(bare).toMatch(/\[data-knob-cards="shadow"\]\s+\.rounded-card\s*\{[^}]*box-shadow:/)
    expect(bare).not.toMatch(/data-knob-cards="border"/)
  })

  it("lines change only the border tokens", () => {
    for (const v of ["subtle", "strong"]) {
      const m = bare.match(new RegExp(`\\[data-knob-lines="${v}"\\]\\s*\\{([^}]*)\\}`))
      expect(m, v).toBeTruthy()
      const props = [...m![1].matchAll(/--([a-z-]+):/g)].map((x) => x[1]).sort()
      expect(props.every((p) => ["border", "input", "sidebar-border", "line-strong"].includes(p)), props.join()).toBe(true)
    }
  })
})

describe("applying a style to the page", () => {
  const html = () => document.documentElement
  beforeEach(() => {
    for (const a of ["data-style", "data-knob-radius", "data-knob-cards", "data-knob-lines"]) html().removeAttribute(a)
  })

  it("paints the style and every known knob; a default knob leaves no attribute", () => {
    applyStyleAttributes(html(), { v: 1, style: "slate", knobs: { radius: "round", cards: "border" } }, "dark")
    expect(html().getAttribute("data-style")).toBe("slate")
    expect(html().getAttribute("data-knob-radius")).toBe("round")
    expect(html().hasAttribute("data-knob-cards")).toBe(false)
    expect(html().hasAttribute("data-knob-lines")).toBe(false)
  })

  it("each knob changes only its own attribute", () => {
    const before = { v: 1 as const, style: "harbor", knobs: { radius: "sharp" } }
    applyStyleAttributes(html(), before, "light")
    const next = setKnobValue(before, "cards", "shadow")
    applyStyleAttributes(html(), next, "light")
    expect(html().getAttribute("data-knob-radius")).toBe("sharp")
    expect(html().getAttribute("data-knob-cards")).toBe("shadow")
    expect(html().hasAttribute("data-knob-lines")).toBe(false)
    expect(next.knobs).toEqual({ radius: "sharp", cards: "shadow" })
  })

  it("an unknown style, an unsupported scheme or an unknown knob value paints Harbor / the default and stays stored", () => {
    const stored = { v: 1 as const, style: "from-the-future", knobs: { radius: "huge", glow: "max" } }
    applyStyleAttributes(html(), stored, "dark")
    expect(html().getAttribute("data-style")).toBe("harbor")
    expect(html().hasAttribute("data-knob-radius")).toBe(false)
    expect(stored.knobs).toEqual({ radius: "huge", glow: "max" })
    applyStyleAttributes(html(), { v: 1, style: "harbor-night", knobs: {} }, "light")
    expect(html().getAttribute("data-style")).toBe("harbor")
  })

  it("setKnobValue keeps unknown knobs and drops a knob set back to its default", () => {
    const s = setKnobValue({ v: 1, style: "harbor", knobs: { glow: "max", radius: "sharp" } }, "radius", "default")
    expect(s.knobs).toEqual({ glow: "max" })
  })
})

describe("picking and resetting", () => {
  it("every style suggests its own accent; a single-scheme style also sets the theme", () => {
    for (const s of STYLES) {
      const plan = planStyleSelection(s.id, null, "system")
      expect(plan.accent).toBe(s.accent)
      expect(plan.style.style).toBe(s.id)
      expect(plan.theme).toBe(s.schemes.length === 1 ? s.schemes[0] : undefined)
    }
  })

  it("a single-scheme style allows only its scheme (no System)", () => {
    expect(allowedSchemes("harbor-night")).toEqual(["dark"])
    expect(allowedSchemes("slate")).toEqual(["dark", "light", "system"])
  })

  it("reset gives Harbor with default knobs", () => {
    expect(resetStyleState()).toEqual({ v: 1, style: "harbor", knobs: {} })
  })
})

describe("boot snippet knows the knobs", () => {
  let store: Map<string, string>
  beforeEach(() => {
    store = new Map()
    vi.stubGlobal("localStorage", { getItem: (k: string) => store.get(k) ?? null, setItem: (k: string, v: string) => void store.set(k, v), removeItem: (k: string) => void store.delete(k) })
    for (const a of ["data-style", "data-knob-radius", "data-knob-cards", "data-knob-lines"]) document.documentElement.removeAttribute(a)
  })
  afterEach(() => vi.unstubAllGlobals())
  const run = () => new Function(`var d=document.documentElement,s=localStorage;${STYLE_BOOT_SNIPPET}`)()

  it("paints a known style and known knob values before first paint", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "slate", knobs: { radius: "sharp", cards: "shadow", lines: "strong" } }))
    run()
    expect(document.documentElement.getAttribute("data-style")).toBe("slate")
    expect(document.documentElement.getAttribute("data-knob-radius")).toBe("sharp")
    expect(document.documentElement.getAttribute("data-knob-cards")).toBe("shadow")
    expect(document.documentElement.getAttribute("data-knob-lines")).toBe("strong")
  })

  it("ignores values it does not know", () => {
    store.set(STYLE_STORAGE_KEY, JSON.stringify({ v: 1, style: "slate", knobs: { radius: "x\" onload=\"1", cards: "flat" } }))
    run()
    expect(document.documentElement.hasAttribute("data-knob-radius")).toBe(false)
    expect(document.documentElement.hasAttribute("data-knob-cards")).toBe(false)
  })
})

describe("preview attributes", () => {
  it("name the style and scheme so a tile can show a style without the page being in it", () => {
    expect(stylePreviewAttrs("slate", "dark")).toEqual({ "data-style": "slate", className: "dark" })
    expect(stylePreviewAttrs("slate", "light")).toEqual({ "data-style": "slate" })
    expect(stylePreviewAttrs("harbor", "dark")).toEqual({ "data-style": "harbor", className: "dark" })
  })
})
