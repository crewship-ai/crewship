import { describe, it, expect } from "vitest"
import {
  INITIAL_STATE,
  MEMORY_PRESETS,
  CPU_PRESETS,
  TTL_PRESETS,
  SLUG_MAX,
  normalizeSlug,
  slugFromName,
} from "../types"

/**
 * The server's rule, transcribed rather than approximated:
 * internal/api/helpers.go → `validSlugRe = ^[a-z0-9][a-z0-9_-]*$`, with the
 * 2–50 length check in crews_create.go. Anything normalizeSlug emits has to
 * satisfy it, because the field no longer offers the user a way to type
 * something that does not.
 */
const VALID_SLUG = /^[a-z0-9][a-z0-9_-]*$/

describe("create-crew/types — slug normalisation", () => {
  it("makes the 400 the server used to send unreachable", () => {
    // The literal string that reproduced it: POST /api/v1/crews with
    // slug "Engineering Team!" answers 400 "slug must contain only
    // lowercase letters, numbers, hyphens, and underscores".
    expect(normalizeSlug("Engineering Team!")).toBe("engineering-team-")
    expect(normalizeSlug("Engineering Team!")).toMatch(VALID_SLUG)
  })

  it("lowercases, and folds every run of rejects into one hyphen", () => {
    expect(normalizeSlug("Sales & Ops / Q1")).toBe("sales-ops-q1")
    expect(normalizeSlug("Foo   ---   Bar")).toBe("foo-bar")
  })

  it("keeps the underscore the server accepts", () => {
    // The old inline deriver used [^a-z0-9]+ and threw underscores away
    // even though validSlugRe allows them.
    expect(normalizeSlug("crew_two")).toBe("crew_two")
  })

  it("drops a leading separator, which is the one thing validSlugRe rejects", () => {
    expect(normalizeSlug("-lead")).toBe("lead")
    expect(normalizeSlug("__lead")).toBe("lead")
    expect(normalizeSlug("!!!lead")).toBe("lead")
  })

  it("leaves a trailing hyphen so typing a space does not eat it", () => {
    // Typing "my crew" one character at a time: the field's own output is
    // the next keystroke's input, so trimming here would turn "my " back
    // into "my" and land the next character as "myc".
    expect(normalizeSlug("my ")).toBe("my-")
    expect(normalizeSlug(normalizeSlug("my ") + "c")).toBe("my-c")
  })

  it("caps at the length the server accepts", () => {
    expect(normalizeSlug("a".repeat(200))).toHaveLength(SLUG_MAX)
    expect(normalizeSlug("a".repeat(200))).toMatch(VALID_SLUG)
  })

  it("emits a server-valid slug for anything typeable", () => {
    for (const raw of [
      "Engineering", "ENGINEERING", "Ops 2024", "účetnictví", "  spaced  ",
      "a", "9lives", "-", "___", "!@#$%", "Tab\tSep", "emoji 🚀 crew",
    ]) {
      const out = normalizeSlug(raw)
      // "-", "___" and "!@#$%" normalise to empty — that is the empty field,
      // which the step's required-check catches, not an invalid slug.
      if (out !== "") expect(out, `input: ${JSON.stringify(raw)}`).toMatch(VALID_SLUG)
    }
  })

  describe("slugFromName — the auto-fill while Slug is untouched", () => {
    it("trims the trailing separator, unlike the field itself", () => {
      // Safe here because the edited field is Name: each keystroke
      // re-derives from the whole name, so no separator is ever lost.
      expect(slugFromName("Engineering Team!")).toBe("engineering-team")
      expect(slugFromName("my ")).toBe("my")
    })

    it("agrees with normalizeSlug on everything else", () => {
      for (const name of ["Customer Support", "Sales & Ops", "crew_two", "9lives"]) {
        expect(slugFromName(name)).toBe(normalizeSlug(name).replace(/[-_]+$/, ""))
      }
    })
  })
})

describe("create-crew/types", () => {
  describe("INITIAL_STATE", () => {
    it("starts with empty identity", () => {
      expect(INITIAL_STATE.name).toBe("")
      expect(INITIAL_STATE.slug).toBe("")
      expect(INITIAL_STATE.slugTouched).toBe(false)
      expect(INITIAL_STATE.description).toBe("")
    })

    it("defaults icon and color so the preview tile is never blank", () => {
      expect(INITIAL_STATE.icon).toBeTruthy()
      expect(INITIAL_STATE.color).toBeTruthy()
    })

    it("starts in browse mode with no template picked", () => {
      expect(INITIAL_STATE.mode).toBe("browse")
      expect(INITIAL_STATE.pickedTemplateSlug).toBeNull()
      expect(INITIAL_STATE.pickedTemplateMeta).toBeNull()
    })

    it("uses sane container defaults matching backend defaults", () => {
      // Backend defaults in crews_create.go: memoryMB=4096, cpus=2.0, no TTL.
      expect(INITIAL_STATE.memoryMB).toBe(4096)
      expect(INITIAL_STATE.cpus).toBe(2)
      expect(INITIAL_STATE.ttlHours).toBeNull()
    })

    // Not the backend's fail-safe, on purpose. The server still writes
    // `restricted` for any caller that says nothing (crew_defaults.go); the
    // wizard is not that caller — it asks, and what it proposes is the
    // product decision: open now, throttled later, with the allowlist built
    // and one switch away. An allowlist that is still maturing fails as a
    // silent timeout deep inside a run.
    it("proposes open egress, with nothing listed", () => {
      expect(INITIAL_STATE.networkMode).toBe("free")
      expect(INITIAL_STATE.allowedDomains).toEqual([])
    })
  })

  describe("MEMORY_PRESETS", () => {
    it("includes the default 4 GB so 4096 maps to a chip exactly", () => {
      expect(MEMORY_PRESETS.some((p) => p.value === 4096)).toBe(true)
    })

    it("values are positive and ascending", () => {
      let prev = 0
      for (const p of MEMORY_PRESETS) {
        expect(p.value).toBeGreaterThan(prev)
        prev = p.value
      }
    })

    it("labels stay concise (≤ 8 chars) so chips don't wrap", () => {
      for (const p of MEMORY_PRESETS) {
        expect(p.label.length).toBeLessThanOrEqual(8)
      }
    })
  })

  describe("CPU_PRESETS", () => {
    it("includes default 2 CPUs and a sub-1 fractional value", () => {
      expect(CPU_PRESETS.some((p) => p.value === 2)).toBe(true)
      expect(CPU_PRESETS.some((p) => p.value < 1)).toBe(true)
    })
  })

  describe("TTL_PRESETS", () => {
    it("includes Never (null) as first option to match `INITIAL_STATE.ttlHours = null`", () => {
      expect(TTL_PRESETS[0].value).toBeNull()
    })

    it("only `Never` is null; numeric options are positive hours", () => {
      const numeric = TTL_PRESETS.filter((p) => p.value !== null)
      expect(numeric.length).toBeGreaterThan(0)
      for (const p of numeric) {
        expect(p.value).toBeGreaterThan(0)
      }
    })
  })
})
