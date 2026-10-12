import { describe, expect, it } from "vitest"
import { shadeNodes, HEATMAP_BORDER_CLASS } from "../percentile-heatmap"

describe("sibling metric heatmap", () => {
  it("omits coloring when disabled or no measurements exist", () => {
    expect(shadeNodes([{ stepId: "a", cost: 3 }], "off").size).toBe(0)
    expect(shadeNodes([], "cost").size).toBe(0)
    expect(shadeNodes([{ stepId: "pending" }, { stepId: "running", cost: -1 }], "cost").size).toBe(0)
  })
  it.each(["cost", "duration"] as const)("ranks %s across all five buckets including zero", mode => {
    const metrics = Array.from({ length: 21 }, (_, value) => ({ stepId: String(value), [mode]: value }))
    const original = structuredClone(metrics)
    const shaded = shadeNodes(metrics, mode)
    expect(shaded.size).toBe(21)
    expect(shaded.get("0")).toBe("p20")
    expect(shaded.get("4")).toBe("p20")
    expect(shaded.get("5")).toBe("p60")
    expect(shaded.get("12")).toBe("p60")
    expect(shaded.get("13")).toBe("p80")
    expect(shaded.get("16")).toBe("p80")
    expect(shaded.get("17")).toBe("p95")
    expect(shaded.get("19")).toBe("p95")
    expect(shaded.get("20")).toBe("outlier")
    expect(metrics).toEqual(original)
    for (const bucket of shaded.values()) expect(HEATMAP_BORDER_CLASS[bucket]).toContain("!border-2")
  })
  it("gives tied measurements the same bucket independent of input order", () => {
    const metrics = [{ stepId: "high", cost: 10 }, { stepId: "low", cost: 0 }, { stepId: "tie", cost: 0 }]
    const forward = shadeNodes(metrics, "cost")
    expect(forward.get("low")).toBe(forward.get("tie"))
    expect(shadeNodes([...metrics].reverse(), "cost")).toEqual(forward)
    expect(shadeNodes([{ stepId: "only", cost: 0 }], "cost").get("only")).toBe("p20")
  })
  it("uses the selected metric and ignores absent or negative measurements", () => {
    const metrics = [{ stepId: "cost-only", cost: 2 }, { stepId: "duration-only", duration: 3 }, { stepId: "negative", duration: -1 }]
    expect([...shadeNodes(metrics, "duration").keys()]).toEqual(["duration-only"])
    expect([...shadeNodes(metrics, "cost").keys()]).toEqual(["cost-only"])
  })
})
