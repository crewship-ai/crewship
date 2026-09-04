import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

import {
  MODEL_CATALOG,
  adapterDefaultModel,
  adapterModels,
  catalogModelLabel,
  providerDefaultModel,
  providerModels,
} from "@/lib/model-catalog"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS, getModelLabel, getModelsForAdapter } from "@/lib/cli-adapters"

// config/models.json is read by the Go binary and by this bundle. These pin
// the file's contract from the web side (internal/llm/models_catalog_test.go
// pins it from the Go side): every adapter resolves, every default is
// offered, and no picker can carry a model id of its own.

describe("config/models.json", () => {
  it("is the same file the Go side embeds", () => {
    const onDisk = JSON.parse(readFileSync(join(process.cwd(), "config", "models.json"), "utf8"))
    expect(MODEL_CATALOG.version).toBe(onDisk.version)
    expect(Object.keys(MODEL_CATALOG.adapters)).toEqual(Object.keys(onDisk.adapters))
  })

  it("has a row for every adapter in the registry, and no adapter the registry lacks", () => {
    expect(Object.keys(MODEL_CATALOG.adapters).sort()).toEqual([...CLI_ADAPTER_KEYS].sort())
  })

  it("resolves every string reference to a provider row", () => {
    for (const [key, adapter] of Object.entries(MODEL_CATALOG.adapters)) {
      const refs = adapter.models.filter((m): m is string => typeof m === "string")
      const ids = new Set(providerModels(adapter.provider).map((m) => m.id))
      for (const ref of refs) expect(ids.has(ref), `${key} refers to unknown ${adapter.provider} model ${ref}`).toBe(true)
      expect(adapterModels(key)).toHaveLength(adapter.models.length)
    }
  })

  it("offers each adapter's default in its own list", () => {
    for (const key of CLI_ADAPTER_KEYS) {
      const def = adapterDefaultModel(key)
      expect(def, `${key} has no default`).toBeTruthy()
      expect(getModelsForAdapter(key).map((m) => m.value)).toContain(def)
      expect(CLI_ADAPTERS[key].defaultModel).toBe(def)
    }
  })

  it("has unique ids within every provider and adapter", () => {
    for (const [name, p] of Object.entries(MODEL_CATALOG.providers)) {
      const ids = p.models.map((m) => m.id)
      expect(new Set(ids).size, `provider ${name}`).toBe(ids.length)
    }
    for (const key of CLI_ADAPTER_KEYS) {
      const ids = adapterModels(key).map((m) => m.id)
      expect(new Set(ids).size, `adapter ${key}`).toBe(ids.length)
    }
  })

  it("gives the Guide a cheap, a default and a top model for every served provider", () => {
    for (const provider of ["anthropic", "openai", "google"]) {
      const roles = providerModels(provider).map((m) => m.role).filter(Boolean)
      expect(roles, provider).toEqual(expect.arrayContaining(["cheap", "default", "top"]))
      expect(providerModels(provider).find((m) => m.role === "default")?.id).toBe(providerDefaultModel(provider))
    }
  })
})

describe("the Claude Code model picker", () => {
  it("offers the whole curated Anthropic list, most capable first, Sonnet 5 recommended", () => {
    const offered = getModelsForAdapter("CLAUDE_CODE").map((m) => m.value)
    expect(offered).toEqual(providerModels("anthropic").map((m) => m.id))
    expect(offered.length).toBeGreaterThanOrEqual(5)
    expect(CLI_ADAPTERS.CLAUDE_CODE.defaultModel).toBe("claude-sonnet-5")
  })

  it("carries bare aliases only — never a date-suffixed id", () => {
    for (const id of getModelsForAdapter("CLAUDE_CODE").map((m) => m.value)) {
      expect(id).not.toMatch(/\d{8}$/)
    }
  })
})

describe("getModelLabel", () => {
  it("names a superseded alias a workspace may still store", () => {
    expect(getModelLabel("claude-opus-4-5-20251101")).toBe("Claude Opus 4.5")
    expect(catalogModelLabel("claude-haiku-4-5-20251001")).toBe("Claude Haiku 4.5")
  })

  it("prefers the provider's own name over an adapter's suffixed one", () => {
    expect(getModelLabel("claude-sonnet-4-6")).toBe("Claude Sonnet 4.6")
  })

  it("returns an unknown id unchanged", () => {
    expect(getModelLabel("my-custom-model")).toBe("my-custom-model")
  })
})
