vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import * as Y from "yjs"
import { DesignEditor } from "../design-editor"
import { designNodes, removeDesignNode, setDesignNodeLink, upsertDesignNode } from "../design-document"

const state = vi.hoisted(() => ({ editable: true, doc: null as Y.Doc | null }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => null }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("../use-design", () => ({ useDesign: () => {
 const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, error: null }
 return {
  query: { data: { page_id: "page", can_edit: state.editable, can_comment: state.editable, latest_version: 1, comments: [] }, isError: false },
  comments: { data: { comments: [] }, isError: false }, versions: { data: { versions: [{ version: 1 }], shared_version: 1 }, isError: false },
  doc: state.doc, nodes: designNodes(state.doc!), peers: [], connection: "connected", editable: state.editable, dirty: false, draftError: false,
  save: mutation, restore: mutation, share: mutation, comment: mutation, connectAgent: mutation, setCursor: vi.fn(),
 }
} }))

function seed(doc: Y.Doc) {
 for (const [index, id] of ["a", "b", "c"].entries()) upsertDesignNode(doc, { id, type: "frame", name: `Screen ${id.toUpperCase()}`, x: 64 + index * 424, y: 64, width: 360, height: 640, fill: "#ffffff" })
 upsertDesignNode(doc, { id: "to-b", type: "rect", parentId: "a", name: "Go to B", x: 24, y: 24, width: 120, height: 48, fill: "#2563eb" })
 upsertDesignNode(doc, { id: "to-c", type: "rect", parentId: "b", name: "Go to C", x: 24, y: 24, width: 120, height: 48, fill: "#2563eb" })
 setDesignNodeLink(doc, "to-b", "b")
 setDesignNodeLink(doc, "to-c", "c")
}

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_PROTOTYPE", "true"); state.editable = true; state.doc = new Y.Doc(); seed(state.doc) })
afterEach(() => { cleanup(); state.doc?.destroy(); vi.unstubAllEnvs() })

const select = (name: string) => fireEvent.click(within(screen.getByRole("complementary", { name: "Layers" })).getByRole("button", { name }))

describe("Design prototype flag", () => {
 it("shows no Play button or link field while the prototype flag is off", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_PROTOTYPE", "false")
  render(<DesignEditor workspaceId="ws" slug="flow" />)
  select("Go to B")
  expect(screen.queryByRole("button", { name: "Play" })).not.toBeInTheDocument()
  expect(screen.queryByLabelText("Link to screen")).not.toBeInTheDocument()
 })
})

describe("Design prototype links in the editor", () => {
 it("lets an editor set and remove a link target", () => {
  render(<DesignEditor workspaceId="ws" slug="flow" />)
  select("Go to B")
  const field = screen.getByLabelText("Link to screen")
  expect(field).toHaveValue("b")
  fireEvent.change(field, { target: { value: "c" } })
  expect(designNodes(state.doc!).find(node => node.id === "to-b")?.linkTo).toBe("c")
  fireEvent.change(screen.getByLabelText("Link to screen"), { target: { value: "" } })
  expect(state.doc!.getMap<Y.Map<unknown>>("nodes").get("to-b")!.has("linkTo")).toBe(false)
 })
 it("does not let Read or Comment change a link", () => {
  state.editable = false
  render(<DesignEditor workspaceId="ws" slug="flow" />)
  select("Go to B")
  let updates = 0
  state.doc!.on("update", () => { updates++ })
  const field = screen.getByLabelText("Link to screen")
  expect(field).toBeDisabled()
  fireEvent.change(field, { target: { value: "c" } })
  expect(updates).toBe(0)
  expect(designNodes(state.doc!).find(node => node.id === "to-b")?.linkTo).toBe("b")
 })
 it("marks a link to a deleted screen in Properties", () => {
  removeDesignNode(state.doc!, "c")
  render(<DesignEditor workspaceId="ws" slug="flow" />)
  select("Go to C")
  expect(screen.getByText(/linked screen was deleted/i)).toBeInTheDocument()
 })
})

describe("Design prototype playback", () => {
 it.each([true, false])("plays linked screens without writing to the Y.Doc (editable %s)", editable => {
  state.editable = editable
  render(<DesignEditor workspaceId="ws" slug="flow" />)
  let updates = 0
  state.doc!.on("update", () => { updates++ })
  fireEvent.click(screen.getByRole("button", { name: "Play" }))
  const player = screen.getByRole("dialog", { name: "Prototype playback" })
  expect(within(player).getByRole("heading", { name: "Screen A" })).toBeInTheDocument()
  fireEvent.click(within(player).getByRole("button", { name: "Go to B: open Screen B" }))
  expect(within(player).getByRole("heading", { name: "Screen B" })).toBeInTheDocument()
  fireEvent.keyDown(within(player).getByRole("button", { name: "Go to C: open Screen C" }), { key: "Enter" })
  expect(within(player).getByRole("heading", { name: "Screen C" })).toBeInTheDocument()
  fireEvent.click(within(player).getByRole("button", { name: "Back" }))
  expect(within(player).getByRole("heading", { name: "Screen B" })).toBeInTheDocument()
  fireEvent.keyDown(player, { key: "Escape" })
  expect(screen.queryByRole("dialog", { name: "Prototype playback" })).not.toBeInTheDocument()
  expect(updates).toBe(0)
 })
 it("starts from the selected screen", () => {
  render(<DesignEditor workspaceId="ws" slug="flow" />)
  select("Screen B")
  fireEvent.click(screen.getByRole("button", { name: "Play" }))
  expect(within(screen.getByRole("dialog", { name: "Prototype playback" })).getByRole("heading", { name: "Screen B" })).toBeInTheDocument()
 })
 it("keeps a hotspot to a deleted screen inert with a hint", () => {
  removeDesignNode(state.doc!, "b")
  render(<DesignEditor workspaceId="ws" slug="flow" />)
  fireEvent.click(screen.getByRole("button", { name: "Play" }))
  const player = screen.getByRole("dialog", { name: "Prototype playback" })
  const hotspot = within(player).getByRole("button", { name: "Go to B: linked screen was deleted" })
  expect(hotspot).toHaveAttribute("aria-disabled", "true")
  fireEvent.click(hotspot)
  expect(within(player).getByRole("heading", { name: "Screen A" })).toBeInTheDocument()
  expect(within(player).getByRole("status")).toHaveTextContent(/linked screen was deleted/i)
 })
})
