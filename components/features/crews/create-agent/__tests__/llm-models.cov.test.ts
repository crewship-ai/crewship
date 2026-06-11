import { describe, it, expect, vi } from "vitest"

// Coverage companion for llm-models.test.ts — drives the last-resort
// fallbacks in defaultModelForProvider that are unreachable with the real
// CLI_ADAPTERS catalog (every real provider has a non-empty curated list).
// The catalog is mocked so a provider can end up with an EMPTY
// MODELS_BY_PROVIDER entry while still owning an adapter defaultModel.

vi.mock("@/lib/cli-adapters", () => ({
  CLI_ADAPTERS: {
    // CURSOR's only model value is namespaced, so modelsForProvider("CURSOR")
    // collects nothing (no prefix recovery exists for CURSOR) — yet the
    // adapter still declares a defaultModel. This is exactly the "future
    // provider added to the union without a populated list" scenario the
    // fallback exists for.
    CURSOR_CLI: {
      provider: "CURSOR",
      defaultModel: "composer",
      models: [{ value: "router/cursor/composer", label: "Composer (routed)" }],
    },
    // ANTHROPIC keeps a normal, populated catalog so the primary path
    // stays observable under the mock too.
    CLAUDE_CODE: {
      provider: "ANTHROPIC",
      defaultModel: "claude-sonnet-4-6",
      models: [
        { value: "claude-sonnet-4-6", label: "Sonnet" },
        { value: "claude-haiku-4-5-20251001", label: "Haiku" },
      ],
    },
  },
}))

import { MODELS_BY_PROVIDER, defaultModelForProvider, isKnownModel } from "../llm-models"

describe("defaultModelForProvider — fallback ladder (mocked catalog)", () => {
  it("step 1: returns the adapter default when it is in the curated list", () => {
    expect(MODELS_BY_PROVIDER.ANTHROPIC).toContain("claude-sonnet-4-6")
    expect(defaultModelForProvider("ANTHROPIC")).toBe("claude-sonnet-4-6")
  })

  it("step 3: falls back to the adapter default even when the curated list is empty", () => {
    // The mocked CURSOR adapter only lists a nested router path, which the
    // projection rejects — so MODELS_BY_PROVIDER.CURSOR is empty…
    expect(MODELS_BY_PROVIDER.CURSOR).toEqual([])
    // …but the function must still return a real string (the adapter's
    // defaultModel) instead of crashing the picker with undefined.
    expect(defaultModelForProvider("CURSOR")).toBe("composer")
  })

  it("returns '' when no adapter claims the provider and the list is empty", () => {
    // GOOGLE has no adapter in the mocked catalog and no curated models.
    expect(MODELS_BY_PROVIDER.GOOGLE).toEqual([])
    expect(defaultModelForProvider("GOOGLE")).toBe("")
  })

  it("an out-of-list fallback default reads as 'custom mode' to the picker", () => {
    // Contract check: when step 3 fires, isKnownModel is false — the
    // picker renders the free-text input pre-filled with the fallback.
    expect(isKnownModel("CURSOR", defaultModelForProvider("CURSOR"))).toBe(false)
  })
})
