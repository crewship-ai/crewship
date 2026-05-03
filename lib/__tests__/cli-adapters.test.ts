import { describe, it, expect } from "vitest"
import {
  CLI_ADAPTERS,
  CLI_ADAPTER_KEYS,
  getAdapterConfig,
  getModelsForAdapter,
  getProviderLabel,
} from "@/lib/cli-adapters"

describe("CLI_ADAPTERS registry", () => {
  it("exports all six supported adapters", () => {
    // Spread before sort — .sort() mutates in place, and CLI_ADAPTER_KEYS
    // is a shared module-level export. Mutating it would leak ordering
    // changes into other tests that import the same array.
    expect([...CLI_ADAPTER_KEYS].sort()).toEqual([
      "CLAUDE_CODE",
      "CODEX_CLI",
      "CURSOR_CLI",
      "FACTORY_DROID",
      "GEMINI_CLI",
      "OPENCODE",
    ])
  })

  it("every adapter has the required config fields", () => {
    for (const key of CLI_ADAPTER_KEYS) {
      const cfg = CLI_ADAPTERS[key]
      expect(cfg.label).toBeTruthy()
      expect(cfg.provider).toBeTruthy()
      expect(cfg.envVar).toBeTruthy()
      expect(cfg.models.length).toBeGreaterThan(0)
      expect(cfg.defaultModel).toBeTruthy()
      expect(cfg.description).toBeTruthy()
    }
  })

  it("default model is in the adapter's models list", () => {
    for (const key of CLI_ADAPTER_KEYS) {
      const cfg = CLI_ADAPTERS[key]
      const found = cfg.models.some((m) => m.value === cfg.defaultModel)
      expect(found, `${key}: defaultModel ${cfg.defaultModel} not in models list`).toBe(true)
    }
  })

  it("Anthropic-tier adapters use ANTHROPIC_API_KEY env var", () => {
    expect(CLI_ADAPTERS.CLAUDE_CODE.envVar).toBe("ANTHROPIC_API_KEY")
    expect(CLI_ADAPTERS.OPENCODE.envVar).toBe("ANTHROPIC_API_KEY")
  })

  it("CODEX_CLI uses OPENAI provider + key", () => {
    expect(CLI_ADAPTERS.CODEX_CLI.provider).toBe("OPENAI")
    expect(CLI_ADAPTERS.CODEX_CLI.envVar).toBe("OPENAI_API_KEY")
  })

  it("GEMINI_CLI uses GOOGLE provider + key", () => {
    expect(CLI_ADAPTERS.GEMINI_CLI.provider).toBe("GOOGLE")
    expect(CLI_ADAPTERS.GEMINI_CLI.envVar).toBe("GOOGLE_API_KEY")
  })

  it("CURSOR_CLI uses CURSOR provider + key", () => {
    expect(CLI_ADAPTERS.CURSOR_CLI.provider).toBe("CURSOR")
    expect(CLI_ADAPTERS.CURSOR_CLI.envVar).toBe("CURSOR_API_KEY")
  })

  it("FACTORY_DROID uses FACTORY provider + key", () => {
    expect(CLI_ADAPTERS.FACTORY_DROID.provider).toBe("FACTORY")
    expect(CLI_ADAPTERS.FACTORY_DROID.envVar).toBe("FACTORY_API_KEY")
  })

  it("OPENCODE bundles both Anthropic + OpenAI model lists", () => {
    const values = CLI_ADAPTERS.OPENCODE.models.map((m) => m.value)
    expect(values).toContain("claude-sonnet-4-6")
    expect(values).toContain("o3")
    expect(values).toContain("gpt-4o")
  })

  it("frontier Anthropic models are present in CLAUDE_CODE", () => {
    const values = CLI_ADAPTERS.CLAUDE_CODE.models.map((m) => m.value)
    expect(values).toContain("claude-opus-4-7")
    expect(values).toContain("claude-sonnet-4-6")
    expect(values).toContain("claude-haiku-4-5-20251001")
  })
})

describe("getAdapterConfig", () => {
  it("returns the config for a known key", () => {
    expect(getAdapterConfig("CLAUDE_CODE")?.label).toBe("Claude Code")
  })

  it("returns undefined for unknown keys", () => {
    expect(getAdapterConfig("WHO_KNOWS")).toBeUndefined()
    expect(getAdapterConfig("")).toBeUndefined()
  })
})

describe("getModelsForAdapter", () => {
  it("returns the adapter's model list", () => {
    const got = getModelsForAdapter("GEMINI_CLI")
    expect(got.length).toBeGreaterThan(0)
    expect(got[0].value).toContain("gemini")
  })

  it("returns empty array for unknown adapter (safe default)", () => {
    expect(getModelsForAdapter("UNKNOWN")).toEqual([])
  })
})

describe("getProviderLabel", () => {
  it("maps known providers to display labels", () => {
    expect(getProviderLabel("ANTHROPIC")).toBe("Anthropic")
    expect(getProviderLabel("OPENAI")).toBe("OpenAI")
    expect(getProviderLabel("GOOGLE")).toBe("Google")
    expect(getProviderLabel("CURSOR")).toBe("Cursor")
    expect(getProviderLabel("FACTORY")).toBe("Factory")
    expect(getProviderLabel("NONE")).toBe("--")
  })

  it("falls back to the input string for unknown providers", () => {
    expect(getProviderLabel("XAI")).toBe("XAI")
    expect(getProviderLabel("DeepSeek")).toBe("DeepSeek")
  })

  it("does NOT lowercase or modify unknown labels", () => {
    expect(getProviderLabel("custom-provider")).toBe("custom-provider")
  })
})
