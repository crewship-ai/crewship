import { describe, it, expect } from "vitest"
import { CLI_ADAPTERS, getModelsForAdapter } from "@/lib/cli-adapters"

// The model picker in onboarding step 3 is the first model decision a new
// workspace makes. Two properties matter and neither is enforced by types:
// the list offers only models that are current, and every value is a bare
// alias. Both went wrong at once — the list carried six superseded aliases,
// it was missing Claude Opus 5 entirely, and the Haiku entry used the dated
// form the model docs explicitly warn against.

const ANTHROPIC_ADAPTERS = Object.keys(CLI_ADAPTERS).filter(
  (key) => CLI_ADAPTERS[key].provider === "ANTHROPIC"
)

/** Anthropic models as offered by the picker, across every Anthropic adapter. */
function offered(): string[] {
  return [...new Set(ANTHROPIC_ADAPTERS.flatMap((k) => getModelsForAdapter(k).map((m) => m.value)))]
}

describe("the Anthropic model picker", () => {
  it("offers Claude Opus 5", () => {
    // The current Opus, and the one most people should start on. It was
    // absent while three superseded Opus versions were selectable.
    expect(offered()).toContain("claude-opus-5")
  })

  it("offers only current-generation models", () => {
    // Superseded aliases still answer at the API and can be set via the CLI
    // or the API — they are just not offered as a starting point.
    const superseded = [
      "claude-opus-4-7",
      "claude-opus-4-6",
      "claude-opus-4-5",
      "claude-opus-4-1",
      "claude-sonnet-4-6",
      "claude-sonnet-4-5",
    ]
    for (const stale of superseded) {
      expect(offered(), `${stale} is superseded and should not be offered`).not.toContain(stale)
    }
  })

  it("has no claude-3 era models anywhere", () => {
    for (const value of offered()) {
      expect(value).not.toMatch(/claude-3/)
    }
  })

  it("uses bare aliases, never a dated snapshot", () => {
    // "Use only the exact model ID strings from the table — they are complete
    // as-is; never append date suffixes." The Haiku entry was
    // claude-haiku-4-5-20251001, which is exactly that shape.
    for (const value of offered()) {
      expect(value, `${value} carries a date suffix`).not.toMatch(/-20\d{6}$/)
    }
  })

  it("defaults every Anthropic adapter to a model it actually offers", () => {
    // A default outside its own list is selectable only by accident, and
    // renders as a blank picker.
    for (const key of ANTHROPIC_ADAPTERS) {
      const cfg = CLI_ADAPTERS[key]
      const values = getModelsForAdapter(key).map((m) => m.value)
      expect(values, `${key} default ${cfg.defaultModel} is not in its own list`).toContain(
        cfg.defaultModel
      )
    }
  })

  it("keeps a frontier model available on every Anthropic adapter", () => {
    for (const key of ANTHROPIC_ADAPTERS) {
      const models = getModelsForAdapter(key)
      expect(models.some((m) => m.category === "frontier"), `${key} offers no frontier model`).toBe(
        true
      )
    }
  })
})
