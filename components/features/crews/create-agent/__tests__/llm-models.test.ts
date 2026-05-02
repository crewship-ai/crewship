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

  it("provider lists do NOT overlap across providers (claude-* not on OPENAI etc)", () => {
    const allCombined: { provider: string; model: string }[] = []
    for (const provider of ["ANTHROPIC", "OPENAI", "GOOGLE", "OLLAMA"] as const) {
      for (const m of MODELS_BY_PROVIDER[provider]) allCombined.push({ provider, model: m })
    }
    // Models that look like they belong to a specific provider should appear
    // only on that provider's list. Catches a copy-paste mistake where an
    // ANTHROPIC model is accidentally added to OPENAI.
    const fingerprints: Record<string, string> = {
      claude: "ANTHROPIC",
      gpt: "OPENAI",
      gemini: "GOOGLE",
    }
    for (const { provider, model } of allCombined) {
      for (const [needle, expectedProvider] of Object.entries(fingerprints)) {
        if (model.toLowerCase().includes(needle)) {
          expect(provider).toBe(expectedProvider)
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
      expect(isKnownModel("OPENAI", "gpt-4o-mini")).toBe(true)
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
      // gpt-4o; now coming back the field has gpt-4o which is NOT known
      // on ANTHROPIC, so reset to anthropic default.
      expect(isKnownModel("ANTHROPIC", "gpt-4o")).toBe(false)
    })
  })
})
