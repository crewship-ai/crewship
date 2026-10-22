import React from "react"
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react"
import { afterEach, expect, it, vi } from "vitest"
import { PageWorkbook } from "../page-workbook"

const requests = vi.hoisted(() => vi.fn())
vi.mock("@/lib/api-fetch", () => ({ apiFetch: requests, broadcastSessionExpired: vi.fn() }))
afterEach(() => { cleanup(); requests.mockReset() })

const text = (v: string) => ({ value: { t: "Text", v }, formula: null, style: 0 })
const number = (v: number) => ({ value: { t: "Number", v }, formula: null, style: 0 })
// GridCraft 0.10.0 output: B2 = 1, C9 = B2*2 (cached 2), plus a #DIV/0! and a boolean.
const sheet1 = { name: "Sheet1", cells: { bands: { "0": { rows: {
  "0": { "0": text("Item"), "1": text("Qty"), "3": { value: { t: "Error", v: "Div0" }, formula: { text: "1/0", array: null }, style: 0 }, "4": { value: { t: "Bool", v: true }, formula: null, style: 0 } },
  "1": { "0": text("Apples"), "1": number(10.0) },
  "8": { "0": text("Derived"), "2": { value: { t: "Number", v: 2.0 }, formula: { text: "B2*2", array: null }, style: 0 } },
} } }, len: 7 } }
const workbook = (rev: number, sheets: unknown[] = [sheet1], active_sheet = 0) => Response.json({ schema_version: 1, rev, generation: 1, snapshot: { sheets, active_sheet } }, { headers: { "cache-control": "no-store" } })

async function open(...responses: Array<Response | (() => Response)>) {
  for (const response of responses) requests.mockImplementationOnce(async () => typeof response === "function" ? response() : response)
  await act(async () => { render(<PageWorkbook workspaceId="ws" pageId="page-1" name="Budget" />) })
}
/** The td at an A1 address in the rendered grid (header row and row-number column skipped). */
function cell(address: string) {
  const [, letters, digits] = /^([A-Z]+)(\d+)$/.exec(address)!
  const col = [...letters].reduce((n, letter) => n * 26 + letter.charCodeAt(0) - 64, 0) - 1
  const row = within(screen.getByRole("table")).getAllByRole("row")[Number(digits)]
  return row.querySelectorAll("td")[col] as HTMLTableCellElement
}

it("shows values at their addresses, the revision and the read-only note", async () => {
  await open(workbook(7))
  expect(await screen.findByRole("table", { name: "Sheet Sheet1" })).toBeInTheDocument()
  expect(screen.getByRole("heading", { name: "Budget" })).toBeInTheDocument()
  expect(screen.getByText("Revision 7")).toBeInTheDocument()
  expect(screen.getByText("Read-only preview. Edit by importing a file or through an admitted agent.")).toBeInTheDocument()
  expect(cell("A1")).toHaveTextContent("Item")
  expect(cell("A2")).toHaveTextContent("Apples")
  expect(cell("B2")).toHaveTextContent(/^10$/)
  expect(cell("D1")).toHaveTextContent("#DIV/0!")
  expect(cell("E1")).toHaveTextContent("TRUE")
  expect(cell("B9")).toHaveTextContent("")
  // Used range A1:E9 is padded to the 10 x 5 minimum.
  expect(screen.getAllByRole("columnheader").map(header => header.textContent)).toEqual(["A", "B", "C", "D", "E"])
  expect(screen.getAllByRole("rowheader").map(header => header.textContent)).toEqual(["1", "2", "3", "4", "5", "6", "7", "8", "9", "10"])
})

it("shows a formula cell's value with the formula as its tooltip", async () => {
  await open(workbook(1))
  await screen.findByRole("table")
  expect(cell("C9")).toHaveTextContent(/^2$/)
  expect(cell("C9")).toHaveAttribute("title", "=B2*2")
  expect(cell("D1")).toHaveAttribute("title", "=1/0")
  expect(cell("B2")).not.toHaveAttribute("title")
})

it("reads the immutable Page route without caching", async () => {
  await open(workbook(1))
  await screen.findByRole("table")
  const [url, init] = requests.mock.calls[0]
  expect(url).toBe("/api/v1/pages/%40page-1/workbook?workspace_id=ws")
  expect(init).toMatchObject({ cache: "no-store" })
})

it("says the workbook is unavailable on 404 and renders no cells", async () => {
  await open(Response.json({ error: "not found" }, { status: 404 }))
  expect(await screen.findByRole("alert")).toHaveTextContent("Workbook unavailable")
  expect(screen.queryByRole("table")).toBeNull()
  expect(screen.queryByRole("button", { name: /retry/i })).toBeNull()
})

it("offers a retry after a server error", async () => {
  await open(Response.json({ error: "unavailable" }, { status: 503 }), workbook(2))
  expect(await screen.findByRole("alert")).toHaveTextContent("temporarily unavailable")
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Retry" })) })
  expect(await screen.findByText("Revision 2")).toBeInTheDocument()
  expect(cell("A2")).toHaveTextContent("Apples")
})

it("refuses a snapshot whose rows are filed under the wrong band", async () => {
  const misplaced = { name: "Sheet1", cells: { bands: { "0": { rows: { "70": { "0": text("x") } } } }, len: 1 } }
  await open(workbook(1, [misplaced]))
  expect(await screen.findByRole("alert")).toHaveTextContent("could not be read")
  expect(screen.queryByRole("table")).toBeNull()
})

it("starts on the active sheet and switches sheets", async () => {
  const sheet2 = { name: "Totals", cells: { bands: { "0": { rows: { "0": { "0": text("Grand") } } } }, len: 1 } }
  await open(workbook(1, [sheet1, sheet2], 1))
  expect(await screen.findByRole("table", { name: "Sheet Totals" })).toBeInTheDocument()
  expect(cell("A1")).toHaveTextContent("Grand")
  const tab = screen.getByRole("tab", { name: "Sheet1" })
  await act(async () => { fireEvent.mouseDown(tab, { button: 0 }) })
  expect(await screen.findByRole("table", { name: "Sheet Sheet1" })).toBeInTheDocument()
  expect(cell("A2")).toHaveTextContent("Apples")
})

it("shows an empty sheet as an empty grid with a note", async () => {
  await open(workbook(0, [{ name: "Sheet1", cells: { bands: {}, len: 0 } }]))
  expect(await screen.findByText("This sheet is empty.")).toBeInTheDocument()
  expect(screen.getAllByRole("columnheader")).toHaveLength(5)
})

it("names the part of a large sheet it does not draw", async () => {
  const far = { name: "Sheet1", cells: { bands: { "12": { rows: { "799": { "59": text("far") } } } }, len: 1 } }
  await open(workbook(1, [far]))
  expect(await screen.findByText("Showing A1:AZ500 of a sheet with 800 rows and 60 columns.")).toBeInTheDocument()
  expect(screen.getAllByRole("rowheader")).toHaveLength(500)
  expect(screen.getAllByRole("columnheader")).toHaveLength(52)
})

it("re-reads on focus so agent edits appear, and drops the cells when access is gone", async () => {
  const edited = structuredClone(sheet1)
  edited.cells.bands["0"].rows["1"]["1"] = number(42)
  await open(workbook(1), workbook(2, [edited]), Response.json({}, { status: 404 }))
  await screen.findByText("Revision 1")
  await act(async () => { window.dispatchEvent(new Event("focus")) })
  expect(await screen.findByText("Revision 2")).toBeInTheDocument()
  expect(cell("B2")).toHaveTextContent(/^42$/)
  await act(async () => { window.dispatchEvent(new Event("focus")) })
  await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Workbook unavailable"))
  expect(screen.queryByRole("table")).toBeNull()
})

it("keeps the last revision on screen when a refresh fails", async () => {
  await open(workbook(3), Response.json({}, { status: 500 }))
  await screen.findByText("Revision 3")
  await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Refresh" })) })
  expect(await screen.findByRole("alert")).toHaveTextContent("Showing the last loaded revision.")
  expect(cell("A2")).toHaveTextContent("Apples")
})
