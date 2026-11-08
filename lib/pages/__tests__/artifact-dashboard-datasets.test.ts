import { describe, expect, it } from "vitest"
import { DASHBOARD_DATASETS_FILE, parseDashboardDatasets } from "../artifact-dashboard"

const files = (...contents: string[]) => [
  { path: "dashboard.json", media_type: "application/json", content: '{"schema_version":1,"collections":["tasks","costs"]}' },
  ...contents.map(content => ({ path: DASHBOARD_DATASETS_FILE, media_type: "application/json", content })),
]
const declared = ["tasks", "costs"]

describe("parseDashboardDatasets", () => {
  it("is absent when the dashboard has no datasets.json, so v1 dashboards are unchanged", () => {
    expect(parseDashboardDatasets(files(), declared)).toEqual({ status: "absent" })
  })

  it("admits title, description, mode, sample and max age per declared collection", () => {
    const result = parseDashboardDatasets(files(JSON.stringify({ schema_version: 1, datasets: {
      tasks: { title: "Crew tasks", description: "Three crews over six weeks.\nInvented for a test.", mode: "fixed", sample: true },
      costs: { mode: "live", max_age_seconds: 3600 },
    } })), declared)
    expect(result).toEqual({ status: "ok", datasets: {
      tasks: { title: "Crew tasks", description: "Three crews over six weeks.\nInvented for a test.", mode: "fixed", sample: true, maxAgeSeconds: null },
      costs: { title: null, description: null, mode: "live", sample: false, maxAgeSeconds: 3600 },
    } })
  })

  it.each([
    ["unparseable JSON", "{"],
    ["wrong schema version", '{"schema_version":2,"datasets":{}}'],
    ["unknown top-level key", '{"schema_version":1,"datasets":{},"widgets":[]}'],
    ["missing datasets", '{"schema_version":1}'],
    ["undeclared collection", '{"schema_version":1,"datasets":{"sales":{"mode":"fixed"}}}'],
    ["unknown dataset key", '{"schema_version":1,"datasets":{"tasks":{"mode":"fixed","query":"select 1"}}}'],
    ["missing mode", '{"schema_version":1,"datasets":{"tasks":{"title":"Tasks"}}}'],
    ["unknown mode", '{"schema_version":1,"datasets":{"tasks":{"mode":"realtime"}}}'],
    ["max age on a fixed dataset", '{"schema_version":1,"datasets":{"tasks":{"mode":"fixed","max_age_seconds":60}}}'],
    ["max age below one minute", '{"schema_version":1,"datasets":{"tasks":{"mode":"live","max_age_seconds":59}}}'],
    ["fractional max age", '{"schema_version":1,"datasets":{"tasks":{"mode":"planned","max_age_seconds":90.5}}}'],
    ["sample not a boolean", '{"schema_version":1,"datasets":{"tasks":{"mode":"fixed","sample":"yes"}}}'],
    ["control character in title", '{"schema_version":1,"datasets":{"tasks":{"mode":"fixed","title":"a\\u0007b"}}}'],
    ["empty title", '{"schema_version":1,"datasets":{"tasks":{"mode":"fixed","title":"  "}}}'],
    ["title too long", JSON.stringify({ schema_version: 1, datasets: { tasks: { mode: "fixed", title: "x".repeat(121) } } })],
    ["duplicate collection key", '{"schema_version":1,"datasets":{"tasks":{"mode":"fixed"},"tasks":{"mode":"live"}}}'],
    ["duplicate field key", '{"schema_version":1,"datasets":{"tasks":{"mode":"fixed","mode":"live"}}}'],
    ["prototype key", '{"schema_version":1,"datasets":{"__proto__":{"mode":"fixed"}}}'],
    ["array instead of object", '{"schema_version":1,"datasets":[]}'],
  ])("rejects the whole file on %s", (_, content) => {
    expect(parseDashboardDatasets(files(content), declared)).toEqual({ status: "invalid" })
  })

  it("rejects two datasets.json files and an oversized file", () => {
    const ok = '{"schema_version":1,"datasets":{"tasks":{"mode":"fixed"}}}'
    expect(parseDashboardDatasets(files(ok, ok), declared)).toEqual({ status: "invalid" })
    expect(parseDashboardDatasets(files(`{"schema_version":1,"datasets":{"tasks":{"mode":"fixed","description":"${"x".repeat(17000)}"}}}`), declared)).toEqual({ status: "invalid" })
  })
})
