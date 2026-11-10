// @vitest-environment node
/**
 * B01 on a real dataset: the Template Crewship Dashboard mirror (rwrx) runs
 * with its 18 original rows. A reader's crew filter recomputes every KPI,
 * the chart and the table from the same rows, writes nothing to the shared
 * data, and does not reach a second reader. Empty data and a failed read
 * show their own state instead of zeros.
 */
import { readFileSync } from "node:fs"
import { join } from "node:path"
import { Window } from "happy-dom"
import { afterEach, describe, expect, it, vi } from "vitest"

const MIRROR = join(__dirname, "../../../cmd/crewship/seeddata/showcase/mirror/rwrx")
const manifest = JSON.parse(readFileSync(join(MIRROR, "manifest.json"), "utf8")) as { files: { path: string; content: string }[] }
const page = manifest.files.find(file => file.path === "index.html")!.content
const rows = (JSON.parse(readFileSync(join(MIRROR, "data-tasks.json"), "utf8")) as { documents: { id: string; value: Record<string, unknown> }[] }).documents

type Get = (collection: string) => Promise<{ documents: { id: string; value: Record<string, unknown> }[] }>
const windows: Window[] = []
afterEach(async () => { for (const w of windows.splice(0)) await w.happyDOM.close() })

async function open(get: Get) {
  const window = new Window({ url: "https://runtime.example.test/", settings: { enableJavaScriptEvaluation: true, suppressCodeGenerationFromStringsWarning: true, disableCSSFileLoading: true, disableJavaScriptFileLoading: true, navigation: { disableChildFrameNavigation: true } } })
  windows.push(window)
  const writes = { batch: vi.fn(), set: vi.fn(), delete: vi.fn() }
  ;(window as unknown as { crewship: unknown }).crewship = { data: { get: vi.fn(get), ...writes } }
  window.document.write(page)
  await window.happyDOM.waitUntilComplete()
  for (let i = 0; i < 5; i++) { await new Promise(resolve => setTimeout(resolve, 20)); await window.happyDOM.waitUntilComplete() }
  const text = (selector: string) => window.document.querySelector(selector)?.textContent?.trim() ?? null
  const kpis = () => ["kpi-done", "kpi-rate", "kpi-cost", "kpi-cpt"].map(name => text(`[data-draw="${name}"]`))
  const pick = async (crew: string) => {
    // happy-dom's own element types, not the lib.dom ones the test project compiles against.
    const select = window.document.querySelector("#cx-crew") as unknown as { value: string; dispatchEvent(event: unknown): boolean }
    select.value = crew
    select.dispatchEvent(new window.Event("change"))
    for (let i = 0; i < 3; i++) { await new Promise(resolve => setTimeout(resolve, 20)); await window.happyDOM.waitUntilComplete() }
  }
  return { window, writes, kpis, text, pick, root: window.document.getElementById("dash-root")! }
}

const sum = (crew: string | null, field: string) => rows.filter(row => crew === null || row.value.crew === crew).reduce((total, row) => total + (row.value[field] as number), 0)

describe("rwrx dashboard mirror runtime (B01)", () => {
  it("recomputes every KPI and the chart from the same 18 rows when a reader filters, and writes nothing", async () => {
    expect(rows).toHaveLength(18)
    const reader = await open(async () => ({ documents: rows }))
    const all = reader.kpis()
    expect(all[0]).toBe(String(sum(null, "done")))
    const bars = () => reader.window.document.querySelectorAll('[data-draw="chart"] rect').length
    const allBars = bars()
    await reader.pick("Frontend")
    const frontend = reader.kpis()
    expect(frontend[0]).toBe(String(sum("Frontend", "done")))
    expect(frontend.every((value, index) => value !== all[index])).toBe(true)
    expect(bars()).toBeGreaterThan(0)
    expect(allBars).toBeGreaterThan(0)
    for (const write of Object.values(reader.writes)) expect(write).not.toHaveBeenCalled()
    // A second reader of the same shared rows opens with the page default, unaffected by the first reader's filter.
    const other = await open(async () => ({ documents: rows }))
    expect(other.kpis()).toEqual(all)
    expect(reader.kpis()).toEqual(frontend)
  })

  it("shows dashes, not zeros, for an empty collection, and an error state for a failed read", async () => {
    const empty = await open(async () => ({ documents: [] }))
    expect(empty.kpis()).toEqual(["—", "—", "—", "—"])
    expect(empty.root.getAttribute("data-dash-state")).toBe("empty")
    const failed = await open(async () => { throw new Error("403") })
    expect(failed.root.getAttribute("data-dash-state")).toBe("error")
    expect(failed.kpis().every(value => value === "—")).toBe(true)
  })
})
