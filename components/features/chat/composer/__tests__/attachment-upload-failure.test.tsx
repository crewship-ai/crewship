import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react"

// =============================================================================
// A failed upload must not look like an attached file.
//
// What happened on dev2: two files were dropped in the composer, BOTH uploads
// were refused by the server ("create parent dir: … permission denied"), and
// the user was left looking at one error toast naming one file and two chips
// that read exactly like two successfully attached documents. They believed
// one had landed. Neither had.
//
// Two separate promises are pinned here, and they fail differently:
//
//   · The CHIP is the durable surface. A toast is transient, and sonner stacks
//     toasts — the second one is a sliver behind the first — so the toast can
//     never be the only place a failure is stated. The chip must say "Upload
//     failed" in words, for every file that failed, and offer the retry.
//   · The MESSAGE is the load-bearing one. A confusing chip wastes a minute;
//     a path in the outgoing message for a file that was never written sends
//     the agent off to read something that does not exist, and it will report
//     back about a file it could not open. `composeMessageWithAttachments` is
//     asserted directly, not just the DOM, because that string is the contract
//     with the agent.
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

const sendMessage = vi.fn()
const ensureSession = vi.fn(async () => true)

const baseProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Filip",
  isStreaming: false,
  connectionStatus: "connected",
  stopGeneration: vi.fn(),
  ensureSession,
  sendMessage,
}

/** The two shapes a refused upload actually arrives in. A 500 with the Go
 *  handler's `{"error": …}` body is what dev2 produced; a fetch that throws is
 *  the offline / dropped-connection case, and it must not be handled by a
 *  different branch. */
const okResponse = (name: string) => ({
  ok: true,
  status: 200,
  json: async () => ({
    path: `attachments/sess-1/${name}`,
    agent_path: `/output/filip/attachments/sess-1/${name}`,
  }),
})

const serverError = () => ({
  ok: false,
  status: 500,
  json: async () => ({ error: "create parent dir: /output/filip/attachments: permission denied" }),
})

const serverErrorNoBody = () => ({
  ok: false,
  status: 502,
  json: async () => {
    throw new SyntaxError("Unexpected token < in JSON")
  },
})

function file(name: string, type = "text/plain") {
  return new File(["payload"], name, { type })
}

/** The paperclip's input. PromptInput ships its own unused file input with an
 *  aria-label, hence the :not() — this is the one AttachmentTrigger renders. */
function pickFiles(files: File[]) {
  const input = document.querySelector<HTMLInputElement>(
    'input[type="file"]:not([aria-label])',
  )!
  Object.defineProperty(input, "files", { value: files, configurable: true })
  fireEvent.change(input)
}

function storeList() {
  return useComposerStore.getState().attachments["sess-1"] ?? []
}

/** Every chip carries a data-status, so "is this on screen as a failure" is a
 *  question the test can ask without matching on Tailwind classes. */
function chip(name: string): HTMLElement {
  const el = screen.getByText(name).closest("[data-status]")
  expect(el, `no chip found for ${name}`).not.toBeNull()
  return el as HTMLElement
}

function submitForm() {
  fireEvent.submit(document.querySelector("form")!)
}

function typeInComposer(text: string) {
  fireEvent.change(document.querySelector("textarea")!, { target: { value: text } })
}

describe("a rejected upload never reads as an attached file", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    sendMessage.mockClear()
    ensureSession.mockClear()
    toastError.mockClear()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  const rejections: Array<{ name: string; impl: () => unknown; reason: RegExp }> = [
    {
      name: "the server refuses it (500 with an error body)",
      impl: () => Promise.resolve(serverError()),
      reason: /permission denied/i,
    },
    {
      name: "the server refuses it with no JSON body at all",
      impl: () => Promise.resolve(serverErrorNoBody()),
      reason: /502/,
    },
    {
      name: "the request never completes (fetch throws)",
      impl: () => Promise.reject(new TypeError("Failed to fetch")),
      reason: /failed to fetch/i,
    },
  ]

  for (const c of rejections) {
    it(`marks the chip failed and names the file when ${c.name}`, async () => {
      global.fetch = vi.fn(c.impl) as unknown as typeof fetch
      render(<ChatComposer {...baseProps} variant="desktop" />)

      pickFiles([file("invoice.pdf", "application/pdf")])

      await waitFor(() => expect(storeList()[0]?.status).toBe("error"))

      // Nothing landed, so nothing may carry a path — the path is the only
      // reason the message names a file at all.
      expect(storeList()[0].path).toBeFalsy()

      // The chip is still on screen (removing it would make the failure
      // invisible the moment the toast expires), but it says what it is.
      const c1 = chip("invoice.pdf")
      expect(c1.getAttribute("data-status")).toBe("error")
      expect(within(c1).getByText(/failed/i)).toBeInTheDocument()
      // …and it offers the way out, because the file is still on the user's
      // disk and re-picking it is three screens on a phone.
      expect(within(c1).getByRole("button", { name: /retry invoice\.pdf/i })).toBeInTheDocument()

      // One toast, naming the file, saying the reason and what to do next.
      await waitFor(() => expect(toastError).toHaveBeenCalledTimes(1))
      const [title, opts] = toastError.mock.calls[0]
      const said = `${title} ${JSON.stringify(opts ?? {})}`
      expect(said).toContain("invoice.pdf")
      expect(said).toMatch(c.reason)
      expect(said).toMatch(/retry/i)

      // And the message that would go out says nothing about it.
      expect(composeMessageWithAttachments("have a look", storeList())).toBe("have a look")
    })
  }

  it("gives two failed files two errors, not one", async () => {
    global.fetch = vi.fn(() => Promise.resolve(serverError())) as unknown as typeof fetch
    render(<ChatComposer {...baseProps} variant="desktop" />)

    pickFiles([file("first.pdf"), file("second.pdf")])

    await waitFor(() => expect(storeList()).toHaveLength(2))
    await waitFor(() => expect(storeList().every((a) => a.status === "error")).toBe(true))

    // Two toasts, one per file, each naming ITS file. The dev2 report was one
    // toast for two failures, which is how a user concludes the other one
    // worked.
    await waitFor(() => expect(toastError).toHaveBeenCalledTimes(2))
    const said = toastError.mock.calls.map(([t, o]) => `${t} ${JSON.stringify(o ?? {})}`)
    expect(said.filter((s) => s.includes("first.pdf"))).toHaveLength(1)
    expect(said.filter((s) => s.includes("second.pdf"))).toHaveLength(1)
    // Distinct toast ids, so sonner can never collapse one file's failure into
    // another's.
    const ids = toastError.mock.calls.map(([, o]) => (o as { id?: string } | undefined)?.id)
    expect(new Set(ids).size).toBe(2)
    expect(ids.every(Boolean)).toBe(true)

    // Both chips say so.
    expect(chip("first.pdf").getAttribute("data-status")).toBe("error")
    expect(chip("second.pdf").getAttribute("data-status")).toBe("error")

    expect(composeMessageWithAttachments("both of these", storeList())).toBe("both of these")
  })

  it("one succeeds and one fails: the message carries exactly the one that landed", async () => {
    let call = 0
    global.fetch = vi.fn(() => {
      call += 1
      return call === 1 ? Promise.resolve(okResponse("good.pdf")) : Promise.resolve(serverError())
    }) as unknown as typeof fetch

    render(<ChatComposer {...baseProps} variant="desktop" />)
    pickFiles([file("good.pdf"), file("bad.pdf")])

    await waitFor(() => expect(storeList()).toHaveLength(2))
    await waitFor(() => expect(storeList().map((a) => a.status)).toEqual(["ready", "error"]))

    // Asserted on the composer function itself, not only on what the DOM
    // shows: this string is the contract with the agent.
    const composed = composeMessageWithAttachments("two invoices", storeList())
    expect(composed).toContain("attachments/sess-1/good.pdf")
    expect(composed).not.toContain("bad.pdf")
    expect(composed).toMatch(/I've attached a file/)

    // …and the send agrees with it: exactly one path on the wire.
    typeInComposer("two invoices")
    submitForm()

    await waitFor(() => expect(sendMessage).toHaveBeenCalledTimes(1))
    const [content] = sendMessage.mock.calls[0] as [string]
    expect(content).toContain("attachments/sess-1/good.pdf")
    expect(content).not.toContain("bad.pdf")
    expect(content.match(/^- /gm) ?? []).toHaveLength(1)
  })

  it("will not send a message whose only attachment failed", async () => {
    global.fetch = vi.fn(() => Promise.resolve(serverError())) as unknown as typeof fetch
    render(<ChatComposer {...baseProps} variant="desktop" />)
    pickFiles([file("only.pdf")])

    await waitFor(() => expect(storeList()[0]?.status).toBe("error"))
    toastError.mockClear()
    // The upload itself calls ensureSession — attaching a file creates the
    // conversation on a draft session, the same way sending does
    // (attachment-cold-session.test.tsx). What this test is about is the SEND
    // below, so the counter starts here.
    ensureSession.mockClear()

    // With no text, a failed attachment is not content: Send used to look
    // available and then do nothing at all when pressed.
    expect(screen.getByRole("button", { name: "Submit" })).toBeDisabled()

    submitForm()

    await new Promise((r) => setTimeout(r, 0))
    expect(sendMessage).not.toHaveBeenCalled()
    expect(ensureSession).not.toHaveBeenCalled()
  })

  it("retries the same file in place, and the retry can succeed", async () => {
    let call = 0
    global.fetch = vi.fn(() => {
      call += 1
      return call === 1 ? Promise.resolve(serverError()) : Promise.resolve(okResponse("flaky.pdf"))
    }) as unknown as typeof fetch

    render(<ChatComposer {...baseProps} variant="desktop" />)
    pickFiles([file("flaky.pdf")])

    await waitFor(() => expect(storeList()[0]?.status).toBe("error"))
    const failedId = storeList()[0].id

    fireEvent.click(screen.getByRole("button", { name: /retry flaky\.pdf/i }))

    await waitFor(() => expect(storeList()[0]?.status).toBe("ready"))
    // Same chip, not a second one appended to the end of the list: the user's
    // attachments stay in the order they added them.
    expect(storeList()).toHaveLength(1)
    expect(storeList()[0].id).toBe(failedId)
    expect(storeList()[0].path).toBe("attachments/sess-1/flaky.pdf")
    expect(chip("flaky.pdf").getAttribute("data-status")).toBe("ready")

    expect(composeMessageWithAttachments("here", storeList())).toContain(
      "attachments/sess-1/flaky.pdf",
    )
  })

  it("a removed failed chip stops being anybody's problem", async () => {
    global.fetch = vi.fn(() => Promise.resolve(serverError())) as unknown as typeof fetch
    render(<ChatComposer {...baseProps} variant="desktop" />)
    pickFiles([file("gone.pdf")])

    await waitFor(() => expect(storeList()[0]?.status).toBe("error"))

    fireEvent.click(screen.getByRole("button", { name: /remove gone\.pdf/i }))

    await waitFor(() => expect(storeList()).toHaveLength(0))
    expect(screen.queryByText("gone.pdf")).toBeNull()
  })
})
