// The Design canvas inside the ArtifactFrame (Harbor 2 v87): controls in the slots and the drawer.
vi.mock("@/lib/local-draft-seal", () => ({
  createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
  forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import * as Y from "yjs"
import { DesignEditor, type DesignEditorFrameState } from "@/components/features/artifacts-design/design-editor"
import { designNodes, upsertDesignNode } from "@/components/features/artifacts-design/design-document"
import { ArtifactFrame, NO_FRAME_CAPABILITIES, type FrameCapabilities, type FrameLayout } from "../../../frame"
import { DesignPresence } from "../design-detail"
import { designFrameStatus } from "../design-status"

const state = vi.hoisted(() => ({ editable: true, origin: null as null | { chat_id: string; agent_id: string }, doc: null as Y.Doc | null, unsynced: false, saveData: undefined as undefined | { version: number }, saveSuccess: false }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => ({ data: { user: { id: "person" } } }) }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("@/components/features/artifacts-design/use-design", () => ({ useDesign: (_workspace: string, _slug: string, selectedVersion?: number) => {
  const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, isSuccess: false, error: null }
  return {
    query: { data: { page_id: "page", can_edit: state.editable, can_share: true, can_comment: true, latest_version: 2, origin: state.origin, comments: [] }, isError: false },
    comments: { data: { comments: [] }, isError: false },
    versions: { data: { versions: [{ version: 1 }, { version: 2 }], shared_version: 1 }, isError: false },
    doc: state.doc, nodes: designNodes(state.doc!), peers: [{ clientId: 2, name: "Marie", color: "#2563eb", cursor: { x: 12, y: 24 } }],
    connection: "connected", editable: state.editable && !selectedVersion, dirty: false, unsynced: state.unsynced, draftError: false,
    save: { ...mutation, isSuccess: state.saveSuccess, data: state.saveData }, restore: mutation, share: mutation, comment: mutation, connectAgent: mutation,
    setCursor: vi.fn(), conflicts: [], dismissConflict: vi.fn(),
  }
} }))

const ask = { ask: { chatHref: (prompt: string) => `/chat?page=landing&prompt=${prompt}` } }
function mount(layout: FrameLayout = "desktop", onFrameState?: (state: DesignEditorFrameState) => void) {
  const caps: FrameCapabilities = { ...NO_FRAME_CAPABILITIES, canEdit: state.editable, agent: ask }
  return render(<ArtifactFrame name="Landing" kind={{ kind: "design", mode: "canvas" }} caps={caps} onHome={vi.fn()} layout={layout}>
    <DesignEditor workspaceId="workspace" slug="@page" onFrameState={onFrameState} />
  </ArtifactFrame>)
}
const slot = (name: string) => document.querySelector(`[data-artifact-slot="${name}"]`) as HTMLElement | null

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_PROTOTYPE", "true"); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_EXPORT", "true")
  state.editable = true; state.origin = null; state.unsynced = false; state.saveData = undefined; state.saveSuccess = false
  state.doc = new Y.Doc()
  upsertDesignNode(state.doc, { id: "desk", type: "frame", name: "Landing · Desktop", x: 64, y: 64, width: 1440, height: 1100, fill: "#ffffff" })
  upsertDesignNode(state.doc, { id: "mob", type: "frame", name: "Landing · Mobile", x: 1600, y: 64, width: 390, height: 844, fill: "#ffffff" })
  upsertDesignNode(state.doc, { id: "cta", type: "rect", name: "Call to action", parentId: "desk", x: 32, y: 300, width: 168, height: 48, fill: "#2563eb" })
})
afterEach(() => { cleanup(); state.doc?.destroy(); vi.unstubAllEnvs() })

describe("Design in the artifact frame", () => {
  it("puts its controls in the frame's slots instead of its own toolbars", () => {
    mount()
    expect(screen.queryByRole("banner")).toBeNull()
    expect(screen.queryByRole("button", { name: "Save version" })).toBeNull()
    expect(within(slot("view")!).getByRole("button", { name: "Artboard: Landing · Desktop" })).toHaveTextContent("2 artboards")
    expect(within(slot("output")!).getByRole("button", { name: "Play prototype" })).toBeInTheDocument()
    expect(within(slot("output")!).getByRole("button", { name: "Export HTML (version 2)" })).toBeInTheDocument()
    expect(within(slot("viewport")!).getByRole("button", { name: "Fit" })).toHaveAttribute("aria-pressed", "true")
    expect(within(slot("nav")!).getByText("1 / 2")).toBeInTheDocument()
    fireEvent.click(within(slot("tools")!).getByRole("button", { name: "Screen" }))
    expect(designNodes(state.doc!).filter(node => node.type === "frame")).toHaveLength(3)
    // Properties, Comments and History are drawer tabs.
    expect(screen.getByRole("button", { name: "Properties" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Comments" })).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "History" })).toBeInTheDocument()
  })

  it("hides what the backend does not serve: Library, Publish, library updates and token usage", () => {
    mount()
    expect(screen.queryByRole("radio", { name: /Library/ })).toBeNull()
    expect(screen.queryByText(/Publish/)).toBeNull()
    expect(screen.queryByText(/available/)).toBeNull()
    expect(screen.queryByText(/Used by/)).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "Properties" }))
    expect(screen.queryByText(/Library tokens/)).toBeNull()
  })

  it("moves through artboards with nav and the picker, and shows Layers in the panel slot", () => {
    mount()
    fireEvent.click(within(slot("nav")!).getByRole("button", { name: "Next artboard" }))
    expect(within(slot("nav")!).getByText("2 / 2")).toBeInTheDocument()
    expect(within(slot("nav")!).getByRole("button", { name: "Next artboard" })).toBeDisabled()
    expect(slot("panel")).toBeNull()
    fireEvent.click(within(slot("view")!).getByRole("button", { name: "Layers" }))
    const layers = screen.getByRole("group", { name: "Layers" })
    fireEvent.click(within(layers).getByRole("button", { name: "Call to action" }))
    fireEvent.click(screen.getByRole("button", { name: "Properties" }))
    expect(screen.getByLabelText("Layer name")).toHaveValue("Call to action")
    expect(screen.getByLabelText("Layer name")).toBeEnabled()
  })

  it("skips a hidden screen in next, previous, the artboard picker and Fit (#5228)", () => {
    upsertDesignNode(state.doc!, { id: "draft", type: "frame", name: "Old draft", x: 800, y: 64, width: 390, height: 844, fill: "#ffffff", hidden: true })
    upsertDesignNode(state.doc!, { id: "far", type: "frame", name: "Far away", x: 90000, y: 90000, width: 390, height: 844, fill: "#ffffff", hidden: true })
    mount()
    expect(within(slot("view")!).getByRole("button", { name: "Artboard: Landing · Desktop" })).toHaveTextContent("2 artboards")
    expect(within(slot("nav")!).getByText("1 / 2")).toBeInTheDocument()
    fireEvent.click(within(slot("nav")!).getByRole("button", { name: "Next artboard" }))
    expect(within(slot("nav")!).getByText("2 / 2")).toBeInTheDocument()
    expect(within(slot("view")!).getByRole("button", { name: "Artboard: Landing · Mobile" })).toBeInTheDocument()
    expect(within(slot("nav")!).getByRole("button", { name: "Next artboard" })).toBeDisabled()
  })
  it("focuses the selected visible Layers screen even when a hidden screen precedes it", () => {
    vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_HIDE_LOCK", "true")
    upsertDesignNode(state.doc!, { id: "draft", type: "frame", name: "Old draft", x: 800, y: 64, width: 390, height: 844, hidden: true })
    const order = state.doc!.getArray<string>("order")
    order.delete(order.length - 1, 1)
    order.insert(0, ["draft"])
    mount()
    fireEvent.click(within(slot("nav")!).getByRole("button", { name: "Next artboard" }))
    fireEvent.click(within(slot("view")!).getByRole("button", { name: "Layers" }))
    const layers = within(screen.getByRole("group", { name: "Layers" }))
    fireEvent.click(layers.getByRole("button", { name: "Landing · Desktop" }))
    expect(within(slot("view")!).getByRole("button", { name: "Artboard: Landing · Desktop" })).toBeInTheDocument()
    fireEvent.click(layers.getByRole("button", { name: "Landing · Mobile" }))
    fireEvent.click(layers.getByRole("button", { name: "Old draft" }))
    expect(within(slot("view")!).getByRole("button", { name: "Artboard: Landing · Mobile" })).toBeInTheDocument()
  })

  it("zooms in steps and returns to Fit", () => {
    mount()
    fireEvent.click(within(slot("viewport")!).getByRole("button", { name: "Zoom in" }))
    expect(within(slot("viewport")!).getByRole("button", { name: "Fit" })).toHaveAttribute("aria-pressed", "false")
    const canvas = screen.getByRole("img", { name: "Design canvas" })
    expect(canvas.getAttribute("viewBox")).toMatch(/^0 0 /)
    fireEvent.click(within(slot("viewport")!).getByRole("button", { name: "Fit" }))
    expect(within(slot("viewport")!).getByRole("button", { name: "Fit" })).toHaveAttribute("aria-pressed", "true")
  })

  it("offers no tools to Read or Comment and reports the read-only state", () => {
    state.editable = false
    const reported = vi.fn()
    mount("desktop", reported)
    expect(slot("tools")).toBeNull()
    expect(designFrameStatus(reported.mock.lastCall![0])?.key).toBe("readonly")
    fireEvent.click(screen.getByRole("button", { name: "Properties" }))
    expect(screen.getByText(/Select a screen or layer/)).toBeInTheDocument()
  })

  it("reports Saving… for unconfirmed work and Saved only with the server's version", () => {
    const reported = vi.fn()
    state.unsynced = true
    const view = mount("desktop", reported)
    expect(designFrameStatus(reported.mock.lastCall![0])?.key).toBe("saving")
    view.unmount()
    state.unsynced = false; state.saveSuccess = true; state.saveData = { version: 3 }
    mount("desktop", reported)
    expect(designFrameStatus(reported.mock.lastCall![0])).toMatchObject({ key: "saved", label: "Saved · v3" })
    expect(reported.mock.lastCall![0].peers).toEqual([{ clientId: 2, name: "Marie", color: "#2563eb" }])
  })

  it("keeps a phone to review: fit to width, no editing and says why, comments allowed", () => {
    mount("phone")
    const tools = slot("tools")!
    expect(within(tools).queryByRole("button", { name: "Screen" })).toBeNull()
    expect(within(tools).getByText("Review only. Edit on a larger screen.")).toBeInTheDocument()
    expect(slot("viewport")!.className).toContain("hidden")
    expect(screen.getByRole("region", { name: "Design editor" })).toHaveAttribute("data-design-review", "phone")
    // Selecting works; changing does not.
    const before = designNodes(state.doc!)
    const canvas = screen.getByRole("img", { name: "Design canvas" })
    fireEvent.pointerDown(canvas.querySelector('[data-design-node="cta"]')!, { clientX: 10, clientY: 20, pointerId: 1 })
    fireEvent.pointerMove(canvas, { clientX: 200, clientY: 200, pointerId: 1 })
    fireEvent.pointerUp(canvas, { pointerId: 1 })
    expect(designNodes(state.doc!)).toEqual(before)
    fireEvent.click(within(tools).getByRole("button", { name: "Comment" }))
    expect(screen.getByLabelText("Comment for agent")).toBeInTheDocument()
    expect(screen.getByLabelText("Layer name")).toBeDisabled()
  })

  it("registers sending a comment to the agent as Propose, only where that path exists", () => {
    const first = mount()
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    expect(screen.queryByRole("radiogroup", { name: "Agent mode" })).toBeNull()
    first.unmount()
    state.origin = { chat_id: "chat-1", agent_id: "agent-1" }
    mount()
    fireEvent.click(screen.getByRole("button", { name: "Agent" }))
    const modes = screen.getByRole("radiogroup", { name: "Agent mode" })
    expect(within(modes).getAllByRole("radio").map(radio => radio.textContent)).toEqual(["Ask", "Propose"])
    fireEvent.click(within(modes).getByRole("radio", { name: "Propose" }))
    expect(screen.getByLabelText("Request for the agent")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Send to agent" })).toBeDisabled()
  })
})

describe("DesignPresence", () => {
  it("shows the real people on the canvas and nothing when alone", () => {
    const { container, rerender } = render(<DesignPresence peers={[]} />)
    expect(container).toBeEmptyDOMElement()
    rerender(<DesignPresence peers={[{ clientId: 1, name: "Jana", color: "#059669" }]} />)
    expect(screen.getByTitle("Jana")).toHaveTextContent("JA")
  })
})
