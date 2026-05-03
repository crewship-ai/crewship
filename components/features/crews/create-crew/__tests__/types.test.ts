import { describe, it, expect } from "vitest"
import {
  INITIAL_STATE,
  MEMORY_PRESETS,
  CPU_PRESETS,
  TTL_PRESETS,
} from "../types"

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

    it("starts in free network mode", () => {
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
