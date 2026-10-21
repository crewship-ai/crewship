import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { TablePreview } from "../table-preview"
import { parseDelimited, type TableRow } from "../table-data"

vi.mock("react-virtuoso", () => ({ TableVirtuoso: ({ data, fixedHeaderContent, itemContent }: { data: TableRow[]; fixedHeaderContent: () => React.ReactNode; itemContent: (index: number, row: TableRow) => React.ReactNode }) => <table><thead>{fixedHeaderContent()}</thead><tbody>{data.map((row, index) => <tr key={row.index}>{itemContent(index, row)}</tr>)}</tbody></table> }))

const workers: TestWorker[] = []
class TestWorker {
  onmessage: ((event: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  terminate = vi.fn()
  constructor() { workers.push(this) }
  postMessage = vi.fn()
  deliver(text: string) { this.onmessage?.({ data: { table: parseDelimited(text, ",") } } as MessageEvent) }
}
const bytes = (text: string) => new TextEncoder().encode(text)
beforeEach(() => {
  workers.length = 0
  vi.stubGlobal("Worker", TestWorker)
  vi.stubGlobal("crypto", { subtle: { digest: vi.fn().mockResolvedValue(new Uint8Array(32).buffer) } })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.useRealTimers() })

describe("worker table preview", () => {
  it("keeps source coordinates when sorted/filtered and asks only after a user click", async () => {
    const onAsk = vi.fn()
    render(<TablePreview path="reports/a.csv" bytes={bytes("Alpha,10\nBeta,2")} onAskSelection={onAsk} />)
    expect(screen.getByRole("status")).toHaveTextContent("Preparing")
    expect(workers[0].postMessage).toHaveBeenCalledWith(expect.objectContaining({ separator: "," }), expect.any(Array))
    await act(async () => workers[0].deliver("Alpha,10\nBeta,2"))
    expect(onAsk).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Sort column B" }))
    fireEvent.change(screen.getByLabelText("Filter table"), { target: { value: "Beta" } })
    fireEvent.click(screen.getByRole("checkbox", { name: "Select source row 2" }))
    fireEvent.click(screen.getByRole("button", { name: "Ask about selection (1)" }))
    expect(onAsk).toHaveBeenCalledTimes(1)
    expect(onAsk.mock.calls[0][0]).toContain('"range": "A2:B2"')
    expect(onAsk.mock.calls[0][0]).toContain('"file": "reports/a.csv"')
    expect(onAsk.mock.calls[0][0]).toContain('"sha256": "' + "0".repeat(64) + '"')
    expect(screen.getByText(/Original row and column coordinates/)).toBeInTheDocument()
  })
  it("drops old selection immediately on new bytes and ignores a canceled worker", async () => {
    const first = bytes("old,1"), next = bytes("new,2")
    const view = render(<TablePreview path="a.csv" bytes={first} onAskSelection={vi.fn()} />)
    await act(async () => workers[0].deliver("old,1"))
    fireEvent.click(screen.getByRole("checkbox", { name: "Select source row 1" }))
    view.rerender(<TablePreview path="a.csv" bytes={next} onAskSelection={vi.fn()} />)
    expect(screen.queryByText("old")).not.toBeInTheDocument()
    expect(workers[0].terminate).toHaveBeenCalled()
    await act(async () => workers[0].deliver("stale,99"))
    expect(screen.queryByText("stale")).not.toBeInTheDocument()
    await act(async () => workers[1].deliver("new,2"))
    expect(screen.getByRole("button", { name: "Ask about selection (0)" })).toBeDisabled()
    expect(screen.getByRole("checkbox", { name: "Select source row 1" })).not.toBeChecked()
  })
  it("terminates timed-out and unmounted workers", async () => {
    vi.useFakeTimers()
    const view = render(<TablePreview path="a.csv" bytes={bytes("x")} />)
    act(() => vi.advanceTimersByTime(5001))
    expect(screen.getByRole("alert")).toHaveTextContent("timed out")
    expect(workers[0].terminate).toHaveBeenCalled()
    await act(async () => workers[0].deliver("late,1"))
    expect(screen.getByRole("alert")).toHaveTextContent("timed out")
    expect(screen.queryByText("late")).not.toBeInTheDocument()
    view.unmount()
    expect(workers[0].terminate.mock.calls.length).toBeGreaterThan(1)
  })
  it("shows parser errors and refuses context without a verified hash", async () => {
    const view = render(<TablePreview path="a.csv" bytes={bytes("x")} onAskSelection={vi.fn()} />)
    await act(async () => { workers[0].onmessage?.({ data: { error: "Malformed table" } } as MessageEvent) })
    expect(screen.getByRole("alert")).toHaveTextContent("Malformed table")
    view.unmount()
    vi.stubGlobal("crypto", { subtle: { digest: vi.fn().mockRejectedValue(new Error("unavailable")) } })
    render(<TablePreview path="b.csv" bytes={bytes("safe")} onAskSelection={vi.fn()} />)
    await act(async () => workers[1].deliver("safe"))
    fireEvent.click(screen.getByRole("checkbox", { name: "Select source row 1" }))
    await waitFor(() => expect(screen.getByRole("button", { name: "Ask about selection (1)" })).toBeDisabled())
  })
})
