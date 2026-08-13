import { describe, it, expect, vi, beforeEach, afterEach } from "vitest"
import { render, screen, fireEvent, waitFor } from "@testing-library/react"

// =============================================================================
// On a phone, the composer offered a file browser and no camera.
//
// The whole composer tree had no `accept` and no `capture` on any input, so
// tapping the paperclip opened the OS document picker. "Photograph this and
// send it to the agent" — the single most obvious thing to do from a phone —
// took a detour through the camera app and the gallery.
//
// `capture="environment"` on an `<input type="file" accept="image/*">` is what
// asks the browser for the rear camera directly. The attribute is the whole
// mechanism; there is nothing else to build, which is why this was worth
// doing. What a headless test CAN prove is asserted below: the attributes are
// on the input, the control exists only on the mobile composer, and a file
// chosen through it goes through the SAME upload path as the paperclip rather
// than a second copy of it. Whether a phone actually opens the camera is a
// device check (PRD §9) and no assertion here claims otherwise.
// =============================================================================

vi.mock("@/hooks/use-workspace", () => ({
  useWorkspace: () => ({ workspaceId: "ws-test", loading: false }),
}))

// The mention picker and the slash palette are irrelevant here and pull in
// heavy trees; the desktop branch only needs to render enough to be counted.
vi.mock("../mention-autocomplete", () => ({
  MentionAutocomplete: () => null,
}))

const toastError = vi.fn()
vi.mock("sonner", () => ({
  toast: { error: (m: string) => toastError(m), success: vi.fn(), info: vi.fn() },
}))

import { ChatComposer } from "../chat-composer"
import { useComposerStore } from "@/stores/composer-store"

const baseProps = {
  agentId: "agent-1",
  sessionId: "sess-1",
  agentName: "Filip",
  isStreaming: false,
  connectionStatus: "connected",
  stopGeneration: vi.fn(),
  ensureSession: vi.fn(async () => {}),
  sendMessage: vi.fn(),
}

function cameraInput(): HTMLInputElement | null {
  return document.querySelector<HTMLInputElement>('input[type="file"][capture]')
}

describe("composer camera capture", () => {
  beforeEach(() => {
    useComposerStore.setState({ attachments: {}, drafts: {} })
    toastError.mockClear()
    global.fetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ path: "/p/photo.jpg", agent_path: "/output/filip/attachments/sess-1/photo.jpg" }),
      }),
    ) as unknown as typeof fetch
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it("mobile: exposes a camera control asking for the rear camera", () => {
    render(<ChatComposer {...baseProps} variant="mobile" />)

    expect(screen.getByRole("button", { name: /take a photo/i })).toBeInTheDocument()
    const input = cameraInput()
    expect(input).not.toBeNull()
    expect(input!.getAttribute("accept")).toBe("image/*")
    expect(input!.getAttribute("capture")).toBe("environment")
    // Several shots in a row without reopening the composer.
    expect(input!.multiple).toBe(true)
  })

  it("desktop: no camera control at all", () => {
    render(<ChatComposer {...baseProps} variant="desktop" />)

    expect(screen.queryByRole("button", { name: /take a photo/i })).not.toBeInTheDocument()
    expect(cameraInput()).toBeNull()
  })

  it("a captured photo goes through the same upload endpoint as the paperclip", async () => {
    render(<ChatComposer {...baseProps} variant="mobile" />)
    const input = cameraInput()!

    const file = new File(["binary"], "IMG_0001.jpg", { type: "image/jpeg" })
    Object.defineProperty(input, "files", { value: [file], configurable: true })
    fireEvent.change(input)

    await waitFor(() => expect(global.fetch).toHaveBeenCalled())
    const url = String(vi.mocked(global.fetch).mock.calls[0][0])
    expect(url).toContain("/api/v1/agents/agent-1/chats/sess-1/attachments")
    expect(url).toContain("workspace_id=ws-test")
    const init = vi.mocked(global.fetch).mock.calls[0][1] as RequestInit
    expect(init.method).toBe("POST")
    expect(init.body).toBeInstanceOf(FormData)

    // …and the chip lands in the same per-session attachment list the
    // paperclip writes to, promoted to "ready" with the server-side path.
    await waitFor(() => {
      const list = useComposerStore.getState().attachments["sess-1"] ?? []
      expect(list).toHaveLength(1)
      expect(list[0].status).toBe("ready")
      expect(list[0].url).toBe("/output/filip/attachments/sess-1/photo.jpg")
    })
  })

  it("the mobile composer shows the resulting attachment chips", async () => {
    render(<ChatComposer {...baseProps} variant="mobile" />)
    const input = cameraInput()!
    const file = new File(["binary"], "IMG_0002.jpg", { type: "image/jpeg" })
    Object.defineProperty(input, "files", { value: [file], configurable: true })
    fireEvent.change(input)

    await waitFor(() => expect(screen.getByText("IMG_0002.jpg")).toBeInTheDocument())
  })
})
