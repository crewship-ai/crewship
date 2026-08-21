import { describe, it, expect } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { CLI_ADAPTERS, getModelLabel, getModelsForAdapter } from "@/lib/cli-adapters"

// The onboarding picker decides what a brand-new workspace starts on. Two
// things went wrong with it at once, in opposite directions:
//
//   - It was populated from Anthropic's published catalogue, so it offered
//     five models of which one had actually been run against Claude Code.
//     Publishing a model is not the same as having verified the adapter.
//   - It was a third independent copy of "which models exist", alongside
//     internal/llm/models_curated.go (which the backend serves at
//     GET /api/v1/models) and the CLI's own adapter defaults. Three lists,
//     three different contents.
//
// So: the Go curated list is the source of truth, this file's offering is a
// deliberately narrower subset of it, and the subset relation is enforced
// here rather than left to memory.

const GO_CURATED = join(process.cwd(), "internal", "llm", "models_curated.go")

/** The ids curatedModels["anthropic"] declares in the Go source. */
function goCuratedAnthropic(): string[] {
  const src = readFileSync(GO_CURATED, "utf8")
  const start = src.indexOf('"anthropic": {')
  expect(start, "curatedModels[\"anthropic\"] not found — did the Go file move?").toBeGreaterThan(-1)
  const block = src.slice(start, src.indexOf("\n\t},", start))
  const ids = [...block.matchAll(/\{ID:\s*"([^"]+)"/g)].map((m) => m[1])
  expect(ids.length, "parsed no ids out of the Go block").toBeGreaterThan(0)
  return ids
}

const offered = () => getModelsForAdapter("CLAUDE_CODE").map((m) => m.value)

describe("the Claude Code model picker", () => {
  it("offers only models the Go curated list carries", () => {
    // The backend serves that list; offering something outside it means the
    // wizard can hand a workspace an id no other surface knows about.
    const curated = goCuratedAnthropic()
    for (const value of offered()) {
      expect(curated, `${value} is not in internal/llm/models_curated.go`).toContain(value)
    }
  })

  it("offers only what has been verified with the adapter", () => {
    // Narrower than curated on purpose. Widening this is a deliberate act:
    // run the adapter against the model first. If that list ever grows, this
    // assertion is the place to record why.
    expect(offered()).toEqual(["claude-sonnet-5"])
  })

  it("defaults to a model it actually offers", () => {
    // A default outside its own list is selectable only by accident, and
    // renders as a blank picker.
    expect(offered()).toContain(CLI_ADAPTERS.CLAUDE_CODE.defaultModel)
  })

  it("uses bare aliases, never a dated snapshot", () => {
    // "Use only the exact model ID strings from the table — they are
    // complete as-is; never append date suffixes." The Haiku entry used to
    // be claude-haiku-4-5-20251001, which is exactly that shape.
    for (const value of offered()) {
      expect(value, `${value} carries a date suffix`).not.toMatch(/-20\d{6}$/)
    }
  })

  it("has no claude-3 era models", () => {
    for (const value of offered()) {
      expect(value).not.toMatch(/claude-3/)
    }
  })
})

describe("models the picker no longer offers", () => {
  it("keep their display names", () => {
    // Narrowing the picker changes what a NEW workspace may choose, not how
    // an EXISTING one reads. claude-sonnet-4-6 is the sharp case: it is also
    // registered under Cursor, so without the superseded table getModelLabel
    // silently relabelled live workspaces to "Claude Sonnet 4.6 (Cursor)".
    expect(getModelLabel("claude-sonnet-4-6")).toBe("Claude Sonnet 4.6")
    expect(getModelLabel("claude-opus-4-7")).toBe("Claude Opus 4.7")
    expect(getModelLabel("claude-haiku-4-5-20251001")).toBe("Claude Haiku 4.5")
    expect(getModelLabel("claude-opus-4-5-20251101")).toBe("Claude Opus 4.5")
  })

  it("covers every id the Go curated list carries but the picker drops", () => {
    // Anything curated-but-not-offered can still be the stored model of a
    // workspace configured through the CLI or the API, so all of it needs a
    // name — including the models this picker deliberately omits.
    const dropped = goCuratedAnthropic().filter((id) => !offered().includes(id))
    expect(dropped.length, "expected the picker to be narrower than curated").toBeGreaterThan(0)
    for (const id of dropped) {
      expect(getModelLabel(id), `${id} renders as its raw id`).not.toBe(id)
    }
  })
})
