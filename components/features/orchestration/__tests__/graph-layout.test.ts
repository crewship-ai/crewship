import { describe, it, expect } from "vitest"
import {
  parseDependsOn,
  pickEdgeColor,
  computeTopologicalLevels,
  groupByLevel,
  computeCrewDimensions,
  computeTaskPosition,
  selectActiveMissions,
  LAYOUT,
  edgeColorPalette,
} from "../graph-layout"

// --- parseDependsOn ---

describe("parseDependsOn", () => {
  it("returns empty array for null", () => {
    expect(parseDependsOn(null)).toEqual([])
  })

  it("returns empty array for undefined", () => {
    expect(parseDependsOn(undefined)).toEqual([])
  })

  it("returns empty array for empty string", () => {
    expect(parseDependsOn("")).toEqual([])
  })

  it("parses valid JSON array of strings", () => {
    expect(parseDependsOn('["task-1", "task-2"]')).toEqual(["task-1", "task-2"])
  })

  it("returns empty array for malformed JSON", () => {
    expect(parseDependsOn("{not valid json")).toEqual([])
  })

  it("returns empty array for JSON non-array", () => {
    expect(parseDependsOn('"just a string"')).toEqual([])
    expect(parseDependsOn("42")).toEqual([])
    expect(parseDependsOn('{"key": "value"}')).toEqual([])
  })

  it("filters out non-string values from array", () => {
    expect(parseDependsOn('[1, "task-1", null, true, "task-2"]')).toEqual(["task-1", "task-2"])
  })

  it("handles empty JSON array", () => {
    expect(parseDependsOn("[]")).toEqual([])
  })
})

// --- pickEdgeColor ---

describe("pickEdgeColor", () => {
  it("returns a color from the palette", () => {
    const color = pickEdgeColor("source-1", "target-1")
    expect(edgeColorPalette).toContain(color)
  })

  it("is deterministic for same inputs", () => {
    const a = pickEdgeColor("abc", "def")
    const b = pickEdgeColor("abc", "def")
    expect(a).toBe(b)
  })

  it("produces different colors for different pairs (usually)", () => {
    const colors = new Set<string>()
    for (let i = 0; i < 20; i++) {
      colors.add(pickEdgeColor(`source-${i}`, `target-${i}`))
    }
    // Should hit multiple palette entries
    expect(colors.size).toBeGreaterThan(1)
  })
})

// --- computeTopologicalLevels ---

describe("computeTopologicalLevels", () => {
  it("assigns level 0 to tasks with no dependencies", () => {
    const deps = new Map<string, string[]>([
      ["a", []],
      ["b", []],
    ])
    const levels = computeTopologicalLevels(["a", "b"], deps)
    expect(levels.get("a")).toBe(0)
    expect(levels.get("b")).toBe(0)
  })

  it("assigns incremental levels for a chain", () => {
    // a -> b -> c
    const deps = new Map<string, string[]>([
      ["a", []],
      ["b", ["a"]],
      ["c", ["b"]],
    ])
    const levels = computeTopologicalLevels(["a", "b", "c"], deps)
    expect(levels.get("a")).toBe(0)
    expect(levels.get("b")).toBe(1)
    expect(levels.get("c")).toBe(2)
  })

  it("handles diamond dependencies", () => {
    //   a
    //  / \
    // b   c
    //  \ /
    //   d
    const deps = new Map<string, string[]>([
      ["a", []],
      ["b", ["a"]],
      ["c", ["a"]],
      ["d", ["b", "c"]],
    ])
    const levels = computeTopologicalLevels(["a", "b", "c", "d"], deps)
    expect(levels.get("a")).toBe(0)
    expect(levels.get("b")).toBe(1)
    expect(levels.get("c")).toBe(1)
    expect(levels.get("d")).toBe(2)
  })

  it("handles cycles by breaking recursion", () => {
    // a -> b -> a (cycle)
    const deps = new Map<string, string[]>([
      ["a", ["b"]],
      ["b", ["a"]],
    ])
    const levels = computeTopologicalLevels(["a", "b"], deps)
    // Should not throw; levels will be assigned (cycle-breaker returns 0)
    expect(levels.has("a")).toBe(true)
    expect(levels.has("b")).toBe(true)
  })

  it("handles tasks not in the dependency map", () => {
    const deps = new Map<string, string[]>()
    const levels = computeTopologicalLevels(["orphan"], deps)
    expect(levels.get("orphan")).toBe(0)
  })

  it("handles parallel tasks (all at level 0)", () => {
    const deps = new Map<string, string[]>([
      ["a", []],
      ["b", []],
      ["c", []],
    ])
    const levels = computeTopologicalLevels(["a", "b", "c"], deps)
    expect(levels.get("a")).toBe(0)
    expect(levels.get("b")).toBe(0)
    expect(levels.get("c")).toBe(0)
  })
})

// --- groupByLevel ---

describe("groupByLevel", () => {
  it("groups items by their level", () => {
    const items = [
      { id: "a", name: "A" },
      { id: "b", name: "B" },
      { id: "c", name: "C" },
    ]
    const levels = new Map([["a", 0], ["b", 1], ["c", 0]])
    const groups = groupByLevel(items, (i) => i.id, levels)

    expect(groups.get(0)?.map((i) => i.id)).toEqual(["a", "c"])
    expect(groups.get(1)?.map((i) => i.id)).toEqual(["b"])
  })

  it("defaults to level 0 for items not in levels map", () => {
    const items = [{ id: "x" }]
    const levels = new Map<string, number>()
    const groups = groupByLevel(items, (i) => i.id, levels)
    expect(groups.get(0)?.length).toBe(1)
  })
})

// --- computeCrewDimensions ---

describe("computeCrewDimensions", () => {
  it("computes correct dimensions for single task", () => {
    const { width, height } = computeCrewDimensions(0, 1)
    expect(width).toBe(1 * (LAYOUT.TASK_WIDTH + LAYOUT.TASK_H_GAP) + LAYOUT.CREW_PADDING_SIDE * 2)
    expect(height).toBe(1 * (LAYOUT.TASK_HEIGHT + LAYOUT.TASK_V_GAP) + LAYOUT.CREW_PADDING_TOP + LAYOUT.CREW_PADDING_BOTTOM)
  })

  it("scales with levels and level size", () => {
    const { width: w1 } = computeCrewDimensions(0, 1)
    const { width: w2 } = computeCrewDimensions(2, 1)
    expect(w2).toBeGreaterThan(w1)

    const { height: h1 } = computeCrewDimensions(0, 1)
    const { height: h2 } = computeCrewDimensions(0, 5)
    expect(h2).toBeGreaterThan(h1)
  })
})

// --- computeTaskPosition ---

describe("computeTaskPosition", () => {
  it("positions first task at padding offsets", () => {
    const { x, y } = computeTaskPosition(0, 0)
    expect(x).toBe(LAYOUT.CREW_PADDING_SIDE)
    expect(y).toBe(LAYOUT.CREW_PADDING_TOP)
  })

  it("increments x by level", () => {
    const { x: x0 } = computeTaskPosition(0, 0)
    const { x: x1 } = computeTaskPosition(1, 0)
    expect(x1 - x0).toBe(LAYOUT.TASK_WIDTH + LAYOUT.TASK_H_GAP)
  })

  it("increments y by index", () => {
    const { y: y0 } = computeTaskPosition(0, 0)
    const { y: y1 } = computeTaskPosition(0, 1)
    expect(y1 - y0).toBe(LAYOUT.TASK_HEIGHT + LAYOUT.TASK_V_GAP)
  })
})

// --- selectActiveMissions ---

describe("selectActiveMissions", () => {
  const makeMission = (id: string, status: string, updated_at: string) => ({
    id,
    status,
    updated_at,
  })

  it("returns active missions when available", () => {
    const missions = [
      makeMission("1", "COMPLETED", "2026-01-01"),
      makeMission("2", "IN_PROGRESS", "2026-01-02"),
      makeMission("3", "PLANNING", "2026-01-03"),
    ]
    const result = selectActiveMissions(missions)
    expect(result.map((m) => m.id)).toEqual(["2", "3"])
  })

  it("includes REVIEW status as active", () => {
    const missions = [
      makeMission("1", "COMPLETED", "2026-01-01"),
      makeMission("2", "REVIEW", "2026-01-02"),
    ]
    const result = selectActiveMissions(missions)
    expect(result.map((m) => m.id)).toEqual(["2"])
  })

  it("falls back to most recent when no active missions", () => {
    const missions = [
      makeMission("1", "COMPLETED", "2026-01-01"),
      makeMission("2", "COMPLETED", "2026-01-03"),
      makeMission("3", "FAILED", "2026-01-02"),
      makeMission("4", "COMPLETED", "2026-01-04"),
    ]
    const result = selectActiveMissions(missions, 3)
    expect(result.map((m) => m.id)).toEqual(["4", "2", "3"])
  })

  it("returns empty for empty input", () => {
    expect(selectActiveMissions([])).toEqual([])
  })

  it("respects custom fallback count", () => {
    const missions = [
      makeMission("1", "COMPLETED", "2026-01-01"),
      makeMission("2", "COMPLETED", "2026-01-02"),
    ]
    const result = selectActiveMissions(missions, 1)
    expect(result.length).toBe(1)
    expect(result[0].id).toBe("2")
  })
})
