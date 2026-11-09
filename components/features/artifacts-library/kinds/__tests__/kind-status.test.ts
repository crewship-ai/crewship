import { describe, expect, it } from "vitest"
import type { DashboardCollection, DashboardDataset } from "@/lib/pages/artifact-dashboard"
import type { DashboardRunSummary } from "@/lib/pages/artifact-dashboard-runs"
import { appCapabilities, appFrameStatus, runtimeMissing } from "../app/app-status"
import { dashboardFrameStatus, dashboardFreshness, freshnessLabel } from "../dashboard/freshness"

const runtime = { runtime_url: "https://apps.example/runtime", development_same_origin: false, collections: ["quotes"] }

describe("App capabilities", () => {
  it("declares nothing for a static project", () => {
    expect(appCapabilities(undefined, true)).toEqual([])
  })
  it("lists only what the interactive runtime offers: the viewer and the declared collections", () => {
    const caps = appCapabilities(runtime, false)
    expect(caps.map(cap => cap.label)).toEqual(["User", "Data"])
    expect(caps[1].detail).toContain("Reads this artifact's shared data (quotes)")
    expect(appCapabilities(runtime, true)[1].detail).toContain("Reads and saves")
  })
  it("treats an empty runtime URL as a missing runtime origin", () => {
    expect(runtimeMissing({ ...runtime, runtime_url: "" })).toBe(true)
    expect(runtimeMissing(runtime)).toBe(false)
    expect(runtimeMissing(undefined)).toBe(false)
  })
})

describe("App status", () => {
  const base = { canWrite: true, canComment: true, viewingOld: false, shown: 4 }
  it.each([
    [{ ...base, viewingOld: true }, "readonly", "Viewing v4"],
    [{ ...base, interactive: { ...runtime, runtime_url: "" } }, "error", "Can't run here"],
    [{ ...base, canWrite: false }, "readonly", "Can comment"],
    [{ ...base, canWrite: false, canComment: false }, "readonly", undefined],
    [{ ...base, interactive: runtime }, "live", "Running"],
  ])("maps %o to %s", (input, key, label) => {
    expect(appFrameStatus(input)).toMatchObject({ key, ...(label ? { label } : {}) })
  })
  it("reports nothing for a static app on its latest version", () => {
    expect(appFrameStatus(base)).toBeNull()
  })
})

const collection = (name: string, docs: [string, string][]): DashboardCollection => ({
  collection: name, code_version: 1,
  documents: docs.map(([updated, source], index) => ({ id: `d${index}`, version: 1, value: {}, updated_at: updated, source_label: source, source_kind: "agent" })),
})

describe("Dashboard freshness", () => {
  const now = new Date("2026-11-08T12:00:00")
  it("takes the newest updated_at and the named sources from the data read", () => {
    const fresh = dashboardFreshness([collection("deals", [["2026-11-08T10:32:00", "HubSpot"], ["2026-11-08T09:00:00", "HubSpot"]]), collection("reps", [])])
    expect(fresh.updatedAt?.toISOString()).toBe(new Date("2026-11-08T10:32:00").toISOString())
    expect(fresh.sources).toEqual(["HubSpot"])
    expect(fresh.collections[1]).toMatchObject({ name: "reps", documents: 0, updatedAt: null })
    expect(freshnessLabel(fresh, now)).toBe("Updated 10:32 · HubSpot")
  })
  it("counts several sources and says when nothing is written", () => {
    expect(freshnessLabel(dashboardFreshness([collection("a", [["2026-11-03T10:00:00", "Scout"], ["2026-11-03T09:00:00", "Pavel"]])]), now)).toBe("Updated 3 Nov · 2 sources")
    expect(freshnessLabel(dashboardFreshness([collection("a", [])]), now)).toBe("No data yet")
  })
  it("maps the read to Live or an error, and never invents Stale or Sample data", () => {
    const fresh = dashboardFreshness([collection("a", [["2026-11-08T10:32:00", "Scout"]])])
    const base = { pending: false, error: false, limitExceeded: false, freshness: fresh, viewingOld: false, shown: 2, now }
    expect(dashboardFrameStatus(base)).toMatchObject({ key: "live" })
    expect(dashboardFrameStatus({ ...base, pending: true, freshness: null })).toBeNull()
    expect(dashboardFrameStatus({ ...base, error: true })).toMatchObject({ key: "error", label: "Data unavailable" })
    expect(dashboardFrameStatus({ ...base, error: true, limitExceeded: true })).toMatchObject({ key: "error", label: "Too much data" })
    for (const input of [base, { ...base, viewingOld: true }]) expect(["stale", "refreshing"]).not.toContain(dashboardFrameStatus(input)?.key)
  })

  const ds = (over: Partial<DashboardDataset>): DashboardDataset => ({ title: null, description: null, mode: "planned", sample: false, maxAgeSeconds: null, ...over })
  const status = (datasets: Record<string, DashboardDataset>, written = "2026-11-08T10:00:00", extra: Partial<Parameters<typeof dashboardFrameStatus>[0]> = {}) => {
    const freshness = dashboardFreshness([collection("a", [[written, "Scout"]])], { status: "ok", datasets }, now)
    return dashboardFrameStatus({ pending: false, error: false, limitExceeded: false, freshness, viewingOld: false, shown: 2, now, ...extra })
  }
  it.each([
    ["planned data older than its max age is Stale", { a: ds({ title: "Tasks", maxAgeSeconds: 3600 }) }, "stale", /Tasks was last written 10:00, older than its stated maximum age of 1 h/],
    ["live data within its max age stays Live", { a: ds({ mode: "live", maxAgeSeconds: 3 * 3600 }) }, "live", /every few seconds/],
    ["planned data without a stated age is never Stale", { a: ds({}) }, "live", /every few seconds/],
    ["a fixed dataset is Fixed, never Stale", { a: ds({ mode: "fixed" }) }, "fixed", /no automatic refresh\. Last written 10:00/],
    ["sample data says so", { a: ds({ mode: "fixed", sample: true }) }, "sample", /marked this data as sample data/],
    ["stale wins over sample", { a: ds({ sample: true, maxAgeSeconds: 60 }) }, "stale", /older than its stated maximum age of 1 min/],
  ])("%s", (_, datasets, key, detail) => {
    expect(status(datasets)).toMatchObject({ key, detail: expect.stringMatching(detail) })
  })
  it("does not read a dataset from Object.prototype for a collection named constructor", () => {
    const fresh = dashboardFreshness([collection("constructor", [["2026-11-08T10:00:00", "Scout"]])], { status: "ok", datasets: {} }, now)
    expect(fresh.collections[0]).toMatchObject({ dataset: null, stale: false })
  })
  it("keeps the read error above every dataset state and names invalid details in the chip", () => {
    expect(status({ a: ds({ maxAgeSeconds: 60 }) }, undefined, { error: true })).toMatchObject({ key: "error" })
    const invalid = dashboardFreshness([collection("a", [["2026-11-08T10:00:00", "Scout"]])], { status: "invalid" }, now)
    expect(invalid).toMatchObject({ details: "invalid", collections: [{ dataset: null, stale: false }] })
    const chip = dashboardFrameStatus({ pending: false, error: false, limitExceeded: false, freshness: invalid, viewingOld: false, shown: 2, now })
    expect(chip).toMatchObject({ key: "live" })
    expect(chip!.detail).toContain("Dataset details in datasets.json are invalid")
  })

  describe("producer runs", () => {
    const run = (over: Partial<DashboardRunSummary> = {}): DashboardRunSummary => ({ collection: "a", state: "succeeded", startedAt: new Date("2026-11-08T11:40:00"), finishedAt: new Date("2026-11-08T11:41:00"), lastAttemptAt: new Date("2026-11-08T11:40:00"), lastSuccessAt: new Date("2026-11-08T11:41:00"), reason: null, startedBy: "Agent", ...over })
    const chip = (runs: DashboardRunSummary[] | null, datasets: Record<string, DashboardDataset> = { a: ds({ title: "Sales", maxAgeSeconds: 3600 }) }, written = "2026-11-08T11:41:00") => {
      const freshness = dashboardFreshness([collection("a", [[written, "Scout"]])], { status: "ok", datasets }, now, runs)
      return { freshness, status: dashboardFrameStatus({ pending: false, error: false, limitExceeded: false, freshness, viewingOld: false, shown: 2, now }) }
    }
    it("shows Refreshing while a run is in progress and keeps the last good values", () => {
      const { freshness, status } = chip([run({ state: "running", startedAt: new Date("2026-11-08T11:55:00"), finishedAt: null, lastAttemptAt: new Date("2026-11-08T11:55:00") })])
      expect(status).toMatchObject({ key: "refreshing", detail: expect.stringMatching(/Sales is refreshing since 11:55\. Showing the last good data from 11:41/) })
      expect(freshness.collections[0]).toMatchObject({ documents: 1, stale: false })
    })
    it("says the refresh failed with the reason the server returned, without dropping values", () => {
      const { freshness, status } = chip([run({ state: "failed", startedAt: new Date("2026-11-08T11:50:00"), finishedAt: new Date("2026-11-08T11:51:00"), lastAttemptAt: new Date("2026-11-08T11:50:00"), lastSuccessAt: new Date("2026-11-08T11:41:00"), reason: "warehouse 502" })])
      expect(status).toMatchObject({ key: "stale", label: "Refresh failed", detail: expect.stringMatching(/Sales: the refresh at 11:50 failed: warehouse 502\. Values keep the last successful refresh from 11:41\./) })
      expect(freshness.collections[0].documents).toBe(1)
    })
    it("does not invent a reason for a reader and says when nothing ever succeeded", () => {
      const { status } = chip([run({ state: "failed", lastSuccessAt: null, reason: null, lastAttemptAt: new Date("2026-11-08T11:50:00") })])
      expect(status).toMatchObject({ key: "stale", label: "Refresh failed" })
      expect(status!.detail).toMatch(/Sales: the refresh at 11:50 failed\. No refresh has succeeded yet\./)
      expect(status!.detail).not.toMatch(/: undefined|null/)
    })
    it("computes Stale from the last successful refresh, not from a later stray write", () => {
      const { freshness, status } = chip([run({ lastSuccessAt: new Date("2026-11-08T10:00:00"), finishedAt: new Date("2026-11-08T10:00:00") })], undefined, "2026-11-08T11:59:00")
      expect(freshness.collections[0]).toMatchObject({ stale: true, lastSuccessAt: new Date("2026-11-08T10:00:00") })
      expect(status).toMatchObject({ key: "stale", detail: expect.stringMatching(/Sales last refreshed successfully 10:00, older than its stated maximum age of 1 h/) })
    })
    it("stays Live after a recent success and falls back to the last write without runs", () => {
      expect(chip([run()]).status).toMatchObject({ key: "live", detail: expect.stringMatching(/Last successful refresh 11:41/) })
      expect(chip(null).status).toMatchObject({ key: "live" })
      expect(chip(null, undefined, "2026-11-08T09:00:00").status).toMatchObject({ key: "stale", detail: expect.stringMatching(/was last written 09:00/) })
    })
    it("calls a run that has been running for over 30 minutes stalled", () => {
      const { status } = chip([run({ state: "running", startedAt: new Date("2026-11-08T11:10:00"), finishedAt: null, lastAttemptAt: new Date("2026-11-08T11:10:00") })])
      expect(status).toMatchObject({ key: "stale", label: "Refresh stalled", detail: expect.stringMatching(/Sales has been refreshing since 11:10 without finishing/) })
    })
    it("lets a read error win over every run state", () => {
      const freshness = dashboardFreshness([collection("a", [["2026-11-08T11:00:00", "Scout"]])], { status: "absent" }, now, [run({ state: "failed" })])
      expect(dashboardFrameStatus({ pending: false, error: true, limitExceeded: false, freshness, viewingOld: false, shown: 2, now })).toMatchObject({ key: "error" })
    })
  })
})

