import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

// =============================================================================
// Attaching a file to a conversation that does not exist yet.
//
// A chat with no sessions opens on a DRAFT id: the page mints it locally and
// nothing is written server-side until the first message goes out (PRD Step 3 —
// arriving is not sending). The upload endpoint, however, resolves the chat row
// before it will take a byte — `SELECT agent_id FROM chats WHERE id = ?`,
// internal/api/proxy_attachments.go — and answers 404 Chat not found when there
// is none. So the very first conversation used to accept text before it would
// accept a file, and "photograph the receipt, attach it, send" — the flow the
// product was designed around — failed on the attach.
//
// Attaching IS an intent to converse, so the composer now runs the same
// `ensureSession()` the send path runs before it uploads. Three properties are
// pinned here, all of them about ORDER and about not lying:
//
//   · the row is ensured BEFORE the bytes leave — an upload that races the
//     create is the 404 all over again, intermittently;
//   · a create that fails uploads nothing and leaves NO chip that reads as an
//     attached file. A chip that says "attached" for a file the agent cannot
//     open is worse than no chip at all;
//   · opening the file picker and choosing nothing creates nothing. The trigger
//     is a real upload, not a dialog.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

vi.mock("../mention-autocomplete", () => ({
  MentionAutocomplete: () => null,
}))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: {
    error: (m: string, opts?: unknown) => toastError(m, opts),
    success: vi.fn(),
    info: vi.fn(),
  },
}))

import { ChatComposer } from "../chat-composer"
import { useComposerStore } from "@/stores/composer-store"
import { composeMessageWithAttachments } from "@/lib/attachment-message"
import type { AskForm } from "../../asks/types"

const sendMessage = vi.fn()

/** ChatPanel's `ensureSessionForSend`, as the composer sees it: a promise that
 *  says whether the `chats` row is there. Resolved by the test, so "did the
 *  upload wait for it?" is an assertion rather than a guess about timing. */
function deferredEnsure() {
  let resolve!: (ok: boolean) => void
  const gate = new Promise<boolean>((r) => { resolve = r })
  const fn = vi.fn(() => gate)
  return { fn, resolve }
}

const okUpload = (name: string) => ({
  ok: true,
  status: 200,
  json: async () => ({
    path: `attachments/sess-draft/${name}`,
    agent_path: `/output/filip/attachments/sess-draft/${name}`,
  }),
})

function baseProps(ensureSession: () => Promise<boolean>) {
  return {
    agentId: "agent-1",
    sessionId: "sess-draft",
    agentName: "Filip",
    isStreaming: false,
    connectionStatus: "connected",
    stopGeneration: vi.fn(),
    ensureSession,
    sendMessage,
  }
}

function file(name: string, type = "application/pdf") {
  return new File(["payload"], name, { type })
}

/** The paperclip's input. */
function pickFiles(files: File[]) {
  const input = document.querySelector<HTMLInputElement>(
    'input[type="file"]:not([aria-label])',
  )!
  Object.defineProperty(input, "files", { value: files, configurable: true })
  fireEvent.change(input)
}

/** The picker opened and dismissed: a change event carrying nothing. */
function pickNothing() {
  const input = document.querySelector<HTMLInputElement>(
    'input[type="file"]:not([aria-label])',
  )!
  Object.defineProperty(input, "files", { value: [], configurable: true })
  fireEvent.change(input)
}

function storeList() {
  return useComposerStore.getState().attachments["sess-draft"] ?? []
}

function chip(name: string): HTMLElement {
  const el = screen.getByText(name).closest("[data-status]")
  expect(el, `no chip found for ${name}`).not.toBeNull()
  return el as HTMLElement
}

/** Let queued microtasks run without asserting anything — used to prove that
 *  something did NOT happen. */
const settle = () => new Promise((r) => setTimeout(r, 0))

describe("attaching to a chat that has no row yet", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    sendMessage.mockClear()
    toastError.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("creates the conversation before it sends a single byte", async () => {
    global.fetch = vi.fn(() => Promise.resolve(okUpload("receipt.pdf"))) as unknown as typeof fetch
    const ensure = deferredEnsure()
    render(<ChatComposer {...baseProps(ensure.fn)} variant="desktop" />)

    pickFiles([file("receipt.pdf")])

    // The chip is up immediately — the file was accepted — but nothing has gone
    // to the attachments endpoint, because there is nowhere to put it yet.
    await waitFor(() => expect(ensure.fn).toHaveBeenCalled())
    await settle()
    expect(global.fetch).not.toHaveBeenCalled()
    expect(storeList()[0]?.status).toBe("uploading")

    ensure.resolve(true)

    await waitFor(() => expect(global.fetch).toHaveBeenCalledTimes(1))
    const url = String(vi.mocked(global.fetch).mock.calls[0][0])
    expect(url).toContain("/api/v1/agents/agent-1/chats/sess-draft/attachments")

    await waitFor(() => expect(storeList()[0]?.status).toBe("ready"))
    expect(composeMessageWithAttachments("have a look", storeList())).toContain(
      "attachments/sess-draft/receipt.pdf",
    )
  })

  it("uploads nothing, and says the file did not attach, when the row cannot be created", async () => {
    global.fetch = vi.fn(() => Promise.resolve(okUpload("receipt.pdf"))) as unknown as typeof fetch
    render(<ChatComposer {...baseProps(async () => false)} variant="desktop" />)

    pickFiles([file("receipt.pdf")])

    await waitFor(() => expect(storeList()[0]?.status).toBe("error"))
    // Not one byte: the endpoint would 404 on the missing row, and a request
    // that cannot succeed is not worth making.
    expect(global.fetch).not.toHaveBeenCalled()

    // The durable statement is the chip, and it says the file is NOT attached.
    const c = chip("receipt.pdf")
    expect(c.getAttribute("data-status")).toBe("error")
    expect(within(c).getByText(/failed/i)).toBeInTheDocument()
    expect(within(c).getByRole("button", { name: /retry receipt\.pdf/i })).toBeInTheDocument()
    expect(storeList()[0].path).toBeFalsy()

    // …announced once, naming the file and saying what to do about it.
    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
    const [title, opts] = toastError.mock.calls[0]
    const said = `${title} ${JSON.stringify(opts ?? {})}`
    expect(said).toContain("receipt.pdf")
    expect(said).toMatch(/retry/i)

    // And a message sent anyway names nothing that is not there.
    expect(composeMessageWithAttachments("have a look", storeList())).toBe("have a look")
  })

  it("retries the create, not just the upload, when the user presses Retry", async () => {
    global.fetch = vi.fn(() => Promise.resolve(okUpload("receipt.pdf"))) as unknown as typeof fetch
    let rowExists = false
    const ensureSession = vi.fn(async () => rowExists)
    render(<ChatComposer {...baseProps(ensureSession)} variant="desktop" />)

    pickFiles([file("receipt.pdf")])
    await waitFor(() => expect(storeList()[0]?.status).toBe("error"))

    rowExists = true
    fireEvent.click(screen.getByRole("button", { name: /retry receipt\.pdf/i }))

    await waitFor(() => expect(storeList()[0]?.status).toBe("ready"))
    expect(ensureSession).toHaveBeenCalledTimes(2)
    expect(global.fetch).toHaveBeenCalledTimes(1)
  })

  it("creates nothing when the picker is opened and nothing is chosen", async () => {
    global.fetch = vi.fn(() => Promise.resolve(okUpload("x"))) as unknown as typeof fetch
    const ensureSession = vi.fn(async () => true)
    render(<ChatComposer {...baseProps(ensureSession)} variant="desktop" />)

    pickNothing()
    await settle()

    // Arriving creates nothing, and neither does browsing for a file. The row
    // is created by an upload actually starting.
    expect(ensureSession).not.toHaveBeenCalled()
    expect(global.fetch).not.toHaveBeenCalled()
    expect(storeList()).toHaveLength(0)
  })

  it("ensures the row for an upload started from an ask form's own field", async () => {
    // The sheet mounts its own AttachmentZone over the same per-session list
    // (asks/ask-form-sheet.tsx). Its upload needs the row for exactly the same
    // reason the composer's does, and it is reached by context rather than by a
    // prop — this is what stops the two zones from drifting apart.
    global.fetch = vi.fn(() => Promise.resolve(okUpload("receipt.pdf"))) as unknown as typeof fetch
    const ensureSession = vi.fn(async () => true)
    const askForm: AskForm = {
      id: "expense",
      label: "Log an expense",
      template: "Expense",
      fields: [{ name: "document", label: "Document", type: "file" }],
    }
    render(
      <ChatComposer
        {...baseProps(ensureSession)}
        variant="desktop"
        askForm={askForm}
        onCloseAskForm={vi.fn()}
        renderAskTemplate={() => "Expense"}
      />,
    )

    const sheetInput = screen
      .getByTestId("ask-sheet")
      .querySelector<HTMLInputElement>('input[type="file"]:not([aria-label])')!
    Object.defineProperty(sheetInput, "files", { value: [file("receipt.pdf")], configurable: true })
    fireEvent.change(sheetInput)

    await waitFor(() => expect(storeList()[0]?.status).toBe("ready"))
    expect(ensureSession).toHaveBeenCalledTimes(1)
  })

  it("creates nothing when every chosen file is refused before it is queued", async () => {
    global.fetch = vi.fn(() => Promise.resolve(okUpload("x"))) as unknown as typeof fetch
    const ensureSession = vi.fn(async () => true)
    render(<ChatComposer {...baseProps(ensureSession)} variant="desktop" />)

    // 26 MB — over the composer's own cap, so it never becomes an upload.
    const huge = new File([new Uint8Array(26 * 1024 * 1024)], "dump.bin", {
      type: "application/octet-stream",
    })
    pickFiles([huge])
    await settle()

    expect(ensureSession).not.toHaveBeenCalled()
    expect(global.fetch).not.toHaveBeenCalled()
  })
})
