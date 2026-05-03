import { describe, it, expect } from "vitest"
import {
  CLI_ADAPTERS,
  CLI_ADAPTER_KEYS,
  getAdapterConfig,
  getModelsForAdapter,
  getProviderLabel,
  getModelLabel,
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

  it("OPENCODE uses provider/model namespaced strings", () => {
    // OpenCode requires "provider/model" form (anthropic/claude-..., openai/gpt-...)
    // — bare model IDs are rejected by the CLI. Pre-fix our list mixed bare
    // and namespaced; this test pins the canonical convention.
    const values = CLI_ADAPTERS.OPENCODE.models.map((m) => m.value)
    expect(values).toContain("anthropic/claude-sonnet-4-6")
    expect(values).toContain("openai/gpt-5.5")
    expect(values).toContain("openai/o3")
    expect(values).toContain("google/gemini-2.5-pro")
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

describe("getModelLabel", () => {
  it("translates Anthropic API IDs to friendly labels", () => {
    expect(getModelLabel("claude-sonnet-4-6")).toBe("Claude Sonnet 4.6")
    expect(getModelLabel("claude-opus-4-7")).toBe("Claude Opus 4.7")
    expect(getModelLabel("claude-haiku-4-5-20251001")).toBe("Claude Haiku 4.5")
  })

  it("translates OpenAI API IDs to friendly labels", () => {
    expect(getModelLabel("gpt-5.5")).toBe("GPT-5.5")
    expect(getModelLabel("gpt-5.4")).toBe("GPT-5.4")
    expect(getModelLabel("gpt-5.3-codex")).toBe("GPT-5.3 Codex")
  })

  it("translates Google API IDs to friendly labels", () => {
    expect(getModelLabel("gemini-2.5-pro")).toBe("Gemini 2.5 Pro")
    expect(getModelLabel("gemini-3.1-pro-preview")).toBe("Gemini 3.1 Pro (Preview)")
  })

  it("returns OpenCode 'provider/model' label", () => {
    expect(getModelLabel("anthropic/claude-sonnet-4-6")).toBe("Anthropic / Claude Sonnet 4.6")
    expect(getModelLabel("openai/gpt-5.5")).toBe("OpenAI / GPT-5.5")
  })

  it("returns input unchanged for unknown / custom models", () => {
    expect(getModelLabel("custom-fine-tune-v3")).toBe("custom-fine-tune-v3")
    expect(getModelLabel("not-a-real-model")).toBe("not-a-real-model")
  })

  it("returns empty string for empty input", () => {
    expect(getModelLabel("")).toBe("")
  })
})

describe("model catalog completeness", () => {
  it("each adapter's defaultModel is present in its models[] list", () => {
    for (const [name, cfg] of Object.entries(CLI_ADAPTERS)) {
      const found = cfg.models.some((m) => m.value === cfg.defaultModel)
      expect(found, `${name}: defaultModel "${cfg.defaultModel}" not in models list`).toBe(true)
    }
  })

  it("each model has a non-empty label", () => {
    for (const [name, cfg] of Object.entries(CLI_ADAPTERS)) {
      for (const m of cfg.models) {
        expect(m.label, `${name}: model "${m.value}" has empty label`).toBeTruthy()
      }
    }
  })

  it("no duplicate model values within an adapter", () => {
    for (const [name, cfg] of Object.entries(CLI_ADAPTERS)) {
      const values = cfg.models.map((m) => m.value)
      expect(new Set(values).size, `${name}: duplicate model values`).toBe(values.length)
    }
  })

  it("CODEX_CLI lists ONLY GPT-5.x family (Codex CLI rejects o-series)", () => {
    const values = CLI_ADAPTERS.CODEX_CLI.models.map((m) => m.value)
    for (const v of values) {
      expect(v.startsWith("gpt-"), `CODEX_CLI model "${v}" must be gpt-* family`).toBe(true)
    }
    expect(values).not.toContain("o3")
    expect(values).not.toContain("o4-mini")
  })

  it("CURSOR_CLI default 'composer' is Cursor's in-house model", () => {
    expect(CLI_ADAPTERS.CURSOR_CLI.defaultModel).toBe("composer")
  })

  it("FACTORY_DROID accepts bare provider model IDs (no prefix)", () => {
    const values = CLI_ADAPTERS.FACTORY_DROID.models.map((m) => m.value)
    // Droid does NOT use anthropic/ or openai/ prefix per docs.factory.ai
    for (const v of values) {
      expect(v.includes("/"), `FACTORY_DROID model "${v}" must NOT have provider prefix`).toBe(false)
    }
  })

  it("OPENCODE models all use provider/model namespaced form", () => {
    const values = CLI_ADAPTERS.OPENCODE.models.map((m) => m.value)
    for (const v of values) {
      expect(v.includes("/"), `OPENCODE model "${v}" MUST be namespaced (e.g. anthropic/claude-...)`).toBe(true)
    }
  })

  it("ANTHROPIC list excludes deprecated Claude 3.5 / -20250514 models", () => {
    const values = CLI_ADAPTERS.CLAUDE_CODE.models.map((m) => m.value)
    expect(values).not.toContain("claude-3-5-sonnet-20241022")
    expect(values).not.toContain("claude-3-5-haiku-20241022")
    expect(values).not.toContain("claude-sonnet-4-20250514") // retiring 2026-06-15
    expect(values).not.toContain("claude-opus-4-20250514")
  })
})
