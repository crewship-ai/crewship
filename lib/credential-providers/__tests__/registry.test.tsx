// The brand registry is a 450-row hand-maintained table, and every one of
// its failure modes is silent at author time: an `Si*` export that no longer
// exists is `undefined` and only explodes when React tries to render it; a
// keyword that happens to be a substring of another brand's keyword quietly
// re-points every credential named after it; the generic fallback leaking
// into the grid gives the user a "brand" called Generic secret to pick.
//
// These tests exercise the whole table rather than a sample, because a table
// is exactly the shape of input where a spot check proves nothing about row
// 300.

import { describe, it, expect } from "vitest"
import { render } from "@testing-library/react"
import { createElement } from "react"

import {
  BRAND_REGISTRY,
  BRAND_CATEGORIES,
  GENERIC_BRAND,
  brandColor,
  detectBrandFromName,
  detectBrandFromValue,
  getBrand,
  type BrandEntry,
} from "../registry"

const byKey = (key: string): BrandEntry => {
  const hit = BRAND_REGISTRY.find((b) => b.key === key)
  expect(hit, `brand ${key} is missing from the registry`).toBeDefined()
  return hit!
}

describe("registry shape", () => {
  it("covers the household names an operator expects to find, not just dev tooling", () => {
    // A sample across the categories the expansion was for. Each of these was
    // absent while the picker still claimed to be a brand picker.
    for (const key of [
      "MICROSOFT", "MICROSOFT_365", "OUTLOOK", "MSTEAMS", "AZURE", "ONEDRIVE",
      "GMAIL", "GOOGLE_CALENDAR", "GOOGLE_SHEETS", "GOOGLE_MEET",
      "APPLE", "ICLOUD", "AMAZON", "LINKEDIN", "ADOBE", "CANVA", "DOCUSIGN",
      "ZENDESK", "INTERCOM", "QUICKBOOKS", "XERO", "BOX", "ORACLE", "IBM",
      "MISTRAL", "DEEPSEEK", "GROQ", "COHERE", "OPENROUTER", "COPILOT",
      "NETFLIX", "UBER", "AIRBNB", "STEAM", "TAILSCALE", "SPLUNK", "FASTLY",
    ]) {
      byKey(key)
    }
  })

  it("gives every row a renderable icon", () => {
    // The single failure this file exists for: `SiWhatever` dropped upstream
    // imports as undefined, and `<undefined />` is a render-time crash in the
    // credentials list, not a build error.
    for (const b of BRAND_REGISTRY) {
      expect(b.Icon, `${b.key} has no Icon`).toBeTruthy()
      const { container } = render(createElement(b.Icon, { className: "size-4" }))
      expect(container.querySelector("svg"), `${b.key} rendered no <svg>`).not.toBeNull()
    }
  })

  it("keys are unique and UPPER_SNAKE", () => {
    const seen = new Set<string>()
    for (const b of BRAND_REGISTRY) {
      expect(b.key, `duplicate key ${b.key}`).not.toBe([...seen].find((k) => k === b.key))
      expect(seen.has(b.key), `duplicate key ${b.key}`).toBe(false)
      seen.add(b.key)
      expect(b.key, `${b.key} is not UPPER_SNAKE`).toMatch(/^[A-Z0-9]+(_[A-Z0-9]+)*$/)
    }
  })

  it("labels are unique, so two tiles never read the same", () => {
    const seen = new Set<string>()
    for (const b of BRAND_REGISTRY) {
      expect(seen.has(b.label), `duplicate label ${b.label}`).toBe(false)
      seen.add(b.label)
    }
  })

  it("colours are 6-digit uppercase hex", () => {
    for (const b of BRAND_REGISTRY) {
      expect(b.hex, `${b.key} hex`).toMatch(/^#[0-9A-F]{6}$/)
      if (b.darkHex !== undefined) expect(b.darkHex, `${b.key} darkHex`).toMatch(/^#[0-9A-F]{6}$/)
    }
  })

  // The app is dark by default (see brandColor's INVARIANT note), so a
  // near-black brand without a darkHex is an invisible tile.
  it("every near-black brand carries a dark-surface colour", () => {
    const luminance = (hex: string) => {
      const channels = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
      const linear = (c: number) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4)
      const [r, g, b] = channels.map(linear)
      return 0.2126 * r + 0.7152 * g + 0.0722 * b
    }
    for (const b of BRAND_REGISTRY) {
      if (luminance(b.hex) >= 0.03) continue
      expect(luminance(brandColor(b)), `${b.key} (${b.hex}) vanishes on the dark theme`).toBeGreaterThan(0.05)
    }
  })

  it("uses only declared categories", () => {
    for (const b of BRAND_REGISTRY) {
      expect(BRAND_CATEGORIES, `${b.key} category ${b.category}`).toContain(b.category)
    }
  })

  it("keeps keywords lowercase, since the matcher lowercases the name only", () => {
    for (const b of BRAND_REGISTRY) {
      for (const k of b.keywords ?? []) expect(k, `${b.key} keyword`).toBe(k.toLowerCase())
    }
  })
})

// ── Keyword shadowing ────────────────────────────────────────────────
//
// detectBrandFromName takes the first hit walking the array top to bottom, so
// a keyword that is a substring of a LATER entry's keyword steals it. This is
// the failure a new row introduces most easily and the one nobody notices:
// the credential still saves, it just wears the wrong badge forever.
//
// A handful of overlaps are the intended reading rather than an accident, and
// they are enumerated here so that adding a 451st brand cannot quietly join
// the list. Each entry is "this keyword resolves to that brand, on purpose".
const DELIBERATE_UMBRELLAS: Record<string, string> = {
  // "google" is the umbrella for the whole Google estate: a credential named
  // GOOGLE_SHEETS_KEY landing on the Google mark is right, and predates the
  // expansion (GCP, GA, GDRIVE and GMAPS were already read this way).
  googlecloud: "GOOGLE",
  googleanalytics: "GOOGLE",
  googledrive: "GOOGLE",
  googlemaps: "GOOGLE",
  googlemeet: "GOOGLE",
  googlechat: "GOOGLE",
  googlecalendar: "GOOGLE",
  googlesheets: "GOOGLE",
  googledocs: "GOOGLE",
  googleslides: "GOOGLE",
  googleforms: "GOOGLE",
  googlekeep: "GOOGLE",
  googlepay: "GOOGLE",
  googlephotos: "GOOGLE",
  googletagmanager: "GOOGLE",
  googleplay: "GOOGLE",
  googleads: "GOOGLE",
  // Same company, same colour, same mark family.
  elasticsearch: "ELASTIC",
  githubactions: "GITHUB",
}

describe("detectBrandFromName", () => {
  it("resolves every keyword to the brand that declares it", () => {
    const stolen: string[] = []
    for (const b of BRAND_REGISTRY) {
      for (const k of b.keywords ?? []) {
        const hit = detectBrandFromName(k)
        if (hit?.key === b.key) continue
        if (DELIBERATE_UMBRELLAS[k] === hit?.key) continue
        stolen.push(`${b.key} keyword "${k}" resolves to ${hit?.key ?? "nothing"}`)
      }
    }
    expect(stolen).toEqual([])
  })

  it("does not list an umbrella exception for a keyword that no longer collides", () => {
    // Keeps the allow-list honest: an entry that has been fixed must be
    // deleted from it rather than left as cover for the next collision.
    for (const [keyword, expected] of Object.entries(DELIBERATE_UMBRELLAS)) {
      expect(detectBrandFromName(keyword)?.key, `${keyword} no longer resolves to ${expected}`).toBe(expected)
    }
  })

  // The regressions the expansion fixed or could have caused, pinned by name.
  it.each([
    ["LINEAR_API_KEY", "LINEAR"],   // LINE's bare "line" keyword used to win
    ["NGROK_AUTHTOKEN", "NGROK"],   // "grok" is a substring of "ngrok"
    ["ONEDRIVE_TOKEN", "ONEDRIVE"], // GDRIVE's "drive" is a substring of it
    ["AZURE_DEVOPS_PAT", "AZURE_DEVOPS"],
    ["AZURE_STORAGE_KEY", "AZURE"],
    ["MICROSOFT_365_CLIENT_SECRET", "MICROSOFT_365"],
    ["APPLE_PAY_CERT", "APPLE_PAY"],
    ["AWS_ACCESS_KEY_ID", "AWS"],
    ["AMAZON_SELLER_TOKEN", "AMAZON"],
    ["WHATSAPP_TOKEN", "WHATSAPP"],  // ahead of SAP's "sap"
    ["GITHUB_TOKEN", "GITHUB"],      // ahead of Copilot
    ["GITHUB_COPILOT_TOKEN", "COPILOT"],
    ["VAULTWARDEN_KEY", "VAULTWARDEN"], // ahead of Vault's "vault"
    ["KUBERNETES_TOKEN", "KUBERNETES"], // ahead of Uber's "uber" and Elasticsearch's old "es_"
    ["MAPBOX_TOKEN", "MAPBOX"],
    ["PROTONMAIL_BRIDGE", "PROTONMAIL"],
  ])("reads %s as %s", (name, key) => {
    expect(detectBrandFromName(name)?.key).toBe(key)
  })

  it("returns null rather than the generic brand when nothing matches", () => {
    expect(detectBrandFromName("qqq_zzz_nothing_like_a_brand")).toBeNull()
  })
})

describe("detectBrandFromValue", () => {
  it("resolves every prefix to the brand that declares it", () => {
    const stolen: string[] = []
    for (const b of BRAND_REGISTRY) {
      for (const p of b.prefixes ?? []) {
        // Pad so the 4-character minimum in detectBrandFromValue can't be the
        // reason a short prefix looks unmatched.
        const hit = detectBrandFromValue(p + "0123456789")
        if (hit?.key !== b.key) stolen.push(`${b.key} prefix "${p}" resolves to ${hit?.key ?? "nothing"}`)
      }
    }
    expect(stolen).toEqual([])
  })

  it("returns null for an unfamiliar shape", () => {
    expect(detectBrandFromValue("just-some-opaque-secret")).toBeNull()
  })
})

// ── The generic fallback ─────────────────────────────────────────────
//
// GENERIC_BRAND is what you get when nothing matched. The user must never be
// able to reach it as though it were a brand: not in the grid, not through
// search, not through detection. It is the absence of a brand.
describe("GENERIC_BRAND", () => {
  it("is not a registry row", () => {
    expect(BRAND_REGISTRY.find((b) => b.key === GENERIC_BRAND.key)).toBeUndefined()
    expect(BRAND_REGISTRY).not.toContain(GENERIC_BRAND)
  })

  it("is unreachable through name detection", () => {
    // Every word the picker's search field could plausibly be fed against the
    // fallback's own identity, plus the whole keyword corpus.
    const probes = ["none", "generic", "generic secret", "no brand", "", "   ", GENERIC_BRAND.key, GENERIC_BRAND.label]
    for (const probe of probes) {
      expect(detectBrandFromName(probe), `"${probe}" reached the generic brand`).not.toBe(GENERIC_BRAND)
    }
    for (const b of BRAND_REGISTRY) {
      for (const k of b.keywords ?? []) expect(detectBrandFromName(k)).not.toBe(GENERIC_BRAND)
    }
  })

  it("is unreachable through value detection", () => {
    for (const value of ["NONE", "generic-secret-value", "x".repeat(40)]) {
      expect(detectBrandFromValue(value)).not.toBe(GENERIC_BRAND)
    }
  })

  // The picker filters BRAND_REGISTRY by label/key/keywords (brand-picker.tsx),
  // so "not a row" is the same statement as "not searchable" — this pins the
  // half of it the picker relies on.
  it("carries no keywords for a search to match", () => {
    expect((GENERIC_BRAND as BrandEntry).keywords).toBeUndefined()
    expect((GENERIC_BRAND as BrandEntry).prefixes).toBeUndefined()
  })
})

describe("getBrand", () => {
  it("returns the generic brand for an unknown, empty or missing key", () => {
    for (const key of ["SOME_NEW_THING", "", "NONE", null, undefined]) {
      expect(getBrand(key)).toBe(GENERIC_BRAND)
    }
  })

  it("returns the row itself for a known key", () => {
    expect(getBrand("GITHUB")).toBe(byKey("GITHUB"))
    expect(getBrand("MICROSOFT")).toBe(byKey("MICROSOFT"))
  })

  // The generic brand still has to render — it is the icon every unknown
  // provider in the list draws with.
  it("renders", () => {
    const { container } = render(createElement(GENERIC_BRAND.Icon, {}))
    expect(container.querySelector("svg")).not.toBeNull()
  })
})
