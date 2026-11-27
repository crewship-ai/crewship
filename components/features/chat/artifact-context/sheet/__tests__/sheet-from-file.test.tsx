import { describe, it, expect, vi, beforeEach } from "vitest"
import { render, screen, cleanup, fireEvent, act, waitFor } from "@testing-library/react"

const h = vi.hoisted(() => ({
  allowed: true, gridcraft: true as boolean | undefined, workbook: false, pending: false,
  mutateAsync: vi.fn(), importXlsx: vi.fn(), initializeFromImport: vi.fn(), initializeNativeWorkbook: vi.fn(), discard: vi.fn(),
  emptyPage: null as { id: string; slug: string } | null,
}))
vi.mock("@/components/features/pages/use-word-document-creation", () => ({
  useWordDocumentCreation: () => ({ allowed: h.allowed, availability: { gridcraftNative: h.gridcraft, gridcraftWorkbook: h.workbook, isPending: h.pending }, create: { mutateAsync: h.mutateAsync }, emptyPage: h.emptyPage, discardEmptyPage: h.discard }),
}))
vi.mock("@/lib/craft-xlsx-import", async () => ({ ...await vi.importActual("@/lib/craft-xlsx-import"), importXlsx: h.importXlsx, initializeFromImport: h.initializeFromImport }))
vi.mock("@/lib/mooring/native-document-client", async () => ({ ...await vi.importActual("@/lib/mooring/native-document-client"), initializeNativeWorkbook: h.initializeNativeWorkbook }))

import { SheetFromFileRow } from "../sheet-from-file"
import { splitForSheets } from "../sheet-intake"
import { unfinishedImports } from "../unfinished-imports"
import { XlsxImportError } from "@/lib/craft-xlsx-import"
import { installMemoryStorage } from "./memory-storage"

installMemoryStorage()

const MiB = 1024 * 1024
const xlsx = (name = "Budget.xlsx", size = 10) => { const f = new File(["x"], name); Object.defineProperty(f, "size", { value: size }); return f }
const row = (over: Partial<Parameters<typeof SheetFromFileRow>[0]> = {}) => {
  const props = { workspaceId: "ws1", file: xlsx(), attached: true, sideBySide: true, onOpen: vi.fn(), onDismiss: vi.fn(), ...over }
  render(<SheetFromFileRow {...props} />)
  return props
}
const open = () => act(async () => { fireEvent.click(screen.getByRole("button", { name: /Open Budget.xlsx as a Sheet/ })) })

describe("which files can become a Sheet", () => {
  it("offers each .xlsx and leaves every other file alone", () => {
    const a = xlsx("a.XLSX"), b = xlsx("b.csv"), c = xlsx("c.xlsx")
    expect(splitForSheets([a, b, c])).toEqual({ offers: [{ file: a, attached: true }, { file: c, attached: true }], attach: [a, b, c] })
  })
  it("offers a workbook too large to attach, and does not try to attach it", () => {
    const big = xlsx("big.xlsx", 30 * MiB)
    expect(splitForSheets([big])).toEqual({ offers: [{ file: big, attached: false }], attach: [] })
  })
  it("offers a macro-enabled workbook (.xlsm) too", () => {
    const m = xlsx("Model.xlsm"), b = xlsx("Model.xlsb")
    expect(splitForSheets([m, b])).toEqual({ offers: [{ file: m, attached: true }], attach: [m, b] })
  })
  it("leaves a workbook too large for both to the ordinary refusal", () => {
    const huge = xlsx("huge.xlsx", 65 * MiB)
    expect(splitForSheets([huge])).toEqual({ offers: [], attach: [huge] })
  })
})

describe("SheetFromFileRow", () => {
  beforeEach(() => { cleanup(); Object.assign(h, { allowed: true, gridcraft: true, workbook: false, pending: false, emptyPage: null }); for (const f of [h.mutateAsync, h.importXlsx, h.initializeFromImport, h.initializeNativeWorkbook, h.discard]) f.mockReset() })

  type Prepare = { prepareImport: (id: string) => Promise<() => Promise<Response>> }
  const throughUpload = () => h.mutateAsync.mockImplementation(async (input: Prepare) => { await (await input.prepareImport("pg_9"))(); return { slug: "s", nativePageId: "pg_9" } })

  // With workbook Sheets an Excel file opens as a Sheet at once, and only one row stays.
  it("on a workbook server opens an .xlsx or .xlsm as a Sheet at once, without a button to press", async () => {
    h.workbook = true
    h.initializeNativeWorkbook.mockResolvedValue(new Response("{}"))
    throughUpload()
    const { onOpen } = row({ file: xlsx("Budget.xlsm") })
    // onOpen also names the chat the Sheet was tied to (987c19b6c); this row has none.
    await waitFor(() => expect(onOpen).toHaveBeenCalledWith("pg_9", undefined))
    expect(h.mutateAsync).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole("button", { name: /as a Sheet/ })).toBeNull()
  })
  it("once open beside the chat, leaves the Sheet's own chip as the only row for the file", async () => {
    h.workbook = true
    h.initializeNativeWorkbook.mockResolvedValue(new Response("{}"))
    throughUpload()
    const { onOpen } = row({ sideBySide: true })
    await waitFor(() => expect(onOpen).toHaveBeenCalled())
    expect(screen.queryByTestId("sheet-from-file")).toBeNull()
    expect(screen.queryByText("Opened beside the chat.")).toBeNull()
  })
  it("still asks before it opens one on a server without workbook Sheets, and opens nothing it may not", async () => {
    row()
    await act(async () => {})
    expect(h.mutateAsync).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: /Open Budget.xlsx as a Sheet/ })).toBeInTheDocument()
    cleanup(); h.workbook = true; h.allowed = false
    row()
    await act(async () => {})
    expect(h.mutateAsync).not.toHaveBeenCalled()
    cleanup(); h.allowed = true
    row({ file: xlsx("Huge.xlsx", 65 * 1024 * 1024) })
    await act(async () => {})
    expect(h.mutateAsync, "over the Sheet limit nothing is created").not.toHaveBeenCalled()
  })
  it("ties the Sheet to this chat from version 1, and says so when the chat is not this person's", async () => {
    h.initializeNativeWorkbook.mockResolvedValue(new Response("{}"))
    throughUpload()
    row({ chatId: "chat_7" })
    await open()
    expect(h.initializeNativeWorkbook).toHaveBeenCalledWith(expect.objectContaining({ chatId: "chat_7" }))
    cleanup(); h.initializeNativeWorkbook.mockReset()
    h.initializeNativeWorkbook.mockResolvedValue(new Response(JSON.stringify({ code: "chat_not_found" }), { status: 404 }))
    throughUpload()
    row({ chatId: "chat_x" })
    await open()
    expect(screen.getByRole("alert").textContent).toContain("This chat is not yours to add a Sheet to.")
  })
  // A new chat whose first act is a workbook made the Sheet before the chat was on
  // the server, so it was tied to no chat, the next message carried only the attachment and the agent used Python.
  it("starts the chat on the server first, so a new chat's Sheet is tied to it from version 1", async () => {
    h.initializeNativeWorkbook.mockResolvedValue(new Response("{}"))
    throughUpload()
    const ensureChat = vi.fn(async () => "chat_new")
    const { onOpen } = row({ ensureChat })
    await open()
    expect(ensureChat.mock.invocationCallOrder[0], "the chat first").toBeLessThan(h.mutateAsync.mock.invocationCallOrder[0])
    expect(h.initializeNativeWorkbook).toHaveBeenCalledWith(expect.objectContaining({ chatId: "chat_new" }))
    expect(onOpen).toHaveBeenCalledWith(expect.any(String), "chat_new")
  })
  // Send waits while a Sheet is being made, so the message carries the Sheet, not the file.
  it("says while it works, and when it stopped, so Send can wait for it", async () => {
    h.initializeNativeWorkbook.mockResolvedValue(new Response("{}"))
    throughUpload()
    const onWorking = vi.fn()
    row({ ensureChat: vi.fn(async () => "chat_new"), onWorking })
    await open()
    expect(onWorking.mock.calls.map(c => c[0])).toEqual([true, false])
  })
  it("creates no Sheet when the chat could not be started, and says so", async () => {
    throughUpload()
    // A Sheet without its chat is a failed Sheet. The message waits; the agent never gets the raw
    // workbook by itself, only when the person chooses to send it as a plain file.
    const { onFailed, onSendPlain } = row({ ensureChat: vi.fn(async () => undefined), onFailed: vi.fn(), onSendPlain: vi.fn() })
    await open()
    expect(h.mutateAsync).not.toHaveBeenCalled()
    expect(h.initializeNativeWorkbook).not.toHaveBeenCalled()
    expect(screen.getByRole("alert")).toHaveTextContent("The chat could not be started, so no Sheet was created.")
    expect(onFailed).toHaveBeenLastCalledWith(true)
    expect(screen.getByRole("alert")).toHaveTextContent("The message waits until you try again or send the workbook as a plain file.")
    expect(onSendPlain).not.toHaveBeenCalled()
  })
  it("uploads the workbook itself as the Sheet's first version and opens it, converting nothing", async () => {
    h.initializeNativeWorkbook.mockResolvedValue(new Response("{}"))
    throughUpload()
    const file = xlsx("Budget.xlsm")
    const { onOpen } = row({ file })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: /Open Budget.xlsm as a Sheet/ })) })
    expect(h.mutateAsync.mock.calls[0][0]).toMatchObject({ name: "Budget", format: "sheet" })
    expect(h.initializeNativeWorkbook).toHaveBeenCalledWith(expect.objectContaining({ workspaceId: "ws1", pageId: "pg_9", file, signal: expect.any(AbortSignal) }))
    expect(h.importXlsx).not.toHaveBeenCalled()
    expect(h.initializeFromImport).not.toHaveBeenCalled()
    expect(onOpen).toHaveBeenCalledWith("pg_9", undefined)
  })

  it("says it is uploading the workbook while it does, and nothing about converting", async () => {
    h.initializeNativeWorkbook.mockImplementation(() => new Promise(() => {}))
    h.mutateAsync.mockImplementation((input: Prepare) => input.prepareImport("pg_9").then(step => step()))
    row()
    await open()
    expect(screen.getByText("Uploading the workbook…")).toBeInTheDocument()
    expect(screen.queryByText(/Converting/)).toBeNull()
  })

  it.each([[413, "unpacked_too_large"], [415, "encrypted"], [415, "not_workbook"]])("turns a refused upload (%s %s) into the chat's reason, marked as refused before anything was written", async (status, code) => {
    h.initializeNativeWorkbook.mockResolvedValue(new Response(JSON.stringify({ code, error: "server words" }), { status }))
    let thrown: unknown
    h.mutateAsync.mockImplementation(async (input: Prepare) => { try { await (await input.prepareImport("pg_9"))() } catch (e) { thrown = e; throw e } })
    row()
    await open()
    expect(thrown).toBeInstanceOf(XlsxImportError)
    expect(thrown).toMatchObject({ code, beforeWrite: true })
    expect(await screen.findByRole("alert")).not.toHaveTextContent("server words")
  })

  it("leaves any other answer of the upload to the creation's own check", async () => {
    const answer = new Response("{}", { status: 503 })
    h.initializeNativeWorkbook.mockResolvedValue(answer)
    let got: Response | undefined
    h.mutateAsync.mockImplementation(async (input: Prepare) => { got = await (await input.prepareImport("pg_9"))(); return { slug: "s", nativePageId: "pg_9" } })
    row()
    await open()
    expect(got).toBe(answer)
  })

  it("shows the server's reason, keeps the attachment and lets the person try again", async () => {
    h.mutateAsync.mockRejectedValue(new XlsxImportError("too_large", "This workbook is larger than the import limits."))
    const { onOpen } = row()
    await open()
    expect(await screen.findByRole("alert")).toHaveTextContent("larger than a Sheet can open")
    expect(screen.getByRole("alert")).toHaveTextContent("Your original file did not change")
    expect(onOpen).not.toHaveBeenCalled()
    expect(screen.getByRole("button", { name: /Open Budget.xlsx as a Sheet/ })).toBeEnabled()
  })

  it.each([
    ["too_large", /Remove sheets or rows you do not need/],
    ["not_xlsx", /Save it again in Excel as \.xlsx/],
    ["truncated", /Nothing was imported/],
    ["busy", /Try again in a moment/],
    ["failed", /Try again/],
  ])("after a failed import (%s) says what to do next and that the original file did not change", async (code, next) => {
    h.mutateAsync.mockRejectedValue(new XlsxImportError(code, "The server said no."))
    row()
    await open()
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent(code === "too_large" ? /larger than a Sheet can open/ : "The server said no.")
    expect(alert).toHaveTextContent(next)
    expect(alert).toHaveTextContent("Your original file did not change")
  })

  it.each([
    ["unpacked_too_large", /unpacks to more than a Sheet takes/, /Remove sheets or rows you do not need/],
    ["too_many_parts", /more parts than a Sheet takes/, /Remove sheets or rows you do not need/],
    ["not_workbook", /not a readable \.xlsx or \.xlsm workbook/, /save it as \.xlsx or \.xlsm/],
    ["encrypted", /protected with a password/, /without a password/],
    ["unsupported", /\.xlsb, \.xls or \.ods/, /Save it as \.xlsx in Excel/],
    ["quota", /Storage for Sheets here is full/, /delete Sheets you no longer need/],
  ])("names a refused workbook file (%s) in the chat's words and says what to do", async (code, said, next) => {
    h.mutateAsync.mockRejectedValue(new XlsxImportError(code, "The server said no."))
    row()
    await open()
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent(said); expect(alert).toHaveTextContent(next)
    expect(alert).not.toHaveTextContent("The server said no.")
  })

  it.each([["not_empty", /already holds a workbook/], ["forbidden", /owns this empty Sheet/]])("says %s in the words of the chat: Sheet, not page", async (code, text) => {
    h.mutateAsync.mockRejectedValue(new XlsxImportError(code, "This page already holds a Sheet."))
    row()
    await open()
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent(text)
    expect(alert.textContent).not.toMatch(/\bpage\b/)
  })

  it("keeps the keyboard in the row: on its progress while it works, on Try again after a failure", async () => {
    let fail!: (e: Error) => void
    h.mutateAsync.mockImplementation(() => new Promise((_, reject) => { fail = reject }))
    row()
    screen.getByRole("button", { name: /Open Budget.xlsx as a Sheet/ }).focus()
    await open()
    expect(document.activeElement).not.toBe(document.body)
    expect(screen.getByTestId("sheet-from-file").contains(document.activeElement)).toBe(true)
    await act(async () => fail(new XlsxImportError("busy", "The server is busy.")))
    expect(document.activeElement).toBe(screen.getByRole("button", { name: /Open Budget.xlsx as a Sheet/ }))
    expect(document.activeElement).toHaveTextContent("Try again")
  })

  it("does not take the keyboard from where the person went meanwhile", async () => {
    let fail!: (e: Error) => void
    h.mutateAsync.mockImplementation(() => new Promise((_, reject) => { fail = reject }))
    render(<><textarea aria-label="Message" /><SheetFromFileRow workspaceId="ws1" file={xlsx()} attached sideBySide onOpen={vi.fn()} onDismiss={vi.fn()} /></>)
    await open()
    screen.getByRole("textbox", { name: "Message" }).focus()
    await act(async () => fail(new XlsxImportError("busy", "The server is busy.")))
    expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Message" }))
  })

  it("when Sheet is off on the server, says so and offers no retry that cannot help; the original file stays", async () => {
    h.mutateAsync.mockRejectedValue(new XlsxImportError("sheet_off", "Sheet is not available on this server"))
    row()
    await open()
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("Sheet is not available on this server.")
    expect(alert).toHaveTextContent("Your original file did not change")
    expect(screen.queryByRole("button", { name: /Open Budget.xlsx as a Sheet/ })).toBeNull()
  })

  it("never shows an internal code: an error without a sentence says what happened in words", async () => {
    h.mutateAsync.mockRejectedValue(new Error("page_create_failed"))
    row()
    await open()
    const alert = await screen.findByRole("alert")
    expect(alert).toHaveTextContent("The Sheet could not be created.")
    expect(alert.textContent).not.toMatch(/page_create_failed/)
  })

  it("offers to delete the empty Sheet a failed conversion left, never on its own", async () => {
    h.mutateAsync.mockRejectedValue(new XlsxImportError("not_xlsx", "Not a workbook."))
    h.emptyPage = { id: "pg_9", slug: "s" }
    row()
    await open()
    expect(h.discard).not.toHaveBeenCalled()
    await act(async () => { fireEvent.click(await screen.findByRole("button", { name: "Delete the empty Sheet" })) })
    expect(h.discard).toHaveBeenCalled()
  })

  it("can be cancelled while it uploads", async () => {
    let signal: AbortSignal | undefined
    h.initializeNativeWorkbook.mockImplementation(({ signal: s }: { signal: AbortSignal }) => { signal = s; return new Promise((_, reject) => s.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")))) })
    h.mutateAsync.mockImplementation((input: Prepare) => input.prepareImport("pg_9").then(step => step()))
    row()
    await open()
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Cancel" })) })
    expect(signal?.aborted).toBe(true)
    await waitFor(() => expect(screen.queryByText(/Uploading the workbook/)).toBeNull())
    expect(screen.getByRole("alert")).toHaveTextContent("The upload was cancelled.")
  })

  it("says why there is no Sheet for a person who may not create one, and only that", () => {
    h.allowed = false
    row()
    expect(screen.queryByRole("button", { name: /as a Sheet/ })).toBeNull()
    expect(screen.getByText(/may not create Sheets/)).toBeInTheDocument()
  })

  it("says Sheet is not available on this server, and nothing before the answer arrives", () => {
    h.gridcraft = undefined; h.pending = true
    row()
    expect(screen.queryByText(/not available/)).toBeNull()
    cleanup()
    h.gridcraft = false; h.pending = false
    row()
    expect(screen.getByText(/not available on this server/)).toBeInTheDocument()
  })

  it("says a large workbook is not attached and what the limits are", () => {
    row({ attached: false, file: xlsx("Budget.xlsx", 30 * MiB) })
    expect(screen.getByText(/25 MB/)).toBeInTheDocument()
    expect(screen.getByText(/64 MB/)).toBeInTheDocument()
  })

  it("notes a Sheet left empty when the person leaves during the conversion, for the library to offer deleting it", async () => {
    h.emptyPage = { id: "pg_9", slug: "s" }
    h.mutateAsync.mockImplementation(() => new Promise(() => {}))
    const { unmount } = render(<SheetFromFileRow workspaceId="ws1" file={xlsx()} attached sideBySide onOpen={vi.fn()} onDismiss={vi.fn()} userId="u1" />)
    await open()
    unmount()
    expect(unfinishedImports({ userId: "u1", workspaceId: "ws1" })).toEqual(["pg_9"])
    expect(unfinishedImports({ userId: "u2", workspaceId: "ws1" })).toEqual([])
  })
})

// A workbook whose Sheet failed is not sent by itself; the person tries again or sends it as a plain file.
describe("a Sheet that could not be made", () => {
  beforeEach(() => { h.allowed = true; h.gridcraft = true; h.workbook = false; h.pending = false; h.emptyPage = null; h.mutateAsync.mockReset() })
  it("says so to the chat, and offers to send the workbook as a plain file", async () => {
    h.mutateAsync.mockRejectedValue(new Error("The server refused this workbook."))
    const onFailed = vi.fn(), onSendPlain = vi.fn()
    row({ onFailed, onSendPlain })
    await open()
    await waitFor(() => expect(onFailed).toHaveBeenLastCalledWith(true))
    fireEvent.click(screen.getByRole("button", { name: "Send as a plain file" }))
    expect(onSendPlain).toHaveBeenCalledOnce()
    h.mutateAsync.mockResolvedValue({ nativePageId: "pg_9" })
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: /Open Budget.xlsx as a Sheet/ })) })
    await waitFor(() => expect(onFailed).toHaveBeenLastCalledWith(false))
  })
  it("offers no plain file for a workbook that is not attached", async () => {
    h.mutateAsync.mockRejectedValue(new Error("x"))
    row({ attached: false, onFailed: vi.fn(), onSendPlain: vi.fn() })
    await open()
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument())
    expect(screen.queryByRole("button", { name: "Send as a plain file" })).toBeNull()
  })
})
