vi.mock("@/lib/local-draft-seal", () => ({
 createLocalDraftCipher: async () => ({ seal: async (text: string) => text, open: async (text: string) => text, close() {} }),
 forgetLocalDraftPage() {}, isSealedLocalDraft: () => true, LocalDraftReadRevoked: class extends Error {},
}))
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react"
import * as Y from "yjs"
import { DesignNodeShape } from "../design-node-shape"
import { DesignEditor } from "../design-editor"
import { designNodes, designShapeStyle, upsertDesignNode, type DesignNode } from "../design-document"

const state = vi.hoisted(() => ({ doc: null as Y.Doc | null, editable: true }))
vi.mock("@/hooks/use-auth", () => ({ useSessionSafe: () => null }))
vi.mock("@/hooks/use-artifact-agent-dispatch", () => ({ useArtifactAgentDispatch: () => ({ ready: false, dispatch: vi.fn() }) }))
vi.mock("../use-design", () => ({ useDesign: () => {
 const mutation = { mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false, error: null }
 return {
  query: { data: { page_id: "page", can_edit: state.editable, can_comment: true, latest_version: 1, comments: [] }, isError: false },
  comments: { data: { comments: [] }, isError: false }, versions: { data: { versions: [{ version: 1 }] }, isError: false },
  doc: state.doc, nodes: designNodes(state.doc!), peers: [], connection: "connected", editable: state.editable, dirty: false, unsynced: false, draftError: false,
  save: mutation, restore: mutation, share: mutation, comment: mutation, connectAgent: mutation, setCursor: vi.fn(), conflicts: [], dismissConflict: vi.fn(),
 }
} }))

const rect = (extra: Partial<DesignNode> = {}): DesignNode => ({ id: "r", type: "rect", x: 0, y: 0, width: 200, height: 100, fill: "#2563eb", ...extra })
const draw = (node: DesignNode) => render(<svg><DesignNodeShape node={node} width={node.width} height={node.height} /></svg>).container.querySelector("svg")!

beforeEach(() => { vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN", "true"); vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_SHAPE_STYLE", "true"); state.editable = true; state.doc = new Y.Doc() })
afterEach(() => { cleanup(); state.doc?.destroy(); vi.unstubAllEnvs() })

describe("Design shape style (V02, #5219)", () => {
 it("keeps the old look without the fields: rectangles 6 px round, images square, no stroke", () => {
  expect(designShapeStyle(rect())).toEqual({ radius: 6, strokeWidth: 0 })
  expect(designShapeStyle(rect({ type: "image" }))).toEqual({ radius: 0, strokeWidth: 0 })
  const svg = draw(rect())
  expect(svg.querySelector("rect")).toHaveAttribute("rx", "6")
  expect(svg.querySelector("[data-shape-stroke]")).toBeNull()
 })
 it("draws the radius and a stroke inside the box; a colour without a width draws 1 px; unknown values fall back", () => {
  const svg = draw(rect({ radius: 16, stroke: "#0f172a", strokeWidth: 4 }))
  expect(svg.querySelector("rect")).toHaveAttribute("rx", "16")
  const stroke = svg.querySelector("[data-shape-stroke]")!
  expect([stroke.getAttribute("x"), stroke.getAttribute("width"), stroke.getAttribute("stroke"), stroke.getAttribute("stroke-width"), stroke.getAttribute("rx")]).toEqual(["2", "196", "#0f172a", "4", "14"])
  expect(designShapeStyle(rect({ stroke: "#fff" }))).toEqual({ radius: 6, stroke: "#fff", strokeWidth: 1 })
  expect(designShapeStyle(rect({ stroke: "red", radius: -3 }))).toEqual({ radius: 6, strokeWidth: 0 })
  // A rounded image is clipped to its corners.
  const image = draw(rect({ type: "image", src: "https://example.test/a.png", radius: 50 }))
  expect(image.querySelector("clipPath rect")).toHaveAttribute("rx", "50")
  expect(image.querySelector("image")?.getAttribute("clip-path")).toMatch(/^url\(#design-clip-/)
 })
 it("edits radius and stroke in Properties for rectangles and images, stored on the layer", () => {
  upsertDesignNode(state.doc!, { id: "s", type: "frame", name: "Screen", x: 0, y: 0, width: 360, height: 640, fill: "#ffffff" })
  upsertDesignNode(state.doc!, { id: "r", type: "rect", name: "Card", parentId: "s", x: 10, y: 10, width: 200, height: 100, fill: "#ffffff" })
  upsertDesignNode(state.doc!, { id: "t", type: "text", name: "Title", parentId: "s", x: 10, y: 200, width: 200, height: 40, fill: "#000000", text: "Hi" })
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  const layers = within(screen.getByLabelText("Layers"))
  fireEvent.click(layers.getByRole("button", { name: "Card" }))
  const style = within(screen.getByRole("group", { name: "Shape style" }))
  expect(style.getByLabelText("Corner radius")).toHaveValue(6)
  fireEvent.change(style.getByLabelText("Corner radius"), { target: { value: "20" } })
  fireEvent.change(style.getByLabelText("Stroke width"), { target: { value: "3" } })
  fireEvent.change(style.getByLabelText("Stroke color"), { target: { value: "#dc2626" } })
  expect(designNodes(state.doc!).find(node => node.id === "r")).toMatchObject({ radius: 20, strokeWidth: 3, stroke: "#dc2626" })
  // Out of range is not written.
  fireEvent.change(style.getByLabelText("Corner radius"), { target: { value: "-5" } })
  expect(designNodes(state.doc!).find(node => node.id === "r")).toMatchObject({ radius: 20 })
  // Text layers have no shape style.
  fireEvent.click(layers.getByRole("button", { name: "Title" }))
  expect(screen.queryByRole("group", { name: "Shape style" })).toBeNull()
 })
 it("keeps style read only when the parent screen is locked", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_HIDE_LOCK", "true")
  upsertDesignNode(state.doc!, { id: "s", type: "frame", name: "Screen", x: 0, y: 0, width: 360, height: 640, locked: true })
  upsertDesignNode(state.doc!, rect({ name: "Card", parentId: "s", radius: 12, stroke: "#123456", strokeWidth: 2 }))
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(within(screen.getByLabelText("Layers")).getByRole("button", { name: "Card" }))
  const style = within(screen.getByRole("group", { name: "Shape style" }))
  expect(style.getByLabelText("Corner radius")).toBeDisabled()
  expect(style.getByLabelText("Stroke width")).toBeDisabled()
  expect(style.getByLabelText("Stroke color")).toBeDisabled()
  fireEvent.change(style.getByLabelText("Corner radius"), { target: { value: "22" } })
  expect(designNodes(state.doc!).find(node => node.id === "r")).toMatchObject({ radius: 12, stroke: "#123456", strokeWidth: 2 })
 })
 it("offers no shape style controls without the flag", () => {
  vi.stubEnv("NEXT_PUBLIC_ARTIFACTS_DESIGN_SHAPE_STYLE", "")
  upsertDesignNode(state.doc!, { id: "r", type: "rect", name: "Card", x: 10, y: 10, width: 200, height: 100, fill: "#ffffff", radius: 12 })
  render(<DesignEditor workspaceId="workspace" slug="shop" />)
  fireEvent.click(within(screen.getByLabelText("Layers")).getByRole("button", { name: "Card" }))
  expect(screen.queryByRole("group", { name: "Shape style" })).toBeNull()
  expect(document.querySelector('[data-design-node="r"] rect')).toHaveAttribute("rx", "12")
 })
})
