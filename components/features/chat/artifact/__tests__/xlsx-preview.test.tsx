import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react"
import { XlsxPreview } from "../xlsx-preview"
import type { WorkbookData } from "../xlsx-data"
import type { TableRow } from "../table-data"

vi.mock("react-virtuoso", () => ({ TableVirtuoso: ({ data, fixedHeaderContent, itemContent }: { data: TableRow[]; fixedHeaderContent: () => React.ReactNode; itemContent: (index: number, row: TableRow) => React.ReactNode }) => <table><thead>{fixedHeaderContent()}</thead><tbody>{data.map((row, index) => <tr key={row.index}>{itemContent(index, row)}</tr>)}</tbody></table> }))
const workbook: WorkbookData = { formulas: 2, missingFormulaResults: 1, sheets: [
  { name: "Revenue", table: { columns: 2, rows: [["Alpha", "10"], ["Beta", "2"]] } },
  { name: "Costs", table: { columns: 1, rows: [["Saved cost"]] } },
] }
const workers: TestWorker[] = []
class TestWorker {
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  terminate = vi.fn()
  postMessage = vi.fn()
  constructor(public url: string) { workers.push(this) }
  deliver(data = workbook) { this.onmessage?.({ data: { workbook: data } } as MessageEvent) }
}
beforeEach(() => { workers.length = 0; vi.stubGlobal("Worker", TestWorker); vi.stubGlobal("crypto", { subtle: { digest: vi.fn().mockResolvedValue(new Uint8Array(32).buffer) } }) })
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.useRealTimers() })

describe("XLSX worker preview", () => {
  it("uses local worker, saved values and source sheet/coordinates only on explicit selection", async () => {
    const ask = vi.fn(), source = new Uint8Array([1, 2, 3])
    render(<XlsxPreview path="reports/budget.xlsx" bytes={source} onAskSelection={ask} />)
    expect(workers[0].url).toBe("/chat-workers/xlsx-v1.js")
    expect(workers[0].postMessage).toHaveBeenCalledWith({ bytes: expect.any(ArrayBuffer) }, [expect.any(ArrayBuffer)])
    await act(async () => workers[0].deliver())
    expect(screen.getByText(/Saved values only/)).toHaveTextContent("1 formula results were not saved")
    expect(ask).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Sort column B" }))
    fireEvent.change(screen.getByLabelText("Filter table"), { target: { value: "Beta" } })
    fireEvent.click(screen.getByLabelText("Select source row 2"))
    fireEvent.click(screen.getByRole("button", { name: "Ask about selection (1)" }))
    expect(ask.mock.calls[0][0]).toContain('"sheet": "Revenue"')
    expect(ask.mock.calls[0][0]).toContain('"range": "A2:B2"')
    expect(ask.mock.calls[0][0]).toContain('"sha256": "' + "0".repeat(64) + '"')
    fireEvent.change(screen.getByLabelText("Worksheet"), { target: { value: "1" } })
    expect(screen.getByText("Saved cost")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Ask about selection (0)" })).toBeDisabled()
  })
  it("hides old values and selection immediately when source/path changes, ignores stale workers", async () => {
    const source = new Uint8Array([1]), next = new Uint8Array([2]), ask = vi.fn()
    const view = render(<XlsxPreview path="a.xlsx" bytes={source} onAskSelection={ask} />)
    await act(async () => workers[0].deliver())
    fireEvent.click(screen.getByLabelText("Select source row 1"))
    view.rerender(<XlsxPreview path="b.xlsx" bytes={next} onAskSelection={ask} />)
    expect(screen.queryByText("Alpha")).not.toBeInTheDocument()
    expect(workers[0].terminate).toHaveBeenCalled()
    await act(async () => workers[0].deliver())
    expect(screen.queryByText("Alpha")).not.toBeInTheDocument()
    await act(async () => workers[1].deliver())
    expect(screen.getByRole("button", { name: "Ask about selection (0)" })).toBeDisabled()
  })
  it("timeout is terminal and unmount terminates the worker", async () => {
    vi.useFakeTimers()
    const view = render(<XlsxPreview path="a.xlsx" bytes={new Uint8Array([1])} />)
    act(() => vi.advanceTimersByTime(5001))
    expect(screen.getByRole("alert")).toHaveTextContent("timed out")
    await act(async () => workers[0].deliver())
    expect(screen.queryByText("Alpha")).not.toBeInTheDocument()
    expect(screen.getByRole("alert")).toHaveTextContent("timed out")
    view.unmount(); expect(workers[0].terminate).toHaveBeenCalled()
  })
  it("offers fallback on parse error and disables selection context without SHA", async () => {
    const view = render(<XlsxPreview path="a.xlsx" bytes={new Uint8Array([1])} />)
    await act(async () => workers[0].onmessage?.({ data: { error: "Encrypted workbook. Download the original workbook." } } as MessageEvent))
    expect(screen.getByRole("alert")).toHaveTextContent("Download")
    view.unmount()
    vi.stubGlobal("crypto", { subtle: { digest: vi.fn().mockRejectedValue(new Error("unavailable")) } })
    render(<XlsxPreview path="a.xlsx" bytes={new Uint8Array([1])} onAskSelection={vi.fn()} />)
    await act(async () => workers[1].deliver())
    fireEvent.click(screen.getByLabelText("Select source row 1"))
    expect(screen.getByRole("button", { name: "Ask about selection (1)" })).toBeDisabled()
  })
})
