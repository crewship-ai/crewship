import { describe, expect, it } from "vitest"
import { readFileSync } from "node:fs"
import { join } from "node:path"

import { dashboardMeta, formatUntil, greeting, nextScheduledRun } from "../dashboard-head"
import type { PipelineSchedule } from "@/hooks/use-pipeline-schedules"

const at = (h: number, m = 0) => new Date(2026, 9, 15, h, m)

describe("page head", () => {
  it("greets by the person's local hour and first name", () => {
    expect(greeting(at(9), "Pavel Srba")).toBe("Good morning, Pavel")
    expect(greeting(at(12), "Pavel")).toBe("Good afternoon, Pavel")
    expect(greeting(at(18, 1), "Pavel")).toBe("Good evening, Pavel")
    expect(greeting(at(9), "")).toBe("Good morning")
    expect(greeting(at(9), null)).toBe("Good morning")
  })

  it("leaves out parts that would say zero", () => {
    const now = at(9, 12)
    expect(dashboardMeta({ now, attentionCount: 4, attentionKnown: true, runningAgents: 3, nextRunAt: at(9, 30).getTime() }))
      .toBe("Thursday 15 October · 4 things need you · 3 agents running · next routine in 18 min")
    expect(dashboardMeta({ now, attentionCount: 0, attentionKnown: true, runningAgents: 0, nextRunAt: null }))
      .toBe("Thursday 15 October · Nothing needs you")
    // Not knowing is not the same as nothing.
    expect(dashboardMeta({ now, attentionCount: 0, attentionKnown: false, runningAgents: 1, nextRunAt: null }))
      .toBe("Thursday 15 October · 1 agent running")
  })

  it("says how long until the next run in the largest sensible unit", () => {
    expect(formatUntil(18 * 60_000)).toBe("18 min")
    expect(formatUntil(20_000)).toBe("1 min")
    expect(formatUntil(3 * 3_600_000)).toBe("3 h")
    expect(formatUntil(3 * 86_400_000)).toBe("3 d")
  })

  it("picks the soonest enabled schedule still ahead", () => {
    const s = (id: string, next: Date | null, enabled = true) => ({ id, enabled, next_run_at: next?.toISOString() }) as PipelineSchedule
    const now = at(9).getTime()
    expect(nextScheduledRun([s("past", at(8)), s("off", at(9, 5), false), s("late", at(11)), s("soon", at(9, 30)), s("none", null)], now)).toBe(at(9, 30).getTime())
    expect(nextScheduledRun([], now)).toBeNull()
  })
})

describe("greeting size", () => {
  it("uses the phone page-title size below md and the page title above it (18 px at Small on a phone)", () => {
    const src = readFileSync(join(process.cwd(), "components/features/dashboard/dashboard-head.tsx"), "utf8")
    const h1 = /<h1 data-testid="dashboard-greeting" className="([^"]+)"/.exec(src)?.[1] ?? ""
    expect(h1.split(/\s+/)).toContain("text-title-phone")
    expect(h1.split(/\s+/)).toContain("md:text-title")
    expect(h1.split(/\s+/)).not.toContain("text-heading")
  })
})
