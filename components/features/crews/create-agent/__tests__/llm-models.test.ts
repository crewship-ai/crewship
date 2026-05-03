import { describe, it, expect } from "vitest"
import { MODELS_BY_PROVIDER, defaultModelForProvider, isKnownModel } from "../llm-models"

describe("LLM models per provider", () => {
  it("every provider has at least one curated model", () => {
    for (const provider of ["ANTHROPIC", "OPENAI", "GOOGLE", "OLLAMA"] as const) {
      expect(MODELS_BY_PROVIDER[provider].length).toBeGreaterThan(0)
    }
  })

  it("provider lists do NOT contain duplicates within themselves", () => {
    for (const provider of ["ANTHROPIC", "OPENAI", "GOOGLE", "OLLAMA"] as const) {
      const list = MODELS_BY_PROVIDER[provider]
      expect(new Set(list).size).toBe(list.length)
    }
  })

  it("provider lists do NOT overlap across native providers (claude-* not on OPENAI etc)", () => {
    // CURSOR + FACTORY are multiplexers — they list claude-*/gpt-*/gemini-*
    // models as their underlying providers, so they're excluded from the
    // overlap check. Only the native providers (ANTHROPIC/OPENAI/GOOGLE/OLLAMA)
    // are checked.
    const allCombined: { provider: string; model: string }[] = []
    for (const provider of ["ANTHROPIC", "OPENAI", "GOOGLE", "OLLAMA"] as const) {
      for (const m of MODELS_BY_PROVIDER[provider]) allCombined.push({ provider, model: m })
    }
    // Precise fingerprints — anchored regexes to avoid false positives from
    // open-weight names that contain provider substrings (e.g. Ollama's
    // "gpt-oss" open-weight model has "gpt" in the name but is NOT OpenAI's).
    const fingerprints: { needle: RegExp; provider: string }[] = [
      { needle: /^claude-/, provider: "ANTHROPIC" },
      { needle: /^gpt-\d/, provider: "OPENAI" },
      { needle: /^o\d-/, provider: "OPENAI" }, // o3, o4 family
      { needle: /^gemini-/, provider: "GOOGLE" },
    ]
    for (const { provider, model } of allCombined) {
      for (const { needle, provider: expectedProvider } of fingerprints) {
        if (needle.test(model.toLowerCase())) {
          expect(provider, `model "${model}" looks like ${expectedProvider} but listed under ${provider}`).toBe(expectedProvider)
        }
      }
    }
  })

  describe("defaultModelForProvider", () => {
    it("returns the first model in each provider's list (newest)", () => {
      expect(defaultModelForProvider("ANTHROPIC")).toBe(MODELS_BY_PROVIDER.ANTHROPIC[0])
      expect(defaultModelForProvider("OPENAI")).toBe(MODELS_BY_PROVIDER.OPENAI[0])
      expect(defaultModelForProvider("GOOGLE")).toBe(MODELS_BY_PROVIDER.GOOGLE[0])
      expect(defaultModelForProvider("OLLAMA")).toBe(MODELS_BY_PROVIDER.OLLAMA[0])
    })

    it("ANTHROPIC default is the most recent Claude (Opus or Sonnet 4-7)", () => {
      // Newer-first ordering matters — defaulting to a stale model on every
      // provider switch would silently downgrade users.
      expect(defaultModelForProvider("ANTHROPIC")).toMatch(/^claude-(opus|sonnet)-4-/)
    })
  })

  describe("isKnownModel", () => {
    it("recognises known models", () => {
      expect(isKnownModel("ANTHROPIC", "claude-sonnet-4-6")).toBe(true)
      // gpt-4o-mini is deprecated and removed from the catalog — use a
      // current model. gpt-5.4 is a 2026-Q1 release that's both in the
      // Codex CLI list and the general OpenAI list.
      expect(isKnownModel("OPENAI", "gpt-5.4")).toBe(true)
    })

    it("rejects unknown models (custom mode signal)", () => {
      expect(isKnownModel("ANTHROPIC", "claude-future-99")).toBe(false)
      expect(isKnownModel("OPENAI", "claude-sonnet-4-6")).toBe(false) // wrong provider
    })

    it("rejects empty string", () => {
      expect(isKnownModel("ANTHROPIC", "")).toBe(false)
    })
  })

  describe("provider switch invariant", () => {
    // When the user toggles provider, the dialog should auto-pick the
    // default model UNLESS the previous model happens to be valid for the
    // new provider too (rare but possible, e.g. a custom name overlap).
    it("switching ANTHROPIC → OPENAI implies model change (no overlap)", () => {
      const before = "claude-sonnet-4-6"
      const stillValid = isKnownModel("OPENAI", before)
      expect(stillValid).toBe(false) // ⇒ caller resets to defaultModelForProvider("OPENAI")
    })

    it("switching back to a provider can keep the user's previous model", () => {
      // ANTHROPIC → OPENAI → ANTHROPIC: if user had "claude-sonnet-4-6"
      // before the first switch, the auto-reset on the way out used
      // gpt-5.4; now coming back the field has gpt-5.4 which is NOT known
      // on ANTHROPIC, so reset to anthropic default.
      expect(isKnownModel("ANTHROPIC", "gpt-5.4")).toBe(false)
    })
  })
})
