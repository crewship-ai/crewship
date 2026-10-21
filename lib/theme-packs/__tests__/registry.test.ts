import { describe, it, expect } from "vitest"
import { readFileSync, readdirSync, writeFileSync } from "node:fs"
import path from "node:path"

import { PACKS, PACK_CATEGORIES, RESERVED_TOKEN, getPack, packAvatarStyle } from "@/lib/theme-packs"
import { packsCss } from "@/lib/theme-packs/css"
import { STYLES, STYLE_CATEGORIES, allowedSchemes, planStyleSelection, renderedStyle, STYLE_BOOT_SNIPPET } from "@/lib/theme/appearance-style"

// #3790: the theme-pack registry. A pack is one file plus one registry line;
// everything else (gallery tile, boot script, workspace default, CSS) follows.

const root = path.resolve(__dirname, "../../..")
const read = (f: string) => readFileSync(path.join(root, f), "utf8")
const CSS_FILE = "app/styles/packs.generated.css"

// Names a pack must not carry (no third-party trademarks, game names or logos).
const TRADEMARKS = ["minecraft", "mojang", "roblox", "fortnite", "valorant", "zelda", "nintendo", "pokemon", "steam", "playstation", "xbox", "blizzard", "league of legends", "counter-strike", "twitch", "discord"]

describe("registry", () => {
  it("ships Afterglow in the Gaming category", () => {
    const p = getPack("afterglow")!
    expect(p.name).toBe("Afterglow")
    expect(p.category).toBe("Gaming")
    expect(PACK_CATEGORIES).toContain("Gaming")
    expect(STYLE_CATEGORIES).toContain("Gaming")
  })

  it("ids are well-formed and unique across packs and built-in styles", () => {
    const ids = STYLES.map((s) => s.id)
    expect(new Set(ids).size).toBe(ids.length)
    for (const p of PACKS) expect(p.id).toMatch(/^[a-z0-9-]{1,32}$/)
  })

  it("every pack appears in STYLES with its facts, after the built-in styles", () => {
    for (const p of PACKS) {
      const s = STYLES.find((x) => x.id === p.id)!
      expect(s, p.id).toBeTruthy()
      expect(s.category).toBe(p.category)
      expect(s.schemes).toEqual(p.schemes)
      expect(s.accent).toBe(p.accent)
      expect(s.swatch).toEqual(p.swatch)
    }
    expect(STYLES[0].id).toBe("harbor")
  })

  it("a pack has a palette for every scheme it claims, and none for another", () => {
    for (const p of PACKS) {
      for (const scheme of ["light", "dark"] as const) {
        const has = Object.keys(p.tokens[scheme] ?? {}).length > 15
        expect(has, `${p.id} ${scheme}`).toBe(p.schemes.includes(scheme))
      }
    }
  })

  it("Afterglow is dark only: a light-mode user is moved to Dark when picking it, and sees Harbor otherwise", () => {
    expect(allowedSchemes("afterglow")).toEqual(["dark"])
    expect(planStyleSelection("afterglow", null, "light").theme).toBe("dark")
    expect(planStyleSelection("afterglow", null, "system").theme).toBe("dark")
    const state = { v: 1 as const, style: "afterglow", knobs: {} }
    expect(renderedStyle(state, "dark")).toBe("afterglow")
    expect(renderedStyle(state, "light")).toBe("harbor")
  })

  it("the boot script knows pack ids", () => {
    expect(STYLE_BOOT_SNIPPET).toContain('"afterglow"')
  })
})

describe("what a pack may not do", () => {
  it("never sets a brand, logo or sail token: the Crewship sail stays the fixed Crewship blue", () => {
    for (const p of PACKS) {
      for (const scheme of ["light", "dark"] as const) {
        for (const name of Object.keys(p.tokens[scheme] ?? {})) expect(name, `${p.id} --${name}`).not.toMatch(RESERVED_TOKEN)
      }
      expect(p.accent, p.id).toBe("blue")
    }
  })

  it("the generated CSS touches no brand token", () => {
    expect(read(CSS_FILE)).not.toMatch(/--(brand|logo|sail)[a-z-]*\s*:/)
  })

  it("the sail components keep their own blue and read no pack token", () => {
    const logo = read("components/branding/crewship-logo.tsx")
    expect(logo).toContain("#1B75FE")
    expect(logo).not.toMatch(/var\(--(pack|primary|brand)/)
    expect(read("lib/brand-mark.ts")).not.toMatch(/var\(--/)
  })

  it("carries no third-party trademark in an id, a name, a blurb, a token or the generated CSS", () => {
    const text = [...PACKS.flatMap((p) => [p.id, p.name, p.blurb, p.avatarStyle ?? "", JSON.stringify(p.tokens)]), read(CSS_FILE)].join(" ").toLowerCase()
    for (const t of TRADEMARKS) expect(text, t).not.toContain(t)
  })

  it("token values are plain colours or gradients, nothing that loads or runs", () => {
    for (const p of PACKS) {
      for (const tokens of Object.values(p.tokens)) {
        for (const [name, value] of Object.entries(tokens ?? {})) {
          expect(name).toMatch(/^[a-z0-9-]+$/)
          expect(value, name).not.toMatch(/url\(|@import|expression|;|\{|\}|\/\*/)
        }
      }
    }
  })
})

describe("generated stylesheet", () => {
  // Regenerate with: UPDATE_PACKS_CSS=1 pnpm vitest run lib/theme-packs
  if (process.env.UPDATE_PACKS_CSS) writeFileSync(path.join(root, CSS_FILE), packsCss(PACKS))

  it("is up to date with the pack files", () => {
    expect(read(CSS_FILE)).toBe(packsCss(PACKS))
  })

  it("has the block selectors the style system parses, per scheme the pack supports", () => {
    const css = read(CSS_FILE)
    for (const p of PACKS) {
      expect(css).toContain(`[data-style="${p.id}"] {`)
      expect(css.includes(`.dark[data-style="${p.id}"] {`)).toBe(p.schemes.includes("dark"))
      expect(css.includes(`[data-style="${p.id}"]:not(.dark) {`)).toBe(p.schemes.includes("light"))
    }
  })

  it("loads fonts only through @font-face for files that exist, and never at import time", () => {
    const css = read(CSS_FILE)
    expect(css).not.toMatch(/@import/)
    for (const m of css.matchAll(/url\((\/fonts\/[^)]+)\)/g)) expect(() => readFileSync(path.join(root, "public", m[1])), m[1]).not.toThrow()
    expect(css).toMatch(/font-display:\s*swap/)
  })

  it("page titles and big numbers wear the display font only inside the pack", () => {
    const css = read(CSS_FILE)
    const rule = css.match(/\[data-style="afterglow"\] :is\((.*)\)\s*\{([^}]*)\}/)!
    expect(rule[1]).toContain(".type-title")
    expect(rule[1]).toContain('h1[class*="text-title"]')
    expect(rule[1]).toContain('[data-slot="settings-page-head"]')
    expect(rule[2]).toContain("font-family: var(--font-display)")
    expect(rule[2]).toContain("text-transform: uppercase")
    expect(css).toContain('--font-display: "Big Shoulders Display"')
    expect(css).toContain('--font-sans: "Manrope"')
    const body = css.replace(/\/\*[\s\S]*?\*\//g, "")
    for (const sel of [...body.matchAll(/([^{}]+)\{/g)].map((m) => m[1].trim()).filter((s) => !s.startsWith("@") && !/^\d+%|^from|^to/.test(s))) {
      expect(sel, "every rule is scoped to a pack").toMatch(/data-style="[a-z0-9-]+"/)
    }
  })

  it("glow pulsing is off under reduce motion, in both the OS query and the app setting", () => {
    const css = read(CSS_FILE)
    expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\)\s*\{[^@]*:not\(\[data-motion="full"\]\)[^@]*animation:\s*none/)
    expect(css).toMatch(/:root\[data-motion="reduce"\]\[data-style="afterglow"\] \.pack-pulse\s*\{\s*animation:\s*none/)
  })

  it("the postcard tilt applies to overview cards only, never to a list", () => {
    const css = read(CSS_FILE)
    expect(css).toMatch(/\[data-style="afterglow"\] \.pack-postcard/)
    expect(css).not.toMatch(/\[data-style="afterglow"\] (li|tr|\[role="?row)/)
  })
})

describe("avatar default", () => {
  it("Afterglow asks for voxel-bot, and only when the build can render it", () => {
    expect(getPack("afterglow")!.avatarStyle).toBe("voxel-bot")
    expect(packAvatarStyle("afterglow", (s) => s === "voxel-bot")).toBe("voxel-bot")
    expect(packAvatarStyle("afterglow", () => false)).toBeUndefined()
    expect(packAvatarStyle("harbor", () => true)).toBeUndefined()
    expect(packAvatarStyle(undefined, () => true)).toBeUndefined()
  })
})

describe("one file per pack", () => {
  it("every pack file in lib/theme-packs is registered", () => {
    const files = readdirSync(path.join(root, "lib/theme-packs")).filter((f) => f.endsWith(".ts") && !["index.ts", "types.ts", "css.ts"].includes(f))
    expect(files.map((f) => f.replace(/\.ts$/, "")).sort()).toEqual(PACKS.map((p) => p.id).sort())
  })
})
