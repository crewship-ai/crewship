// An .xlsx among the agent's output files opens as a Sheet in this chat, a new one beside any
// Sheet of the same name, never over it; the preview's size refusal offers the same.
import { afterEach, beforeEach, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"

const read = vi.fn()
vi.mock("@/components/features/chat/artifact/artifact-file-io", () => ({ readArtifactSnapshot: (o: unknown) => read(o) }))
const rows: Record<string, unknown>[] = []
vi.mock("../sheet-from-file", () => ({ SheetFromFileRow: (p: Record<string, unknown>) => { rows.push(p); return <button type="button" onClick={() => (p.onOpen as (id: string) => void)("page-9")}>row</button> } }))
const open = vi.fn()
let panelChat: string | undefined
vi.mock("../sheet-panel-context", () => ({ useSheetPanel: () => ({ open, chatId: panelChat }) }))
import { OutputSheetOffer, outputSheetName } from "../output-sheet-offer"

beforeEach(() => { read.mockReset(); rows.length = 0; open.mockReset(); panelChat = undefined })
afterEach(cleanup)

it("names the new Sheet after the file, so it sits beside one of the same name", () => {
  expect(outputSheetName("reports/Plan.xlsx")).toBe("Plan (agent output)")
  expect(outputSheetName("Q3 budget.xlsm")).toBe("Q3 budget (agent output)")
})
it("reads the output file and makes a Sheet of it in this chat, then opens it beside the chat", async () => {
  read.mockResolvedValue({ path: "out/Plan.xlsx", bytes: new Uint8Array([80, 75, 3, 4]) })
  render(<OutputSheetOffer workspaceId="w" agentId="a" path="out/Plan.xlsx" chatId="c1" viewerId="u" />)
  fireEvent.click(screen.getByRole("button", { name: "Open as a Sheet" }))
  await waitFor(() => expect(rows.length).toBeGreaterThan(0))
  expect(read).toHaveBeenCalledWith(expect.objectContaining({ agentId: "a", workspaceId: "w", path: "out/Plan.xlsx" }))
  const row = rows.at(-1)!
  expect((row.file as File).name).toBe("Plan.xlsx")
  expect((row.file as File).size).toBe(4)
  expect(row).toMatchObject({ workspaceId: "w", chatId: "c1", userId: "u", sheetName: "Plan (agent output)", sideBySide: true })
  fireEvent.click(screen.getByRole("button", { name: "row" }))
  expect(open).toHaveBeenCalledWith("page-9")
})
it("says in words when the file could not be read, and keeps the offer", async () => {
  read.mockRejectedValue(new Error("Failed to load: HTTP 500"))
  render(<OutputSheetOffer workspaceId="w" agentId="a" path="out/Plan.xlsx" />)
  fireEvent.click(screen.getByRole("button", { name: "Open as a Sheet" }))
  expect(await screen.findByRole("alert")).toHaveTextContent("The file could not be read, so no Sheet was created. Try again.")
  expect(screen.getByRole("button", { name: "Open as a Sheet" })).toBeEnabled()
  expect(rows).toHaveLength(0)
})
it("ties the Sheet to the chat the panel belongs to when the caller does not name one (the preview's refusal)", async () => {
  panelChat = "c7"
  read.mockResolvedValue({ path: "Plan.xlsx", bytes: new Uint8Array([1]) })
  render(<OutputSheetOffer workspaceId="w" agentId="a" path="Plan.xlsx" />)
  fireEvent.click(screen.getByRole("button", { name: "Open as a Sheet" }))
  await waitFor(() => expect(rows.at(-1)).toMatchObject({ chatId: "c7" }))
})
