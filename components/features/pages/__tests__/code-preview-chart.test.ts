import { describe, expect, it } from "vitest"
import { CHART_LIMITS, renderChart } from "../code-preview-chart"

const ok = (spec: unknown) => {
  const result = renderChart(typeof spec === "string" ? spec : JSON.stringify(spec))
  if (!result.ok) throw new Error(result.error)
  return result.node
}
const fail = (spec: unknown) => {
  const result = renderChart(typeof spec === "string" ? spec : JSON.stringify(spec))
  if (result.ok) throw new Error("expected an error")
  return result.error
}

const SALES = { type: "bar", title: "Sales", x: ["Q1", "Q2", "Q3"], series: [{ name: "North", values: [10, 20, 15] }, { name: "South", values: [5, -4, 12.5] }] }

describe("renderChart", () => {
  it("draws a bar chart as an accessible SVG with a value title per bar and series colours from tokens", () => {
    const node = ok(SALES)
    const svg = node.querySelector("svg")!
    expect(svg.getAttribute("role")).toBe("img")
    expect(svg.getAttribute("aria-label")).toBe("Bar chart: Sales. 2 series (North, South) over 3 categories (Q1 to Q3); values from -4 to 20.")
    const bars = [...svg.querySelectorAll("rect[data-chart-value]")]
    expect(bars).toHaveLength(6)
    expect(bars.map(bar => bar.querySelector("title")!.textContent)).toEqual(["North, Q1: 10", "North, Q2: 20", "North, Q3: 15", "South, Q1: 5", "South, Q2: -4", "South, Q3: 12.5"])
    expect(bars[0].getAttribute("fill")).toBe("var(--series-1)")
    expect(bars[3].getAttribute("fill")).toBe("var(--series-2)")
    expect(svg.querySelector("[data-chart-title]")!.textContent).toBe("Sales")
    expect([...svg.querySelectorAll("[data-chart-legend] text")].map(text => text.textContent)).toEqual(["North", "South"])
  })

  it("offers the values as a data table behind Show data table", () => {
    const node = ok(SALES)
    const details = node.querySelector("details")!
    expect(details.open).toBe(false)
    expect(details.querySelector("summary")!.textContent).toBe("Show data table")
    const table = details.querySelector("table")!
    expect(table.querySelector("caption")!.textContent).toBe("Sales")
    expect([...table.querySelectorAll("thead th")].map(th => th.textContent)).toEqual(["", "North", "South"])
    expect([...table.querySelectorAll("tbody tr")].map(tr => [...tr.children].map(cell => cell.textContent))).toEqual([["Q1", "10", "5"], ["Q2", "20", "-4"], ["Q3", "15", "12.5"]])
    expect(table.querySelector("tbody th")!.getAttribute("scope")).toBe("row")
  })

  it("draws a line chart with one line and point titles per series", () => {
    const node = ok({ type: "line", x: ["Mon", "Tue", "Wed", "Thu"], series: [{ name: "Runs", values: [3, 5, 2, 8] }] })
    const svg = node.querySelector("svg")!
    expect(svg.getAttribute("aria-label")).toBe("Line chart. 1 series (Runs) over 4 categories (Mon to Thu); values from 2 to 8.")
    const lines = svg.querySelectorAll("polyline[data-chart-series]")
    expect(lines).toHaveLength(1)
    expect(lines[0].getAttribute("stroke")).toBe("var(--series-1)")
    expect(lines[0].getAttribute("points")!.split(" ")).toHaveLength(4)
    expect([...svg.querySelectorAll("circle title")].map(title => title.textContent)).toEqual(["Runs, Mon: 3", "Runs, Tue: 5", "Runs, Wed: 2", "Runs, Thu: 8"])
    expect(svg.querySelector("[data-chart-title]")).toBeNull()
  })

  it("keeps markup in titles, labels and names as text", () => {
    const payload = "<img src=x onerror=\"window.__ran = true\">"
    const node = ok({ type: "bar", title: payload, x: ["<script>window.__ran = true</script>", "b"], series: [{ name: "<a href=\"javascript:alert(1)\">s</a>", values: [1, 2] }] })
    expect(node.querySelector("img, script, a, [onerror], [href]")).toBeNull()
    expect(node.querySelector("[data-chart-title]")!.textContent).toBe(payload)
    expect(node.textContent).toContain("<script>window.__ran = true</script>")
    expect(node.querySelector("svg")!.getAttribute("aria-label")).toContain(payload)
    expect((window as unknown as { __ran?: boolean }).__ran).toBeUndefined()
  })

  it("shortens long axis labels on the chart but keeps them whole in titles and the table", () => {
    const long = "A very long category label that does not fit"
    const node = ok({ type: "bar", x: [long], series: [{ name: "s", values: [1] }] })
    const label = node.querySelector("[data-chart-x] text")!
    expect(label.textContent!.length).toBeLessThan(long.length)
    expect(label.querySelector("title")?.textContent ?? label.parentElement!.querySelector("title")!.textContent).toBe(long)
    expect(node.querySelector("tbody th")!.textContent).toBe(long)
  })

  it.each([
    ["not JSON", "{type: bar}", /^Cannot show this chart: it is not valid JSON/],
    ["not an object", [1, 2], /an object/],
    ["an unknown type", { ...SALES, type: "pie" }, /"type" must be "bar" or "line"/],
    ["an unknown key", { ...SALES, colors: ["red"] }, /unknown key "colors"/],
    ["an unknown series key", { ...SALES, series: [{ name: "a", values: [1, 2, 3], color: "red" }] }, /unknown key "color" in series 1/],
    ["a missing x", { type: "bar", series: [{ name: "a", values: [1] }] }, /"x" must be a list of labels/],
    ["no series", { ...SALES, series: [] }, /at least one series/],
    ["too many series", { ...SALES, series: Array.from({ length: CHART_LIMITS.series + 1 }, (_, i) => ({ name: `s${i}`, values: [1, 2, 3] })) }, /at most 8 series/],
    ["too many points", { type: "line", x: Array.from({ length: CHART_LIMITS.points + 1 }, (_, i) => `${i}`), series: [{ name: "a", values: Array.from({ length: CHART_LIMITS.points + 1 }, () => 1) }] }, /at most 200 points/],
    ["a value count mismatch", { ...SALES, series: [{ name: "a", values: [1, 2] }] }, /series 1 has 2 values, "x" has 3/],
    ["a string value", { ...SALES, series: [{ name: "a", values: [1, "2", 3] }] }, /series 1, value 2 must be a finite number/],
    ["an infinite value", '{"type":"bar","x":["a"],"series":[{"name":"a","values":[1e999]}]}', /series 1, value 1 must be a finite number/],
    ["a huge value", '{"type":"bar","x":["a"],"series":[{"name":"a","values":[1.7e308]}]}', /series 1, value 1 is larger than 1e300/],
    ["a huge negative value", '{"type":"bar","x":["a"],"series":[{"name":"a","values":[-1e301]}]}', /series 1, value 1 is larger than 1e300/],
    ["a null value", { ...SALES, series: [{ name: "a", values: [1, null, 3] }] }, /must be a finite number/],
    ["a too long title", { ...SALES, title: "t".repeat(CHART_LIMITS.text + 1) }, /"title" is longer than 200 characters/],
    ["a too long label", { ...SALES, x: ["a", "b", "c".repeat(CHART_LIMITS.text + 1)] }, /label 3 is longer than 200 characters/],
    ["a non-string label", { ...SALES, x: ["a", 2, "c"] }, /label 2 must be text/],
    ["a too long series name", { ...SALES, series: [{ name: "n".repeat(CHART_LIMITS.text + 1), values: [1, 2, 3] }] }, /name of series 1 is longer than 200 characters/],
  ])("refuses %s with a reason", (_name, spec, reason) => {
    expect(fail(spec)).toMatch(/^Cannot show this chart: /)
    expect(fail(spec)).toMatch(reason)
  })

  it("refuses empty and oversized sources", () => {
    expect(fail("  ")).toBe("Nothing to show yet. Write a chart in Source.")
    expect(fail(" ".repeat(CHART_LIMITS.chars + 1) + "{}")).toMatch(/longer than 50,000 characters/)
  })
})
