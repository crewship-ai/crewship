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
      // o-series reasoning models: o3, o3-pro, o4-mini etc. The boundary
      // is hyphen OR end-of-string so bare "o3" is not silently exempted
      // — without the (?:-|$) the regex required a hyphen and failed to
      // catch a future "o3" entry mis-parented under e.g. ANTHROPIC.
      { needle: /^o\d(?:-|$)/, provider: "OPENAI" },
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
    it("uses each adapter's explicit defaultModel when one matches the provider", () => {
      // Pinned to the adapter's defaultModel field (cli-adapters.ts) rather
      // than positional MODELS_BY_PROVIDER[0], so reordering models[]
      // can't silently shift the UI default. The literal values mirror
      // CLI_ADAPTERS[*].defaultModel exactly — update both files together.
      expect(defaultModelForProvider("ANTHROPIC")).toBe("claude-sonnet-4-6")
      expect(defaultModelForProvider("OPENAI")).toBe("gpt-5.5")
      expect(defaultModelForProvider("GOOGLE")).toBe("gemini-2.5-pro")
      expect(defaultModelForProvider("CURSOR")).toBe("composer")
      expect(defaultModelForProvider("FACTORY")).toBe("claude-sonnet-4-6")
    })

    it("falls back to first MODELS_BY_PROVIDER entry for providers without a matching adapter", () => {
      // OLLAMA has no dedicated CLI adapter (served via OpenCode prefix),
      // so the array-order fallback kicks in.
      expect(defaultModelForProvider("OLLAMA")).toBe(MODELS_BY_PROVIDER.OLLAMA[0])
    })

    it("ANTHROPIC default is a current Claude (Opus or Sonnet, not legacy 4-1)", () => {
      // Defaulting to a stale model on every provider switch would silently
      // downgrade users. Pin to the 4-x family.
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
